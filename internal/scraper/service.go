package scraper

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"
	"emby-lite/internal/scanner"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	settingTMDBAPIKey       = "tmdb_api_key"
	settingTMDBReadToken    = "tmdb_read_token"
	settingTMDBAPIBase      = "tmdb_api_base"
	settingTMDBImageBase    = "tmdb_image_base"
	settingTMDBProxyURL     = "tmdb_proxy_url"
	settingTMDBLanguage     = "tmdb_language"
	settingTMDBThreshold    = "tmdb_auto_threshold"
	settingTMDBWriteNFO     = "tmdb_write_nfo"
)

// ProgressFunc reports scraping progress into the task queue / SSE bus.
type ProgressFunc func(progress, total int, message string)

// MetadataUpdatePayload describes manual metadata edits and field locks.
type MetadataUpdatePayload struct {
	Name            *string  `json:"name"`
	OriginalName    *string  `json:"original_name"`
	SortName        *string  `json:"sort_name"`
	Overview        *string  `json:"overview"`
	Tagline         *string  `json:"tagline"`
	Year            *int     `json:"year"`
	PremiereDate    *string  `json:"premiere_date"`
	CommunityRating *float64 `json:"community_rating"`
	OfficialRating  *string  `json:"official_rating"`
	Genres          []string `json:"genres"`
	UpdateGenres    bool     `json:"update_genres"`
	Tags            []string `json:"tags"`
	UpdateTags      bool     `json:"update_tags"`
	LockedFields    []string `json:"locked_fields"`
	WriteNFO        bool     `json:"write_nfo"`
}

type Service struct {
	db *database.DB
}

func NewService(db *database.DB) *Service {
	return &Service{db: db}
}

func (s *Service) secret() string {
	return s.db.GetSetting("image_secret", "default-secret")
}

// GetConfig loads decrypted TMDB settings for internal requests.
func (s *Service) GetConfig() TMDBConfig {
	apiKey, _ := decryptSecret(s.secret(), s.db.GetSetting(settingTMDBAPIKey, ""))
	readTok, _ := decryptSecret(s.secret(), s.db.GetSetting(settingTMDBReadToken, ""))
	proxyURL, _ := decryptSecret(s.secret(), s.db.GetSetting(settingTMDBProxyURL, ""))
	thresh, _ := strconv.ParseFloat(s.db.GetSetting(settingTMDBThreshold, "75"), 64)
	if thresh <= 0 || thresh > 100 {
		thresh = 75
	}
	return TMDBConfig{
		APIKey:        apiKey,
		ReadToken:     readTok,
		APIBaseURL:    s.db.GetSetting(settingTMDBAPIBase, defaultTMDBAPIBase),
		ImageBaseURL:  s.db.GetSetting(settingTMDBImageBase, defaultTMDBImageBase),
		ProxyURL:      proxyURL,
		Language:      s.db.GetSetting(settingTMDBLanguage, defaultTMDBLanguage),
		AutoThreshold: thresh,
		WriteNFO:      s.db.GetSetting(settingTMDBWriteNFO, "false") == "true",
	}
}

// GetPublicConfig returns config safe for Admin UI (credentials are write-only, never echoed back).
func (s *Service) GetPublicConfig() map[string]any {
	cfg := s.GetConfig()
	return map[string]any{
		"has_api_key":    cfg.APIKey != "",
		"has_read_token": cfg.ReadToken != "",
		"has_proxy":      cfg.ProxyURL != "",
		"api_base_url":   cfg.APIBaseURL,
		"image_base_url": cfg.ImageBaseURL,
		"language":       cfg.Language,
		"auto_threshold": cfg.AutoThreshold,
		"write_nfo":      cfg.WriteNFO,
	}
}

// SaveConfig encrypts secrets and updates TMDB settings immediately without restart.
func (s *Service) SaveConfig(in TMDBConfig) error {
	if strings.TrimSpace(in.APIKey) != "" {
		enc, err := encryptSecret(s.secret(), strings.TrimSpace(in.APIKey))
		if err != nil {
			return err
		}
		if err := s.db.SetSetting(settingTMDBAPIKey, enc, true); err != nil {
			return err
		}
	}
	if strings.TrimSpace(in.ReadToken) != "" {
		enc, err := encryptSecret(s.secret(), strings.TrimSpace(in.ReadToken))
		if err != nil {
			return err
		}
		if err := s.db.SetSetting(settingTMDBReadToken, enc, true); err != nil {
			return err
		}
	}
	if in.ProxyURL != "" {
		val := strings.TrimSpace(in.ProxyURL)
		if val == "__clear__" {
			_ = s.db.SetSetting(settingTMDBProxyURL, "", true)
		} else {
			enc, err := encryptSecret(s.secret(), val)
			if err != nil {
				return err
			}
			_ = s.db.SetSetting(settingTMDBProxyURL, enc, true)
		}
	}
	if strings.TrimSpace(in.APIBaseURL) != "" {
		_ = s.db.SetSetting(settingTMDBAPIBase, strings.TrimSpace(in.APIBaseURL), false)
	}
	if strings.TrimSpace(in.ImageBaseURL) != "" {
		_ = s.db.SetSetting(settingTMDBImageBase, strings.TrimSpace(in.ImageBaseURL), false)
	}
	if strings.TrimSpace(in.Language) != "" {
		_ = s.db.SetSetting(settingTMDBLanguage, strings.TrimSpace(in.Language), false)
	}
	if in.AutoThreshold > 0 && in.AutoThreshold <= 100 {
		_ = s.db.SetSetting(settingTMDBThreshold, fmt.Sprintf("%.0f", in.AutoThreshold), false)
	}
	_ = s.db.SetSetting(settingTMDBWriteNFO, strconv.FormatBool(in.WriteNFO), false)
	return nil
}

func (s *Service) Client() *TMDBClient {
	return NewTMDBClient(s.db, s.GetConfig())
}

// ResolveRemoteImage rewrites TMDB image URLs to custom mirror and returns an HTTP client honoring configured proxy.
func (s *Service) ResolveRemoteImage(rawURL string) (string, *http.Client) {
	c := s.Client()
	return c.RewriteImageURL(rawURL), c.HTTPClient()
}

// LookupPersonPortrait fetches a missing actor/director portrait from TMDB (cached in provider_cache).
func (s *Service) LookupPersonPortrait(ctx context.Context, name string) (tmdbID string, imageURL string) {
	c := s.Client()
	if !c.HasCredentials() {
		return "", ""
	}
	id, u, err := c.SearchPersonProfile(ctx, name)
	if err != nil {
		return "", ""
	}
	return id, u
}

func (s *Service) TestConnection(ctx context.Context) error {
	return s.Client().TestConnection(ctx)
}

// ScrapeStats summarizes a scraping run.
type ScrapeStats struct {
	Total       int `json:"total"`
	Matched     int `json:"matched"`
	Queued      int `json:"queued"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
}

// ScrapeLibrary scrapes Movies & Series in a library via TMDB (respecting local priority & field locks).
func (s *Service) ScrapeLibrary(ctx context.Context, libraryID string, force bool, report ProgressFunc) (*ScrapeStats, error) {
	client := s.Client()
	if !client.HasCredentials() {
		return nil, errors.New("请先在后台配置 TMDB API Key 或 Read Access Token")
	}

	var items []model.Item
	q := s.db.Gorm.Where("library_id = ? AND soft_deleted_at IS NULL AND type IN ?", libraryID, []string{"Movie", "Series"})
	if err := q.Order("added_at DESC").Find(&items).Error; err != nil {
		return nil, err
	}

	stats := &ScrapeStats{Total: len(items)}
	for i := range items {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		it := &items[i]
		if report != nil {
			report(i, len(items), fmt.Sprintf("刮削 (%d/%d): %s", i+1, len(items), it.Name))
		}
		// Skip already-matched items with overview & poster unless force=true.
		if !force && s.hasCompleteMetadata(it) {
			stats.Skipped++
			continue
		}
		outcome, err := s.ScrapeItem(ctx, it.ID, force)
		if err != nil {
			stats.Failed++
			continue
		}
		switch outcome {
		case "matched":
			stats.Matched++
		case "queued":
			stats.Queued++
		default:
			stats.Skipped++
		}
	}
	if report != nil {
		report(len(items), len(items), fmt.Sprintf("刮削完成：自动匹配 %d，待人工确认 %d，跳过 %d，失败 %d",
			stats.Matched, stats.Queued, stats.Skipped, stats.Failed))
	}
	s.db.BumpLibraryRevision()
	return stats, nil
}

// ScrapeItem scrapes a single Movie or Series using existing TMDB ID or scored search.
func (s *Service) ScrapeItem(ctx context.Context, itemID string, force bool) (string, error) {
	var item model.Item
	if err := s.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		return "", err
	}
	if item.Type != "Movie" && item.Type != "Series" {
		return "skipped", nil
	}

	cfg := s.GetConfig()
	client := NewTMDBClient(s.db, cfg)
	if !client.HasCredentials() {
		return "", errors.New("TMDB credentials not configured")
	}

	mediaType := "movie"
	if item.Type == "Series" {
		mediaType = "tv"
	}

	// 1. If item already has a TMDB provider ID (e.g. from local NFO), fetch detail directly!
	pids := map[string]string{}
	_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &pids)
	if rawID := strings.TrimSpace(pids["Tmdb"]); rawID != "" {
		if tmdbID, err := strconv.Atoi(rawID); err == nil && tmdbID > 0 {
			if err := s.ApplyTMDBMatch(ctx, &item, mediaType, tmdbID); err != nil {
				return "", err
			}
			return "matched", nil
		}
	}

	// 2. Otherwise search TMDB & score candidates.
	var hits []TMDBSearchItem
	var err error
	if mediaType == "tv" {
		hits, err = client.SearchTV(ctx, item.Name, item.Year)
		if err == nil && len(hits) == 0 && item.Year > 0 {
			hits, err = client.SearchTV(ctx, item.Name, 0)
		}
	} else {
		hits, err = client.SearchMovie(ctx, item.Name, item.Year)
		if err == nil && len(hits) == 0 && item.Year > 0 {
			hits, err = client.SearchMovie(ctx, item.Name, 0)
		}
	}
	if err != nil {
		return "", err
	}

	candidates := ScoreCandidates(item.Name, item.OriginalName, item.Year, mediaType, hits, client.ImageURL)
	if len(candidates) > 0 && candidates[0].Score >= cfg.AutoThreshold {
		if err := s.ApplyTMDBMatch(ctx, &item, mediaType, candidates[0].ProviderID); err != nil {
			return "", err
		}
		_ = s.db.Gorm.Model(&model.ScrapeMatchQueue{}).
			Where("item_id = ?", item.ID).
			Updates(map[string]any{"status": "resolved", "updated_at": time.Now().UTC()}).Error
		return "matched", nil
	}

	// 3. Low confidence or no exact hit -> enqueue into manual match queue!
	bestScore := 0.0
	if len(candidates) > 0 {
		bestScore = candidates[0].Score
	}
	candBytes, _ := json.Marshal(candidates)
	now := time.Now().UTC()
	var existing model.ScrapeMatchQueue
	if err := s.db.Gorm.Where("item_id = ?", item.ID).First(&existing).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		_ = s.db.Gorm.Create(&model.ScrapeMatchQueue{
			ID:             strings.ReplaceAll(uuid.NewString(), "-", ""),
			ItemID:         item.ID,
			LibraryID:      item.LibraryID,
			ItemType:       item.Type,
			ItemName:       item.Name,
			ItemYear:       item.Year,
			Status:         "pending",
			BestScore:      bestScore,
			CandidatesJSON: string(candBytes),
			CreatedAt:      now,
			UpdatedAt:      now,
		}).Error
	} else if err == nil {
		_ = s.db.Gorm.Model(&existing).Updates(map[string]any{
			"item_name":       item.Name,
			"item_year":       item.Year,
			"status":          "pending",
			"best_score":      bestScore,
			"candidates_json": string(candBytes),
			"updated_at":      now,
		}).Error
	}
	return "queued", nil
}

// ApplyTMDBMatch fetches full TMDB detail and merges unlocked fields & missing images into the item.
func (s *Service) ApplyTMDBMatch(ctx context.Context, item *model.Item, mediaType string, tmdbID int) error {
	cfg := s.GetConfig()
	client := NewTMDBClient(s.db, cfg)

	var detail *TMDBDetail
	var err error
	if mediaType == "tv" || item.Type == "Series" {
		detail, err = client.TVDetail(ctx, tmdbID)
	} else {
		detail, err = client.MovieDetail(ctx, tmdbID)
	}
	if err != nil {
		return err
	}

	locks := s.GetLockedFields(item.ID)
	isLocked := func(field string) bool { return locks[strings.ToLower(field)] }

	if !isLocked("name") && strings.TrimSpace(detail.EffectiveTitle()) != "" {
		item.Name = strings.TrimSpace(detail.EffectiveTitle())
		if !isLocked("sort_name") {
			item.SortName = scanner.BuildSortName(item.Name)
		}
	}
	if !isLocked("original_name") && strings.TrimSpace(detail.EffectiveOriginalTitle()) != "" {
		item.OriginalName = strings.TrimSpace(detail.EffectiveOriginalTitle())
	}
	item.PinyinInitials = scanner.PinyinInitials(item.Name + " " + item.OriginalName)

	if !isLocked("overview") && strings.TrimSpace(detail.Overview) != "" {
		item.Overview = strings.TrimSpace(detail.Overview)
	}
	if !isLocked("tagline") && strings.TrimSpace(detail.Tagline) != "" {
		item.Tagline = strings.TrimSpace(detail.Tagline)
	}
	if d := strings.TrimSpace(detail.EffectiveDate()); d != "" {
		if !isLocked("premiere_date") {
			item.PremiereDate = d
		}
		if !isLocked("year") && len(d) >= 4 {
			if y, err := strconv.Atoi(d[:4]); err == nil && y > 1900 {
				item.Year = y
			}
		}
	}
	if !isLocked("community_rating") && detail.VoteAverage > 0 {
		item.CommunityRating = detail.VoteAverage
	}
	if item.RuntimeTicks == 0 && detail.Runtime > 0 {
		item.RuntimeTicks = int64(detail.Runtime) * 60 * 1e7
	}

	// Merge ProviderIds
	pids := map[string]string{}
	_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &pids)
	pids["Tmdb"] = strconv.Itoa(detail.ID)
	if imdb := strings.TrimSpace(detail.EffectiveIMDB()); imdb != "" {
		pids["Imdb"] = imdb
	}
	if detail.ExternalIDs.TVDBID > 0 {
		pids["Tvdb"] = strconv.Itoa(detail.ExternalIDs.TVDBID)
	}
	if b, err := json.Marshal(pids); err == nil {
		item.ProviderIDsJSON = string(b)
	}

	// Studios
	var studios []string
	for _, st := range detail.ProductionCompanies {
		if n := strings.TrimSpace(st.Name); n != "" {
			studios = append(studios, n)
		}
	}
	if len(studios) > 0 {
		if b, err := json.Marshal(studios); err == nil {
			item.StudiosJSON = string(b)
		}
	}

	if err := s.db.Gorm.Save(item).Error; err != nil {
		return err
	}
	if s.db.HasFTS5 {
		_ = s.db.Gorm.Exec("DELETE FROM items_fts WHERE item_id = ?", item.ID).Error
		_ = s.db.Gorm.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
			item.ID, item.Name, item.OriginalName, item.PinyinInitials).Error
	}

	// Genres (respect lock)
	if !isLocked("genres") && len(detail.Genres) > 0 {
		_ = s.db.Gorm.Where("item_id = ?", item.ID).Delete(&model.ItemGenre{}).Error
		for _, g := range detail.Genres {
			if n := strings.TrimSpace(g.Name); n != "" {
				_ = s.db.Gorm.Create(&model.ItemGenre{ItemID: item.ID, Genre: n}).Error
			}
		}
	}

	// People (Actors + Directors)
	if len(detail.Credits.Cast) > 0 || len(detail.Credits.Crew) > 0 {
		_ = s.db.Gorm.Where("item_id = ?", item.ID).Delete(&model.ItemPerson{}).Error
		idx := 0
		for _, c := range detail.Credits.Cast {
			if idx >= 15 {
				break
			}
			name := strings.TrimSpace(c.Name)
			if name == "" {
				continue
			}
			pid := scanner.StableID("person", strings.ToLower(name))
			thumb := ""
			if c.ProfilePath != "" {
				thumb = client.ImageURL("w185", c.ProfilePath)
			}
			_ = s.db.Gorm.Save(&model.Person{
				ID:        pid,
				Name:      name,
				TMDBID:    strconv.Itoa(c.ID),
				ThumbPath: thumb,
				UpdatedAt: time.Now().UTC(),
			}).Error
			_ = s.db.Gorm.Save(&model.ItemPerson{
				ItemID:   item.ID,
				PersonID: pid,
				RoleType: "Actor",
				RoleName: strings.TrimSpace(c.Character),
				SortIdx:  idx,
			}).Error
			idx++
		}
		for _, cr := range detail.Credits.Crew {
			if cr.Job != "Director" {
				continue
			}
			name := strings.TrimSpace(cr.Name)
			if name == "" {
				continue
			}
			pid := scanner.StableID("person", strings.ToLower(name))
			thumb := ""
			if cr.ProfilePath != "" {
				thumb = client.ImageURL("w185", cr.ProfilePath)
			}
			_ = s.db.Gorm.Save(&model.Person{
				ID:        pid,
				Name:      name,
				TMDBID:    strconv.Itoa(cr.ID),
				ThumbPath: thumb,
				UpdatedAt: time.Now().UTC(),
			}).Error
			_ = s.db.Gorm.Save(&model.ItemPerson{
				ItemID:   item.ID,
				PersonID: pid,
				RoleType: "Director",
				SortIdx:  idx,
			}).Error
			idx++
		}
	}

	// Remote Images: only fill if NO local or uploaded image already exists for that kind!
	if detail.PosterPath != "" {
		s.upsertRemoteImageIfMissing(item.ID, "Primary", client.ImageURL("w780", detail.PosterPath))
	}
	if detail.BackdropPath != "" {
		s.upsertRemoteImageIfMissing(item.ID, "Backdrop", client.ImageURL("w1280", detail.BackdropPath))
	}

	if cfg.WriteNFO {
		_ = s.WriteItemNFO(item)
	}
	return nil
}

func (s *Service) upsertRemoteImageIfMissing(itemID, kind, remoteURL string) {
	if remoteURL == "" {
		return
	}
	var existing model.ItemImage
	if err := s.db.Gorm.Where("item_id = ? AND kind = ? AND index_num = 0", itemID, kind).First(&existing).Error; err == nil {
		// Never overwrite local or manually uploaded artwork with remote TMDB artwork!
		if existing.SourceType == "local" || existing.SourceType == "upload" {
			return
		}
	}
	tag := scanner.StableID("imgtag", remoteURL)[:16]
	_ = s.db.Gorm.Save(&model.ItemImage{
		ID:         scanner.StableID("img", itemID+"|"+kind+"|0"),
		ItemID:     itemID,
		Kind:       kind,
		IndexNum:   0,
		SourceType: "remote",
		SourceRef:  remoteURL,
		Tag:        tag,
		UpdatedAt:  time.Now().UTC(),
	}).Error
}

func (s *Service) hasCompleteMetadata(item *model.Item) bool {
	if strings.TrimSpace(item.Overview) == "" {
		return false
	}
	var imgCount int64
	s.db.Gorm.Model(&model.ItemImage{}).Where("item_id = ? AND kind = ?", item.ID, "Primary").Count(&imgCount)
	return imgCount > 0
}

// --- Manual Match Queue ---

func (s *Service) ListMatchQueue(status string) ([]map[string]any, error) {
	q := s.db.Gorm.Model(&model.ScrapeMatchQueue{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []model.ScrapeMatchQueue
	if err := q.Order("updated_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var cands []MatchCandidate
		_ = json.Unmarshal([]byte(r.CandidatesJSON), &cands)
		out = append(out, map[string]any{
			"id":         r.ID,
			"item_id":    r.ItemID,
			"library_id": r.LibraryID,
			"item_type":  r.ItemType,
			"item_name":  r.ItemName,
			"item_year":  r.ItemYear,
			"status":     r.Status,
			"best_score": r.BestScore,
			"candidates": cands,
			"updated_at": r.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Service) ResolveManualMatch(ctx context.Context, itemID, mediaType string, tmdbID int) error {
	var item model.Item
	if err := s.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		return err
	}
	if err := s.ApplyTMDBMatch(ctx, &item, mediaType, tmdbID); err != nil {
		return err
	}
	_ = s.db.Gorm.Model(&model.ScrapeMatchQueue{}).
		Where("item_id = ?", itemID).
		Updates(map[string]any{"status": "resolved", "updated_at": time.Now().UTC()}).Error
	s.db.BumpLibraryRevision()
	return nil
}

func (s *Service) IgnoreManualMatch(itemID string) error {
	return s.db.Gorm.Model(&model.ScrapeMatchQueue{}).
		Where("item_id = ?", itemID).
		Updates(map[string]any{"status": "ignored", "updated_at": time.Now().UTC()}).Error
}

// --- Manual Metadata Editing & Field Locking ---

func (s *Service) GetLockedFields(itemID string) map[string]bool {
	out := map[string]bool{}
	var lock model.ItemMetadataLock
	if err := s.db.Gorm.Where("item_id = ?", itemID).First(&lock).Error; err != nil {
		return out
	}
	var fields []string
	if json.Unmarshal([]byte(lock.LockedFieldsJSON), &fields) == nil {
		for _, f := range fields {
			out[strings.ToLower(strings.TrimSpace(f))] = true
		}
	}
	return out
}

func (s *Service) GetItemMetadata(itemID string) (map[string]any, error) {
	var item model.Item
	if err := s.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		return nil, err
	}
	var genres []string
	_ = s.db.Gorm.Model(&model.ItemGenre{}).Where("item_id = ?", itemID).Pluck("genre", &genres).Error
	var tags []string
	_ = s.db.Gorm.Model(&model.ItemTag{}).Where("item_id = ?", itemID).Pluck("tag", &tags).Error

	var lockedFields []string
	var lock model.ItemMetadataLock
	if err := s.db.Gorm.Where("item_id = ?", itemID).First(&lock).Error; err == nil {
		_ = json.Unmarshal([]byte(lock.LockedFieldsJSON), &lockedFields)
	}
	pids := map[string]string{}
	_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &pids)

	return map[string]any{
		"id":               item.ID,
		"type":             item.Type,
		"name":             item.Name,
		"original_name":    item.OriginalName,
		"sort_name":        item.SortName,
		"overview":         item.Overview,
		"tagline":          item.Tagline,
		"year":             item.Year,
		"premiere_date":    item.PremiereDate,
		"community_rating": item.CommunityRating,
		"official_rating":  item.OfficialRating,
		"genres":           genres,
		"tags":             tags,
		"provider_ids":     pids,
		"locked_fields":    lockedFields,
	}, nil
}

func (s *Service) UpdateItemMetadata(itemID string, in MetadataUpdatePayload) error {
	var item model.Item
	if err := s.db.Gorm.Where("id = ? AND soft_deleted_at IS NULL", itemID).First(&item).Error; err != nil {
		return err
	}
	if in.Name != nil {
		item.Name = strings.TrimSpace(*in.Name)
	}
	if in.OriginalName != nil {
		item.OriginalName = strings.TrimSpace(*in.OriginalName)
	}
	if in.SortName != nil && strings.TrimSpace(*in.SortName) != "" {
		item.SortName = strings.TrimSpace(*in.SortName)
	} else {
		item.SortName = scanner.BuildSortName(item.Name)
	}
	item.PinyinInitials = scanner.PinyinInitials(item.Name + " " + item.OriginalName)

	if in.Overview != nil {
		item.Overview = strings.TrimSpace(*in.Overview)
	}
	if in.Tagline != nil {
		item.Tagline = strings.TrimSpace(*in.Tagline)
	}
	if in.Year != nil {
		item.Year = *in.Year
	}
	if in.PremiereDate != nil {
		item.PremiereDate = strings.TrimSpace(*in.PremiereDate)
	}
	if in.CommunityRating != nil {
		item.CommunityRating = *in.CommunityRating
	}
	if in.OfficialRating != nil {
		item.OfficialRating = strings.TrimSpace(*in.OfficialRating)
	}

	if err := s.db.Gorm.Save(&item).Error; err != nil {
		return err
	}
	if s.db.HasFTS5 {
		_ = s.db.Gorm.Exec("DELETE FROM items_fts WHERE item_id = ?", item.ID).Error
		_ = s.db.Gorm.Exec("INSERT INTO items_fts(item_id, name, original_name, pinyin_initials) VALUES (?, ?, ?, ?)",
			item.ID, item.Name, item.OriginalName, item.PinyinInitials).Error
	}

	if in.UpdateGenres {
		_ = s.db.Gorm.Where("item_id = ?", itemID).Delete(&model.ItemGenre{}).Error
		for _, g := range in.Genres {
			if n := strings.TrimSpace(g); n != "" {
				_ = s.db.Gorm.Create(&model.ItemGenre{ItemID: itemID, Genre: n}).Error
			}
		}
	}
	if in.UpdateTags {
		_ = s.db.Gorm.Where("item_id = ?", itemID).Delete(&model.ItemTag{}).Error
		for _, t := range in.Tags {
			if n := strings.TrimSpace(t); n != "" {
				_ = s.db.Gorm.Create(&model.ItemTag{ItemID: itemID, Tag: n}).Error
			}
		}
	}

	// Persist locked fields list.
	cleanedLocks := make([]string, 0, len(in.LockedFields))
	for _, f := range in.LockedFields {
		if lf := strings.ToLower(strings.TrimSpace(f)); lf != "" {
			cleanedLocks = append(cleanedLocks, lf)
		}
	}
	lockBytes, _ := json.Marshal(cleanedLocks)
	var existingLock model.ItemMetadataLock
	var rev int64 = 1
	if err := s.db.Gorm.Where("item_id = ?", itemID).First(&existingLock).Error; err == nil {
		rev = existingLock.Revision + 1
	}
	_ = s.db.Gorm.Save(&model.ItemMetadataLock{
		ItemID:           itemID,
		LockedFieldsJSON: string(lockBytes),
		Revision:         rev,
		UpdatedAt:        time.Now().UTC(),
	}).Error

	if in.WriteNFO {
		_ = s.WriteItemNFO(&item)
	}
	s.db.BumpLibraryRevision()
	return nil
}

// WriteItemNFO writes an Emby/Kodi-compatible XML NFO next to the media item.
func (s *Service) WriteItemNFO(item *model.Item) error {
	if item.DirPath == "" {
		return nil
	}
	var targetPath, rootTag string
	switch item.Type {
	case "Series":
		targetPath = filepath.Join(item.DirPath, "tvshow.nfo")
		rootTag = "tvshow"
	case "Movie":
		var src model.MediaSource
		if err := s.db.Gorm.Where("item_id = ?", item.ID).First(&src).Error; err == nil && src.Path != "" {
			stem := strings.TrimSuffix(src.Path, filepath.Ext(src.Path))
			targetPath = stem + ".nfo"
		} else {
			targetPath = filepath.Join(item.DirPath, "movie.nfo")
		}
		rootTag = "movie"
	default:
		return nil
	}

	var genres []string
	_ = s.db.Gorm.Model(&model.ItemGenre{}).Where("item_id = ?", item.ID).Pluck("genre", &genres).Error
	pids := map[string]string{}
	_ = json.Unmarshal([]byte(item.ProviderIDsJSON), &pids)

	type xmlDoc struct {
		XMLName       xml.Name
		Title         string   `xml:"title"`
		OriginalTitle string   `xml:"originaltitle,omitempty"`
		SortTitle     string   `xml:"sorttitle,omitempty"`
		Plot          string   `xml:"plot,omitempty"`
		Tagline       string   `xml:"tagline,omitempty"`
		Year          int      `xml:"year,omitempty"`
		Premiered     string   `xml:"premiered,omitempty"`
		Rating        float64  `xml:"rating,omitempty"`
		MPAA          string   `xml:"mpaa,omitempty"`
		Genres        []string `xml:"genre,omitempty"`
		TMDBID        string   `xml:"tmdbid,omitempty"`
		IMDBID        string   `xml:"imdbid,omitempty"`
	}
	doc := xmlDoc{
		XMLName:       xml.Name{Local: rootTag},
		Title:         item.Name,
		OriginalTitle: item.OriginalName,
		SortTitle:     item.SortName,
		Plot:          item.Overview,
		Tagline:       item.Tagline,
		Year:          item.Year,
		Premiered:     item.PremiereDate,
		Rating:        item.CommunityRating,
		MPAA:          item.OfficialRating,
		Genres:        genres,
		TMDBID:        pids["Tmdb"],
		IMDBID:        pids["Imdb"],
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	content := append([]byte(xml.Header), out...)
	return os.WriteFile(targetPath, content, 0o644)
}
