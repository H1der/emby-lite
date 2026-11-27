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
	"time"

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
	if item == nil {
		return []map[string]any{}
	}
	res := s.BuildMediaSourcesBatch(r, p, []model.Item{*item})
	if out, ok := res[item.ID]; ok {
		return out
	}
	return []map[string]any{}
}

// BuildMediaSourcesBatch builds Emby-compatible MediaSources DTOs for multiple items in 2 batched queries.
func (s *Service) BuildMediaSourcesBatch(r *http.Request, p *auth.Principal, items []model.Item) map[string][]map[string]any {
	result := make(map[string][]map[string]any, len(items))
	if len(items) == 0 {
		return result
	}

	itemByID := make(map[string]*model.Item, len(items))
	itemIDs := make([]string, 0, len(items))
	for i := range items {
		itemByID[items[i].ID] = &items[i]
		itemIDs = append(itemIDs, items[i].ID)
	}

	var sources []model.MediaSource
	if err := s.db.Read().Where("item_id IN ?", itemIDs).Order("height DESC, bitrate DESC, file_size DESC, id ASC").Find(&sources).Error; err != nil || len(sources) == 0 {
		return result
	}

	sourceCountByItem := make(map[string]int, len(items))
	sourceIDs := make([]string, 0, len(sources))
	for _, src := range sources {
		sourceIDs = append(sourceIDs, src.ID)
		sourceCountByItem[src.ItemID]++
	}

	subsBySource := make(map[string][]model.MediaSubtitle, len(sourceIDs))
	var allSubs []model.MediaSubtitle
	if err := s.db.Read().Where("media_source_id IN ?", sourceIDs).Order("stream_index ASC").Find(&allSubs).Error; err == nil {
		for _, sub := range allSubs {
			subsBySource[sub.MediaSourceID] = append(subsBySource[sub.MediaSourceID], sub)
		}
	}

	seriesNamesByItem := s.resolveEpisodeSeriesNames(items)
	for _, src := range sources {
		item := itemByID[src.ItemID]
		if item == nil {
			continue
		}
		ext := src.Container
		if ext == "" {
			ext = "mkv"
		}
		relURL := fmt.Sprintf("/Videos/%s/stream.%s?Static=true&MediaSourceId=%s", item.ID, ext, url.QueryEscape(src.ID))
		if p != nil && p.Token != "" {
			relURL += "&api_key=" + url.QueryEscape(p.Token)
		}

		pathVal, msProto, isRemote := resolveMediaSourceDisplayPath(&src)
		streamProto := msProto

		// Collect embedded + external subtitles.
		streams := []map[string]any{}
		if src.ProbeJSON != "" && src.ProbeJSON != "null" {
			var parsed []map[string]any
			if json.Unmarshal([]byte(src.ProbeJSON), &parsed) == nil {
				for _, st := range parsed {
					if st == nil {
						continue
					}
					streams = append(streams, normalizeMediaStream(st, len(streams), streamProto))
				}
			}
		}

		for _, sub := range subsBySource[src.ID] {
			subURL := fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/Stream.%s", item.ID, src.ID, sub.StreamIndex, sub.Codec)
			if p != nil && p.Token != "" {
				subURL += "?api_key=" + url.QueryEscape(p.Token)
			}
			subStream := map[string]any{
				"Type":                   "Subtitle",
				"Index":                  sub.StreamIndex,
				"Codec":                  sub.Codec,
				"Language":               sub.Language,
				"DisplayLanguage":        sub.Language,
				"DisplayTitle":           sub.Title,
				"Title":                  sub.Title,
				"Protocol":               "File",
				"IsExternal":             sub.IsExternal,
				"IsExternalUrl":          false,
				"IsInterlaced":           false,
				"IsHearingImpaired":      false,
				"IsTextSubtitleStream":   isTextSubtitleCodec(sub.Codec, sub.IsExternal),
				"IsDefault":              sub.IsDefault,
				"IsForced":               sub.IsForced,
				"SupportsExternalStream": true,
				"DeliveryMethod":         "External",
				"DeliveryUrl":            subURL,
			}
			if strings.TrimSpace(sub.Title) == "" {
				subStream["DisplayTitle"] = buildMediaStreamDisplayTitle(subStream)
			}
			streams = append(streams, subStream)
		}

		name := formatMediaSourceName(item, seriesNamesByItem[item.ID], &src, sourceCountByItem[item.ID])

		runtimeTicks := item.RuntimeTicks
		if runtimeTicks == 0 && src.DurationSecs > 0 {
			runtimeTicks = int64(src.DurationSecs * 1e7)
		}

		ms := map[string]any{
			"Id":                         src.ID,
			"ItemId":                     item.ID,
			"Name":                       name,
			"Protocol":                   msProto,
			"Type":                       "Default",
			"Container":                  ext,
			"Path":                       pathVal,
			"DirectStreamUrl":            relURL,
			"SupportsDirectPlay":         true,
			"SupportsDirectStream":       true,
			"SupportsTranscoding":        false,
			"SupportsProbing":            true,
			"IsRemote":                   isRemote,
			"IsInfiniteStream":           false,
			"RequiresOpening":            false,
			"RequiresClosing":            false,
			"RequiresLooping":            false,
			"HasSegments":                false,
			"IgnoreDts":                  false,
			"IgnoreIndex":                false,
			"GenPtsInput":                false,
			"ReadAtNativeFramerate":      false,
			"AddApiKeyToDirectStreamUrl": false,
			"Formats":                    []string{},
			"RequiredHttpHeaders":        map[string]string{},
			"MediaAttachments":           []any{},
			"MediaStreams":               streams,
		}
		if src.FileSize > 0 {
			ms["Size"] = src.FileSize
		}
		// Advertise default audio / subtitle stream indices when available.
		defaultAudioIdx := -1
		firstAudioIdx := -1
		defaultSubIdx := -1
		for _, st := range streams {
			stType, _ := st["Type"].(string)
			idxVal := streamIndexInt(st["Index"])
			isDef, _ := st["IsDefault"].(bool)
			if strings.EqualFold(stType, "Audio") && idxVal >= 0 {
				if firstAudioIdx < 0 {
					firstAudioIdx = idxVal
				}
				if isDef && defaultAudioIdx < 0 {
					defaultAudioIdx = idxVal
				}
			}
			if strings.EqualFold(stType, "Subtitle") && idxVal >= 0 && isDef && defaultSubIdx < 0 {
				defaultSubIdx = idxVal
			}
		}
		if defaultAudioIdx >= 0 {
			ms["DefaultAudioStreamIndex"] = defaultAudioIdx
		} else if firstAudioIdx >= 0 {
			ms["DefaultAudioStreamIndex"] = firstAudioIdx
		}
		if defaultSubIdx >= 0 {
			ms["DefaultSubtitleStreamIndex"] = defaultSubIdx
		}
		if src.Width > 0 {
			ms["Width"] = src.Width
		}
		if src.Height > 0 {
			ms["Height"] = src.Height
		}
		if src.VideoCodec != "" {
			ms["VideoCodec"] = src.VideoCodec
		}
		if src.HDRType != "" {
			ms["HDRType"] = src.HDRType
		}
		if runtimeTicks > 0 {
			ms["RunTimeTicks"] = runtimeTicks
		}
		if src.Bitrate > 0 {
			ms["Bitrate"] = src.Bitrate
		}
		result[item.ID] = append(result[item.ID], ms)
	}
	return result
}

func resolveMediaSourceDisplayPath(src *model.MediaSource) (pathVal, protocol string, isRemote bool) {
	if src == nil {
		return "", "File", false
	}
	if src.SourceType == "strm" {
		target := strings.TrimSpace(src.StrmURL)
		if target != "" {
			lower := strings.ToLower(target)
			if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
				return target, "Http", true
			}
			if strings.HasPrefix(lower, "rtsp://") {
				return target, "Rtsp", true
			}
			if strings.HasPrefix(lower, "rtmp://") {
				return target, "Rtmp", true
			}
			if strings.HasPrefix(lower, "ftp://") {
				return target, "Ftp", true
			}
			return target, "File", false
		}
	}
	return src.Path, "File", false
}

func (s *Service) resolveEpisodeSeriesNames(items []model.Item) map[string]string {
	epItems := make([]*model.Item, 0)
	parentIDSet := make(map[string]bool)
	for i := range items {
		if items[i].Type != "Episode" {
			continue
		}
		epItems = append(epItems, &items[i])
		if items[i].SeriesID != "" {
			parentIDSet[items[i].SeriesID] = true
		}
		if items[i].SeasonID != "" {
			parentIDSet[items[i].SeasonID] = true
		}
		if items[i].ParentID != "" {
			parentIDSet[items[i].ParentID] = true
		}
	}
	if len(epItems) == 0 {
		return nil
	}

	type parentRow struct {
		ID            string
		Type          string
		Name          string
		SeriesID      string
		ParentID      string
		SoftDeletedAt *time.Time
	}
	rowsByID := make(map[string]parentRow, len(parentIDSet))
	load := func(idSet map[string]bool) {
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			if id != "" {
				if _, ok := rowsByID[id]; !ok {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			return
		}
		var rows []parentRow
		_ = s.db.Read().Table("items").
			Select("id, type, name, series_id, parent_id, soft_deleted_at").
			Where("id IN ?", ids).
			Scan(&rows).Error
		for _, r := range rows {
			rowsByID[r.ID] = r
		}
	}
	load(parentIDSet)

	secondHop := make(map[string]bool)
	for _, r := range rowsByID {
		if r.SeriesID != "" {
			if _, ok := rowsByID[r.SeriesID]; !ok {
				secondHop[r.SeriesID] = true
			}
		}
		if r.ParentID != "" && r.Type != "Series" {
			if _, ok := rowsByID[r.ParentID]; !ok {
				secondHop[r.ParentID] = true
			}
		}
	}
	load(secondHop)

	out := make(map[string]string, len(epItems))
	for _, it := range epItems {
		candidates := make([]string, 0, 4)
		addCand := func(id string) {
			if id == "" {
				return
			}
			for _, c := range candidates {
				if c == id {
					return
				}
			}
			candidates = append(candidates, id)
		}
		addCand(it.SeriesID)
		if it.SeasonID != "" {
			if sr, ok := rowsByID[it.SeasonID]; ok {
				addCand(sr.SeriesID)
				if sr.ParentID != "" {
					if gr, ok := rowsByID[sr.ParentID]; ok && gr.Type == "Series" {
						addCand(gr.ID)
					}
				}
			}
		}
		if it.ParentID != "" {
			if pr, ok := rowsByID[it.ParentID]; ok {
				if pr.Type == "Series" {
					addCand(pr.ID)
				} else {
					addCand(pr.SeriesID)
					if pr.ParentID != "" {
						if gr, ok := rowsByID[pr.ParentID]; ok && gr.Type == "Series" {
							addCand(gr.ID)
						}
					}
				}
			}
		}

		name := ""
		for _, cid := range candidates {
			if r, ok := rowsByID[cid]; ok && r.SoftDeletedAt == nil && r.Type == "Series" && strings.TrimSpace(r.Name) != "" {
				name = strings.TrimSpace(r.Name)
				break
			}
		}
		if name == "" {
			for _, cid := range candidates {
				if r, ok := rowsByID[cid]; ok && r.SoftDeletedAt == nil && r.Type != "Season" && strings.TrimSpace(r.Name) != "" {
					name = strings.TrimSpace(r.Name)
					break
				}
			}
		}
		if name == "" {
			for _, cid := range candidates {
				if r, ok := rowsByID[cid]; ok && r.Type == "Series" && strings.TrimSpace(r.Name) != "" {
					name = strings.TrimSpace(r.Name)
					break
				}
			}
		}
		if name != "" {
			out[it.ID] = name
		}
	}
	return out
}

func formatMediaSourceName(item *model.Item, seriesName string, src *model.MediaSource, sourceCount int) string {
	ver := strings.TrimSpace(src.VersionLabel)
	if ver == "" && sourceCount > 1 {
		ver = strings.TrimSpace(fallbackVersionLabel(src))
	}

	if item.Type == "Episode" {
		seCode := ""
		if item.EpisodeNum > 0 {
			seasonNum := item.SeasonNum
			if seasonNum < 0 {
				seasonNum = 0
			}
			seCode = fmt.Sprintf("S%02dE%02d", seasonNum, item.EpisodeNum)
		}
		base := strings.TrimSpace(seriesName)
		switch {
		case base != "" && seCode != "":
			base = base + " " + seCode
		case base == "" && seCode != "":
			if epName := strings.TrimSpace(item.Name); epName != "" {
				base = seCode + " " + epName
			} else {
				base = seCode
			}
		case base == "":
			base = strings.TrimSpace(item.Name)
		}
		if ver != "" {
			if base != "" {
				return base + " " + ver
			}
			return ver
		}
		if base != "" {
			return base
		}
		return item.Name
	}

	if ver != "" {
		return item.Name + " - " + ver
	}
	return item.Name
}

func normalizeMediaStream(st map[string]any, fallbackIndex int, defaultProtocol string) map[string]any {
	streamType, _ := st["Type"].(string)
	if strings.TrimSpace(streamType) == "" {
		streamType = "Unknown"
		st["Type"] = streamType
	}
	if idx := streamIndexInt(st["Index"]); idx >= 0 {
		st["Index"] = idx
	} else {
		st["Index"] = fallbackIndex
	}
	if _, ok := st["IsDefault"].(bool); !ok {
		st["IsDefault"] = false
	}
	if _, ok := st["IsForced"].(bool); !ok {
		st["IsForced"] = false
	}
	isExternal, ok := st["IsExternal"].(bool)
	if !ok {
		isExternal = false
		st["IsExternal"] = false
	}
	if _, ok := st["IsExternalUrl"].(bool); !ok {
		st["IsExternalUrl"] = false
	}
	if _, ok := st["IsInterlaced"].(bool); !ok {
		st["IsInterlaced"] = false
	}
	if _, ok := st["IsHearingImpaired"].(bool); !ok {
		st["IsHearingImpaired"] = false
	}
	if _, ok := st["IsTextSubtitleStream"].(bool); !ok {
		if strings.EqualFold(streamType, "Subtitle") {
			codec, _ := st["Codec"].(string)
			st["IsTextSubtitleStream"] = isTextSubtitleCodec(codec, isExternal)
		} else {
			st["IsTextSubtitleStream"] = false
		}
	}
	if _, ok := st["SupportsExternalStream"].(bool); !ok {
		st["SupportsExternalStream"] = isExternal
	}
	if proto, _ := st["Protocol"].(string); strings.TrimSpace(proto) == "" {
		if defaultProtocol == "" {
			defaultProtocol = "File"
		}
		st["Protocol"] = defaultProtocol
	}
	// Strip empty placeholder strings emitted by older migrations.
	if tb, ok := st["TimeBase"].(string); ok && strings.TrimSpace(tb) == "" {
		delete(st, "TimeBase")
	}
	if ct, ok := st["CodecTag"].(string); ok && strings.TrimSpace(ct) == "" {
		delete(st, "CodecTag")
	}
	// Normalize legacy lowercase typo key ExtendedVideoSubtype -> Emby OpenAPI ExtendedVideoSubType.
	if legacySub, ok := st["ExtendedVideoSubtype"]; ok {
		if _, hasCanonical := st["ExtendedVideoSubType"]; !hasCanonical {
			if s, ok := legacySub.(string); ok && strings.TrimSpace(s) != "" {
				st["ExtendedVideoSubType"] = s
			}
		}
		delete(st, "ExtendedVideoSubtype")
	}
	if strings.EqualFold(streamType, "Video") {
		if evt, _ := st["ExtendedVideoType"].(string); strings.TrimSpace(evt) == "" {
			vr, _ := st["VideoRange"].(string)
			st["ExtendedVideoType"] = mapVideoRangeToExtendedType(vr)
		}
	}
	if strings.EqualFold(streamType, "Subtitle") && !isExternal {
		if dm, _ := st["DeliveryMethod"].(string); strings.TrimSpace(dm) == "" {
			st["DeliveryMethod"] = "Embed"
		}
	}
	if dt, _ := st["DisplayTitle"].(string); strings.TrimSpace(dt) == "" {
		if generated := buildMediaStreamDisplayTitle(st); generated != "" {
			st["DisplayTitle"] = generated
		}
	}
	return st
}

func mapVideoRangeToExtendedType(videoRange string) string {
	switch strings.ToUpper(strings.TrimSpace(videoRange)) {
	case "HDR10+", "HDR10PLUS":
		return "Hdr10Plus"
	case "HDR10", "HDR":
		return "Hdr10"
	case "HLG", "HYPERLOGGAMMA":
		return "HyperLogGamma"
	case "DOVI", "DV", "DOLBY VISION", "DOLBYVISION":
		return "DolbyVision"
	default:
		return "None"
	}
}

func buildMediaStreamDisplayTitle(st map[string]any) string {
	streamType, _ := st["Type"].(string)
	codec, _ := st["Codec"].(string)
	lang, _ := st["Language"].(string)
	title, _ := st["Title"].(string)
	isDefault, _ := st["IsDefault"].(bool)
	isForced, _ := st["IsForced"].(bool)
	isExternal, _ := st["IsExternal"].(bool)
	isInterlaced, _ := st["IsInterlaced"].(bool)

	var parts []string
	switch strings.ToLower(strings.TrimSpace(streamType)) {
	case "video":
		w := streamIndexInt(st["Width"])
		h := streamIndexInt(st["Height"])
		suffix := "p"
		if isInterlaced {
			suffix = "i"
		}
		switch {
		case w >= 7600 || h >= 4200:
			parts = append(parts, "8K")
		case w >= 3800 || h >= 2100:
			parts = append(parts, "4K")
		case w >= 2500 || h >= 1400:
			parts = append(parts, "1440"+suffix)
		case w >= 1900 || h >= 1000:
			parts = append(parts, "1080"+suffix)
		case w >= 1200 || h >= 700:
			parts = append(parts, "720"+suffix)
		case h >= 400:
			parts = append(parts, "480"+suffix)
		}
		if c := strings.ToUpper(strings.TrimSpace(codec)); c != "" {
			parts = append(parts, c)
		}
		if vr := strings.TrimSpace(fmt.Sprint(st["VideoRange"])); vr != "" && vr != "<nil>" && !strings.EqualFold(vr, "SDR") && !strings.EqualFold(vr, "Unknown") {
			if strings.EqualFold(vr, "DOVI") || strings.EqualFold(vr, "DV") {
				parts = append(parts, "Dolby Vision")
			} else {
				parts = append(parts, strings.ToUpper(vr))
			}
		} else if evt, _ := st["ExtendedVideoType"].(string); evt != "" && !strings.EqualFold(evt, "None") {
			parts = append(parts, evt)
		}
		res := strings.Join(parts, " ")
		if t := strings.TrimSpace(title); t != "" {
			if res == "" {
				return t
			}
			return t + " - " + res
		}
		return res
	case "audio":
		if l := strings.TrimSpace(lang); l != "" && !strings.EqualFold(l, "und") {
			parts = append(parts, strings.ToUpper(l))
		}
		if c := strings.ToUpper(strings.TrimSpace(codec)); c != "" {
			parts = append(parts, c)
		}
		if cl, _ := st["ChannelLayout"].(string); strings.TrimSpace(cl) != "" {
			parts = append(parts, strings.TrimSpace(cl))
		} else if ch := streamIndexInt(st["Channels"]); ch > 0 {
			switch ch {
			case 2:
				parts = append(parts, "stereo")
			case 6:
				parts = append(parts, "5.1")
			case 8:
				parts = append(parts, "7.1")
			default:
				parts = append(parts, fmt.Sprintf("%d ch", ch))
			}
		}
		if isDefault {
			parts = append(parts, "Default")
		}
		res := strings.Join(parts, " - ")
		if t := strings.TrimSpace(title); t != "" {
			if res == "" {
				return t
			}
			return t + " - " + res
		}
		return res
	case "subtitle":
		if t := strings.TrimSpace(title); t != "" {
			parts = append(parts, t)
		} else if l := strings.TrimSpace(lang); l != "" {
			parts = append(parts, strings.ToUpper(l))
		} else {
			parts = append(parts, "Und")
		}
		if c := strings.ToUpper(strings.TrimSpace(codec)); c != "" {
			parts = append(parts, c)
		}
		if isDefault {
			parts = append(parts, "Default")
		}
		if isForced {
			parts = append(parts, "Forced")
		}
		if isExternal {
			parts = append(parts, "External")
		}
		return strings.Join(parts, " - ")
	default:
		return strings.TrimSpace(title)
	}
}

func isTextSubtitleCodec(codec string, isExternal bool) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	if c == "" {
		return isExternal
	}
	if strings.Contains(c, "pgs") ||
		strings.Contains(c, "dvdsub") ||
		strings.Contains(c, "dvd_subtitle") ||
		strings.Contains(c, "vobsub") ||
		strings.Contains(c, "dvbsub") ||
		strings.Contains(c, "dvb_subtitle") ||
		strings.Contains(c, "xsub") ||
		c == "sup" ||
		c == "sub" {
		return false
	}
	return true
}

func streamIndexInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return -1
	}
}

// BuildPlaybackInfo builds the full Emby PlaybackInfo JSON payload.
func (s *Service) BuildPlaybackInfo(r *http.Request, p *auth.Principal, item *model.Item) map[string]any {
	return s.BuildPlaybackInfoWithSource(r, p, item, "")
}

// BuildPlaybackInfoWithSource builds Emby PlaybackInfo, prioritizing preferredSourceID when specified.
func (s *Service) BuildPlaybackInfoWithSource(r *http.Request, p *auth.Principal, item *model.Item, preferredSourceID string) map[string]any {
	playSessionID := strings.ReplaceAll(uuid.NewString(), "-", "")
	sources := s.BuildMediaSources(r, p, item)
	if preferredSourceID != "" && len(sources) > 1 {
		for i, src := range sources {
			if id, _ := src["Id"].(string); id == preferredSourceID && i > 0 {
				reordered := make([]map[string]any, 0, len(sources))
				reordered = append(reordered, sources[i])
				reordered = append(reordered, sources[:i]...)
				reordered = append(reordered, sources[i+1:]...)
				sources = reordered
				break
			}
		}
	}
	return map[string]any{
		"MediaSources":  sources,
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
	if err := q.Order("height DESC, bitrate DESC, file_size DESC, id ASC").First(&src).Error; err != nil {
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

func fallbackVersionLabel(src *model.MediaSource) string {
	if src == nil {
		return ""
	}
	var parts []string
	if src.Width >= 3800 || src.Height >= 2100 {
		parts = append(parts, "4K")
	} else if src.Width >= 1900 || src.Height >= 1000 {
		parts = append(parts, "1080p")
	} else if src.Width >= 1200 || src.Height >= 700 {
		parts = append(parts, "720p")
	}
	if src.HDRType != "" && !strings.EqualFold(src.HDRType, "SDR") {
		parts = append(parts, strings.ToUpper(src.HDRType))
	}
	if src.VideoCodec != "" {
		parts = append(parts, strings.ToUpper(src.VideoCodec))
	}
	if src.FileSize > 0 {
		gb := float64(src.FileSize) / (1024 * 1024 * 1024)
		if gb >= 1 {
			parts = append(parts, fmt.Sprintf("%.1fGB", gb))
		} else {
			parts = append(parts, fmt.Sprintf("%.0fMB", float64(src.FileSize)/(1024*1024)))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	if src.Path != "" {
		base := filepath.Base(src.Path)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return ""
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   msg,
		"Message": msg,
	})
}
