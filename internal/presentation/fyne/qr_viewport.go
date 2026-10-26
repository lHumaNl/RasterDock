package fyneui

import (
	"image"
	"math"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// newQRViewport is shared by the production window and native presentation test.
func newQRViewport(picture *canvas.Image, target fyne.Canvas, observer ...*viewportCapacity) fyne.CanvasObject {
	var capacity *viewportCapacity
	if len(observer) > 0 {
		capacity = observer[0]
	}
	layout := &physicalImageLayout{picture: picture, pixels: target.PixelCoordinateForPosition, capacity: capacity}
	return container.New(layout, picture)
}

// ImageFillOriginal's natural size does not account for every native framebuffer
// scale. Never let a stretching parent turn protocol pixels into logical units.
type physicalImageLayout struct {
	picture  *canvas.Image
	pixels   func(fyne.Position) (int, int)
	capacity *viewportCapacity
}

func (l *physicalImageLayout) MinSize(_ []fyne.CanvasObject) fyne.Size {
	return fyne.Size{} // A sender may shrink, but must never scroll or crop QR data.
}

func (l *physicalImageLayout) Layout(_ []fyne.CanvasObject, available fyne.Size) {
	x0, y0 := l.pixels(fyne.Position{})
	x1, y1 := l.pixels(fyne.NewPos(available.Width, available.Height))
	space := image.Pt(max(0, x1-x0), max(0, y1-y0))
	bounds := l.picture.Image.Bounds().Size()
	l.capacity.store(space, bounds)
	if bounds.X > space.X || bounds.Y > space.Y {
		l.picture.Hide()
		return
	}
	l.picture.Show()
	size, pos := physicalImageGeometry(l.picture.Image.Bounds(), available, l.pixels)
	l.picture.Move(pos)
	l.picture.Resize(size)
}

// UI writes immutable snapshots; the engine never reads widget state. Lost is
// sticky across rapid shrink/restore events so auto cannot silently resume.
type viewportState struct {
	Size image.Point
	Lost uint64
}
type viewportCapacity struct{ value atomic.Pointer[viewportState] }

func (v *viewportCapacity) load() viewportState {
	if v != nil {
		if state := v.value.Load(); state != nil {
			return *state
		}
	}
	return viewportState{}
}

func (v *viewportCapacity) store(size, picture image.Point) {
	if v == nil {
		return
	}
	old := v.load()
	if old.Size == size {
		return
	}
	if picture.X > size.X || picture.Y > size.Y {
		old.Lost++
	}
	v.value.Store(&viewportState{Size: size, Lost: old.Lost})
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
