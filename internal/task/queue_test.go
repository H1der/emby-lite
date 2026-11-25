package task

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
)

func TestTaskQueueExecutionDedupeAndSSE(t *testing.T) {
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "task.db")

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	q := NewQueue(db)
	doneCh := make(chan string, 1)
	q.Register("demo", func(_ context.Context, payload []byte, report ProgressReporter) error {
		var p map[string]string
		_ = json.Unmarshal(payload, &p)
		report(1, 2, "halfway")
		report(2, 2, "done")
		doneCh <- p["id"]
		return nil
	})

	sub := q.Subscribe()
	defer q.Unsubscribe(sub)

	t1, created1, err := q.Enqueue("demo", 10, map[string]string{"id": "lib-1"}, "queued")
	if err != nil || !created1 {
		t.Fatalf("enqueue 1 failed: created=%v err=%v", created1, err)
	}
	// Duplicate enqueue while pending must deduplicate!
	t2, created2, err := q.Enqueue("demo", 10, map[string]string{"id": "lib-1"}, "queued")
	if err != nil || created2 || t2.ID != t1.ID {
		t.Fatalf("expected dedupe to return same task id, got created=%v id=%s", created2, t2.ID)
	}

	q.Start(1)
	defer q.Stop()

	select {
	case got := <-doneCh:
		if got != "lib-1" {
			t.Fatalf("unexpected payload id: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for task execution")
	}

	// Wait briefly for completed event on SSE channel
	gotCompleted := false
	timeout := time.After(3 * time.Second)
	for !gotCompleted {
		select {
		case ev := <-sub:
			if ev.EventType == StatusCompleted && ev.Task.ID == t1.ID {
				gotCompleted = true
			}
		case <-timeout:
			t.Fatal("did not receive completed SSE event")
		}
	}
}

func TestTaskQueueGetAndHasActive(t *testing.T) {
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "task-get.db")

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	q := NewQueue(db)
	q.Register("slow", func(ctx context.Context, _ []byte, report ProgressReporter) error {
		report(1, 10, "working")
		<-ctx.Done()
		return ctx.Err()
	})

	t1, _, err := q.Enqueue("slow", 10, map[string]string{"n": "1"}, "queued")
	if err != nil {
		t.Fatal(err)
	}

	// HasActive must gate a second run of the same type even with a different payload.
	if !q.HasActive("slow") {
		t.Fatal("expected HasActive to report the pending task")
	}
	if q.HasActive("other") {
		t.Fatal("HasActive must not report an unrelated task type")
	}

	got, err := q.Get(t1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != t1.ID || got.Status != StatusPending {
		t.Fatalf("unexpected task: id=%s status=%s", got.ID, got.Status)
	}
	if _, err := q.Get("missing-id"); err == nil {
		t.Fatal("expected error for unknown task id")
	}
}
