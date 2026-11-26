package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"emby-lite/internal/model"
)

func doEmbyReq(t *testing.T, h http.Handler, method, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestResumeEpisodeSeriesNameArtworkAndClientCompat(t *testing.T) {
	srv, _ := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite",
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
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatalf("login decode: %v", err)
	}
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

	now := time.Now().UTC()
	deletedAt := now.Add(-time.Hour)
	t1 := now.Add(-10 * time.Minute)
	t2 := now.Add(-20 * time.Minute)
	t3 := now.Add(-30 * time.Minute)
	t4 := now.Add(-40 * time.Minute)
	t5 := now.Add(-50 * time.Minute)

	// 1. Standard linked Series -> Season -> Episode (no own episode Primary image; Series has Primary/Backdrop/Thumb/Logo)
	series1 := model.Item{
		ID: "ser-1", LibraryID: lib.ID, ParentID: lib.ID, Type: "Series",
		Name: "绝命毒师", SortName: "绝命毒师", IsFolder: true,
		ScanKey: "ser-1", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	season1 := model.Item{
		ID: "sea-1", LibraryID: lib.ID, ParentID: series1.ID, SeriesID: series1.ID, Type: "Season",
		Name: "第 1 季", SortName: "001", SeasonNum: 1, IsFolder: true,
		ScanKey: "sea-1", RandomKey: "r2", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	epNormal := model.Item{
		ID: "ep-normal", LibraryID: lib.ID, ParentID: season1.ID, SeriesID: series1.ID, SeasonID: season1.ID, Type: "Episode",
		Name: "空手套白狼", SortName: "001-0006", SeasonNum: 1, EpisodeNum: 6, RuntimeTicks: 3000 * 1e7,
		DirPath: "/media/tv/绝命毒师/Season 1",
		ScanKey: "ep-normal", RandomKey: "r3", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	// 2. Episode with EMPTY SeriesID, only linked via SeasonID -> Season -> Series
	epViaSeason := model.Item{
		ID: "ep-via-season", LibraryID: lib.ID, ParentID: season1.ID, SeriesID: "", SeasonID: season1.ID, Type: "Episode",
		Name: "第 7 集", SortName: "001-0007", SeasonNum: 1, EpisodeNum: 7, RuntimeTicks: 3000 * 1e7,
		DirPath: "/media/tv/绝命毒师/Season 1",
		ScanKey: "ep-via-season", RandomKey: "r4", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	// 3. Episode pointing to a soft-deleted Series row (must still resolve SeriesName instead of omitting it)
	seriesSoftDel := model.Item{
		ID: "ser-softdel", LibraryID: lib.ID, ParentID: lib.ID, Type: "Series",
		Name: "两京十五日", SortName: "两京十五日", IsFolder: true, SoftDeletedAt: &deletedAt,
		ScanKey: "ser-softdel", RandomKey: "r5", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	epSoftDelSeries := model.Item{
		ID: "ep-softdel", LibraryID: lib.ID, ParentID: seriesSoftDel.ID, SeriesID: seriesSoftDel.ID, SeasonID: "", Type: "Episode",
		Name: "虎硫药", SortName: "001-0001", SeasonNum: 1, EpisodeNum: 1, RuntimeTicks: 2700 * 1e7,
		DirPath: "/media/tv/两京十五日 (2026)/Season 1",
		ScanKey: "ep-softdel", RandomKey: "r6", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	// 4. Episode with dangling SeriesID and missing Season row (falls back to DirPath folder name!)
	epPathFallback := model.Item{
		ID: "ep-path", LibraryID: lib.ID, ParentID: lib.ID, SeriesID: "missing-series", SeasonID: "", Type: "Episode",
		Name: "第 22 集", SortName: "001-0022", SeasonNum: 1, EpisodeNum: 22, RuntimeTicks: 2700 * 1e7,
		DirPath: "/media/tv/西游记后传 (2000)/Season 1",
		ScanKey: "ep-path", RandomKey: "r7", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	// 5. Resumable Movie for IncludeItemTypes / ExcludeItemTypes filter verification
	movie := model.Item{
		ID: "mov-1", LibraryID: lib.ID, ParentID: lib.ID, Type: "Movie",
		Name: "沙丘", SortName: "沙丘", RuntimeTicks: 9000 * 1e7,
		ScanKey: "mov-1", RandomKey: "r8", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}

	for _, it := range []model.Item{series1, season1, epNormal, epViaSeason, seriesSoftDel, epSoftDelSeries, epPathFallback, movie} {
		if err := srv.DB.Gorm.Create(&it).Error; err != nil {
			t.Fatalf("create item %s: %v", it.ID, err)
		}
	}

	// Attach Series artwork (Primary, Backdrop, Thumb, Logo)
	for i, img := range []model.ItemImage{
		{ID: "img-s1-p", ItemID: series1.ID, Kind: "Primary", IndexNum: 0, SourceType: "local", SourceRef: "/tmp/s1-poster.jpg", Tag: "s1primarytag", UpdatedAt: now},
		{ID: "img-s1-b", ItemID: series1.ID, Kind: "Backdrop", IndexNum: 0, SourceType: "local", SourceRef: "/tmp/s1-bg.jpg", Tag: "s1backdroptag", UpdatedAt: now},
		{ID: "img-s1-t", ItemID: series1.ID, Kind: "Thumb", IndexNum: 0, SourceType: "local", SourceRef: "/tmp/s1-thumb.jpg", Tag: "s1thumbtag", UpdatedAt: now},
		{ID: "img-s1-l", ItemID: series1.ID, Kind: "Logo", IndexNum: 0, SourceType: "local", SourceRef: "/tmp/s1-logo.png", Tag: "s1logotag", UpdatedAt: now},
	} {
		if err := srv.DB.Gorm.Create(&img).Error; err != nil {
			t.Fatalf("create image %d: %v", i, err)
		}
	}

	// All UserData rows share identical UpdatedAt (mimicking Emby migration) but distinct LastPlayedAt.
	for _, ud := range []model.UserData{
		{UserID: uid, ItemID: epNormal.ID, PositionTicks: 1200 * 1e7, Played: false, LastPlayedAt: &t1, UpdatedAt: now},
		{UserID: uid, ItemID: epViaSeason.ID, PositionTicks: 1000 * 1e7, Played: false, LastPlayedAt: &t2, UpdatedAt: now},
		{UserID: uid, ItemID: epSoftDelSeries.ID, PositionTicks: 900 * 1e7, Played: false, LastPlayedAt: &t3, UpdatedAt: now},
		{UserID: uid, ItemID: epPathFallback.ID, PositionTicks: 800 * 1e7, Played: false, LastPlayedAt: &t4, UpdatedAt: now},
		{UserID: uid, ItemID: movie.ID, PositionTicks: 700 * 1e7, Played: false, LastPlayedAt: &t5, UpdatedAt: now},
	} {
		if err := srv.DB.Gorm.Create(&ud).Error; err != nil {
			t.Fatalf("create user_data %s: %v", ud.ItemID, err)
		}
	}

	// --- Case A: Yamby / Standard Emby client on /Items/Resume ---
	yambyHeaders := map[string]string{
		"X-Emby-Authorization": `MediaBrowser Client="Yamby", Device="Pixel 9", DeviceId="yamby-1", Version="1.0.9"`,
	}
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video&Limit=20", tok, yambyHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("Yamby resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		TotalRecordCount int              `json:"TotalRecordCount"`
		Items            []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode Yamby resume: %v", err)
	}
	if res.TotalRecordCount != 5 || len(res.Items) != 5 {
		t.Fatalf("expected 5 resume items, got total=%d len=%d", res.TotalRecordCount, len(res.Items))
	}

	// Verify LastPlayedAt ordering (most recently played first).
	wantOrder := []string{epNormal.ID, epViaSeason.ID, epSoftDelSeries.ID, epPathFallback.ID, movie.ID}
	for i, wantID := range wantOrder {
		if gotID, _ := res.Items[i]["Id"].(string); gotID != wantID {
			t.Fatalf("resume order[%d] = %q, want %q", i, gotID, wantID)
		}
	}

	byID := map[string]map[string]any{}
	for _, it := range res.Items {
		id, _ := it["Id"].(string)
		byID[id] = it
	}

	// 1. Standard episode keeps standard Emby Name + SeriesName + full parent artwork metadata.
	got1 := byID[epNormal.ID]
	if got1["Name"] != "空手套白狼" || got1["SeriesName"] != "绝命毒师" || got1["SeriesId"] != series1.ID || got1["SeasonName"] != "第 1 季" {
		t.Fatalf("normal episode mismatch: %#v", got1)
	}
	if got1["SeriesPrimaryImageTag"] == nil || got1["ParentThumbItemId"] != series1.ID || got1["ParentThumbImageTag"] == nil ||
		got1["ParentLogoItemId"] != series1.ID || got1["ParentLogoImageTag"] == nil ||
		got1["ParentPrimaryImageItemId"] != series1.ID || got1["ParentPrimaryImageTag"] == nil ||
		got1["ParentBackdropItemId"] != series1.ID {
		t.Fatalf("missing inherited parent artwork fields on normal episode: %#v", got1)
	}
	// Because epNormal has no own Primary image, Resume artwork attaches Series Primary!
	if imgTags, ok := got1["ImageTags"].(map[string]any); !ok || imgTags["Primary"] == "" || got1["PrimaryImageItemId"] != series1.ID {
		t.Fatalf("expected Resume episode without own thumb to inherit Series Primary: %#v", got1)
	}

	// 2. Episode with empty SeriesID recovered SeriesId + SeriesName + Series artwork via SeasonID!
	got2 := byID[epViaSeason.ID]
	if got2["SeriesName"] != "绝命毒师" || got2["SeriesId"] != series1.ID || got2["SeriesPrimaryImageTag"] == nil {
		t.Fatalf("epViaSeason failed to recover Series via SeasonID: %#v", got2)
	}

	// 3. Episode with soft-deleted Series still preserves SeriesName + default SeasonName.
	got3 := byID[epSoftDelSeries.ID]
	if got3["SeriesName"] != "两京十五日" || got3["SeasonName"] != "第 1 季" {
		t.Fatalf("epSoftDelSeries failed to preserve SeriesName: %#v", got3)
	}

	// 4. Episode with dangling SeriesID falls back to DirPath folder name (stripped of "(2000)").
	got4 := byID[epPathFallback.ID]
	if got4["SeriesName"] != "西游记后传" || got4["SeasonName"] != "第 1 季" {
		t.Fatalf("epPathFallback failed DirPath fallback: %#v", got4)
	}

	// --- Case B: Query filters on /Items/Resume (IncludeItemTypes, ExcludeItemTypes, MediaTypes) ---
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?IncludeItemTypes=Episode", tok, yambyHeaders)
	var epOnly struct {
		TotalRecordCount int              `json:"TotalRecordCount"`
		Items            []map[string]any `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &epOnly)
	if epOnly.TotalRecordCount != 4 || len(epOnly.Items) != 4 {
		t.Fatalf("IncludeItemTypes=Episode expected 4, got total=%d len=%d", epOnly.TotalRecordCount, len(epOnly.Items))
	}
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Audio", tok, yambyHeaders)
	var audioOnly struct {
		TotalRecordCount int              `json:"TotalRecordCount"`
		Items            []map[string]any `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &audioOnly)
	if audioOnly.TotalRecordCount != 0 || len(audioOnly.Items) != 0 {
		t.Fatalf("MediaTypes=Audio expected 0, got total=%d len=%d", audioOnly.TotalRecordCount, len(audioOnly.Items))
	}

	// --- Case C: Filmly (网易爆米花) Resume compatibility + cache isolation from Yamby ---
	filmlyHeaders := map[string]string{
		"User-Agent": "Filmly/162 CFNetwork/3896.100.1.2.1 Darwin/27.0.0",
	}
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video&Limit=20", tok, filmlyHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("Filmly resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	var filmlyRes struct {
		Items []map[string]any `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &filmlyRes)
	filmlyByID := map[string]map[string]any{}
	for _, it := range filmlyRes.Items {
		id, _ := it["Id"].(string)
		filmlyByID[id] = it
	}
	if f1 := filmlyByID[epNormal.ID]; f1["Name"] != "绝命毒师 第6集 空手套白狼" {
		t.Fatalf("Filmly normal episode title = %#v, want '绝命毒师 第6集 空手套白狼'", f1["Name"])
	} else {
		for _, omitted := range []string{"SeriesId", "SeriesName", "SeasonId", "SeasonName", "ParentId", "ParentIndexNumber", "ParentBackdropItemId"} {
			if _, exists := f1[omitted]; exists {
				t.Fatalf("Filmly episode must omit %s: %#v", omitted, f1)
			}
		}
		if etag, _ := f1["Etag"].(string); !strings.HasSuffix(etag, "-filmly-resume-episode-v1") {
			t.Fatalf("Filmly episode Etag missing compat suffix: %q", etag)
		}
		if ud, _ := f1["UserData"].(map[string]any); ud["ItemId"] != epNormal.ID || ud["Key"] != epNormal.ID {
			t.Fatalf("Filmly UserData identity mismatch: %#v", ud)
		}
	}
	// Duplicate "第 7 集" / "第 22 集" must not become "绝命毒师 第7集 第 7 集"!
	if f2 := filmlyByID[epViaSeason.ID]; f2["Name"] != "绝命毒师 第7集" {
		t.Fatalf("Filmly duplicate episode label = %#v, want '绝命毒师 第7集'", f2["Name"])
	}
	if f4 := filmlyByID[epPathFallback.ID]; f4["Name"] != "西游记后传 第22集" {
		t.Fatalf("Filmly path-fallback episode title = %#v, want '西游记后传 第22集'", f4["Name"])
	}
	// Movie keeps original name, gets UserData.ItemId.
	if fm := filmlyByID[movie.ID]; fm["Name"] != "沙丘" {
		t.Fatalf("Filmly movie title changed: %#v", fm["Name"])
	}

	// Verify Yamby cache was NOT polluted by Filmly request (and vice versa).
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?MediaTypes=Video&Limit=20", tok, yambyHeaders)
	var yambyAgain struct {
		Items []map[string]any `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &yambyAgain)
	if yambyAgain.Items[0]["Name"] != "空手套白狼" || yambyAgain.Items[0]["SeriesName"] != "绝命毒师" {
		t.Fatalf("Yamby resume response polluted by Filmly cache: %#v", yambyAgain.Items[0])
	}

	// Verify /Items?Filters=IsResumable also applies SeriesName + Filmly compat cleanly.
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Filters=IsResumable&Recursive=true&IncludeItemTypes=Episode", tok, filmlyHeaders)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "绝命毒师 第6集 空手套白狼") {
		t.Fatalf("Filmly IsResumable query failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doEmbyReq(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Filters=IsResumable&Recursive=true&IncludeItemTypes=Episode", tok, yambyHeaders)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"SeriesName":"绝命毒师"`) {
		t.Fatalf("Yamby IsResumable query failed: %d %s", rec.Code, rec.Body.String())
	}
}
