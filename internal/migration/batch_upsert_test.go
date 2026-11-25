package migration

import (
	"os"
	"path/filepath"
	"testing"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// writeProbeEmbyLibrary creates a tiny Emby config whose single movie carries the given
// title, so a re-import can be shown to update rather than duplicate it.
func writeProbeEmbyLibrary(t *testing.T, dir, title string) string {
	t.Helper()
	embyCfg := filepath.Join(dir, "emby-config")
	if err := os.MkdirAll(filepath.Join(embyCfg, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	libDir := filepath.Join(embyCfg, "root", "default", "电影库")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	options := `<?xml version="1.0"?><LibraryOptions><ContentType>movies</ContentType><PathInfos><MediaPathInfo><Path>/old-nas/Movies</Path></MediaPathInfo></PathInfos></LibraryOptions>`
	if err := os.WriteFile(filepath.Join(libDir, "options.xml"), []byte(options), 0o644); err != nil {
		t.Fatal(err)
	}

	uDB, err := gorm.Open(sqlite.Open(filepath.Join(embyCfg, "data", "users.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := uDB.Exec(`CREATE TABLE LocalUsersv2 (Id TEXT PRIMARY KEY, Name TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := uDB.Exec(`INSERT INTO LocalUsersv2 (Id, Name) VALUES ('emby-u1', 'bob')`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := uDB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	lDB, err := gorm.Open(sqlite.Open(filepath.Join(embyCfg, "data", "library.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE MediaItems (
			Id INTEGER PRIMARY KEY, ParentId INTEGER, TopParentId INTEGER, SeriesId INTEGER, SeasonId INTEGER,
			UserDataKeyId INTEGER, type INTEGER, Name TEXT, OriginalTitle TEXT, Overview TEXT,
			ProductionYear INTEGER, PremiereDate TEXT, CommunityRating REAL, RunTimeTicks INTEGER,
			IndexNumber INTEGER, ParentIndexNumber INTEGER, Path TEXT, ProviderIds TEXT, Genres TEXT
		)`,
		`CREATE TABLE UserDatas (userId TEXT, UserDataKeyId INTEGER, played INTEGER, playbackPositionTicks INTEGER, playCount INTEGER, isFavorite INTEGER)`,
		`INSERT INTO MediaItems VALUES (100, 1, 1, NULL, NULL, 1001, 5, '` + title + `', 'Interstellar', '', 2014, '2014-11-07', 8.7, 101400000000, 0, 0, '/old-nas/Movies/Interstellar (2014).mp4', 'Tmdb=157336', '科幻|冒险')`,
		`INSERT INTO UserDatas VALUES ('emby-u1', 1001, 1, 0, 3, 1)`,
	}
	for _, stmt := range stmts {
		if err := lDB.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := lDB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	return embyCfg
}

func openProbeTarget(t *testing.T, dir string) *database.DB {
	t.Helper()
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(dir, "target.db")
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestEmbyMigrationBatchUpsertIsIdempotent covers the batched write path: importing the
// same source twice must not duplicate rows, a plain re-run must skip everything, and an
// overwrite run must update the existing row in place (same primary key) instead of
// inserting a second one. This is the guard for the upsert replacing the old
// First + Save pair.
func TestEmbyMigrationBatchUpsertIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	embyCfg := writeProbeEmbyLibrary(t, tmp, "星际穿越")
	// The remapped media path is <tmp>/Movies/Interstellar (2014).mp4, so a poster dropped
	// into that folder proves the cached directory discovery still writes images.
	movieDir := filepath.Join(tmp, "Movies")
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(movieDir, "poster.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := openProbeTarget(t, tmp)
	svc := NewService(db)
	req := EmbyMigrationRequest{
		EmbyConfigDir: embyCfg,
		PathMappings:  []PathMapping{{From: "/old-nas", To: tmp}},
	}

	first, err := svc.Import(req)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if first.ItemsImported != 1 || first.ItemsFailed != 0 || first.ItemsSkipped != 0 {
		t.Fatalf("unexpected first import report: %+v", first)
	}

	var itemCount int64
	if err := db.Gorm.Model(&model.Item{}).Count(&itemCount).Error; err != nil {
		t.Fatal(err)
	}
	var sourceCount, genreCount, userDataCount, imageCount int64
	_ = db.Gorm.Model(&model.MediaSource{}).Count(&sourceCount).Error
	_ = db.Gorm.Model(&model.ItemGenre{}).Count(&genreCount).Error
	_ = db.Gorm.Model(&model.UserData{}).Count(&userDataCount).Error
	_ = db.Gorm.Model(&model.ItemImage{}).Count(&imageCount).Error
	if itemCount != 1 || sourceCount != 1 || genreCount != 2 || userDataCount != 1 || imageCount == 0 {
		t.Fatalf("unexpected row counts after first import: items=%d sources=%d genres=%d userdata=%d images=%d",
			itemCount, sourceCount, genreCount, userDataCount, imageCount)
	}

	var original model.Item
	if err := db.Gorm.Where("type = ?", "Movie").First(&original).Error; err != nil {
		t.Fatal(err)
	}

	// Second run without overwrite: everything is recognised as existing.
	second, err := svc.Import(req)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if second.ItemsImported != 0 || second.ItemsSkipped != 1 || second.ItemsFailed != 0 {
		t.Fatalf("unexpected second import report: %+v", second)
	}

	// Third run with overwrite: the same row must be updated in place.
	statBefore, err := os.Stat(filepath.Join(tmp, "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	overwriteReq := req
	overwriteReq.Overwrite = true
	third, err := svc.Import(overwriteReq)
	if err != nil {
		t.Fatalf("overwrite import failed: %v", err)
	}
	if third.ItemsImported != 1 || third.ItemsFailed != 0 {
		t.Fatalf("unexpected overwrite import report: %+v", third)
	}
	_ = statBefore

	var afterCount int64
	if err := db.Gorm.Model(&model.Item{}).Count(&afterCount).Error; err != nil {
		t.Fatal(err)
	}
	if afterCount != 1 {
		t.Fatalf("overwrite duplicated items: %d rows", afterCount)
	}
	var after model.Item
	if err := db.Gorm.Where("type = ?", "Movie").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.ID != original.ID || after.CreatedAt.Unix() != original.CreatedAt.Unix() {
		t.Fatalf("overwrite changed the item identity: before=%+v after=%+v", original, after)
	}
	var ftsRows int64
	if db.HasFTS5 {
		if err := db.Gorm.Raw("SELECT COUNT(*) FROM items_fts WHERE item_id = ?", original.ID).Scan(&ftsRows).Error; err != nil {
			t.Fatal(err)
		}
		if ftsRows != 1 {
			t.Fatalf("expected exactly one FTS row per item after overwrite, got %d", ftsRows)
		}
	}

	// Watch state must also survive a re-run without duplicating.
	var ud model.UserData
	if err := db.Gorm.Where("play_count = ?", 3).First(&ud).Error; err != nil {
		t.Fatalf("watch state lost across re-import: %v", err)
	}
	var udCount int64
	_ = db.Gorm.Model(&model.UserData{}).Count(&udCount).Error
	if udCount != 1 {
		t.Fatalf("watch state duplicated: %d rows", udCount)
	}
}
