package scraper

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// MatchCandidate represents a scored candidate for automatic or manual matching.
type MatchCandidate struct {
	Provider      string   `json:"provider"` // tmdb
	ProviderID    int      `json:"provider_id"`
	MediaType     string   `json:"media_type"` // movie | tv
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int      `json:"year"`
	Overview      string   `json:"overview"`
	PosterURL     string   `json:"poster_url"`
	Rating        float64  `json:"rating"`
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons"`
}

// ScoreCandidates ranks TMDB search hits against local item title, original title, and year.
func ScoreCandidates(localTitle, localOrig string, localYear int, mediaType string, hits []TMDBSearchItem, imgBuilder func(size, p string) string) []MatchCandidate {
	out := make([]MatchCandidate, 0, len(hits))
	normLocal := normalizeTitle(localTitle)
	normLocalOrig := normalizeTitle(localOrig)

	for _, h := range hits {
		cTitle := h.EffectiveTitle()
		cOrig := h.EffectiveOriginalTitle()
		cYear := h.EffectiveYear()

		sim1 := bestSimilarity(normLocal, normalizeTitle(cTitle), normalizeTitle(cOrig))
		sim2 := 0.0
		if normLocalOrig != "" {
			sim2 = bestSimilarity(normLocalOrig, normalizeTitle(cTitle), normalizeTitle(cOrig))
		}
		titleSim := math.Max(sim1, sim2)
		titleScore := titleSim * 75.0 // Title accounts for up to 75 points

		var reasons []string
		if titleSim >= 0.98 {
			reasons = append(reasons, "标题完全匹配 (+75)")
		} else {
			reasons = append(reasons, "标题相似度 "+formatScore(titleScore)+"/75")
		}

		// Year proximity accounts for up to 25 points (or neutral 15 if localYear unknown).
		yearScore := 0.0
		if localYear > 0 && cYear > 0 {
			diff := localYear - cYear
			if diff < 0 {
				diff = -diff
			}
			switch {
			case diff == 0:
				yearScore = 25
				reasons = append(reasons, "年份完全一致 (+25)")
			case diff == 1:
				yearScore = 15
				reasons = append(reasons, "年份相差 1 年 (+15)")
			case diff == 2:
				yearScore = 5
				reasons = append(reasons, "年份相差 2 年 (+5)")
			default:
				yearScore = -10
				reasons = append(reasons, "年份偏差较大 (-10)")
			}
		} else {
			yearScore = 15
			reasons = append(reasons, "无年份参考 (+15)")
		}

		total := math.Round((titleScore+yearScore)*10) / 10
		if total < 0 {
			total = 0
		}
		if total > 100 {
			total = 100
		}

		poster := ""
		if imgBuilder != nil && h.PosterPath != "" {
			poster = imgBuilder("w342", h.PosterPath)
		}

		out = append(out, MatchCandidate{
			Provider:      "tmdb",
			ProviderID:    h.ID,
			MediaType:     mediaType,
			Title:         cTitle,
			OriginalTitle: cOrig,
			Year:          cYear,
			Overview:      h.Overview,
			PosterURL:     poster,
			Rating:        math.Round(h.VoteAverage*10) / 10,
			Score:         total,
			Reasons:       reasons,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func bestSimilarity(target, a, b string) float64 {
	s1 := bigramDice(target, a)
	s2 := bigramDice(target, b)
	if s2 > s1 {
		return s2
	}
	return s1
}

func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bigramDice computes Dice coefficient over Unicode rune bigrams (works cleanly for Chinese & English).
func bigramDice(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1.0
	}
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 1 || len(rb) == 1 {
		if strings.Contains(a, b) || strings.Contains(b, a) {
			return 0.7
		}
		return 0
	}
	ba := make(map[string]int, len(ra)-1)
	for i := 0; i < len(ra)-1; i++ {
		ba[string(ra[i:i+2])]++
	}
	matches := 0
	for i := 0; i < len(rb)-1; i++ {
		k := string(rb[i : i+2])
		if ba[k] > 0 {
			ba[k]--
			matches++
		}
	}
	return float64(2*matches) / float64((len(ra)-1)+(len(rb)-1))
}

func formatScore(v float64) string {
	return strings.TrimRight(strings.TrimRight(
		strings.ReplaceAll(fmtFloat1(v), " ", ""), "0"), ".")
}

func fmtFloat1(v float64) string {
	i := int(math.Round(v * 10))
	whole := i / 10
	frac := i % 10
	if frac < 0 {
		frac = -frac
	}
	return intToStr(whole) + "." + intToStr(frac)
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var digits [20]byte
	pos := len(digits)
	for n > 0 {
		pos--
		digits[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		digits[pos] = '-'
	}
	return string(digits[pos:])
}
