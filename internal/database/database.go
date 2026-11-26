package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	Gorm     *gorm.DB
	ReadGorm *gorm.DB
	SQL      *sql.DB
	ReadSQL  *sql.DB
	Dialect  string // "sqlite" | "postgres"
	Path     string // sqlite database path (empty for in-memory)
	HasTrgm  bool
	HasFTS5  bool

	settingsMu    sync.RWMutex
	settingsCache map[string]string
}

// Read returns the read-optimized GORM handle (concurrent reader pool in SQLite WAL mode).
func (d *DB) Read() *gorm.DB {
	if d.ReadGorm != nil {
		return d.ReadGorm
	}
	return d.Gorm
}

func Open(cfg *config.Config) (*DB, error) {
	gormCfg := &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent),
		PrepareStmt: true,
	}

	var gdb *gorm.DB
	var readGdb *gorm.DB
	var err error
	dialect := strings.ToLower(cfg.Database.Type)
	if dialect == "" {
		dialect = "sqlite"
	}

	switch dialect {
	case "sqlite":
		dir := filepath.Dir(cfg.Database.SQLitePath)
		if dir != "" && dir != "." && cfg.Database.SQLitePath != ":memory:" && !strings.HasPrefix(cfg.Database.SQLitePath, "file:") {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				return nil, fmt.Errorf("create sqlite directory: %w", mkErr)
			}
		}
		rawPath := cfg.Database.SQLitePath
		isMem := rawPath == ":memory:" || strings.HasPrefix(rawPath, "file::memory:")
		dsn := rawPath
		if !isMem && !strings.Contains(dsn, "?") {
			dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)&_pragma=mmap_size(134217728)"
		}
		gdb, err = gorm.Open(sqlite.Open(dsn), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}

		// Open a separate read-only connection pool for file-backed SQLite in WAL mode
		// so concurrent UI/Emby reads never block behind background scan writes.
		if !isMem && !strings.Contains(rawPath, "?") {
			readDSN := rawPath + "?_pragma=busy_timeout(10000)&_pragma=query_only(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)&_pragma=mmap_size(134217728)"
			if rg, rErr := gorm.Open(sqlite.Open(readDSN), gormCfg); rErr == nil {
				readGdb = rg
			} else {
				slog.Warn("failed to open sqlite read pool, falling back to shared handle", "err", rErr)
			}
		}
	case "postgres", "postgresql":
		dialect = "postgres"
		if cfg.Database.DSN == "" {
			return nil, fmt.Errorf("postgres database selected but DSN is empty")
		}
		gdb, err = gorm.Open(postgres.Open(cfg.Database.DSN), gormCfg)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dialect)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("unwrap sql.DB: %w", err)
	}

	var readSQL *sql.DB
	if dialect == "sqlite" {
		// Single writer connection prevents SQLITE_BUSY under concurrent scans/requests.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		if readGdb != nil {
			if rsql, rErr := readGdb.DB(); rErr == nil {
				readConns := runtime.NumCPU()
				if readConns < 4 {
					readConns = 4
				}
				if readConns > 16 {
					readConns = 16
				}
				rsql.SetMaxOpenConns(readConns)
				rsql.SetMaxIdleConns(readConns)
				rsql.SetConnMaxLifetime(30 * time.Minute)
				readSQL = rsql
			}
		}
	} else {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	if readGdb == nil {
		readGdb = gdb
		readSQL = sqlDB
	}

	db := &DB{
		Gorm:          gdb,
		ReadGorm:      readGdb,
		SQL:           sqlDB,
		ReadSQL:       readSQL,
		Dialect:       dialect,
		Path:          cfg.Database.SQLitePath,
		settingsCache: make(map[string]string, 16),
	}

	if err := db.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.SeedDefaults(cfg); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func (d *DB) Migrate() error {
	if err := d.Gorm.AutoMigrate(model.AllModels()...); err != nil {
		return fmt.Errorf("auto migrate: %w", err)
	}

	// Composite indexes for hot Emby browsing, sorting, resume, and batch DTO preloading.
	compositeIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_items_browse_added ON items (library_id, soft_deleted_at, type, added_at DESC, id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_browse_sort ON items (library_id, soft_deleted_at, type, sort_name ASC, id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_browse_rating ON items (library_id, soft_deleted_at, type, community_rating DESC, id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_browse_year ON items (library_id, soft_deleted_at, type, year DESC, id ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_parent_active ON items (parent_id, soft_deleted_at, sort_name ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_series_season_ep ON items (series_id, type, soft_deleted_at, season_num ASC, episode_num ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_user_data_resume ON user_data (user_id, played, hide_from_resume, position_ticks, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_user_data_fav ON user_data (user_id, is_favorite, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_item_images_item_kind_idx ON item_images (item_id, kind, index_num)`,
		`CREATE INDEX IF NOT EXISTS idx_item_people_item_sort ON item_people (item_id, sort_idx ASC)`,
	}
	for _, stmt := range compositeIndexes {
		if err := d.Gorm.Exec(stmt).Error; err != nil {
			slog.Warn("create composite index warning", "sql", stmt, "err", err)
		}
	}

	// Dialect-specific search acceleration (best-effort, degrades cleanly).
	if d.Dialect == "postgres" {
		if err := d.Gorm.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err == nil {
			d.HasTrgm = true
			_ = d.Gorm.Exec("CREATE INDEX IF NOT EXISTS idx_items_name_trgm ON items USING gin (name gin_trgm_ops)").Error
			_ = d.Gorm.Exec("CREATE INDEX IF NOT EXISTS idx_items_orig_name_trgm ON items USING gin (original_name gin_trgm_ops)").Error
		} else {
			slog.Warn("pg_trgm extension unavailable, falling back to ILIKE search", "err", err)
		}
	} else if d.Dialect == "sqlite" {
		if err := d.Gorm.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(item_id UNINDEXED, name, original_name, pinyin_initials)`).Error; err == nil {
			d.HasFTS5 = true
		} else {
			slog.Warn("sqlite FTS5 unavailable, falling back to LIKE search", "err", err)
		}
	}

	return nil
}

func (d *DB) SeedDefaults(cfg *config.Config) error {
	if err := d.ensureSetting("server_id", randomHex(16)); err != nil {
		return err
	}
	if err := d.ensureSetting("server_name", cfg.Server.ServerName); err != nil {
		return err
	}
	secret := cfg.Auth.ImageSecretOverride
	if secret == "" {
		secret = randomHex(32)
	}
	if err := d.ensureSetting("image_secret", secret); err != nil {
		return err
	}
	if err := d.ensureSetting("library_revision", "1"); err != nil {
		return err
	}

	// Seed default permission group templates if none exist.
	var count int64
	if err := d.Gorm.Model(&model.PermissionGroup{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		defaults := []model.PermissionGroup{
			{
				ID:                   "group-standard",
				Name:                 "标准用户",
				Description:          "允许直连播放所有非隐藏媒体库，默认禁止下载",
				AllowPlayback:        true,
				AllowDownload:        false,
				MaxDevices:           5,
				MaxConcurrentStreams: 3,
				CanViewHidden:        false,
			},
			{
				ID:                   "group-family",
				Name:                 "家庭成员",
				Description:          "允许播放与离线下载，支持多设备同时观看",
				AllowPlayback:        true,
				AllowDownload:        true,
				MaxDevices:           10,
				MaxConcurrentStreams: 5,
				CanViewHidden:        false,
			},
		}
		for _, g := range defaults {
			if err := d.Gorm.Create(&g).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) ensureSetting(key, value string) error {
	var existing model.Setting
	err := d.Gorm.Where("key = ?", key).First(&existing).Error
	if err == nil {
		d.cacheSetting(key, existing.Value)
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	if err := d.Gorm.Create(&model.Setting{
		Key:       key,
		Value:     value,
		UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	d.cacheSetting(key, value)
	return nil
}

func (d *DB) cacheSetting(key, value string) {
	d.settingsMu.Lock()
	if d.settingsCache == nil {
		d.settingsCache = make(map[string]string, 16)
	}
	d.settingsCache[key] = value
	d.settingsMu.Unlock()
}

func (d *DB) InvalidateSettingsCache() {
	d.settingsMu.Lock()
	d.settingsCache = make(map[string]string, 16)
	d.settingsMu.Unlock()
}

func (d *DB) GetSetting(key, fallback string) string {
	d.settingsMu.RLock()
	if v, ok := d.settingsCache[key]; ok {
		d.settingsMu.RUnlock()
		return v
	}
	d.settingsMu.RUnlock()

	var s model.Setting
	if err := d.Read().Where("key = ?", key).First(&s).Error; err != nil {
		return fallback
	}
	d.cacheSetting(key, s.Value)
	return s.Value
}

func (d *DB) SetSetting(key, value string, encrypted bool) error {
	if err := d.Gorm.Save(&model.Setting{
		Key:       key,
		Value:     value,
		Encrypted: encrypted,
		UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	d.cacheSetting(key, value)
	return nil
}

func (d *DB) GetLibraryRevision() string {
	return d.GetSetting("library_revision", "1")
}

func (d *DB) BumpLibraryRevision() {
	now := fmt.Sprintf("%d", time.Now().UnixNano())
	_ = d.SetSetting("library_revision", now, false)
}

func (d *DB) Ping(ctx context.Context) error {
	return d.SQL.PingContext(ctx)
}

func (d *DB) Close() error {
	var firstErr error
	if d.ReadSQL != nil && d.ReadSQL != d.SQL {
		if err := d.ReadSQL.Close(); err != nil {
			firstErr = err
		}
	}
	if d.SQL != nil {
		if err := d.SQL.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
