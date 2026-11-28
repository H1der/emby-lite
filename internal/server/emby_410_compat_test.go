package server_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-lite/internal/emby"
	"emby-lite/internal/model"
	"emby-lite/internal/scraper"
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

	// 9. /Users/:userId/Items/:itemId/PlaybackInfo & GetItemDetail with MediaSourceId filtering + 4.10 subtitle path with StartPositionTicks
	// 9a. PlaybackInfo with query MediaSourceId returns ONLY the selected version (official Emby/Jellyfin semantics).
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
			ID     string `json:"Id"`
			Width  int    `json:"Width"`
			Height int    `json:"Height"`
		} `json:"MediaSources"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	if len(pb.MediaSources) != 1 || pb.MediaSources[0].ID != "src-2" || pb.MediaSources[0].Height != 1080 {
		t.Fatalf("expected ONLY selected MediaSourceId src-2 in PlaybackInfo, got %+v", pb.MediaSources)
	}

	// 9b. PlaybackInfo with lowercase camelCase mediaSourceId in JSON body also returns ONLY that selected version.
	rec = doJSON(t, h, http.MethodPost, "/emby/Items/"+movie.ID+"/PlaybackInfo", tok, map[string]any{
		"mediaSourceId": "src-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo body mediaSourceId failed: %d %s", rec.Code, rec.Body.String())
	}
	pb.MediaSources = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	if len(pb.MediaSources) != 1 || pb.MediaSources[0].ID != "src-1" || pb.MediaSources[0].Height != 2160 {
		t.Fatalf("expected ONLY selected MediaSourceId src-1 from JSON body, got %+v", pb.MediaSources)
	}

	// 9c. PlaybackInfo without MediaSourceId still returns all versions for client version discovery.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie.ID+"/PlaybackInfo", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo without MediaSourceId failed: %d %s", rec.Code, rec.Body.String())
	}
	pb.MediaSources = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	if len(pb.MediaSources) != 2 || pb.MediaSources[0].ID != "src-1" || pb.MediaSources[1].ID != "src-2" {
		t.Fatalf("expected all 2 MediaSources when MediaSourceId omitted, got %+v", pb.MediaSources)
	}

	// 9d. PlaybackInfo with unknown MediaSourceId returns empty MediaSources.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie.ID+"/PlaybackInfo?MediaSourceId=missing-src", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo unknown MediaSourceId failed: %d %s", rec.Code, rec.Body.String())
	}
	pb.MediaSources = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &pb)
	if len(pb.MediaSources) != 0 {
		t.Fatalf("expected empty MediaSources for unknown MediaSourceId, got %+v", pb.MediaSources)
	}

	// 9e. GetItemDetail with MediaSourceId returns ONLY the selected version and updates top-level media summary.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie.ID+"?MediaSourceId=src-2", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetItemDetail with MediaSourceId failed: %d %s", rec.Code, rec.Body.String())
	}
	var detailSelected struct {
		Width            int    `json:"Width"`
		Height           int    `json:"Height"`
		HasSubtitles     bool   `json:"HasSubtitles"`
		MediaSourceCount int    `json:"MediaSourceCount"`
		MediaSources     []struct {
			ID string `json:"Id"`
		} `json:"MediaSources"`
		MediaStreams []map[string]any `json:"MediaStreams"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &detailSelected)
	if len(detailSelected.MediaSources) != 1 || detailSelected.MediaSources[0].ID != "src-2" {
		t.Fatalf("GetItemDetail?MediaSourceId=src-2 expected only src-2, got %+v", detailSelected.MediaSources)
	}
	if detailSelected.Width != 1920 || detailSelected.Height != 1080 || detailSelected.HasSubtitles || len(detailSelected.MediaStreams) != 0 {
		t.Fatalf("GetItemDetail?MediaSourceId=src-2 did not refresh top-level media summary: %+v", detailSelected)
	}
	if detailSelected.MediaSourceCount != 2 {
		t.Fatalf("GetItemDetail?MediaSourceId=src-2 MediaSourceCount = %d, want 2", detailSelected.MediaSourceCount)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Videos/"+movie.ID+"/src-1/Subtitles/2/0/Stream.srt", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "沙丘") {
		t.Fatalf("Emby 4.10 subtitle route with startPos failed: %d %s", rec.Code, rec.Body.String())
	}

	// 10. Progress reporting with float PositionTicks + Sections/resume/Items
	rec = doJSON(t, h, http.MethodPost, "/emby/Sessions/Playing/Progress", tok, map[string]any{
		"ItemId":        movie.ID,
		"PositionTicks": 6000000000.0, // 600s (~6.45% of 9300s movie, above 5% MinResumePct)
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

	// 4. MediaSources Path exposes STRM target URL for STRM and real local path for local files,
	// while ExternalPlay still builds absolute playback URLs from the request Host/Origin.
	_ = srv.DB.Gorm.Create(&model.MediaSource{
		ID: "spec-src-local-2", ItemID: movie2.ID, SourceType: "local",
		Path: filepath.Join(mediaDir, "b.mkv"), Container: "mkv", FileSize: 1024, Width: 1920, Height: 1080,
	}).Error

	reqLan := httptest.NewRequest(http.MethodGet, "/emby/Users/"+uid+"/Items/Resume?Fields=MediaSources", nil)
	reqLan.Host = "192.168.1.10:8096"
	reqLan.Header.Set("X-Emby-Token", tok)
	recLan := httptest.NewRecorder()
	h.ServeHTTP(recLan, reqLan)
	if !strings.Contains(recLan.Body.String(), `"Path":"https://cdn.example.com/a.mkv"`) || !strings.Contains(recLan.Body.String(), `"Protocol":"Http"`) {
		t.Fatalf("STRM MediaSource Path should expose STRM target URL, got: %s", recLan.Body.String())
	}

	recLocalDetail := doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+movie2.ID, tok, nil)
	var localDetail map[string]any
	_ = json.Unmarshal(recLocalDetail.Body.Bytes(), &localDetail)
	wantLocalPath := filepath.Join(mediaDir, "b.mkv")
	if localDetail["Path"] != wantLocalPath {
		t.Fatalf("local BaseItemDto.Path = %v, want %q", localDetail["Path"], wantLocalPath)
	}
	localSources, _ := localDetail["MediaSources"].([]any)
	if len(localSources) != 1 {
		t.Fatalf("expected 1 local MediaSource, got %v", localSources)
	}
	localMS := localSources[0].(map[string]any)
	if localMS["Path"] != wantLocalPath || localMS["Protocol"] != "File" || localMS["IsRemote"] != false {
		t.Fatalf("local MediaSource Path/Protocol mismatch: %#v", localMS)
	}

	reqExtLan := httptest.NewRequest(http.MethodGet, "/api/web/external-play/"+movie2.ID, nil)
	reqExtLan.Host = "192.168.1.10:8096"
	reqExtLan.Header.Set("X-Emby-Token", tok)
	recExtLan := httptest.NewRecorder()
	h.ServeHTTP(recExtLan, reqExtLan)
	if !strings.Contains(recExtLan.Body.String(), "http://192.168.1.10:8096/emby/Videos/") {
		t.Fatalf("ExternalPlay LAN missing LAN direct_url: %s", recExtLan.Body.String())
	}
	reqExtWan := httptest.NewRequest(http.MethodGet, "/api/web/external-play/"+movie2.ID, nil)
	reqExtWan.Host = "emby.example.com"
	reqExtWan.Header.Set("X-Forwarded-Proto", "https")
	reqExtWan.Header.Set("X-Emby-Token", tok)
	recExtWan := httptest.NewRecorder()
	h.ServeHTTP(recExtWan, reqExtWan)
	if !strings.Contains(recExtWan.Body.String(), "https://emby.example.com/emby/Videos/") {
		t.Fatalf("ExternalPlay WAN missing WAN direct_url: %s", recExtWan.Body.String())
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

func TestDailyUseFixesSimilarPathRandomAndYambySearchSort(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite DailyFixes",
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

	tvDir := filepath.Join(tmp, "tv")
	mvDir := filepath.Join(tmp, "movies")
	_ = os.MkdirAll(tvDir, 0o755)
	_ = os.MkdirAll(mvDir, 0o755)

	createLib := func(name, kind, dir string) string {
		r := doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
			"name":  name,
			"kind":  kind,
			"paths": []string{dir},
		})
		var resp struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp.ID
	}
	tvLibID := createLib("剧集", "tvshows", tvDir)
	mvLibID := createLib("电影", "movies", mvDir)

	now := time.Now().UTC()

	// Seed 3 TV series + multiple episodes so we can verify Episode /Similar returns ONLY other Series.
	s1 := model.Item{ID: "ser-1", LibraryID: tvLibID, ParentID: tvLibID, Type: "Series", Name: "绝命毒师", SortName: "绝命毒师", PinyinInitials: "jmds", IsFolder: true, CommunityRating: 9.5, ScanKey: "df:s1", RandomKey: "rk01", AddedAt: now.Add(-10 * time.Hour), CreatedAt: now, UpdatedAt: now}
	s2 := model.Item{ID: "ser-2", LibraryID: tvLibID, ParentID: tvLibID, Type: "Series", Name: "风骚律师", SortName: "风骚律师", PinyinInitials: "fsls", IsFolder: true, CommunityRating: 9.3, ScanKey: "df:s2", RandomKey: "rk02", AddedAt: now.Add(-20 * time.Hour), CreatedAt: now, UpdatedAt: now}
	s3 := model.Item{ID: "ser-3", LibraryID: tvLibID, ParentID: tvLibID, Type: "Series", Name: "老友记", SortName: "老友记", PinyinInitials: "lyj", IsFolder: true, CommunityRating: 9.0, ScanKey: "df:s3", RandomKey: "rk03", AddedAt: now.Add(-5 * time.Hour), CreatedAt: now, UpdatedAt: now}
	sea1 := model.Item{ID: "sea-1", LibraryID: tvLibID, ParentID: s1.ID, SeriesID: s1.ID, Type: "Season", Name: "第 1 季", SortName: "001", SeasonNum: 1, IsFolder: true, ScanKey: "df:sea1", RandomKey: "rk04", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	ep1 := model.Item{ID: "ep-1", LibraryID: tvLibID, ParentID: sea1.ID, SeriesID: s1.ID, SeasonID: sea1.ID, Type: "Episode", Name: "试播集", SortName: "001-0001", SeasonNum: 1, EpisodeNum: 1, ScanKey: "df:ep1", RandomKey: "rk05", AddedAt: now.Add(-1 * time.Hour), CreatedAt: now, UpdatedAt: now}
	ep2 := model.Item{ID: "ep-2", LibraryID: tvLibID, ParentID: sea1.ID, SeriesID: s1.ID, SeasonID: sea1.ID, Type: "Episode", Name: "猫在袋中", SortName: "001-0002", SeasonNum: 1, EpisodeNum: 2, ScanKey: "df:ep2", RandomKey: "rk06", AddedAt: now.Add(-2 * time.Hour), CreatedAt: now, UpdatedAt: now}
	// Newer episode under s2 (even though s2.AddedAt is older than s1 and s3) for DateLastContentAdded sort check.
	epS2 := model.Item{ID: "ep-s2-1", LibraryID: tvLibID, ParentID: s2.ID, SeriesID: s2.ID, Type: "Episode", Name: "新更一集", SortName: "001-0001", SeasonNum: 1, EpisodeNum: 1, ScanKey: "df:eps2", RandomKey: "rk07", AddedAt: now.Add(-1 * time.Minute), CreatedAt: now, UpdatedAt: now}

	for _, it := range []*model.Item{&s1, &s2, &s3, &sea1, &ep1, &ep2, &epS2} {
		if err := srv.DB.Gorm.Create(it).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: s1.ID, Genre: "犯罪"}).Error
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: s2.ID, Genre: "犯罪"}).Error
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: s3.ID, Genre: "喜剧"}).Error

	// 1. Episode /Similar must recommend ONLY Series (never Episode/Season), exclude its own series, and rank shared-genre Series first.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+ep1.ID+"/Similar?Limit=12", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("episode similar failed: %d %s", rec.Code, rec.Body.String())
	}
	var simResp struct {
		Items []struct {
			ID   string `json:"Id"`
			Type string `json:"Type"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &simResp)
	if len(simResp.Items) != 2 {
		t.Fatalf("expected 2 similar Series for episode, got %+v", simResp.Items)
	}
	for _, it := range simResp.Items {
		if it.Type != "Series" || it.ID == s1.ID {
			t.Fatalf("episode similar must only return other Series, got %+v", simResp.Items)
		}
	}
	if simResp.Items[0].ID != s2.ID {
		t.Fatalf("expected shared-genre series %s first, got %+v", s2.ID, simResp.Items)
	}

	// 2. DateLastContentAdded DESC on Series should put s2 first (newest episode added_at).
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+tvLibID+"&IncludeItemTypes=Series&SortBy=DateLastContentAdded,SortName&SortOrder=Descending", tok, nil)
	var latestContent struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &latestContent)
	if len(latestContent.Items) != 3 || latestContent.Items[0].ID != s2.ID {
		t.Fatalf("DateLastContentAdded DESC expected s2 first, got %+v", latestContent.Items)
	}

	// 3. Seed 12 movies for Random shuffle + Yamby search & sort tests.
	movies := []model.Item{
		{ID: "mv-a", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "阿凡达", OriginalName: "Avatar", SortName: "阿凡达", PinyinInitials: "afdavatar", Year: 2009, PremiereDate: "2009-12-18", CommunityRating: 8.8, StudiosJSON: `["20th Century Fox"]`, ScanKey: "df:mva", RandomKey: "a01", AddedAt: now.Add(-12 * time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: "mv-h", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "哈利·波特与魔法石", OriginalName: "Harry Potter", SortName: "哈利·波特与魔法石", PinyinInitials: "hlbtymfs", Year: 2001, PremiereDate: "2001-11-16", CommunityRating: 9.1, StudiosJSON: `["Warner Bros."]`, ScanKey: "df:mvh", RandomKey: "a02", AddedAt: now.Add(-11 * time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: "mv-s", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "沙丘", OriginalName: "Dune", SortName: "沙丘", PinyinInitials: "sqdune", Year: 2021, PremiereDate: "2021-10-22", CommunityRating: 8.0, StudiosJSON: `["Legendary"]`, ScanKey: "df:mvs", RandomKey: "a03", AddedAt: now.Add(-10 * time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: "mv-x", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "星际穿越", OriginalName: "Interstellar", SortName: "星际穿越", PinyinInitials: "xjcyinterstellar", Year: 0, PremiereDate: "", CommunityRating: 0, StudiosJSON: `["Paramount"]`, ScanKey: "df:mvx", RandomKey: "a04", AddedAt: now.Add(-9 * time.Hour), CreatedAt: now, UpdatedAt: now},
	}
	for i := 5; i <= 12; i++ {
		id := "mv-extra-" + strconv.Itoa(i)
		movies = append(movies, model.Item{
			ID: id, LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie",
			Name: "测试影片 " + strconv.Itoa(i), SortName: "movie " + strconv.Itoa(i), PinyinInitials: "csyp",
			Year: 2010 + i, PremiereDate: strconv.Itoa(2010+i) + "-05-01", CommunityRating: 7.0,
			ScanKey: "df:" + id, RandomKey: "rk" + strconv.Itoa(i), AddedAt: now.Add(-time.Duration(i) * time.Minute), CreatedAt: now, UpdatedAt: now,
		})
	}
	for i := range movies {
		if err := srv.DB.Gorm.Create(&movies[i]).Error; err != nil {
			t.Fatal(err)
		}
		_ = srv.DB.Gorm.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
			movies[i].ID, movies[i].Name, movies[i].OriginalName, movies[i].PinyinInitials).Error
	}
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: "mv-s", Genre: "科幻"}).Error
	_ = srv.DB.Gorm.Create(&model.ItemTag{ItemID: "mv-s", Tag: "IMAX"}).Error
	_ = srv.DB.Gorm.Create(&model.Person{ID: "per-dv", Name: "Denis Villeneuve", UpdatedAt: now}).Error
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: "mv-s", PersonID: "per-dv", RoleType: "Director"}).Error
	_ = srv.DB.Gorm.Save(&model.UserData{UserID: uid, ItemID: "mv-h", Played: true, PlayCount: 7, UpdatedAt: now}).Error

	// 3a. SortBy=Random must produce distinct orders across repeated calls (not static RandomKey!).
	seenFirst := map[string]bool{}
	for i := 0; i < 16; i++ {
		r := doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Recursive=true&Filters=IsNotFolder&MediaTypes=Video&SortBy=Random&Limit=5", tok, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("random query failed: %d %s", r.Code, r.Body.String())
		}
		var randResp struct {
			Items []struct {
				ID   string `json:"Id"`
				Type string `json:"Type"`
			} `json:"Items"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &randResp)
		if len(randResp.Items) != 5 {
			t.Fatalf("expected 5 random items, got %d", len(randResp.Items))
		}
		seenFirst[randResp.Items[0].ID] = true
	}
	if len(seenFirst) < 2 {
		t.Fatalf("SortBy=Random always returned the same first item: %+v", seenFirst)
	}

	// 3b. InstantMix returns randomized playable items.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+mvLibID+"/InstantMix?Limit=5", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"TotalRecordCount":5`) {
		t.Fatalf("InstantMix failed: %d %s", rec.Code, rec.Body.String())
	}

	// 4a. Yamby search: punctuation / FTS special chars never 500; multi-word Chinese search matches; Persons search returns non-zero TotalRecordCount!
	for _, rawQ := range []string{"*", `foo"bar`, "a:b(c)", "Spider-Man"} {
		r := doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Recursive=true&SearchTerm="+rawQ, tok, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("special char SearchTerm=%q returned %d: %s", rawQ, r.Code, r.Body.String())
		}
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?Recursive=true&SearchTerm=%E5%93%88%E5%88%A9%20%E9%AD%94%E6%B3%95%E7%9F%B3", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "mv-h") {
		t.Fatalf("multi-word Chinese search failed: %d %s", rec.Code, rec.Body.String())
	}
	// Persons search via /Persons and via /Items?IncludeItemTypes=Person must return TotalRecordCount=1!
	for _, personURL := range []string{
		"/emby/Persons?SearchTerm=Denis",
		"/emby/Users/" + uid + "/Items?Recursive=true&IncludeItemTypes=Person&SearchTerm=Denis",
	} {
		r := doJSON(t, h, http.MethodGet, personURL, tok, nil)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"TotalRecordCount":1`) || !strings.Contains(r.Body.String(), "Denis Villeneuve") {
			t.Fatalf("GET %s failed: %d %s", personURL, r.Code, r.Body.String())
		}
	}
	// Genres/Studios/Tags SearchTerm must filter instead of returning all entries.
	rec = doJSON(t, h, http.MethodGet, "/emby/Genres?SearchTerm=%E7%A7%91%E5%B9%BB", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "科幻") || strings.Contains(rec.Body.String(), "犯罪") {
		t.Fatalf("GET /Genres?SearchTerm did not filter: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Studios?SearchTerm=Warner", tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Warner Bros.") || strings.Contains(rec.Body.String(), "Legendary") {
		t.Fatalf("GET /Studios?SearchTerm did not filter: %d %s", rec.Code, rec.Body.String())
	}

	// 4b. Yamby sorting: Chinese SortName pinyin order (阿凡达 < 哈利 < 沙丘 < 星际穿越), NameStartsWithOrGreater=S excludes 阿凡达,
	// PremiereDate/CommunityRating ASC put missing/0 values LAST, PlayCount DESC works, repeated SortBy params work.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Ids=mv-a,mv-h,mv-s,mv-x&SortBy=SortName&SortOrder=Ascending", tok, nil)
	var nameOrder struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &nameOrder)
	wantNameIDs := []string{"mv-a", "mv-h", "mv-s", "mv-x"}
	if len(nameOrder.Items) != 4 {
		t.Fatalf("expected 4 movies, got %+v", nameOrder.Items)
	}
	for i, want := range wantNameIDs {
		if nameOrder.Items[i].ID != want {
			t.Fatalf("Chinese SortName order[%d] = %s, want %s (full=%+v)", i, nameOrder.Items[i].ID, want, nameOrder.Items)
		}
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Ids=mv-a,mv-h,mv-s,mv-x&NameStartsWithOrGreater=S", tok, nil)
	if strings.Contains(rec.Body.String(), `"mv-a"`) || !strings.Contains(rec.Body.String(), `"mv-s"`) || !strings.Contains(rec.Body.String(), `"mv-x"`) {
		t.Fatalf("NameStartsWithOrGreater=S wrong filter for Chinese items: %s", rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Ids=mv-a,mv-h,mv-s,mv-x&SortBy=CommunityRating&SortOrder=Ascending", tok, nil)
	var ratingAsc struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ratingAsc)
	if len(ratingAsc.Items) != 4 || ratingAsc.Items[0].ID != "mv-s" || ratingAsc.Items[3].ID != "mv-x" {
		t.Fatalf("CommunityRating ASC must place unrated (0) item mv-x LAST, got %+v", ratingAsc.Items)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Ids=mv-a,mv-h,mv-s,mv-x&SortBy=PremiereDate&SortBy=SortName&SortOrder=Ascending", tok, nil)
	var premiereAsc struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &premiereAsc)
	if len(premiereAsc.Items) != 4 || premiereAsc.Items[0].ID != "mv-h" || premiereAsc.Items[3].ID != "mv-x" {
		t.Fatalf("PremiereDate ASC must place missing date mv-x LAST, got %+v", premiereAsc.Items)
	}

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items?ParentId="+mvLibID+"&Ids=mv-a,mv-h,mv-s,mv-x&SortBy=PlayCount&SortOrder=Descending", tok, nil)
	var playCntDesc struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &playCntDesc)
	if len(playCntDesc.Items) != 4 || playCntDesc.Items[0].ID != "mv-h" {
		t.Fatalf("PlayCount DESC expected mv-h first, got %+v", playCntDesc.Items)
	}

	// 5. Similar items MUST isolate hidden / !ShowInMyMedia / MyMediaExcludes libraries even for admin,
	// and rank by multi-signal similarity (BoxSet, Director, Actor, Tag, multiple Genres).
	hidDir := filepath.Join(tmp, "hidden-lib")
	exclDir := filepath.Join(tmp, "excl-lib")
	_ = os.MkdirAll(hidDir, 0o755)
	_ = os.MkdirAll(exclDir, 0o755)
	falseVal := false
	recHidLib := doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":   "隐藏私密库",
		"kind":   "movies",
		"paths":  []string{hidDir},
		"hidden": true,
	})
	var hidLibResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(recHidLib.Body.Bytes(), &hidLibResp)
	recExclLib := doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":             "首页隐藏库",
		"kind":             "movies",
		"paths":            []string{exclDir},
		"show_in_my_media": &falseVal,
	})
	var exclLibResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(recExclLib.Body.Bytes(), &exclLibResp)

	// Add high-rated movies in hidden & excluded libraries sharing ALL genres/director with mv-s (沙丘).
	hidMov := model.Item{ID: "mv-hidden-1", LibraryID: hidLibResp.ID, ParentID: hidLibResp.ID, Type: "Movie", Name: "隐藏库高分片", SortName: "h1", Year: 2021, CommunityRating: 9.9, ScanKey: "df:hid1", RandomKey: "rh1", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	hidMov2 := model.Item{ID: "mv-hidden-2", LibraryID: hidLibResp.ID, ParentID: hidLibResp.ID, Type: "Movie", Name: "隐藏库同款片", SortName: "h2", Year: 2022, CommunityRating: 8.5, ScanKey: "df:hid2", RandomKey: "rh2", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	exclMov := model.Item{ID: "mv-excl-1", LibraryID: exclLibResp.ID, ParentID: exclLibResp.ID, Type: "Movie", Name: "首页隐藏高分片", SortName: "e1", Year: 2021, CommunityRating: 9.8, ScanKey: "df:exc1", RandomKey: "re1", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	// Normal library movies: mv-s2 (same BoxSet + Director + 2 Genres -> #1), mv-arrival (same Director + 1 Genre -> #2).
	mvS2 := model.Item{ID: "mv-s2", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "沙丘2", SortName: "shaqiu2", Year: 2024, CommunityRating: 8.6, StudiosJSON: `["Legendary"]`, ScanKey: "df:mvs2", RandomKey: "rs2", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	mvArrival := model.Item{ID: "mv-arrival", LibraryID: mvLibID, ParentID: mvLibID, Type: "Movie", Name: "降临", SortName: "jianglin", Year: 2016, CommunityRating: 8.0, ScanKey: "df:mvarrival", RandomKey: "rarr", AddedAt: now, CreatedAt: now, UpdatedAt: now}
	for _, it := range []*model.Item{&hidMov, &hidMov2, &exclMov, &mvS2, &mvArrival} {
		if err := srv.DB.Gorm.Create(it).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Give mv-s multiple genres (科幻, 冒险) to also verify multi-element IN clause!
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: "mv-s", Genre: "冒险"}).Error
	for _, id := range []string{hidMov.ID, exclMov.ID, mvS2.ID} {
		_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: id, Genre: "科幻"}).Error
		_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: id, Genre: "冒险"}).Error
		_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: id, PersonID: "per-dv", RoleType: "Director"}).Error
	}
	_ = srv.DB.Gorm.Create(&model.ItemGenre{ItemID: mvArrival.ID, Genre: "科幻"}).Error
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: mvArrival.ID, PersonID: "per-dv", RoleType: "Director"}).Error
	_ = srv.DB.Gorm.Create(&model.BoxSetItem{BoxSetID: "box-dune", ItemID: "mv-s", SortIdx: 0}).Error
	_ = srv.DB.Gorm.Create(&model.BoxSetItem{BoxSetID: "box-dune", ItemID: mvS2.ID, SortIdx: 1}).Error

	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/mv-s/Similar?Limit=12", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("similar for mv-s failed: %d %s", rec.Code, rec.Body.String())
	}
	var mvSim struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &mvSim)
	if len(mvSim.Items) < 2 {
		t.Fatalf("expected similar movies for mv-s, got %+v", mvSim.Items)
	}
	for _, it := range mvSim.Items {
		if it.ID == hidMov.ID || it.ID == hidMov2.ID || it.ID == exclMov.ID {
			t.Fatalf("normal library similar leaked hidden/excluded library item %s: %+v", it.ID, mvSim.Items)
		}
	}
	if mvSim.Items[0].ID != mvS2.ID || mvSim.Items[1].ID != mvArrival.ID {
		t.Fatalf("expected similarity order [mv-s2, mv-arrival, ...], got %+v", mvSim.Items)
	}

	// Viewing an item INSIDE a hidden library should stay strictly inside that hidden library.
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+hidMov.ID+"/Similar?Limit=12", tok, nil)
	var hidSim struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &hidSim)
	if len(hidSim.Items) != 1 || hidSim.Items[0].ID != hidMov2.ID {
		t.Fatalf("hidden library item similar must stay within same hidden library, got %+v", hidSim.Items)
	}
}

func TestPersonPortraitEndpointsAndTMDBFallback(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite PersonPortrait",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	var setupResp struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &setupResp)
	tok, uid := setupResp.AccessToken, setupResp.User.ID

	// Build a valid tiny JPEG payload for local + mock TMDB image responses.
	var jpgBuf bytes.Buffer
	imgObj := image.NewRGBA(image.Rect(0, 0, 8, 8))
	imgObj.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	if err := jpeg.Encode(&jpgBuf, imgObj, nil); err != nil {
		t.Fatal(err)
	}
	validJPG := jpgBuf.Bytes()

	// Create library + movie.
	mvDir := filepath.Join(tmp, "movies")
	_ = os.MkdirAll(mvDir, 0o755)
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name": "电影", "kind": "movies", "paths": []string{mvDir},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create lib failed: %d %s", rec.Code, rec.Body.String())
	}
	var libResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &libResp)

	now := time.Now().UTC()
	mv := model.Item{
		ID: "mv-actor-1", LibraryID: libResp.ID, ParentID: libResp.ID,
		Type: "Movie", Name: "盗梦空间", SortName: "daomengkongjian", Year: 2010,
		ScanKey: "pa:mv1", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := srv.DB.Gorm.Create(&mv).Error; err != nil {
		t.Fatal(err)
	}

	localThumb := filepath.Join(tmp, "nolan.jpg")
	if err := os.WriteFile(localThumb, validJPG, 0o644); err != nil {
		t.Fatal(err)
	}

	pLocal := model.Person{ID: "per-local", Name: "克里斯托弗·诺兰", TMDBID: "525", ThumbPath: localThumb, UpdatedAt: now}
	pRemoteTMDB := model.Person{ID: "per-tmdb-hit", Name: "莱昂纳多·迪卡普里奥", TMDBID: "6193", ThumbPath: "", UpdatedAt: now}
	pNoPhoto := model.Person{ID: "per-tmdb-miss", Name: "无图演员", TMDBID: "99999", ThumbPath: "", UpdatedAt: now}
	for _, p := range []*model.Person{&pLocal, &pRemoteTMDB, &pNoPhoto} {
		if err := srv.DB.Gorm.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: mv.ID, PersonID: pLocal.ID, RoleType: "Director", SortIdx: 0}).Error
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: mv.ID, PersonID: pRemoteTMDB.ID, RoleType: "Actor", RoleName: "柯布", SortIdx: 1}).Error
	_ = srv.DB.Gorm.Create(&model.ItemPerson{ItemID: mv.ID, PersonID: pNoPhoto.ID, RoleType: "Actor", RoleName: "路人", SortIdx: 2}).Error

	// 1. Standard Emby client routes: /Items/:personId/Images/Primary, /Persons/:personId/Images/Primary/0/:tag, /Users/:uid/Items/:personId.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+pLocal.ID+"/Images/Primary?maxWidth=185", tok, nil)
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 {
		t.Fatalf("GET /Items/:personId/Images/Primary failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Persons/"+pLocal.ID+"/Images/Primary/0/testtag", tok, nil)
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 {
		t.Fatalf("GET /Persons/:personId/Images/Primary/0/:tag failed: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+pLocal.ID, tok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Type":"Person"`) {
		t.Fatalf("GET /Users/:uid/Items/:personId failed: %d %s", rec.Code, rec.Body.String())
	}

	// 2. Without TMDB credentials, persons with empty ThumbPath must NOT emit PrimaryImageTag.
	fetchPeopleTags := func() map[string]string {
		t.Helper()
		r := doJSON(t, h, http.MethodGet, "/emby/Users/"+uid+"/Items/"+mv.ID, tok, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("item detail failed: %d %s", r.Code, r.Body.String())
		}
		var detail struct {
			People []struct {
				ID              string `json:"Id"`
				PrimaryImageTag string `json:"PrimaryImageTag"`
			} `json:"People"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &detail)
		out := map[string]string{}
		for _, p := range detail.People {
			out[p.ID] = p.PrimaryImageTag
		}
		return out
	}
	tagsBefore := fetchPeopleTags()
	if tagsBefore[pLocal.ID] == "" || tagsBefore[pRemoteTMDB.ID] != "" || tagsBefore[pNoPhoto.ID] != "" {
		t.Fatalf("unexpected tags without TMDB creds: %+v", tagsBefore)
	}

	// 3. Configure mock TMDB server: verify exact /person/6193 lookup (not /search/person) + negative caching for 99999.
	var personByIDHits atomic.Int32
	var missHits atomic.Int32
	mockTMDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/person/6193":
			personByIDHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":6193,"profile_path":"/leo.jpg"}`))
		case "/person/99999":
			missHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":99999,"profile_path":""}`))
		case "/search/person":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "/w185/leo.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(validJPG)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockTMDB.Close()

	if err := srv.Scraper.SaveConfig(scraper.TMDBConfig{
		APIKey:       "test-key",
		APIBaseURL:   mockTMDB.URL,
		ImageBaseURL: mockTMDB.URL,
		Language:     "zh-CN",
	}); err != nil {
		t.Fatalf("save TMDB config: %v", err)
	}

	// With TMDB credentials configured, PrimaryImageTag is emitted so clients request the portrait on demand.
	tagsWithTMDB := fetchPeopleTags()
	if tagsWithTMDB[pRemoteTMDB.ID] == "" || tagsWithTMDB[pNoPhoto.ID] == "" {
		t.Fatalf("expected PrimaryImageTag for TMDB on-demand lookup, got %+v", tagsWithTMDB)
	}

	// Requesting via standard Emby /Items/:personId/Images/Primary triggers /person/6193 and persists thumb_path.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+pRemoteTMDB.ID+"/Images/Primary?tag="+tagsWithTMDB[pRemoteTMDB.ID], tok, nil)
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 {
		t.Fatalf("on-demand TMDB portrait via /Items/:personId/Images/Primary failed: %d", rec.Code)
	}
	if personByIDHits.Load() != 1 {
		t.Fatalf("expected exact /person/6193 lookup hit once, got %d", personByIDHits.Load())
	}
	var updatedLeo model.Person
	_ = srv.DB.Gorm.Where("id = ?", pRemoteTMDB.ID).First(&updatedLeo).Error
	if !strings.HasSuffix(updatedLeo.ThumbPath, "/w185/leo.jpg") {
		t.Fatalf("expected persisted TMDB thumb_path, got %q", updatedLeo.ThumbPath)
	}

	// Missing portrait caches negative result so subsequent detail calls stop advertising PrimaryImageTag.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+pNoPhoto.ID+"/Images/Primary?tag="+tagsWithTMDB[pNoPhoto.ID], tok, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for portrait-less person, got %d", rec.Code)
	}
	tagsAfterMiss := fetchPeopleTags()
	if tagsAfterMiss[pNoPhoto.ID] != "" {
		t.Fatalf("expected PrimaryImageTag suppressed after negative cache, got %+v", tagsAfterMiss)
	}
	// Repeat request must hit negative cache without calling TMDB again.
	rec = doJSON(t, h, http.MethodGet, "/emby/Items/"+pNoPhoto.ID+"/Images/Primary", tok, nil)
	if rec.Code != http.StatusNotFound || missHits.Load() != 1 {
		t.Fatalf("expected negative cache to block repeat TMDB lookup, code=%d hits=%d", rec.Code, missHits.Load())
	}
}
