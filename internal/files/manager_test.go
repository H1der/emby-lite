package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
)

func TestFileManagerBoundaryAndOperations(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "media")
	outside := filepath.Join(tmp, "secret")
	_ = os.MkdirAll(allowed, 0o755)
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "key.txt"), []byte("secret"), 0o644)

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "files.db")
	cfg.Server.AllowedMediaRoots = []string{allowed}

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mgr := NewManager(db, cfg)

	// Reject outside path traversal
	if _, err := mgr.ResolveSafe(filepath.Join(allowed, "..", "secret", "key.txt"), true); err == nil {
		t.Fatal("expected boundary violation for traversal outside allowed root")
	}

	// Mkdir, upload, rename, move, list, delete inside boundary
	dirA, err := mgr.Mkdir(allowed, "Movies")
	if err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}
	dirB, err := mgr.Mkdir(allowed, "Archive")
	if err != nil {
		t.Fatalf("Mkdir B failed: %v", err)
	}
	uploaded, n, err := mgr.SaveUpload(dirA, "demo.srt", strings.NewReader("subtitle content"))
	if err != nil || n == 0 {
		t.Fatalf("SaveUpload failed: n=%d err=%v", n, err)
	}
	renamed, err := mgr.Rename(uploaded, "demo.zh.srt")
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	moved, err := mgr.Move(renamed, dirB)
	if err != nil {
		t.Fatalf("Move failed: %v", err)
	}
	_, _, _, entries, err := mgr.List(dirB)
	if err != nil || len(entries) != 1 || entries[0].Name != "demo.zh.srt" {
		t.Fatalf("unexpected List after move: %+v err=%v", entries, err)
	}
	if err := mgr.Delete(moved); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}
