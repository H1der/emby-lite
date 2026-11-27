package migration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"emby-lite/internal/model"
	"gorm.io/gorm/clause"
)

// each image as "path*etag*type*width*height*..." and entries separated by '|'.
type embyImageRow struct {
	Path   string
	Type   string
	Width  int
	Height int
}

type embyChapterRow struct {
	IndexNum   int
	StartTicks int64
	Name       string
	MarkerType int
	ImagePath  string
}

type embyStreamRow struct {
	Index             int
	StreamType        int
	Codec             string
	Language          string
	ChannelLayout     string
	Profile           string
	AspectRatio       string
	Path              string
	PixelFormat       string
	CodecTag          string
	Comment           string
	Title             string
	MimeType          string
	ExtendedVideoType string
	ExtendedVideoSub  string
	BitRate           int64
	Channels          int
	SampleRate        int
	Width             int
	Height            int
	BitDepth          int
	RefFrames         int
	Rotation          int
	AttachmentSize    int64
	IsDefault         bool
	IsForced          bool
	IsHearingImpaired bool
	IsExternal        bool
	IsInterlaced      bool
	IsAnamorphic      bool
	AverageFrameRate  float64
	RealFrameRate     float64
}

type embyBoxSetRow struct {
	EmbyID          string
	Name            string
	SortName        string
	Overview        string
	ProviderIDsJSON string
}

type embyExtras struct {
	tags        map[string][]string
	chapters    map[string][]embyChapterRow
	streams     map[string][]embyStreamRow
	images      map[string][]embyImageRow
	boxSets     map[string]embyBoxSetRow
	boxSetItems map[string][]string // box set item id -> member item ids
}

// embyLibraryOptions is the subset of Emby's LibraryOptions extradata that
// decides how a collection folder is presented and scanned.
type embyLibraryOptions struct {
	ContentType                   string
	MinCollectionItems            int
	EnableAutomaticSeriesGrouping *bool
}

// readEmbyLibraryOptions reads the ContentType of every library from
// ItemExtradata. Emby 4.8+ keeps library options in the database instead of
// root/default/<library>/options.xml, so this is the only way to recognise a
// boxsets library when the Emby config directory is not available.
func readEmbyLibraryOptions(sqlDB *sql.DB) map[string]embyLibraryOptions {
	out := map[string]embyLibraryOptions{}
	if len(tableColumns(sqlDB, "ItemExtradata")) == 0 || !tableColumns(sqlDB, "ItemExtradata")["extradatatypeid"] {
		return out
	}
	q := `SELECT CAST(e.ItemId AS TEXT), COALESCE(CAST(e.Value AS TEXT), '')
		FROM ItemExtradata e WHERE e.ExtradataTypeId = 2`
	if len(tableColumns(sqlDB, "ItemExtradataTypes")) > 0 {
		q = `SELECT CAST(e.ItemId AS TEXT), COALESCE(CAST(e.Value AS TEXT), '')
			FROM ItemExtradata e
			JOIN ItemExtradataTypes t ON t.ExtradataTypeId = e.ExtradataTypeId
			WHERE LOWER(t.Name) = 'libraryoptions'`
	}
	rows, err := sqlDB.Query(q)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var itemID, raw string
		if rows.Scan(&itemID, &raw) != nil || strings.TrimSpace(raw) == "" {
			continue
		}
		var payload struct {
			ContentType                   string `json:"ContentType"`
			MinCollectionItems            int    `json:"MinCollectionItems"`
			EnableAutomaticSeriesGrouping *bool  `json:"EnableAutomaticSeriesGrouping"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil {
			out[itemID] = embyLibraryOptions{
				ContentType:                   payload.ContentType,
				MinCollectionItems:            payload.MinCollectionItems,
				EnableAutomaticSeriesGrouping: payload.EnableAutomaticSeriesGrouping,
			}
		}
	}
	return out
}

// readEmbyItemExtras reads the tables that carry metadata complementary to an
// item row. Every table is optional so older Emby schemas keep working.
func readEmbyItemExtras(sqlDB *sql.DB, table string, idCol string, enumKind bool) embyExtras {
	out := embyExtras{
		tags:        map[string][]string{},
		chapters:    map[string][]embyChapterRow{},
		streams:     map[string][]embyStreamRow{},
		images:      map[string][]embyImageRow{},
		boxSets:     map[string]embyBoxSetRow{},
		boxSetItems: map[string][]string{},
	}

	// Tags from ItemLinks2 type 4, with the legacy pipe column as a fallback.
	if len(tableColumns(sqlDB, "ItemLinks2")) > 0 {
		q := fmt.Sprintf(`SELECT CAST(l.ItemId AS TEXT), COALESCE(x.Name, '')
			FROM ItemLinks2 l JOIN %s x ON x.Id = l.LinkedId
			WHERE l.Type = 4 ORDER BY l.ItemId, l.LinkOrder`, table)
		if rows, err := sqlDB.Query(q); err == nil {
			for rows.Next() {
				var itemID, name string
				if rows.Scan(&itemID, &name) == nil && strings.TrimSpace(name) != "" {
					out.tags[itemID] = append(out.tags[itemID], strings.TrimSpace(name))
				}
			}
			_ = rows.Close()
		}
	}
	if len(tableColumns(sqlDB, table)) > 0 {
		cols := tableColumns(sqlDB, table)
		if cols["tags"] {
			q := fmt.Sprintf("SELECT CAST(%s AS TEXT), COALESCE(CAST(Tags AS TEXT), '') FROM %s", idCol, table)
			if rows, err := sqlDB.Query(q); err == nil {
				for rows.Next() {
					var itemID, raw string
					if rows.Scan(&itemID, &raw) == nil {
						if existing := out.tags[itemID]; len(existing) == 0 {
							out.tags[itemID] = splitPipe(raw)
						}
					}
				}
				_ = rows.Close()
			}
		}
		if cols["images"] {
			q := fmt.Sprintf("SELECT CAST(%s AS TEXT), COALESCE(CAST(Images AS TEXT), '') FROM %s", idCol, table)
			if rows, err := sqlDB.Query(q); err == nil {
				for rows.Next() {
					var itemID, raw string
					if rows.Scan(&itemID, &raw) == nil {
						out.images[itemID] = parseEmbyImages(raw)
					}
				}
				_ = rows.Close()
			}
		}
	}

	// Chapters3 (Emby 4.x).
	if len(tableColumns(sqlDB, "Chapters3")) > 0 {
		rows, err := sqlDB.Query(`SELECT CAST(ItemId AS TEXT), ChapterIndex, StartPositionTicks, COALESCE(Name, ''), COALESCE(MarkerType, 0), COALESCE(ImagePath, '')
			FROM Chapters3 ORDER BY ItemId, ChapterIndex`)
		if err == nil {
			for rows.Next() {
				var itemID string
				var c embyChapterRow
				if rows.Scan(&itemID, &c.IndexNum, &c.StartTicks, &c.Name, &c.MarkerType, &c.ImagePath) == nil {
					out.chapters[itemID] = append(out.chapters[itemID], c)
				}
			}
			_ = rows.Close()
		}
	}

	// MediaStreams2. Use SELECT * so newer/older column sets both work.
	if len(tableColumns(sqlDB, "MediaStreams2")) > 0 {
		if rows, err := sqlDB.Query(`SELECT * FROM MediaStreams2 ORDER BY ItemId, StreamIndex`); err == nil {
			names, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(names))
				ptrs := make([]any, len(names))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if rows.Scan(ptrs...) != nil {
					continue
				}
				get := func(name string) string {
					for i, n := range names {
						if strings.EqualFold(n, name) {
							if vals[i] == nil {
								return ""
							}
							switch v := vals[i].(type) {
							case []byte:
								return string(v)
							case string:
								return v
							default:
								return fmt.Sprint(v)
							}
						}
					}
					return ""
				}
				atoi := func(name string) int { n, _ := strconv.Atoi(get(name)); return n }
				atoi64 := func(name string) int64 { n, _ := strconv.ParseInt(get(name), 10, 64); return n }
				parseBool := func(name string) bool {
					v := strings.ToLower(strings.TrimSpace(get(name)))
					return v == "1" || v == "true" || v == "yes"
				}
				parseFloat := func(name string) float64 { f, _ := strconv.ParseFloat(get(name), 64); return f }
				itemID := get("ItemId")
				s := embyStreamRow{
					Index:             atoi("StreamIndex"),
					StreamType:        atoi("StreamType"),
					Codec:             get("Codec"),
					Language:          get("Language"),
					ChannelLayout:     get("ChannelLayout"),
					Profile:           get("Profile"),
					AspectRatio:       get("AspectRatio"),
					Path:              applyPathMappings(get("Path"), nil),
					PixelFormat:       get("PixelFormat"),
					CodecTag:          get("CodecTag"),
					Comment:           get("Comment"),
					Title:             get("Title"),
					MimeType:          get("MimeType"),
					ExtendedVideoType: get("ExtendedVideoType"),
					ExtendedVideoSub:  get("ExtendedVideoSubtype"),
					BitRate:           atoi64("BitRate"),
					Channels:          atoi("Channels"),
					SampleRate:        atoi("SampleRate"),
					Width:             atoi("Width"),
					Height:            atoi("Height"),
					BitDepth:          atoi("BitDepth"),
					RefFrames:         atoi("RefFrames"),
					Rotation:          atoi("Rotation"),
					AttachmentSize:    atoi64("AttachmentSize"),
					IsDefault:         parseBool("IsDefault"),
					IsForced:          parseBool("IsForced"),
					IsHearingImpaired: parseBool("IsHearingImpaired"),
					IsExternal:        parseBool("IsExternal"),
					IsInterlaced:      parseBool("IsInterlaced"),
					IsAnamorphic:      parseBool("IsAnamorphic"),
					AverageFrameRate:  parseFloat("AverageFrameRate"),
					RealFrameRate:     parseFloat("RealFrameRate"),
				}
				// Paths are already absolute here; applyPathMappings with nil is a
				// no-op, so mapping is done by the caller before writing subtitles.
				out.streams[itemID] = append(out.streams[itemID], s)
			}
			_ = rows.Close()
		}
	}

	// Box sets are MediaItems.type=9 and are linked to their members through
	// ItemLinks2.Type=6 (Collection).
	if len(tableColumns(sqlDB, "ItemLinks2")) > 0 {
		boxSetIDs := map[string]bool{}
		if rows, err := sqlDB.Query(`SELECT CAST(LinkedId AS TEXT), CAST(ItemId AS TEXT) FROM ItemLinks2 WHERE Type = 6 ORDER BY LinkedId, LinkOrder`); err == nil {
			for rows.Next() {
				var box, member string
				if rows.Scan(&box, &member) == nil {
					out.boxSetItems[box] = append(out.boxSetItems[box], member)
					boxSetIDs[box] = true
				}
			}
			_ = rows.Close()
		}
		typeCol := "type"
		cols := tableColumns(sqlDB, table)
		nameExpr := "COALESCE(CAST(Name AS TEXT), '')"
		sortExpr := "''"
		if cols["sortname"] {
			sortExpr = "COALESCE(CAST(SortName AS TEXT), '')"
		}
		overviewExpr := "''"
		if cols["overview"] {
			overviewExpr = "COALESCE(CAST(Overview AS TEXT), '')"
		}
		pidsExpr := "''"
		if cols["providerids"] {
			pidsExpr = "COALESCE(CAST(ProviderIds AS TEXT), '')"
		}
		for boxID := range boxSetIDs {
			var name, sortName, overview, pids string
			var rawType string
			q := fmt.Sprintf(`SELECT %s, %s, %s, %s, COALESCE(CAST(%s AS TEXT), '') FROM %s WHERE CAST(%s AS TEXT) = ?`, nameExpr, sortExpr, overviewExpr, pidsExpr, typeCol, table, idCol)
			if err := sqlDB.QueryRow(q, boxID).Scan(&name, &sortName, &overview, &pids, &rawType); err == nil {
				if classifyEmbyType(rawType, enumKind) == "BoxSet" || strings.EqualFold(strings.TrimSpace(rawType), "BoxSet") {
					out.boxSets[boxID] = embyBoxSetRow{EmbyID: boxID, Name: name, SortName: sortName, Overview: overview, ProviderIDsJSON: pids}
				}
			}
		}
	}
	return out
}

func parseEmbyImages(raw string) []embyImageRow {
	var out []embyImageRow
	for _, entry := range strings.Split(raw, "|") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "*")
		if len(parts) < 3 {
			continue
		}
		img := embyImageRow{Path: strings.TrimSpace(parts[0]), Type: strings.TrimSpace(parts[2])}
		if len(parts) > 3 {
			img.Width, _ = strconv.Atoi(parts[3])
		}
		if len(parts) > 4 {
			img.Height, _ = strconv.Atoi(parts[4])
		}
		if img.Path != "" && img.Type != "" {
			out = append(out, img)
		}
	}
	return out
}

// resolveEmbyMetadataPath expands Emby's %MetadataPath% placeholder against
// the old server's config directory so local artwork can be imported when the
// metadata folder is mounted and reachable.
func resolveEmbyMetadataPath(raw, configDir string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "%MetadataPath%") || strings.TrimSpace(configDir) == "" {
		return raw
	}
	rel := strings.TrimPrefix(raw, "%MetadataPath%")
	rel = strings.TrimLeft(rel, `/\\`)
	return filepath.Join(configDir, "metadata", rel)
}
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func mustJSONString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return mustJSON(v)
}

func firstEmbyStream(streams []embyStreamRow, streamType int) embyStreamRow {
	for _, s := range streams {
		if s.StreamType == streamType {
			return s
		}
	}
	return embyStreamRow{}
}

var embyStreamTypeNames = map[int]string{
	0: "Unknown", 1: "Audio", 2: "Video", 3: "Subtitle", 4: "EmbeddedImage", 5: "Attachment",
}

// embyProbeJSON converts Emby MediaStreams2 rows into the Emby-compatible stream
// maps that playback.BuildMediaSourcesBatch exposes as MediaStreams. External
// subtitles are stored separately in media_subtitles and skipped here.
func embyProbeJSON(streams []embyStreamRow, mappings []PathMapping) string {
	out := make([]map[string]any, 0, len(streams))
	for _, s := range streams {
		if s.StreamType == 3 && s.IsExternal {
			continue
		}
		m := map[string]any{
			"Type": embyStreamTypeNames[s.StreamType], "Index": s.Index, "Codec": s.Codec,
			"Language": s.Language, "IsExternal": false,
			"IsDefault": s.IsDefault, "IsForced": s.IsForced,
			"IsInterlaced": s.IsInterlaced, "IsHearingImpaired": false,
			"SupportsExternalStream": false,
		}
		if s.Title != "" {
			m["Title"] = s.Title
			m["DisplayTitle"] = s.Title
		} else if s.Language != "" {
			m["DisplayTitle"] = strings.ToUpper(s.Language)
		}
		if s.CodecTag != "" {
			m["CodecTag"] = s.CodecTag
		}
		if s.Profile != "" {
			m["Profile"] = s.Profile
		}
		if s.ChannelLayout != "" {
			m["ChannelLayout"] = s.ChannelLayout
		}
		if s.AspectRatio != "" {
			m["AspectRatio"] = s.AspectRatio
		}
		if s.PixelFormat != "" {
			m["PixelFormat"] = s.PixelFormat
		}
		if s.BitRate > 0 {
			m["BitRate"] = s.BitRate
		}
		if s.Channels > 0 {
			m["Channels"] = s.Channels
		}
		if s.SampleRate > 0 {
			m["SampleRate"] = s.SampleRate
		}
		if s.Width > 0 {
			m["Width"] = s.Width
		}
		if s.Height > 0 {
			m["Height"] = s.Height
		}
		if s.BitDepth > 0 {
			m["BitDepth"] = s.BitDepth
		}
		if s.RefFrames > 0 {
			m["RefFrames"] = s.RefFrames
		}
		if s.Rotation != 0 {
			m["Rotation"] = s.Rotation
		}
		if s.AttachmentSize > 0 {
			m["AttachmentSize"] = s.AttachmentSize
		}
		if s.MimeType != "" {
			m["MimeType"] = s.MimeType
		}
		if s.ExtendedVideoType != "" {
			m["ExtendedVideoType"] = s.ExtendedVideoType
		}
		if s.ExtendedVideoSub != "" {
			m["ExtendedVideoSubType"] = s.ExtendedVideoSub
		}
		if s.IsAnamorphic {
			m["IsAnamorphic"] = true
		}
		if s.AverageFrameRate > 0 {
			m["AverageFrameRate"] = s.AverageFrameRate
		}
		if s.RealFrameRate > 0 {
			m["RealFrameRate"] = s.RealFrameRate
		}
		if s.StreamType == 3 && s.Path != "" {
			if p := applyPathMappings(s.Path, mappings); p != "" {
				m["Path"] = p
			}
		}
		out = append(out, m)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// importUserSettings upserts the opaque Emby per-user settings blobs.
func (s *Service) importUserSettings(userID string, settings map[string]string, now time.Time) (int, error) {
	if userID == "" || len(settings) == 0 {
		return 0, nil
	}
	rows := make([]model.UserSetting, 0, len(settings))
	for key, value := range settings {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		rows = append(rows, model.UserSetting{UserID: userID, Key: key, Value: value, UpdatedAt: now})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := s.db.Gorm.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).CreateInBatches(rows, 200).Error; err != nil {
		return 0, err
	}
	return len(rows), nil
}
