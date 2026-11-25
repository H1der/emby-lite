package server

import (
	"net/http"
	"strings"

	"emby-lite/internal/scraper"

	"github.com/gin-gonic/gin"
)

// --- TMDB Config & Connection Test ---

func (a *AdminHandler) GetTMDBConfig(c *gin.Context) {
	c.JSON(http.StatusOK, a.scraper.GetPublicConfig())
}

func (a *AdminHandler) UpdateTMDBConfig(c *gin.Context) {
	var body scraper.TMDBConfig
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.scraper.SaveConfig(body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, a.scraper.GetPublicConfig())
}

func (a *AdminHandler) TestTMDBConnection(c *gin.Context) {
	if err := a.scraper.TestConnection(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "TMDB 连接测试成功"})
}

// --- Scraping Triggers & Manual Match Queue ---

func (a *AdminHandler) ScrapeLibrary(c *gin.Context) {
	id := c.Param("id")
	force := strings.EqualFold(c.Query("force"), "true")
	if c.Query("sync") == "true" {
		stats, err := a.scraper.ScrapeLibrary(c.Request.Context(), id, force, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "stats": stats})
			return
		}
		c.JSON(http.StatusOK, stats)
		return
	}
	t, created, err := a.tasks.Enqueue("scrape", 8, map[string]any{
		"library_id": id,
		"force":      force,
	}, "等待 TMDB 刮削")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"started": created, "library_id": id, "task": t})
}

func (a *AdminHandler) ScrapeSingleItem(c *gin.Context) {
	itemID := c.Param("id")
	force := strings.EqualFold(c.Query("force"), "true")
	outcome, err := a.scraper.ScrapeItem(c.Request.Context(), itemID, force)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"item_id": itemID, "outcome": outcome})
}

func (a *AdminHandler) ListMatchQueue(c *gin.Context) {
	status := c.DefaultQuery("status", "pending")
	if status == "all" {
		status = ""
	}
	items, err := a.scraper.ListMatchQueue(status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (a *AdminHandler) ManualTMDBSearch(c *gin.Context) {
	var body struct {
		Query     string `json:"query" binding:"required"`
		Year      int    `json:"year"`
		MediaType string `json:"media_type"` // movie | tv
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(body.MediaType))
	if mediaType != "tv" {
		mediaType = "movie"
	}
	client := a.scraper.Client()
	var hits []scraper.TMDBSearchItem
	var err error
	if mediaType == "tv" {
		hits, err = client.SearchTV(c.Request.Context(), body.Query, body.Year)
	} else {
		hits, err = client.SearchMovie(c.Request.Context(), body.Query, body.Year)
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	candidates := scraper.ScoreCandidates(body.Query, "", body.Year, mediaType, hits, client.ImageURL)
	c.JSON(http.StatusOK, gin.H{"candidates": candidates})
}

func (a *AdminHandler) ResolveManualMatch(c *gin.Context) {
	itemID := c.Param("itemId")
	var body struct {
		TMDBID    int    `json:"tmdb_id" binding:"required"`
		MediaType string `json:"media_type"` // movie | tv
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.scraper.ResolveManualMatch(c.Request.Context(), itemID, body.MediaType, body.TMDBID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *AdminHandler) IgnoreManualMatch(c *gin.Context) {
	itemID := c.Param("itemId")
	if err := a.scraper.IgnoreManualMatch(itemID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// --- Manual Metadata Editor & Field Locks ---

func (a *AdminHandler) GetItemMetadata(c *gin.Context) {
	meta, err := a.scraper.GetItemMetadata(c.Param("itemId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, meta)
}

func (a *AdminHandler) UpdateItemMetadata(c *gin.Context) {
	var body scraper.MetadataUpdatePayload
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := a.scraper.UpdateItemMetadata(c.Param("itemId"), body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	meta, _ := a.scraper.GetItemMetadata(c.Param("itemId"))
	c.JSON(http.StatusOK, meta)
}
