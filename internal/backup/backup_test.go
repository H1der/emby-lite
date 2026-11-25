package backup

import (
	"path/filepath"
	"testing"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func TestPortableBackupAndRestoreRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	backupDir := filepath.Join(tmp, "backups")

	// Source DB
	cfg1 := config.Default()
	cfg1.Database.Type = "sqlite"
	cfg1.Database.SQLitePath = filepath.Join(tmp, "src.db")
	db1, err := database.Open(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()

	_ = db1.Gorm.Create(&model.User{ID: "u-1", Username: "alice", PasswordHash: "hash", IsAdmin: true}).Error
	_ = db1.Gorm.Create(&model.Library{ID: "lib-1", Name: "Movies", Kind: "movies", PathsJSON: `["/media"]`}).Error
	_ = db1.Gorm.Create(&model.Item{
		ID: "item-1", LibraryID: "lib-1", ParentID: "lib-1", Type: "Movie",
		Name: "Dune", SortName: "dune", ScanKey: "movie:lib-1:dune", AddedAt: time.Now().UTC(),
	}).Error
	_ = db1.Gorm.Create(&model.UserData{UserID: "u-1", ItemID: "item-1", PositionTicks: 123450000, IsFavorite: true}).Error

	svc1 := NewService(db1, backupDir)
	info, err := svc1.CreatePortable()
	if err != nil || info.Size == 0 {
		t.Fatalf("CreatePortable failed: info=%+v err=%v", info, err)
	}
	snap, err := svc1.CreateSQLiteNativeSnapshot()
	if err != nil || snap.Size == 0 {
		t.Fatalf("CreateSQLiteNativeSnapshot failed: snap=%+v err=%v", snap, err)
	}

	// Restore portable backup into a brand-new empty DB!
	cfg2 := config.Default()
	cfg2.Database.Type = "sqlite"
	cfg2.Database.SQLitePath = filepath.Join(tmp, "dst.db")
	db2, err := database.Open(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	svc2 := NewService(db2, backupDir)
	counts, err := svc2.RestorePortable(info.Filename)
	if err != nil {
		t.Fatalf("RestorePortable failed: %v", err)
	}
	if counts["users"] != 1 || counts["items"] != 1 || counts["user_data"] != 1 {
		t.Fatalf("unexpected restore counts: %+v", counts)
	}

	var restoredUD model.UserData
	if err := db2.Gorm.Where("user_id = ? AND item_id = ?", "u-1", "item-1").First(&restoredUD).Error; err != nil {
		t.Fatalf("restored userdata missing: %v", err)
	}
	if !restoredUD.IsFavorite || restoredUD.PositionTicks != 123450000 {
		t.Fatalf("unexpected restored userdata: %+v", restoredUD)
	}
}
