package task

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/google/uuid"
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Event represents a task lifecycle update broadcast over SSE.
type Event struct {
	EventType string               `json:"event"` // created | progress | completed | failed | cancelled
	Task      model.BackgroundTask `json:"task"`
}

// ProgressReporter lets a task handler report live progress to DB and SSE subscribers.
type ProgressReporter func(progress, total int, message string)

// HandlerFunc executes a background task of a registered type.
type HandlerFunc func(ctx context.Context, payload []byte, report ProgressReporter) error

type Queue struct {
	db       *database.DB
	handlers map[string]HandlerFunc

	mu       sync.Mutex
	active   map[string]context.CancelFunc
	subs     map[chan Event]struct{}
	wakeCh   chan struct{}
	stopCh   chan struct{}
	wg       sync.WaitGroup
	started  bool
}

func NewQueue(db *database.DB) *Queue {
	return &Queue{
		db:       db,
		handlers: make(map[string]HandlerFunc),
		active:   make(map[string]context.CancelFunc),
		subs:     make(map[chan Event]struct{}),
		wakeCh:   make(chan struct{}, 32),
		stopCh:   make(chan struct{}),
	}
}

func (q *Queue) Register(taskType string, fn HandlerFunc) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[taskType] = fn
}

// Start recovers interrupted tasks from previous runs and launches worker goroutines.
func (q *Queue) Start(workers int) {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()

	if workers <= 0 {
		workers = 2
	}

	// Recover tasks left in "running" state across unclean restarts.
	_ = q.db.Gorm.Model(&model.BackgroundTask{}).
		Where("status = ?", StatusRunning).
		Updates(map[string]any{
			"status":  StatusPending,
			"message": "服务重启后重新排队",
		}).Error

	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.workerLoop()
	}
	q.signalWake()
}

func (q *Queue) Stop() {
	q.mu.Lock()
	if !q.started {
		q.mu.Unlock()
		return
	}
	q.started = false
	for _, cancel := range q.active {
		cancel()
	}
	close(q.stopCh)
	q.mu.Unlock()
	q.wg.Wait()
}

// Enqueue persists a task to the database, deduplicating identical pending/running jobs.
func (q *Queue) Enqueue(taskType string, priority int, payload any, message string) (*model.BackgroundTask, bool, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	payloadStr := string(rawPayload)

	// Deduplicate active task with same type + payload.
	var existing model.BackgroundTask
	if err := q.db.Gorm.
		Where("type = ? AND payload_json = ? AND status IN ?", taskType, payloadStr, []string{StatusPending, StatusRunning}).
		First(&existing).Error; err == nil {
		return &existing, false, nil
	}

	now := time.Now().UTC()
	t := model.BackgroundTask{
		ID:          strings.ReplaceAll(uuid.NewString(), "-", ""),
		Type:        taskType,
		Status:      StatusPending,
		Priority:    priority,
		PayloadJSON: payloadStr,
		Message:     message,
		CreatedAt:   now,
	}
	if err := q.db.Gorm.Create(&t).Error; err != nil {
		return nil, false, err
	}
	q.broadcast(Event{EventType: "created", Task: t})
	q.signalWake()
	return &t, true, nil
}

// Cancel cancels a pending or running task.
func (q *Queue) Cancel(taskID string) error {
	q.mu.Lock()
	cancel, isActive := q.active[taskID]
	q.mu.Unlock()
	if isActive {
		cancel()
		return nil
	}
	now := time.Now().UTC()
	res := q.db.Gorm.Model(&model.BackgroundTask{}).
		Where("id = ? AND status = ?", taskID, StatusPending).
		Updates(map[string]any{"status": StatusCancelled, "finished_at": now, "message": "已取消"})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		var t model.BackgroundTask
		if q.db.Gorm.Where("id = ?", taskID).First(&t).Error == nil {
			q.broadcast(Event{EventType: "cancelled", Task: t})
		}
	}
	return nil
}

// List returns the most recent background tasks.
func (q *Queue) List(limit int) ([]model.BackgroundTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var tasks []model.BackgroundTask
	err := q.db.Gorm.Order("created_at DESC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (q *Queue) HasActive(taskType string) bool {
	var count int64
	q.db.Gorm.Model(&model.BackgroundTask{}).
		Where("type = ? AND status IN ?", taskType, []string{StatusPending, StatusRunning}).
		Count(&count)
	return count > 0
}

// Subscribe registers an SSE subscriber channel.
func (q *Queue) Subscribe() chan Event {
	ch := make(chan Event, 32)
	q.mu.Lock()
	q.subs[ch] = struct{}{}
	q.mu.Unlock()
	return ch
}

func (q *Queue) Unsubscribe(ch chan Event) {
	q.mu.Lock()
	if _, ok := q.subs[ch]; ok {
		delete(q.subs, ch)
		close(ch)
	}
	q.mu.Unlock()
}

func (q *Queue) broadcast(ev Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for ch := range q.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (q *Queue) signalWake() {
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

func (q *Queue) workerLoop() {
	defer q.wg.Done()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		if q.processNext() {
			continue
		}
		select {
		case <-q.stopCh:
			return
		case <-q.wakeCh:
		case <-ticker.C:
		}
	}
}

func (q *Queue) processNext() bool {
	// Atomically claim one pending task.
	q.mu.Lock()
	var t model.BackgroundTask
	err := q.db.Gorm.
		Where("status = ?", StatusPending).
		Order("priority DESC, created_at ASC").
		First(&t).Error
	if err != nil {
		q.mu.Unlock()
		return false
	}
	now := time.Now().UTC()
	res := q.db.Gorm.Model(&model.BackgroundTask{}).
		Where("id = ? AND status = ?", t.ID, StatusPending).
		Updates(map[string]any{"status": StatusRunning, "started_at": now})
	if res.Error != nil || res.RowsAffected == 0 {
		q.mu.Unlock()
		return false
	}
	t.Status = StatusRunning
	t.StartedAt = &now

	handler := q.handlers[t.Type]
	ctx, cancel := context.WithCancel(context.Background())
	q.active[t.ID] = cancel
	q.mu.Unlock()

	defer func() {
		cancel()
		q.mu.Lock()
		delete(q.active, t.ID)
		q.mu.Unlock()
	}()

	q.broadcast(Event{EventType: "progress", Task: t})

	if handler == nil {
		q.finishTask(&t, StatusFailed, errors.New("no handler registered for task type: "+t.Type))
		return true
	}

	report := func(progress, total int, message string) {
		t.Progress = progress
		t.Total = total
		if message != "" {
			t.Message = message
		}
		_ = q.db.Gorm.Model(&model.BackgroundTask{}).Where("id = ?", t.ID).Updates(map[string]any{
			"progress": progress,
			"total":    total,
			"message":  t.Message,
		}).Error
		q.broadcast(Event{EventType: "progress", Task: t})
	}

	runErr := handler(ctx, []byte(t.PayloadJSON), report)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
			q.finishTask(&t, StatusCancelled, nil)
		} else {
			slog.Warn("background task failed", "task_id", t.ID, "type", t.Type, "err", runErr)
			q.finishTask(&t, StatusFailed, runErr)
		}
		return true
	}
	q.finishTask(&t, StatusCompleted, nil)
	return true
}

func (q *Queue) finishTask(t *model.BackgroundTask, status string, runErr error) {
	now := time.Now().UTC()
	t.Status = status
	t.FinishedAt = &now
	if runErr != nil {
		t.Error = runErr.Error()
	} else if status == StatusCompleted && t.Total > 0 {
		t.Progress = t.Total
	}
	_ = q.db.Gorm.Model(&model.BackgroundTask{}).Where("id = ?", t.ID).Updates(map[string]any{
		"status":      t.Status,
		"progress":    t.Progress,
		"total":       t.Total,
		"message":     t.Message,
		"error":       t.Error,
		"finished_at": now,
	}).Error
	q.broadcast(Event{EventType: status, Task: *t})
}
