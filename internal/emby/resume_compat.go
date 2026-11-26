package emby

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"emby-lite/internal/auth"

	"github.com/gin-gonic/gin"
)

const filmlyResumeETagSuffix = "-filmly-resume-episode-v1"

var (
	seasonDirNameRE   = regexp.MustCompile(`(?i)^(?:season\s*\d+|s\d{1,3}|specials?|ova|sp|第\s*\d+\s*季|特别篇)$`)
	trailingYearTagRE = regexp.MustCompile(`\s*[\(\[（【]\s*(?:19|20)\d{2}\s*[\)\]）】]\s*$`)
	episodeOnlyNameRE = regexp.MustCompile(`(?i)^(?:第\s*\d+\s*[集话話]|ep?\.?\s*\d+|s\d{1,2}e\d{1,3})$`)
)

func defaultSeasonName(seasonNum int) string {
	if seasonNum == 0 {
		return "特别篇"
	}
	if seasonNum < 0 {
		return ""
	}
	return fmt.Sprintf("第 %d 季", seasonNum)
}

// inferSeriesNameFromDirPath mirrors MediaStationGo's path fallback so an Episode
// with a missing/dangling series_id still yields a clean SeriesName instead of omitting it.
func inferSeriesNameFromDirPath(dirPath string) string {
	p := strings.TrimSpace(dirPath)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, `\`, `/`)
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	base := filepath.Base(p)
	if seasonDirNameRE.MatchString(strings.TrimSpace(base)) {
		parent := filepath.Dir(p)
		if parent != "" && parent != "." && parent != "/" && parent != p {
			base = filepath.Base(parent)
		}
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	base = strings.TrimSpace(trailingYearTagRE.ReplaceAllString(base, ""))
	if base == "" || episodeOnlyNameRE.MatchString(base) || seasonDirNameRE.MatchString(base) {
		return ""
	}
	return base
}

// attachResumeSeriesArtwork ensures Episode cards in Resume / Continue Watching
// expose Primary artwork even when the episode itself has no still/thumb image,
// inheriting the Series Primary poster and owner ID (matching MediaStationGo).
func attachResumeSeriesArtwork(items []map[string]any) {
	for _, item := range items {
		itemType, _ := item["Type"].(string)
		if !strings.EqualFold(strings.TrimSpace(itemType), "Episode") {
			continue
		}
		imageTags, ok := item["ImageTags"].(map[string]string)
		if !ok || imageTags == nil {
			continue
		}
		if strings.TrimSpace(imageTags["Primary"]) != "" {
			continue
		}
		seriesID, _ := item["SeriesId"].(string)
		seriesPrimaryTag, _ := item["SeriesPrimaryImageTag"].(string)
		seriesID = strings.TrimSpace(seriesID)
		seriesPrimaryTag = strings.TrimSpace(seriesPrimaryTag)
		if seriesID == "" || seriesPrimaryTag == "" {
			continue
		}
		imageTags["Primary"] = seriesPrimaryTag
		item["PrimaryImageItemId"] = seriesID
		item["PrimaryImageTag"] = seriesPrimaryTag
	}
}

func isFilmlyRequest(c *gin.Context, p *auth.Principal) bool {
	if p != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Client.Client)), "filmly") {
		return true
	}
	if c == nil || c.Request == nil {
		return false
	}
	meta := auth.ExtractClientMeta(c.Request)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(meta.Client)), "filmly")
}

// applyFilmlyResumeCompatibility mirrors MediaStationGo's Filmly (网易爆米花) Resume
// compatibility: Filmly ignores SeriesName on Continue Watching cards and collapses
// episodes by SeriesId, so we keep episode-scoped presentation with "<SeriesName> 第N集 <Name>"
// for Filmly only while preserving standard Emby DTOs for Yamby and all other clients.
func applyFilmlyResumeCompatibility(items []map[string]any) {
	for _, item := range items {
		itemID, _ := item["Id"].(string)
		itemID = strings.TrimSpace(itemID)
		if itemID == "" {
			continue
		}
		if ud, ok := item["UserData"].(map[string]any); ok && ud != nil {
			ud["Key"] = itemID
			ud["ItemId"] = itemID
		}
		itemType, _ := item["Type"].(string)
		if !strings.EqualFold(strings.TrimSpace(itemType), "Episode") {
			continue
		}
		seriesName, _ := item["SeriesName"].(string)
		name, _ := item["Name"].(string)
		indexNum, _ := item["IndexNumber"].(int)
		if strings.TrimSpace(seriesName) != "" {
			item["Name"] = formatFilmlyResumeEpisodeTitle(seriesName, indexNum, name)
		}
		if etag, _ := item["Etag"].(string); strings.TrimSpace(etag) != "" && !strings.HasSuffix(etag, filmlyResumeETagSuffix) {
			item["Etag"] = etag + filmlyResumeETagSuffix
		}
		for _, key := range []string{
			"ParentId",
			"ParentIndexNumber",
			"SeasonId",
			"SeasonName",
			"SeriesId",
			"SeriesName",
			"BackdropImageItemId",
			"ParentBackdropItemId",
			"ParentBackdropImageTags",
		} {
			delete(item, key)
		}
	}
}

func formatFilmlyResumeEpisodeTitle(seriesName string, indexNumber int, name string) string {
	parts := make([]string, 0, 3)
	if seriesName = strings.TrimSpace(seriesName); seriesName != "" {
		parts = append(parts, seriesName)
	}
	if indexNumber > 0 {
		episodeLabel := fmt.Sprintf("第%d集", indexNumber)
		parts = append(parts, episodeLabel)
		trimmedName := strings.TrimSpace(name)
		compactName := strings.ReplaceAll(trimmedName, " ", "")
		if trimmedName == episodeLabel || compactName == episodeLabel {
			name = ""
		}
	}
	if name = strings.TrimSpace(name); name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, " ")
}
