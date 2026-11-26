package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-lite/internal/emby"
	"emby-lite/internal/model"
)

func TestEmby410AndYambyCompatibility(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	// Setup admin
	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite 4.10",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	// 1. Public SystemInfo endpoints (/emby, /emby/System/Info/Public, HEAD, double slashes)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/emby/System/Info/Public"},
		{http.MethodHead, "/emby/System/Info/Public"},
		{http.MethodGet, "//emby//emby/System/Info/Public/"},
		{http.MethodGet, "/emby"},
		{http.MethodGet, "/emby/"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Host = "nas.local:8096"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if tc.method == http.MethodGet {
			var info map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
				t.Fatalf("%s %s invalid json: %v body=%s", tc.method, tc.path, err, w.Body.String())
			}
			if emby.ProtocolVersion != "4.10.2.0" || info["Version"] != "4.10.2.0" {
				t.Fatalf("Version = %#v (ProtocolVersion=%q), want 4.10.2.0", info["Version"], emby.ProtocolVersion)
			}
			if info["ProductName"] != "Emby Server" {
				t.Fatalf("ProductName = %#v, want Emby Server", info["ProductName"])
			}
			if info["WanAddress"] == "" || info["LocalAddress"] == "" {
				t.Fatalf("missing address fields: %#v", info)
			}
		}
	}

	// Public pre-login endpoints must not return 401 or SPA HTML.
	for _, path := range []string{
		"/emby/System/Endpoint",
		"/emby/Branding/Configuration",
		"/emby/Localization/Options",
		"/emby/Localization/Cultures",
		"/emby/QuickConnect/Enabled",
		"/emby/System/Configuration/Public",
		"/emby/DisplayPreferences/usersettings",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "<!doctype html>") {
			t.Fatalf("GET %s failed: %d %s", path, w.Code, w.Body.String())
		}
	}

	// Unknown /emby/* GET endpoint MUST return 404 JSON, NEVER 200 HTML!
	{
		req := httptest.NewRequest(http.MethodGet, "/emby/Some/Unknown/Endpoint", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("unknown /emby route must return 404 JSON, got %d ct=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}

	// 2. Login via form-urlencoded with Yamby headers & duplicate slashes/trailing slash
	loginReq := httptest.NewRequest(http.MethodPost, "//emby/Users/AuthenticateByName/", strings.NewReader("Username=admin&Pw=SecretPass123!"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Header.Set("X-Emby-Authorization", `MediaBrowser Client = "Yamby", Device = "Pixel 9", DeviceId = "yamby-dev-1", Version = "1.0.9"`)
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("Yamby form login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var loginResp struct {
		AccessToken string `json:"AccessToken"`
		ServerID    string `json:"ServerId"`
		User        struct {
			ID            string  `json:"Id"`
			Name          string  `json:"Name"`
			LastLoginDate *string `json:"LastLoginDate"`
		} `json:"User"`
		SessionInfo struct {
			ServerID        string         `json:"ServerId"`
			Client          string         `json:"Client"`
			DeviceName      string         `json:"DeviceName"`
			DeviceID        string         `json:"DeviceId"`
			IsActive        bool           `json:"IsActive"`
			PlayState       map[string]any `json:"PlayState"`
			AdditionalUsers []any          `json:"AdditionalUsers"`
		} `json:"SessionInfo"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal loginResp: %v", err)
	}
	if loginResp.AccessToken == "" || loginResp.User.ID == "" || !loginResp.SessionInfo.IsActive || loginResp.SessionInfo.PlayState == nil || loginResp.SessionInfo.Client != "Yamby" {
		t.Fatalf("incomplete Yamby login response: %+v", loginResp)
	}
	tok := loginResp.AccessToken
	uid := loginResp.User.ID

	// 3. Seed library, movie with studios/genres/tags/DisplayOrder/intro/subtitles, and series/season/episodes
	mediaDir := filepath.Join(tmp, "media")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	moviePath := filepath.Join(mediaDir, "Dune.mkv")
	subPath := filepath.Join(mediaDir, "Dune.zh.srt")
	_ = os.WriteFile(moviePath, []byte("FAKE-DUNE-MKV"), 0o644)
	_ = os.WriteFile(subPath, []byte("1\n00:00:01,000 --> 00:00:02,000\n沙丘\n"), 0o644)

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "电影",
		"kind":  "movies",
		"paths": []string{mediaDir},
	})
	var libResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &libResp)
	libID := libResp.ID

	now := time.Now().UTC()
	movie := model.Item{
		ID:              "movie-yamby-1",
		LibraryID:       libID,
		ParentID:        libID,
		Type:            "Movie",
		Name:            "沙丘",
		OriginalName:    "Dune",
		SortName:        "shaqiu",
		PinyinInitials:  "sq",
		Year:            2021,
		OfficialRating:  "PG-13",
		CommunityRating: 8.3,
		RuntimeTicks:    9300 * 1e7,
		DisplayOrder:    1, // Must serialize as string "aired", NOT integer 1!
		StudiosJSON:     `["Legendary Pictures","Warner Bros."]`,
		ScanKey:         "yamby:movie:1",
		RandomKey:       "r1",
		AddedAt:         now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := srv.DB.Gorm.Create(&movie).Error; err != nil {
		t.Fatal(err)
	}
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: movie.ID, Genre: "科幻"}).Error
	_ = srv.DB.Gorm.Create(&model.ItemTag{ItemID: movie.ID, Tag: "IMAX"}).Error
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "src-1", ItemID: movie.ID, SourceType: "local", Path: moviePath, Container: "mkv", FileSize: 13, Width: 3840, Height: 2160,
	}).Error
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "src-2", ItemID: movie.ID, SourceType: "local", Path: moviePath, Container: "mkv", FileSize: 13, Width: 1920, Height: 1080,
	}).Error
	_ = srv.DB.Gorm.Create(&model.MediaSubtitle{
		ID: "sub-1", MediaSourceID: "src-1", ItemID: movie.ID, StreamIndex: 2, IsExternal: true, Path: subPath, Language: "chi", Title: "中文", Codec: "srt", IsDefault: true,
	}).Error
	_ = srv.DB.Gorm.Create(&model.IntroMarker{
		ID: "intro-1", ItemID: movie.ID, IntroStartTicks: 10 * 1e7, IntroEndTicks: 90 * 1e7, CreditsStartTicks: 9000 * 1e7,
	}).Error

	// 4. Strict Kotlin/Yamby schema check on BaseItemDto detail (NameLongIdPair int64 IDs + string DisplayOrder!)
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("item detail failed: %d %s", rec.Code, rec.Body.String())
	}
	type nameLongIDPair struct {
		Name string `json:"Name"`
		Id   int64  `json:"Id"`
	}
	var strictItem struct {
		ID           string           `json:"Id"`
		Name         string           `json:"Name"`
		DisplayOrder string           `json:"DisplayOrder"`
		GenreItems   []nameLongIDPair `json:"GenreItems"`
		Studios      []nameLongIDPair `json:"Studios"`
		TagItems     []nameLongIDPair `json:"TagItems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &strictItem); err != nil {
		t.Fatalf("strict Yamby BaseItemDto decode failed (type mismatch!): %v body=%s", err, rec.Body.String())
	}
	if strictItem.DisplayOrder != "aired" {
		t.Fatalf("DisplayOrder = %q, want aired", strictItem.DisplayOrder)
	}
	if len(strictItem.GenreItems) != 1 || strictItem.GenreItems[0].Id <= 0 {
		t.Fatalf("invalid GenreItems: %+v", strictItem.GenreItems)
	}
	if len(strictItem.Studios) != 2 || strictItem.Studios[0].Id <= 0 || strictItem.Studios[1].Id <= 0 {
		t.Fatalf("invalid Studios: %+v", strictItem.Studios)
	}
	if len(strictItem.TagItems) != 1 || strictItem.TagItems[0].Id <= 0 {
		t.Fatalf("invalid TagItems: %+v", strictItem.TagItems)
	}

	// 5. Strict schema check on /Items/Filters2 (NameLongIdPair int64 IDs for Genres, Tags, Studios)
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/Filters2?ParentId="+libID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Filters2 failed: %d %s", rec.Code, rec.Body.String())
	}
	var filters2 struct {
		Genres          []nameLongIDPair `json:"Genres"`
		Tags            []nameLongIDPair `json:"Tags"`
		Studios         []nameLongIDPair `json:"Studios"`
		OfficialRatings []string         `json:"OfficialRatings"`
		Years           []int            `json:"Years"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &filters2); err != nil {
		t.Fatalf("strict Filters2 decode failed: %v body=%s", err, rec.Body.String())
	}
	if len(filters2.Genres) != 1 || len(filters2.Tags) != 1 || len(filters2.Studios) != 2 {
		t.Fatalf("unexpected Filters2 payload: %+v", filters2)
	}

	// 6. Query /Items by numeric GenreIds, StudioIds, and lowercase IncludeItemTypes=movie
	genreIDStr := strconv.FormatInt(filters2.Genres[0].Id, 10)
	studioIDStr := strconv.FormatInt(filters2.Studios[0].Id, 10)
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Recursive=true&IncludeItemTypes=movie&GenreIds="+genreIDStr+"&StudioIds="+studioIDStr, tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), movie.ID) {
		t.Fatalf("Items filter by numeric GenreIds/StudioIds + lowercase type failed: %d %s", rec.Code, rec.Body.String())
	}

	// 7. Library discovery via IncludeItemTypes=CollectionFolder,UserView
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?IncludeItemTypes=CollectionFolder,UserView", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), libID) {
		t.Fatalf("CollectionFolder discovery failed: %d %s", rec.Code, rec.Body.String())
	}

	// 8. Emby 4.10 HomeSections & Sections/:section/Items + Search/Hints + MediaSegments
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/HomeSections", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nextup"`) {
		t.Fatalf("HomeSections failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Sections/latestmedia/Items?Limit=10", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), movie.ID) {
		t.Fatalf("Sections/latestmedia failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Search/Hints?SearchTerm=Dune", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"SearchHints"`) || !strings.Contains(rec.Body.String(), movie.ID) {
		t.Fatalf("Search/Hints failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/MediaSegments/"+movie.ID, tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Type":"Intro"`) || !strings.Contains(rec.Body.String(), `"Type":"Outro"`) {
		t.Fatalf("MediaSegments failed: %d %s", rec.Code, rec.Body.String())
	}

	// 9. /Users/:userId/Items/:itemId/PlaybackInfo with MediaSourceId preference + 4.10 subtitle path with StartPositionTicks
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/Items/"+movie.ID+"/PlaybackInfo?MediaSourceId=src-2", tok, map[string]any{
		"DeviceProfile": map[string]any{
			"Name": "Yamby",
			"TranscodingProfiles": []map[string]any{
				{"Container": "ts", "MaxAudioChannels": 2, "MinSegments": "4"},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("user-scoped PlaybackInfo failed: %d %s", rec.Code, rec.Body.String())
	}
	var pb struct {
		MediaSources []struct {
			ID string `json:"Id"`
		} `json:"MediaSources"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	if len(pb.MediaSources) != 2 || pb.MediaSources[0].ID != "src-2" {
		t.Fatalf("expected preferred MediaSourceId src-2 first, got %+v", pb.MediaSources)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Videos/"+movie.ID+"/src-1/Subtitles/2/0/Stream.srt", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "沙丘") {
		t.Fatalf("Emby 4.10 subtitle route with startPos failed: %d %s", rec.Code, rec.Body.String())
	}

	// 10. Progress reporting with float PositionTicks + Sections/resume/Items
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Progress", tok, map[string]any{
		"ItemId":        movie.ID,
		"PositionTicks": 1200000000.0,
		"IsPaused":      false,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("float PositionTicks progress failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Sections/resume/Items", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), movie.ID) {
		t.Fatalf("Sections/resume/Items missing resumed movie: %d %s", rec.Code, rec.Body.String())
	}

	// 11. PlayedItems POST /Delete compat + DisplayPreferences POST
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/PlayedItems/"+movie.ID, tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Played":true`) {
		t.Fatalf("mark played failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/PlayedItems/"+movie.ID+"/Delete", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Played":false`) {
		t.Fatalf("PlayedItems POST /Delete failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/DisplayPreferences/usersettings", tok, map[string]any{"SortBy": "SortName"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST DisplayPreferences failed: %d", rec.Code)
	}
}
