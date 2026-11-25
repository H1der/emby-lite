package scanner

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// NFO represents standard Kodi/Emby XML metadata sidecar files (movie/tvshow/season/episodedetails).
type NFO struct {
	Title         string   `xml:"title"`
	OriginalTitle string   `xml:"originaltitle"`
	SortTitle     string   `xml:"sorttitle"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Tagline       string   `xml:"tagline"`
	Year          int      `xml:"year"`
	Premiered     string   `xml:"premiered"`
	Aired         string   `xml:"aired"`
	Season        int      `xml:"season"`
	Episode       int      `xml:"episode"`
	Rating        float64  `xml:"rating"`
	Ratings       []struct {
		Name    string  `xml:"name,attr"`
		Default bool    `xml:"default,attr"`
		Value   float64 `xml:"value"`
	} `xml:"ratings>rating"`
	MPAA      string   `xml:"mpaa"`
	Runtime   float64  `xml:"runtime"` // minutes
	Genres    []string `xml:"genre"`
	Tags      []string `xml:"tag"`
	Studios   []string `xml:"studio"`
	Directors []string `xml:"director"`
	Writers   []string `xml:"credits"`
	Actors    []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Thumb string `xml:"thumb"`
	} `xml:"actor"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	TMDBID string `xml:"tmdbid"`
	IMDBID string `xml:"imdbid"`
	FileInfo struct {
		StreamDetails struct {
			Video []struct {
				Codec    string  `xml:"codec"`
				Width    int     `xml:"width"`
				Height   int     `xml:"height"`
				Duration float64 `xml:"durationinseconds"`
				Aspect   string  `xml:"aspect"`
			} `xml:"video"`
			Audio []struct {
				Codec    string `xml:"codec"`
				Language string `xml:"language"`
				Channels int    `xml:"channels"`
			} `xml:"audio"`
			Subtitle []struct {
				Codec    string `xml:"codec"`
				Language string `xml:"language"`
			} `xml:"subtitle"`
		} `xml:"streamdetails"`
	} `xml:"fileinfo"`
}

func (n *NFO) EffectivePlot() string {
	if s := strings.TrimSpace(n.Plot); s != "" {
		return s
	}
	return strings.TrimSpace(n.Outline)
}

func (n *NFO) EffectivePremiereDate() string {
	if s := strings.TrimSpace(n.Premiered); s != "" {
		return s
	}
	return strings.TrimSpace(n.Aired)
}

func (n *NFO) EffectiveYear() int {
	if n.Year > 0 {
		return n.Year
	}
	if d := n.EffectivePremiereDate(); len(d) >= 4 {
		if y, err := strconv.Atoi(d[:4]); err == nil && y >= 1900 && y <= 2099 {
			return y
		}
	}
	return 0
}

func (n *NFO) EffectiveRating() float64 {
	if n.Rating > 0 {
		return n.Rating
	}
	for _, r := range n.Ratings {
		if r.Default && r.Value > 0 {
			return r.Value
		}
	}
	for _, r := range n.Ratings {
		if r.Value > 0 {
			return r.Value
		}
	}
	return 0
}

func (n *NFO) EffectiveDurationSeconds() float64 {
	for _, v := range n.FileInfo.StreamDetails.Video {
		if v.Duration > 0 {
			return v.Duration
		}
	}
	if n.Runtime > 0 {
		return n.Runtime * 60
	}
	return 0
}

func (n *NFO) ProviderIDs() map[string]string {
	ids := map[string]string{}
	if v := strings.TrimSpace(n.TMDBID); v != "" {
		ids["Tmdb"] = v
	}
	if v := strings.TrimSpace(n.IMDBID); v != "" {
		ids["Imdb"] = v
	}
	for _, u := range n.UniqueIDs {
		val := strings.TrimSpace(u.Value)
		if val == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(u.Type)) {
		case "tmdb":
			ids["Tmdb"] = val
		case "imdb":
			ids["Imdb"] = val
		case "tvdb":
			ids["Tvdb"] = val
		}
	}
	return ids
}

// ReadFirstNFO loads the first valid NFO from the candidate paths (max 2MB).
func ReadFirstNFO(paths ...string) (*NFO, bool) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var n NFO
		err = xml.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&n)
		_ = f.Close()
		if err == nil {
			return &n, true
		}
	}
	return nil, false
}

// ReadSTRMTarget reads the first non-empty target URL or path from a .strm file.
func ReadSTRMTarget(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\xef\xbb\xbf"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

// DiscoverLocalImages returns discovered local image paths keyed by Emby image kind.
func DiscoverLocalImages(dir, mediaStem string) map[string]string {
	found := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return found
	}

	byLower := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !IsImageFile(e.Name()) {
			continue
		}
		byLower[strings.ToLower(e.Name())] = filepath.Join(dir, e.Name())
	}

	pick := func(kind string, bases []string) {
		if _, ok := found[kind]; ok {
			return
		}
		for _, base := range bases {
			for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
				if p, ok := byLower[strings.ToLower(base+ext)]; ok {
					found[kind] = p
					return
				}
			}
		}
	}

	var primaryBases, backdropBases, thumbBases, logoBases, bannerBases []string
	if mediaStem != "" {
		primaryBases = append(primaryBases, mediaStem+"-poster", mediaStem+"-thumb", mediaStem)
		backdropBases = append(backdropBases, mediaStem+"-fanart", mediaStem+"-backdrop")
		thumbBases = append(thumbBases, mediaStem+"-thumb", mediaStem+"-landscape")
		logoBases = append(logoBases, mediaStem+"-clearlogo", mediaStem+"-logo")
		bannerBases = append(bannerBases, mediaStem+"-banner")
	}
	primaryBases = append(primaryBases, "poster", "folder", "cover", "movie", "show")
	backdropBases = append(backdropBases, "fanart", "backdrop", "backdrop1", "background")
	thumbBases = append(thumbBases, "thumb", "landscape")
	logoBases = append(logoBases, "clearlogo", "logo")
	bannerBases = append(bannerBases, "banner")

	pick("Primary", primaryBases)
	pick("Backdrop", backdropBases)
	pick("Thumb", thumbBases)
	pick("Logo", logoBases)
	pick("Banner", bannerBases)
	return found
}

// DiscoverSeasonPoster finds seasonXX-poster / poster inside a season or series directory.
func DiscoverSeasonPoster(seasonDir, seriesDir string, seasonNum int) string {
	imgs := DiscoverLocalImages(seasonDir, "")
	if p, ok := imgs["Primary"]; ok {
		return p
	}
	if seriesDir == "" {
		return ""
	}
	var names []string
	if seasonNum == 0 {
		names = []string{"season-specials-poster", "season00-poster"}
	} else {
		names = []string{
			"season" + pad2(seasonNum) + "-poster",
			"season" + strconv.Itoa(seasonNum) + "-poster",
		}
	}
	entries, err := os.ReadDir(seriesDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !IsImageFile(e.Name()) {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		for _, want := range names {
			if stem == want {
				return filepath.Join(seriesDir, e.Name())
			}
		}
	}
	return ""
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
