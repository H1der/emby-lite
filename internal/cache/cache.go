package cache

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/config"

	"github.com/redis/go-redis/v9"
)

// Store defines the unified cache/rate-limit interface.
// When Redis is not configured, an in-process MemoryStore is used transparently.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	DeletePrefix(ctx context.Context, prefix string) error
	IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error)
	BackendName() string
	Ping(ctx context.Context) error
	Close() error
}

// New initializes RedisStore if cfg.Redis.URL is set and reachable, else falls back to MemoryStore.
func New(ctx context.Context, cfg *config.Config) Store {
	if strings.TrimSpace(cfg.Redis.URL) != "" {
		rs, err := NewRedisStore(ctx, cfg.Redis)
		if err == nil {
			slog.Info("cache backend initialized", "backend", "redis")
			return rs
		}
		slog.Warn("redis unavailable, falling back to in-memory cache", "err", err)
	}
	slog.Info("cache backend initialized", "backend", "memory")
	return NewMemoryStore(4096)
}

type memEntry struct {
	value     []byte
	counter   int64
	isCounter bool
	expiresAt time.Time
}

type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]memEntry
	maxSize int
	stopCh  chan struct{}
}

func NewMemoryStore(maxSize int) *MemoryStore {
	if maxSize <= 0 {
		maxSize = 4096
	}
	m := &MemoryStore{
		entries: make(map[string]memEntry, 256),
		maxSize: maxSize,
		stopCh:  make(chan struct{}),
	}
	go m.evictLoop()
	return m
}

func (m *MemoryStore) evictLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			for k, v := range m.entries {
				if !v.expiresAt.IsZero() && now.After(v.expiresAt) {
					delete(m.entries, k)
				}
			}
			m.mu.Unlock()
		case <-m.stopCh:
			return
		}
	}
}

func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()
	if !ok || e.isCounter {
		return nil, false
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		m.mu.Lock()
		delete(m.entries, key)
		m.mu.Unlock()
		return nil, false
	}
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

func (m *MemoryStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) >= m.maxSize {
		// Evict expired first, otherwise drop one arbitrary entry.
		now := time.Now()
		evicted := 0
		for k, v := range m.entries {
			if !v.expiresAt.IsZero() && now.After(v.expiresAt) {
				delete(m.entries, k)
				evicted++
			}
		}
		if evicted == 0 {
			for k := range m.entries {
				delete(m.entries, k)
				break
			}
		}
	}
	val := make([]byte, len(value))
	copy(val, value)
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.entries[key] = memEntry{value: val, expiresAt: exp}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.entries, key)
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	for k := range m.entries {
		if strings.HasPrefix(k, prefix) {
			delete(m.entries, k)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) IncrWithTTL(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	e, ok := m.entries[key]
	if !ok || (!e.expiresAt.IsZero() && now.After(e.expiresAt)) {
		e = memEntry{
			counter:   1,
			isCounter: true,
			expiresAt: now.Add(ttl),
		}
		m.entries[key] = e
		return 1, nil
	}
	e.counter++
	m.entries[key] = e
	return e.counter, nil
}

func (m *MemoryStore) BackendName() string          { return "memory" }
func (m *MemoryStore) Ping(_ context.Context) error { return nil }
func (m *MemoryStore) Close() error {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	return nil
}

// RedisStore implements Store backed by Redis.
type RedisStore struct {
	client *redis.Client
	prefix string
}

func NewRedisStore(ctx context.Context, cfg config.RedisConfig) (*RedisStore, error) {
	var opts *redis.Options
	var err error
	if strings.HasPrefix(cfg.URL, "redis://") || strings.HasPrefix(cfg.URL, "rediss://") {
		opts, err = redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("parse redis url: %w", err)
		}
	} else {
		opts = &redis.Options{
			Addr:     cfg.URL,
			Password: cfg.Password,
			DB:       cfg.DB,
		}
	}
	if cfg.Password != "" && opts.Password == "" {
		opts.Password = cfg.Password
	}
	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RedisStore{client: client, prefix: cfg.Prefix}, nil
}

func (r *RedisStore) k(key string) string { return r.prefix + key }

func (r *RedisStore) Get(ctx context.Context, key string) ([]byte, bool) {
	b, err := r.client.Get(ctx, r.k(key)).Bytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

func (r *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return r.client.Set(ctx, r.k(key), value, ttl).Err()
}

func (r *RedisStore) Delete(ctx context.Context, key string) error {
	return r.client.Del(ctx, r.k(key)).Err()
}

func (r *RedisStore) DeletePrefix(ctx context.Context, prefix string) error {
	iter := r.client.Scan(ctx, 0, r.k(prefix)+"*", 200).Iterator()
	var batch []string
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= 100 {
			_ = r.client.Del(ctx, batch...).Err()
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		_ = r.client.Del(ctx, batch...).Err()
	}
	return iter.Err()
}

func (r *RedisStore) IncrWithTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	full := r.k(key)
	val, err := r.client.Incr(ctx, full).Result()
	if err != nil {
		return 0, err
	}
	if val == 1 && ttl > 0 {
		_ = r.client.Expire(ctx, full, ttl).Err()
	}
	return val, nil
}

func (r *RedisStore) BackendName() string { return "redis" }
func (r *RedisStore) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}
func (r *RedisStore) Close() error { return r.client.Close() }
