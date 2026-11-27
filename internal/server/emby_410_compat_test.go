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
		// Legacy ProbeJSON emitted before removing omitempty from IsDefault/IsForced.
		ProbeJSON: `[{"Type":"Video","Index":0,"Codec":"hevc","Width":3840,"Height":2160,"VideoRange":"HDR10","IsExternal":false},{"Type":"Audio","Index":1,"Codec":"eac3","Channels":6,"IsExternal":false}]`,
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
	type strictMediaStream struct {
		Type                   string `json:"Type"`
		Index                  *int   `json:"Index"`
		IsDefault              *bool  `json:"IsDefault"`
		IsForced               *bool  `json:"IsForced"`
		IsExternal             *bool  `json:"IsExternal"`
		IsInterlaced           *bool  `json:"IsInterlaced"`
		IsTextSubtitleStream   *bool  `json:"IsTextSubtitleStream"`
		SupportsExternalStream *bool  `json:"SupportsExternalStream"`
	}
	type strictMediaSource struct {
		ID           string              `json:"Id"`
		MediaStreams []strictMediaStream `json:"MediaStreams"`
	}
	assertStrictStreams := func(label string, streams []strictMediaStream, minCount int) {
		t.Helper()
		if len(streams) < minCount {
			t.Fatalf("%s: expected at least %d MediaStreams, got %d", label, minCount, len(streams))
		}
		for i, st := range streams {
			if st.Type == "" || st.Index == nil || st.IsDefault == nil || st.IsForced == nil || st.IsExternal == nil || st.IsInterlaced == nil || st.IsTextSubtitleStream == nil || st.SupportsExternalStream == nil {
				t.Fatalf("%s: stream[%d] missing required EmbyMediaStream fields: %+v", label, i, st)
			}
		}
	}
	var strictItem struct {
		ID           string              `json:"Id"`
		Name         string              `json:"Name"`
		DisplayOrder string              `json:"DisplayOrder"`
		GenreItems   []nameLongIDPair    `json:"GenreItems"`
		Studios      []nameLongIDPair    `json:"Studios"`
		TagItems     []nameLongIDPair    `json:"TagItems"`
		MediaSources []strictMediaSource `json:"MediaSources"`
		MediaStreams []strictMediaStream `json:"MediaStreams"`
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
	if len(strictItem.MediaSources) < 1 {
		t.Fatalf("expected MediaSources in item detail")
	}
	assertStrictStreams("item detail MediaSources[0]", strictItem.MediaSources[0].MediaStreams, 3)
	assertStrictStreams("item detail MediaStreams", strictItem.MediaStreams, 3)

	// Mark favorite and verify Favorites + Search with Fields=MediaSources also normalize legacy ProbeJSON.
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/FavoriteItems/"+movie.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark favorite failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, listPath := range []string{
		"/emby/Users/" + uid + "/Items?Recursive=true&Filters=IsFavorite&Fields=MediaSources",
		"/emby/Users/" + uid + "/Items?Recursive=true&SearchTerm=Dune&Fields=MediaSources",
	} {
		rec = doJSON(t, h, http.MethodGet, listPath, tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s failed: %d %s", listPath, rec.Code, rec.Body.String())
		}
		var listResp struct {
			Items []struct {
				ID           string              `json:"Id"`
				MediaSources []strictMediaSource `json:"MediaSources"`
			} `json:"Items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
			t.Fatalf("decode %s failed: %v", listPath, err)
		}
		if len(listResp.Items) != 1 || len(listResp.Items[0].MediaSources) < 1 {
			t.Fatalf("GET %s expected 1 item with MediaSources, got %s", listPath, rec.Body.String())
		}
		assertStrictStreams(listPath, listResp.Items[0].MediaSources[0].MediaStreams, 3)
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

func TestEpisodeMultiVersionMediaSourceName(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite EpVersion",
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

	mediaDir := filepath.Join(tmp, "tv")
	_ = os.MkdirAll(mediaDir, 0o755)
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", login.AccessToken, map[string]any{
		"name":  "剧集",
		"kind":  "tvshows",
		"paths": []string{mediaDir},
	})
	var libResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &libResp)

	now := time.Now().UTC()
	series := model.Item{
		ID: "series-lj", LibraryID: libResp.ID, ParentID: libResp.ID, Type: "Series",
		Name: "两京十五日", SortName: "liangjingshiwuri", ScanKey: "tv:lj", RandomKey: "r1",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	season := model.Item{
		ID: "season-lj-1", LibraryID: libResp.ID, ParentID: series.ID, SeriesID: series.ID, Type: "Season",
		Name: "第 1 季", SortName: "s01", SeasonNum: 1, ScanKey: "tv:lj:s1", RandomKey: "r2",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	// Intentionally omit SeriesID on ep to verify SeasonID -> Series lookup works too.
	ep := model.Item{
		ID: "ep-lj-14", LibraryID: libResp.ID, ParentID: season.ID, SeasonID: season.ID, Type: "Episode",
		Name: "泥", SortName: "s01e14", SeasonNum: 1, EpisodeNum: 14, ScanKey: "tv:lj:s1e14", RandomKey: "r3",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	for _, it := range []*model.Item{&series, &season, &ep} {
		if err := srv.DB.Gorm.Create(it).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "ep-src-hdr", ItemID: ep.ID, SourceType: "strm", Path: filepath.Join(mediaDir, "两京十五日 S01E14 2160p.HDR.strm"),
		StrmURL: "https://example.com/hdr.mkv", Container: "mkv", Width: 3840, Height: 2160, Bitrate: 12790000, HDRType: "HDR",
		VersionLabel: "4K HDR",
	}).Error
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "ep-src-sdr", ItemID: ep.ID, SourceType: "strm", Path: filepath.Join(mediaDir, "两京十五日 S01E14 2160p.strm"),
		StrmURL: "https://example.com/sdr.mkv", Container: "mkv", Width: 3840, Height: 2160, Bitrate: 4780000,
		VersionLabel: "4K",
	}).Error

	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/emby/Users/" + login.User.ID + "/Items/" + ep.ID},
		{http.MethodPost, "/emby/Users/" + login.User.ID + "/Items/" + ep.ID + "/PlaybackInfo"},
	} {
		rec = doJSON(t, h, endpoint.method, endpoint.path, login.AccessToken, map[string]any{})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s failed: %d %s", endpoint.method, endpoint.path, rec.Code, rec.Body.String())
		}
		var resp struct {
			MediaSources []struct {
				ID   string `json:"Id"`
				Name string `json:"Name"`
			} `json:"MediaSources"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s failed: %v", endpoint.path, err)
		}
		if len(resp.MediaSources) != 2 {
			t.Fatalf("%s: expected 2 MediaSources, got %+v", endpoint.path, resp.MediaSources)
		}
		if resp.MediaSources[0].Name != "两京十五日 S01E14 4K HDR" {
			t.Fatalf("%s: MediaSources[0].Name = %q, want %q", endpoint.path, resp.MediaSources[0].Name, "两京十五日 S01E14 4K HDR")
		}
		if resp.MediaSources[1].Name != "两京十五日 S01E14 4K" {
			t.Fatalf("%s: MediaSources[1].Name = %q, want %q", endpoint.path, resp.MediaSources[1].Name, "两京十五日 S01E14 4K")
		}
	}
}

func TestEmbyRestAPISchemaAlignment(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite SpecAudit",
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

	mediaDir := filepath.Join(tmp, "spec")
	_ = os.MkdirAll(mediaDir, 0o755)
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "综合库",
		"kind":  "mixed",
		"paths": []string{mediaDir},
	})
	var libResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &libResp)

	now := time.Now().UTC()
	movie1 := model.Item{
		ID: "spec-mov-1", LibraryID: libResp.ID, ParentID: libResp.ID, Type: "Movie",
		Name: "电影A", SortName: "a", RuntimeTicks: 7200 * 1e7, ScanKey: "spec:m1", RandomKey: "r1",
		AddedAt: now.Add(-2 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	movie2 := model.Item{
		ID: "spec-mov-2", LibraryID: libResp.ID, ParentID: libResp.ID, Type: "Movie",
		Name: "电影B", SortName: "b", RuntimeTicks: 7200 * 1e7, ScanKey: "spec:m2", RandomKey: "r2",
		AddedAt: now.Add(-1 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	series := model.Item{
		ID: "spec-ser-1", LibraryID: libResp.ID, ParentID: libResp.ID, Type: "Series",
		Name: "剧集S", SortName: "s", IsFolder: true, ScanKey: "spec:s1", RandomKey: "r3",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	season := model.Item{
		ID: "spec-sea-1", LibraryID: libResp.ID, ParentID: series.ID, SeriesID: series.ID, Type: "Season",
		Name: "第 1 季", SortName: "s01", SeasonNum: 1, IsFolder: true, ScanKey: "spec:sea1", RandomKey: "r4",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	ep1 := model.Item{
		ID: "spec-ep-1", LibraryID: libResp.ID, ParentID: season.ID, SeriesID: series.ID, SeasonID: season.ID, Type: "Episode",
		Name: "E01", SortName: "s01e01", SeasonNum: 1, EpisodeNum: 1, RuntimeTicks: 3000 * 1e7, ScanKey: "spec:ep1", RandomKey: "r5",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	ep2 := model.Item{
		ID: "spec-ep-2", LibraryID: libResp.ID, ParentID: season.ID, SeriesID: series.ID, SeasonID: season.ID, Type: "Episode",
		Name: "E02", SortName: "s01e02", SeasonNum: 1, EpisodeNum: 2, RuntimeTicks: 3000 * 1e7, ScanKey: "spec:ep2", RandomKey: "r6",
		AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	for _, it := range []*model.Item{&movie1, &movie2, &series, &season, &ep1, &ep2} {
		if err := srv.DB.Gorm.Create(it).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Legacy migrated ProbeJSON containing empty TimeBase, typo ExtendedVideoSubtype, and missing DisplayTitle/Protocol.
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "spec-src-1", ItemID: movie1.ID, SourceType: "strm", StrmURL: "https://cdn.example.com/a.mkv",
		Path: filepath.Join(mediaDir, "a.strm"), Container: "mkv", FileSize: 0, Width: 3840, Height: 2160, VideoCodec: "hevc", HDRType: "HDR10",
		ProbeJSON: `[{"Type":"Video","Index":0,"Codec":"hevc","Width":3840,"Height":2160,"VideoRange":"HDR10","TimeBase":"","CodecTag":"","ExtendedVideoSubtype":"DoviProfile8"},{"Type":"Audio","Index":1,"Codec":"eac3","Language":"chi","Channels":6,"IsDefault":true},{"Type":"Subtitle","Index":2,"Codec":"srt","Language":"chi"}]`,
	}).Error
	_ = srv.DB.Gorm.Create(&model.IntroMarker{
		ID: "spec-intro-1", ItemID: movie1.ID, IntroStartTicks: 10 * 1e7, IntroEndTicks: 90 * 1e7, CreditsStartTicks: 6900 * 1e7,
	}).Error

	// 1. Verify MediaSourceInfo & MediaStream & BaseItemDto & ChapterInfo & UserData fields.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie1.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("item detail failed: %d %s", rec.Code, rec.Body.String())
	}
	var itemDetail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &itemDetail); err != nil {
		t.Fatalf("unmarshal item detail: %v", err)
	}
	if itemDetail["Width"] != float64(3840) || itemDetail["Height"] != float64(2160) || itemDetail["IsHD"] != true || itemDetail["HasSubtitles"] != true {
		t.Fatalf("BaseItemDto media summary mismatch: Width=%v Height=%v IsHD=%v HasSubtitles=%v", itemDetail["Width"], itemDetail["Height"], itemDetail["IsHD"], itemDetail["HasSubtitles"])
	}
	udMap, _ := itemDetail["UserData"].(map[string]any)
	if udMap["ItemId"] != movie1.ID {
		t.Fatalf("UserData.ItemId = %v, want %s", udMap["ItemId"], movie1.ID)
	}
	chapters, _ := itemDetail["Chapters"].([]any)
	if len(chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(chapters))
	}
	for i, rawCh := range chapters {
		ch := rawCh.(map[string]any)
		if ch["ChapterIndex"] != float64(i) {
			t.Fatalf("chapter[%d].ChapterIndex = %v, want %d", i, ch["ChapterIndex"], i)
		}
	}
	sources, _ := itemDetail["MediaSources"].([]any)
	ms0 := sources[0].(map[string]any)
	if ms0["ItemId"] != movie1.ID {
		t.Fatalf("MediaSourceInfo.ItemId = %v, want %s", ms0["ItemId"], movie1.ID)
	}
	if _, hasZeroSize := ms0["Size"]; hasZeroSize {
		t.Fatalf("unprobed STRM MediaSourceInfo with FileSize=0 must omit Size, got %v", ms0["Size"])
	}
	for _, key := range []string{"Formats", "RequiredHttpHeaders", "MediaAttachments", "RequiresLooping", "HasSegments"} {
		if _, exists := ms0[key]; !exists {
			t.Fatalf("MediaSourceInfo missing standard Emby field %q", key)
		}
	}
	streams := ms0["MediaStreams"].([]any)
	v0 := streams[0].(map[string]any)
	if v0["Protocol"] != "Http" || v0["ExtendedVideoType"] != "Hdr10" || v0["ExtendedVideoSubType"] != "DoviProfile8" {
		t.Fatalf("video stream alignment mismatch: %#v", v0)
	}
	if _, hasTypo := v0["ExtendedVideoSubtype"]; hasTypo {
		t.Fatalf("legacy typo key ExtendedVideoSubtype must be removed: %#v", v0)
	}
	if _, hasEmptyTB := v0["TimeBase"]; hasEmptyTB {
		t.Fatalf("empty TimeBase should be omitted: %#v", v0)
	}
	if dt, _ := v0["DisplayTitle"].(string); !strings.Contains(dt, "4K") || !strings.Contains(dt, "HEVC") {
		t.Fatalf("video stream missing generated DisplayTitle: %q", dt)
	}
	sub2 := streams[2].(map[string]any)
	if sub2["DeliveryMethod"] != "Embed" || sub2["IsTextSubtitleStream"] != true {
		t.Fatalf("embedded subtitle alignment mismatch: %#v", sub2)
	}

	// 2. Series & Season UnplayedItemCount + updating after marking ep1 played.
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/PlayedItems/"+ep1.ID, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark ep1 played failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+libResp.ID+"&IncludeItemTypes=Series", tok, nil)
	var seriesList struct {
		Items []struct {
			ID       string `json:"Id"`
			UserData struct {
				UnplayedItemCount *int `json:"UnplayedItemCount"`
				ItemID            string `json:"ItemId"`
			} `json:"UserData"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &seriesList)
	if len(seriesList.Items) != 1 || seriesList.Items[0].UserData.UnplayedItemCount == nil || *seriesList.Items[0].UserData.UnplayedItemCount != 1 {
		t.Fatalf("expected Series UnplayedItemCount=1, got %s", rec.Body.String())
	}

	// 3. Rewatching an already-played movie clears Played so it reappears in Continue Watching.
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+uid+"/PlayedItems/"+movie1.ID, tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Played":true`) {
		t.Fatalf("mark movie1 played failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Progress", tok, map[string]any{
		"ItemId":        movie1.ID,
		"PositionTicks": int64(1800 * 1e7), // 25% of 7200s
		"IsPaused":      false,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("rewatch progress failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?Fields=MediaSources", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), movie1.ID) {
		t.Fatalf("rewatched movie1 missing from Resume: %d %s", rec.Code, rec.Body.String())
	}

	// 4. Resume cache isolates distinct request Host/Origin when Fields=MediaSources is requested.
	reqLan := httptest.NewRequest(http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?Fields=MediaSources", nil)
	reqLan.Host = "192.168.1.10:8096"
	reqLan.Header.Set("X-Emby-Token", tok)
	recLan := httptest.NewRecorder()
	h.ServeHTTP(recLan, reqLan)
	if !strings.Contains(recLan.Body.String(), "http://192.168.1.10:8096/emby/Videos/") {
		t.Fatalf("LAN resume missing LAN origin: %s", recLan.Body.String())
	}
	reqWan := httptest.NewRequest(http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?Fields=MediaSources", nil)
	reqWan.Host = "emby.example.com"
	reqWan.Header.Set("X-Forwarded-Proto", "https")
	reqWan.Header.Set("X-Emby-Token", tok)
	recWan := httptest.NewRecorder()
	h.ServeHTTP(recWan, reqWan)
	if !strings.Contains(recWan.Body.String(), "https://emby.example.com/emby/Videos/") {
		t.Fatalf("WAN resume polluted by LAN cached origin: %s", recWan.Body.String())
	}

	// 5. SortBy=DatePlayed orders by actual user_data.last_played_at rather than items.added_at.
	tOld := now.Add(-1 * time.Hour)
	tNew := now.Add(-5 * time.Minute)
	_ = srv.DB.Gorm.Save(&model.UserData{UserID: uid, ItemID: movie1.ID, Played: true, LastPlayedAt: &tNew, UpdatedAt: tNew}).Error
	_ = srv.DB.Gorm.Save(&model.UserData{UserID: uid, ItemID: movie2.ID, Played: true, LastPlayedAt: &tOld, UpdatedAt: tOld}).Error
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Recursive=true&IncludeItemTypes=Movie&SortBy=DatePlayed&SortOrder=Descending", tok, nil)
	var playedOrder struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &playedOrder)
	if len(playedOrder.Items) != 2 || playedOrder.Items[0].ID != movie1.ID {
		t.Fatalf("SortBy=DatePlayed DESC expected movie1 first (more recently played despite older added_at), got %+v", playedOrder.Items)
	}

	// 6. Full ItemCounts schema + user-scoped alias routes.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/Counts", tok, nil)
	var counts map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &counts); err != nil {
		t.Fatalf("decode /Items/Counts: %v", err)
	}
	for _, k := range []string{"MovieCount", "SeriesCount", "EpisodeCount", "BoxSetCount", "SongCount", "AlbumCount", "ArtistCount", "MusicVideoCount", "BookCount", "TrailerCount", "ItemCount"} {
		if _, ok := counts[k]; !ok {
			t.Fatalf("/Items/Counts missing standard field %q: %+v", k, counts)
		}
	}
	if counts["MovieCount"] != 2 || counts["SeriesCount"] != 1 || counts["EpisodeCount"] != 2 || counts["ItemCount"] != 5 {
		t.Fatalf("unexpected counts: %+v", counts)
	}

	for _, aliasPath := range []string{
		"/emby/Users/" + uid + "/Items/" + movie1.ID + "/Images",
		"/emby/Users/" + uid + "/Items/" + movie1.ID + "/ThemeMedia",
		"/emby/Users/" + uid + "/Items/" + movie1.ID + "/ThemeSongs",
		"/emby/Users/" + uid + "/Items/" + movie1.ID + "/ThemeVideos",
		"/emby/Users/" + uid + "/Items/" + movie1.ID + "/ThumbnailSet",
		"/emby/Users/" + uid + "/MediaSegments/" + movie1.ID,
	} {
		rec = doJSON(t, h, http.MethodGet, aliasPath, tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s failed: %d %s", aliasPath, rec.Code, rec.Body.String())
		}
	}
}
