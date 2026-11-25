package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestEmbyMigrationImportIsQueuedAsBackgroundTask locks in the async contract of the
// migration wizard: POST /admin/migration/emby/import must return 202 with a task id
// instead of blocking for the whole import, and GET /admin/tasks/:id must expose it.
func TestEmbyMigrationImportIsQueuedAsBackgroundTask(t *testing.T) {
	srv, _ := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "My NAS Emby",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodPost, "/emby/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "SecretPass123!",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("emby login failed: %d %s", rec.Code, rec.Body.String())
	}
	var loginResp struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("parse login resp: %v", err)
	}
	if loginResp.AccessToken == "" {
		t.Fatalf("missing admin access token: %s", rec.Body.String())
	}
	adminTok := loginResp.AccessToken

	// The import endpoint must answer immediately with a queued task, not with a report.
	rec = doJSON(t, h, http.MethodPost, "/api/admin/migration/emby/import", adminTok, map[string]any{
		"emby_config_dir": "/nonexistent/emby-config",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 from async import, got %d %s", rec.Code, rec.Body.String())
	}
	var queued struct {
		TaskID string `json:"task_id"`
		Queued bool   `json:"queued"`
		Task   struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &queued); err != nil {
		t.Fatalf("parse import resp: %v", err)
	}
	if queued.TaskID == "" || !queued.Queued {
		t.Fatalf("import was not queued: %s", rec.Body.String())
	}
	if queued.Task.Type != "migrate_emby" || queued.Task.ID != queued.TaskID {
		t.Fatalf("unexpected queued task: %s", rec.Body.String())
	}

	// The wizard polls this endpoint after a reload, so it must resolve the same task.
	rec = doJSON(t, h, http.MethodGet, "/api/admin/tasks/"+queued.TaskID, adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from task lookup, got %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse task resp: %v", err)
	}
	if got.ID != queued.TaskID || got.Type != "migrate_emby" {
		t.Fatalf("unexpected task payload: %s", rec.Body.String())
	}
	switch got.Status {
	case "pending", "running", "failed":
	default:
		t.Fatalf("unexpected task status %q for a bogus config dir", got.Status)
	}

	rec = doJSON(t, h, http.MethodGet, "/api/admin/tasks/unknown-task-id", adminTok, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown task, got %d %s", rec.Code, rec.Body.String())
	}
}
