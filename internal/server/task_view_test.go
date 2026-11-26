package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-lite/internal/model"
)

func TestAdminTasksLibraryNameAndDurationEnrichment(t *testing.T) {
	srv, tmp := setupTestServer(t)
	h := srv.Handler

	rec := doJSON(t, h, http.MethodPost, "/api/web/setup", "", map[string]any{
		"username":    "admin",
		"password":    "SecretPass123!",
		"server_name": "Task Test Server",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	var setupResp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &setupResp)
	tok := setupResp.AccessToken

	mediaDir := filepath.Join(tmp, "movies")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(mediaDir, "Arrival (2016).mkv"), []byte("FAKE"), 0o644)

	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries", tok, map[string]any{
		"name":  "蓝光电影",
		"kind":  "movies",
		"paths": []string{mediaDir},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library failed: %d %s", rec.Code, rec.Body.String())
	}
	var libResp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &libResp)
	libID := libResp.ID

	// 1. Insert a historical task row (old message format without library name) to verify backward enrichment.
	started := time.Now().UTC().Add(-2 * time.Hour)
	finished := started.Add(1*time.Hour + 15*time.Minute + 23*time.Second)
	histTask := model.BackgroundTask{
		ID:          "hist-task-1",
		Type:        "scan",
		Status:      "completed",
		PayloadJSON: `{"library_id":"` + libID + `"}`,
		Message:     "完成 (4523000ms)：新增 1，移动 0，更新 0，跳过 0，移除 0",
		CreatedAt:   started.Add(-10 * time.Second),
		StartedAt:   &started,
		FinishedAt:  &finished,
	}
	if err := srv.DB.Gorm.Create(&histTask).Error; err != nil {
		t.Fatal(err)
	}

	// 2. Trigger a live scan task and wait for completion.
	rec = doJSON(t, h, http.MethodPost, "/api/admin/libraries/"+libID+"/scan", tok, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scan enqueue failed: %d %s", rec.Code, rec.Body.String())
	}
	var enqResp struct {
		Task struct {
			ID          string `json:"id"`
			LibraryID   string `json:"library_id"`
			LibraryName string `json:"library_name"`
			Message     string `json:"message"`
		} `json:"task"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &enqResp)
	if enqResp.Task.LibraryID != libID || enqResp.Task.LibraryName != "蓝光电影" {
		t.Fatalf("enqueued task missing library enrichment: %+v", enqResp.Task)
	}

	// Poll GetTask until completed.
	var completedTask struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		LibraryID   string `json:"library_id"`
		LibraryName string `json:"library_name"`
		DurationMs  int64  `json:"duration_ms"`
		Message     string `json:"message"`
		FinishedAt  string `json:"finished_at"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := doJSON(t, h, http.MethodGet, "/api/admin/tasks/"+enqResp.Task.ID, tok, nil)
		if r.Code == http.StatusOK {
			_ = json.Unmarshal(r.Body.Bytes(), &completedTask)
			if completedTask.Status == "completed" || completedTask.Status == "failed" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if completedTask.Status != "completed" {
		t.Fatalf("scan task did not complete: %+v", completedTask)
	}
	if completedTask.LibraryName != "蓝光电影" {
		t.Fatalf("GetTask missing library_name: %+v", completedTask)
	}
	if !strings.Contains(completedTask.Message, "【蓝光电影】") || !strings.Contains(completedTask.Message, "耗时") {
		t.Fatalf("scan completion message missing library name or formatted duration: %q", completedTask.Message)
	}

	// 3. Verify ListTasks enriches both the live task and the historical task.
	rec = doJSON(t, h, http.MethodGet, "/api/admin/tasks", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListTasks failed: %d %s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Items []struct {
			ID          string `json:"id"`
			LibraryID   string `json:"library_id"`
			LibraryName string `json:"library_name"`
			DurationMs  int64  `json:"duration_ms"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	foundHist := false
	for _, it := range listResp.Items {
		if it.ID == histTask.ID {
			foundHist = true
			if it.LibraryID != libID || it.LibraryName != "蓝光电影" {
				t.Fatalf("historical task not enriched with library name: %+v", it)
			}
			if it.DurationMs != 4523000 {
				t.Fatalf("historical task duration_ms = %d, want 4523000", it.DurationMs)
			}
		}
	}
	if !foundHist {
		t.Fatalf("historical task not found in ListTasks response")
	}
}
