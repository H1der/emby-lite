package emby

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	hotCacheTTL     = 60 * time.Second
	dtoCacheVersion = "v5"
)

type jsonFlightCall struct {
	wg  sync.WaitGroup
	raw []byte
	err error
}

func (h *Handler) userDataRevKey(uid string) string {
	return "udrev:" + uid
}

func (h *Handler) getUserDataRev(ctx context.Context, uid string) string {
	if uid == "" || h.cache == nil {
		return "0"
	}
	if b, ok := h.cache.Get(ctx, h.userDataRevKey(uid)); ok && len(b) > 0 {
		return string(b)
	}
	return "0"
}

func (h *Handler) bumpUserDataRev(ctx context.Context, uid string) {
	if uid == "" || h.cache == nil {
		return
	}
	rev := fmt.Sprintf("%d", time.Now().UnixNano())
	_ = h.cache.Set(ctx, h.userDataRevKey(uid), []byte(rev), 24*time.Hour)
}

func visibleIDsHash(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	cp := make([]string, len(ids))
	copy(cp, ids)
	sort.Strings(cp)
	sum := sha256.Sum256([]byte(strings.Join(cp, ",")))
	return hex.EncodeToString(sum[:8])
}

func (h *Handler) writeCachedJSON(c *gin.Context, key string, ttl time.Duration, build func() (any, error)) {
	h.writeCachedJSONWithHook(c, key, ttl, nil, build)
}

func (h *Handler) writeCachedJSONWithHook(c *gin.Context, key string, ttl time.Duration, beforeWrite func(raw []byte), build func() (any, error)) {
	ctx := c.Request.Context()
	if h.cache != nil {
		if raw, ok := h.cache.Get(ctx, key); ok {
			if beforeWrite != nil {
				beforeWrite(raw)
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
			return
		}
	}

	h.cacheFlightMu.Lock()
	if h.cacheFlights == nil {
		h.cacheFlights = make(map[string]*jsonFlightCall)
	}
	if existing, ok := h.cacheFlights[key]; ok {
		h.cacheFlightMu.Unlock()
		existing.wg.Wait()
		if existing.err != nil {
			jsonErr(c, http.StatusInternalServerError, existing.err.Error())
			return
		}
		if beforeWrite != nil {
			beforeWrite(existing.raw)
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", existing.raw)
		return
	}
	call := &jsonFlightCall{}
	call.wg.Add(1)
	h.cacheFlights[key] = call
	h.cacheFlightMu.Unlock()

	defer func() {
		call.wg.Done()
		h.cacheFlightMu.Lock()
		delete(h.cacheFlights, key)
		h.cacheFlightMu.Unlock()
	}()

	payload, err := build()
	if err != nil {
		call.err = err
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		call.err = err
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	call.raw = raw
	if h.cache != nil {
		_ = h.cache.Set(ctx, key, raw, ttl)
	}
	if beforeWrite != nil {
		beforeWrite(raw)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func (h *Handler) buildUserViewsItems(p *auth.Principal, visibleIDs []string, includeHidden bool) ([]map[string]any, error) {
	if len(visibleIDs) == 0 {
		return []map[string]any{}, nil
	}
	var cfg UserConfiguration
	if p != nil && p.User != nil {
		cfg = h.loadUserConfig(p.User.ID)
	}
	myMediaExc := idSliceToSet(cfg.MyMediaExcludes)

	filteredIDs := visibleIDs
	if !includeHidden {
		filteredIDs = h.filterLibraryIDsForSection(p, visibleIDs, "mymedia")
	}

	var items []map[string]any
	if len(filteredIDs) > 0 {
		var libs []model.Library
		if err := h.db.Read().Where("id IN ?", filteredIDs).Order("sort_order ASC, name ASC").Find(&libs).Error; err != nil {
			return nil, err
		}
		items = h.libraryViewsDTOs(libs, p)
	} else {
		items = []map[string]any{}
	}

	var vlibs []model.VirtualLibrary
	if err := h.db.Read().Where("enabled = ?", true).Order("sort_order ASC, name ASC").Find(&vlibs).Error; err == nil {
		for i := range vlibs {
			if !includeHidden && myMediaExc[vlibs[i].ID] {
				continue
			}
			items = append(items, h.virtualLibraryViewDTO(&vlibs[i], p))
		}
	}

	sortViewsByOrderedViews(items, cfg.OrderedViews)
	return items, nil
}

func (h *Handler) buildLatestDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, limit int) []map[string]any {
	return h.buildLatestDTOsWithMask(r, p, visibleIDs, parentID, limit, nil, "", dtoFieldsBasic())
}

func (h *Handler) buildLatestDTOsWithOpts(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, limit int, includeTypes []string, isPlayed string, detail bool) []map[string]any {
	mask := dtoFieldsBasic()
	if detail {
		mask = dtoFieldsFull()
	}
	return h.buildLatestDTOsWithMask(r, p, visibleIDs, parentID, limit, includeTypes, isPlayed, mask)
}

func (h *Handler) buildLatestDTOsWithMask(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, limit int, includeTypes []string, isPlayed string, mask dtoFieldMask) []map[string]any {
	if len(visibleIDs) == 0 {
		return []map[string]any{}
	}
	effectiveIDs := visibleIDs
	if parentID == "" || parentID == "root" {
		effectiveIDs = h.filterLibraryIDsForSection(p, visibleIDs, "latest")
		if len(effectiveIDs) == 0 {
			return []map[string]any{}
		}
	}
	types := normalizeItemTypes(includeTypes)
	if len(types) == 0 {
		types = []string{"Movie", "Series"}
	}
	q := h.db.Read().Model(&model.Item{}).
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ? AND items.type IN ?", effectiveIDs, types)
	if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
		q = q.Where("items.library_id = ?", parentID)
	}
	if p != nil && p.User != nil {
		hidePlayed := isPlayed == "" && h.loadUserConfig(p.User.ID).HidePlayedInLatest
		if strings.EqualFold(isPlayed, "false") || hidePlayed {
			q = q.Where("NOT EXISTS (SELECT 1 FROM user_data ud WHERE ud.user_id = ? AND ud.item_id = items.id AND ud.played = ?)", p.User.ID, true)
		} else if strings.EqualFold(isPlayed, "true") {
			q = q.Where("EXISTS (SELECT 1 FROM user_data ud WHERE ud.user_id = ? AND ud.item_id = items.id AND ud.played = ?)", p.User.ID, true)
		}
	}
	var rows []model.Item
	_ = q.Order("items.added_at DESC, items.id ASC").Limit(limit).Find(&rows).Error
	return h.itemsToDTOsWithMask(r, p, rows, mask)
}

type resumeQueryOpts struct {
	ParentID     string
	IncludeTypes []string
	ExcludeTypes []string
	MediaTypes   []string
	StartIndex   int
	Limit        int
	Detail       bool
	Mask         dtoFieldMask
	HasMask      bool
}

func (h *Handler) buildResumeDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, limit int) []map[string]any {
	out, _ := h.buildResumeDTOsWithOpts(r, p, visibleIDs, resumeQueryOpts{Limit: limit})
	return out
}

func (h *Handler) buildResumeDTOsPaged(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, startIndex, limit int, detail bool) ([]map[string]any, int64) {
	return h.buildResumeDTOsWithOpts(r, p, visibleIDs, resumeQueryOpts{
		ParentID:   parentID,
		StartIndex: startIndex,
		Limit:      limit,
		Detail:     detail,
	})
}

func (h *Handler) buildResumeDTOsWithOpts(r *http.Request, p *auth.Principal, visibleIDs []string, opts resumeQueryOpts) ([]map[string]any, int64) {
	if p == nil || p.User == nil || len(visibleIDs) == 0 {
		return []map[string]any{}, 0
	}
	if len(opts.MediaTypes) > 0 && !containsCI(opts.MediaTypes, "Video") {
		return []map[string]any{}, 0
	}
	effectiveIDs := visibleIDs
	if opts.ParentID == "" || opts.ParentID == "root" {
		effectiveIDs = h.filterLibraryIDsForSection(p, visibleIDs, "resume")
		if len(effectiveIDs) == 0 {
			return []map[string]any{}, 0
		}
	}
	base := h.db.Read().Table("items").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", effectiveIDs).
		Where("user_data.user_id = ? AND user_data.position_ticks > 0 AND user_data.played = ? AND user_data.hide_from_resume = ?", p.User.ID, false, false)
	if opts.ParentID != "" && opts.ParentID != "root" && !strings.HasPrefix(opts.ParentID, "vlib-") {
		base = base.Where("(items.library_id = ? OR items.parent_id = ? OR items.series_id = ?)", opts.ParentID, opts.ParentID, opts.ParentID)
	}
	if len(opts.MediaTypes) > 0 {
		base = base.Where("items.type IN ?", []string{"Movie", "Episode"})
	}
	if inc := normalizeItemTypes(opts.IncludeTypes); len(inc) > 0 {
		base = base.Where("items.type IN ?", inc)
	}
	if exc := normalizeItemTypes(opts.ExcludeTypes); len(exc) > 0 {
		base = base.Where("items.type NOT IN ?", exc)
	}
	type resumeCandidate struct {
		ID             string
		Type           string
		SeriesID       string
		SeasonID       string
		SeasonSeriesID string
		PositionTicks  int64
		RuntimeTicks   int64
	}
	var candidates []resumeCandidate
	_ = base.Session(&gorm.Session{}).
		Select("items.id, items.type, items.series_id, items.season_id, COALESCE(season_parent.series_id, '') AS season_series_id, user_data.position_ticks, items.runtime_ticks").
		Joins("LEFT JOIN items AS season_parent ON season_parent.id = items.season_id AND items.type = 'Episode' AND items.series_id = ''").
		Order("COALESCE(user_data.last_played_at, user_data.updated_at) DESC, user_data.updated_at DESC, items.season_num DESC, items.episode_num DESC, items.id ASC").
		Scan(&candidates).Error

	thresholds := h.resumeThresholds()

	// Filter out entries outside Resume thresholds (e.g. 1s/2s opens, <5% progress, short clips, >=90%)
	// and keep at most one resumable episode per series (most recently watched, breaking ties by latest season/episode).
	seenSeries := make(map[string]bool, len(candidates))
	winnerIDs := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		if !isPositionResumable(cand.PositionTicks, cand.RuntimeTicks, thresholds) {
			continue
		}
		if cand.Type == "Episode" {
			seriesKey := cand.SeriesID
			if seriesKey == "" {
				seriesKey = cand.SeasonSeriesID
			}
			if seriesKey == "" && cand.SeasonID != "" {
				seriesKey = "season:" + cand.SeasonID
			}
			if seriesKey != "" {
				if seenSeries[seriesKey] {
					continue
				}
				seenSeries[seriesKey] = true
			}
		}
		winnerIDs = append(winnerIDs, cand.ID)
	}

	total := int64(len(winnerIDs))
	if opts.StartIndex >= len(winnerIDs) || opts.Limit <= 0 {
		return []map[string]any{}, total
	}
	end := opts.StartIndex + opts.Limit
	if end > len(winnerIDs) {
		end = len(winnerIDs)
	}
	pageIDs := winnerIDs[opts.StartIndex:end]

	var fetched []model.Item
	_ = h.db.Read().Where("id IN ?", pageIDs).Find(&fetched).Error
	byID := make(map[string]model.Item, len(fetched))
	for _, it := range fetched {
		byID[it.ID] = it
	}
	rows := make([]model.Item, 0, len(pageIDs))
	for _, id := range pageIDs {
		if it, ok := byID[id]; ok {
			rows = append(rows, it)
		}
	}

	var dtos []map[string]any
	if opts.HasMask {
		dtos = h.itemsToDTOsWithMask(r, p, rows, opts.Mask)
	} else {
		dtos = h.itemsToDTOs(r, p, rows, opts.Detail)
	}
	attachResumeSeriesArtwork(dtos)
	return dtos, total
}

func (h *Handler) buildNextUpDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, limit int) []map[string]any {
	out, _ := h.buildNextUpDTOsPagedMask(r, p, visibleIDs, "", "", 0, limit, dtoFieldsBasic())
	return out
}

func (h *Handler) buildNextUpDTOsPaged(r *http.Request, p *auth.Principal, visibleIDs []string, parentID, seriesID string, startIndex, limit int, detail bool) ([]map[string]any, int) {
	mask := dtoFieldsBasic()
	if detail {
		mask = dtoFieldsFull()
	}
	return h.buildNextUpDTOsPagedMask(r, p, visibleIDs, parentID, seriesID, startIndex, limit, mask)
}

func (h *Handler) buildNextUpDTOsPagedMask(r *http.Request, p *auth.Principal, visibleIDs []string, parentID, seriesID string, startIndex, limit int, mask dtoFieldMask) ([]map[string]any, int) {
	if p == nil || p.User == nil || len(visibleIDs) == 0 {
		return []map[string]any{}, 0
	}
	effectiveIDs := visibleIDs
	if (parentID == "" || parentID == "root") && seriesID == "" {
		effectiveIDs = h.filterLibraryIDsForSection(p, visibleIDs, "nextup")
		if len(effectiveIDs) == 0 {
			return []map[string]any{}, 0
		}
	}
	type playedEpRow struct {
		SeriesID   string
		SeasonNum  int
		EpisodeNum int
	}
	q := h.db.Read().Table("items").
		Select("items.series_id, items.season_num, items.episode_num").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.type = ? AND items.library_id IN ?", "Episode", effectiveIDs).
		Where("user_data.user_id = ? AND user_data.played = ?", p.User.ID, true)
	if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
		q = q.Where("items.library_id = ?", parentID)
	}
	if seriesID != "" {
		q = q.Where("items.series_id = ?", seriesID)
	}
	var playedEps []playedEpRow
	_ = q.Order("COALESCE(user_data.last_played_at, user_data.updated_at) DESC, user_data.updated_at DESC").Limit(200).Scan(&playedEps).Error

	// Deduplicate to the most recently played episode per series, preserving watch recency order.
	type seriesCursor struct {
		seriesID   string
		seasonNum  int
		episodeNum int
	}
	seenSeries := make(map[string]bool, len(playedEps))
	orderedSeries := make([]seriesCursor, 0, len(playedEps))
	seriesIDs := make([]string, 0, len(playedEps))
	for _, ep := range playedEps {
		if ep.SeriesID == "" || seenSeries[ep.SeriesID] {
			continue
		}
		seenSeries[ep.SeriesID] = true
		orderedSeries = append(orderedSeries, seriesCursor{
			seriesID:   ep.SeriesID,
			seasonNum:  ep.SeasonNum,
			episodeNum: ep.EpisodeNum,
		})
		seriesIDs = append(seriesIDs, ep.SeriesID)
	}
	if len(seriesIDs) == 0 {
		return []map[string]any{}, 0
	}

	// Single batched query for all candidate unplayed episodes across those series (eliminates N+1 loop).
	var candidates []model.Item
	_ = h.db.Read().Model(&model.Item{}).
		Where("items.series_id IN ? AND items.type = ? AND items.soft_deleted_at IS NULL", seriesIDs, "Episode").
		Where("NOT EXISTS (SELECT 1 FROM user_data ud WHERE ud.user_id = ? AND ud.item_id = items.id AND ud.played = ?)", p.User.ID, true).
		Order("items.series_id ASC, items.season_num ASC, items.episode_num ASC, items.id ASC").
		Find(&candidates).Error

	candsBySeries := make(map[string][]model.Item, len(seriesIDs))
	for _, c := range candidates {
		candsBySeries[c.SeriesID] = append(candsBySeries[c.SeriesID], c)
	}

	allNext := make([]model.Item, 0, len(orderedSeries))
	for _, sc := range orderedSeries {
		for _, cand := range candsBySeries[sc.seriesID] {
			if cand.SeasonNum > sc.seasonNum || (cand.SeasonNum == sc.seasonNum && cand.EpisodeNum > sc.episodeNum) {
				allNext = append(allNext, cand)
				break
			}
		}
	}
	total := len(allNext)
	if startIndex >= total {
		return []map[string]any{}, total
	}
	end := startIndex + limit
	if end > total {
		end = total
	}
	return h.itemsToDTOsWithMask(r, p, allNext[startIndex:end], mask), total
}

func (h *Handler) buildCounts(visibleIDs []string) map[string]int64 {
	counts := map[string]int64{
		"MovieCount":      0,
		"SeriesCount":     0,
		"EpisodeCount":    0,
		"GameCount":       0,
		"ArtistCount":     0,
		"ProgramCount":    0,
		"GameSystemCount": 0,
		"TrailerCount":    0,
		"SongCount":       0,
		"AlbumCount":      0,
		"MusicVideoCount": 0,
		"BoxSetCount":     0,
		"BookCount":       0,
		"ItemCount":       0,
	}
	if len(visibleIDs) == 0 {
		return counts
	}
	type cntRow struct {
		Type string
		Cnt  int64
	}
	var rows []cntRow
	_ = h.db.Read().Table("items").
		Select("type, COUNT(*) AS cnt").
		Where("library_id IN ? AND soft_deleted_at IS NULL AND type IN ?", visibleIDs, []string{"Movie", "Series", "Episode", "BoxSet"}).
		Group("type").
		Scan(&rows).Error
	var total int64
	for _, r := range rows {
		switch r.Type {
		case "Movie":
			counts["MovieCount"] = r.Cnt
			total += r.Cnt
		case "Series":
			counts["SeriesCount"] = r.Cnt
			total += r.Cnt
		case "Episode":
			counts["EpisodeCount"] = r.Cnt
			total += r.Cnt
		case "BoxSet":
			counts["BoxSetCount"] = r.Cnt
			total += r.Cnt
		}
	}
	counts["ItemCount"] = total
	return counts
}

// WebHome is a single-round-trip aggregate endpoint for the Web SPA homepage.
// It reuses the exact same Emby DTO schemas and CacheStore invalidation keys.
func (h *Handler) WebHome(c *gin.Context) {
	p := getPrincipal(c)
	visibleIDs, err := h.auth.VisibleLibraryIDs(p)
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	uid := "anon"
	if p != nil && p.User != nil {
		uid = p.User.ID
	}
	libRev := h.db.GetLibraryRevision()
	userRev := h.getUserDataRev(c.Request.Context(), uid)
	visHash := visibleIDsHash(visibleIDs)
	cacheKey := fmt.Sprintf("%s:webhome:%s:%s:%s:%s", dtoCacheVersion, libRev, userRev, uid, visHash)

	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		views, err := h.buildUserViewsItems(p, visibleIDs, false)
		if err != nil {
			return nil, err
		}
		resume := h.buildResumeDTOs(c.Request, p, visibleIDs, 12)
		nextUp := h.buildNextUpDTOs(c.Request, p, visibleIDs, 12)
		latest := h.buildLatestDTOs(c.Request, p, visibleIDs, "", 24)
		counts := h.buildCounts(visibleIDs)

		// Pick up to 6 featured items (preferring items with Backdrop + Overview/Rating) for Hero banner.
		featured := make([]map[string]any, 0, 6)
		for _, it := range latest {
			bgs, _ := it["BackdropImageTags"].([]string)
			if len(bgs) > 0 {
				featured = append(featured, it)
				if len(featured) >= 6 {
					break
				}
			}
		}
		if len(featured) == 0 && len(latest) > 0 {
			maxN := 5
			if len(latest) < maxN {
				maxN = len(latest)
			}
			featured = append(featured, latest[:maxN]...)
		}

		return gin.H{
			"views":    views,
			"resume":   resume,
			"next_up":  nextUp,
			"latest":   latest,
			"featured": featured,
			"counts":   counts,
		}, nil
	})
}
