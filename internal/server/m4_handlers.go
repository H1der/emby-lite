package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	imgpipe "emby-lite/internal/image"
	"emby-lite/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// --- Permission Groups CRUD ---

type permissionGroupPayload struct {
	Name                 string   `json:"name" binding:"required"`
	Description          string   `json:"description"`
	AllowPlayback        bool     `json:"allow_playback"`
	AllowDownload        bool     `json:"allow_download"`
	MaxDevices           int      `json:"max_devices"`
	MaxConcurrentStreams int      `json:"max_concurrent_streams"`
	CanViewHidden        bool     `json:"can_view_hidden"`
	LibraryIDs           []string `json:"library_ids"`
}

func (a *AdminHandler) ListPermissionGroupsFull(c *gin.Context) {
	var groups []model.PermissionGroup
	_ = a.db.Gorm.Order("created_at ASC").Find(&groups).Error
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		var libIDs []string
		_ = a.db.Gorm.Model(&model.GroupLibrary{}).Where("group_id = ?", g.ID).Pluck("library_id", &libIDs).Error
		out = append(out, map[string]any{
			"id":                     g.ID,
			"name":                   g.Name,
			"description":            g.Description,
			"allow_playback":         g.AllowPlayback,
			"allow_download":         g.AllowDownload,
			"max_devices":            g.MaxDevices,
			"max_concurrent_streams": g.MaxConcurrentStreams,
			"can_view_hidden":        g.CanViewHidden,
			"library_ids":            libIDs,
			"created_at":             g.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (a *AdminHandler) CreatePermissionGroup(c *gin.Context) {
	var body permissionGroupPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	g := model.PermissionGroup{
		ID:                   "group-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		Name:                 strings.TrimSpace(body.Name),
		Description:          strings.TrimSpace(body.Description),
		AllowPlayback:        body.AllowPlayback,
		AllowDownload:        body.AllowDownload,
		MaxDevices:           body.MaxDevices,
		MaxConcurrentStreams: body.MaxConcurrentStreams,
		CanViewHidden:        body.CanViewHidden,
	}
	if err := a.db.Gorm.Create(&g).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, lid := range body.LibraryIDs {
		_ = a.db.Gorm.Create(&model.GroupLibrary{GroupID: g.ID, LibraryID: lid}).Error
	}
	c.JSON(http.StatusCreated, g)
}

func (a *AdminHandler) UpdatePermissionGroup(c *gin.Context) {
	id := c.Param("id")
	var g model.PermissionGroup
	if err := a.db.Gorm.Where("id = ?", id).First(&g).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "permission group not found"})
		return
	}
	var body permissionGroupPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	g.Name = strings.TrimSpace(body.Name)
	g.Description = strings.TrimSpace(body.Description)
	g.AllowPlayback = body.AllowPlayback
	g.AllowDownload = body.AllowDownload
	g.MaxDevices = body.MaxDevices
	g.MaxConcurrentStreams = body.MaxConcurrentStreams
	g.CanViewHidden = body.CanViewHidden
	if err := a.db.Gorm.Save(&g).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = a.db.Gorm.Where("group_id = ?", g.ID).Delete(&model.GroupLibrary{}).Error
	for _, lid := range body.LibraryIDs {
		_ = a.db.Gorm.Create(&model.GroupLibrary{GroupID: g.ID, LibraryID: lid}).Error
	}
	_ = a.cache.DeletePrefix(c.Request.Context(), "tok:")
	c.JSON(http.StatusOK, g)
}

func (a *AdminHandler) DeletePermissionGroup(c *gin.Context) {
	id := c.Param("id")
	_ = a.db.Gorm.Model(&model.User{}).Where("permission_group_id = ?", id).Update("permission_group_id", "").Error
	_ = a.db.Gorm.Where("group_id = ?", id).Delete(&model.GroupLibrary{}).Error
	_ = a.db.Gorm.Where("id = ?", id).Delete(&model.PermissionGroup{}).Error
	_ = a.cache.DeletePrefix(c.Request.Context(), "tok:")
	c.Status(http.StatusNoContent)
}

// --- User Devices & Active Playback Leases ---

func (a *AdminHandler) ListDevices(c *gin.Context) {
	type deviceRow struct {
		model.UserDevice
		Username string `json:"username"`
	}
	var rows []deviceRow
	_ = a.db.Gorm.Table("user_devices").
		Select("user_devices.*, users.username").
		Joins("LEFT JOIN users ON users.id = user_devices.user_id").
		Order("user_devices.last_seen_at DESC").
		Limit(200).
		Scan(&rows).Error
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

func (a *AdminHandler) ToggleBlockDevice(c *gin.Context) {
	userID := c.Param("userId")
	deviceID := c.Param("deviceId")
	var body struct {
		Blocked bool `json:"blocked"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = a.db.Gorm.Model(&model.UserDevice{}).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		Update("blocked", body.Blocked).Error
	if body.Blocked {
		_ = a.db.Gorm.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&model.Token{}).Error
		_ = a.db.Gorm.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&model.PlaybackLease{}).Error
		a.auth.InvalidateUserCache(c.Request.Context(), userID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "blocked": body.Blocked})
}

func (a *AdminHandler) DeleteDevice(c *gin.Context) {
	userID := c.Param("userId")
	deviceID := c.Param("deviceId")
	_ = a.db.Gorm.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&model.Token{}).Error
	_ = a.db.Gorm.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&model.PlaybackLease{}).Error
	_ = a.db.Gorm.Where("user_id = ? AND device_id = ?", userID, deviceID).Delete(&model.UserDevice{}).Error
	a.auth.InvalidateUserCache(c.Request.Context(), userID)
	c.Status(http.StatusNoContent)
}

func (a *AdminHandler) ListActiveSessions(c *gin.Context) {
	type leaseRow struct {
		model.PlaybackLease
		Username string `json:"username"`
		ItemName string `json:"item_name"`
	}
	var rows []leaseRow
	_ = a.db.Gorm.Table("playback_leases").
		Select("playback_leases.*, users.username, items.name AS item_name").
		Joins("LEFT JOIN users ON users.id = playback_leases.user_id").
		Joins("LEFT JOIN items ON items.id = playback_leases.item_id").
		Where("playback_leases.expires_at >= ?", time.Now().UTC()).
		Order("playback_leases.updated_at DESC").
		Scan(&rows).Error
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

func (a *AdminHandler) TerminateSession(c *gin.Context) {
	sid := c.Param("sessionId")
	_ = a.db.Gorm.Where("play_session_id = ?", sid).Delete(&model.PlaybackLease{}).Error
	c.Status(http.StatusNoContent)
}

// --- Virtual / Composite Libraries ---

type VirtualLibraryRule struct {
	LibraryIDs []string `json:"library_ids,omitempty"`
	ItemTypes  []string `json:"item_types,omitempty"` // Movie | Series
	Genres     []string `json:"genres,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	MinRating  float64  `json:"min_rating,omitempty"`
	MinYear    int      `json:"min_year,omitempty"`
	MaxYear    int      `json:"max_year,omitempty"`
	SortBy     string   `json:"sort_by,omitempty"`
}

type virtualLibraryPayload struct {
	Name      string             `json:"name" binding:"required"`
	Rule      VirtualLibraryRule `json:"rule"`
	SortOrder int                `json:"sort_order"`
	Enabled   bool               `json:"enabled"`
}

func (a *AdminHandler) ListVirtualLibraries(c *gin.Context) {
	var vlibs []model.VirtualLibrary
	_ = a.db.Gorm.Order("sort_order ASC, name ASC").Find(&vlibs).Error
	out := make([]map[string]any, 0, len(vlibs))
	for _, vl := range vlibs {
		var rule VirtualLibraryRule
		_ = json.Unmarshal([]byte(vl.RuleJSON), &rule)
		out = append(out, map[string]any{
			"id":             vl.ID,
			"name":           vl.Name,
			"rule":           rule,
			"sort_order":     vl.SortOrder,
			"enabled":        vl.Enabled,
			"cover_revision": vl.CoverRevision,
			"created_at":     vl.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (a *AdminHandler) CreateVirtualLibrary(c *gin.Context) {
	var body virtualLibraryPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ruleBytes, _ := json.Marshal(body.Rule)
	vl := model.VirtualLibrary{
		ID:        "vlib-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Name:      strings.TrimSpace(body.Name),
		RuleJSON:  string(ruleBytes),
		SortOrder: body.SortOrder,
		Enabled:   body.Enabled,
	}
	if err := a.db.Gorm.Create(&vl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.db.BumpLibraryRevision()
	c.JSON(http.StatusCreated, gin.H{"id": vl.ID, "name": vl.Name})
}

func (a *AdminHandler) UpdateVirtualLibrary(c *gin.Context) {
	id := c.Param("id")
	var vl model.VirtualLibrary
	if err := a.db.Gorm.Where("id = ?", id).First(&vl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "virtual library not found"})
		return
	}
	var body virtualLibraryPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ruleBytes, _ := json.Marshal(body.Rule)
	vl.Name = strings.TrimSpace(body.Name)
	vl.RuleJSON = string(ruleBytes)
	vl.SortOrder = body.SortOrder
	vl.Enabled = body.Enabled
	if err := a.db.Gorm.Save(&vl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.db.BumpLibraryRevision()
	c.JSON(http.StatusOK, gin.H{"id": vl.ID, "name": vl.Name})
}

func (a *AdminHandler) DeleteVirtualLibrary(c *gin.Context) {
	id := c.Param("id")
	_ = a.db.Gorm.Where("library_id = ?", id).Delete(&model.LibraryCover{}).Error
	_ = a.db.Gorm.Where("id = ?", id).Delete(&model.VirtualLibrary{}).Error
	a.db.BumpLibraryRevision()
	c.Status(http.StatusNoContent)
}

// --- Library & Virtual Library Covers (Auto Collage & Custom Upload) ---

func (a *AdminHandler) GenerateLibraryCover(c *gin.Context) {
	libID := c.Param("id")
	var posterPaths []string

	// Collect up to 4 local Primary posters from physical or virtual library items.
	if strings.HasPrefix(libID, "vlib-") {
		var vl model.VirtualLibrary
		if err := a.db.Gorm.Where("id = ?", libID).First(&vl).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "virtual library not found"})
			return
		}
		var rule VirtualLibraryRule
		_ = json.Unmarshal([]byte(vl.RuleJSON), &rule)
		q := a.db.Gorm.Table("item_images").
			Select("item_images.source_ref").
			Joins("JOIN items ON items.id = item_images.item_id").
			Where("items.soft_deleted_at IS NULL AND item_images.kind = ? AND item_images.source_type = ?", "Primary", "local")
		if len(rule.LibraryIDs) > 0 {
			q = q.Where("items.library_id IN ?", rule.LibraryIDs)
		}
		if len(rule.ItemTypes) > 0 {
			q = q.Where("items.type IN ?", rule.ItemTypes)
		}
		if len(rule.Genres) > 0 {
			q = q.Where("items.id IN (SELECT item_id FROM item_genres WHERE genre IN ?)", rule.Genres)
		}
		_ = q.Order("items.community_rating DESC, items.added_at DESC").Limit(4).Pluck("source_ref", &posterPaths).Error
	} else {
		var lib model.Library
		if err := a.db.Gorm.Where("id = ?", libID).First(&lib).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
			return
		}
		_ = a.db.Gorm.Table("item_images").
			Select("item_images.source_ref").
			Joins("JOIN items ON items.id = item_images.item_id").
			Where("items.library_id = ? AND items.soft_deleted_at IS NULL AND item_images.kind = ? AND item_images.source_type = ?", libID, "Primary", "local").
			Order("items.added_at DESC").
			Limit(4).
			Pluck("source_ref", &posterPaths).Error
	}

	jpegBytes, err := imgpipe.GenerateLibraryCollage(posterPaths)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rev := saveCoverBytes(a, libID, "collage", "image/jpeg", jpegBytes)
	c.JSON(http.StatusOK, gin.H{"ok": true, "library_id": libID, "revision": rev, "poster_count": len(posterPaths)})
}

func (a *AdminHandler) UploadLibraryCover(c *gin.Context) {
	libID := c.Param("id")
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file upload field"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil || len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty or unreadable image"})
		return
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "uploaded file must be an image (" + header.Filename + ")"})
		return
	}
	rev := saveCoverBytes(a, libID, "custom", mime, data)
	c.JSON(http.StatusOK, gin.H{"ok": true, "library_id": libID, "revision": rev})
}

func (a *AdminHandler) DeleteLibraryCover(c *gin.Context) {
	libID := c.Param("id")
	_ = a.db.Gorm.Where("library_id = ?", libID).Delete(&model.LibraryCover{}).Error
	if strings.HasPrefix(libID, "vlib-") {
		_ = a.db.Gorm.Model(&model.VirtualLibrary{}).Where("id = ?", libID).Update("cover_revision", "").Error
	} else {
		_ = a.db.Gorm.Model(&model.Library{}).Where("id = ?", libID).Update("cover_revision", "").Error
	}
	a.db.BumpLibraryRevision()
	c.Status(http.StatusNoContent)
}

func saveCoverBytes(a *AdminHandler, libID, kind, mime string, data []byte) string {
	sum := sha256.Sum256(data)
	rev := hex.EncodeToString(sum[:8])
	_ = a.db.Gorm.Save(&model.LibraryCover{
		LibraryID:  libID,
		Kind:       kind,
		Mime:       mime,
		Data:       data,
		Revision:   rev,
		SourceHash: rev,
		UpdatedAt:  time.Now().UTC(),
	}).Error
	if strings.HasPrefix(libID, "vlib-") {
		_ = a.db.Gorm.Model(&model.VirtualLibrary{}).Where("id = ?", libID).Update("cover_revision", rev).Error
	} else {
		_ = a.db.Gorm.Model(&model.Library{}).Where("id = ?", libID).Update("cover_revision", rev).Error
	}
	a.db.BumpLibraryRevision()
	return rev
}

// --- Intro & Credits Skip Markers ---

type introMarkerPayload struct {
	SeriesID          string  `json:"series_id"`
	SeasonNum         int     `json:"season_num"`
	ItemID            string  `json:"item_id"`
	IntroStartSecs    float64 `json:"intro_start_secs"`
	IntroEndSecs      float64 `json:"intro_end_secs"`
	CreditsStartSecs  float64 `json:"credits_start_secs"`
}

func (a *AdminHandler) ListIntroMarkers(c *gin.Context) {
	var markers []model.IntroMarker
	q := a.db.Gorm.Order("updated_at DESC")
	if sid := strings.TrimSpace(c.Query("series_id")); sid != "" {
		q = q.Where("series_id = ?", sid)
	}
	if iid := strings.TrimSpace(c.Query("item_id")); iid != "" {
		q = q.Where("item_id = ?", iid)
	}
	_ = q.Limit(200).Find(&markers).Error
	c.JSON(http.StatusOK, gin.H{"items": markers})
}

func (a *AdminHandler) UpsertIntroMarker(c *gin.Context) {
	var body introMarkerPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.ItemID == "" && body.SeriesID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "item_id or series_id is required"})
		return
	}
	if body.IntroEndSecs > 0 && body.IntroEndSecs <= body.IntroStartSecs {
		c.JSON(http.StatusBadRequest, gin.H{"error": "intro_end_secs must be greater than intro_start_secs"})
		return
	}

	var existing model.IntroMarker
	var err error
	if body.ItemID != "" {
		err = a.db.Gorm.Where("item_id = ?", body.ItemID).First(&existing).Error
	} else {
		err = a.db.Gorm.Where("series_id = ? AND season_num = ? AND item_id = ''", body.SeriesID, body.SeasonNum).First(&existing).Error
	}
	id := existing.ID
	if err != nil || id == "" {
		id = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	m := model.IntroMarker{
		ID:                id,
		SeriesID:          body.SeriesID,
		SeasonNum:         body.SeasonNum,
		ItemID:            body.ItemID,
		IntroStartTicks:   int64(body.IntroStartSecs * 1e7),
		IntroEndTicks:     int64(body.IntroEndSecs * 1e7),
		CreditsStartTicks: int64(body.CreditsStartSecs * 1e7),
		Source:            "manual",
		Confidence:        1.0,
		UpdatedAt:         time.Now().UTC(),
	}
	if err := a.db.Gorm.Save(&m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (a *AdminHandler) DeleteIntroMarker(c *gin.Context) {
	_ = a.db.Gorm.Where("id = ?", c.Param("id")).Delete(&model.IntroMarker{}).Error
	c.Status(http.StatusNoContent)
}

// GetItemIntroWeb returns resolved intro/credits marker (episode-specific > series+season fallback) for the Web player.
func (a *AdminHandler) GetItemIntroWeb(c *gin.Context) {
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
	var marker model.IntroMarker
	err = a.db.Gorm.Where("item_id = ?", item.ID).First(&marker).Error
	if err != nil && item.SeriesID != "" {
		err = a.db.Gorm.Where("series_id = ? AND season_num = ? AND item_id = ''", item.SeriesID, item.SeasonNum).First(&marker).Error
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"has_marker": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"has_marker":         true,
		"intro_start_secs":   float64(marker.IntroStartTicks) / 1e7,
		"intro_end_secs":     float64(marker.IntroEndTicks) / 1e7,
		"credits_start_secs": float64(marker.CreditsStartTicks) / 1e7,
		"source":             marker.Source,
	})
}
