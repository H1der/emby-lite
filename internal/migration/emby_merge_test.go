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

func TestEmbyMigrationAutoMergeVersionsAndCrossFolderSeries(t *testing.T) {
	tmp := t.TempDir()
	embyCfg := filepath.Join(tmp, "emby-config")
	_ = os.MkdirAll(filepath.Join(embyCfg, "data"), 0o755)
	_ = os.MkdirAll(filepath.Join(embyCfg, "root", "default", "电影"), 0o755)
	_ = os.MkdirAll(filepath.Join(embyCfg, "root", "default", "剧集-开启合并"), 0o755)
	_ = os.MkdirAll(filepath.Join(embyCfg, "root", "default", "剧集-关闭合并"), 0o755)

	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "电影", "options.xml"), []byte(`<?xml version="1.0"?>
<LibraryOptions>
  <ContentType>movies</ContentType>
  <PathInfos><MediaPathInfo><Path>/nas/movies</Path></MediaPathInfo></PathInfos>
</LibraryOptions>`), 0o644)

	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "剧集-开启合并", "options.xml"), []byte(`<?xml version="1.0"?>
<LibraryOptions>
  <ContentType>tvshows</ContentType>
  <EnableAutomaticSeriesGrouping>true</EnableAutomaticSeriesGrouping>
  <PathInfos>
    <MediaPathInfo><Path>/nas/tv1</Path></MediaPathInfo>
    <MediaPathInfo><Path>/nas/tv2</Path></MediaPathInfo>
  </PathInfos>
</LibraryOptions>`), 0o644)

	_ = os.WriteFile(filepath.Join(embyCfg, "root", "default", "剧集-关闭合并", "options.xml"), []byte(`<?xml version="1.0"?>
<LibraryOptions>
  <ContentType>tvshows</ContentType>
  <EnableAutomaticSeriesGrouping>false</EnableAutomaticSeriesGrouping>
  <PathInfos>
    <MediaPathInfo><Path>/nas/tv-off1</Path></MediaPathInfo>
    <MediaPathInfo><Path>/nas/tv-off2</Path></MediaPathInfo>
  </PathInfos>
</LibraryOptions>`), 0o644)

	uDB, err := gorm.Open(sqlite.Open(filepath.Join(embyCfg, "data", "users.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_ = uDB.Exec(`CREATE TABLE LocalUsersv2 (Id TEXT PRIMARY KEY, Name TEXT)`).Error
	_ = uDB.Exec(`INSERT INTO LocalUsersv2 (Id, Name) VALUES ('u1', 'alice')`).Error
	sqlU, _ := uDB.DB()
	_ = sqlU.Close()

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

	// 1. Two versions of the same Movie (different files, same TMDB ID) with watch data split across versions!
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES
		(101, 1, 1, NULL, NULL, 1001, 5, '流浪地球2', 'The Wandering Earth II', '4K版本', 2023, '2023-01-22', 8.3, 103800000000, 0, 0, '/nas/movies/Wandering Earth 2 (2023)/Wandering Earth 2 (2023) - 4K.mkv', 'Tmdb=842675', '科幻'),
		(102, 1, 1, NULL, NULL, 1002, 5, '流浪地球2', 'The Wandering Earth II', '', 2023, '2023-01-22', 8.3, 103800000000, 0, 0, '/nas/movies/Wandering Earth 2 (2023)/Wandering Earth 2 (2023) - 1080p.mkv', 'Tmdb=842675', '科幻')`).Error
	_ = lDB.Exec(`INSERT INTO UserDatas VALUES
		('u1', 1001, 1, 0, 2, 0),
		('u1', 1002, 0, 500000000, 1, 1)`).Error

	// 2. Cross-folder TV Series (EnableAutomaticSeriesGrouping=true): Season 1 in /nas/tv1, Season 2 in /nas/tv2
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES
		(201, 2, 2, NULL, NULL, 2001, 6, '庆余年', 'Joy of Life', '第一季', 2019, '2019-11-26', 8.0, 0, 0, 0, '/nas/tv1/庆余年', 'Tmdb=96215', '古装'),
		(202, 201, 2, 201, NULL, 2002, 7, '第 1 季', '', '', 2019, '', 0, 0, 1, 0, '/nas/tv1/庆余年/Season 01', '', ''),
		(203, 202, 2, 201, 202, 2003, 8, '第 1 集', '', '', 2019, '', 0, 27000000000, 1, 1, '/nas/tv1/庆余年/Season 01/S01E01.mkv', '', ''),
		(211, 2, 2, NULL, NULL, 2011, 6, '庆余年', 'Joy of Life', '第二季', 2019, '2019-11-26', 8.0, 0, 0, 0, '/nas/tv2/庆余年', 'Tmdb=96215', '古装'),
		(212, 211, 2, 211, NULL, 2012, 7, '第 2 季', '', '', 2024, '', 0, 0, 2, 0, '/nas/tv2/庆余年/Season 02', '', ''),
		(213, 212, 2, 211, 212, 2013, 8, '第 1 集', '', '', 2024, '', 0, 27000000000, 1, 2, '/nas/tv2/庆余年/Season 02/S02E01.mkv', '', '')`).Error

	// 3. Cross-folder TV Series with EnableAutomaticSeriesGrouping=false: should remain separate!
	_ = lDB.Exec(`INSERT INTO MediaItems VALUES
		(301, 3, 3, NULL, NULL, 3001, 6, '测试剧', '', '', 2020, '', 0, 0, 0, 0, '/nas/tv-off1/测试剧', 'Tmdb=77777', ''),
		(302, 3, 3, NULL, NULL, 3002, 6, '测试剧', '', '', 2020, '', 0, 0, 0, 0, '/nas/tv-off2/测试剧', 'Tmdb=77777', '')`).Error

	sqlL, _ := lDB.DB()
	_ = sqlL.Close()

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "target.db")
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := NewService(db)
	rep, err := svc.Import(EmbyMigrationRequest{EmbyConfigDir: embyCfg})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if rep.MoviesMerged != 1 || rep.SeriesMerged != 1 {
		t.Fatalf("expected MoviesMerged=1 SeriesMerged=1, got %+v", rep)
	}

	// Verify Movie merged into 1 item with 2 MediaSources and merged UserData (Played=true AND IsFavorite=true)
	var movies []model.Item
	db.Read().Where("type = 'Movie' AND soft_deleted_at IS NULL").Find(&movies)
	if len(movies) != 1 {
		t.Fatalf("expected 1 merged movie, got %d", len(movies))
	}
	var srcs []model.MediaSource
	db.Read().Where("item_id = ?", movies[0].ID).Find(&srcs)
	if len(srcs) != 2 {
		t.Fatalf("expected 2 movie MediaSources, got %d", len(srcs))
	}
	var ud model.UserData
	if err := db.Read().Where("item_id = ?", movies[0].ID).First(&ud).Error; err != nil {
		t.Fatalf("expected merged UserData: %v", err)
	}
	if !ud.Played || !ud.IsFavorite || ud.PlayCount != 2 {
		t.Fatalf("expected merged UserData (played=true, fav=true, count=2), got %+v", ud)
	}

	// Verify grouped TV library merged into 1 Series with 2 Seasons
	var tvOnLib model.Library
	db.Read().Where("name = ?", "剧集-开启合并").First(&tvOnLib)
	if !tvOnLib.AutoGroupSeries {
		t.Fatalf("expected AutoGroupSeries=true on 剧集-开启合并")
	}
	var onSeries []model.Item
	db.Read().Where("library_id = ? AND type = 'Series' AND soft_deleted_at IS NULL", tvOnLib.ID).Find(&onSeries)
	if len(onSeries) != 1 {
		t.Fatalf("expected 1 merged Series in 剧集-开启合并, got %d", len(onSeries))
	}
	var onSeasons []model.Item
	db.Read().Where("series_id = ? AND type = 'Season' AND soft_deleted_at IS NULL", onSeries[0].ID).Order("season_num ASC").Find(&onSeasons)
	if len(onSeasons) != 2 || onSeasons[0].SeasonNum != 1 || onSeasons[1].SeasonNum != 2 {
		t.Fatalf("expected Season 1 & 2 under merged Series, got %+v", onSeasons)
	}

	// Verify ungrouped TV library respected EnableAutomaticSeriesGrouping=false
	var tvOffLib model.Library
	db.Read().Where("name = ?", "剧集-关闭合并").First(&tvOffLib)
	if tvOffLib.AutoGroupSeries {
		t.Fatalf("expected AutoGroupSeries=false on 剧集-关闭合并")
	}
	var offSeries []model.Item
	db.Read().Where("library_id = ? AND type = 'Series' AND soft_deleted_at IS NULL", tvOffLib.ID).Find(&offSeries)
	if len(offSeries) != 2 {
		t.Fatalf("expected 2 ungrouped Series in 剧集-关闭合并, got %d", len(offSeries))
	}
}
