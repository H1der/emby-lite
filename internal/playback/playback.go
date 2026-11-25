package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"emby-lite/internal/auth"
	"emby-lite/internal/config"
	"emby-lite/internal/database"
	"emby-lite/internal/model"

	"github.com/google/uuid"
)

// Resolver is the pluggable Fast Path / 302 redirect hook for remote/STRM sources.
type Resolver interface {
	Resolve(ctx context.Context, r *http.Request, src *model.MediaSource) (targetURL string, handled bool, err error)
}

// DirectResolver returns the raw STRM HTTP/HTTPS URL directly.
type DirectResolver struct{}

func (DirectResolver) Resolve(_ context.Context, _ *http.Request, src *model.MediaSource) (string, bool, error) {
	u := strings.TrimSpace(src.StrmURL)
	if u == "" {
		return "", false, errors.New("empty strm target")
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, fmt.Errorf("invalid remote strm url: %s", u)
	}
	return u, true, nil
}

type Service struct {
	db        *database.DB
	auth      *auth.Service
	cfg       *config.Config
	resolvers []Resolver
}

func NewService(db *database.DB, authSvc *auth.Service, cfg *config.Config, customResolvers ...Resolver) *Service {
	resolvers := append([]Resolver{}, customResolvers...)
	resolvers = append(resolvers, DirectResolver{})
	return &Service{
		db:        db,
		auth:      authSvc,
		cfg:       cfg,
		resolvers: resolvers,
	}
}

// BuildMediaSources builds Emby-compatible MediaSources DTO list for an Item.
func (s *Service) BuildMediaSources(r *http.Request, p *auth.Principal, item *model.Item) []map[string]any {
	var sources []model.MediaSource
	if err := s.db.Gorm.Where("item_id = ?", item.ID).Order("id ASC").Find(&sources).Error; err != nil || len(sources) == 0 {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		ext := src.Container
		if ext == "" {
			ext = "mkv"
		}
		relURL := fmt.Sprintf("/Videos/%s/stream.%s?Static=true&MediaSourceId=%s", item.ID, ext, url.QueryEscape(src.ID))
		if p != nil && p.Token != "" {
			relURL += "&api_key=" + url.QueryEscape(p.Token)
		}

		// Collect embedded + external subtitles.
		streams := []map[string]any{}
		if src.ProbeJSON != "" && src.ProbeJSON != "null" {
			var parsed []map[string]any
			if json.Unmarshal([]byte(src.ProbeJSON), &parsed) == nil {
				streams = append(streams, parsed...)
			}
		}

		var subs []model.MediaSubtitle
		_ = s.db.Gorm.Where("media_source_id = ?", src.ID).Order("stream_index ASC").Find(&subs).Error
		for _, sub := range subs {
			subURL := fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.%s", item.ID, src.ID, sub.StreamIndex, sub.Codec)
			if p != nil && p.Token != "" {
				subURL += "?api_key=" + url.QueryEscape(p.Token)
			}
			streams = append(streams, map[string]any{
				"Type":                  "Subtitle",
				"Index":                 sub.StreamIndex,
				"Codec":                 sub.Codec,
				"Language":              sub.Language,
				"DisplayTitle":          sub.Title,
				"Title":                 sub.Title,
				"IsExternal":            sub.IsExternal,
				"IsDefault":             sub.IsDefault,
				"IsForced":              sub.IsForced,
				"SupportsExternalStream": true,
				"DeliveryMethod":        "External",
				"DeliveryUrl":           subURL,
			})
		}

		name := item.Name
		if src.VersionLabel != "" {
			name = item.Name + " - " + src.VersionLabel
		}

		runtimeTicks := item.RuntimeTicks
		if runtimeTicks == 0 && src.DurationSecs > 0 {
			runtimeTicks = int64(src.DurationSecs * 1e7)
		}

		pathVal := PublicOrigin(r, s.cfg.Server.PublicURL) + "/emby" + relURL
		if p != nil && p.IsAPIKey {
			if src.SourceType == "strm" && src.StrmURL != "" {
				pathVal = src.StrmURL
			} else {
				pathVal = src.Path
			}
		}

		ms := map[string]any{
			"Id":                   src.ID,
			"Name":                 name,
			"Protocol":             "Http",
			"Type":                 "Default",
			"Container":            ext,
			"Size":                 src.FileSize,
			"Path":                 pathVal,
			"DirectStreamUrl":      relURL,
			"SupportsDirectPlay":   true,
			"SupportsDirectStream": true,
			"SupportsTranscoding":  false,
			"IsRemote":             src.SourceType == "strm",
			"ReadAtNativeFramerate": false,
			"MediaStreams":         streams,
		}
		if runtimeTicks > 0 {
			ms["RunTimeTicks"] = runtimeTicks
		}
		if src.Bitrate > 0 {
			ms["Bitrate"] = src.Bitrate
		}
		out = append(out, ms)
	}
	return out
}

// BuildPlaybackInfo builds the full Emby PlaybackInfo JSON payload.
func (s *Service) BuildPlaybackInfo(r *http.Request, p *auth.Principal, item *model.Item) map[string]any {
	playSessionID := strings.ReplaceAll(uuid.NewString(), "-", "")
	return map[string]any{
		"MediaSources":  s.BuildMediaSources(r, p, item),
		"PlaySessionId": playSessionID,
	}
}

// Stream serves either a local file (HTTP Range/206) or a 302 redirect for STRM sources.
func (s *Service) Stream(w http.ResponseWriter, r *http.Request, p *auth.Principal, item *model.Item, mediaSourceID string, asDownload bool) {
	if p == nil || !p.Policy.AllowPlayback {
		writeErr(w, http.StatusForbidden, "playback is not permitted for this user")
		return
	}
	if asDownload && !p.Policy.AllowDownload {
		writeErr(w, http.StatusForbidden, "media download is disabled for this user")
		return
	}
	if !s.auth.CanAccessLibrary(p, item.LibraryID) {
		writeErr(w, http.StatusNotFound, "item not found")
		return
	}

	var src model.MediaSource
	q := s.db.Gorm.Where("item_id = ?", item.ID)
	if mediaSourceID != "" {
		q = q.Where("id = ?", mediaSourceID)
	}
	if err := q.Order("id ASC").First(&src).Error; err != nil {
		writeErr(w, http.StatusNotFound, "media source not found")
		return
	}

	// Enforce concurrent stream lease for non-download playback.
	if !asDownload {
		playSessionID := r.URL.Query().Get("PlaySessionId")
		if err := s.auth.AcquireOrRenewLease(p, playSessionID, item.ID, src.ID, 0, false); err != nil {
			if errors.Is(err, auth.ErrDeviceLimitReached) {
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	// STRM / remote redirect branch.
	if src.SourceType == "strm" {
		target := strings.TrimSpace(src.StrmURL)
		// Support local-path STRM (e.g. cross-disk local STRM referencing an absolute file path).
		if !strings.HasPrefix(strings.ToLower(target), "http://") && !strings.HasPrefix(strings.ToLower(target), "https://") {
			if target != "" && s.isAllowedPath(target) {
				s.serveLocalFile(w, r, target, asDownload)
				return
			}
		}
		for _, res := range s.resolvers {
			u, handled, err := res.Resolve(r.Context(), r, &src)
			if err != nil {
				writeErr(w, http.StatusBadGateway, "failed to resolve strm source: "+err.Error())
				return
			}
			if handled && u != "" {
				w.Header().Set("Location", u)
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusFound)
				return
			}
		}
		writeErr(w, http.StatusUnprocessableEntity, "invalid strm target")
		return
	}

	// Local real file direct stream.
	s.serveLocalFile(w, r, src.Path, asDownload)
}

func (s *Service) serveLocalFile(w http.ResponseWriter, r *http.Request, rawPath string, asDownload bool) {
	realPath, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "media file missing")
		return
	}
	if !s.isAllowedPath(realPath) {
		writeErr(w, http.StatusForbidden, "media path outside configured library roots")
		return
	}
	f, err := os.Open(realPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "unable to open media file")
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, "invalid media file")
		return
	}

	if ct := mediaContentType(realPath); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if asDownload {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(realPath)))
	} else {
		w.Header().Set("Content-Disposition", "inline")
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// isAllowedPath verifies target lies inside either AllowedMediaRoots or a registered Library path.
func (s *Service) isAllowedPath(target string) bool {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}

	var roots []string
	roots = append(roots, s.cfg.Server.AllowedMediaRoots...)

	var libs []model.Library
	if err := s.db.Gorm.Select("paths_json").Find(&libs).Error; err == nil {
		for _, l := range libs {
			var lp []string
			if json.Unmarshal([]byte(l.PathsJSON), &lp) == nil {
				roots = append(roots, lp...)
			}
		}
	}

	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if resolvedRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
			absRoot = resolvedRoot
		}
		rel, err := filepath.Rel(absRoot, absTarget)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func PublicOrigin(r *http.Request, configuredPublicURL string) string {
	if configuredPublicURL != "" {
		return strings.TrimRight(configuredPublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fp := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); fp == "https" || fp == "http" {
		scheme = fp
	}
	host := r.Host
	if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" && !strings.ContainsAny(fh, "/\\?#@ \t\r\n") {
		host = fh
	}
	return scheme + "://" + host
}

func mediaContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".ts", ".m2ts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   msg,
		"Message": msg,
	})
}
