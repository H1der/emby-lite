package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mozillazg/go-pinyin"
)

var (
	videoExts = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".m4v": true,
		".ts": true, ".m2ts": true, ".webm": true, ".flv": true, ".wmv": true,
		".rmvb": true, ".mpg": true, ".mpeg": true, ".strm": true,
	}
	subtitleExts = map[string]string{
		".srt": "srt",
		".ass": "ass",
		".ssa": "ssa",
		".vtt": "vtt",
	}
	imageExts = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	}

	reSeasonEpisode    = regexp.MustCompile(`(?i)[Ss](\d{1,2})[\s._-]*[Ee](\d{1,3})`)
	reAltEpisode       = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	reEpOnly           = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\(])[Ee][Pp]?(\d{1,3})(?:$|[\s._\-\]\)])`)
	reSeasonDir        = regexp.MustCompile(`(?i)^(?:season|s)[\s._-]*(\d{1,2})(?:[\s._\-]+.*)?$`)
	reChineseSeasonDir = regexp.MustCompile(`^第[\s]*([0-9]{1,2}|[零〇一二三四五六七八九十百两]+)[\s]*[季部](?:[\s._\-（(].*)?$`)
	reSpecialsDir      = regexp.MustCompile(`(?i)^(?:specials?|sps?|ova|oav|特别篇|番外篇?|特典)(?:[\s._\-（(].*)?$`)
	reSeasonSuffix     = regexp.MustCompile(`(?i)[\s._\-（(\[]*(?:(?:season|s)[\s._-]*\d{1,2}|第[\s]*(?:[0-9]{1,2}|[零〇一二三四五六七八九十百两]+)[\s]*[季部])[\s._\-）)\]]*$`)
	reYear             = regexp.MustCompile(`(?:[\(\[\.\s_-]|^)((?:19|20)\d{2})(?:[\)\]\.\s_-]|$)`)
	reVersionSplit     = regexp.MustCompile(`\s+-\s+`)
	reQualityHint      = regexp.MustCompile(`(?i)\b(2160p|4k|uhd|1080p|1080i|720p|480p|remux|bluray|blu-ray|bdrip|web-dl|webdl|webrip|hdtv|dv|dovi|dolby[\s._-]*vision|hdr10\+?|hdr|hlg|sdr|hevc|x265|h265|h\.265|avc|x264|h264|h\.264|av1|atmos|truehd|dts-hd|imax|3d|director'?s[\s._-]*cut|extended|unrated|theatrical|60fps|120fps)\b`)
)

func IsVideoFile(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

func IsSTRMFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".strm")
}

func IsImageFile(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

func SubtitleCodec(name string) (string, bool) {
	c, ok := subtitleExts[strings.ToLower(filepath.Ext(name))]
	return c, ok
}

// ParsedMovie holds parsed movie title, year, and optional version label.
type ParsedMovie struct {
	Title        string
	Year         int
	VersionLabel string
}

// ParseMovieName parses a movie filename or parent directory name (zero-rename compatible).
func ParseMovieName(filename, parentDir string) ParsedMovie {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	// Strip virtual extension like movie.mkv.strm -> movie
	if strings.EqualFold(filepath.Ext(filename), ".strm") {
		if innerExt := strings.ToLower(filepath.Ext(stem)); videoExts[innerExt] && innerExt != ".strm" {
			stem = strings.TrimSuffix(stem, filepath.Ext(stem))
		}
	}

	var versionLabel string
	if parts := reVersionSplit.Split(stem, 2); len(parts) == 2 {
		stem = strings.TrimSpace(parts[0])
		versionLabel = strings.TrimSpace(parts[1])
	}

	title, year := extractTitleAndYear(stem)
	if year == 0 && parentDir != "" {
		pTitle, pYear := extractTitleAndYear(parentDir)
		if pYear > 0 {
			year = pYear
			if title == "" {
				title = pTitle
			}
		}
	}
	if title == "" {
		title = stem
	}
	return ParsedMovie{
		Title:        strings.TrimSpace(title),
		Year:         year,
		VersionLabel: versionLabel,
	}
}

// ParsedEpisode holds parsed season/episode numbers and episode title.
type ParsedEpisode struct {
	Season  int
	Episode int
	Title   string
	Found   bool
}

// ParseEpisodeName extracts season & episode numbers from a filename (and optional parent season dir).
func ParseEpisodeName(filename, parentDir string) ParsedEpisode {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.EqualFold(filepath.Ext(filename), ".strm") {
		if innerExt := strings.ToLower(filepath.Ext(stem)); videoExts[innerExt] && innerExt != ".strm" {
			stem = strings.TrimSuffix(stem, filepath.Ext(stem))
		}
	}

	if m := reSeasonEpisode.FindStringSubmatchIndex(stem); len(m) == 6 {
		s, _ := strconv.Atoi(stem[m[2]:m[3]])
		e, _ := strconv.Atoi(stem[m[4]:m[5]])
		rest := cleanTitle(stem[m[1]:])
		return ParsedEpisode{Season: s, Episode: e, Title: rest, Found: true}
	}
	if m := reAltEpisode.FindStringSubmatchIndex(stem); len(m) == 6 {
		s, _ := strconv.Atoi(stem[m[2]:m[3]])
		e, _ := strconv.Atoi(stem[m[4]:m[5]])
		rest := cleanTitle(stem[m[1]:])
		return ParsedEpisode{Season: s, Episode: e, Title: rest, Found: true}
	}
	if m := reEpOnly.FindStringSubmatchIndex(stem); len(m) == 4 {
		e, _ := strconv.Atoi(stem[m[2]:m[3]])
		s := ParseSeasonDir(parentDir)
		if s < 0 {
			s = 1
		}
		rest := cleanTitle(stem[m[1]:])
		return ParsedEpisode{Season: s, Episode: e, Title: rest, Found: true}
	}
	return ParsedEpisode{}
}

// ParseSeasonDir returns the season number if dirName matches "Season 01" / "S01" / "第1季" / "第一季" / "Specials", else -1.
func ParseSeasonDir(dirName string) int {
	name := strings.TrimSpace(dirName)
	if name == "" {
		return -1
	}
	if reSpecialsDir.MatchString(name) {
		return 0
	}
	if m := reSeasonDir.FindStringSubmatch(name); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if m := reChineseSeasonDir.FindStringSubmatch(name); len(m) == 2 {
		if n := parseSeasonNumberToken(m[1]); n >= 0 {
			return n
		}
	}
	return -1
}

// StripSeasonSuffix removes a trailing season indicator (e.g. "第一季", "第2季", "Season 2", "S02")
// from a series title/directory name and reports whether a season marker was stripped.
func StripSeasonSuffix(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	stripped := strings.TrimSpace(reSeasonSuffix.ReplaceAllString(s, ""))
	if stripped != "" && stripped != s {
		return stripped, true
	}
	return s, false
}

// NormalizeMergeTitle produces a punctuation- and case-folded title key for auto-merge matching.
func NormalizeMergeTitle(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ExtractVersionBaseAndLabel splits Emby-style "<base> - <version>" filenames (stripping virtual .strm video ext).
func ExtractVersionBaseAndLabel(filename string) (base, label string, hasSplit bool) {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.EqualFold(filepath.Ext(filename), ".strm") {
		if innerExt := strings.ToLower(filepath.Ext(stem)); videoExts[innerExt] && innerExt != ".strm" {
			stem = strings.TrimSuffix(stem, filepath.Ext(stem))
		}
	}
	if parts := reVersionSplit.Split(stem, 2); len(parts) == 2 {
		b := strings.TrimSpace(parts[0])
		l := strings.TrimSpace(parts[1])
		if b != "" && l != "" {
			return b, l, true
		}
	}
	return strings.TrimSpace(stem), "", false
}

// InferVersionLabelFromFilename extracts a concise quality/edition label from a filename when
// explicit Emby " - Version" naming is absent (e.g. scene releases like Movie.2020.2160p.DV.HEVC.mkv).
func InferVersionLabelFromFilename(filename string) string {
	if _, label, ok := ExtractVersionBaseAndLabel(filename); ok && label != "" {
		return label
	}
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.EqualFold(filepath.Ext(filename), ".strm") {
		if innerExt := strings.ToLower(filepath.Ext(stem)); videoExts[innerExt] && innerExt != ".strm" {
			stem = strings.TrimSuffix(stem, filepath.Ext(stem))
		}
	}
	matches := reQualityHint.FindAllString(stem, -1)
	if len(matches) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(matches))
	parts := make([]string, 0, 4)
	for _, m := range matches {
		norm := strings.ToUpper(strings.NewReplacer(".", "", "_", " ", "-", "").Replace(m))
		switch norm {
		case "2160P", "UHD":
			norm = "4K"
		case "DOVI", "DOLBYVISION", "DOLBY VISION":
			norm = "DV"
		case "X265", "H265":
			norm = "HEVC"
		case "X264", "H264":
			norm = "H264"
		case "WEBDL":
			norm = "WEB-DL"
		case "BLURAY":
			norm = "BluRay"
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		parts = append(parts, norm)
		if len(parts) >= 4 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func parseSeasonNumberToken(tok string) int {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return -1
	}
	if n, err := strconv.Atoi(tok); err == nil {
		return n
	}
	digits := map[rune]int{
		'零': 0, '〇': 0,
		'一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(tok)
	if len(runes) == 1 {
		if runes[0] == '十' {
			return 10
		}
		if d, ok := digits[runes[0]]; ok {
			return d
		}
		return -1
	}
	total := 0
	cur := 0
	hasDigit := false
	for _, r := range runes {
		if d, ok := digits[r]; ok {
			cur = d
			hasDigit = true
		} else if r == '十' {
			if !hasDigit || cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
			hasDigit = true
		} else if r == '百' {
			if !hasDigit || cur == 0 {
				cur = 1
			}
			total += cur * 100
			cur = 0
			hasDigit = true
		} else {
			return -1
		}
	}
	total += cur
	if !hasDigit {
		return -1
	}
	return total
}

func extractTitleAndYear(raw string) (string, int) {
	year := 0
	idx := len(raw)
	if matches := reYear.FindAllStringSubmatchIndex(raw, -1); len(matches) > 0 {
		last := matches[len(matches)-1]
		if y, err := strconv.Atoi(raw[last[2]:last[3]]); err == nil && y >= 1900 && y <= 2099 {
			year = y
			idx = last[0]
		}
	}
	title := cleanTitle(raw[:idx])
	return title, year
}

func cleanTitle(raw string) string {
	s := strings.NewReplacer(".", " ", "_", " ").Replace(raw)
	s = strings.Trim(s, " -()[]")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// SanitizeUTF8 removes invalid UTF-8 byte sequences and NUL bytes that PostgreSQL rejects.
func SanitizeUTF8(s string) string {
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return s
}

// TruncateRunes sanitizes UTF-8 and truncates s to at most maxRunes Unicode code points
// without splitting multi-byte UTF-8 characters.
func TruncateRunes(s string, maxRunes int) string {
	s = SanitizeUTF8(s)
	if maxRunes <= 0 || len(s) <= maxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}

// PinyinInitials computes lowercase ASCII + Chinese pinyin initials for fast search and sorting.
func PinyinInitials(text string) string {
	const maxInitialsRunes = 240
	args := pinyin.NewArgs()
	args.Style = pinyin.FirstLetter
	var b strings.Builder
	written := 0
	for _, r := range SanitizeUTF8(text) {
		if written >= maxInitialsRunes {
			break
		}
		if unicode.Is(unicode.Han, r) {
			res := pinyin.SinglePinyin(r, args)
			if len(res) > 0 && len(res[0]) > 0 {
				firstRune, _ := utf8.DecodeRuneInString(res[0])
				if firstRune != utf8.RuneError {
					b.WriteRune(unicode.ToLower(firstRune))
					written++
				}
			}
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			written++
		}
	}
	return b.String()
}

// BuildSortName produces a normalized sort key.
func BuildSortName(name string) string {
	s := strings.TrimSpace(strings.ToLower(name))
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}
	return s
}

// ParseSubtitleMeta parses language/forced/default hints from a sidecar subtitle filename.
func ParseSubtitleMeta(mediaStem, subFile string) (lang, title string, isDefault, isForced bool) {
	ext := filepath.Ext(subFile)
	base := strings.TrimSuffix(subFile, ext)
	suffix := strings.TrimPrefix(base, mediaStem)
	suffix = strings.Trim(suffix, "._- ")
	if suffix == "" {
		return "und", "External", false, false
	}
	parts := strings.FieldsFunc(suffix, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' })
	lang = "und"
	var labelParts []string
	for _, p := range parts {
		lp := strings.ToLower(p)
		switch lp {
		case "default":
			isDefault = true
		case "forced":
			isForced = true
		case "zh", "chi", "zho", "chs", "cht", "cn", "sc", "tc", "zh-cn", "zh-tw", "hans", "hant":
			lang = "zho"
			labelParts = append(labelParts, p)
		case "en", "eng":
			lang = "eng"
			labelParts = append(labelParts, p)
		case "ja", "jpn":
			lang = "jpn"
			labelParts = append(labelParts, p)
		case "ko", "kor":
			lang = "kor"
			labelParts = append(labelParts, p)
		default:
			labelParts = append(labelParts, p)
		}
	}
	title = strings.Join(labelParts, " ")
	if title == "" {
		title = strings.ToUpper(lang)
	}
	return lang, title, isDefault, isForced
}

// StableID generates a deterministic 32-char hex ID from a namespace and key.
func StableID(namespace, key string) string {
	sum := sha256.Sum256([]byte(namespace + ":" + key))
	return hex.EncodeToString(sum[:16])
}

// ContainerFromPath infers media container extension from local path or STRM filename/URL.
func ContainerFromPath(path, strmURL string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext != "" && ext != "strm" {
		return ext
	}
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	if inner := strings.TrimPrefix(strings.ToLower(filepath.Ext(stem)), "."); inner != "" && videoExts["."+inner] {
		return inner
	}
	if strmURL != "" {
		clean := strings.Split(strings.ToLower(strmURL), "?")[0]
		if uExt := strings.TrimPrefix(filepath.Ext(clean), "."); uExt != "" && videoExts["."+uExt] {
			return uExt
		}
	}
	return "mkv"
}

// Fingerprint computes a lightweight move-detection fingerprint from filename base + size.
func Fingerprint(path string, size int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", strings.ToLower(filepath.Base(path)), size)))
	return hex.EncodeToString(sum[:16])
}
