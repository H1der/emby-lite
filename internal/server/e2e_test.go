package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"
	"emby-lite/internal/server"
)

func setupTestServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Server.Mode = "release"
	cfg.Database.Type = "sqlite"
	cfg.Database.SQLitePath = filepath.Join(tmp, "test.db")
	cfg.Storage.DataDir = tmp
	cfg.Storage.CacheDir = filepath.Join(tmp, "cache")
	cfg.Storage.BackupDir = filepath.Join(tmp, "backups")

	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cs := cache.NewMemoryStore(512)
	t.Cleanup(func() { _ = cs.Close() })

	srv := server.New(cfg, db, cs)
	t.Cleanup(func() { srv.Close() })
	return srv, tmp
}

func doJSON(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Infuse", Device="AppleTV", DeviceId="dev-001", Version="7.8"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestEmbyEndToEndPlaybackAndPermissions(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	// 1. Health & Bootstrap before setup
	rec := doJSON(t, h, http.MethodGet, "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/api/web/bootstrap", "", nil)
	if !strings.Contains(rec.Body.String(), `"initialized":false`) {
		t.Fatalf("expected initialized=false, got %s", rec.Body.String())
	}

	// 2. Setup initial admin
	rec = doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "My NAS Emby",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	// 3. Emby client login via /emby/Users/AuthenticateByName (mixed case!)
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "SecretPass123!",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("emby login failed: %d %s", rec.Code, rec.Body.String())
	}
	var loginResp struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("parse login resp: %v", err)
	}
	if loginResp.AccessToken == "" || loginResp.User.ID == "" {
		t.Fatalf("missing token or user id: %s", rec.Body.String())
	}
	adminTok := loginResp.AccessToken
	adminID := loginResp.User.ID

	// 4. Create sample media directories: local MP4 + NFO + poster + SRT + STRM movie + TV Series
	moviesDir := filepath.Join(tmp, "media", "Movies")
	movie1Dir := filepath.Join(moviesDir, "Interstellar (2014)")
	if err := os.MkdirAll(movie1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoContent := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ-FAKE-MP4-DATA")
	if err := os.WriteFile(filepath.Join(movie1Dir, "Interstellar (2014).mp4"), videoContent, 0o644); err != nil {
		t.Fatal(err)
	}
	nfoXML := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>星际穿越</title>
  <originaltitle>Interstellar</originaltitle>
  <year>2014</year>
  <rating>8.7</rating>
  <runtime>169</runtime>
  <plot>穿越虫洞寻找人类新家园。</plot>
  <genre>科幻</genre>
  <genre>冒险</genre>
  <actor><name>Matthew McConaughey</name><role>Cooper</role></actor>
  <tmdbid>157336</tmdbid>
</movie>`
	if err := os.WriteFile(filepath.Join(movie1Dir, "Interstellar (2014).nfo"), []byte(nfoXML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(movie1Dir, "poster.jpg"), []byte("\xff\xd8\xff\xe0FAKEJPEG"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(movie1Dir, "Interstellar (2014).zh.srt"), []byte("1\n00:00:01,000 --> 00:00:03,000\n你好宇宙\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// STRM movie
	strmDir := filepath.Join(moviesDir, "Cloud Movie (2025)")
	if err := os.MkdirAll(strmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(strmDir, "Cloud Movie (2025).mkv.strm"), []byte("https://cdn.example.com/videos/cloud-movie.mkv?sign=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TV library
	tvDir := filepath.Join(tmp, "media", "TV")
	seasonDir := filepath.Join(tvDir, "Severance (2022)", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seasonDir, "Severance.S01E01.Good.News.About.Hell.mkv"), []byte("FAKE-MKV-EP1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Create Movies & TV libraries via Admin API and run sync scan
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", adminTok, map[string]any{
		"name":  "电影",
		"kind":  "movies",
		"paths": []string{moviesDir},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create movies lib failed: %d %s", rec.Code, rec.Body.String())
	}
	var movieLib struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &movieLib)

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", adminTok, map[string]any{
		"name":  "剧集",
		"kind":  "tvshows",
		"paths": []string{tvDir},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tv lib failed: %d %s", rec.Code, rec.Body.String())
	}
	var tvLib struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tvLib)

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries/"+movieLib.ID+"/scan?sync=true", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan movies failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries/"+tvLib.ID+"/scan?sync=true", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan tv failed: %d %s", rec.Code, rec.Body.String())
	}

	// 6. Emby Views & Items browse + pinyin search
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Views", adminTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "电影") || !strings.Contains(rec.Body.String(), "剧集") {
		t.Fatalf("views mismatch: %d %s", rec.Code, rec.Body.String())
	}

	// Search by Chinese pinyin initials "xjcy" -> 星际穿越
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Items?Recursive=true&SearchTerm=xjcy", adminTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "星际穿越") {
		t.Fatalf("pinyin search failed: %d %s", rec.Code, rec.Body.String())
	}

	// List Movies items to get local & strm IDs
	rec = doJSON(t, h, http.MethodGet, "/Items?ParentId="+movieLib.ID, adminTok, nil)
	var listResp struct {
		Items []struct {
			ID        string            `json:"Id"`
			Name      string            `json:"Name"`
			ImageTags map[string]string `json:"ImageTags"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Items) != 2 {
		t.Fatalf("expected 2 movies, got %d (%s)", len(listResp.Items), rec.Body.String())
	}
	var localMovieID, strmMovieID, posterTag string
	for _, it := range listResp.Items {
		if it.Name == "星际穿越" {
			localMovieID = it.ID
			posterTag = it.ImageTags["Primary"]
		} else {
			strmMovieID = it.ID
		}
	}
	if localMovieID == "" || strmMovieID == "" {
		t.Fatalf("failed to identify scanned movies: %+v", listResp.Items)
	}

	// 7. Image endpoint + ETag 304
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+localMovieID+"/Images/Primary?tag="+posterTag, adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for poster image, got %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	req304 := httptest.NewRequest(http.MethodGet, "/emby/Items/"+localMovieID+"/Images/Primary?tag="+posterTag, nil)
	req304.Header.Set("X-Emby-Token", adminTok)
	req304.Header.Set("If-None-Match", etag)
	rec304 := httptest.NewRecorder()
	h.ServeHTTP(rec304, req304)
	if rec304.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on ETag match, got %d", rec304.Code)
	}

	// 8. PlaybackInfo + Subtitle + Local Range 206 + STRM 302 + Reject Transcode 400
	rec = doJSON(t, h, http.MethodPost, "/emby/Items/"+localMovieID+"/PlaybackInfo", adminTok, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo failed: %d %s", rec.Code, rec.Body.String())
	}
	var pbResp struct {
		PlaySessionID string `json:"PlaySessionId"`
		MediaSources  []struct {
			ID                  string `json:"Id"`
			DirectStreamURL     string `json:"DirectStreamUrl"`
			SupportsDirectPlay  bool   `json:"SupportsDirectPlay"`
			SupportsTranscoding bool   `json:"SupportsTranscoding"`
			MediaStreams        []struct {
				Type        string `json:"Type"`
				DeliveryURL string `json:"DeliveryUrl"`
			} `json:"MediaStreams"`
		} `json:"MediaSources"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pbResp)
	if len(pbResp.MediaSources) != 1 || !pbResp.MediaSources[0].SupportsDirectPlay || pbResp.MediaSources[0].SupportsTranscoding {
		t.Fatalf("unexpected MediaSources: %s", rec.Body.String())
	}
	// Check subtitle delivery
	var subURL string
	for _, ms := range pbResp.MediaSources[0].MediaStreams {
		if ms.Type == "Subtitle" && ms.DeliveryURL != "" {
			subURL = ms.DeliveryURL
		}
	}
	if subURL == "" {
		t.Fatalf("expected external subtitle in MediaStreams: %s", rec.Body.String())
	}
	recSub := doJSON(t, h, http.MethodGet, subURL, adminTok, nil)
	if recSub.Code != http.StatusOK || !strings.Contains(recSub.Body.String(), "你好宇宙") {
		t.Fatalf("subtitle fetch failed: %d %s", recSub.Code, recSub.Body.String())
	}

	// Range 206 local direct stream
	rangeReq := httptest.NewRequest(http.MethodGet, pbResp.MediaSources[0].DirectStreamURL, nil)
	rangeReq.Header.Set("Range", "bytes=0-9")
	rangeRec := httptest.NewRecorder()
	h.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent || rangeRec.Body.String() != "0123456789" {
		t.Fatalf("expected 206 range bytes 0-9, got %d %q", rangeRec.Code, rangeRec.Body.String())
	}

	// STRM 302 redirect
	recStrm := doJSON(t, h, http.MethodGet, "/emby/Videos/"+strmMovieID+"/stream.mkv?Static=true", adminTok, nil)
	if recStrm.Code != http.StatusFound || recStrm.Header().Get("Location") != "https://cdn.example.com/videos/cloud-movie.mkv?sign=abc" {
		t.Fatalf("expected 302 redirect for STRM, got %d loc=%q", recStrm.Code, recStrm.Header().Get("Location"))
	}

	// Reject HLS transcode with 400
	recHLS := doJSON(t, h, http.MethodGet, "/emby/Videos/"+localMovieID+"/master.m3u8", adminTok, nil)
	if recHLS.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for transcode path, got %d", recHLS.Code)
	}

	// 9. Progress & Resume
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Progress", adminTok, map[string]any{
		"ItemId":        localMovieID,
		"MediaSourceId": pbResp.MediaSources[0].ID,
		"PlaySessionId": pbResp.PlaySessionID,
		"PositionTicks": int64(3600 * 1e7),
		"IsPaused":      false,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("progress report failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Items/Resume", adminTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), localMovieID) {
		t.Fatalf("expected movie in resume list: %d %s", rec.Code, rec.Body.String())
	}

	// 10. Regular user library isolation & disabled Download (403)
	canDL := false
	rec = doJSON(t, h, http.MethodPost, "/api/admin/users", adminTok, map[string]any{
		"username":     "alice",
		"password":     "AlicePass123!",
		"is_admin":     false,
		"can_download": &canDL,
		"library_ids":  []string{movieLib.ID}, // only Movies, NOT TV!
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user alice failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "alice",
		"Pw":       "AlicePass123!",
	})
	var aliceLogin struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &aliceLogin)

	// Alice sees Movies but NOT TV
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+aliceLogin.User.ID+"/Views", aliceLogin.AccessToken, nil)
	if !strings.Contains(rec.Body.String(), "电影") || strings.Contains(rec.Body.String(), "剧集") {
		t.Fatalf("library isolation failed for alice: %s", rec.Body.String())
	}
	// Alice cannot download when can_download=false
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+localMovieID+"/Download", aliceLogin.AccessToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disabled download, got %d", rec.Code)
	}

	// 11. Admin directory picker API
	rec = doJSON(t, h, http.MethodGet, "/api/admin/directories?path="+filepath.ToSlash(filepath.Join(tmp, "media")), adminTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Movies") || !strings.Contains(rec.Body.String(), "TV") {
		t.Fatalf("directory browse failed: %d %s", rec.Code, rec.Body.String())
	}
	// Non-admin user must be denied access to directory browser
	rec = doJSON(t, h, http.MethodGet, "/api/admin/directories", aliceLogin.AccessToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin directory browse, got %d", rec.Code)
	}

	// 12. Signed cursor pagination + tampered cursor rejection
	recP1 := doJSON(t, h, http.MethodGet, "/Items?ParentId="+movieLib.ID+"&Limit=1&SortBy=SortName", adminTok, nil)
	if recP1.Code != http.StatusOK {
		t.Fatalf("cursor page 1 failed: %d %s", recP1.Code, recP1.Body.String())
	}
	var p1 struct {
		Items      []map[string]any `json:"Items"`
		NextCursor string           `json:"NextCursor"`
		HasMore    bool             `json:"HasMore"`
	}
	_ = json.Unmarshal(recP1.Body.Bytes(), &p1)
	if !p1.HasMore || p1.NextCursor == "" || recP1.Header().Get("X-Next-Cursor") != p1.NextCursor {
		t.Fatalf("expected signed NextCursor on page 1: %s", recP1.Body.String())
	}
	recP2 := doJSON(t, h, http.MethodGet, "/Items?ParentId="+movieLib.ID+"&Limit=1&SortBy=SortName&Cursor="+p1.NextCursor, adminTok, nil)
	if recP2.Code != http.StatusOK || !strings.Contains(recP2.Body.String(), `"HasMore":false`) {
		t.Fatalf("cursor page 2 failed: %d %s", recP2.Code, recP2.Body.String())
	}
	recBadCur := doJSON(t, h, http.MethodGet, "/Items?ParentId="+movieLib.ID+"&Cursor=tampered.deadbeef", adminTok, nil)
	if recBadCur.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for tampered cursor, got %d", recBadCur.Code)
	}

	// 13. Scanner v2 move detection: move movie to new folder & verify ItemID + watch progress survive!
	movedDir := filepath.Join(moviesDir, "SciFi", "Interstellar (2014)")
	if err := os.MkdirAll(movedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(movie1Dir, "Interstellar (2014).mp4"), filepath.Join(movedDir, "Interstellar (2014).mp4")); err != nil {
		t.Fatal(err)
	}
	recMoveScan := doJSON(t, h, http.MethodPost, "/api/admin/libraries/"+movieLib.ID+"/scan?sync=true", adminTok, nil)
	if recMoveScan.Code != http.StatusOK || !strings.Contains(recMoveScan.Body.String(), `"items_moved":1`) {
		t.Fatalf("expected items_moved=1 after relocation, got %d %s", recMoveScan.Code, recMoveScan.Body.String())
	}
	// Verify watch progress on original localMovieID is intact!
	recDetailAfterMove := doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Items/"+localMovieID, adminTok, nil)
	if recDetailAfterMove.Code != http.StatusOK || !strings.Contains(recDetailAfterMove.Body.String(), `"PlaybackPositionTicks":36000000000`) {
		t.Fatalf("expected watch progress preserved after move, got %d %s", recDetailAfterMove.Code, recDetailAfterMove.Body.String())
	}

	// 14. Admin tasks & cache stats/clear endpoints
	recTasks := doJSON(t, h, http.MethodGet, "/api/admin/tasks", adminTok, nil)
	if recTasks.Code != http.StatusOK {
		t.Fatalf("tasks list failed: %d", recTasks.Code)
	}
	recCache := doJSON(t, h, http.MethodGet, "/api/admin/cache/stats", adminTok, nil)
	if recCache.Code != http.StatusOK || !strings.Contains(recCache.Body.String(), `"image_cache"`) {
		t.Fatalf("cache stats failed: %d %s", recCache.Code, recCache.Body.String())
	}
	recClear := doJSON(t, h, http.MethodPost, "/api/admin/cache/clear", adminTok, nil)
	if recClear.Code != http.StatusOK {
		t.Fatalf("cache clear failed: %d", recClear.Code)
	}

	// 15. M4: Virtual Library + Collage Cover + Intro Markers + Permission Groups & Device Block
	recVLib := doJSON(t, h, http.MethodPost, "/api/admin/virtual-libraries", adminTok, map[string]any{
		"name":    "高分科幻",
		"enabled": true,
		"rule": map[string]any{
			"genres":     []string{"科幻"},
			"min_rating": 8.0,
		},
	})
	if recVLib.Code != http.StatusCreated {
		t.Fatalf("create virtual lib failed: %d %s", recVLib.Code, recVLib.Body.String())
	}
	var vlibResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(recVLib.Body.Bytes(), &vlibResp)

	// Virtual library appears in Emby Views and returns matching movie
	recViewsV := doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Views", adminTok, nil)
	if !strings.Contains(recViewsV.Body.String(), "高分科幻") {
		t.Fatalf("virtual library missing from Emby Views: %s", recViewsV.Body.String())
	}
	recVItems := doJSON(t, h, http.MethodGet, "/emby/Items?ParentId="+vlibResp.ID, adminTok, nil)
	if recVItems.Code != http.StatusOK || !strings.Contains(recVItems.Body.String(), "星际穿越") {
		t.Fatalf("virtual library items mismatch: %d %s", recVItems.Code, recVItems.Body.String())
	}

	// Generate collage cover & serve via Emby Images endpoint
	recCov := doJSON(t, h, http.MethodPost, "/api/admin/libraries/"+movieLib.ID+"/cover/generate", adminTok, nil)
	if recCov.Code != http.StatusOK {
		t.Fatalf("collage cover generation failed: %d %s", recCov.Code, recCov.Body.String())
	}
	recCovImg := doJSON(t, h, http.MethodGet, "/emby/Items/"+movieLib.ID+"/Images/Primary", adminTok, nil)
	if recCovImg.Code != http.StatusOK || !strings.HasPrefix(recCovImg.Header().Get("Content-Type"), "image/jpeg") {
		t.Fatalf("expected 200 image/jpeg for library cover, got %d ct=%s", recCovImg.Code, recCovImg.Header().Get("Content-Type"))
	}

	// Intro/Credits marker -> Emby Chapters + Web intro endpoint
	recIntro := doJSON(t, h, http.MethodPut, "/api/admin/intro-markers", adminTok, map[string]any{
		"item_id":            localMovieID,
		"intro_start_secs":   10,
		"intro_end_secs":     95,
		"credits_start_secs": 9500,
	})
	if recIntro.Code != http.StatusOK {
		t.Fatalf("upsert intro marker failed: %d %s", recIntro.Code, recIntro.Body.String())
	}
	recDetailIntro := doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Items/"+localMovieID, adminTok, nil)
	if !strings.Contains(recDetailIntro.Body.String(), `"MarkerType":"IntroStart"`) || !strings.Contains(recDetailIntro.Body.String(), `"MarkerType":"IntroEnd"`) {
		t.Fatalf("expected Emby Chapters IntroStart/IntroEnd, got %s", recDetailIntro.Body.String())
	}
	recWebIntro := doJSON(t, h, http.MethodGet, "/api/web/intro/"+localMovieID, adminTok, nil)
	if recWebIntro.Code != http.StatusOK || !strings.Contains(recWebIntro.Body.String(), `"has_marker":true`) {
		t.Fatalf("web intro endpoint failed: %d %s", recWebIntro.Code, recWebIntro.Body.String())
	}

	// Device list & block revokes alice's token
	recDevs := doJSON(t, h, http.MethodGet, "/api/admin/devices", adminTok, nil)
	if recDevs.Code != http.StatusOK || !strings.Contains(recDevs.Body.String(), "alice") {
		t.Fatalf("expected alice in device list: %d %s", recDevs.Code, recDevs.Body.String())
	}
	recBlk := doJSON(t, h, http.MethodPost, "/api/admin/devices/"+aliceLogin.User.ID+"/dev-001/block", adminTok, map[string]any{"blocked": true})
	if recBlk.Code != http.StatusOK {
		t.Fatalf("block device failed: %d", recBlk.Code)
	}
	recAliceAfterBlk := doJSON(t, h, http.MethodGet, "/emby/Users/Me", aliceLogin.AccessToken, nil)
	if recAliceAfterBlk.Code != http.StatusUnauthorized {
		t.Fatalf("expected blocked device token to be revoked (401), got %d", recAliceAfterBlk.Code)
	}

	// 16. M5: Restricted File Manager + Backups + API Key Scope + Audit Logs
	recFiles := doJSON(t, h, http.MethodGet, "/api/files", adminTok, nil)
	if recFiles.Code != http.StatusOK || !strings.Contains(recFiles.Body.String(), "Movies") {
		t.Fatalf("file manager root list failed: %d %s", recFiles.Code, recFiles.Body.String())
	}
	recFilesOutside := doJSON(t, h, http.MethodGet, "/api/files?path="+filepath.ToSlash(tmp), adminTok, nil)
	if recFilesOutside.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for file manager outside library roots, got %d", recFilesOutside.Code)
	}

	// Create portable backup + verify audit log recorded
	recBk := doJSON(t, h, http.MethodPost, "/api/admin/backups", adminTok, map[string]any{"format": "portable-zip"})
	if recBk.Code != http.StatusCreated || !strings.Contains(recBk.Body.String(), ".zip") {
		t.Fatalf("backup creation failed: %d %s", recBk.Code, recBk.Body.String())
	}

	// Create API Key: works on Emby /System/Info, MUST be rejected (403) on /api/admin/dashboard!
	recKey := doJSON(t, h, http.MethodPost, "/api/admin/keys", adminTok, map[string]any{"name": "ci-tool"})
	if recKey.Code != http.StatusCreated {
		t.Fatalf("create api key failed: %d %s", recKey.Code, recKey.Body.String())
	}
	var keyResp struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(recKey.Body.Bytes(), &keyResp)
	recKeyInfo := doJSON(t, h, http.MethodGet, "/emby/System/Info", keyResp.Key, nil)
	if recKeyInfo.Code != http.StatusOK {
		t.Fatalf("expected API key to access /System/Info, got %d", recKeyInfo.Code)
	}
	recKeyAdmin := doJSON(t, h, http.MethodGet, "/api/admin/dashboard", keyResp.Key, nil)
	if recKeyAdmin.Code != http.StatusForbidden {
		t.Fatalf("API key must NOT access /api/admin/*, got %d", recKeyAdmin.Code)
	}

	recAudit := doJSON(t, h, http.MethodGet, "/api/admin/audit-logs", adminTok, nil)
	if recAudit.Code != http.StatusOK || !strings.Contains(recAudit.Body.String(), "apikey.create") {
		t.Fatalf("expected audit log entry for apikey.create, got %d %s", recAudit.Code, recAudit.Body.String())
	}

	// 17. Shared Emby metadata/people directory portrait lookup (no <thumb> in NFO!)
	embyMetaDir := filepath.Join(tmp, "emby-metadata")
	actorDir := filepath.Join(embyMetaDir, "people", "M", "Matthew McConaughey")
	if err := os.MkdirAll(actorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(actorDir, "folder.jpg"), []byte("\xff\xd8\xff\xe0ACTORPORTRAIT"), 0o644); err != nil {
		t.Fatal(err)
	}
	recSetMeta := doJSON(t, h, http.MethodPut, "/api/admin/settings", adminTok, map[string]any{
		"emby_metadata_dir":       embyMetaDir,
		"emby_metadata_writeback": true,
	})
	if recSetMeta.Code != http.StatusOK {
		t.Fatalf("update emby_metadata_dir failed: %d %s", recSetMeta.Code, recSetMeta.Body.String())
	}
	recDetailActor := doJSON(t, h, http.MethodGet, "/emby/Users/"+adminID+"/Items/"+localMovieID, adminTok, nil)
	if !strings.Contains(recDetailActor.Body.String(), `"Matthew McConaughey"`) || !strings.Contains(recDetailActor.Body.String(), `"PrimaryImageTag"`) {
		t.Fatalf("expected PrimaryImageTag for actor from mounted Emby metadata/people, got %s", recDetailActor.Body.String())
	}
	recActorImg := doJSON(t, h, http.MethodGet, "/emby/Persons/Matthew%20McConaughey/Images/Primary", adminTok, nil)
	if recActorImg.Code != http.StatusOK || !strings.Contains(recActorImg.Body.String(), "ACTORPORTRAIT") {
		t.Fatalf("expected mounted Emby actor portrait served, got %d %q", recActorImg.Code, recActorImg.Body.String())
	}
}

func TestBoxSetLibraryBrowsing(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "BoxSet-Pass-123",
		"server_name": "BoxSet Test",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "BoxSet-Pass-123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatalf("parse login: %v", err)
	}
	if login.AccessToken == "" || login.User.ID == "" {
		t.Fatalf("missing login fields: %s", rec.Body.String())
	}

	newLibrary := func(name, kind string, paths []string) string {
		rec := doJSON(t, h, http.MethodPost, "/api/admin/libraries", login.AccessToken, map[string]any{
			"name":  name,
			"kind":  kind,
			"paths": paths,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s library failed: %d %s", kind, rec.Code, rec.Body.String())
		}
		var lib struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &lib); err != nil || lib.ID == "" {
			t.Fatalf("parse %s library: %v %s", kind, err, rec.Body.String())
		}
		return lib.ID
	}
	boxLibID := newLibrary("合集", "boxsets", nil)
	movieLibID := newLibrary("电影", "movies", []string{filepath.Join(tmp, "boxset-movies")})

	now := time.Now().UTC()
	boxItem := model.Item{ID: "boxset-e2e", LibraryID: boxLibID, ParentID: boxLibID, Type: "BoxSet", Name: "诺兰合集", SortName: "诺兰合集", IsFolder: true, ScanKey: "e2e:boxset", RandomKey: "boxsete2e", AddedAt: now, CreatedAt: now}
	movieItem := model.Item{ID: "boxset-movie-e2e", LibraryID: movieLibID, Type: "Movie", Name: "盗梦空间", SortName: "盗梦空间", ScanKey: "e2e:boxset-movie", RandomKey: "boxmoviee2e", AddedAt: now, CreatedAt: now}
	if err := srv.DB.Gorm.Create(&boxItem).Error; err != nil {
		t.Fatalf("create boxset item: %v", err)
	}
	if err := srv.DB.Gorm.Create(&movieItem).Error; err != nil {
		t.Fatalf("create movie item: %v", err)
	}
	if err := srv.DB.Gorm.Create(&model.BoxSet{ID: boxItem.ID, LibraryID: boxLibID, Name: boxItem.Name, SortName: boxItem.SortName, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create boxset: %v", err)
	}
	if err := srv.DB.Gorm.Create(&model.BoxSetItem{BoxSetID: boxItem.ID, ItemID: movieItem.ID, SortIdx: 0}).Error; err != nil {
		t.Fatalf("create boxset link: %v", err)
	}

	type itemList struct {
		Items []struct {
			ID       string `json:"Id"`
			Name     string `json:"Name"`
			Type     string `json:"Type"`
			IsFolder bool   `json:"IsFolder"`
		} `json:"Items"`
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+login.User.ID+"/Items?ParentId="+boxLibID, login.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list boxsets library: %d %s", rec.Code, rec.Body.String())
	}
	var listed itemList
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("parse boxsets library: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != boxItem.ID || listed.Items[0].Type != "BoxSet" || !listed.Items[0].IsFolder {
		t.Fatalf("boxset not listed as folder: %+v", listed.Items)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+login.User.ID+"/Items?ParentId="+boxItem.ID, login.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list boxset members: %d %s", rec.Code, rec.Body.String())
	}
	var members itemList
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatalf("parse boxset members: %v", err)
	}
	if len(members.Items) != 1 || members.Items[0].ID != movieItem.ID {
		t.Fatalf("boxset members = %+v, want %s", members.Items, movieItem.ID)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+boxItem.ID, login.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("boxset detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Type       string `json:"Type"`
		IsFolder   bool   `json:"IsFolder"`
		ChildCount int    `json:"ChildCount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("parse boxset detail: %v", err)
	}
	if detail.Type != "BoxSet" || !detail.IsFolder || detail.ChildCount != 1 {
		t.Fatalf("boxset detail = %+v", detail)
	}
}
