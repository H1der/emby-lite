package scanner

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseMovieName(t *testing.T) {
	m := ParseMovieName("Inception (2010) - 4K HDR.mkv", "Inception (2010)")
	if m.Title != "Inception" || m.Year != 2010 || m.VersionLabel != "4K HDR" {
		t.Fatalf("unexpected movie parse: %+v", m)
	}
	m2 := ParseMovieName("Inception.2010.2160p.mkv", "Inception (2010)")
	if m2.Title != "Inception" || m2.Year != 2010 {
		t.Fatalf("unexpected scene movie parse: %+v", m2)
	}
	s := ParseMovieName("Dune (2021).mkv.strm", "")
	if s.Title != "Dune" || s.Year != 2021 {
		t.Fatalf("unexpected strm movie parse: %+v", s)
	}
}

func TestParseEpisodeName(t *testing.T) {
	ep := ParseEpisodeName("Severance.S02E03.Who.Is.Alive.mkv", "Season 02")
	if !ep.Found || ep.Season != 2 || ep.Episode != 3 || ep.Title != "Who Is Alive" {
		t.Fatalf("unexpected episode parse: %+v", ep)
	}
	ep2 := ParseEpisodeName("EP05.mkv.strm", "Season 01")
	if !ep2.Found || ep2.Season != 1 || ep2.Episode != 5 {
		t.Fatalf("unexpected ep-only parse: %+v", ep2)
	}
}

func TestPinyinInitials(t *testing.T) {
	got := PinyinInitials("星际穿越 Interstellar")
	if got != "xjcyinterstellar" {
		t.Fatalf("expected xjcyinterstellar, got %q", got)
	}
}

func TestPinyinInitialsValidUTF8OnLongJapaneseTitles(t *testing.T) {
	samples := []string{
		"SONE-954-オッサンばっかりの职场的社员旅行に娘ほど歳の离れた隙だらけ新卒社员が唯一の女性で参加！ このあと风吕を覗かれ媚薬を盛られ全社员にチ●ポ挿れられちゃいます。 川越にこ - JAVDAY",
		"HMN-831-【キメセクNTR映像】别々の大学に进んだ彼女が初めてのサークル新歓饮みで连络が途绝え 翌朝送られてきたのはガンギマリ下品アクメを缲り返す彼女らしき雌の姿だった。 香水じゅん - JAVDAY",
		strings.Repeat("あいうえおかきくけこ", 40),
	}
	for _, s := range samples {
		got := PinyinInitials(s + " " + s)
		if !utf8.ValidString(got) {
			t.Fatalf("PinyinInitials produced invalid UTF-8 for %q: %x", s, []byte(got))
		}
		if utf8.RuneCountInString(got) > 240 {
			t.Fatalf("PinyinInitials exceeded 240 runes: %d", utf8.RuneCountInString(got))
		}
		if strings.IndexByte(got, 0) >= 0 {
			t.Fatalf("PinyinInitials contained NUL byte")
		}
	}
}

func TestSanitizeAndTruncateRunes(t *testing.T) {
	bad := "abc\xe3\x82def\x00ghi"
	clean := SanitizeUTF8(bad)
	if !utf8.ValidString(clean) || strings.IndexByte(clean, 0) >= 0 || clean != "abcdefghi" {
		t.Fatalf("unexpected SanitizeUTF8(%q) = %q", bad, clean)
	}

	jp := strings.Repeat("あ", 100) // 300 bytes, 100 runes
	trunc := TruncateRunes(jp, 80)
	if !utf8.ValidString(trunc) {
		t.Fatalf("TruncateRunes produced invalid UTF-8")
	}
	if utf8.RuneCountInString(trunc) != 80 {
		t.Fatalf("expected 80 runes, got %d", utf8.RuneCountInString(trunc))
	}
}
