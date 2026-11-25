package image

import (
	"bytes"
	stdimg "image"
	"image/color"
	"image/jpeg"
	"os"

	"golang.org/x/image/draw"
)

const (
	collageWidth  = 640
	collageHeight = 360
)

// GenerateLibraryCollage creates a 16:9 (640x360) dark-styled collage cover JPEG from up to 4 poster file paths.
func GenerateLibraryCollage(posterPaths []string) ([]byte, error) {
	canvas := stdimg.NewRGBA(stdimg.Rect(0, 0, collageWidth, collageHeight))

	// Draw subtle dark cinema diagonal gradient background (#0f172a -> #1e293b).
	for y := 0; y < collageHeight; y++ {
		for x := 0; x < collageWidth; x++ {
			t := float64(x+y) / float64(collageWidth+collageHeight)
			r := uint8(13 + t*19)
			g := uint8(18 + t*25)
			b := uint8(28 + t*34)
			canvas.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}

	var decoded []stdimg.Image
	for _, p := range posterPaths {
		if len(decoded) >= 4 {
			break
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		img, _, err := stdimg.Decode(f)
		_ = f.Close()
		if err == nil {
			decoded = append(decoded, img)
		}
	}

	if len(decoded) > 0 {
		// Compute clean evenly-spaced poster slots (2:3 aspect ratio) across the 640x360 canvas.
		slotH := 284
		slotW := 188
		if len(decoded) == 4 {
			slotW = 142
			slotH = 214
		} else if len(decoded) == 3 {
			slotW = 176
			slotH = 264
		}
		gap := 14
		totalW := len(decoded)*slotW + (len(decoded)-1)*gap
		startX := (collageWidth - totalW) / 2
		startY := (collageHeight - slotH) / 2

		for i, src := range decoded {
			x0 := startX + i*(slotW+gap)
			y0 := startY
			// Subtle vertical stagger for visual depth when >= 3 posters.
			if len(decoded) >= 3 && i%2 == 1 {
				y0 += 10
			} else if len(decoded) >= 3 {
				y0 -= 8
			}

			// Draw soft dark drop-shadow border behind each poster.
			shadowRect := stdimg.Rect(x0-3, y0-3, x0+slotW+5, y0+slotH+6)
			draw.Draw(canvas, shadowRect, &stdimg.Uniform{C: color.RGBA{R: 5, G: 8, B: 14, A: 220}}, stdimg.Point{}, draw.Over)

			dstRect := stdimg.Rect(x0, y0, x0+slotW, y0+slotH)
			srcCrop := centerCropToAspect(src.Bounds(), float64(slotW)/float64(slotH))
			draw.CatmullRom.Scale(canvas, dstRect, src, srcCrop, draw.Over, nil)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func centerCropToAspect(bounds stdimg.Rectangle, targetAspect float64) stdimg.Rectangle {
	w := float64(bounds.Dx())
	h := float64(bounds.Dy())
	if w <= 0 || h <= 0 {
		return bounds
	}
	curAspect := w / h
	if curAspect > targetAspect {
		// Too wide -> crop left/right
		newW := int(h * targetAspect)
		dx := (bounds.Dx() - newW) / 2
		return stdimg.Rect(bounds.Min.X+dx, bounds.Min.Y, bounds.Min.X+dx+newW, bounds.Max.Y)
	}
	// Too tall -> crop top/bottom
	newH := int(w / targetAspect)
	dy := (bounds.Dy() - newH) / 2
	return stdimg.Rect(bounds.Min.X, bounds.Min.Y+dy, bounds.Max.X, bounds.Min.Y+dy+newH)
}
