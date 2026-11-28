package server_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"emby-lite/internal/model"
)

func TestYambyEpisodesSeasonPathAndMediaInfoDates(t *testing.T) {
	srv, _ := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite YambyFix",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "SecretPass123!",
	})
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	tok := login.AccessToken
	uid := login.User.ID

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "剧集",
		"kind":  "tvshows",
		"paths": []string{"/media/tv"},
	})
	var lib struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)

	now := time.Date(2026, 11, 26, 13, 9, 24, 0, time.UTC)
	series := model.Item{
		ID: "ser-lj", LibraryID: lib.ID, ParentID: lib.ID, Type: "Series",
		Name: "两京十五日", SortName: "liangjingshiwuri", IsFolder: true,
		ScanKey: "ser-lj", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	season := model.Item{
		ID: "sea-lj-1", LibraryID: lib.ID, ParentID: series.ID, SeriesID: series.ID, Type: "Season",
		Name: "第1季", SortName: "001", SeasonNum: 1, IsFolder: true,
		ScanKey: "sea-lj-1", RandomKey: "r2", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	ep1 := model.Item{
		ID: "ep-lj-1", LibraryID: lib.ID, ParentID: season.ID, SeriesID: series.ID, SeasonID: season.ID, Type: "Episode",
		Name: "第 1 集", SortName: "001-0001", SeasonNum: 1, EpisodeNum: 1, RuntimeTicks: 2700 * 1e7,
		Overview: "第一集简介", ScanKey: "ep-lj-1", RandomKey: "r3", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	ep2 := model.Item{
		ID: "ep-lj-2", LibraryID: lib.ID, ParentID: season.ID, SeriesID: series.ID, SeasonID: season.ID, Type: "Episode",
		Name: "第 2 集", SortName: "001-0002", SeasonNum: 1, EpisodeNum: 2, RuntimeTicks: 2739 * 1e7,
		Overview: "第二集简介", ScanKey: "ep-lj-2", RandomKey: "r4", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	for _, it := range []model.Item{series, season, ep1, ep2} {
		if err := srv.DB.Gorm.Create(&it).Error; err != nil {
			t.Fatalf("create item %s: %v", it.ID, err)
		}
	}
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "ms-ep2", ItemID: ep2.ID, SourceType: "strm", Path: "/media/tv/两京十五日/Season 1/S01E02.strm",
		StrmURL: "http://192.168.0.186:35244/d/Media/S01E02.mkv", Container: "mkv",
		FileSize: 1617145890, Bitrate: 4722952, Width: 3840, Height: 2160,
	}).Error

	// 1. Yamby Episode detail calls GET /emby/Shows/{seasonId}/Episodes?SeasonId={seasonId}
	rec = doJSON(t, h, http.MethodGet, "/emby/Shows/"+season.ID+"/Episodes?UserId="+uid+"&SeasonId="+season.ID+"&Fields=BasicSyncInfo,Overview", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Shows/{seasonId}/Episodes?SeasonId={seasonId} status=%d body=%s", rec.Code, rec.Body.String())
	}
	var epList struct {
		TotalRecordCount int              `json:"TotalRecordCount"`
		Items            []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &epList); err != nil {
		t.Fatalf("decode episodes: %v", err)
	}
	if epList.TotalRecordCount != 2 || len(epList.Items) != 2 {
		t.Fatalf("expected 2 episodes when path uses SeasonId, got total=%d len=%d body=%s", epList.TotalRecordCount, len(epList.Items), rec.Body.String())
	}
	// Verify lightweight Fields on /Episodes did not needlessly dump People or MediaSources
	if _, hasPeople := epList.Items[0]["People"]; hasPeople {
		t.Fatalf("unexpected People in lightweight /Episodes response")
	}
	if _, hasMS := epList.Items[0]["MediaSources"]; hasMS {
		t.Fatalf("unexpected MediaSources in lightweight /Episodes response")
	}
	if epList.Items[0]["Overview"] != "第一集简介" {
		t.Fatalf("expected Overview when requested in Fields, got %#v", epList.Items[0]["Overview"])
	}

	// 2. Also verify GET /emby/Shows/{seasonId}/Episodes without SeasonId query param works
	rec = doJSON(t, h, http.MethodGet, "/emby/Shows/"+season.ID+"/Episodes?UserId="+uid, tok, nil)
	epList.Items = nil
	epList.TotalRecordCount = 0
	_ = json.Unmarshal(rec.Body.Bytes(), &epList)
	if rec.Code != http.StatusOK || epList.TotalRecordCount != 2 || len(epList.Items) != 2 {
		t.Fatalf("Shows/{seasonId}/Episodes without SeasonId param failed: status=%d total=%d len=%d", rec.Code, epList.TotalRecordCount, len(epList.Items))
	}

	// 3. Episode detail must expose Emby .0000000Z DateCreated & DateModified plus root Size/Bitrate/FileName
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+ep2.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("episode detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var epDetail map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &epDetail)
	embyTimeRE := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{7}Z$`)
	dc, _ := epDetail["DateCreated"].(string)
	dm, _ := epDetail["DateModified"].(string)
	if !embyTimeRE.MatchString(dc) {
		t.Fatalf("DateCreated = %q, want Emby 7-fraction UTC format (.0000000Z)", dc)
	}
	if !embyTimeRE.MatchString(dm) {
		t.Fatalf("DateModified = %q, want Emby 7-fraction UTC format (.0000000Z)", dm)
	}
	if sz, _ := epDetail["Size"].(float64); int64(sz) != 1617145890 {
		t.Fatalf("root Size = %v, want 1617145890", epDetail["Size"])
	}
	if br, _ := epDetail["Bitrate"].(float64); int64(br) != 4722952 {
		t.Fatalf("root Bitrate = %v, want 4722952", epDetail["Bitrate"])
	}
	if fn, _ := epDetail["FileName"].(string); fn != "S01E02.mkv" {
		t.Fatalf("root FileName = %q, want S01E02.mkv", fn)
	}
}

func TestResumeThresholdsPreventSensitiveEntries(t *testing.T) {
	srv, _ := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite ResumeThreshold",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "SecretPass123!",
	})
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	tok := login.AccessToken
	uid := login.User.ID

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "电影",
		"kind":  "movies",
		"paths": []string{"/media/movies"},
	})
	var lib struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)

	now := time.Now().UTC()
	// 1. Unprobed STRM (RuntimeTicks = 0)
	strmMovie := model.Item{
		ID: "mov-strm", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "假面骑士", SortName: "jiamianqishi", RuntimeTicks: 0,
		ScanKey: "mov-strm", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	// 2. Normal movie (RuntimeTicks = 6000s = 100 min; 5% = 300s)
	knownMovie := model.Item{
		ID: "mov-known", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "迈克尔·杰克逊", SortName: "mj", RuntimeTicks: 6000 * 1e7,
		ScanKey: "mov-known", RandomKey: "r2", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	// 3. Short clip (< 300s MinResumeDurationSeconds)
	shortClip := model.Item{
		ID: "mov-short", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "预告片", SortName: "trailer", RuntimeTicks: 120 * 1e7,
		ScanKey: "mov-short", RandomKey: "r3", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	// 4. Pre-existing legacy dirty row in DB (e.g. 2s position from before fix)
	dirtyLegacy := model.Item{
		ID: "mov-dirty", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "历史脏数据", SortName: "dirty", RuntimeTicks: 0,
		ScanKey: "mov-dirty", RandomKey: "r4", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	for _, it := range []model.Item{strmMovie, knownMovie, shortClip, dirtyLegacy} {
		if err := srv.DB.Gorm.Create(&it).Error; err != nil {
			t.Fatalf("create %s: %v", it.ID, err)
		}
	}
	_ = srv.DB.Gorm.Create(&model.UserData{
		UserID: uid, ItemID: dirtyLegacy.ID, PositionTicks: 2 * 1e7, Played: false, LastPlayedAt: &now, UpdatedAt: now,
	}).Error

	// Case A: Opening unprobed STRM for 1s and stopping MUST NOT enter Resume
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing", tok, map[string]any{"ItemId": strmMovie.ID, "PositionTicks": 0})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("start status=%d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": strmMovie.ID, "PositionTicks": 1 * 1e7})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("stop status=%d", rec.Code)
	}

	// Case B: Watching 2% (120s of 6000s) MUST NOT enter Resume (< 5% MinResumePct)
	_ = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": knownMovie.ID, "PositionTicks": 120 * 1e7})

	// Case C: Watching 50% of a 2-min short clip MUST NOT enter Resume (< 300s MinResumeDurationSeconds)
	_ = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": shortClip.ID, "PositionTicks": 60 * 1e7})

	// Verify /Items/Resume is completely empty (even dirtyLegacy 2s row is filtered out!)
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video", tok, nil)
	var res struct {
		TotalRecordCount int              `json:"TotalRecordCount"`
		Items            []map[string]any `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.TotalRecordCount != 0 || len(res.Items) != 0 {
		t.Fatalf("expected 0 resume items for <threshold opens, got total=%d body=%s", res.TotalRecordCount, rec.Body.String())
	}

	// Case D: Watching >= 60s on unprobed STRM AND >= 5% (360s) on known movie MUST enter Resume
	_ = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": strmMovie.ID, "PositionTicks": 65 * 1e7})
	_ = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": knownMovie.ID, "PositionTicks": 360 * 1e7})

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video", tok, nil)
	res.Items = nil
	res.TotalRecordCount = 0
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.TotalRecordCount != 2 || len(res.Items) != 2 {
		t.Fatalf("expected 2 valid resume items, got total=%d body=%s", res.TotalRecordCount, rec.Body.String())
	}

	// Case E: Reaching >= 90% marks Played=true and removes from Resume
	_ = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": knownMovie.ID, "PositionTicks": 5500 * 1e7})
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video", tok, nil)
	res.Items = nil
	res.TotalRecordCount = 0
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.TotalRecordCount != 1 || res.Items[0]["Id"] != strmMovie.ID {
		t.Fatalf("expected only strmMovie left in resume after 90%% completion, got %s", rec.Body.String())
	}
}

func TestGranularFieldsAvoidListBloatAndResizeLibraryCover(t *testing.T) {
	srv, _ := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite Perf",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "SecretPass123!",
	})
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &login)
	tok := login.AccessToken
	uid := login.User.ID

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "电影",
		"kind":  "movies",
		"paths": []string{"/media/movies"},
	})
	var lib struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)

	now := time.Now().UTC()
	mov := model.Item{
		ID: "mov-perf-1", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "性能测试电影", SortName: "perf", Overview: "很长的简介内容",
		ScanKey: "mov-perf-1", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	_ = srv.DB.Gorm.Create(&mov).Error
	_ = srv.DB.Gorm.Create(&model.Person{ID: "p-1", Name: "张三", UpdatedAt: now}).Error
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: mov.ID, PersonID: "p-1", RoleType: "Actor", SortIdx: 0}).Error
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "ms-perf-1", ItemID: mov.ID, SourceType: "strm", Path: "/media/movies/perf.strm",
		StrmURL: "http://cdn.local/perf.mkv", Container: "mkv",
		ProbeJSON: `[{"Type":"Video","Index":0,"Codec":"hevc","IsDefault":true,"IsForced":false,"IsExternal":false}]`,
	}).Error

	// Yamby list request includes Path & Container, NOT People/MediaSources/Overview.
	yambyLatest := "/emby/Users/" + uid + "/Items/Latest?ParentId=" + lib.ID + "&Fields=PrimaryImageAspectRatio,BasicSyncInfo,ProductionYear,Path,Container"
	rec = doJSON(t, h, http.MethodGet, yambyLatest, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("latest status=%d body=%s", rec.Code, rec.Body.String())
	}
	var latest []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &latest)
	if len(latest) != 1 {
		t.Fatalf("expected 1 latest item, got %d", len(latest))
	}
	if latest[0]["Path"] != "http://cdn.local/perf.mkv" || latest[0]["Container"] != "mkv" {
		t.Fatalf("expected lightweight Path & Container, got Path=%#v Container=%#v", latest[0]["Path"], latest[0]["Container"])
	}
	if _, hasPeople := latest[0]["People"]; hasPeople {
		t.Fatalf("Latest with Path,Container must NOT materialize People")
	}
	if _, hasMS := latest[0]["MediaSources"]; hasMS {
		t.Fatalf("Latest with Path,Container must NOT materialize full MediaSources")
	}
	if _, hasOverview := latest[0]["Overview"]; hasOverview {
		t.Fatalf("Latest without Overview in Fields must omit Overview")
	}

	// Verify LibraryCover resizing respects maxWidth (e.g. 1600x900 -> maxWidth=400)
	img := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1600; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	var rawBuf bytes.Buffer
	_ = jpeg.Encode(&rawBuf, img, &jpeg.Options{Quality: 95})
	_ = srv.DB.Gorm.Create(&model.LibraryCover{
		LibraryID: lib.ID, Kind: "custom", Mime: "image/jpeg", Data: rawBuf.Bytes(), Revision: "cov-rev-1", UpdatedAt: now,
	}).Error

	req := httptest.NewRequest(http.MethodGet, "/emby/Items/"+lib.ID+"/Images/Primary?maxWidth=400&quality=85", nil)
	req.Header.Set("X-Emby-Token", tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("library cover status=%d", w.Code)
	}
	decoded, _, err := image.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode resized cover: %v", err)
	}
	if decoded.Bounds().Dx() != 400 {
		t.Fatalf("resized LibraryCover width = %d, want 400", decoded.Bounds().Dx())
	}
	if w.Body.Len() >= rawBuf.Len() {
		t.Fatalf("resized LibraryCover should be smaller than raw (%d >= %d)", w.Body.Len(), rawBuf.Len())
	}
}
