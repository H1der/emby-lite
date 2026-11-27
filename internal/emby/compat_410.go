package emby

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/model"
	"emby-lite/internal/playback"

	"github.com/gin-gonic/gin"
)

// --- Emby 4.10.2.0 Home Sections & Section Feeds ---

// GetHomeSections implements Emby 4.10 GET /Users/:userId/HomeSections.
func (h *Handler) GetHomeSections(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkUserScope(c, p, c.Param("userId")) {
		return
	}
	sections := []map[string]any{
		{
			"Id":               "smalllibrarytiles",
			"Name":             "My Media",
			"Type":             "smalllibrarytiles",
			"EnableInMainMenu": true,
			"EnableInUserMenu": true,
		},
		{
			"Id":               "resume",
			"Name":             "Continue Watching",
			"Type":             "resume",
			"EnableInMainMenu": true,
			"EnableInUserMenu": true,
		},
		{
			"Id":               "nextup",
			"Name":             "Next Up",
			"Type":             "nextup",
			"EnableInMainMenu": true,
			"EnableInUserMenu": true,
		},
		{
			"Id":               "latestmedia",
			"Name":             "Latest Media",
			"Type":             "latestmedia",
			"EnableInMainMenu": true,
			"EnableInUserMenu": true,
		},
	}
	c.JSON(http.StatusOK, sections)
}

// GetSectionItems implements Emby 4.10 GET /Users/:userId/Sections/:sectionName/Items.
func (h *Handler) GetSectionItems(c *gin.Context) {
	p := getPrincipal(c)
	if !h.checkUserScope(c, p, c.Param("userId")) {
		return
	}
	section := strings.ToLower(strings.TrimSpace(c.Param("sectionName")))
	switch section {
	case "nextup":
		h.GetNextUp(c)
	case "resume", "continuewatching", "resumeaudio":
		h.GetResume(c)
	case "myviews", "smalllibrarytiles", "librarytiles", "views":
		h.UserViews(c)
	case "latest", "latestmedia":
		visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
		parentID := queryCI(c, "ParentId")
		limit := queryIntCI(c, "Limit", 20)
		if limit <= 0 || limit > 100 {
			limit = 20
		}
		startIndex := queryIntCI(c, "StartIndex", 0)
		if startIndex < 0 {
			startIndex = 0
		}
		all := h.buildLatestDTOsWithOpts(c.Request, p, visibleIDs, parentID, limit+startIndex, splitCSV(queryCI(c, "IncludeItemTypes")), queryCI(c, "IsPlayed"), fieldsNeedDetail(queryCI(c, "Fields")))
		total := len(all)
		if startIndex >= len(all) {
			all = []map[string]any{}
		} else {
			end := startIndex + limit
			if end > len(all) {
				end = len(all)
			}
			all = all[startIndex:end]
		}
		c.JSON(http.StatusOK, gin.H{"Items": all, "TotalRecordCount": total, "StartIndex": startIndex})
	case "favorites":
		visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
		if p == nil || p.User == nil || len(visibleIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		limit := queryIntCI(c, "Limit", 20)
		if limit <= 0 || limit > 100 {
			limit = 20
		}
		startIndex := queryIntCI(c, "StartIndex", 0)
		if startIndex < 0 {
			startIndex = 0
		}
		q := h.db.Read().Model(&model.Item{}).
			Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", visibleIDs).
			Where("items.id IN (SELECT item_id FROM user_data WHERE user_id = ? AND is_favorite = ?)", p.User.ID, true)
		var total int64
		_ = q.Count(&total).Error
		var rows []model.Item
		_ = q.Order("items.sort_name ASC, items.id ASC").Offset(startIndex).Limit(limit).Find(&rows).Error
		out := h.itemsToDTOs(c.Request, p, rows, fieldsNeedDetail(queryCI(c, "Fields")))
		c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": total, "StartIndex": startIndex})
	default:
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
	}
}

// --- MediaSegments, Search Hints, Studios, Images List, User Avatar ---

// GetMediaSegments implements GET /MediaSegments/:itemId (intro/outro/credits skip segments).
func (h *Handler) GetMediaSegments(c *gin.Context) {
	p := getPrincipal(c)
	item := h.getItemByID(c.Param("itemId"))
	if item == nil || !h.auth.CanAccessLibrary(p, item.LibraryID) {
		c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}

	segments := []map[string]any{}
	var mk model.IntroMarker
	err := h.db.Read().Where("item_id = ?", item.ID).First(&mk).Error
	if err != nil && item.SeriesID != "" {
		err = h.db.Read().Where("series_id = ? AND season_num = ? AND item_id = ''", item.SeriesID, item.SeasonNum).First(&mk).Error
	}
	if err == nil {
		if mk.IntroEndTicks > mk.IntroStartTicks {
			segments = append(segments, map[string]any{
				"Id":         item.ID + "-intro",
				"ItemId":     item.ID,
				"Type":       "Intro",
				"StartTicks": mk.IntroStartTicks,
				"EndTicks":   mk.IntroEndTicks,
			})
		}
		if mk.CreditsStartTicks > 0 {
			endTicks := item.RuntimeTicks
			if endTicks < mk.CreditsStartTicks {
				endTicks = mk.CreditsStartTicks
			}
			segments = append(segments, map[string]any{
				"Id":         item.ID + "-outro",
				"ItemId":     item.ID,
				"Type":       "Outro",
				"StartTicks": mk.CreditsStartTicks,
				"EndTicks":   endTicks,
			})
		}
	} else {
		var chapters []model.ItemChapter
		_ = h.db.Read().Where("item_id = ?", item.ID).Order("index_num ASC").Find(&chapters).Error
		var introStart int64 = -1
		for _, ch := range chapters {
			switch ch.MarkerType {
			case 1:
				introStart = ch.StartTicks
			case 2:
				if introStart >= 0 && ch.StartTicks > introStart {
					segments = append(segments, map[string]any{
						"Id":         item.ID + "-intro",
						"ItemId":     item.ID,
						"Type":       "Intro",
						"StartTicks": introStart,
						"EndTicks":   ch.StartTicks,
					})
					introStart = -1
				}
			case 3:
				endTicks := item.RuntimeTicks
				if endTicks < ch.StartTicks {
					endTicks = ch.StartTicks
				}
				segments = append(segments, map[string]any{
					"Id":         item.ID + "-outro",
					"ItemId":     item.ID,
					"Type":       "Outro",
					"StartTicks": ch.StartTicks,
					"EndTicks":   endTicks,
				})
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"Items": segments, "TotalRecordCount": len(segments), "StartIndex": 0})
}

// SearchHints implements Emby GET /Search/Hints.
func (h *Handler) SearchHints(c *gin.Context) {
	p := getPrincipal(c)
	if uidParam := queryCI(c, "UserId"); uidParam != "" && !h.checkUserScope(c, p, uidParam) {
		return
	}
	visibleIDs, err := h.auth.VisibleLibraryIDs(p)
	if err != nil || len(visibleIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"SearchHints": []any{}, "TotalRecordCount": 0})
		return
	}
	term := strings.TrimSpace(queryCI(c, "SearchTerm"))
	if term == "" {
		c.JSON(http.StatusOK, gin.H{"SearchHints": []any{}, "TotalRecordCount": 0})
		return
	}
	limit := queryIntCI(c, "Limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	includeTypes := normalizeItemTypes(splitCSV(queryCI(c, "IncludeItemTypes")))
	q := h.db.Read().Model(&model.Item{}).
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", visibleIDs)
	if len(includeTypes) > 0 {
		q = q.Where("items.type IN ?", includeTypes)
	} else {
		q = q.Where("items.type IN ?", []string{"Movie", "Series", "Episode", "BoxSet"})
	}
	like := "%" + strings.ToLower(term) + "%"
	switch {
	case h.db.Dialect == "postgres" && h.db.HasTrgm:
		q = q.Where("(items.name % ? OR items.original_name % ? OR items.name ILIKE ? OR items.original_name ILIKE ? OR items.pinyin_initials ILIKE ?)",
			term, term, like, like, like)
	case h.db.Dialect == "sqlite" && h.db.HasFTS5:
		ftsQuery := sanitizeFTSQuery(term)
		if ftsQuery != "" {
			q = q.Where("(items.id IN (SELECT item_id FROM items_fts WHERE items_fts MATCH ?) OR LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)",
				ftsQuery, like, like, like)
		} else {
			q = q.Where("(LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)", like, like, like)
		}
	default:
		q = q.Where("(LOWER(items.name) LIKE ? OR LOWER(items.original_name) LIKE ? OR items.pinyin_initials LIKE ?)", like, like, like)
	}

	var total int64
	_ = q.Count(&total).Error
	var rows []model.Item
	_ = q.Order("items.sort_name ASC, items.id ASC").Limit(limit).Find(&rows).Error
	dtos := h.itemsToDTOs(c.Request, p, rows, false)
	hints := make([]map[string]any, 0, len(dtos))
	for _, d := range dtos {
		hint := map[string]any{
			"ItemId": d["Id"],
			"Id":     d["Id"],
			"Name":   d["Name"],
			"Type":   d["Type"],
		}
		if v, ok := d["ProductionYear"]; ok {
			hint["ProductionYear"] = v
		}
		if v, ok := d["RunTimeTicks"]; ok {
			hint["RunTimeTicks"] = v
		}
		if v, ok := d["MediaType"]; ok {
			hint["MediaType"] = v
		}
		if v, ok := d["SeriesName"]; ok {
			hint["Series"] = v
		}
		if v, ok := d["IndexNumber"]; ok {
			hint["IndexNumber"] = v
		}
		if v, ok := d["ParentIndexNumber"]; ok {
			hint["ParentIndexNumber"] = v
		}
		if tags, ok := d["ImageTags"].(map[string]string); ok {
			if pt := tags["Primary"]; pt != "" {
				hint["PrimaryImageTag"] = pt
			}
		}
		if bgs, ok := d["BackdropImageTags"].([]string); ok && len(bgs) > 0 {
			hint["BackdropImageTag"] = bgs[0]
			hint["BackdropImageItemId"] = d["Id"]
		}
		hints = append(hints, hint)
	}
	c.JSON(http.StatusOK, gin.H{"SearchHints": hints, "TotalRecordCount": total})
}

// GetStudios implements Emby GET /Studios.
func (h *Handler) GetStudios(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, _ := h.auth.VisibleLibraryIDs(p)
	parentID := queryCI(c, "ParentId")
	cacheKey := dtoCacheVersion + ":studios:" + h.db.GetLibraryRevision() + ":" + visibleIDsHash(visibleIDs) + ":" + parentID
	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		names := h.collectStudioNames(visibleIDs, parentID)
		sid := h.serverID()
		items := make([]map[string]any, 0, len(names))
		for _, n := range names {
			items = append(items, map[string]any{
				"Name":      n,
				"Id":        strconv.FormatInt(stableNumericID("studio", n), 10),
				"ServerId":  sid,
				"Type":      "Studio",
				"IsFolder":  false,
				"ImageTags": map[string]string{},
			})
		}
		return gin.H{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0}, nil
	})
}

func (h *Handler) collectStudioNames(visibleIDs []string, parentID string) []string {
	if len(visibleIDs) == 0 {
		return []string{}
	}
	q := h.db.Read().Model(&model.Item{}).
		Select("studios_json").
		Where("library_id IN ? AND soft_deleted_at IS NULL AND studios_json <> '' AND studios_json <> '[]'", visibleIDs)
	if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
		q = q.Where("library_id = ?", parentID)
	}
	var rawList []string
	_ = q.Pluck("studios_json", &rawList).Error
	seen := make(map[string]string)
	for _, raw := range rawList {
		var studios []string
		if json.Unmarshal([]byte(raw), &studios) != nil {
			continue
		}
		for _, s := range studios {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			key := strings.ToLower(s)
			if _, ok := seen[key]; !ok {
				seen[key] = s
			}
		}
	}
	names := make([]string, 0, len(seen))
	for _, s := range seen {
		names = append(names, s)
	}
	sort.Strings(names)
	return names
}

// GetItemImagesList implements GET /Items/:itemId/Images returning Emby ImageInfo[].
func (h *Handler) GetItemImagesList(c *gin.Context) {
	p := getPrincipal(c)
	itemID := c.Param("itemId")
	item := h.getItemByID(itemID)
	if item == nil || !h.auth.CanAccessLibrary(p, item.LibraryID) {
		c.JSON(http.StatusOK, []any{})
		return
	}
	var imgs []model.ItemImage
	_ = h.db.Read().Where("item_id = ?", itemID).Order("kind ASC, index_num ASC").Find(&imgs).Error
	out := make([]map[string]any, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, map[string]any{
			"ImageType":  img.Kind,
			"ImageIndex": img.IndexNum,
			"ImageTag":   h.signTag(p, itemID, img.Kind, img.Tag),
			"Width":      img.Width,
			"Height":     img.Height,
		})
	}
	c.JSON(http.StatusOK, out)
}

// GetUserImage implements GET/HEAD /Users/:userId/Images/:type.
func (h *Handler) GetUserImage(c *gin.Context) {
	uid := c.Param("userId")
	var av model.UserAvatar
	if err := h.db.Read().Where("user_id = ?", uid).First(&av).Error; err != nil || len(av.Data) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	etag := `"` + av.Revision + `"`
	if av.Revision != "" && c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	if av.Revision != "" {
		c.Header("ETag", etag)
	}
	c.Header("Cache-Control", "private, max-age=86400")
	c.Data(http.StatusOK, av.Mime, av.Data)
}

// --- Standard Emby Public / Compatibility Endpoints ---

func (h *Handler) SaveDisplayPreferences(c *gin.Context) {
	if p, err := h.auth.AuthenticateRequest(c.Request); err == nil && p != nil && p.User != nil {
		dpID := c.Param("id")
		uid := queryCI(c, "userId")
		if uid == "" || (!p.User.IsAdmin && uid != p.User.ID) {
			uid = p.User.ID
		}
		if raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			var payload map[string]any
			if json.Unmarshal(raw, &payload) == nil {
				if encoded, err := json.Marshal(payload); err == nil {
					_ = h.db.Gorm.Save(&model.UserSetting{
						UserID:    uid,
						Key:       "dp:" + strings.ToLower(dpID),
						Value:     string(encoded),
						UpdatedAt: time.Now().UTC(),
					}).Error
				}
			}
		}
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

func (h *Handler) EmptyItemsResult(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
}

func (h *Handler) GetThemeMedia(c *gin.Context) {
	empty := gin.H{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0, "OwnerId": c.Param("itemId")}
	c.JSON(http.StatusOK, gin.H{
		"ThemeVideosResult": empty,
		"ThemeSongsResult":  empty,
		"SoundtrackSongsResult": empty,
	})
}

func (h *Handler) BrandingConfiguration(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"LoginDisclaimer": "",
		"CustomCss":       "",
	})
}

func (h *Handler) BrandingCSS(c *gin.Context) {
	c.Data(http.StatusOK, "text/css; charset=utf-8", []byte(""))
}

func (h *Handler) LocalizationOptions(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{
		{"Name": "Chinese Simplified", "Value": "zh-cn"},
		{"Name": "English (United States)", "Value": "en-us"},
	})
}

func (h *Handler) LocalizationCultures(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{
		{"Name": "Chinese", "DisplayName": "中文", "TwoLetterISOLanguageName": "zh", "ThreeLetterISOLanguageName": "zho", "ThreeLetterISOLanguageNames": []string{"zho", "chi"}},
		{"Name": "English", "DisplayName": "English", "TwoLetterISOLanguageName": "en", "ThreeLetterISOLanguageName": "eng", "ThreeLetterISOLanguageNames": []string{"eng"}},
		{"Name": "Japanese", "DisplayName": "日本語", "TwoLetterISOLanguageName": "ja", "ThreeLetterISOLanguageName": "jpn", "ThreeLetterISOLanguageNames": []string{"jpn"}},
	})
}

func (h *Handler) LocalizationCountries(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{
		{"Name": "China", "DisplayName": "China", "TwoLetterISORegionName": "CN", "ThreeLetterISORegionName": "CHN"},
		{"Name": "United States", "DisplayName": "United States", "TwoLetterISORegionName": "US", "ThreeLetterISORegionName": "USA"},
	})
}

func (h *Handler) QuickConnectEnabled(c *gin.Context) {
	c.JSON(http.StatusOK, false)
}

func (h *Handler) SystemConfigurationPublic(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ServerName":             h.serverName(),
		"UICulture":              "zh-cn",
		"StartupWizardCompleted": true,
	})
}

func (h *Handler) SystemConfiguration(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ServerName":             h.serverName(),
		"UICulture":              "zh-cn",
		"StartupWizardCompleted": true,
		"EnableHttps":            strings.HasPrefix(playback.PublicOrigin(c.Request, h.cfg.Server.PublicURL), "https://"),
	})
}

func (h *Handler) WebManifest(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":       h.serverName(),
		"short_name": "Emby",
		"start_url":  "index.html",
		"display":    "standalone",
		"icons":      []any{},
	})
}

// --- Pure-stdlib RFC 6455 Emby WebSocket Keepalive Handler ---

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// EmbyWebSocket accepts GET/HEAD /embywebsocket so official and third-party Emby 4.10 clients
// can establish their expected keepalive socket without receiving 404 or SPA HTML.
func (h *Handler) EmbyWebSocket(c *gin.Context) {
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusNoContent)
		return
	}
	if !strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		c.Status(http.StatusUpgradeRequired)
		return
	}
	key := strings.TrimSpace(c.GetHeader("Sec-WebSocket-Key"))
	if key == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	hijacker, ok := c.Writer.(http.Hijacker)
	if !ok {
		c.Status(http.StatusNotImplemented)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])

	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}

	// Send initial ForceKeepAlive message per Emby WebSocket protocol.
	_ = writeWSFrame(conn, 0x1, []byte(`{"MessageType":"ForceKeepAlive","Data":60}`))

	reader := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		opcode, payload, err := readWSFrame(reader)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8: // Close
			_ = writeWSFrame(conn, 0x8, nil)
			return
		case 0x9: // Ping -> Pong
			_ = writeWSFrame(conn, 0xA, payload)
		case 0x1: // Text (e.g. KeepAlive)
			if strings.Contains(string(payload), "KeepAlive") {
				_ = writeWSFrame(conn, 0x1, []byte(`{"MessageType":"KeepAlive"}`))
			}
		}
	}
}

func readWSFrame(r *bufio.Reader) (byte, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0F
	masked := (hdr[1] & 0x80) != 0
	length := uint64(hdr[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	if length > 1<<20 {
		return 0, nil, io.ErrShortBuffer
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func writeWSFrame(conn net.Conn, opcode byte, payload []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	var hdr []byte
	hdr = append(hdr, 0x80|opcode)
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 65535:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		hdr = append(hdr, 127)
		hdr = append(hdr, b[:]...)
	}
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := conn.Write(payload)
		return err
	}
	return nil
}

// --- Helpers for Emby 4.10 SystemInfo & Query Normalization ---

func originPorts(origin string) (httpPort, httpsPort int, isHTTPS bool) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return 8096, 0, false
	}
	isHTTPS = strings.EqualFold(parsed.Scheme, "https")
	port := 80
	if isHTTPS {
		port = 443
	}
	if p := parsed.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			port = n
		}
	}
	if isHTTPS {
		return 0, port, true
	}
	return port, 0, false
}

func normalizeItemTypes(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	canonical := []string{
		"Movie", "Series", "Season", "Episode", "Folder",
		"CollectionFolder", "UserView", "BoxSet", "Person",
		"Genre", "Tag", "Studio", "Video", "MusicVideo",
		"Audio", "MusicAlbum", "MusicArtist", "Playlist", "Photo",
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		matched := t
		for _, c := range canonical {
			if strings.EqualFold(t, c) {
				matched = c
				break
			}
		}
		out = append(out, matched)
	}
	return out
}

func fieldsNeedDetail(fieldsRaw string) bool {
	if fieldsRaw == "" {
		return false
	}
	f := strings.ToLower(fieldsRaw)
	for _, kw := range []string{"mediasources", "mediastreams", "people", "studios", "tags", "chapters", "taglines", "childcount", "recursiveitemcount", "container", "path", "itemcounts"} {
		if strings.Contains(f, kw) {
			return true
		}
	}
	return false
}

// flexInt64 accepts JSON integers, floats (e.g. 12345.0), or quoted numeric strings ("12345").
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		return nil
	}
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		s = strings.Trim(s, `"`)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt64(n)
		return nil
	}
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt64(fl)
		return nil
	}
	return nil
}
