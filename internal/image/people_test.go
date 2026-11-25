package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindAndWriteEmbyPersonImage(t *testing.T) {
	metaRoot := filepath.Join(t.TempDir(), "metadata")
	existingDir := filepath.Join(metaRoot, "people", "L", "Leonardo DiCaprio-tmdb-6193")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existingImg := filepath.Join(existingDir, "folder.jpg")
	if err := os.WriteFile(existingImg, []byte("\xff\xd8\xff\xe0ORIG"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Existing Emby 4.x folder with -tmdb- suffix is found
	found := FindEmbyPersonImage(metaRoot, "Leonardo DiCaprio", "6193")
	if found != existingImg {
		t.Fatalf("expected %s, got %q", existingImg, found)
	}

	// 2. Writeback MUST NOT overwrite existing image
	gotPath, wrote := WriteEmbyPersonImageIfMissing(metaRoot, "Leonardo DiCaprio", "6193", []byte("\xff\xd8\xff\xe0NEW"))
	if wrote || gotPath != existingImg {
		t.Fatalf("must not overwrite existing image: wrote=%v path=%q", wrote, gotPath)
	}
	raw, _ := os.ReadFile(existingImg)
	if string(raw) != "\xff\xd8\xff\xe0ORIG" {
		t.Fatalf("existing image content was altered!")
	}

	// 3. Missing person writes cleanly into <people>/<initial>/<Name>/folder.jpg (works for Chinese names too!)
	writtenPath, wrote := WriteEmbyPersonImageIfMissing(metaRoot, "刘德华", "25246", []byte("\xff\xd8\xff\xe0ANDY"))
	if !wrote || writtenPath == "" {
		t.Fatalf("expected new portrait written, got wrote=%v path=%q", wrote, writtenPath)
	}
	if FindEmbyPersonImage(metaRoot, "刘德华", "25246") != writtenPath {
		t.Fatalf("failed to find newly written Chinese person portrait")
	}
}
