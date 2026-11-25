package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/database"
	"emby-lite/internal/model"
)

const (
	defaultTMDBAPIBase   = "https://api.themoviedb.org/3"
	defaultTMDBImageBase = "https://image.tmdb.org/t/p"
	defaultTMDBLanguage  = "zh-CN"
	cacheTTLSearch       = 7 * 24 * time.Hour
	cacheTTLDetail       = 30 * 24 * time.Hour
)

// TMDBConfig holds runtime TMDB provider settings (loaded from encrypted DB settings).
type TMDBConfig struct {
	APIKey        string  `json:"api_key,omitempty"`
	ReadToken     string  `json:"read_token,omitempty"`
	APIBaseURL    string  `json:"api_base_url"`
	ImageBaseURL  string  `json:"image_base_url"`
	ProxyURL      string  `json:"proxy_url"`
	Language      string  `json:"language"`
	AutoThreshold float64 `json:"auto_threshold"` // default 75
	WriteNFO      bool    `json:"write_nfo"`
}

// TMDBSearchItem represents a movie or TV candidate returned by TMDB search endpoints.
type TMDBSearchItem struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`          // movie
	OriginalTitle string  `json:"original_title"` // movie
	Name          string  `json:"name"`           // tv
	OriginalName  string  `json:"original_name"`  // tv
	Overview      string  `json:"overview"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	ReleaseDate   string  `json:"release_date"`   // movie
	FirstAirDate  string  `json:"first_air_date"` // tv
	VoteAverage   float64 `json:"vote_average"`
	Popularity    float64 `json:"popularity"`
}

func (it TMDBSearchItem) EffectiveTitle() string {
	if it.Title != "" {
		return it.Title
	}
	return it.Name
}

func (it TMDBSearchItem) EffectiveOriginalTitle() string {
	if it.OriginalTitle != "" {
		return it.OriginalTitle
	}
	return it.OriginalName
}

func (it TMDBSearchItem) EffectiveDate() string {
	if it.ReleaseDate != "" {
		return it.ReleaseDate
	}
	return it.FirstAirDate
}

func (it TMDBSearchItem) EffectiveYear() int {
	d := it.EffectiveDate()
	if len(d) >= 4 {
		y, _ := strconv.Atoi(d[:4])
		return y
	}
	return 0
}

type TMDBDetail struct {
	ID               int     `json:"id"`
	IMDBID           string  `json:"imdb_id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	Name             string  `json:"name"`
	OriginalName     string  `json:"original_name"`
	Overview         string  `json:"overview"`
	Tagline          string  `json:"tagline"`
	ReleaseDate      string  `json:"release_date"`
	FirstAirDate     string  `json:"first_air_date"`
	VoteAverage      float64 `json:"vote_average"`
	Runtime          int     `json:"runtime"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	Genres           []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	ProductionCompanies []struct {
		Name string `json:"name"`
	} `json:"production_companies"`
	Credits struct {
		Cast []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Character   string `json:"character"`
			ProfilePath string `json:"profile_path"`
		} `json:"cast"`
		Crew []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Job         string `json:"job"`
			ProfilePath string `json:"profile_path"`
		} `json:"crew"`
	} `json:"credits"`
	ExternalIDs struct {
		IMDBID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	} `json:"external_ids"`
}

func (d *TMDBDetail) EffectiveTitle() string {
	if d.Title != "" {
		return d.Title
	}
	return d.Name
}

func (d *TMDBDetail) EffectiveOriginalTitle() string {
	if d.OriginalTitle != "" {
		return d.OriginalTitle
	}
	return d.OriginalName
}

func (d *TMDBDetail) EffectiveDate() string {
	if d.ReleaseDate != "" {
		return d.ReleaseDate
	}
	return d.FirstAirDate
}

func (d *TMDBDetail) EffectiveIMDB() string {
	if d.IMDBID != "" {
		return d.IMDBID
	}
	return d.ExternalIDs.IMDBID
}

// TMDBClient queries TMDB with transparent DB provider_cache persistence.
type TMDBClient struct {
	db  *database.DB
	cfg TMDBConfig
	hc  *http.Client
}

func NewTMDBClient(db *database.DB, cfg TMDBConfig) *TMDBClient {
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultTMDBAPIBase
	}
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if strings.TrimSpace(cfg.ImageBaseURL) == "" {
		cfg.ImageBaseURL = defaultTMDBImageBase
	}
	cfg.ImageBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ImageBaseURL), "/")
	if strings.TrimSpace(cfg.Language) == "" {
		cfg.Language = defaultTMDBLanguage
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		if pu, err := url.Parse(strings.TrimSpace(cfg.ProxyURL)); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	return &TMDBClient{
		db:  db,
		cfg: cfg,
		hc: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
}

func (c *TMDBClient) HasCredentials() bool {
	return strings.TrimSpace(c.cfg.APIKey) != "" || strings.TrimSpace(c.cfg.ReadToken) != ""
}

func (c *TMDBClient) HTTPClient() *http.Client {
	return c.hc
}

// RewriteImageURL rewrites default image.tmdb.org URLs to the configured custom image mirror if set.
func (c *TMDBClient) RewriteImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if c.cfg.ImageBaseURL != "" && c.cfg.ImageBaseURL != defaultTMDBImageBase {
		if strings.HasPrefix(raw, defaultTMDBImageBase) {
			return c.cfg.ImageBaseURL + strings.TrimPrefix(raw, defaultTMDBImageBase)
		}
	}
	return raw
}

// SearchPersonProfile looks up a person's TMDB profile image URL (cached in provider_cache).
func (c *TMDBClient) SearchPersonProfile(ctx context.Context, name string) (string, string, error) {
	if !c.HasCredentials() || strings.TrimSpace(name) == "" {
		return "", "", nil
	}
	params := url.Values{}
	params.Set("query", strings.TrimSpace(name))
	params.Set("language", c.cfg.Language)
	params.Set("include_adult", "false")
	cacheKey := fmt.Sprintf("search:person:%s:%s", strings.ToLower(strings.TrimSpace(name)), c.cfg.Language)
	var resp struct {
		Results []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			ProfilePath string `json:"profile_path"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, cacheKey, cacheTTLDetail, "/search/person", params, &resp); err != nil {
		return "", "", err
	}
	for _, r := range resp.Results {
		if r.ProfilePath != "" {
			return strconv.Itoa(r.ID), c.ImageURL("w185", r.ProfilePath), nil
		}
	}
	return "", "", nil
}

func (c *TMDBClient) ImageURL(size, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.cfg.ImageBaseURL + "/" + size + path
}

func (c *TMDBClient) TestConnection(ctx context.Context) error {
	if !c.HasCredentials() {
		return errors.New("未配置 TMDB API Key 或 Read Access Token")
	}
	var out map[string]any
	return c.getJSON(ctx, "", 0, "/configuration", nil, &out)
}

func (c *TMDBClient) SearchMovie(ctx context.Context, query string, year int) ([]TMDBSearchItem, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("language", c.cfg.Language)
	params.Set("include_adult", "false")
	if year > 0 {
		params.Set("year", strconv.Itoa(year))
	}
	cacheKey := fmt.Sprintf("search:movie:%s:%d:%s", strings.ToLower(strings.TrimSpace(query)), year, c.cfg.Language)
	var resp struct {
		Results []TMDBSearchItem `json:"results"`
	}
	if err := c.getJSON(ctx, cacheKey, cacheTTLSearch, "/search/movie", params, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *TMDBClient) SearchTV(ctx context.Context, query string, year int) ([]TMDBSearchItem, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("language", c.cfg.Language)
	params.Set("include_adult", "false")
	if year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(year))
	}
	cacheKey := fmt.Sprintf("search:tv:%s:%d:%s", strings.ToLower(strings.TrimSpace(query)), year, c.cfg.Language)
	var resp struct {
		Results []TMDBSearchItem `json:"results"`
	}
	if err := c.getJSON(ctx, cacheKey, cacheTTLSearch, "/search/tv", params, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (c *TMDBClient) MovieDetail(ctx context.Context, tmdbID int) (*TMDBDetail, error) {
	params := url.Values{}
	params.Set("language", c.cfg.Language)
	params.Set("append_to_response", "credits,external_ids")
	cacheKey := fmt.Sprintf("detail:movie:%d:%s", tmdbID, c.cfg.Language)
	var detail TMDBDetail
	if err := c.getJSON(ctx, cacheKey, cacheTTLDetail, fmt.Sprintf("/movie/%d", tmdbID), params, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (c *TMDBClient) TVDetail(ctx context.Context, tmdbID int) (*TMDBDetail, error) {
	params := url.Values{}
	params.Set("language", c.cfg.Language)
	params.Set("append_to_response", "credits,external_ids")
	cacheKey := fmt.Sprintf("detail:tv:%d:%s", tmdbID, c.cfg.Language)
	var detail TMDBDetail
	if err := c.getJSON(ctx, cacheKey, cacheTTLDetail, fmt.Sprintf("/tv/%d", tmdbID), params, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (c *TMDBClient) getJSON(ctx context.Context, cacheKey string, ttl time.Duration, endpoint string, params url.Values, dst any) error {
	// 1. Check DB provider_cache first (zero API call on re-scrape!).
	if cacheKey != "" && c.db != nil {
		var cached model.ProviderCache
		if err := c.db.Gorm.Where("provider = ? AND cache_key = ? AND expires_at > ?", "tmdb", cacheKey, time.Now().UTC()).First(&cached).Error; err == nil {
			if json.Unmarshal([]byte(cached.PayloadJSON), dst) == nil {
				return nil
			}
		}
	}

	if !c.HasCredentials() {
		return errors.New("TMDB credentials not configured")
	}

	q := url.Values{}
	for k, vals := range params {
		for _, v := range vals {
			q.Add(k, v)
		}
	}
	if strings.TrimSpace(c.cfg.ReadToken) == "" && strings.TrimSpace(c.cfg.APIKey) != "" {
		q.Set("api_key", strings.TrimSpace(c.cfg.APIKey))
	}

	fullURL := c.cfg.APIBaseURL + endpoint
	if enc := q.Encode(); enc != "" {
		fullURL += "?" + enc
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if tok := strings.TrimSpace(c.cfg.ReadToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}

	// 2. Persist raw JSON into provider_cache.
	if cacheKey != "" && ttl > 0 && c.db != nil {
		now := time.Now().UTC()
		_ = c.db.Gorm.Save(&model.ProviderCache{
			Provider:    "tmdb",
			CacheKey:    cacheKey,
			PayloadJSON: string(body),
			ExpiresAt:   now.Add(ttl),
			UpdatedAt:   now,
		}).Error
	}
	return nil
}
