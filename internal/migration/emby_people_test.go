package migration

import "testing"

func TestEmbyPersonRole(t *testing.T) {
	cases := map[string]string{
		"0": "Actor", "actor": "Actor", "ACTOR": "Actor",
		"1": "Director", "director": "Director",
		"2": "Writer", "writer": "Writer",
		"3": "Producer", "producer": "Producer",
		"4": "GuestStar", "gueststar": "GuestStar",
		"5": "Composer", "composer": "Composer",
		"6": "Conductor", "conductor": "Conductor",
		"7": "Lyricist", "lyricist": "Lyricist",
		"8": "Arranger", "arranger": "Arranger",
		"9": "Engineer", "engineer": "Engineer",
		"10": "Mixer", "mixer": "Mixer",
		"11": "Performer", "performer": "Performer",
		"12": "Author", "author": "Author",
		"13": "Illustrator", "illustrator": "Illustrator",
		"14": "Editor", "editor": "Editor",
		"15": "Translator", "translator": "Translator",
		" 3 ": "Producer",
		"99":  "Other",
		"":    "",
	}
	for raw, want := range cases {
		if got := embyPersonRole(raw); got != want {
			t.Errorf("embyPersonRole(%q) = %q, want %q", raw, got, want)
		}
	}
}
