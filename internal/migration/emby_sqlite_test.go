package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEmbyFullConfigLibrariesItemsUsersAndWatchMigration(t *testing.T) {
	tmp := t.TempDir()
	embyCfg := filepath.Join(tmp, "emby-config")
	_ = os.MkdirAll(filepath.Join(embyCfg, "data"), 0o755)
	_ = os.MkdirAll(filepath.Join(embyCfg, "root", "default", "电影库"), 0o755)
	_ = os.MkdirAll(filepath.Join(embyCfg, "root", "default", "美剧库"), 0o755)

	// 1. Write Emby root/default options.xml + .mblink
	moviesXML := `<?xml version="1.0"?>
<LibraryOptions>
  <ContentType>movies</ContentType>
  <PathInfos>
    <MediaPathInfo><Path>/old-nas/Movies</Path></MediaPathInfo>
  </PathInfos>
</LibraryOptions>`
	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "电影库", "options.xml"), []byte(moviesXML), 0o644)
	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "美剧库", "options.xml"), []byte(`<LibraryOptions><ContentType>tvshows</ContentType></LibraryOptions>`), 0o644)
	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "美剧库", "tv.mblink"), []byte("/old-nas/TV\n"), 0o644)

	// 2. Write synthetic Emby users.db
	uDB, err := gorm.Open(sqlite.Open(filepath.Join(embyCfg, "data", "users.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_ = uDB.Exec(`CREATE TABLE LocalUsersv2 (Id TEXT PRIMARY KEY, Name TEXT)`).Error
	_ = uDB.Exec(`INSERT INTO LocalUsersv2 (Id, Name) VALUES ('emby-u1', 'bob')`).Error
	sqlU, _ := uDB.DB()
	_ = sqlU.Close()

	// 3. Write synthetic Emby 4.x library.db (Movies + Series + Season + Episode + UserDatas)
	lDB, err := gorm.Open(sqlite.Open(filepath.Join(embyCfg, "data", "library.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_ = lDB.Exec(`CREATE TABLE MediaItems (
		Id INTEGER PRIMARY KEY, ParentId INTEGER, TopParentId INTEGER, SeriesId INTEGER, SeasonId INTEGER,
		UserDataKeyId INTEGER, type INTEGER, Name TEXT, OriginalTitle TEXT, Overview TEXT,
		ProductionYear INTEGER, PremiereDate TEXT, CommunityRating REAL, RunTimeTicks INTEGER,
		IndexNumber INTEGER, ParentIndexNumber INTEGER, Path TEXT, ProviderIds TEXT, Genres TEXT
	)`).Error
	_ = lDB.Exec(`CREATE TABLE UserDatas (userId TEXT, UserDataKeyId INTEGER, played INTEGER, playbackPositionTicks INTEGER, playCount INTEGER, isFavorite INTEGER)`).Error
	// Movie (type=5)
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES (100, 1, 1, NULL, NULL, 1001, 5, '星际穿越', 'Interstellar', '穿越虫洞', 2014, '2014-11-07T00:00:00Z', 8.7, 101400000000, 0, 0, '/old-nas/Movies/Interstellar (2014).mp4', 'Tmdb=157336|Imdb=tt0816692', '科幻|冒险')`).Error
	// Series (type=6), Season (type=7), Episode (type=8)
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES (200, 2, 2, NULL, NULL, 2001, 6, '人生切割术', 'Severance', '记忆分割', 2022, '2022-02-18', 8.7, 0, 0, 0, '/old-nas/TV/Severance', 'Tmdb=95396', '悬疑')`).Error
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES (201, 200, 2, 200, NULL, 2002, 7, '第 1 季', '', '', 2022, '', 0, 0, 1, 0, '/old-nas/TV/Severance/Season 01', '', '')`).Error
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES (202, 201, 2, 200, 201, 2003, 8, '好消息', '', '首集简介', 2022, '2022-02-18', 8.5, 34000000000, 1, 1, '/old-nas/TV/Severance/Season 01/S01E01.mkv', '', '')`).Error
	// Watch record for bob on Movie
	_ = lDB.Exec(`INSERT INTO UserDatas VALUES ('emby-u1', 1001, 1, 0, 3, 1)`).Error
	sqlL, _ := lDB.DB()
	_ = sqlL.Close()

	// 4. Target brand-new empty emby-lite DB (NO pre-existing libraries or items!)
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "target.db")
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewService(db)
	req := EmbyMigrationRequest{
		EmbyConfigDir: embyCfg,
		PathMappings: []PathMapping{
			{From: "/old-nas", To: "/new-media"},
		},
	}

	// Step A: Preview
	preview, err := svc.Preview(req)
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if preview.LibrariesFound != 2 || preview.NewLibrariesToCreate != 2 {
		t.Fatalf("expected 2 libraries in preview, got %+v", preview)
	}
	if preview.MoviesFound != 1 || preview.SeriesFound != 1 || preview.SeasonsFound != 1 || preview.EpisodesFound != 1 {
		t.Fatalf("unexpected item counts in preview: %+v", preview)
	}
	if preview.UsersFound != 1 || preview.MatchedWatchRecords != 1 {
		t.Fatalf("unexpected user/watch preview: %+v", preview)
	}

	// Step B: Full Import
	imp, err := svc.Import(req)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if imp.LibrariesCreated != 2 || imp.ItemsImported != 4 || imp.UsersCreated != 1 || imp.WatchImported != 1 {
		t.Fatalf("unexpected import report: %+v", imp)
	}

	// Verify library paths were remapped (/old-nas/Movies -> /new-media/Movies)
	var movieLib model.Library
	if err := db.Gorm.Where("name = ?", "电影库").First(&movieLib).Error; err != nil {
		t.Fatalf("migrated movie library missing: %v", err)
	}
	var libPaths []string
	_ = json.Unmarshal([]byte(movieLib.PathsJSON), &libPaths)
	if len(libPaths) != 1 || !strings.Contains(filepath.ToSlash(libPaths[0]), "/new-media/Movies") {
		t.Fatalf("expected remapped library path, got %+v", libPaths)
	}

	// Verify movie metadata, MediaSource path remapping, Genres, and ProviderIds
	var movie model.Item
	if err := db.Gorm.Where("name = ?", "星际穿越").First(&movie).Error; err != nil {
		t.Fatalf("migrated movie missing: %v", err)
	}
	if movie.LibraryID != movieLib.ID || movie.Year != 2014 || !strings.Contains(movie.ProviderIDsJSON, "157336") {
		t.Fatalf("unexpected migrated movie: %+v", movie)
	}
	var src model.MediaSource
	if err := db.Gorm.Where("item_id = ?", movie.ID).First(&src).Error; err != nil {
		t.Fatalf("migrated media source missing: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(src.Path), "/new-media/Movies/Interstellar (2014).mp4") {
		t.Fatalf("expected remapped source path, got %q", src.Path)
	}

	// Verify TV hierarchy (Episode -> Season -> Series)
	var ep model.Item
	if err := db.Gorm.Where("type = ? AND name = ?", "Episode", "好消息").First(&ep).Error; err != nil {
		t.Fatalf("migrated episode missing: %v", err)
	}
	if ep.SeriesID == "" || ep.SeasonID == "" || ep.SeasonNum != 1 || ep.EpisodeNum != 1 {
		t.Fatalf("broken migrated episode hierarchy: %+v", ep)
	}

	// Verify watch data bound cleanly to newly migrated movie!
	var bob model.User
	_ = db.Gorm.Where("username = ?", "bob").First(&bob).Error
	var ud model.UserData
	if err := db.Gorm.Where("user_id = ? AND item_id = ?", bob.ID, movie.ID).First(&ud).Error; err != nil {
		t.Fatalf("migrated watch record not bound to migrated movie: %v", err)
	}
	if !ud.Played || !ud.IsFavorite || ud.PlayCount != 3 {
		t.Fatalf("unexpected migrated userdata: %+v", ud)
	}
}
