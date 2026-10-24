package fyneui

import (
	"image"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// newQRViewport is shared by the production window and native presentation test.
func newQRViewport(picture *canvas.Image, target fyne.Canvas) fyne.CanvasObject {
	layout := &physicalImageLayout{picture: picture, pixels: target.PixelCoordinateForPosition}
	return container.NewScroll(container.New(layout, picture))
}

// ImageFillOriginal's natural size does not account for every native framebuffer
// scale. Never let a stretching parent turn protocol pixels into logical units.
type physicalImageLayout struct {
	picture *canvas.Image
	pixels  func(fyne.Position) (int, int)
}

func (l *physicalImageLayout) MinSize(_ []fyne.CanvasObject) fyne.Size {
	size, _ := physicalImageGeometry(l.picture.Image.Bounds(), fyne.Size{}, l.pixels)
	return size
}

func (l *physicalImageLayout) Layout(_ []fyne.CanvasObject, available fyne.Size) {
	size, pos := physicalImageGeometry(l.picture.Image.Bounds(), available, l.pixels)
	l.picture.Move(pos)
	l.picture.Resize(size)
}

func physicalImageGeometry(bounds image.Rectangle, available fyne.Size, pixels func(fyne.Position) (int, int)) (fyne.Size, fyne.Position) {
	// Sample a long distance so fractional DPI scales do not round to integers.
	const sample = 65536
	x0, y0 := pixels(fyne.Position{})
	x1, y1 := pixels(fyne.NewPos(sample, sample))
	sx, sy := float64(x1-x0)/sample, float64(y1-y0)/sample
	if sx <= 0 {
		sx = 1
	}
	if sy <= 0 {
		sy = 1
	}
	w, h := float64(bounds.Dx()), float64(bounds.Dy())
	size := fyne.NewSize(float32(w/sx), float32(h/sy))
	x := math.Floor(math.Max(0, float64(available.Width)*sx-w)/2) / sx
	y := math.Floor(math.Max(0, float64(available.Height)*sy-h)/2) / sy
	return size, fyne.NewPos(float32(x), float32(y))
}
