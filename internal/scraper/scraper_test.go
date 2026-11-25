package scraper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func TestTMDBScrapeCacheLocksAndManualQueue(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "scraper.db")

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var apiHits atomic.Int32
	mockTMDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/configuration":
			_, _ = w.Write([]byte(`{"images":{"base_url":"https://image.tmdb.org/t/p/"}}`))
		case "/search/movie":
			q := r.URL.Query().Get("query")
			if q == "盗梦空间" {
				_, _ = w.Write([]byte(`{"results":[{"id":27205,"title":"盗梦空间","original_title":"Inception","release_date":"2010-07-16","overview":"梦中梦","poster_path":"/p.jpg","vote_average":8.4}]}`))
			} else {
				// Low confidence mismatch for manual queue test
				_, _ = w.Write([]byte(`{"results":[{"id":99999,"title":"Totally Different Film","original_title":"Other","release_date":"1998-01-01","overview":"x","poster_path":"/o.jpg","vote_average":6.0}]}`))
			}
		case "/movie/27205":
			_, _ = w.Write([]byte(`{
				"id":27205,"imdb_id":"tt1375666","title":"盗梦空间","original_title":"Inception",
				"overview":"潜入他人梦境窃取机密。","tagline":"你的思想就是犯罪现场","release_date":"2010-07-16",
				"vote_average":8.4,"runtime":148,"poster_path":"/poster.jpg","backdrop_path":"/bg.jpg",
				"genres":[{"id":878,"name":"科幻"},{"id":28,"name":"动作"}],
				"credits":{"cast":[{"id":6193,"name":"莱昂纳多·迪卡普里奥","character":"Cobb","profile_path":"/leo.jpg"}]}
			}`))
		case "/movie/99999":
			_, _ = w.Write([]byte(`{
				"id":99999,"title":"手工选定的影片","original_title":"Manual Pick","overview":"手工确认简介",
				"release_date":"2020-01-01","vote_average":7.5,"poster_path":"/manual.jpg"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockTMDB.Close()

	svc := NewService(db)
	if err := svc.SaveConfig(TMDBConfig{
		APIKey:        "secret-api-key-123",
		APIBaseURL:    mockTMDB.URL,
		ImageBaseURL:  mockTMDB.URL + "/img",
		Language:      "zh-CN",
		AutoThreshold: 75,
		WriteNFO:      true,
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Verify secrets are write-only in public config
	pub := svc.GetPublicConfig()
	if pub["has_api_key"] != true {
		t.Fatalf("expected has_api_key=true, got %+v", pub)
	}
	if _, leaked := pub["api_key"]; leaked {
		t.Fatal("api_key must never be echoed in public config")
	}

	if err := svc.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection failed: %v", err)
	}

	// Seed two movie items (one high-confidence, one low-confidence)
	movieDir := filepath.Join(tmp, "Inception")
	_ = os.MkdirAll(movieDir, 0o755)
	highItem := model.Item{
		ID:        "item-high",
		LibraryID: "lib-1",
		ParentID:  "lib-1",
		Type:      "Movie",
		Name:      "盗梦空间",
		SortName:  "盗梦空间",
		Year:      2010,
		DirPath:   movieDir,
		ScanKey:   "movie:lib-1:inception",
		AddedAt:   time.Now().UTC(),
	}
	lowItem := model.Item{
		ID:        "item-low",
		LibraryID: "lib-1",
		ParentID:  "lib-1",
		Type:      "Movie",
		Name:      "模糊代号X",
		SortName:  "模糊代号x",
		Year:      2024,
		ScanKey:   "movie:lib-1:ambiguous",
		AddedAt:   time.Now().UTC(),
	}
	_ = db.Gorm.Create(&highItem).Error
	_ = db.Gorm.Create(&lowItem).Error

	// 1. High-confidence item auto-matches & writes NFO
	outcome, err := svc.ScrapeItem(context.Background(), highItem.ID, true)
	if err != nil || outcome != "matched" {
		t.Fatalf("expected matched for highItem, got outcome=%s err=%v", outcome, err)
	}
	var updated model.Item
	_ = db.Gorm.Where("id = ?", highItem.ID).First(&updated).Error
	if updated.Overview != "潜入他人梦境窃取机密。" || updated.Tagline != "你的思想就是犯罪现场" {
		t.Fatalf("expected TMDB metadata applied, got %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(movieDir, "movie.nfo")); err != nil {
		t.Fatalf("expected NFO written back: %v", err)
	}

	// 2. Re-scrape hits provider_cache with ZERO additional TMDB HTTP calls!
	hitsBefore := apiHits.Load()
	customTitle := "我的自定义标题"
	customOverview := "人工锁定简介"
	if err := svc.UpdateItemMetadata(highItem.ID, MetadataUpdatePayload{
		Name:         &customTitle,
		Overview:     &customOverview,
		LockedFields: []string{"name", "overview"},
	}); err != nil {
		t.Fatalf("UpdateItemMetadata failed: %v", err)
	}
	if _, err := svc.ScrapeItem(context.Background(), highItem.ID, true); err != nil {
		t.Fatalf("re-scrape failed: %v", err)
	}
	if apiHits.Load() != hitsBefore {
		t.Fatalf("expected zero TMDB HTTP calls on cached re-scrape, got delta=%d", apiHits.Load()-hitsBefore)
	}
	_ = db.Gorm.Where("id = ?", highItem.ID).First(&updated).Error
	if updated.Name != customTitle || updated.Overview != customOverview {
		t.Fatalf("locked fields were overwritten! got name=%q overview=%q", updated.Name, updated.Overview)
	}

	// 3. Low-confidence item enters manual match queue & can be manually resolved
	outcomeLow, err := svc.ScrapeItem(context.Background(), lowItem.ID, true)
	if err != nil || outcomeLow != "queued" {
		t.Fatalf("expected queued for lowItem, got outcome=%s err=%v", outcomeLow, err)
	}
	qItems, err := svc.ListMatchQueue("pending")
	if err != nil || len(qItems) != 1 {
		t.Fatalf("expected 1 pending queue item, got %d err=%v", len(qItems), err)
	}
	if err := svc.ResolveManualMatch(context.Background(), lowItem.ID, "movie", 99999); err != nil {
		t.Fatalf("ResolveManualMatch failed: %v", err)
	}
	var resolved model.Item
	_ = db.Gorm.Where("id = ?", lowItem.ID).First(&resolved).Error
	if resolved.Name != "手工选定的影片" {
		t.Fatalf("expected manual match applied, got %+v", resolved)
	}
	var pids map[string]string
	_ = json.Unmarshal([]byte(resolved.ProviderIDsJSON), &pids)
	if pids["Tmdb"] != "99999" {
		t.Fatalf("expected Tmdb provider id 99999, got %+v", pids)
	}
}
