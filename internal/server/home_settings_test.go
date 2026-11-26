package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-lite/internal/model"
)

func TestLibraryOrderingAndHomeSectionVisibility(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	// Setup admin
	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "EmbyLite Home Settings Test",
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
	adminTok := setupResp.AccessToken
	adminUID := setupResp.User.ID

	// Create 3 libraries (movies, tvshows, boxsets)
	dirA := filepath.Join(tmp, "libA")
	dirB := filepath.Join(tmp, "libB")
	_ = os.MkdirAll(dirA, 0o755)
	_ = os.MkdirAll(dirB, 0o755)

	createLib := func(name, kind string, paths []string) string {
		r := doJSON(t, h, http.MethodPost, "/api/admin/libraries", adminTok, map[string]any{
			"name":  name,
			"kind":  kind,
			"paths": paths,
		})
		if r.Code != http.StatusCreated {
			t.Fatalf("create library %s failed: %d %s", name, r.Code, r.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &out)
		return out.ID
	}

	libMovie := createLib("电影", "movies", []string{dirA})
	libTV := createLib("剧集", "tvshows", []string{dirB})
	libBox := createLib("合集", "boxsets", nil)

	// 1. Admin global library reorder: [libBox, libTV, libMovie]
	rec = doJSON(t, h, http.MethodPut, "/api/admin/libraries/reorder", adminTok, map[string]any{
		"library_ids": []string{libBox, libTV, libMovie},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder libraries failed: %d %s", rec.Code, rec.Body.String())
	}

	// Editing libTV without sort_order must preserve its sort_order!
	rec = doJSON(t, h, http.MethodPut, "/api/admin/libraries/"+libTV, adminTok, map[string]any{
		"name":  "剧集",
		"kind":  "tvshows",
		"paths": []string{dirB},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update library failed: %d %s", rec.Code, rec.Body.String())
	}

	getViewIDs := func(tok, uid string, includeHidden bool) []string {
		path := "/emby/Users/" + uid + "/Views"
		if includeHidden {
			path += "?IncludeHidden=true"
		}
		r := doJSON(t, h, http.MethodGet, path, tok, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("GET %s failed: %d %s", path, r.Code, r.Body.String())
		}
		var resp struct {
			Items []struct {
				ID string `json:"Id"`
			} `json:"Items"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		ids := make([]string, 0, len(resp.Items))
		for _, it := range resp.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}

	gotOrder := getViewIDs(adminTok, adminUID, false)
	if len(gotOrder) != 3 || gotOrder[0] != libBox || gotOrder[1] != libTV || gotOrder[2] != libMovie {
		t.Fatalf("global reorder not reflected in Views: got %v, want [%s %s %s]", gotOrder, libBox, libTV, libMovie)
	}

	// Seed media items for resume, nextup, and latest across libMovie and libTV
	now := time.Now().UTC()
	movie := model.Item{
		ID: "mov-1", LibraryID: libMovie, ParentID: libMovie, Type: "Movie",
		Name: "Movie One", SortName: "movie one", RuntimeTicks: 6000 * 1e7,
		ScanKey: "hs:mov:1", RandomKey: "r1", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	series := model.Item{
		ID: "ser-1", LibraryID: libTV, ParentID: libTV, Type: "Series",
		Name: "Series One", SortName: "series one", IsFolder: true,
		ScanKey: "hs:ser:1", RandomKey: "r2", AddedAt: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	ep1 := model.Item{
		ID: "ep-1", LibraryID: libTV, ParentID: "ser-1", SeriesID: "ser-1", Type: "Episode",
		Name: "Ep 1", SortName: "ep 1", SeasonNum: 1, EpisodeNum: 1, RuntimeTicks: 3000 * 1e7,
		ScanKey: "hs:ep:1", RandomKey: "r3", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	ep2 := model.Item{
		ID: "ep-2", LibraryID: libTV, ParentID: "ser-1", SeriesID: "ser-1", Type: "Episode",
		Name: "Ep 2", SortName: "ep 2", SeasonNum: 1, EpisodeNum: 2, RuntimeTicks: 3000 * 1e7,
		ScanKey: "hs:ep:2", RandomKey: "r4", AddedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	for _, it := range []model.Item{movie, series, ep1, ep2} {
		if err := srv.DB.Gorm.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Mark movie in-progress (resume), ep1 played (so ep2 is NextUp!)
	_ = srv.DB.Gorm.Create(&model.UserData{
		UserID: adminUID, ItemID: movie.ID, PositionTicks: 1000 * 1e7, Played: false, LastPlayedAt: &now, UpdatedAt: now,
	}).Error
	_ = srv.DB.Gorm.Create(&model.UserData{
		UserID: adminUID, ItemID: ep1.ID, Played: true, PlayCount: 1, LastPlayedAt: &now, UpdatedAt: now,
	}).Error

	// 2. Per-user Emby POST /Users/:id/Configuration: custom OrderedViews + excludes
	rec = doJSON(t, h, http.MethodPost, "/emby/Users/"+adminUID+"/Configuration", adminTok, map[string]any{
		"OrderedViews":        []string{libMovie, libTV, libBox},
		"MyMediaExcludes":     []string{libBox},
		"ResumeExcludes":      []string{libMovie},
		"NextUpExcludes":      []string{libTV},
		"LatestItemsExcludes": []string{libMovie},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST User Configuration failed: %d %s", rec.Code, rec.Body.String())
	}

	// Verify /Users/Me reflects persisted configuration
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/Me", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Users/Me failed: %d", rec.Code)
	}
	var me struct {
		Configuration struct {
			OrderedViews        []string `json:"OrderedViews"`
			MyMediaExcludes     []string `json:"MyMediaExcludes"`
			LatestItemsExcludes []string `json:"LatestItemsExcludes"`
			ResumeExcludes      []string `json:"ResumeExcludes"`
			NextUpExcludes      []string `json:"NextUpExcludes"`
		} `json:"Configuration"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if len(me.Configuration.OrderedViews) != 3 || me.Configuration.OrderedViews[0] != libMovie {
		t.Fatalf("OrderedViews mismatch: %+v", me.Configuration.OrderedViews)
	}
	if len(me.Configuration.MyMediaExcludes) != 1 || me.Configuration.MyMediaExcludes[0] != libBox {
		t.Fatalf("MyMediaExcludes mismatch: %+v", me.Configuration.MyMediaExcludes)
	}

	// Views without IncludeHidden hides libBox and follows OrderedViews [libMovie, libTV]
	views := getViewIDs(adminTok, adminUID, false)
	if len(views) != 2 || views[0] != libMovie || views[1] != libTV {
		t.Fatalf("Views filtered/ordered mismatch: got %v, want [%s %s]", views, libMovie, libTV)
	}
	// Views WITH IncludeHidden=true returns all 3 in custom order
	allViews := getViewIDs(adminTok, adminUID, true)
	if len(allViews) != 3 || allViews[0] != libMovie || allViews[1] != libTV || allViews[2] != libBox {
		t.Fatalf("Views IncludeHidden mismatch: got %v", allViews)
	}

	// Resume excludes libMovie -> empty
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+adminUID+"/Items/Resume", adminTok, nil)
	var itemsResp struct {
		Items []struct {
			ID string `json:"Id"`
		} `json:"Items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &itemsResp)
	if len(itemsResp.Items) != 0 {
		t.Fatalf("expected Resume empty when libMovie excluded, got %+v", itemsResp.Items)
	}

	// NextUp excludes libTV -> empty
	rec = doJSON(t, h, http.MethodGet, "/emby/Shows/NextUp?UserId="+adminUID, adminTok, nil)
	itemsResp.Items = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &itemsResp)
	if len(itemsResp.Items) != 0 {
		t.Fatalf("expected NextUp empty when libTV excluded, got %+v", itemsResp.Items)
	}

	// Latest excludes libMovie -> only series from libTV remains
	rec = doJSON(t, h, http.MethodGet, "/emby/Users/"+adminUID+"/Items/Latest", adminTok, nil)
	var latestArr []struct {
		ID string `json:"Id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &latestArr)
	if len(latestArr) != 1 || latestArr[0].ID != series.ID {
		t.Fatalf("expected Latest to contain only series %s, got %+v", series.ID, latestArr)
	}

	// WebHome reflects all 4 filters + ordering simultaneously
	rec = doJSON(t, h, http.MethodGet, "/api/web/home", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("WebHome failed: %d %s", rec.Code, rec.Body.String())
	}
	var homeResp struct {
		Views  []struct{ ID string `json:"Id"` } `json:"views"`
		Resume []struct{ ID string `json:"Id"` } `json:"resume"`
		NextUp []struct{ ID string `json:"Id"` } `json:"next_up"`
		Latest []struct{ ID string `json:"Id"` } `json:"latest"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &homeResp)
	if len(homeResp.Views) != 2 || homeResp.Views[0].ID != libMovie || len(homeResp.Resume) != 0 || len(homeResp.NextUp) != 0 || len(homeResp.Latest) != 1 || homeResp.Latest[0].ID != series.ID {
		t.Fatalf("WebHome unexpected payload: %+v", homeResp)
	}

	// 3. Web home-settings API: re-enable everything & reset order
	rec = doJSON(t, h, http.MethodPut, "/api/web/home-settings", adminTok, map[string]any{
		"reset_order": true,
		"libraries": []map[string]any{
			{"id": libMovie, "show_in_my_media": true, "show_in_resume": true, "show_in_next_up": true, "show_in_latest": true},
			{"id": libTV, "show_in_my_media": true, "show_in_resume": true, "show_in_next_up": true, "show_in_latest": true},
			{"id": libBox, "show_in_my_media": true, "show_in_resume": true, "show_in_next_up": true, "show_in_latest": true},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/web/home-settings failed: %d %s", rec.Code, rec.Body.String())
	}

	// Global library-level disable: turn off ShowInNextUp & ShowInMyMedia on libTV globally
	falseVal := false
	rec = doJSON(t, h, http.MethodPut, "/api/admin/libraries/"+libTV, adminTok, map[string]any{
		"name":             "剧集",
		"kind":             "tvshows",
		"paths":            []string{dirB},
		"show_in_my_media": &falseVal,
		"show_in_next_up":  &falseVal,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update libTV global visibility failed: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/api/web/home", adminTok, nil)
	homeResp = struct {
		Views  []struct{ ID string `json:"Id"` } `json:"views"`
		Resume []struct{ ID string `json:"Id"` } `json:"resume"`
		NextUp []struct{ ID string `json:"Id"` } `json:"next_up"`
		Latest []struct{ ID string `json:"Id"` } `json:"latest"`
	}{}
	_ = json.Unmarshal(rec.Body.Bytes(), &homeResp)
	// Global order restored to [libBox, libMovie] (libTV hidden globally from MyMedia & NextUp), movie back in Resume!
	if len(homeResp.Views) != 2 || homeResp.Views[0].ID != libBox || homeResp.Views[1].ID != libMovie {
		t.Fatalf("global ShowInMyMedia/order mismatch: %+v", homeResp.Views)
	}
	if len(homeResp.Resume) != 1 || homeResp.Resume[0].ID != movie.ID {
		t.Fatalf("expected movie back in Resume: %+v", homeResp.Resume)
	}
	if len(homeResp.NextUp) != 0 {
		t.Fatalf("expected NextUp empty when libTV global ShowInNextUp=false: %+v", homeResp.NextUp)
	}
}
