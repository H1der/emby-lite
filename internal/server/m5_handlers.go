package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/files"
	"emby-lite/internal/migration"
	"emby-lite/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (a *AdminHandler) recordAudit(c *gin.Context, action, targetType, targetID string, detail map[string]any) {
	if a.audit == nil {
		return
	}
	uid := ""
	if v, ok := c.Get("admin_principal"); ok {
		if p, ok := v.(*auth.Principal); ok && p.User != nil {
			uid = p.User.ID
		}
	}
	a.audit.Record(uid, action, targetType, targetID, c.ClientIP(), detail)
}

// --- Restricted File Manager (/api/files/*) ---

func (a *AdminHandler) FilesList(c *gin.Context) {
	cur, parent, roots, entries, err := a.files.List(c.Query("path"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"current": cur,
		"parent":  parent,
		"roots":   roots,
		"entries": entries,
	})
}

func (a *AdminHandler) FilesMkdir(c *gin.Context) {
	var body struct {
		Parent string `json:"parent" binding:"required"`
		Name   string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := a.files.Mkdir(body.Parent, body.Name)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "files.mkdir", "dir", created, nil)
	c.JSON(http.StatusCreated, gin.H{"path": created})
}

func (a *AdminHandler) FilesRename(c *gin.Context) {
	var body struct {
		Path    string `json:"path" binding:"required"`
		NewName string `json:"new_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	renamed, err := a.files.Rename(body.Path, body.NewName)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "files.rename", "file", renamed, map[string]any{"from": body.Path})
	c.JSON(http.StatusOK, gin.H{"path": renamed})
}

func (a *AdminHandler) FilesMove(c *gin.Context) {
	var body struct {
		Src    string `json:"src" binding:"required"`
		DstDir string `json:"dst_dir" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	moved, err := a.files.Move(body.Src, body.DstDir)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "files.move", "file", moved, map[string]any{"from": body.Src})
	c.JSON(http.StatusOK, gin.H{"path": moved})
}

func (a *AdminHandler) FilesUpload(c *gin.Context) {
	dstDir := c.PostForm("dir")
	if dstDir == "" {
		dstDir = c.Query("dir")
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing upload file"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()

	saved, size, err := a.files.SaveUpload(dstDir, filepath.Base(fh.Filename), f)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "files.upload", "file", saved, map[string]any{"size": size})
	c.JSON(http.StatusCreated, gin.H{"path": saved, "size": size})
}

func (a *AdminHandler) FilesDownload(c *gin.Context) {
	safe, err := a.files.ResolveSafe(c.Query("path"), true)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	f, err := os.Open(safe)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot download directory"})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fi.Name()))
	http.ServeContent(c.Writer, c.Request, fi.Name(), fi.ModTime(), f)
}

func (a *AdminHandler) FilesDelete(c *gin.Context) {
	target := c.Query("path")
	if err := a.files.Delete(target); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, files.ErrOutsideBoundary) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "files.delete", "file", target, nil)
	c.Status(http.StatusNoContent)
}

// --- Backups & Restore ---

func (a *AdminHandler) ListBackups(c *gin.Context) {
	items, err := a.backups.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"items":      items,
		"backup_dir": a.backups.BackupDir(),
		"dialect":    a.db.Dialect,
	})
}

func (a *AdminHandler) CreateBackup(c *gin.Context) {
	var body struct {
		Format string `json:"format"` // portable-zip | sqlite-db
	}
	_ = c.ShouldBindJSON(&body)

	var info any
	var err error
	if strings.EqualFold(body.Format, "sqlite-db") {
		info, err = a.backups.CreateSQLiteNativeSnapshot()
	} else {
		info, err = a.backups.CreatePortable()
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "backup.create", "backup", body.Format, nil)
	c.JSON(http.StatusCreated, info)
}

func (a *AdminHandler) DownloadBackup(c *gin.Context) {
	full, err := a.backups.ResolveArchivePath(c.Param("filename"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "backup archive not found"})
		return
	}
	c.FileAttachment(full, filepath.Base(full))
}

func (a *AdminHandler) RestoreBackup(c *gin.Context) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !body.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "restore requires explicit confirm=true"})
		return
	}
	filename := c.Param("filename")
	counts, err := a.backups.RestorePortable(filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "backup.restore", "backup", filename, map[string]any{"counts": counts})
	c.JSON(http.StatusOK, gin.H{"ok": true, "counts": counts})
}

func (a *AdminHandler) DeleteBackup(c *gin.Context) {
	filename := c.Param("filename")
	if err := a.backups.Delete(filename); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "backup.delete", "backup", filename, nil)
	c.Status(http.StatusNoContent)
}

// --- Emby SQLite Migration ---

func (a *AdminHandler) PreviewEmbyMigration(c *gin.Context) {
	var body migration.EmbyMigrationRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rep, err := a.migration.Preview(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rep)
}

func (a *AdminHandler) ImportEmbyMigration(c *gin.Context) {
	var body migration.EmbyMigrationRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Importing a full Emby library takes minutes, so it is queued as a background task.
	// The HTTP call returns immediately and the wizard polls GET /api/admin/tasks/:id,
	// which keeps the run alive when the browser tab is closed or the page reloads.
	// Two migrations would interleave writes into the same libraries/items, so refuse a
	// second one even when the payload differs instead of relying on payload dedup alone.
	if a.tasks.HasActive("migrate_emby") {
		c.JSON(http.StatusConflict, gin.H{"error": "已有迁移任务在执行，请等待完成或先取消它"})
		return
	}
	t, created, err := a.tasks.Enqueue("migrate_emby", 20, body, "等待从旧 Emby 迁移")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "migration.emby_import", "migration", t.ID, map[string]any{
		"queued":  created,
		"task_id": t.ID,
	})
	c.JSON(http.StatusAccepted, gin.H{
		"task_id": t.ID,
		"queued":  created,
		"task":    t,
	})
}

// GetTask returns one background task so the UI can poll a long-running job.
func (a *AdminHandler) GetTask(c *gin.Context) {
	t, err := a.tasks.Get(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	c.JSON(http.StatusOK, t)
}

// --- API Keys & Audit Logs ---

func (a *AdminHandler) ListAPIKeys(c *gin.Context) {
	var keys []model.APIKey
	_ = a.db.Gorm.Order("created_at DESC").Find(&keys).Error
	c.JSON(http.StatusOK, gin.H{"items": keys})
}

func (a *AdminHandler) CreateAPIKey(c *gin.Context) {
	var body struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plain, hash, err := auth.GenerateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	uid := ""
	if v, ok := c.Get("admin_principal"); ok {
		if p, ok := v.(*auth.Principal); ok && p.User != nil {
			uid = p.User.ID
		}
	}
	k := model.APIKey{
		ID:        strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:      strings.TrimSpace(body.Name),
		KeyHash:   hash,
		CreatedBy: uid,
		CreatedAt: time.Now().UTC(),
	}
	if err := a.db.Gorm.Create(&k).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.recordAudit(c, "apikey.create", "apikey", k.ID, map[string]any{"name": k.Name})
	c.JSON(http.StatusCreated, gin.H{
		"id":         k.ID,
		"name":       k.Name,
		"key":        plain, // Returned ONCE only!
		"created_at": k.CreatedAt,
	})
}

func (a *AdminHandler) DeleteAPIKey(c *gin.Context) {
	id := c.Param("id")
	_ = a.db.Gorm.Where("id = ?", id).Delete(&model.APIKey{}).Error
	_ = a.cache.DeletePrefix(c.Request.Context(), "tok:")
	a.recordAudit(c, "apikey.delete", "apikey", id, nil)
	c.Status(http.StatusNoContent)
}

func (a *AdminHandler) ListAuditLogs(c *gin.Context) {
	rows, err := a.audit.List(100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}
