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
	if len(visibleIDs) == 0 {
		return []map[string]any{}
	}
	q := h.db.Read().Model(&model.Item{}).
		Where("soft_deleted_at IS NULL AND library_id IN ? AND type IN ?", visibleIDs, []string{"Movie", "Series"})
	if parentID != "" {
		q = q.Where("library_id = ?", parentID)
	}
	var rows []model.Item
	_ = q.Order("added_at DESC, id ASC").Limit(limit).Find(&rows).Error
	return h.itemsToDTOs(r, p, rows, false)
}

func (h *Handler) buildResumeDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, limit int) []map[string]any {
	if p == nil || p.User == nil || len(visibleIDs) == 0 {
		return []map[string]any{}
	}
	var rows []model.Item
	_ = h.db.Read().Table("items").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.library_id IN ?", visibleIDs).
		Where("user_data.user_id = ? AND user_data.position_ticks > 0 AND user_data.played = ? AND user_data.hide_from_resume = ?", p.User.ID, false, false).
		Order("user_data.updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	return h.itemsToDTOs(r, p, rows, false)
}

func (h *Handler) buildNextUpDTOs(r *http.Request, p *auth.Principal, visibleIDs []string, limit int) []map[string]any {
	if p == nil || p.User == nil || len(visibleIDs) == 0 {
		return []map[string]any{}
	}
	var playedEps []model.Item
	_ = h.db.Read().Table("items").
		Joins("JOIN user_data ON user_data.item_id = items.id").
		Where("items.soft_deleted_at IS NULL AND items.type = ? AND items.library_id IN ?", "Episode", visibleIDs).
		Where("user_data.user_id = ? AND user_data.played = ?", p.User.ID, true).
		Order("user_data.updated_at DESC").
		Limit(50).
		Find(&playedEps).Error

	seenSeries := map[string]bool{}
	nextRows := make([]model.Item, 0, limit)
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
			nextRows = append(nextRows, next)
			if len(nextRows) >= limit {
				break
			}
		}
	}
	return h.itemsToDTOs(r, p, nextRows, false)
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
