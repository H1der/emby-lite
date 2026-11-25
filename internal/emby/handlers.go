package emby

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/cache"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	imgpipe "emby-lite/internal/image"
	"emby-lite/internal/model"
	"emby-lite/internal/playback"
	"emby-lite/internal/scanner"
	"emby-lite/internal/scraper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const principalCtxKey = "emby_principal"

type Handler struct {
	db       *database.DB
	cache    cache.Store
	cfg      *config.Config
	auth     *auth.Service
	scanner  *scanner.Service
	playback *playback.Service
	images   *imgpipe.Pipeline
	scraper  *scraper.Service
}

func NewHandler(db *database.DB, cacheStore cache.Store, cfg *config.Config, authSvc *auth.Service, scanSvc *scanner.Service, pbSvc *playback.Service, imgPipeline *imgpipe.Pipeline, scraperSvc *scraper.Service) *Handler {
	return &Handler{
		db:       db,
		cache:    cacheStore,
		cfg:      cfg,
		auth:     authSvc,
		scanner:  scanSvc,
		playback: pbSvc,
		images:   imgPipeline,
		scraper:  scraperSvc,
	}
}

func (h *Handler) embyMetadataDir() string {
	return strings.TrimSpace(h.db.GetSetting("emby_metadata_dir", h.cfg.Storage.EmbyMetadataDir))
}

func (h *Handler) embyMetadataWriteback() bool {
	def := "false"
	if h.cfg.Storage.EmbyMetadataWriteback {
		def = "true"
	}
	return strings.EqualFold(h.db.GetSetting("emby_metadata_writeback", def), "true")
}

// RequireAuth middleware verifies Emby token or API key and injects Principal into Gin context.
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := h.auth.AuthenticateRequest(c.Request)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, auth.ErrUserDisabled) {
				status = http.StatusForbidden
			}
			jsonErr(c, status, err.Error())
			c.Abort()
			return
		}
		c.Set(principalCtxKey, p)
		c.Next()
	}
}

func getPrincipal(c *gin.Context) *auth.Principal {
	if v, ok := c.Get(principalCtxKey); ok {
		if p, ok := v.(*auth.Principal); ok {
			return p
		}
	}
	return nil
}

func jsonErr(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg, "Message": msg})
}

// --- System & Auth ---

func (h *Handler) Health(c *gin.Context) {
	if err := h.db.Ping(c.Request.Context()); err != nil {
		jsonErr(c, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"database": h.db.Dialect,
		"cache":    h.cache.BackendName(),
	})
}

func (h *Handler) Ping(c *gin.Context) {
	c.String(http.StatusOK, "Emby Server")
}

func (h *Handler) SystemInfoPublic(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LocalAddress":           playback.PublicOrigin(c.Request, h.cfg.Server.PublicURL),
		"ServerName":             h.serverName(),
		"Version":                ProtocolVersion,
		"Id":                     h.serverID(),
		"StartupWizardCompleted": true,
	})
}

func (h *Handler) SystemInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LocalAddress":           playback.PublicOrigin(c.Request, h.cfg.Server.PublicURL),
		"ServerName":             h.serverName(),
		"Version":                ProtocolVersion,
		"Id":                     h.serverID(),
		"OperatingSystem":        "Linux",
		"HasPendingRestart":      false,
		"HasUpdateAvailable":     false,
		"SupportsLibraryMonitor": false,
		"WebSocketPortNumber":    0,
		"CanSelfRestart":         false,
		"CanLaunchWebBrowser":    false,
		"SupportsTranscoding":    false,
	})
}

func (h *Handler) SystemEndpoint(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"IsLocal":     true,
		"IsInNetwork": true,
	})
}

func (h *Handler) UsersPublic(c *gin.Context) {
	// Never enumerate user accounts publicly.
	c.JSON(http.StatusOK, []any{})
}

func (h *Handler) AuthenticateByName(c *gin.Context) {
	var body struct {
		Username string `json:"Username" form:"Username"`
		Pw       string `json:"Pw" form:"Pw"`
		Password string `json:"Password" form:"Password"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonErr(c, http.StatusBadRequest, "invalid login payload")
		return
	}
	pw := body.Pw
	if pw == "" {
		pw = body.Password
	}
	meta := auth.ExtractClientMeta(c.Request)
	user, token, err := h.auth.AuthenticateByName(c.Request.Context(), body.Username, pw, meta)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			jsonErr(c, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, auth.ErrUserDisabled), errors.Is(err, auth.ErrDeviceLimitReached):
			jsonErr(c, http.StatusForbidden, err.Error())
		default:
			jsonErr(c, http.StatusUnauthorized, "invalid username or password")
		}
		return
	}

	policy := h.auth.ResolvePolicy(user)
	now := time.Now().UTC().Format(time.RFC3339)
	c.JSON(http.StatusOK, gin.H{
		"User":        h.userDTO(user, policy),
		"AccessToken": token,
		"ServerId":    h.serverID(),
		"SessionInfo": gin.H{
			"Id":                      meta.DeviceID,
			"UserId":                  user.ID,
			"UserName":                user.Username,
			"Client":                  meta.Client,
			"DeviceId":                meta.DeviceID,
			"DeviceName":              meta.Device,
			"ApplicationVersion":      meta.Version,
			"LastActivityDate":        now,
			"SupportsRemoteControl":   false,
			"SupportsMediaControl":    false,
			"PlayableMediaTypes":      []string{"Video"},
			"SupportedCommands":       []string{},
		},
	})
}

func (h *Handler) UserMe(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || p.IsAPIKey || p.User == nil {
		jsonErr(c, http.StatusUnauthorized, "user token required")
		return
	}
	c.JSON(http.StatusOK, h.userDTO(p.User, p.Policy))
}

func (h *Handler) GetUser(c *gin.Context) {
	p := getPrincipal(c)
	targetID := c.Param("userId")
	if p.User != nil && !p.User.IsAdmin && p.User.ID != targetID {
		jsonErr(c, http.StatusForbidden, "cannot access another user's profile")
		return
	}
	var u model.User
	if err := h.db.Gorm.Where("id = ?", targetID).First(&u).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "user not found")
		return
	}
	c.JSON(http.StatusOK, h.userDTO(&u, h.auth.ResolvePolicy(&u)))
}

func (h *Handler) Logout(c *gin.Context) {
	p := getPrincipal(c)
	if p != nil && p.Token != "" && !p.IsAPIKey {
		_ = h.auth.Logout(c.Request.Context(), p.Token)
	}
	c.Status(http.StatusNoContent)
}

// --- Libraries & Views ---

func (h *Handler) UserViews(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkUserScope(c, p, c.Param("userId")) {
		return
	}
	visibleIDs, err := h.auth.VisibleLibraryIDs(p)
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	uid := "anon"
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	cacheKey := "views:" + h.db.GetLibraryRevision() + ":" + uid + ":" + visibleIDsHash(visibleIDs)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		items, err := h.buildUserViewsItems(p, visibleIDs)
		if err != nil {
			return nil, err
		}
		return gin.H{
			"Items":            items,
			"TotalRecordCount": len(items),
			"StartIndex":       0,
		}, nil
	})
}

func (h *Handler) VirtualFolders(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	var libs []model.Library
	if len(visibleIDs) > 0 {
		_ = h.db.Gorm.Where("id IN ?", visibleIDs).Order("sort_order ASC, name ASC").Find(&libs).Error
	}
	out := make([]map[string]any, 0, len(libs))
	for _, l := range libs {
		var paths []string
		_ = json.Unmarshal([]byte(l.PathsJSON), &paths)
		out = append(out, map[string]any{
			"Name":           l.Name,
			"ItemId":         l.ID,
			"CollectionType": l.Kind,
			"Locations":      paths,
		})
	}
	if strings.HasSuffix(strings.ToLower(c.Request.URL.Path), "/query") {
		c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out)})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SelectableMediaFolders(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	var libs []model.Library
	if len(visibleIDs) > 0 {
		_ = h.db.Gorm.Where("id IN ?", visibleIDs).Order("sort_order ASC, name ASC").Find(&libs).Error
	}
	out := make([]map[string]any, 0, len(libs))
	for _, l := range libs {
		var paths []string
		_ = json.Unmarshal([]byte(l.PathsJSON), &paths)
		subs := make([]map[string]any, 0, len(paths))
		for _, pt := range paths {
			subs = append(subs, map[string]any{"Name": filepath.Base(pt), "Path": pt, "Id": l.ID})
		}
		out = append(out, map[string]any{
			"Name":       l.Name,
			"Id":         l.ID,
			"SubFolders": subs,
		})
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) RootItem(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"Name":     "Media Folders",
		"ServerId": h.serverID(),
		"Id":       "root",
		"Type":     "AggregateFolder",
		"IsFolder": true,
	})
}

// --- Items Browsing, Search, Detail ---

func (h *Handler) GetItems(c *gin.Context) {
	p := getPrincipal(c)
	if uidParam := c.Param("userId"); uidParam != "" && !h.checkUserScope(c, p, uidParam) {
		return
	}
	visibleIDs, err := h.auth.VisibleLibraryIDs(p)
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}

	parentID := queryCI(c, "ParentId")
	if childParent := c.Param("itemId"); childParent != "" {
		parentID = childParent
	}
	recursive := strings.EqualFold(queryCI(c, "Recursive"), "true")
	idsParam := queryCI(c, "Ids")
	searchTerm := strings.TrimSpace(queryCI(c, "SearchTerm"))
	nameStartsWith := strings.TrimSpace(queryCI(c, "NameStartsWith"))
	includeTypes := splitCSV(queryCI(c, "IncludeItemTypes"))
	filters := splitCSV(queryCI(c, "Filters"))
	genres := splitMulti(queryCI(c, "Genres"))
	tags := splitMulti(queryCI(c, "Tags"))
	years := splitCSV(queryCI(c, "Years"))
	personIDs := splitCSV(queryCI(c, "PersonIds"))
	isFavorite := queryCI(c, "IsFavorite")
	isPlayed := queryCI(c, "IsPlayed")

	startIndex := queryIntCI(c, "StartIndex", 0)
	if startIndex < 0 {
		startIndex = 0
	}
	limit := queryIntCI(c, "Limit", 60)
	if limit <= 0 {
		limit = 60
	}
	if limit > 10000 {
		limit = 10000
	}

	uid := ""
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	scopeHash := cursorScopeHash(
		uid,
		parentID,
		searchTerm,
		queryCI(c, "IncludeItemTypes"),
		queryCI(c, "SortBy"),
		queryCI(c, "SortOrder"),
		queryCI(c, "Filters")+queryCI(c, "Genres")+queryCI(c, "Tags")+queryCI(c, "Years"),
	)
	if cursorTok := strings.TrimSpace(queryCI(c, "Cursor")); cursorTok != "" {
		cp, err := verifyCursor(h.auth.ImageSecret(), cursorTok, scopeHash)
		if err != nil {
			jsonErr(c, http.StatusBadRequest, "invalid cursor: "+err.Error())
			return
		}
		startIndex = cp.Offset
		limit = cp.Limit
	}

	q := h.db.Read().Model(&model.Item{}).
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", visibleIDs)

	if idsParam != "" {
		q = q.Where("items.id IN ?", splitCSV(idsParam))
	}
	defaultVirtualSort := ""
	if strings.HasPrefix(parentID, "vlib-") {
		var vl model.VirtualLibrary
		if err := h.db.Gorm.Where("id = ? AND enabled = ?", parentID, true).First(&vl).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": startIndex, "HasMore": false})
			return
		}
		var rule struct {
			LibraryIDs []string `json:"library_ids"`
			ItemTypes  []string `json:"item_types"`
			Genres     []string `json:"genres"`
			Tags       []string `json:"tags"`
			MinRating  float64  `json:"min_rating"`
			MinYear    int      `json:"min_year"`
			MaxYear    int      `json:"max_year"`
			SortBy     string   `json:"sort_by"`
		}
		_ = json.Unmarshal([]byte(vl.RuleJSON), &rule)
		if len(rule.LibraryIDs) > 0 {
			// Intersect with user's visible libraries so virtual libraries never leak restricted libraries!
			q = q.Where("items.library_id IN ?", rule.LibraryIDs)
		}
		if len(rule.ItemTypes) > 0 && len(includeTypes) == 0 {
			q = q.Where("items.type IN ?", rule.ItemTypes)
		} else if len(includeTypes) == 0 {
			q = q.Where("items.type IN ?", []string{"Movie", "Series"})
		}
		if len(rule.Genres) > 0 {
			q = q.Where("items.id IN (SELECT item_id FROM item_genres WHERE genre IN ?)", rule.Genres)
		}
		if len(rule.Tags) > 0 {
			q = q.Where("items.id IN (SELECT item_id FROM item_tags WHERE tag IN ?)", rule.Tags)
		}
		if rule.MinRating > 0 {
			q = q.Where("items.community_rating >= ?", rule.MinRating)
		}
		if rule.MinYear > 0 {
			q = q.Where("items.year >= ?", rule.MinYear)
		}
		if rule.MaxYear > 0 {
			q = q.Where("items.year <= ?", rule.MaxYear)
		}
		defaultVirtualSort = rule.SortBy
	} else if parentID != "" && parentID != "root" {
		if recursive {
			// Parent could be a library, series, or season.
			q = q.Where("(items.library_id = ? OR items.parent_id = ? OR items.series_id = ? OR items.season_id = ?)",
				parentID, parentID, parentID, parentID)
		} else {
			q = q.Where("items.parent_id = ?", parentID)
		}
	} else if !recursive && len(includeTypes) == 0 && searchTerm == "" && idsParam == "" {
		q = q.Where("items.type IN ?", []string{"Movie", "Series"})
	}

	if len(includeTypes) > 0 {
		q = q.Where("items.type IN ?", includeTypes)
	}

	if searchTerm != "" {
		like := "%" + strings.ToLower(searchTerm) + "%"
		switch {
		case h.db.Dialect == "postgres" && h.db.HasTrgm:
			q = q.Where("(items.name % ? OR items.original_name % ? OR items.name ILIKE ? OR items.original_name ILIKE ? OR items.pinyin_initials ILIKE ?)",
				searchTerm, searchTerm, like, like, like)
		case h.db.Dialect == "sqlite" && h.db.HasFTS5:
			ftsQuery := sanitizeFTSQuery(searchTerm)
			if ftsQuery != "" {
				q = q.Where("(items.id IN (SELECT item_id FROM items_fts WHERE items_fts MATCH ?) OR LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)",
					ftsQuery, like, like, like)
			} else {
				q = q.Where("(LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)", like, like, like)
			}
		default:
			q = q.Where("(LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)", like, like, like)
		}
	}
	if nameStartsWith != "" {
		prefix := strings.ToLower(nameStartsWith) + "%"
		q = q.Where("(LOWER(items.sort_name) LIKE ? OR items.pinyin_initials LIKE ?)", prefix, prefix)
	}
	if len(years) > 0 {
		var yi []int
		for _, ys := range years {
			if n, err := strconv.Atoi(ys); err == nil {
				yi = append(yi, n)
			}
		}
		if len(yi) > 0 {
			q = q.Where("items.year IN ?", yi)
		}
	}
	if len(genres) > 0 {
		q = q.Where("items.id IN (SELECT item_id FROM item_genres WHERE genre IN ?)", genres)
	}
	if len(tags) > 0 {
		q = q.Where("items.id IN (SELECT item_id FROM item_tags WHERE tag IN ?)", tags)
	}
	if len(personIDs) > 0 {
		q = q.Where("items.id IN (SELECT item_id FROM item_people WHERE person_id IN ?)", personIDs)
	}

	// UserData filters.
	wantFav := strings.EqualFold(isFavorite, "true") || containsCI(filters, "IsFavorite")
	wantPlayed := strings.EqualFold(isPlayed, "true") || containsCI(filters, "IsPlayed")
	wantUnplayed := strings.EqualFold(isPlayed, "false") || containsCI(filters, "IsUnplayed")
	wantResumable := containsCI(filters, "IsResumable")

	if uid != "" {
		if wantFav {
			q = q.Where("items.id IN (SELECT item_id FROM user_data WHERE user_id = ? AND is_favorite = ?)", uid, true)
		}
		if wantPlayed {
			q = q.Where("items.id IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", uid, true)
		}
		if wantUnplayed {
			q = q.Where("items.id NOT IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", uid, true)
		}
		if wantResumable {
			q = q.Where("items.id IN (SELECT item_id FROM user_data WHERE user_id = ? AND position_ticks > 0 AND played = ? AND hide_from_resume = ?)", uid, false, false)
		}
	}

	var total int64
	if !strings.EqualFold(queryCI(c, "EnableTotalRecordCount"), "false") {
		if err := q.Count(&total).Error; err != nil {
			jsonErr(c, http.StatusInternalServerError, err.Error())
			return
		}
	}

	sortByParam := queryCI(c, "SortBy")
	if sortByParam == "" && defaultVirtualSort != "" {
		sortByParam = defaultVirtualSort
	}
	orderClause := buildOrderClause(sortByParam, queryCI(c, "SortOrder"))
	var rows []model.Item
	if err := q.Order(orderClause).Offset(startIndex).Limit(limit).Find(&rows).Error; err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}

	fields := strings.ToLower(queryCI(c, "Fields"))
	needDetail := strings.Contains(fields, "mediasources") || strings.Contains(fields, "mediastreams") || strings.Contains(fields, "people")

	out := h.itemsToDTOs(c.Request, p, rows, needDetail)
	if total == 0 && len(out) > 0 {
		total = int64(startIndex + len(out))
	}

	hasMore := int64(startIndex+len(out)) < total
	nextCursor := ""
	if hasMore {
		nextCursor = signCursor(h.auth.ImageSecret(), cursorPayload{
			Offset:    startIndex + len(out),
			Limit:     limit,
			ScopeHash: scopeHash,
			Exp:       time.Now().Add(15 * time.Minute).Unix(),
		})
		c.Header("X-Next-Cursor", nextCursor)
	}

	c.JSON(http.StatusOK, gin.H{
		"Items":            out,
		"TotalRecordCount": total,
		"StartIndex":       startIndex,
		"NextCursor":       nextCursor,
		"HasMore":          hasMore,
	})
}

func (h *Handler) GetItemDetail(c *gin.Context) {
	p := getPrincipal(c)
	if uidParam := c.Param("userId"); uidParam != "" && !h.checkUserScope(c, p, uidParam) {
		return
	}
	itemID := c.Param("itemId")

	// Check if itemID is a VirtualLibrary ID.
	if strings.HasPrefix(itemID, "vlib-") {
		var vl model.VirtualLibrary
		if err := h.db.Gorm.Where("id = ? AND enabled = ?", itemID, true).First(&vl).Error; err == nil {
			c.JSON(http.StatusOK, h.virtualLibraryViewDTO(&vl, p))
			return
		}
	}

	// Check if itemID is a Library ID (some clients request /Items/{libraryId}).
	var lib model.Library
	if err := h.db.Gorm.Where("id = ?", itemID).First(&lib).Error; err == nil {
		if !h.auth.CanAccessLibrary(p, lib.ID) {
			jsonErr(c, http.StatusNotFound, "library not found")
			return
		}
		c.JSON(http.StatusOK, h.libraryViewDTO(&lib, p))
		return
	}

	var item model.Item
	if err := h.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	if !h.auth.CanAccessLibrary(p, item.LibraryID) {
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	c.JSON(http.StatusOK, h.itemDTO(c.Request, p, &item, true))
}

func (h *Handler) GetLatest(c *gin.Context) {
	p := getPrincipal(c)
	if uidParam := c.Param("userId"); uidParam != "" && !h.checkUserScope(c, p, uidParam) {
		return
	}
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, []any{})
		return
	}
	parentID := queryCI(c, "ParentId")
	limit := queryIntCI(c, "Limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	uid := "anon"
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	cacheKey := "latest:" + h.db.GetLibraryRevision() + ":" + h.getUserDataRev(c.Request.Context(), uid) + ":" + uid + ":" + visibleIDsHash(visibleIDs) + ":" + parentID + ":" + strconv.Itoa(limit)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		// Per Emby spec, /Items/Latest returns a plain JSON array.
		return h.buildLatestDTOs(c.Request, p, visibleIDs, parentID, limit), nil
	})
}

func (h *Handler) GetResume(c *gin.Context) {
	p := getPrincipal(c)
	if uidParam := c.Param("userId"); uidParam != "" && !h.checkUserScope(c, p, uidParam) {
		return
	}
	if p == nil || p.User == nil {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	limit := queryIntCI(c, "Limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	cacheKey := "resume:" + h.db.GetLibraryRevision() + ":" + h.getUserDataRev(c.Request.Context(), p.User.ID) + ":" + p.User.ID + ":" + visibleIDsHash(visibleIDs) + ":" + strconv.Itoa(limit)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		out := h.buildResumeDTOs(c.Request, p, visibleIDs, limit)
		return gin.H{
			"Items":            out,
			"TotalRecordCount": len(out),
			"StartIndex":       0,
		}, nil
	})
}

func (h *Handler) GetSeasons(c *gin.Context) {
	p := getPrincipal(c)
	seriesID := c.Param("seriesId")
	series := h.getItemByID(seriesID)
	if series == nil || !h.auth.CanAccessLibrary(p, series.LibraryID) {
		jsonErr(c, http.StatusNotFound, "series not found")
		return
	}
	var seasons []model.Item
	_ = h.db.Read().Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", seriesID, "Season").
		Order("season_num ASC").Find(&seasons).Error
	out := h.itemsToDTOs(c.Request, p, seasons, false)
	c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
}

func (h *Handler) GetEpisodes(c *gin.Context) {
	p := getPrincipal(c)
	seriesID := c.Param("seriesId")
	series := h.getItemByID(seriesID)
	if series == nil || !h.auth.CanAccessLibrary(p, series.LibraryID) {
		jsonErr(c, http.StatusNotFound, "series not found")
		return
	}
	q := h.db.Read().Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", seriesID, "Episode")
	if sid := queryCI(c, "SeasonId"); sid != "" {
		q = q.Where("season_id = ?", sid)
	}
	if snum := queryCI(c, "Season"); snum != "" {
		if n, err := strconv.Atoi(snum); err == nil {
			q = q.Where("season_num = ?", n)
		}
	}
	var eps []model.Item
	_ = q.Order("season_num ASC, episode_num ASC, sort_name ASC").Find(&eps).Error
	out := h.itemsToDTOs(c.Request, p, eps, true)
	c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
}

func (h *Handler) GetNextUp(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || p.User == nil {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	limit := queryIntCI(c, "Limit", 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	cacheKey := "nextup:" + h.db.GetLibraryRevision() + ":" + h.getUserDataRev(c.Request.Context(), p.User.ID) + ":" + p.User.ID + ":" + visibleIDsHash(visibleIDs) + ":" + strconv.Itoa(limit)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		out := h.buildNextUpDTOs(c.Request, p, visibleIDs, limit)
		return gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0}, nil
	})
}

func (h *Handler) GetSimilar(c *gin.Context) {
	p := getPrincipal(c)
	item := h.getItemByID(c.Param("itemId"))
	if item == nil || !h.auth.CanAccessLibrary(p, item.LibraryID) {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0})
		return
	}
	limit := queryIntCI(c, "Limit", 12)
	var rows []model.Item
	_ = h.db.Read().Where("library_id = ? AND type = ? AND id <> ? AND soft_deleted_at IS NULL", item.LibraryID, item.Type, item.ID).
		Order("added_at DESC").Limit(limit).Find(&rows).Error
	out := h.itemsToDTOs(c.Request, p, rows, false)
	c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out)})
}

func (h *Handler) GetCounts(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	cacheKey := "counts:" + h.db.GetLibraryRevision() + ":" + visibleIDsHash(visibleIDs)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		return h.buildCounts(visibleIDs), nil
	})
}

func (h *Handler) GetGenres(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	cacheKey := "genres:" + h.db.GetLibraryRevision() + ":" + visibleIDsHash(visibleIDs)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		var names []string
		if len(visibleIDs) > 0 {
			_ = h.db.Read().Table("item_genres").
				Select("DISTINCT item_genres.genre").
				Joins("JOIN items ON items.id = item_genres.item_id").
				Where("items.library_id IN ? AND items.soft_deleted_at IS NULL", visibleIDs).
				Order("item_genres.genre ASC").
				Pluck("genre", &names).Error
		}
		sid := h.serverID()
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, map[string]any{
				"Name":     n,
				"Id":       "genre-" + n,
				"ServerId": sid,
				"Type":     "Genre",
			})
		}
		return gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}, nil
	})
}

func (h *Handler) GetTags(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	cacheKey := "tags:" + h.db.GetLibraryRevision() + ":" + visibleIDsHash(visibleIDs)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		var names []string
		if len(visibleIDs) > 0 {
			_ = h.db.Read().Table("item_tags").
				Select("DISTINCT item_tags.tag").
				Joins("JOIN items ON items.id = item_tags.item_id").
				Where("items.library_id IN ? AND items.soft_deleted_at IS NULL", visibleIDs).
				Order("item_tags.tag ASC").
				Pluck("tag", &names).Error
		}
		sid := h.serverID()
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, map[string]any{"Name": n, "Id": "tag-" + n, "ServerId": sid, "Type": "Tag"})
		}
		return gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}, nil
	})
}

func (h *Handler) GetFilters(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	parentID := queryCI(c, "ParentId")
	isV2 := strings.HasSuffix(strings.ToLower(c.Request.URL.Path), "filters2")
	cacheKey := "filters:" + h.db.GetLibraryRevision() + ":" + visibleIDsHash(visibleIDs) + ":" + parentID + ":" + strconv.FormatBool(isV2)
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		var genres []string
		var years []int
		if len(visibleIDs) > 0 {
			gq := h.db.Read().Table("item_genres").
				Select("DISTINCT item_genres.genre").
				Joins("JOIN items ON items.id = item_genres.item_id").
				Where("items.library_id IN ? AND items.soft_deleted_at IS NULL", visibleIDs)
			yq := h.db.Read().Model(&model.Item{}).
				Select("DISTINCT year").
				Where("library_id IN ? AND soft_deleted_at IS NULL AND year > 0", visibleIDs)
			if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
				gq = gq.Where("items.library_id = ?", parentID)
				yq = yq.Where("library_id = ?", parentID)
			}
			_ = gq.Order("item_genres.genre ASC").Pluck("genre", &genres).Error
			_ = yq.Order("year DESC").Pluck("year", &years).Error
		}
		if genres == nil {
			genres = []string{}
		}
		if years == nil {
			years = []int{}
		}
		if isV2 {
			gObjs := make([]map[string]any, 0, len(genres))
			for _, g := range genres {
				gObjs = append(gObjs, map[string]any{"Name": g, "Id": "genre-" + g})
			}
			return gin.H{"Genres": gObjs, "Tags": []any{}, "Years": years, "OfficialRatings": []any{}}, nil
		}
		return gin.H{"Genres": genres, "Tags": []string{}, "Years": years, "OfficialRatings": []string{}}, nil
	})
}

func (h *Handler) DisplayPreferences(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"Id":           c.Param("id"),
		"SortBy":       "SortName",
		"SortOrder":    "Ascending",
		"CustomPrefs":  gin.H{},
		"Client":       queryCI(c, "client"),
	})
}

func (h *Handler) EmptyArray(c *gin.Context) {
	c.JSON(http.StatusOK, []any{})
}

// --- Images & Subtitles ---

func (h *Handler) GetItemImage(c *gin.Context) {
	itemID := c.Param("itemId")
	imgType := normalizeImageKind(c.Param("type"))
	idx, _ := strconv.Atoi(c.Param("index"))

	// Authenticate either via normal Emby token or signed tag.
	p, _ := h.auth.AuthenticateRequest(c.Request)
	tagParam := queryCI(c, "tag")
	maxWidth := queryIntCI(c, "maxWidth", queryIntCI(c, "width", 0))
	maxHeight := queryIntCI(c, "maxHeight", queryIntCI(c, "height", 0))
	quality := queryIntCI(c, "quality", 85)

	// Check VirtualLibrary cover first.
	if strings.HasPrefix(itemID, "vlib-") {
		var cover model.LibraryCover
		if err := h.db.Gorm.Where("library_id = ?", itemID).First(&cover).Error; err == nil {
			etag := `"` + cover.Revision + `"`
			if c.GetHeader("If-None-Match") == etag {
				c.Status(http.StatusNotModified)
				return
			}
			c.Header("ETag", etag)
			c.Header("Cache-Control", "private, max-age=86400")
			c.Data(http.StatusOK, cover.Mime, cover.Data)
			return
		}
		c.Status(http.StatusNotFound)
		return
	}

	// Check Library cover first.
	var lib model.Library
	if err := h.db.Gorm.Where("id = ?", itemID).First(&lib).Error; err == nil {
		if p != nil && !h.auth.CanAccessLibrary(p, lib.ID) {
			c.Status(http.StatusNotFound)
			return
		}
		var cover model.LibraryCover
		if err := h.db.Gorm.Where("library_id = ?", lib.ID).First(&cover).Error; err == nil {
			c.Header("ETag", `"`+cover.Revision+`"`)
			c.Header("Cache-Control", "private, max-age=86400")
			c.Data(http.StatusOK, cover.Mime, cover.Data)
			return
		}
		// Fallback to latest item's Primary image in that library.
		var firstImg model.ItemImage
		err := h.db.Gorm.Table("item_images").
			Joins("JOIN items ON items.id = item_images.item_id").
			Where("items.library_id = ? AND items.soft_deleted_at IS NULL AND item_images.kind = ?", lib.ID, "Primary").
			Order("items.added_at DESC").
			First(&firstImg).Error
		if err == nil && firstImg.SourceType == "local" {
			h.serveLocalImage(c, firstImg.SourceRef, firstImg.Tag, maxWidth, maxHeight, quality)
			return
		}
		c.Status(http.StatusNotFound)
		return
	}

	item := h.getItemByID(itemID)
	if item == nil {
		c.Status(http.StatusNotFound)
		return
	}
	if p != nil {
		if !h.auth.CanAccessLibrary(p, item.LibraryID) {
			c.Status(http.StatusNotFound)
			return
		}
	} else if tagParam == "" {
		c.Status(http.StatusUnauthorized)
		return
	}

	var img model.ItemImage
	err := h.db.Gorm.Where("item_id = ? AND kind = ? AND index_num = ?", itemID, imgType, idx).First(&img).Error
	if err != nil && imgType == "Primary" && item.Type == "Episode" && item.SeriesID != "" {
		// Fallback to Series poster when episode lacks a thumb.
		err = h.db.Gorm.Where("item_id = ? AND kind = ? AND index_num = ?", item.SeriesID, "Primary", 0).First(&img).Error
	}
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	if img.SourceType == "local" {
		h.serveLocalImage(c, img.SourceRef, img.Tag, maxWidth, maxHeight, quality)
		return
	}
	if img.SourceType == "remote" && h.images != nil {
		remoteURL := img.SourceRef
		var hc *http.Client
		if h.scraper != nil {
			remoteURL, hc = h.scraper.ResolveRemoteImage(remoteURL)
		}
		if err := h.images.ServeRemoteURLWithClient(c.Request.Context(), c.Writer, c.Request, remoteURL, img.Tag, maxWidth, maxHeight, quality, hc); err != nil {
			c.Status(http.StatusNotFound)
		}
		return
	}
	c.Status(http.StatusNotFound)
}

func (h *Handler) GetPersons(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	if len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	limit := queryIntCI(c, "Limit", 60)
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	startIndex := queryIntCI(c, "StartIndex", 0)
	search := strings.TrimSpace(queryCI(c, "SearchTerm"))

	q := h.db.Gorm.Table("people").
		Select("DISTINCT people.*").
		Joins("JOIN item_people ON item_people.person_id = people.id").
		Joins("JOIN items ON items.id = item_people.item_id").
		Where("items.library_id IN ? AND items.soft_deleted_at IS NULL", visibleIDs)
	if search != "" {
		q = q.Where("LOWER(people.name) LIKE ?", "%"+strings.ToLower(search)+"%")
	}
	var total int64
	_ = q.Count(&total).Error

	var rows []model.Person
	_ = q.Order("people.name ASC").Offset(startIndex).Limit(limit).Find(&rows).Error
	metaDir := h.embyMetadataDir()
	items := make([]map[string]any, 0, len(rows))
	for _, pr := range rows {
		imgTags := map[string]string{}
		if pr.ThumbPath != "" || (metaDir != "" && imgpipe.FindEmbyPersonImage(metaDir, pr.Name, pr.TMDBID) != "") {
			imgTags["Primary"] = h.signTag(p, pr.ID, "Primary", "person")
		}
		items = append(items, map[string]any{
			"Id":        pr.ID,
			"Name":      pr.Name,
			"ServerId":  h.serverID(),
			"Type":      "Person",
			"ImageTags": imgTags,
		})
	}
	c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": total, "StartIndex": startIndex})
}

func (h *Handler) GetPersonDetail(c *gin.Context) {
	p := getPrincipal(c)
	key := c.Param("personId")
	var pr model.Person
	if err := h.db.Gorm.Where("id = ? OR LOWER(name) = ?", key, strings.ToLower(key)).First(&pr).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "person not found")
		return
	}
	metaDir := h.embyMetadataDir()
	imgTags := map[string]string{}
	if pr.ThumbPath != "" || (metaDir != "" && imgpipe.FindEmbyPersonImage(metaDir, pr.Name, pr.TMDBID) != "") {
		imgTags["Primary"] = h.signTag(p, pr.ID, "Primary", "person")
	}
	c.JSON(http.StatusOK, map[string]any{
		"Id":        pr.ID,
		"Name":      pr.Name,
		"ServerId":  h.serverID(),
		"Type":      "Person",
		"ImageTags": imgTags,
		"ProviderIds": func() map[string]string {
			if pr.TMDBID != "" {
				return map[string]string{"Tmdb": pr.TMDBID}
			}
			return map[string]string{}
		}(),
	})
}

func (h *Handler) GetPersonImage(c *gin.Context) {
	key := c.Param("personId")
	var pr model.Person
	if err := h.db.Gorm.Where("id = ? OR LOWER(name) = ?", key, strings.ToLower(key)).First(&pr).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	maxW := queryIntCI(c, "maxWidth", queryIntCI(c, "width", 0))
	maxH := queryIntCI(c, "maxHeight", queryIntCI(c, "height", 0))
	quality := queryIntCI(c, "quality", 85)

	// 1. Local .actors/ or local absolute file path on Person record.
	if pr.ThumbPath != "" && !strings.HasPrefix(pr.ThumbPath, "http://") && !strings.HasPrefix(pr.ThumbPath, "https://") {
		if _, err := os.Stat(pr.ThumbPath); err == nil {
			h.serveLocalImage(c, pr.ThumbPath, pr.ID, maxW, maxH, quality)
			return
		}
	}

	// 2. Mounted Emby metadata/people/ directory (zero-copy direct hit!).
	metaDir := h.embyMetadataDir()
	if metaDir != "" {
		if localEmbyImg := imgpipe.FindEmbyPersonImage(metaDir, pr.Name, pr.TMDBID); localEmbyImg != "" {
			h.serveLocalImage(c, localEmbyImg, pr.ID, maxW, maxH, quality)
			return
		}
	}

	// 3. Remote URL (from NFO/TMDB scrape) or on-demand TMDB person lookup.
	remoteURL := pr.ThumbPath
	if remoteURL == "" && h.scraper != nil {
		if tid, u := h.scraper.LookupPersonPortrait(c.Request.Context(), pr.Name); u != "" {
			remoteURL = u
			updates := map[string]any{"thumb_path": u, "updated_at": time.Now().UTC()}
			if tid != "" && pr.TMDBID == "" {
				updates["tmdb_id"] = tid
				pr.TMDBID = tid
			}
			_ = h.db.Gorm.Model(&pr).Updates(updates).Error
		}
	}

	if strings.HasPrefix(remoteURL, "http://") || strings.HasPrefix(remoteURL, "https://") {
		if h.images == nil {
			c.Status(http.StatusNotFound)
			return
		}
		var hc *http.Client
		if h.scraper != nil {
			remoteURL, hc = h.scraper.ResolveRemoteImage(remoteURL)
		}
		rawPath, fetchedBytes, err := h.images.FetchRemoteCached(c.Request.Context(), remoteURL, hc)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		// Optional fill-missing-only writeback into writable Emby metadata/people/ directory!
		if metaDir != "" && h.embyMetadataWriteback() {
			data := fetchedBytes
			if len(data) == 0 {
				data, _ = os.ReadFile(rawPath)
			}
			if writtenPath, wrote := imgpipe.WriteEmbyPersonImageIfMissing(metaDir, pr.Name, pr.TMDBID, data); wrote && writtenPath != "" {
				rawPath = writtenPath
			}
		}
		h.serveLocalImage(c, rawPath, pr.ID, maxW, maxH, quality)
		return
	}

	c.Status(http.StatusNotFound)
}

func (h *Handler) serveLocalImage(c *gin.Context, path, tag string, maxW, maxH, quality int) {
	if h.images != nil {
		if err := h.images.ServeFile(c.Writer, c.Request, path, tag, maxW, maxH, quality); err != nil {
			c.Status(http.StatusNotFound)
		}
		return
	}
	serveImageFile(c, path, tag)
}

func serveImageFile(c *gin.Context, path, tag string) {
	etag := `"` + tag + `"`
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=86400")
	http.ServeContent(c.Writer, c.Request, fi.Name(), fi.ModTime(), f)
}

func (h *Handler) GetSubtitleStream(c *gin.Context) {
	p := getPrincipal(c)
	itemID := c.Param("itemId")
	sourceID := c.Param("sourceId")
	idx, err := strconv.Atoi(c.Param("index"))
	if err != nil {
		jsonErr(c, http.StatusBadRequest, "invalid subtitle index")
		return
	}
	item := h.getItemByID(itemID)
	if item == nil || !h.auth.CanAccessLibrary(p, item.LibraryID) {
		jsonErr(c, http.StatusNotFound, "subtitle not found")
		return
	}
	if !p.Policy.AllowPlayback {
		jsonErr(c, http.StatusForbidden, "playback not permitted")
		return
	}
	var sub model.MediaSubtitle
	if err := h.db.Gorm.Where("item_id = ? AND media_source_id = ? AND stream_index = ?", itemID, sourceID, idx).First(&sub).Error; err != nil {
		jsonErr(c, http.StatusNotFound, "subtitle not found")
		return
	}
	reqExt := strings.ToLower(strings.TrimPrefix(c.Param("ext"), "."))
	if reqExt != "" && reqExt != strings.ToLower(sub.Codec) {
		jsonErr(c, http.StatusNotFound, "subtitle format conversion is not supported")
		return
	}
	f, err := os.Open(sub.Path)
	if err != nil {
		jsonErr(c, http.StatusNotFound, "subtitle file missing")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		jsonErr(c, http.StatusNotFound, "subtitle file invalid")
		return
	}
	ct := "text/plain; charset=utf-8"
	if sub.Codec == "vtt" {
		ct = "text/vtt; charset=utf-8"
	}
	c.Header("Content-Type", ct)
	c.Header("Cache-Control", "private, max-age=3600")
	http.ServeContent(c.Writer, c.Request, fi.Name(), fi.ModTime(), f)
}

// --- Playback & Progress ---

func (h *Handler) PlaybackInfo(c *gin.Context) {
	p := getPrincipal(c)
	item := h.getItemByID(c.Param("itemId"))
	if item == nil || !h.auth.CanAccessLibrary(p, item.LibraryID) {
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	c.JSON(http.StatusOK, h.playback.BuildPlaybackInfo(c.Request, p, item))
}

func (h *Handler) StreamVideo(c *gin.Context) {
	p := getPrincipal(c)
	item := h.getItemByID(c.Param("itemId"))
	if item == nil {
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	sourceID := queryCI(c, "MediaSourceId")
	h.playback.Stream(c.Writer, c.Request, p, item, sourceID, false)
}

func (h *Handler) DownloadVideo(c *gin.Context) {
	p := getPrincipal(c)
	item := h.getItemByID(c.Param("itemId"))
	if item == nil {
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	sourceID := queryCI(c, "MediaSourceId")
	h.playback.Stream(c.Writer, c.Request, p, item, sourceID, true)
}

func (h *Handler) RejectTranscode(c *gin.Context) {
	jsonErr(c, http.StatusBadRequest, "server-side transcoding and HLS are not supported; use direct play")
}

type playbackEventBody struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId"`
	PlaySessionID string `json:"PlaySessionId"`
	PositionTicks *int64 `json:"PositionTicks"`
	RunTimeTicks  *int64 `json:"RunTimeTicks"`
	IsPaused      bool   `json:"IsPaused"`
}

func (h *Handler) ReportPlaying(c *gin.Context) {
	h.handlePlaybackEvent(c, "start")
}

func (h *Handler) ReportProgress(c *gin.Context) {
	h.handlePlaybackEvent(c, "progress")
}

func (h *Handler) ReportStopped(c *gin.Context) {
	h.handlePlaybackEvent(c, "stop")
}

func (h *Handler) handlePlaybackEvent(c *gin.Context, kind string) {
	p := getPrincipal(c)
	if p == nil || p.IsAPIKey || p.User == nil {
		jsonErr(c, http.StatusUnauthorized, "user token required")
		return
	}
	var body playbackEventBody
	_ = c.ShouldBindJSON(&body)
	if body.ItemID == "" {
		body.ItemID = queryCI(c, "ItemId")
	}
	if body.ItemID == "" {
		c.Status(http.StatusNoContent)
		return
	}

	var pos int64
	hasPos := body.PositionTicks != nil && *body.PositionTicks >= 0
	if hasPos {
		pos = *body.PositionTicks
	}

	if kind == "stop" {
		h.auth.ReleaseLease(p.User.ID, body.PlaySessionID, p.Client.DeviceID, body.ItemID)
	} else if body.PositionTicks == nil || *body.PositionTicks >= 0 {
		_ = h.auth.AcquireOrRenewLease(p, body.PlaySessionID, body.ItemID, body.MediaSourceID, pos, body.IsPaused)
	}

	// Persist UserData progress.
	var ud model.UserData
	err := h.db.Gorm.Where("user_id = ? AND item_id = ?", p.User.ID, body.ItemID).First(&ud).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ud = model.UserData{UserID: p.User.ID, ItemID: body.ItemID}
	}
	now := time.Now().UTC()
	ud.LastPlayedAt = &now
	ud.UpdatedAt = now
	if kind == "start" && ud.PlayCount == 0 {
		ud.PlayCount = 1
	}
	if hasPos {
		ud.PositionTicks = pos
		ud.HideFromResume = false
		var runtime int64
		if body.RunTimeTicks != nil && *body.RunTimeTicks > 0 {
			runtime = *body.RunTimeTicks
		} else if item := h.getItemByID(body.ItemID); item != nil {
			runtime = item.RuntimeTicks
		}
		// Mark played when reaching >= 90% of runtime.
		if runtime > 0 && float64(pos)/float64(runtime) >= 0.90 {
			ud.Played = true
			ud.PositionTicks = 0
			if ud.PlayCount == 0 {
				ud.PlayCount = 1
			}
		}
	}
	_ = h.db.Gorm.Save(&ud).Error
	h.bumpUserDataRev(c.Request.Context(), p.User.ID)
	c.Status(http.StatusNoContent)
}

func (h *Handler) Capabilities(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// --- UserData, Played, Favorite, Resume ---

func (h *Handler) GetUserData(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkUserScope(c, p, c.Param("userId")) {
		return
	}
	uid := c.Param("userId")
	if uid == "" && p.User != nil {
		uid = p.User.ID
	}
	itemID := c.Param("itemId")
	var ud model.UserData
	_ = h.db.Gorm.Where("user_id = ? AND item_id = ?", uid, itemID).First(&ud).Error
	c.JSON(http.StatusOK, userDataResponse(itemID, &ud))
}

func (h *Handler) UpdateUserData(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkSelfUserWrite(c, p, c.Param("userId")) {
		return
	}
	itemID := c.Param("itemId")
	var body struct {
		PlaybackPositionTicks *int64 `json:"PlaybackPositionTicks"`
		Played                *bool  `json:"Played"`
		IsFavorite            *bool  `json:"IsFavorite"`
		PlayCount             *int   `json:"PlayCount"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonErr(c, http.StatusBadRequest, "invalid userdata payload")
		return
	}
	ud := h.loadOrInitUserData(p.User.ID, itemID)
	if body.PlaybackPositionTicks != nil {
		if *body.PlaybackPositionTicks < 0 {
			jsonErr(c, http.StatusBadRequest, "negative position is not allowed")
			return
		}
		ud.PositionTicks = *body.PlaybackPositionTicks
	}
	if body.Played != nil {
		ud.Played = *body.Played
		if ud.Played {
			ud.PositionTicks = 0
		}
	}
	if body.IsFavorite != nil {
		ud.IsFavorite = *body.IsFavorite
	}
	if body.PlayCount != nil {
		if *body.PlayCount < 0 {
			jsonErr(c, http.StatusBadRequest, "negative play count is not allowed")
			return
		}
		ud.PlayCount = *body.PlayCount
	}
	now := time.Now().UTC()
	ud.UpdatedAt = now
	_ = h.db.Gorm.Save(ud).Error
	h.bumpUserDataRev(c.Request.Context(), p.User.ID)
	c.JSON(http.StatusOK, userDataResponse(itemID, ud))
}

func (h *Handler) SetPlayed(played bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := getPrincipal(c)
		if !h.checkSelfUserWrite(c, p, c.Param("userId")) {
			return
		}
		itemID := c.Param("itemId")
		ud := h.loadOrInitUserData(p.User.ID, itemID)
		ud.Played = played
		if played {
			ud.PositionTicks = 0
			if ud.PlayCount == 0 {
				ud.PlayCount = 1
			}
			now := time.Now().UTC()
			ud.LastPlayedAt = &now
		}
		ud.UpdatedAt = time.Now().UTC()
		_ = h.db.Gorm.Save(ud).Error
		h.bumpUserDataRev(c.Request.Context(), p.User.ID)
		c.JSON(http.StatusOK, userDataResponse(itemID, ud))
	}
}

func (h *Handler) SetFavorite(fav bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := getPrincipal(c)
		if !h.checkSelfUserWrite(c, p, c.Param("userId")) {
			return
		}
		itemID := c.Param("itemId")
		ud := h.loadOrInitUserData(p.User.ID, itemID)
		ud.IsFavorite = fav
		ud.UpdatedAt = time.Now().UTC()
		_ = h.db.Gorm.Save(ud).Error
		h.bumpUserDataRev(c.Request.Context(), p.User.ID)
		c.JSON(http.StatusOK, userDataResponse(itemID, ud))
	}
}

func (h *Handler) HideFromResume(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkSelfUserWrite(c, p, c.Param("userId")) {
		return
	}
	itemID := c.Param("itemId")
	hide := true
	if raw := queryCI(c, "Hide"); raw != "" {
		hide = strings.EqualFold(raw, "true")
	} else {
		var body struct {
			Hide *bool `json:"Hide"`
		}
		if c.ShouldBindJSON(&body) == nil && body.Hide != nil {
			hide = *body.Hide
		}
	}
	ud := h.loadOrInitUserData(p.User.ID, itemID)
	ud.HideFromResume = hide
	ud.UpdatedAt = time.Now().UTC()
	_ = h.db.Gorm.Save(ud).Error
	h.bumpUserDataRev(c.Request.Context(), p.User.ID)
	c.JSON(http.StatusOK, userDataResponse(itemID, ud))
}

func (h *Handler) ClearResume(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || p.IsAPIKey || p.User == nil {
		jsonErr(c, http.StatusUnauthorized, "user token required")
		return
	}
	if itemID := c.Param("itemId"); itemID != "" {
		_ = h.db.Gorm.Model(&model.UserData{}).
			Where("user_id = ? AND item_id = ?", p.User.ID, itemID).
			Updates(map[string]any{"position_ticks": 0, "hide_from_resume": false}).Error
	} else {
		_ = h.db.Gorm.Model(&model.UserData{}).
			Where("user_id = ?", p.User.ID).
			Updates(map[string]any{"position_ticks": 0, "hide_from_resume": false}).Error
	}
	h.bumpUserDataRev(c.Request.Context(), p.User.ID)
	c.Status(http.StatusNoContent)
}

// --- Standard Emby Management Compat ---

func (h *Handler) ListUsersCompat(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || (!p.IsAPIKey && (p.User == nil || !p.User.IsAdmin)) {
		jsonErr(c, http.StatusForbidden, "admin required")
		return
	}
	var users []model.User
	_ = h.db.Gorm.Order("created_at ASC").Find(&users).Error
	out := make([]map[string]any, 0, len(users))
	for i := range users {
		out = append(out, h.userDTO(&users[i], h.auth.ResolvePolicy(&users[i])))
	}
	if strings.HasSuffix(strings.ToLower(c.Request.URL.Path), "/query") {
		c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out)})
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ListSessionsCompat(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || (!p.IsAPIKey && (p.User == nil || !p.User.IsAdmin)) {
		jsonErr(c, http.StatusForbidden, "admin required")
		return
	}
	var leases []model.PlaybackLease
	_ = h.db.Gorm.Where("expires_at >= ?", time.Now().UTC()).Find(&leases).Error
	out := make([]map[string]any, 0, len(leases))
	for _, l := range leases {
		out = append(out, map[string]any{
			"Id":               l.PlaySessionID,
			"UserId":           l.UserID,
			"DeviceId":         l.DeviceID,
			"DeviceName":       l.DeviceName,
			"Client":           l.Client,
			"LastActivityDate": l.UpdatedAt.UTC().Format(time.RFC3339),
			"PlayState": map[string]any{
				"PositionTicks": l.PositionTicks,
				"IsPaused":      l.IsPaused,
			},
		})
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ScheduledTasksCompat(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || (!p.IsAPIKey && (p.User == nil || !p.User.IsAdmin)) {
		jsonErr(c, http.StatusForbidden, "admin required")
		return
	}
	state := "Idle"
	if h.scanner.IsScanning() {
		state = "Running"
	}
	c.JSON(http.StatusOK, []gin.H{
		{
			"Name":        "Scan media library",
			"Key":         "RefreshLibrary",
			"Id":          "refresh-library",
			"State":       state,
			"Description": "Scans all media libraries for new and updated files.",
			"Category":    "Library",
		},
	})
}

func (h *Handler) RefreshLibraryCompat(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil || (!p.IsAPIKey && (p.User == nil || !p.User.IsAdmin)) {
		jsonErr(c, http.StatusForbidden, "admin required")
		return
	}
	if itemID := c.Param("itemId"); itemID != "" {
		// Check if it's a library ID or an item inside a library.
		var lib model.Library
		if err := h.db.Gorm.Where("id = ?", itemID).First(&lib).Error; err == nil {
			h.scanner.ScanLibraryAsync(lib.ID)
			c.Status(http.StatusNoContent)
			return
		}
		if item := h.getItemByID(itemID); item != nil {
			h.scanner.ScanLibraryAsync(item.LibraryID)
			c.Status(http.StatusNoContent)
			return
		}
		jsonErr(c, http.StatusNotFound, "item not found")
		return
	}
	h.scanner.ScanAllAsync()
	c.Status(http.StatusNoContent)
}

// --- Helpers ---

func (h *Handler) checkUserScope(c *gin.Context, p *auth.Principal, targetUserID string) bool {
	if targetUserID == "" || p == nil || p.IsAPIKey || (p.User != nil && p.User.IsAdmin) {
		return true
	}
	if p.User == nil || p.User.ID != targetUserID {
		jsonErr(c, http.StatusForbidden, "cannot access another user's scope")
		return false
	}
	return true
}

func (h *Handler) checkSelfUserWrite(c *gin.Context, p *auth.Principal, targetUserID string) bool {
	if p == nil || p.IsAPIKey || p.User == nil {
		jsonErr(c, http.StatusUnauthorized, "user token required")
		return false
	}
	if targetUserID != "" && p.User.ID != targetUserID {
		jsonErr(c, http.StatusForbidden, "can only modify your own user data")
		return false
	}
	return true
}

func (h *Handler) loadOrInitUserData(userID, itemID string) *model.UserData {
	var ud model.UserData
	if err := h.db.Gorm.Where("user_id = ? AND item_id = ?", userID, itemID).First(&ud).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return &model.UserData{UserID: userID, ItemID: itemID}
	}
	return &ud
}

func userDataResponse(itemID string, ud *model.UserData) map[string]any {
	m := map[string]any{
		"PlaybackPositionTicks": ud.PositionTicks,
		"PlayCount":             ud.PlayCount,
		"IsFavorite":            ud.IsFavorite,
		"Played":                ud.Played,
		"Key":                   itemID,
	}
	if ud.LastPlayedAt != nil {
		m["LastPlayedDate"] = ud.LastPlayedAt.UTC().Format(time.RFC3339)
	}
	return m
}

func queryCI(c *gin.Context, key string) string {
	if v := c.Query(key); v != "" {
		return v
	}
	for k, vals := range c.Request.URL.Query() {
		if strings.EqualFold(k, key) && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func queryIntCI(c *gin.Context, key string, fallback int) int {
	raw := queryCI(c, key)
	if raw == "" {
		return fallback
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return fallback
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitMulti(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func containsCI(slice []string, target string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}

func normalizeImageKind(raw string) string {
	switch strings.ToLower(raw) {
	case "primary":
		return "Primary"
	case "backdrop":
		return "Backdrop"
	case "thumb":
		return "Thumb"
	case "logo":
		return "Logo"
	case "banner":
		return "Banner"
	default:
		return raw
	}
}

func buildOrderClause(sortBy, sortOrder string) string {
	dir := "ASC"
	if strings.EqualFold(strings.TrimSpace(sortOrder), "Descending") {
		dir = "DESC"
	}
	fields := splitCSV(sortBy)
	if len(fields) == 0 {
		return "items.sort_name ASC, items.id ASC"
	}
	var clauses []string
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "sortname", "name":
			clauses = append(clauses, "items.sort_name "+dir)
		case "productionyear":
			clauses = append(clauses, "items.year "+dir)
		case "datecreated", "latestadded", "datelastcontentadded", "seriesdatelastcontentadded":
			clauses = append(clauses, "items.added_at "+dir)
		case "premieredate", "seriesdatelastreleased":
			clauses = append(clauses, "items.premiere_date "+dir)
		case "communityrating", "criticrating":
			clauses = append(clauses, "items.community_rating "+dir)
		case "indexnumber":
			clauses = append(clauses, "items.episode_num "+dir)
		case "parentindexnumber":
			clauses = append(clauses, "items.season_num "+dir)
		case "random":
			clauses = append(clauses, "items.random_key "+dir)
		}
	}
	if len(clauses) == 0 {
		return "items.sort_name ASC, items.id ASC"
	}
	clauses = append(clauses, "items.id ASC")
	return strings.Join(clauses, ", ")
}

func sanitizeFTSQuery(raw string) string {
	fields := strings.Fields(raw)
	var terms []string
	for _, f := range fields {
		cleaned := strings.ReplaceAll(f, `"`, "")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned != "" {
			terms = append(terms, `"`+cleaned+`"*`)
		}
	}
	return strings.Join(terms, " ")
}
