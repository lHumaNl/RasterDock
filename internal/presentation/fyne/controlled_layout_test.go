package fyneui

import (
	"image"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"rasterdock/locales"
)

type controlledScaledCanvas struct{ fyne.Canvas }

func (c controlledScaledCanvas) PixelCoordinateForPosition(p fyne.Position) (int, int) {
	return int(p.X * 2), int(p.Y * 2)
}

// Uses only the software test driver, never a native window or capture API.
func TestControlledInitialLayoutAdmission(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	w := a.NewWindow("headless admission")
	defer w.Close()
	catalog, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	capacity := &viewportCapacity{}
	picture := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	viewport := newQRViewport(picture, controlledScaledCanvas{w.Canvas()}, capacity)
	u := &controlUI{window: w, catalog: catalog}
	u.authorized.Store(true)
	u.clear = func() {
		picture.Image = image.NewRGBA(image.Rect(0, 0, 1, 1))
		picture.Refresh()
		viewport.Refresh()
	}
	trace := func(stage string) {
		t.Logf("%s capacity=%+v image=%v visible=%t active=%t quarantine=%t authorized=%t", stage, capacity.load(), picture.Image.Bounds(), picture.Visible(), u.active, u.quarantined, u.authorized.Load())
	}
	u.install(viewport)
	trace("installed")
	split := container.NewHSplit(container.NewScroll(widget.NewLabel("controls")), viewport)
	split.SetOffset(0.4)
	w.SetContent(split)
	w.Resize(fyne.NewSize(607, 262))
	picture.Image = image.NewRGBA(image.Rect(0, 0, 664, 460))
	picture.Refresh()
	viewport.Refresh()
	trace("prepared")
	initial := capacity.load()
	if initial.Size.X < 664 || initial.Size.Y < 460 || !picture.Visible() {
		t.Fatal("fixture prepared frame does not fit", initial)
	}
	v, io := viewerFixture(t)
	v.lost = initial.Lost
	io.viewport, io.viewerAuthorized = capacity, u.authorizedNow
	emit := func(s snapshot) {
		trace("before guard")
		s = u.guard(s)
		u.apply(s)
		trace("after apply before paint")
		u.paint(s, func() {
			picture.Image = s.picture
			picture.Refresh()
			trace("after image refresh")
			viewport.Refresh()
			trace("after viewport refresh")
		})
		trace("before ack")
		u.acknowledge(s, picture.Image.Bounds(), picture.Visible())
		trace("after ack")
	}
	err = v.openSurface(t.Context(), emit, io)
	if err != nil {
		t.Fatalf("fitting initial immersive surface rejected: %v", err)
	}
	if capacity.load().Lost != initial.Lost || !u.active || u.quarantined {
		t.Fatal("successful admission changed generation or entered quarantine")
	}
	if err := v.publish(t.Context(), 0, 0, emit, io); err != nil {
		t.Fatal("first full controlled publication failed", err)
	}
	if !picture.Visible() || picture.Size().Width*2 != float32(picture.Image.Bounds().Dx()) || picture.Size().Height*2 != float32(picture.Image.Bounds().Dy()) {
		t.Fatal("full frame hidden or scaled", picture.Size(), picture.Image.Bounds())
	}
}
