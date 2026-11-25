package image

import (
	"bytes"
	stdimg "image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestResizeAndDiskCache(t *testing.T) {
	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "poster.png")

	// Create a 400x600 synthetic PNG poster.
	img := stdimg.NewRGBA(stdimg.Rect(0, 0, 400, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{R: 30, G: 140, B: 200, A: 255})
		}
	}
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	pipe := NewPipeline(filepath.Join(tmp, "cache"))

	// Request resized maxWidth=200
	req := httptest.NewRequest(http.MethodGet, "/Items/1/Images/Primary?maxWidth=200", nil)
	rec := httptest.NewRecorder()
	if err := pipe.ServeFile(rec, req, srcPath, "tag-v1", 200, 0, 85); err != nil {
		t.Fatalf("ServeFile resize failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	decoded, _, err := stdimg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode resized output: %v", err)
	}
	if decoded.Bounds().Dx() != 200 || decoded.Bounds().Dy() != 300 {
		t.Fatalf("expected 200x300, got %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}

	// Verify disk cache hit & ETag 304
	stats := pipe.Stats()
	if stats.FileCount != 1 || stats.TotalBytes == 0 {
		t.Fatalf("expected 1 cached rendition, got %+v", stats)
	}
	req304 := httptest.NewRequest(http.MethodGet, "/Items/1/Images/Primary?maxWidth=200", nil)
	req304.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec304 := httptest.NewRecorder()
	if err := pipe.ServeFile(rec304, req304, srcPath, "tag-v1", 200, 0, 85); err != nil {
		t.Fatal(err)
	}
	if rec304.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on ETag match, got %d", rec304.Code)
	}

	// Clear cache
	cleared, err := pipe.Clear()
	if err != nil || cleared != 1 {
		t.Fatalf("expected 1 cleared file, got %d err=%v", cleared, err)
	}
	if pipe.Stats().FileCount != 0 {
		t.Fatal("expected empty cache after clear")
	}
}
