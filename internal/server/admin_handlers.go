package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"emby-lite/internal/audit"
	"emby-lite/internal/auth"
	"emby-lite/internal/backup"
	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/files"
	imgpipe "emby-lite/internal/image"
	"emby-lite/internal/migration"
	"emby-lite/internal/model"
	"emby-lite/internal/playback"
	"emby-lite/internal/scanner"
	"emby-lite/internal/scraper"
	"emby-lite/internal/task"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AdminHandler struct {
	db        *database.DB
	cache     cache.Store
	cfg       *config.Config
	auth      *auth.Service
	scanner   *scanner.Service
	playback  *playback.Service
	tasks     *task.Queue
	images    *imgpipe.Pipeline
	scraper   *scraper.Service
	files     *files.Manager
	backups   *backup.Service
	migration *migration.Service
	audit     *audit.Logger
}

func NewAdminHandler(
	db *database.DB,
	cacheStore cache.Store,
	cfg *config.Config,
	authSvc *auth.Service,
	scanSvc *scanner.Service,
	pbSvc *playback.Service,
	taskQueue *task.Queue,
	imgPipeline *imgpipe.Pipeline,
	scraperSvc *scraper.Service,
	fileMgr *files.Manager,
	backupSvc *backup.Service,
	migSvc *migration.Service,
	auditLog *audit.Logger,
) *AdminHandler {
	return &AdminHandler{
		db:        db,
		cache:     cacheStore,
		cfg:       cfg,
		auth:      authSvc,
		scanner:   scanSvc,
		playback:  pbSvc,
		tasks:     taskQueue,
		images:    imgPipeline,
		scraper:   scraperSvc,
		files:     fileMgr,
		backups:   backupSvc,
		migration: migSvc,
		audit:     auditLog,
	}
}

func (a *AdminHandler) RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := a.auth.AuthenticateRequest(c.Request)
		if err != nil || p == nil || p.IsAPIKey || p.User == nil || !p.User.IsAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "administrator login required"})
			c.Abort()
			return
		}
		c.Set("admin_principal", p)
		c.Next()
	}
}

// --- Web Bootstrap & Setup Wizard ---

func (a *AdminHandler) Bootstrap(c *gin.Context) {
	initialized, err := a.auth.IsInitialized()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"initialized": initialized,
		"server_name": a.db.GetSetting("server_name", a.cfg.Server.ServerName),
		"server_id":   a.db.GetSetting("server_id", ""),
		"version":     "0.3.14",
	})
}

func (a *AdminHandler) Setup(c *gin.Context) {
	var body struct {
		Username   string `json:"username" binding:"required"`
		Password   string `json:"password" binding:"required,min=4"`
		ServerName string `json:"server_name"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := a.auth.CreateInitialAdmin(body.Username, body.Password, body.ServerName)
	if err != nil {
		status := http.StatusBadRequest
		if err == auth.ErrAlreadyInitialized {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	meta := auth.ExtractClientMeta(c.Request)
	if meta.Client == "" {
		meta.Client = "Emby-Lite Web"
	}
	if meta.Device == "" {
		meta.Device = "Browser"
	}
	_, token, err := a.auth.AuthenticateByName(c.Request.Context(), body.Username, body.Password, meta)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"user":         user,
		"access_token": token,
	})
}

// ExternalPlay returns direct stream URL + external player URI schemes (IINA, PotPlayer, Infuse).
func (a *AdminHandler) ExternalPlay(c *gin.Context) {
	p, err := a.auth.AuthenticateRequest(c.Request)
	if err != nil || p == nil || p.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	itemID := c.Param("itemId")
	var item model.Item
	if err := a.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	if !a.auth.CanAccessLibrary(p, item.LibraryID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	sources := a.playback.BuildMediaSources(c.Request, p, &item)
	if len(sources) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no playable source"})
		return
	}
	selected := sources[0]
	if wantSrc := strings.TrimSpace(c.Query("media_source_id")); wantSrc != "" {
		for _, s := range sources {
			if id, _ := s["Id"].(string); id == wantSrc {
				selected = s
				break
			}
		}
	}
	relDirect, _ := selected["DirectStreamUrl"].(string)
	directURL := playback.PublicOrigin(c.Request, a.cfg.Server.PublicURL) + "/emby" + relDirect
	if relDirect == "" {
		directURL, _ = selected["Path"].(string)
	}
	encoded := url.QueryEscape(directURL)
	c.JSON(http.StatusOK, gin.H{
		"item_id":         item.ID,
		"name":            item.Name,
		"media_source_id": selected["Id"],
		"direct_url":      directURL,
		"players": gin.H{
			"iina":      "iina://weblink?url=" + encoded,
			"potplayer": "potplayer://" + directURL,
			"infuse":    "infuse://x-callback-url/play?url=" + encoded,
			"vlc":       "vlc://" + directURL,
		},
		"sources": sources,
	})
}

// --- Admin Dashboard ---

func (a *AdminHandler) Dashboard(c *gin.Context) {
	rdb := a.db.Read()
	var libCount, movieCount, seriesCount, episodeCount, userCount, activeLeases int64
	rdb.Model(&model.Library{}).Count(&libCount)
	type typeCnt struct {
		Type string
		Cnt  int64
	}
	var itemCounts []typeCnt
	rdb.Table("items").
		Select("type, COUNT(*) AS cnt").
		Where("soft_deleted_at IS NULL AND type IN ?", []string{"Movie", "Series", "Episode"}).
		Group("type").
		Scan(&itemCounts)
	for _, tc := range itemCounts {
		switch tc.Type {
		case "Movie":
			movieCount = tc.Cnt
		case "Series":
			seriesCount = tc.Cnt
		case "Episode":
			episodeCount = tc.Cnt
		}
	}
	rdb.Model(&model.User{}).Count(&userCount)
	rdb.Model(&model.PlaybackLease{}).Where("expires_at >= ?", time.Now().UTC()).Count(&activeLeases)

	var imgStats imgpipe.CacheStats
	if a.images != nil {
		imgStats = a.images.Stats()
	}

	c.JSON(http.StatusOK, gin.H{
		"server_name":       a.db.GetSetting("server_name", a.cfg.Server.ServerName),
		"server_id":         a.db.GetSetting("server_id", ""),
		"database":          a.db.Dialect,
		"has_fts5":          a.db.HasFTS5,
		"has_trgm":          a.db.HasTrgm,
		"cache":             a.cache.BackendName(),
		"ffprobe_available": a.scanner.Prober().HasFFprobe(),
		"scan_concurrency":  a.scanner.Concurrency(),
		"image_cache_files": imgStats.FileCount,
		"image_cache_bytes": imgStats.TotalBytes,
		"scanning":          a.scanner.IsScanning() || (a.tasks != nil && a.tasks.HasActive("scan")),
		"libraries":         libCount,
		"movies":            movieCount,
		"series":            seriesCount,
		"episodes":          episodeCount,
		"users":             userCount,
		"active_streams":    activeLeases,
	})
}

// --- Admin Libraries ---

type libraryPayload struct {
	Name              string   `json:"name" binding:"required"`
	Kind              string   `json:"kind" binding:"required"` // movies | tvshows | mixed | boxsets
	Paths             []string `json:"paths"`
	Hidden            bool     `json:"hidden"`
	ShowInMyMedia     *bool    `json:"show_in_my_media"`
	ShowInResume      *bool    `json:"show_in_resume"`
	ShowInNextUp      *bool    `json:"show_in_next_up"`
	ShowInLatest      *bool    `json:"show_in_latest"`
	AutoMergeVersions *bool    `json:"auto_merge_versions"`
	AutoGroupSeries   *bool    `json:"auto_group_series"`
	SortOrder         *int     `json:"sort_order"`
	ScanSchedule      string   `json:"scan_schedule"`
}

func (a *AdminHandler) ListLibraries(c *gin.Context) {
	var libs []model.Library
	if err := a.db.Gorm.Order("sort_order ASC, name ASC").Find(&libs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(libs))
	for _, l := range libs {
		var paths []string
		_ = json.Unmarshal([]byte(l.PathsJSON), &paths)
		out = append(out, map[string]any{
			"id":                  l.ID,
			"name":                l.Name,
			"kind":                l.Kind,
			"paths":               paths,
			"hidden":              l.Hidden,
			"show_in_my_media":    l.ShowInMyMedia,
			"show_in_resume":      l.ShowInResume,
			"show_in_next_up":     l.ShowInNextUp,
			"show_in_latest":      l.ShowInLatest,
			"auto_merge_versions": l.AutoMergeVersions,
			"auto_group_series":   l.AutoGroupSeries,
			"sort_order":          l.SortOrder,
			"scan_schedule":       l.ScanSchedule,
			"status":              l.Status,
			"last_error":          l.LastError,
			"item_count":          l.ItemCount,
			"last_scanned_at":     l.LastScannedAt,
			"scanning":            a.scanner.IsLibraryScanning(l.ID),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (a *AdminHandler) CreateLibrary(c *gin.Context) {
	var body libraryPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind != "movies" && kind != "tvshows" && kind != "mixed" && kind != "boxsets" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be movies, tvshows, mixed, or boxsets"})
		return
	}
	if kind != "boxsets" && len(cleanPaths(body.Paths)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paths is required"})
		return
	}
	autoMerge := true
	if body.AutoMergeVersions != nil {
		autoMerge = *body.AutoMergeVersions
	}
	autoGroup := true
	if body.AutoGroupSeries != nil {
		autoGroup = *body.AutoGroupSeries
	}
	showMyMedia := true
	if body.ShowInMyMedia != nil {
		showMyMedia = *body.ShowInMyMedia
	}
	showResume := true
	if body.ShowInResume != nil {
		showResume = *body.ShowInResume
	}
	showNextUp := true
	if body.ShowInNextUp != nil {
		showNextUp = *body.ShowInNextUp
	}
	showLatest := true
	if body.ShowInLatest != nil {
		showLatest = *body.ShowInLatest
	}
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	} else {
		var count int64
		_ = a.db.Gorm.Model(&model.Library{}).Count(&count).Error
		if count > 0 {
			var maxOrder int
			_ = a.db.Gorm.Model(&model.Library{}).Select("COALESCE(MAX(sort_order), 0)").Scan(&maxOrder).Error
			sortOrder = maxOrder + 1
		}
	}
	pathsBytes, _ := json.Marshal(cleanPaths(body.Paths))
	lib := model.Library{
		ID:                strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:              strings.TrimSpace(body.Name),
		Kind:              kind,
		PathsJSON:         string(pathsBytes),
		Hidden:            body.Hidden,
		ShowInMyMedia:     showMyMedia,
		ShowInResume:      showResume,
		ShowInNextUp:      showNextUp,
		ShowInLatest:      showLatest,
		AutoMergeVersions: autoMerge,
		AutoGroupSeries:   autoGroup,
		SortOrder:         sortOrder,
		ScanSchedule:      body.ScanSchedule,
		Status:            "idle",
	}
	if err := a.db.Gorm.Create(&lib).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !autoMerge || !autoGroup || !showMyMedia || !showResume || !showNextUp || !showLatest {
		lib.AutoMergeVersions = autoMerge
		lib.AutoGroupSeries = autoGroup
		lib.ShowInMyMedia = showMyMedia
		lib.ShowInResume = showResume
		lib.ShowInNextUp = showNextUp
		lib.ShowInLatest = showLatest
		_ = a.db.Gorm.Model(&model.Library{}).Where("id = ?", lib.ID).Updates(map[string]any{
			"auto_merge_versions": autoMerge,
			"auto_group_series":   autoGroup,
			"show_in_my_media":    showMyMedia,
			"show_in_resume":      showResume,
			"show_in_next_up":     showNextUp,
			"show_in_latest":      showLatest,
		}).Error
	}
	a.db.BumpLibraryRevision()
	c.JSON(http.StatusCreated, gin.H{
		"id":                  lib.ID,
		"name":                lib.Name,
		"kind":                lib.Kind,
		"paths":               cleanPaths(body.Paths),
		"sort_order":          lib.SortOrder,
		"show_in_my_media":    lib.ShowInMyMedia,
		"show_in_resume":      lib.ShowInResume,
		"show_in_next_up":     lib.ShowInNextUp,
		"show_in_latest":      lib.ShowInLatest,
		"auto_merge_versions": lib.AutoMergeVersions,
		"auto_group_series":   lib.AutoGroupSeries,
	})
}

func (a *AdminHandler) UpdateLibrary(c *gin.Context) {
	id := c.Param("id")
	var lib model.Library
	if err := a.db.Gorm.Where("id = ?", id).First(&lib).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
		return
	}
	var body libraryPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	prevMerge := lib.AutoMergeVersions
	prevGroup := lib.AutoGroupSeries
	pathsBytes, _ := json.Marshal(cleanPaths(body.Paths))
	lib.Name = strings.TrimSpace(body.Name)
	lib.Kind = strings.ToLower(strings.TrimSpace(body.Kind))
	if lib.Kind != "boxsets" && len(cleanPaths(body.Paths)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paths is required"})
		return
	}
	lib.PathsJSON = string(pathsBytes)
	lib.Hidden = body.Hidden
	if body.ShowInMyMedia != nil {
		lib.ShowInMyMedia = *body.ShowInMyMedia
	}
	if body.ShowInResume != nil {
		lib.ShowInResume = *body.ShowInResume
	}
	if body.ShowInNextUp != nil {
		lib.ShowInNextUp = *body.ShowInNextUp
	}
	if body.ShowInLatest != nil {
		lib.ShowInLatest = *body.ShowInLatest
	}
	if body.AutoMergeVersions != nil {
		lib.AutoMergeVersions = *body.AutoMergeVersions
	}
	if body.AutoGroupSeries != nil {
		lib.AutoGroupSeries = *body.AutoGroupSeries
	}
	if body.SortOrder != nil {
		lib.SortOrder = *body.SortOrder
	}
	lib.ScanSchedule = body.ScanSchedule
	if err := a.db.Gorm.Save(&lib).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var merged *scanner.ConsolidateStats
	if (!prevMerge && lib.AutoMergeVersions) || (!prevGroup && lib.AutoGroupSeries) {
		merged, _ = scanner.ConsolidateLibrary(a.db, lib.ID)
	}
	a.db.BumpLibraryRevision()
	c.JSON(http.StatusOK, gin.H{
		"id":                  lib.ID,
		"name":                lib.Name,
		"sort_order":          lib.SortOrder,
		"show_in_my_media":    lib.ShowInMyMedia,
		"show_in_resume":      lib.ShowInResume,
		"show_in_next_up":     lib.ShowInNextUp,
		"show_in_latest":      lib.ShowInLatest,
		"auto_merge_versions": lib.AutoMergeVersions,
		"auto_group_series":   lib.AutoGroupSeries,
		"consolidated":        merged,
	})
}

func (a *AdminHandler) ReorderLibraries(c *gin.Context) {
	var body struct {
		LibraryIDs []string `json:"library_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	tx := a.db.Gorm.Begin()
	for idx, rawID := range body.LibraryIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if err := tx.Model(&model.Library{}).Where("id = ?", id).Update("sort_order", idx).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.db.BumpLibraryRevision()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *AdminHandler) ConsolidateLibrary(c *gin.Context) {
	id := c.Param("id")
	stats, err := scanner.ConsolidateLibrary(a.db, id)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (a *AdminHandler) DeleteLibrary(c *gin.Context) {
	id := c.Param("id")
	_ = a.db.Gorm.Where("library_id = ?", id).Delete(&model.ScanFile{}).Error
	_ = a.db.Gorm.Where("library_id = ?", id).Delete(&model.Item{}).Error
	_ = a.db.Gorm.Where("library_id = ?", id).Delete(&model.UserLibrary{}).Error
	_ = a.db.Gorm.Where("library_id = ?", id).Delete(&model.GroupLibrary{}).Error
	if err := a.db.Gorm.Where("id = ?", id).Delete(&model.Library{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.db.BumpLibraryRevision()
	c.Status(http.StatusNoContent)
}

func (a *AdminHandler) libraryNameByID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	var lib model.Library
	if err := a.db.Read().Select("name").Where("id = ?", id).First(&lib).Error; err == nil {
		return strings.TrimSpace(lib.Name)
	}
	return ""
}

type taskView struct {
	model.BackgroundTask
	LibraryID   string `json:"library_id,omitempty"`
	LibraryName string `json:"library_name,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
}

func extractTaskLibraryID(payloadJSON string) string {
	if strings.TrimSpace(payloadJSON) == "" {
		return ""
	}
	var p struct {
		LibraryID string `json:"library_id"`
	}
	if json.Unmarshal([]byte(payloadJSON), &p) == nil {
		return strings.TrimSpace(p.LibraryID)
	}
	return ""
}

func computeTaskDurationMs(t model.BackgroundTask) int64 {
	if t.FinishedAt == nil {
		return 0
	}
	start := t.CreatedAt
	if t.StartedAt != nil {
		start = *t.StartedAt
	}
	ms := t.FinishedAt.Sub(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func (a *AdminHandler) enrichTasks(tasks []model.BackgroundTask) []taskView {
	out := make([]taskView, len(tasks))
	libIDSet := make(map[string]struct{})
	for i, t := range tasks {
		lid := extractTaskLibraryID(t.PayloadJSON)
		out[i] = taskView{
			BackgroundTask: t,
			LibraryID:      lid,
			DurationMs:     computeTaskDurationMs(t),
		}
		if lid != "" {
			libIDSet[lid] = struct{}{}
		}
	}
	if len(libIDSet) > 0 {
		ids := make([]string, 0, len(libIDSet))
		for lid := range libIDSet {
			ids = append(ids, lid)
		}
		var libs []model.Library
		_ = a.db.Read().Select("id, name").Where("id IN ?", ids).Find(&libs).Error
		nameByID := make(map[string]string, len(libs))
		for _, l := range libs {
			nameByID[l.ID] = strings.TrimSpace(l.Name)
		}
		for i := range out {
			if out[i].LibraryID != "" {
				out[i].LibraryName = nameByID[out[i].LibraryID]
			}
		}
	}
	return out
}

func (a *AdminHandler) enrichTask(t model.BackgroundTask) taskView {
	views := a.enrichTasks([]model.BackgroundTask{t})
	return views[0]
}

func (a *AdminHandler) ScanLibrary(c *gin.Context) {
	id := c.Param("id")
	if c.Query("sync") == "true" {
		stats, err := a.scanner.ScanLibrary(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "stats": stats})
			return
		}
		c.JSON(http.StatusOK, stats)
		return
	}
	if a.tasks != nil {
		msg := "等待扫描媒体库"
		if libName := a.libraryNameByID(id); libName != "" {
			msg = "等待扫描：" + libName
		}
		t, created, err := a.tasks.Enqueue("scan", 10, map[string]string{"library_id": id}, msg)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"started": created, "library_id": id, "task": a.enrichTask(*t)})
		return
	}
	started := a.scanner.ScanLibraryAsync(id)
	c.JSON(http.StatusAccepted, gin.H{"started": started, "library_id": id})
}

func (a *AdminHandler) ProbeLibrary(c *gin.Context) {
	id := c.Param("id")
	includeSTRM := strings.EqualFold(c.Query("include_strm"), "true")
	if c.Query("sync") == "true" {
		n, err := a.scanner.ProbeLibrarySources(c.Request.Context(), id, includeSTRM, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"probed": n, "library_id": id})
		return
	}
	msg := "等待探测媒体信息"
	if libName := a.libraryNameByID(id); libName != "" {
		msg = "等待探测：" + libName
	}
	t, created, err := a.tasks.Enqueue("probe", 5, map[string]any{
		"library_id":   id,
		"include_strm": includeSTRM,
	}, msg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"started": created, "library_id": id, "task": a.enrichTask(*t)})
}

func (a *AdminHandler) ScanAll(c *gin.Context) {
	if a.tasks != nil {
		var libs []model.Library
		_ = a.db.Gorm.Find(&libs).Error
		queued := 0
		for _, l := range libs {
			if _, created, err := a.tasks.Enqueue("scan", 10, map[string]string{"library_id": l.ID}, "等待扫描："+l.Name); err == nil && created {
				queued++
			}
		}
		c.JSON(http.StatusAccepted, gin.H{"started": queued})
		return
	}
	started := a.scanner.ScanAllAsync()
	c.JSON(http.StatusAccepted, gin.H{"started": started})
}

// --- Background Tasks & SSE ---

func (a *AdminHandler) ListTasks(c *gin.Context) {
	tasks, err := a.tasks.List(50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": a.enrichTasks(tasks)})
}

func (a *AdminHandler) CancelTask(c *gin.Context) {
	if err := a.tasks.Cancel(c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *AdminHandler) TaskEventsSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	sub := a.tasks.Subscribe()
	defer a.tasks.Unsubscribe(sub)

	flusher, ok := c.Writer.(http.Flusher)
	if ok {
		_, _ = fmt.Fprint(c.Writer, "event: connected\ndata: {\"ok\":true}\n\n")
		flusher.Flush()
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(c.Writer, ": ping\n\n")
			if ok {
				flusher.Flush()
			}
		case ev, open := <-sub:
			if !open {
				return
			}
			b, err := json.Marshal(gin.H{
				"event": ev.EventType,
				"task":  a.enrichTask(ev.Task),
			})
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(c.Writer, "event: task\ndata: %s\n\n", b)
			if ok {
				flusher.Flush()
			}
		}
	}
}

// --- Image & Runtime Cache Management ---

func (a *AdminHandler) CacheStats(c *gin.Context) {
	var imgStats imgpipe.CacheStats
	if a.images != nil {
		imgStats = a.images.Stats()
	}
	c.JSON(http.StatusOK, gin.H{
		"cache_backend": a.cache.BackendName(),
		"image_cache":   imgStats,
	})
}

func (a *AdminHandler) ClearCache(c *gin.Context) {
	var cleared int64
	if a.images != nil {
		n, err := a.images.Clear()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		cleared = n
	}
	_ = a.cache.DeletePrefix(c.Request.Context(), "")
	c.JSON(http.StatusOK, gin.H{
		"ok":                  true,
		"image_files_cleared": cleared,
	})
}

// --- Admin Users & Permission Groups ---

func (a *AdminHandler) ListUsers(c *gin.Context) {
	var users []model.User
	_ = a.db.Gorm.Order("created_at ASC").Find(&users).Error
	out := make([]map[string]any, 0, len(users))
	for i := range users {
		u := users[i]
		var libIDs []string
		_ = a.db.Gorm.Model(&model.UserLibrary{}).Where("user_id = ?", u.ID).Pluck("library_id", &libIDs).Error
		policy := a.auth.ResolvePolicy(&u)
		out = append(out, map[string]any{
			"id":                  u.ID,
			"username":            u.Username,
			"display_name":        u.DisplayName,
			"is_admin":            u.IsAdmin,
			"is_primary_admin":    u.IsPrimaryAdmin,
			"permission_group_id": u.PermissionGroupID,
			"disabled":            u.Disabled,
			"can_download":        policy.AllowDownload,
			"allow_playback":      policy.AllowPlayback,
			"max_devices":         policy.MaxDevices,
			"library_ids":         libIDs,
			"last_login_at":       u.LastLoginAt,
			"created_at":          u.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (a *AdminHandler) CreateUser(c *gin.Context) {
	var body struct {
		Username          string   `json:"username" binding:"required"`
		Password          string   `json:"password" binding:"required,min=4"`
		IsAdmin           bool     `json:"is_admin"`
		PermissionGroupID string   `json:"permission_group_id"`
		CanDownload       *bool    `json:"can_download"`
		LibraryIDs        []string `json:"library_ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := a.auth.CreateUser(body.Username, body.Password, body.IsAdmin, body.PermissionGroupID, body.CanDownload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, lid := range body.LibraryIDs {
		_ = a.db.Gorm.Create(&model.UserLibrary{UserID: user.ID, LibraryID: lid}).Error
	}
	c.JSON(http.StatusCreated, user)
}

func (a *AdminHandler) UpdateUser(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := a.db.Gorm.Where("id = ?", id).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	var body struct {
		DisplayName       *string  `json:"display_name"`
		Password          string   `json:"password"`
		IsAdmin           *bool    `json:"is_admin"`
		Disabled          *bool    `json:"disabled"`
		PermissionGroupID *string  `json:"permission_group_id"`
		CanDownload       *bool    `json:"can_download"`
		LibraryIDs        []string `json:"library_ids"`
		UpdateLibraries   bool     `json:"update_libraries"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if user.IsPrimaryAdmin {
		if body.Disabled != nil && *body.Disabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot disable primary administrator"})
			return
		}
		if body.IsAdmin != nil && !*body.IsAdmin {
			c.JSON(http.StatusForbidden, gin.H{"error": "cannot demote primary administrator"})
			return
		}
	}
	if body.DisplayName != nil {
		user.DisplayName = strings.TrimSpace(*body.DisplayName)
	}
	if body.Password != "" {
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		user.PasswordHash = hash
	}
	if body.IsAdmin != nil {
		user.IsAdmin = *body.IsAdmin
	}
	if body.Disabled != nil {
		user.Disabled = *body.Disabled
	}
	if body.PermissionGroupID != nil {
		user.PermissionGroupID = *body.PermissionGroupID
	}
	if body.CanDownload != nil {
		user.CanDownload = body.CanDownload
	}
	if err := a.db.Gorm.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if body.UpdateLibraries {
		_ = a.db.Gorm.Where("user_id = ?", user.ID).Delete(&model.UserLibrary{}).Error
		for _, lid := range body.LibraryIDs {
			_ = a.db.Gorm.Create(&model.UserLibrary{UserID: user.ID, LibraryID: lid}).Error
		}
	}
	a.auth.InvalidateUserCache(c.Request.Context(), user.ID)
	c.JSON(http.StatusOK, user)
}

func (a *AdminHandler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := a.db.Gorm.Where("id = ?", id).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if user.IsPrimaryAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot delete primary administrator"})
		return
	}
	_ = a.db.Gorm.Where("user_id = ?", id).Delete(&model.Token{}).Error
	_ = a.db.Gorm.Where("user_id = ?", id).Delete(&model.UserLibrary{}).Error
	_ = a.db.Gorm.Where("user_id = ?", id).Delete(&model.UserDevice{}).Error
	_ = a.db.Gorm.Where("id = ?", id).Delete(&model.User{}).Error
	a.auth.InvalidateUserCache(c.Request.Context(), id)
	c.Status(http.StatusNoContent)
}

func (a *AdminHandler) ListPermissionGroups(c *gin.Context) {
	var groups []model.PermissionGroup
	_ = a.db.Gorm.Order("created_at ASC").Find(&groups).Error
	c.JSON(http.StatusOK, gin.H{"items": groups})
}

// --- Settings ---

func (a *AdminHandler) GetSettings(c *gin.Context) {
	metaDir := strings.TrimSpace(a.db.GetSetting("emby_metadata_dir", a.cfg.Storage.EmbyMetadataDir))
	defWB := "false"
	if a.cfg.Storage.EmbyMetadataWriteback {
		defWB = "true"
	}
	writeback := strings.EqualFold(a.db.GetSetting("emby_metadata_writeback", defWB), "true")
	metaExists, metaWritable := imgpipe.CheckDirectoryWritable(metaDir)

	c.JSON(http.StatusOK, gin.H{
		"server_name":             a.db.GetSetting("server_name", a.cfg.Server.ServerName),
		"server_id":               a.db.GetSetting("server_id", ""),
		"database":                a.db.Dialect,
		"cache":                   a.cache.BackendName(),
		"listen_addr":             a.cfg.Server.ListenAddr,
		"emby_metadata_dir":       metaDir,
		"emby_metadata_writeback": writeback,
		"emby_metadata_exists":    metaExists,
		"emby_metadata_writable":  metaWritable,
	})
}

func (a *AdminHandler) UpdateSettings(c *gin.Context) {
	var body struct {
		ServerName            string  `json:"server_name"`
		EmbyMetadataDir       *string `json:"emby_metadata_dir"`
		EmbyMetadataWriteback *bool   `json:"emby_metadata_writeback"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(body.ServerName) != "" {
		_ = a.db.SetSetting("server_name", strings.TrimSpace(body.ServerName), false)
	}
	if body.EmbyMetadataDir != nil {
		_ = a.db.SetSetting("emby_metadata_dir", strings.TrimSpace(*body.EmbyMetadataDir), false)
	}
	if body.EmbyMetadataWriteback != nil {
		val := "false"
		if *body.EmbyMetadataWriteback {
			val = "true"
		}
		_ = a.db.SetSetting("emby_metadata_writeback", val, false)
	}
	a.recordAudit(c, "settings.update", "settings", "global", nil)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func cleanPaths(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
