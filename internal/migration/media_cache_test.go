package migration

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"emby-lite/internal/scanner"
)

// TestMediaProbeCacheMatchesScanner guards the one behaviour the migration relies on: the
// cached discovery must return exactly what scanner.DiscoverLocalImages returns, otherwise
// posters silently stop being migrated.
func TestMediaProbeCacheMatchesScanner(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Movies", "Inception (2010)")
	seasonDir := filepath.Join(root, "TV", "Severance", "Season 01")
	for _, dir := range []string{movieDir, seasonDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	movieFiles := map[string]string{
		"Inception (2010).mkv":        "",
		"Inception (2010)-poster.jpg": "p",
		"fanart.jpg":                  "f",
		"thumb.png":                   "t",
		"clearlogo.webp":              "l",
		".hidden.jpg":                 "",
		"notes.txt":                   "",
	}
	for name, body := range movieFiles {
		if err := os.WriteFile(filepath.Join(movieDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Season folder: shared season poster, one episode poster, mixed case extension.
	seasonFiles := map[string]string{
		"S01E01.mkv":           "",
		"poster.JPG":           "p",
		"S01E01-thumb.png":     "t",
		"S01E02-thumb.jpg":     "t",
		"season-banner.jpeg":   "",
		"landscape.webp":       "l",
		"subdir/ignored.jpg":   "",
		"S01E02.mkv":           "",
		"S01E02-fanart.jpg":    "f",
		"S01E01-banner.png":    "b",
		"severity-posters.jpg": "wrong name",
	}
	for name, body := range seasonFiles {
		full := filepath.Join(seasonDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cache := newMediaProbeCache()
	cases := []struct{ dir, stem string }{
		{movieDir, "Inception (2010)"},
		{movieDir, ""},
		{seasonDir, "S01E01"},
		{seasonDir, "S01E02"},
		{seasonDir, ""},
		{filepath.Join(root, "Missing"), "Whatever"},
		{"", "Whatever"},
	}
	for _, c := range cases {
		want := scanner.DiscoverLocalImages(c.dir, c.stem)
		got := cache.discoverImages(c.dir, c.stem)
		// discoverImages returns nil for an empty dir, the scanner returns an empty map.
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("dir=%q stem=%q: cached discovery %v != scanner %v", c.dir, c.stem, got, want)
		}
		// A second pass must be served from the cache and stay identical.
		if again := cache.discoverImages(c.dir, c.stem); !reflect.DeepEqual(got, again) {
			t.Fatalf("dir=%q stem=%q: cached result changed between calls: %v != %v", c.dir, c.stem, got, again)
		}
	}
}

// TestMediaProbeCacheStatFile covers the stat wrapper used for video and image paths.
func TestMediaProbeCacheStatFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.mkv")
	if err := os.WriteFile(file, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := newMediaProbeCache()
	size, mtime, modTime, ok := cache.statFile(file)
	if !ok || size != 10 || mtime == 0 || modTime.IsZero() {
		t.Fatalf("unexpected stat for existing file: size=%d mtime=%d ok=%v", size, mtime, ok)
	}
	// The cached value must be reused rather than re-read from disk.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if size2, _, _, ok2 := cache.statFile(file); !ok2 || size2 != size {
		t.Fatalf("expected cached stat to survive removal, got size=%d ok=%v", size2, ok2)
	}

	if _, _, _, ok := cache.statFile(filepath.Join(root, "missing.mkv")); ok {
		t.Fatal("missing path must not be reported as a regular file")
	}
	if _, _, _, ok := cache.statFile(root); ok {
		t.Fatal("directory must not be reported as a regular file")
	}
	if _, _, _, ok := cache.statFile(""); ok {
		t.Fatal("empty path must not be reported as a regular file")
	}
}
