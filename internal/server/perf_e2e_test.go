package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

func TestWebHomeBFFGzipAndHotCacheInvalidation(t *testing.T) {
	tmp := t.TempDir()
	mediaRoot := filepath.Join(tmp, "movies")
	for i := 1; i <= 30; i++ {
		dir := filepath.Join(mediaRoot, fmt.Sprintf("Movie %02d (2024)", i))
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("Movie.%02d.2024.strm", i)), []byte("https://cdn.example.com/v.mp4\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(fmt.Sprintf(`<movie><title>星际电影 %02d</title><year>2024</year><rating>8.8</rating><genre>科幻</genre></movie>`, i)), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("jpg"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "fanart.jpg"), []byte("jpg"), 0o644)
	}

	cfg := config.Default()
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "emby.db")
	cfg.Storage.DataDir = tmp
	cfg.Storage.CacheDir = filepath.Join(tmp, "cache")
	cfg.Storage.BackupDir = filepath.Join(tmp, "backups")
	cfg.Scanner.Concurrency = 6

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cs := cache.NewMemoryStore(1024)
	defer cs.Close()

	srv := New(cfg, db, cs)
	defer srv.Close()

	// Setup admin user & login
	user, err := srv.Auth.CreateInitialAdmin("admin", "pass1234", "GlassTest")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := srv.Auth.AuthenticateByName(context.Background(), "admin", "pass1234", auth.ClientMeta{
		Client: "PerfTest", Device: "Browser", DeviceID: "perf-dev-1", Version: "1.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	lib := model.Library{ID: "lib-1", Name: "科幻电影", Kind: "movies", PathsJSON: fmt.Sprintf(`[%q]`, mediaRoot)}
	if err := db.Gorm.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := srv.Scanner.ScanLibrary(context.Background(), lib.ID)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if stats.ItemsAdded != 30 {
		t.Fatalf("expected 30 movies added, got %+v", stats)
	}

	// 1. Test /api/web/home with gzip compression
	req := httptest.NewRequest(http.MethodGet, "/api/web/home", nil)
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	startCold := time.Now()
	srv.Handler.ServeHTTP(rec, req)
	coldDur := time.Since(startCold)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding, got %q", rec.Header().Get("Content-Encoding"))
	}
	gzr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	raw, _ := io.ReadAll(gzr)
	_ = gzr.Close()

	var home struct {
		Views    []map[string]any `json:"views"`
		Latest   []map[string]any `json:"latest"`
		Featured []map[string]any `json:"featured"`
		Counts   map[string]int   `json:"counts"`
	}
	if err := json.Unmarshal(raw, &home); err != nil {
		t.Fatalf("unmarshal home: %v", err)
	}
	if len(home.Views) != 1 || len(home.Latest) != 24 || len(home.Featured) != 6 || home.Counts["MovieCount"] != 30 {
		t.Fatalf("unexpected web home payload: views=%d latest=%d featured=%d counts=%v", len(home.Views), len(home.Latest), len(home.Featured), home.Counts)
	}

	// 2. Second request hits CacheStore hot cache
	req2 := httptest.NewRequest(http.MethodGet, "/api/web/home", nil)
	req2.Header.Set("X-Emby-Token", token)
	rec2 := httptest.NewRecorder()
	startWarm := time.Now()
	srv.Handler.ServeHTTP(rec2, req2)
	warmDur := time.Since(startWarm)
	if rec2.Code != http.StatusOK {
		t.Fatalf("warm home expected 200, got %d", rec2.Code)
	}

	// 3. Toggle favorite on first item -> must immediately invalidate user's cached /api/web/home!
	firstID := home.Latest[0]["Id"].(string)
	favReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/emby/Users/%s/FavoriteItems/%s", user.ID, firstID), nil)
	favReq.Header.Set("X-Emby-Token", token)
	favRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(favRec, favReq)
	if favRec.Code != http.StatusOK {
		t.Fatalf("favorite expected 200, got %d", favRec.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/web/home", nil)
	req3.Header.Set("X-Emby-Token", token)
	rec3 := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec3, req3)
	var home3 struct {
		Latest []map[string]any `json:"latest"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &home3)
	ud, _ := home3.Latest[0]["UserData"].(map[string]any)
	if fav, _ := ud["IsFavorite"].(bool); !fav {
		t.Fatalf("expected IsFavorite=true after cache invalidation, got %+v", ud)
	}

	// 4. Test batch GetItems (30 items)
	listReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/emby/Users/%s/Items?ParentId=%s&Limit=60", user.ID, lib.ID), nil)
	listReq.Header.Set("X-Emby-Token", token)
	listRec := httptest.NewRecorder()
	startList := time.Now()
	srv.Handler.ServeHTTP(listRec, listReq)
	listDur := time.Since(startList)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GetItems expected 200, got %d", listRec.Code)
	}

	t.Logf("Scan(30 movies): %dms | WebHome cold: %v | WebHome cached: %v | GetItems(30 batched): %v | Gzip ratio: %d -> %d bytes",
		stats.DurationMs, coldDur, warmDur, listDur, len(raw), rec.Body.Len())
}
