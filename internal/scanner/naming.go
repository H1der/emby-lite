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

	reSeasonEpisode = regexp.MustCompile(`(?i)[Ss](\d{1,2})[\s._-]*[Ee](\d{1,3})`)
	reAltEpisode    = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	reEpOnly        = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\(])[Ee][Pp]?(\d{1,3})(?:$|[\s._\-\]\)])`)
	reSeasonDir     = regexp.MustCompile(`(?i)^(?:season|s)[\s._-]*(\d{1,2})$`)
	reSpecialsDir   = regexp.MustCompile(`(?i)^specials?$`)
	reYear          = regexp.MustCompile(`(?:[\(\[\.\s_-]|^)((?:19|20)\d{2})(?:[\)\]\.\s_-]|$)`)
	reVersionSplit  = regexp.MustCompile(`\s+-\s+`)
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

// ParseSeasonDir returns the season number if dirName matches "Season 01" / "S01" / "Specials", else -1.
func ParseSeasonDir(dirName string) int {
	name := strings.TrimSpace(dirName)
	if reSpecialsDir.MatchString(name) {
		return 0
	}
	if m := reSeasonDir.FindStringSubmatch(name); len(m) == 2 {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return n
		}
	}
	return -1
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

// PinyinInitials computes lowercase ASCII + Chinese pinyin initials for fast search and sorting.
func PinyinInitials(text string) string {
	args := pinyin.NewArgs()
	args.Style = pinyin.FirstLetter
	var b strings.Builder
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			res := pinyin.SinglePinyin(r, args)
			if len(res) > 0 && len(res[0]) > 0 {
				b.WriteString(strings.ToLower(res[0][:1]))
			}
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	out := b.String()
	if len(out) > 240 {
		out = out[:240]
	}
	return out
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
