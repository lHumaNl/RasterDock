package fyneui

import (
	"image"
	"math"
	"testing"

	"fyne.io/fyne/v2"
)

func TestQRGeometryUsesPhysicalPixels(t *testing.T) {
	for _, scale := range []float64{1, 1.1, 1.25, 1.3, 1.5, 2, 2.5, 3, 4} {
		pixels := func(pos fyne.Position) (int, int) {
			return 37 + int(math.Round(float64(pos.X)*scale)), 19 + int(math.Round(float64(pos.Y)*scale))
		}
		size, pos := physicalImageGeometry(image.Rect(0, 0, 664, 460), fyne.NewSize(720, 520), pixels)
		x, y := pixels(fyne.NewPos(size.Width, size.Height))
		if x-37 != 664 || y-19 != 460 {
			t.Fatalf("scale=%g: size=%v maps to %dx%d", scale, size, x-37, y-19)
		}
		for _, offset := range []float32{pos.X, pos.Y} {
			physical := float64(offset) * scale
			if math.Abs(physical-math.Round(physical)) > 0.01 {
				t.Fatalf("scale=%g: unaligned position %v", scale, pos)
			}
		}
		smallSize, smallPos := physicalImageGeometry(image.Rect(0, 0, 664, 460), fyne.NewSize(10, 10), pixels)
		if smallSize != size || smallPos != (fyne.Position{}) {
			t.Fatal("small viewport must scroll rather than shrink QR", smallSize, smallPos)
		}
	}
}
