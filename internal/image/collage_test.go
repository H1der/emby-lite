package image

import (
	"bytes"
	stdimg "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateLibraryCollage(t *testing.T) {
	tmp := t.TempDir()
	var paths []string
	colors := []color.RGBA{
		{R: 200, G: 50, B: 60, A: 255},
		{R: 40, G: 180, B: 90, A: 255},
		{R: 60, G: 110, B: 220, A: 255},
		{R: 210, G: 160, B: 40, A: 255},
	}
	for i, c := range colors {
		p := filepath.Join(tmp, string(rune('a'+i))+".png")
		img := stdimg.NewRGBA(stdimg.Rect(0, 0, 200, 300))
		for y := 0; y < 300; y++ {
			for x := 0; x < 200; x++ {
				img.Set(x, y, c)
			}
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		_ = png.Encode(f, img)
		_ = f.Close()
		paths = append(paths, p)
	}

	jpegBytes, err := GenerateLibraryCollage(paths)
	if err != nil {
		t.Fatalf("GenerateLibraryCollage failed: %v", err)
	}
	outImg, format, err := stdimg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("decode collage failed: %v", err)
	}
	if format != "jpeg" || outImg.Bounds().Dx() != collageWidth || outImg.Bounds().Dy() != collageHeight {
		t.Fatalf("unexpected collage output: format=%s size=%dx%d", format, outImg.Bounds().Dx(), outImg.Bounds().Dy())
	}
}
