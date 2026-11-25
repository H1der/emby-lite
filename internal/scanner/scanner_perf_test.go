package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func TestConcurrentScanAndFastIncrementalSkip(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	mediaRoot := filepath.Join(tmp, "media")

	// Create 3 TV series x 2 seasons x 10 episodes (60 episodes) + 20 movies with NFOs, posters & subtitles.
	for s := 1; s <= 3; s++ {
		seriesDir := filepath.Join(mediaRoot, fmt.Sprintf("Test Series %d (2024)", s))
		if err := os.MkdirAll(seriesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tvNFO := fmt.Sprintf(`<tvshow><title>测试剧集 %d</title><year>2024</year><genre>科幻</genre><actor><name>演员A</name><role>主角</role></actor></tvshow>`, s)
		_ = os.WriteFile(filepath.Join(seriesDir, "tvshow.nfo"), []byte(tvNFO), 0o644)
		_ = os.WriteFile(filepath.Join(seriesDir, "poster.jpg"), []byte("fakejpg"), 0o644)

		for seas := 1; seas <= 2; seas++ {
			seasDir := filepath.Join(seriesDir, fmt.Sprintf("Season %02d", seas))
			_ = os.MkdirAll(seasDir, 0o755)
			for ep := 1; ep <= 10; ep++ {
				epPath := filepath.Join(seasDir, fmt.Sprintf("Show.S%02dE%02d.strm", seas, ep))
				_ = os.WriteFile(epPath, []byte("https://example.com/video.mkv\n"), 0o644)
				subPath := filepath.Join(seasDir, fmt.Sprintf("Show.S%02dE%02d.zh.srt", seas, ep))
				_ = os.WriteFile(subPath, []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o644)
			}
		}
	}

	movieRoot := filepath.Join(tmp, "movies")
	for m := 1; m <= 20; m++ {
		mDir := filepath.Join(movieRoot, fmt.Sprintf("Movie %02d (2023)", m))
		_ = os.MkdirAll(mDir, 0o755)
		_ = os.WriteFile(filepath.Join(mDir, fmt.Sprintf("Movie.%02d.2023.strm", m)), []byte("https://example.com/m.mp4\n"), 0o644)
		_ = os.WriteFile(filepath.Join(mDir, "movie.nfo"), []byte(fmt.Sprintf(`<movie><title>测试电影 %02d</title><year>2023</year><rating>8.5</rating><genre>动作</genre></movie>`, m)), 0o644)
		_ = os.WriteFile(filepath.Join(mDir, "poster.jpg"), []byte("fakejpg"), 0o644)
	}

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = dbPath
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	tvLib := model.Library{ID: "lib-tv", Name: "剧集", Kind: "tvshows", PathsJSON: fmt.Sprintf(`[%q]`, mediaRoot)}
	mvLib := model.Library{ID: "lib-mv", Name: "电影", Kind: "movies", PathsJSON: fmt.Sprintf(`[%q]`, movieRoot)}
	if err := db.Gorm.Create(&tvLib).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Gorm.Create(&mvLib).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewService(db, nil)
	svc.SetConcurrency(6)

	// 1. Cold scan TV (60 episodes across 3 series)
	tvStats, err := svc.ScanLibrary(context.Background(), tvLib.ID)
	if err != nil {
		t.Fatalf("tv cold scan failed: %v", err)
	}
	if tvStats.FilesFound != 60 || tvStats.ItemsAdded != 60 {
		t.Fatalf("unexpected tv cold scan stats: %+v", tvStats)
	}

	// 2. Incremental scan TV (0 changes -> all 60 skipped in memory!)
	tvInc, err := svc.ScanLibrary(context.Background(), tvLib.ID)
	if err != nil {
		t.Fatalf("tv inc scan failed: %v", err)
	}
	if tvInc.ItemsSkipped != 60 || tvInc.ItemsAdded != 0 || tvInc.ItemsUpdated != 0 || tvInc.ItemsRemoved != 0 {
		t.Fatalf("unexpected tv incremental scan stats: %+v", tvInc)
	}

	// 3. Cold scan Movies (20 movies)
	mvStats, err := svc.ScanLibrary(context.Background(), mvLib.ID)
	if err != nil {
		t.Fatalf("mv cold scan failed: %v", err)
	}
	if mvStats.FilesFound != 20 || mvStats.ItemsAdded != 20 {
		t.Fatalf("unexpected mv cold scan stats: %+v", mvStats)
	}

	// Verify series/season/episode counts via Read() pool!
	var seriesCnt, epCnt, subCnt int64
	db.Read().Model(&model.Item{}).Where("library_id = ? AND type = 'Series' AND soft_deleted_at IS NULL", tvLib.ID).Count(&seriesCnt)
	db.Read().Model(&model.Item{}).Where("library_id = ? AND type = 'Episode' AND soft_deleted_at IS NULL", tvLib.ID).Count(&epCnt)
	db.Read().Model(&model.MediaSubtitle{}).Count(&subCnt)
	if seriesCnt != 3 || epCnt != 60 || subCnt != 60 {
		t.Fatalf("unexpected counts: series=%d ep=%d sub=%d", seriesCnt, epCnt, subCnt)
	}

	t.Logf("TV cold scan (60 eps): %dms | TV incremental skip (60 eps): %dms | Movies cold scan (20): %dms",
		tvStats.DurationMs, tvInc.DurationMs, mvStats.DurationMs)
}
