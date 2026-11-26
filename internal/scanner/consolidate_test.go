package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func openTestScannerDB(t *testing.T) *database.DB {
	t.Helper()
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(t.TempDir(), "test.db")
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestParseSeasonDirChineseAndSpecials(t *testing.T) {
	cases := []struct {
		dir  string
		want int
	}{
		{"Season 01", 1},
		{"Season 2 (2022)", 2},
		{"S03", 3},
		{"第1季", 1},
		{"第 02 季", 2},
		{"第一季", 1},
		{"第三季", 3},
		{"第十季", 10},
		{"第十二季", 12},
		{"第二十一季", 21},
		{"Specials", 0},
		{"特别篇", 0},
		{"番外", 0},
		{"OVA", 0},
		{"RandomFolder", -1},
	}
	for _, tc := range cases {
		if got := ParseSeasonDir(tc.dir); got != tc.want {
			t.Errorf("ParseSeasonDir(%q) = %d, want %d", tc.dir, got, tc.want)
		}
	}
}

func TestAutoMergeMultiVersionMoviesAndIncrementalDelete(t *testing.T) {
	db := openTestScannerDB(t)
	root1 := filepath.Join(t.TempDir(), "movies-4k")
	root2 := filepath.Join(t.TempDir(), "movies-1080p")

	// Case 1: Same-folder Emby multi-version naming ("Title (Year) - Version.ext")
	incDir := filepath.Join(root1, "Inception (2010)")
	_ = os.MkdirAll(incDir, 0o755)
	f4k := filepath.Join(incDir, "Inception (2010) - 4K HDR.strm")
	f1080 := filepath.Join(incDir, "Inception (2010) - 1080p.strm")
	_ = os.WriteFile(f4k, []byte("https://example.com/inc-4k.mkv\n"), 0o644)
	_ = os.WriteFile(f1080, []byte("https://example.com/inc-1080.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(incDir, "movie.nfo"), []byte(`<movie><title>盗梦空间</title><year>2010</year><uniqueid type="tmdb">27205</uniqueid></movie>`), 0o644)

	// Case 2: Cross-folder multi-version movie sharing TMDB ID
	duneDir1 := filepath.Join(root1, "Dune (2021)")
	duneDir2 := filepath.Join(root2, "沙丘")
	_ = os.MkdirAll(duneDir1, 0o755)
	_ = os.MkdirAll(duneDir2, 0o755)
	_ = os.WriteFile(filepath.Join(duneDir1, "Dune.2021.2160p.DV.HEVC.strm"), []byte("https://example.com/dune-4k.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(duneDir1, "movie.nfo"), []byte(`<movie><title>沙丘</title><year>2021</year><uniqueid type="tmdb">438631</uniqueid></movie>`), 0o644)
	_ = os.WriteFile(filepath.Join(duneDir2, "Dune.1080p.H264.strm"), []byte("https://example.com/dune-1080.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(duneDir2, "movie.nfo"), []byte(`<movie><title>沙丘</title><year>2021</year><uniqueid type="tmdb">438631</uniqueid></movie>`), 0o644)

	pathsJSON, _ := json.Marshal([]string{root1, root2})
	lib := model.Library{
		ID:                "lib-mv",
		Name:              "电影",
		Kind:              "movies",
		PathsJSON:         string(pathsJSON),
		AutoMergeVersions: true,
		AutoGroupSeries:   true,
	}
	if err := db.Gorm.Select("*").Create(&lib).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, nil)
	stats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if stats.MoviesMerged != 2 {
		t.Fatalf("expected 2 duplicate movie versions merged, got %+v", stats)
	}

	var movies []model.Item
	db.Read().Where("library_id = ? AND type = 'Movie' AND soft_deleted_at IS NULL", lib.ID).Find(&movies)
	if len(movies) != 2 {
		t.Fatalf("expected 2 merged movies, got %d", len(movies))
	}

	for _, mv := range movies {
		var srcs []model.MediaSource
		db.Read().Where("item_id = ?", mv.ID).Find(&srcs)
		if len(srcs) != 2 {
			t.Fatalf("expected movie %q to have 2 MediaSources, got %d", mv.Name, len(srcs))
		}
		for _, s := range srcs {
			if s.VersionLabel == "" {
				t.Errorf("expected non-empty VersionLabel on %q (%s)", mv.Name, s.Path)
			}
		}
	}

	// Incremental rescan with no changes should skip all 4 files cleanly.
	incStats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("incremental scan failed: %v", err)
	}
	if incStats.ItemsSkipped != 4 || incStats.ItemsAdded != 0 || incStats.ItemsRemoved != 0 {
		t.Fatalf("unexpected incremental scan stats: %+v", incStats)
	}

	// Delete ONE version of Inception: the Movie must remain alive with 1 MediaSource!
	if err := os.Remove(f1080); err != nil {
		t.Fatal(err)
	}
	delStats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("post-delete scan failed: %v", err)
	}
	if delStats.ItemsRemoved != 0 {
		t.Fatalf("expected Movie not to be soft-deleted while 4K version remains, got removed=%d", delStats.ItemsRemoved)
	}
	var incMovie model.Item
	if err := db.Read().Where("library_id = ? AND name = '盗梦空间' AND soft_deleted_at IS NULL", lib.ID).First(&incMovie).Error; err != nil {
		t.Fatalf("Inception should still be active: %v", err)
	}
	var remSrcs []model.MediaSource
	db.Read().Where("item_id = ?", incMovie.ID).Find(&remSrcs)
	if len(remSrcs) != 1 || remSrcs[0].Path != f4k {
		t.Fatalf("expected only 4K source to remain, got %+v", remSrcs)
	}
}

func TestAutoGroupCrossFolderSeriesAndMultiVersionEpisodes(t *testing.T) {
	db := openTestScannerDB(t)
	disk1 := filepath.Join(t.TempDir(), "disk1")
	disk2 := filepath.Join(t.TempDir(), "disk2")

	// Folder 1: Season 1 (Chinese dir name "第一季") + S01E01 4K
	s1Dir := filepath.Join(disk1, "三体")
	_ = os.MkdirAll(filepath.Join(s1Dir, "第一季"), 0o755)
	_ = os.WriteFile(filepath.Join(s1Dir, "tvshow.nfo"), []byte(`<tvshow><title>三体</title><year>2023</year><uniqueid type="tmdb">123456</uniqueid></tvshow>`), 0o644)
	_ = os.WriteFile(filepath.Join(s1Dir, "第一季", "ThreeBody.S01E01.2160p.HEVC.strm"), []byte("https://example.com/s01e01-4k.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(s1Dir, "第一季", "ThreeBody.S01E02.2160p.HEVC.strm"), []byte("https://example.com/s01e02-4k.mkv\n"), 0o644)

	// Folder 2 (separate root disk): Season 2 + another 1080p version of S01E01!
	s2Dir := filepath.Join(disk2, "三体 第二季")
	_ = os.MkdirAll(filepath.Join(s2Dir, "Season 01"), 0o755)
	_ = os.MkdirAll(filepath.Join(s2Dir, "Season 02"), 0o755)
	_ = os.WriteFile(filepath.Join(s2Dir, "tvshow.nfo"), []byte(`<tvshow><title>三体</title><year>2023</year><uniqueid type="tmdb">123456</uniqueid></tvshow>`), 0o644)
	_ = os.WriteFile(filepath.Join(s2Dir, "Season 01", "ThreeBody.S01E01.1080p.H264.strm"), []byte("https://example.com/s01e01-1080.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(s2Dir, "Season 02", "ThreeBody.S02E01.1080p.H264.strm"), []byte("https://example.com/s02e01-1080.mkv\n"), 0o644)

	pathsJSON, _ := json.Marshal([]string{disk1, disk2})
	lib := model.Library{
		ID:                "lib-tv",
		Name:              "剧集",
		Kind:              "tvshows",
		PathsJSON:         string(pathsJSON),
		AutoMergeVersions: true,
		AutoGroupSeries:   true,
	}
	if err := db.Gorm.Select("*").Create(&lib).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, nil)
	stats, err := svc.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("tv scan failed: %v", err)
	}
	if stats.SeriesMerged != 1 || stats.SeasonsMerged != 1 || stats.EpisodesMerged != 1 {
		t.Fatalf("expected 1 series, 1 season, 1 episode merged, got %+v", stats)
	}

	var series []model.Item
	db.Read().Where("library_id = ? AND type = 'Series' AND soft_deleted_at IS NULL", lib.ID).Find(&series)
	if len(series) != 1 {
		t.Fatalf("expected 1 grouped Series, got %d", len(series))
	}

	var seasons []model.Item
	db.Read().Where("series_id = ? AND type = 'Season' AND soft_deleted_at IS NULL", series[0].ID).Order("season_num ASC").Find(&seasons)
	if len(seasons) != 2 || seasons[0].SeasonNum != 1 || seasons[1].SeasonNum != 2 {
		t.Fatalf("expected Season 1 & Season 2 under grouped Series, got %+v", seasons)
	}

	var eps []model.Item
	db.Read().Where("series_id = ? AND type = 'Episode' AND soft_deleted_at IS NULL", series[0].ID).Order("season_num ASC, episode_num ASC").Find(&eps)
	if len(eps) != 3 {
		t.Fatalf("expected 3 unique episodes (S01E01 merged, S01E02, S02E01), got %d", len(eps))
	}

	var s1e1Srcs []model.MediaSource
	db.Read().Where("item_id = ?", eps[0].ID).Find(&s1e1Srcs)
	if len(s1e1Srcs) != 2 {
		t.Fatalf("expected S01E01 to have 2 MediaSources, got %d", len(s1e1Srcs))
	}
}

func TestManualSplitAndMergeVersionsRespectsDisableAutoMerge(t *testing.T) {
	db := openTestScannerDB(t)
	root := filepath.Join(t.TempDir(), "movies")
	mDir := filepath.Join(root, "Matrix (1999)")
	_ = os.MkdirAll(mDir, 0o755)
	_ = os.WriteFile(filepath.Join(mDir, "Matrix (1999) - 4K.strm"), []byte("https://example.com/m4k.mkv\n"), 0o644)
	_ = os.WriteFile(filepath.Join(mDir, "Matrix (1999) - 1080p.strm"), []byte("https://example.com/m1080.mkv\n"), 0o644)

	pathsJSON, _ := json.Marshal([]string{root})
	lib := model.Library{
		ID:                "lib-split",
		Name:              "电影",
		Kind:              "movies",
		PathsJSON:         string(pathsJSON),
		AutoMergeVersions: true,
		AutoGroupSeries:   true,
	}
	_ = db.Gorm.Select("*").Create(&lib).Error

	svc := NewService(db, nil)
	if _, err := svc.ScanLibrary(context.Background(), lib.ID); err != nil {
		t.Fatal(err)
	}

	var movies []model.Item
	db.Read().Where("library_id = ? AND type = 'Movie' AND soft_deleted_at IS NULL", lib.ID).Find(&movies)
	if len(movies) != 1 {
		t.Fatalf("expected 1 merged movie before split, got %d", len(movies))
	}

	// Split manually -> 2 separate movies, marked DisableAutoMerge=true
	splitIDs, err := SplitItemVersions(db, movies[0].ID)
	if err != nil {
		t.Fatalf("SplitItemVersions failed: %v", err)
	}
	if len(splitIDs) != 2 {
		t.Fatalf("expected 2 split IDs, got %v", splitIDs)
	}

	// ConsolidateLibrary must NOT re-merge manually split items.
	cStats, err := ConsolidateLibrary(db, lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cStats.MoviesMerged != 0 {
		t.Fatalf("manual split should not be auto-remerged, got %+v", cStats)
	}

	// Manual merge brings them back together and clears DisableAutoMerge.
	mergedID, err := MergeItemsManual(db, splitIDs)
	if err != nil {
		t.Fatalf("MergeItemsManual failed: %v", err)
	}
	var srcs []model.MediaSource
	db.Read().Where("item_id = ?", mergedID).Find(&srcs)
	if len(srcs) != 2 {
		t.Fatalf("expected 2 sources after manual merge, got %d", len(srcs))
	}
}
