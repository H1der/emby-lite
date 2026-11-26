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

// TestEmby49MediaItemsSchemaMigration covers the newer Emby 4.9+ layout: numeric
// BaseItemKind types, libraries as CollectionFolder items whose folders live in
// ItemLinks2, genres/studios only in ItemLinks2, LocalUsersv2 profile JSON and
// UserDatas keyed by UserDataKeys2.
func TestEmby49MediaItemsSchemaMigration(t *testing.T) {
	tmp := t.TempDir()
	usersPath := filepath.Join(tmp, "users.db")
	libraryPath := filepath.Join(tmp, "library.db")

	// users.db: no Name column, the profile lives in the data JSON blob.
	uDB, err := gorm.Open(sqlite.Open(usersPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_ = uDB.Exec(`CREATE TABLE LocalUsersv2 (Id INTEGER PRIMARY KEY, guid BLOB, data TEXT)`).Error
	_ = uDB.Exec(`CREATE TABLE UserSettings (UserId INTEGER NOT NULL, UserSettingsKeyId INTEGER NOT NULL, Value TEXT NOT NULL, PRIMARY KEY (UserId, UserSettingsKeyId))`).Error
	_ = uDB.Exec(`CREATE TABLE UserSettingsKeys (UserSettingsKeyId INTEGER PRIMARY KEY AUTOINCREMENT, Name TEXT)`).Error
	_ = uDB.Exec(`INSERT INTO LocalUsersv2 (Id, guid, data) VALUES
		(1, NULL, '{"Name":"alice","IdString":"alice-guid","Password":"x","Policy":{"IsAdministrator":true}}'),
		(2, NULL, '{"Name":"carol","IdString":"carol-guid","Password":"y"}')`).Error
	_ = uDB.Exec(`INSERT INTO UserSettingsKeys (UserSettingsKeyId, Name) VALUES (1, 'usersettings'), (2, 'usernotifications')`).Error
	_ = uDB.Exec(`INSERT INTO UserSettings (UserId, UserSettingsKeyId, Value) VALUES
		(1, 1, '{"Playback":{"MaxStreamingBitrate":120000000}}'),
		(1, 2, '{"NotificationOptions":[]}')`).Error
	sqlU, _ := uDB.DB()
	_ = sqlU.Close()

	lDB, err := gorm.Open(sqlite.Open(libraryPath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_ = lDB.Exec(`CREATE TABLE MediaItems (
		Id INTEGER PRIMARY KEY, guid BLOB, type INTEGER, ParentId INTEGER, Path TEXT, Name TEXT,
		PremiereDate TEXT, ProductionYear INTEGER, CommunityRating REAL, RunTimeTicks INTEGER,
		IndexNumber INTEGER, ParentIndexNumber INTEGER, ProviderIds TEXT, Images TEXT, UserDataKeyId INTEGER,
		SortName TEXT, Overview TEXT,
		Status TEXT, DisplayOrder INTEGER, CustomRating TEXT, CriticRating REAL, ProductionLocations TEXT, RemoteTrailers TEXT, Tags TEXT
	)`).Error
	_ = lDB.Exec(`CREATE TABLE ItemLinks2 (ItemId INTEGER, Type INTEGER, LinkedId INTEGER, LinkOrder INTEGER)`).Error
	_ = lDB.Exec(`CREATE TABLE ItemPeople2 (ItemId INTEGER, PersonId INTEGER, Role TEXT, PersonType INTEGER NOT NULL, ListOrder INTEGER)`).Error
	_ = lDB.Exec(`CREATE TABLE UserDataKeys2 (Id INTEGER PRIMARY KEY, UserDataKey TEXT)`).Error
	_ = lDB.Exec(`CREATE TABLE UserDatas (UserDataKeyId INTEGER, userId INTEGER, played INTEGER, playCount INTEGER, isFavorite INTEGER, playbackPositionTicks INTEGER, LastPlayedDateInt INTEGER)`).Error
	_ = lDB.Exec(`CREATE TABLE Chapters3 (ItemId INTEGER, ChapterIndex INTEGER, StartPositionTicks INTEGER, Name TEXT, ImagePath TEXT, ImageDateModified TEXT, MarkerType INTEGER, PRIMARY KEY (ItemId, ChapterIndex))`).Error
	_ = lDB.Exec(`CREATE TABLE MediaStreams2 (ItemId INTEGER, StreamIndex INTEGER, StreamType INTEGER, Codec TEXT, Language TEXT, Path TEXT, Width INTEGER, Height INTEGER, IsExternal INTEGER, IsDefault INTEGER, Title TEXT, PRIMARY KEY (ItemId, StreamIndex))`).Error
	_ = lDB.Exec(`CREATE TABLE ItemExtradataTypes (ExtradataTypeId INTEGER PRIMARY KEY, Name TEXT)`).Error
	_ = lDB.Exec(`CREATE TABLE ItemExtradata (ItemId INTEGER, ExtradataTypeId INTEGER, Value TEXT, PRIMARY KEY (ItemId, ExtradataTypeId))`).Error
	_ = lDB.Exec(`INSERT INTO ItemExtradataTypes (ExtradataTypeId, Name) VALUES (1, 'IntroDetectionExtradata'), (2, 'LibraryOptions')`).Error

	// Root + "Media Folders" pseudo library, then two real libraries (type 4)
	// that only reference their physical folders through ItemLinks2 type 8.
	_ = lDB.Exec(`INSERT INTO MediaItems (Id, type, ParentId, Path, Name) VALUES
		(10, 1, NULL, NULL, 'root'),
		(11, 2, 10, NULL, 'Media Folders'),
		(4, 4, 11, NULL, '电影'),
		(5, 4, 11, NULL, '电视节目'),
		(6, 4, 11, NULL, '合集'),
		(1, 3, 11, '/emby/movies', 'movies'),
		(2, 3, 11, '/emby/tv', 'tv'),
		(300, 21, NULL, NULL, '科幻'),
		(400, 29, NULL, NULL, '传奇影业'),
		(700, 34, NULL, NULL, '经典'),
		(800, 9, NULL, NULL, '诺兰合集')`).Error
	_ = lDB.Exec(`INSERT INTO ItemExtradata (ItemId, ExtradataTypeId, Value) VALUES (6, 2, '{"ContentType":"boxsets","PathInfos":[],"MinCollectionItems":2}')`).Error
	_ = lDB.Exec(`UPDATE MediaItems SET Images = 'https://image.tmdb.org/t/p/original/nolan.jpg*0*Primary*1000*1500*null' WHERE Id = 800`).Error
	_ = lDB.Exec(`INSERT INTO ItemLinks2 (ItemId, Type, LinkedId, LinkOrder) VALUES
		(4, 8, 1, 0),
		(5, 8, 2, 0),
		(100, 2, 300, 0),
		(100, 3, 400, 0),
		(100, 4, 700, 0),
		(100, 6, 800, 0)`).Error

	// Movie: epoch PremiereDate, no ProductionYear column value, genres/studios only via links.
	_ = lDB.Exec(`INSERT INTO MediaItems (Id, type, ParentId, Path, Name, PremiereDate, ProductionYear, CommunityRating, RunTimeTicks, ProviderIds, UserDataKeyId, Status, DisplayOrder, CustomRating, CriticRating, ProductionLocations, RemoteTrailers) VALUES
		(100, 5, 1, '/emby/movies/Inception (2010)/Inception.mkv', '盗梦空间', '1293840000', 0, 8.8, 88800000000, 'Tmdb=27205|Imdb=tt1375666', 501, '', 1, 'PG-13', 8.5, 'United States of America', 'https://youtube.com/watch?v=abc')`).Error
	// Series path resolves the library, the season has no path at all and must
	// inherit it from the series; 203 is an episode hanging off the series.
	_ = lDB.Exec(`INSERT INTO MediaItems (Id, type, ParentId, Path, Name, IndexNumber, UserDataKeyId) VALUES
		(200, 6, 2, '/emby/tv/Breaking Bad', '绝命毒师', NULL, NULL),
		(201, 7, 200, NULL, '第 1 季', 1, NULL)`).Error
	_ = lDB.Exec(`INSERT INTO MediaItems (Id, type, ParentId, Path, Name, IndexNumber, ParentIndexNumber, PremiereDate, UserDataKeyId) VALUES
		(202, 8, 201, '/emby/tv/Breaking Bad/S01E01.mkv', '试播集', 1, 1, '1295000000', 502),
		(203, 8, 200, '/emby/tv/Breaking Bad/Featurette.mkv', '幕后花絮', 2, 2, '', 503),
		(204, 8, 200, '/emby/tv/Breaking Bad/S01E02.mkv', '猫在袋子里', 2, 1, '1295000100', 504)`).Error
	_ = lDB.Exec(`INSERT INTO UserDataKeys2 (Id, UserDataKey) VALUES (501, 'items/501'), (502, 'items/502'), (503, 'items/503'), (504, 'items/504')`).Error
	_ = lDB.Exec(`INSERT INTO UserDatas (UserDataKeyId, userId, played, playCount, isFavorite, playbackPositionTicks, LastPlayedDateInt) VALUES
		(501, 1, 1, 2, 1, 1234, 1600000000),
		(502, 1, 1, 1, 0, 0, 1600000100),
		(503, 2, 0, 0, 1, 0, 1600000200)`).Error
	_ = lDB.Exec(`INSERT INTO Chapters3 (ItemId, ChapterIndex, StartPositionTicks, Name, MarkerType) VALUES
		(100, 0, 0, '开场', 0),
		(100, 1, 30000000, '片头', 1),
		(100, 2, 60000000, '片尾', 2)`).Error
	_ = lDB.Exec(`INSERT INTO MediaStreams2 (ItemId, StreamIndex, StreamType, Codec, Language, Path, Width, Height, IsExternal, IsDefault, Title) VALUES
		(100, 0, 2, 'h264', 'und', '', 1920, 1080, 0, 1, '1080p'),
		(100, 1, 1, 'aac', 'chi', '', 0, 0, 0, 1, '国语'),
		(100, 2, 3, 'ass', 'chi', '/emby/subs/inception.ass', 0, 0, 1, 0, '简体中文')`).Error
	// Person items (type 23) and their links. 900 has a remote portrait and a
	// TMDB id; the movie carries every major PersonType to prove nothing is dropped.
	_ = lDB.Exec(`INSERT INTO MediaItems (Id, type, Name, ProviderIds, Images) VALUES
		(900, 23, '莱昂纳多·迪卡普里奥', 'Tmdb=6193', 'https://image.tmdb.org/t/p/original/leonardo.jpg*0*Primary*0*0*null'),
		(901, 23, '约瑟夫·高登-莱维特', 'Tmdb=24045', ''),
		(902, 23, '克里斯托弗·诺兰', 'Tmdb=525', ''),
		(903, 23, '乔纳森·诺兰', 'Tmdb=1010', ''),
		(904, 23, '艾玛·托马斯', 'Tmdb=9999', ''),
		(905, 23, '渡边谦', 'Tmdb=3894', ''),
		(906, 23, '汉斯·季默', 'Tmdb=947', '')`).Error
	_ = lDB.Exec(`INSERT INTO ItemPeople2 (ItemId, PersonId, Role, PersonType, ListOrder) VALUES
		(100, 900, '柯布', 0, 1),
		(100, 901, '亚瑟', 0, 0),
		(100, 902, '', 1, 0),
		(100, 903, '', 2, 0),
		(100, 904, '', 3, 0),
		(100, 905, '客串角色', 4, 0),
		(100, 906, '', 5, 0),
		(202, 900, '', 0, 0)`).Error
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
	snapshotDir := t.TempDir()
	req := EmbyMigrationRequest{UsersDBPath: usersPath, LibraryDBPath: libraryPath, SnapshotDir: snapshotDir}

	preview, err := svc.Preview(req)
	if err != nil {
		t.Fatalf("Preview failed: %v", err)
	}
	if preview.LibrariesFound != 3 || preview.MoviesFound != 1 || preview.SeriesFound != 1 ||
		preview.SeasonsFound != 2 || preview.EpisodesFound != 3 || preview.BoxSetsFound != 1 ||
		preview.TagsFound != 1 || preview.ChaptersFound != 3 || preview.StreamsFound != 3 ||
		preview.UsersFound != 2 || preview.WatchRecordsFound != 3 || preview.MatchedWatchRecords != 3 {
		t.Fatalf("unexpected preview for Emby 4.9+ schema: %+v", preview)
	}
	for _, w := range preview.Warnings {
		if strings.Contains(w, "未识别") || strings.Contains(w, "未找到") {
			t.Fatalf("unexpected warning: %q", w)
		}
	}

	imp, err := svc.Import(req)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if imp.LibrariesCreated != 3 || imp.ItemsImported != 7 || imp.UsersCreated != 2 || imp.WatchImported != 3 ||
		imp.TagsImported != 1 || imp.ChaptersImported != 3 || imp.StreamsImported != 3 || imp.BoxSetsImported != 1 ||
		imp.SettingsImported != 2 {
		t.Fatalf("unexpected import report: %+v", imp)
	}

	var movieLib model.Library
	if err := db.Gorm.Where("name = ?", "电影").First(&movieLib).Error; err != nil {
		t.Fatalf("movie library missing: %v", err)
	}
	var libPaths []string
	_ = json.Unmarshal([]byte(movieLib.PathsJSON), &libPaths)
	if len(libPaths) != 1 || !strings.Contains(filepath.ToSlash(libPaths[0]), "/emby/movies") {
		t.Fatalf("expected library path from ItemLinks2, got %+v", libPaths)
	}

	var movie model.Item
	if err := db.Gorm.Where("type = ? AND name = ?", "Movie", "盗梦空间").First(&movie).Error; err != nil {
		t.Fatalf("migrated movie missing: %v", err)
	}
	if movie.LibraryID != movieLib.ID {
		t.Fatalf("movie bound to wrong library: %+v", movie)
	}
	if movie.Year != 2011 {
		t.Fatalf("expected year 2011 from epoch PremiereDate, got %d", movie.Year)
	}
	if !strings.Contains(movie.ProviderIDsJSON, "27205") {
		t.Fatalf("provider ids not migrated: %q", movie.ProviderIDsJSON)
	}
	if movie.CustomRating != "PG-13" || movie.CriticRating != 8.5 || movie.DisplayOrder != 1 || !strings.Contains(movie.ProductionLocationsJSON, "United States") {
		t.Fatalf("extended movie metadata not migrated: %+v", movie)
	}
	if movie.OriginalName != "" {
		t.Fatalf("unexpected original name: %+v", movie)
	}
	var tagCount, chapterCount, subtitleCount, boxSetCount, boxItemCount int64
	_ = db.Gorm.Model(&model.ItemTag{}).Where("item_id = ?", movie.ID).Count(&tagCount).Error
	_ = db.Gorm.Model(&model.ItemChapter{}).Where("item_id = ?", movie.ID).Count(&chapterCount).Error
	_ = db.Gorm.Model(&model.MediaSubtitle{}).Where("item_id = ?", movie.ID).Count(&subtitleCount).Error
	_ = db.Gorm.Model(&model.BoxSet{}).Count(&boxSetCount).Error
	_ = db.Gorm.Model(&model.BoxSetItem{}).Count(&boxItemCount).Error
	if tagCount != 1 || chapterCount != 3 || subtitleCount != 1 || boxSetCount != 1 || boxItemCount != 1 {
		t.Fatalf("extras not migrated: tags=%d chapters=%d subtitles=%d boxsets=%d boxitems=%d", tagCount, chapterCount, subtitleCount, boxSetCount, boxItemCount)
	}
	var boxsetLib model.Library
	if err := db.Gorm.Where("name = ?", "合集").First(&boxsetLib).Error; err != nil {
		t.Fatalf("boxsets library missing: %v", err)
	}
	if boxsetLib.Kind != "boxsets" {
		t.Fatalf("boxsets library kind = %q", boxsetLib.Kind)
	}
	var boxsetItem model.Item
	if err := db.Gorm.Where("type = ?", "BoxSet").First(&boxsetItem).Error; err != nil {
		t.Fatalf("boxset item missing: %v", err)
	}
	if boxsetItem.LibraryID != boxsetLib.ID || boxsetItem.ParentID != boxsetLib.ID || !boxsetItem.IsFolder {
		t.Fatalf("boxset item not attached to boxsets library: %+v", boxsetItem)
	}
	var boxsetLink model.BoxSetItem
	if err := db.Gorm.Where("box_set_id = ?", boxsetItem.ID).First(&boxsetLink).Error; err != nil {
		t.Fatalf("boxset membership missing: %v", err)
	}
	if boxsetLink.ItemID != movie.ID {
		t.Fatalf("boxset member = %q, want %q", boxsetLink.ItemID, movie.ID)
	}
	var boxsetImage model.ItemImage
	if err := db.Gorm.Where("item_id = ? AND kind = ?", boxsetItem.ID, "primary").First(&boxsetImage).Error; err != nil {
		t.Fatalf("boxset image missing: %v", err)
	}
	if !strings.Contains(boxsetImage.SourceRef, "nolan.jpg") {
		t.Fatalf("boxset image source = %q", boxsetImage.SourceRef)
	}
	var source model.MediaSource
	if err := db.Gorm.Where("item_id = ?", movie.ID).First(&source).Error; err != nil {
		t.Fatalf("movie source missing: %v", err)
	}
	if !strings.Contains(source.ProbeJSON, "h264") || source.Width != 1920 || source.Height != 1080 {
		t.Fatalf("probe streams not migrated: %+v", source)
	}
	if imp.SnapshotDir == "" || len(imp.SnapshotFiles) != 2 {
		t.Fatalf("source snapshot missing: %+v", imp)
	}
	for _, f := range imp.SnapshotFiles {
		if st, err := os.Stat(f); err != nil || st.Size() == 0 {
			t.Fatalf("snapshot file invalid: %s (%v)", f, err)
		}
	}
	var genres []model.ItemGenre
	_ = db.Gorm.Where("item_id = ?", movie.ID).Find(&genres).Error
	if len(genres) != 1 || genres[0].Genre != "科幻" {
		t.Fatalf("genres from ItemLinks2 not migrated: %+v", genres)
	}
	if !strings.Contains(movie.StudiosJSON, "传奇影业") {
		t.Fatalf("studios from ItemLinks2 not migrated: %q", movie.StudiosJSON)
	}

	var tvLib model.Library
	if err := db.Gorm.Where("name = ?", "电视节目").First(&tvLib).Error; err != nil {
		t.Fatalf("tv library missing: %v", err)
	}
	var series, season, ep, extra model.Item
	if err := db.Gorm.Where("type = ? AND name = ?", "Series", "绝命毒师").First(&series).Error; err != nil {
		t.Fatalf("migrated series missing: %v", err)
	}
	if err := db.Gorm.Where("type = ? AND name = ?", "Episode", "试播集").First(&ep).Error; err != nil {
		t.Fatalf("migrated episode missing: %v", err)
	}
	if err := db.Gorm.Where("type = ? AND name = ?", "Episode", "幕后花絮").First(&extra).Error; err != nil {
		t.Fatalf("migrated season-less episode missing: %v", err)
	}
	if err := db.Gorm.Where("id = ?", ep.SeasonID).First(&season).Error; err != nil {
		t.Fatalf("migrated season missing: %v", err)
	}
	if series.LibraryID != tvLib.ID || season.LibraryID != tvLib.ID {
		t.Fatalf("tv hierarchy bound to wrong library: series=%+v season=%+v", series, season)
	}
	if season.SeriesID != series.ID || ep.SeriesID != series.ID || ep.SeasonID != season.ID {
		t.Fatalf("broken tv hierarchy: season=%+v ep=%+v", season, ep)
	}
	// Emby hung episode 203 directly off the series with a season number that
	// has no season row; the migration must synthesise it so emby-lite can
	// still browse the episode. Episode 204 hangs off the series too but its
	// season already exists, so it must bind to that real season instead.
	if extra.SeasonID == "" || extra.SeriesID != series.ID {
		t.Fatalf("season-less episode should get a synthetic season: %+v", extra)
	}
	var synth model.Item
	if err := db.Gorm.Where("id = ?", extra.SeasonID).First(&synth).Error; err != nil {
		t.Fatalf("synthetic season missing: %v", err)
	}
	if synth.Type != "Season" || synth.SeriesID != series.ID || synth.ParentID != series.ID || synth.SeasonNum != 2 {
		t.Fatalf("unexpected synthetic season: %+v", synth)
	}
	var seasonBound model.Item
	if err := db.Gorm.Where("type = ? AND name = ?", "Episode", "猫在袋子里").First(&seasonBound).Error; err != nil {
		t.Fatalf("episode bound to an existing season missing: %v", err)
	}
	if seasonBound.SeasonID != season.ID || seasonBound.SeriesID != series.ID {
		t.Fatalf("episode should reuse the existing season: %+v", seasonBound)
	}

	var alice, carol model.User
	_ = db.Gorm.Where("username = ?", "alice").First(&alice).Error
	_ = db.Gorm.Where("username = ?", "carol").First(&carol).Error
	if alice.ID == "" || carol.ID == "" {
		t.Fatalf("users from LocalUsersv2 JSON not migrated: %+v %+v", alice, carol)
	}
	if !alice.IsAdmin || carol.IsAdmin {
		t.Fatalf("admin flag not migrated: alice=%+v carol=%+v", alice, carol)
	}
	var settings []model.UserSetting
	_ = db.Gorm.Where("user_id = ?", alice.ID).Order("key ASC").Find(&settings).Error
	if len(settings) != 2 || !strings.Contains(settings[0].Value+settings[1].Value, "MaxStreamingBitrate") {
		t.Fatalf("user settings not migrated: %+v", settings)
	}
	var aliceData model.UserData
	if err := db.Gorm.Where("user_id = ? AND item_id = ?", alice.ID, movie.ID).First(&aliceData).Error; err != nil {
		t.Fatalf("watch record not bound to movie: %v", err)
	}
	if !aliceData.Played || !aliceData.IsFavorite || aliceData.PlayCount != 2 || aliceData.PositionTicks != 1234 {
		t.Fatalf("unexpected migrated userdata: %+v", aliceData)
	}
	var carolData model.UserData
	if err := db.Gorm.Where("user_id = ? AND item_id = ?", carol.ID, extra.ID).First(&carolData).Error; err != nil {
		t.Fatalf("watch record not bound by Emby item id: %v", err)
	}
	if !carolData.IsFavorite {
		t.Fatalf("unexpected favorite state: %+v", carolData)
	}
	if imp.PeopleImported != 8 {
		t.Fatalf("expected 8 migrated people links, got %+v", imp)
	}

	var peopleLinks []model.ItemPerson
	if err := db.Gorm.Where("item_id = ?", movie.ID).Order("sort_idx ASC").Find(&peopleLinks).Error; err != nil {
		t.Fatalf("load movie people: %v", err)
	}
	if len(peopleLinks) != 7 {
		t.Fatalf("expected 7 movie people links, got %+v", peopleLinks)
	}
	wantRoles := []struct{ roleType, roleName string }{
		{"Actor", "亚瑟"},
		{"Actor", "柯布"},
		{"GuestStar", "客串角色"},
		{"Director", ""},
		{"Writer", ""},
		{"Producer", ""},
		{"Composer", ""},
	}
	for i, want := range wantRoles {
		link := peopleLinks[i]
		if link.RoleType != want.roleType || link.RoleName != want.roleName {
			t.Fatalf("role %d mismatch: got %+v want %+v", i, link, want)
		}
		if link.SortIdx != i {
			t.Fatalf("expected sequential sort index %d, got %+v", i, link)
		}
	}

	var person model.Person
	if err := db.Gorm.Where("name = ?", "莱昂纳多·迪卡普里奥").First(&person).Error; err != nil {
		t.Fatalf("migrated person missing: %v", err)
	}
	if person.TMDBID != "6193" || !strings.Contains(person.ThumbPath, "image.tmdb.org") {
		t.Fatalf("person metadata not migrated: %+v", person)
	}
	var producer model.Person
	if err := db.Gorm.Where("name = ?", "艾玛·托马斯").First(&producer).Error; err != nil {
		t.Fatalf("producer missing: %v", err)
	}
	var producerLink model.ItemPerson
	if err := db.Gorm.Where("item_id = ? AND person_id = ? AND role_type = ?", movie.ID, producer.ID, "Producer").First(&producerLink).Error; err != nil {
		t.Fatalf("producer link missing: %v", err)
	}
	var personCount int64
	_ = db.Gorm.Model(&model.Person{}).Count(&personCount).Error
	if personCount != 7 {
		t.Fatalf("expected 7 imported people, got %d", personCount)
	}

	var episodeCast []model.ItemPerson
	if err := db.Gorm.Where("item_id = ?", ep.ID).Find(&episodeCast).Error; err != nil {
		t.Fatalf("load episode cast: %v", err)
	}
	if len(episodeCast) != 1 || episodeCast[0].RoleType != "Actor" {
		t.Fatalf("episode cast not migrated: %+v", episodeCast)
	}

	// Running again while item_people rows exist must not duplicate them.
	again, err := svc.Import(req)
	if err != nil {
		t.Fatalf("second Import failed: %v", err)
	}
	if again.ItemsSkipped != 8 || again.PeopleImported != 0 {
		t.Fatalf("unexpected second import report: %+v", again)
	}
	var peopleCount int64
	_ = db.Gorm.Model(&model.ItemPerson{}).Where("item_id = ?", movie.ID).Count(&peopleCount).Error
	if peopleCount != 7 {
		t.Fatalf("people rows were duplicated: %d", peopleCount)
	}

	// Removing a role added by this version and re-running must repair the gap
	// without touching the links that already exist.
	if err := db.Gorm.Where("item_id = ? AND person_id = ? AND role_type = ?", movie.ID, producer.ID, "Producer").Delete(&model.ItemPerson{}).Error; err != nil {
		t.Fatalf("delete producer link: %v", err)
	}
	missingRole, err := svc.Import(req)
	if err != nil {
		t.Fatalf("missing-role Import failed: %v", err)
	}
	if missingRole.PeopleImported != 1 {
		t.Fatalf("expected one repaired producer link, got %+v", missingRole)
	}
	var restoredProducer model.ItemPerson
	if err := db.Gorm.Where("item_id = ? AND person_id = ? AND role_type = ?", movie.ID, producer.ID, "Producer").First(&restoredProducer).Error; err != nil {
		t.Fatalf("producer link was not repaired: %v", err)
	}

	// Re-running an older migration without overwrite must repair all missing
	// people links for already-imported items instead of skipping them entirely.
	if err := db.Gorm.Exec("DELETE FROM item_people").Error; err != nil {
		t.Fatalf("clear item_people: %v", err)
	}
	if err := db.Gorm.Exec("DELETE FROM people").Error; err != nil {
		t.Fatalf("clear people: %v", err)
	}
	repair, err := svc.Import(req)
	if err != nil {
		t.Fatalf("repair Import failed: %v", err)
	}
	if repair.ItemsSkipped != 8 || repair.PeopleImported != 8 {
		t.Fatalf("unexpected repair report: %+v", repair)
	}
	var repaired []model.ItemPerson
	if err := db.Gorm.Where("item_id = ?", movie.ID).Order("sort_idx ASC").Find(&repaired).Error; err != nil {
		t.Fatalf("load repaired people: %v", err)
	}
	if len(repaired) != 7 {
		t.Fatalf("people links were not repaired: %+v", repaired)
	}

	// Older migrations created the boxsets library as "mixed" because
	// ItemExtradata was not read yet. A re-run must upgrade the kind.
	if err := db.Gorm.Model(&model.Library{}).Where("id = ?", boxsetLib.ID).Update("kind", "mixed").Error; err != nil {
		t.Fatalf("downgrade boxsets library: %v", err)
	}
	if _, err := svc.Import(req); err != nil {
		t.Fatalf("kind-upgrade Import failed: %v", err)
	}
	var upgradedLib model.Library
	if err := db.Gorm.First(&upgradedLib, "id = ?", boxsetLib.ID).Error; err != nil {
		t.Fatalf("load upgraded library: %v", err)
	}
	if upgradedLib.Kind != "boxsets" {
		t.Fatalf("boxsets library kind was not upgraded: %q", upgradedLib.Kind)
	}
}
