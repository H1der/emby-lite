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
	"time"

	"emby-lite/internal/auth"
	"emby-lite/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const hotCacheTTL = 60 * time.Second

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
	ctx := c.Request.Context()
	if h.cache != nil {
		if raw, ok := h.cache.Get(ctx, key); ok {
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
			return
		}
	}
	payload, err := build()
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		jsonErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	if h.cache != nil {
		_ = h.cache.Set(ctx, key, raw, ttl)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func (h *Handler) buildUserViewsItems(p *auth.Principal, visibleIDs []string) ([]map[string]any, error) {
	if len(visibleIDs) == 0 {
		return []map[string]any{}, nil
	}
	var libs []model.Library
	if err := h.db.Read().Where("id IN ?", visibleIDs).Order("sort_order ASC, name ASC").Find(&libs).Error; err != nil {
		return nil, err
	}
	items := h.libraryViewsDTOs(libs, p)

	var vlibs []model.VirtualLibrary
	if err := h.db.Read().Where("enabled = ?", true).Order("sort_order ASC, name ASC").Find(&vlibs).Error; err == nil {
		for i := range vlibs {
			items = append(items, h.virtualLibraryViewDTO(&vlibs[i], p))
		}
	}
	return items, nil
}

func (h *Handler) buildLatestDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, limit int) []map[string]any {
	return h.buildLatestDTOsWithOpts(r, p, visibleIDs, parentID, limit, nil, "", false)
}

func (h *Handler) buildLatestDTOsWithOpts(r *http.Request, p *auth.Principal, visibleIDs []string, parentID string, limit int, includeTypes []string, isPlayed string, detail bool) []map[string]any {
	if len(visibleIDs) == 0 {
		return []map[string]any{}
	}
	types := normalizeItemTypes(includeTypes)
	if len(types) == 0 {
		types = []string{"Movie", "Series"}
	}
	q := h.db.Read().Model(&model.Item{}).
		Where("soft_deleted_at IS NULL AND library_id IN ? AND type IN ?", visibleIDs, types)
	if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
		q = q.Where("library_id = ?", parentID)
	}
	if p != nil && p.User != nil {
		if strings.EqualFold(isPlayed, "false") {
			q = q.Where("id NOT IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", p.User.ID, true)
		} else if strings.EqualFold(isPlayed, "true") {
			q = q.Where("id IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", p.User.ID, true)
		}
	}
	var rows []model.Item
	_ = q.Order("added_at DESC, id ASC").Limit(limit).Find(&rows).Error
	return h.itemsToDTOs(r, p, rows, detail)
}

type resumeQueryOpts struct {
	ParentID     string
	IncludeTypes []string
	ExcludeTypes []string
	MediaTypes   []string
	StartIndex   int
	Limit        int
	Detail       bool
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
	base := h.db.Read().Table("items").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", visibleIDs).
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
	}
	var candidates []resumeCandidate
	_ = base.Session(&gorm.Session{}).
		Select("items.id, items.type, items.series_id, items.season_id, COALESCE(season_parent.series_id, '') AS season_series_id").
		Joins("LEFT JOIN items AS season_parent ON season_parent.id = items.season_id AND items.type = 'Episode' AND items.series_id = ''").
		Order("COALESCE(user_data.last_played_at, user_data.updated_at) DESC, user_data.updated_at DESC, items.season_num DESC, items.episode_num DESC, items.id ASC").
		Scan(&candidates).Error

	// Keep at most one resumable episode per series (most recently watched, breaking ties by latest season/episode).
	seenSeries := make(map[string]bool, len(candidates))
	winnerIDs := make([]string, 0, len(candidates))
	for _, cand := range candidates {
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

	dtos := h.itemsToDTOs(r, p, rows, opts.Detail)
	attachResumeSeriesArtwork(dtos)
	return dtos, total
}

func (h *Handler) buildNextUpDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, limit int) []map[string]any {
	out, _ := h.buildNextUpDTOsPaged(r, p, visibleIDs, "", "", 0, limit, false)
	return out
}

func (h *Handler) buildNextUpDTOsPaged(r *http.Request, p *auth.Principal, visibleIDs []string, parentID, seriesID string, startIndex, limit int, detail bool) ([]map[string]any, int) {
	if p == nil || p.User == nil || len(visibleIDs) == 0 {
		return []map[string]any{}, 0
	}
	q := h.db.Read().Table("items").
		Select("items.*").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.type = ? AND items.library_id IN ?", "Episode", visibleIDs).
		Where("user_data.user_id = ? AND user_data.played = ?", p.User.ID, true)
	if parentID != "" && parentID != "root" && !strings.HasPrefix(parentID, "vlib-") {
		q = q.Where("items.library_id = ?", parentID)
	}
	if seriesID != "" {
		q = q.Where("items.series_id = ?", seriesID)
	}
	var playedEps []model.Item
	_ = q.Order("COALESCE(user_data.last_played_at, user_data.updated_at) DESC, user_data.updated_at DESC").Limit(200).Find(&playedEps).Error

	seenSeries := map[string]bool{}
	allNext := make([]model.Item, 0, limit)
	for _, ep := range playedEps {
		if ep.SeriesID == "" || seenSeries[ep.SeriesID] {
			continue
		}
		seenSeries[ep.SeriesID] = true
		var next model.Item
		err := h.db.Read().Where("series_id = ? AND type = ? AND soft_deleted_at IS NULL", ep.SeriesID, "Episode").
			Where("(season_num > ? OR (season_num = ? AND episode_num > ?))", ep.SeasonNum, ep.SeasonNum, ep.EpisodeNum).
			Where("id NOT IN (SELECT item_id FROM user_data WHERE user_id = ? AND played = ?)", p.User.ID, true).
			Order("season_num ASC, episode_num ASC").
			First(&next).Error
		if err == nil {
			allNext = append(allNext, next)
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
	return h.itemsToDTOs(r, p, allNext[startIndex:end], detail), total
}

func (h *Handler) buildCounts(visibleIDs []string) map[string]int64 {
	counts := map[string]int64{
		"MovieCount":   0,
		"SeriesCount":  0,
		"EpisodeCount": 0,
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
		Where("library_id IN ? AND soft_deleted_at IS NULL AND type IN ?", visibleIDs, []string{"Movie", "Series", "Episode"}).
		Group("type").
		Scan(&rows).Error
	for _, r := range rows {
		switch r.Type {
		case "Movie":
			counts["MovieCount"] = r.Cnt
		case "Series":
			counts["SeriesCount"] = r.Cnt
		case "Episode":
			counts["EpisodeCount"] = r.Cnt
		}
	}
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
	cacheKey := fmt.Sprintf("webhome:%s:%s:%s:%s", libRev, userRev, uid, visHash)

	h.writeCachedJSON(c, cacheKey, hotCacheTTL, func() (any, error) {
		views, err := h.buildUserViewsItems(p, visibleIDs)
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
