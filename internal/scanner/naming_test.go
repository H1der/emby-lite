package scanner

import "testing"

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
