package fyneui

import (
	"image"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"

	"rasterdock/internal/application"
	"rasterdock/locales"
)

func TestGeometrySelectorsFullProfile(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	var got application.VisualProfile
	v := newVisualControls(c, func(p application.VisualProfile) { got = p })
	if got != (application.VisualProfile{}) || v.module.Selected != "4" || v.grid.Selected != "1 × 1" {
		t.Fatal("default selection")
	}
	v.module.SetSelected("8")
	v.grid.SetSelected("3 × 2")
	v.colors.SetSelected(v.colors.Options[2])
	if got != largeProfile() {
		t.Fatal("rapid selections lost a profile field", got)
	}
	v.reflect(application.DefaultVisualProfile(application.Monochrome))
	if got != largeProfile() || v.module.Selected != "4" || v.grid.Selected != "1 × 1" {
		t.Fatal("rollback callback/fields")
	}
}

func TestGeometryViewportHidesInsteadOfCropping(t *testing.T) {
	picture := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 664, 460)))
	capacity := &viewportCapacity{}
	layout := physicalImageLayout{picture: picture, capacity: capacity, pixels: func(p fyne.Position) (int, int) { return int(p.X * 2), int(p.Y * 2) }}
	if layout.MinSize(nil) != (fyne.Size{}) {
		t.Fatal("viewport forces a minimum/scroll size")
	}
	layout.Layout(nil, fyne.NewSize(332, 230))
	if !picture.Visible() || picture.Size() != fyne.NewSize(332, 230) || capacity.load().Size != image.Pt(664, 460) {
		t.Fatal("physical size")
	}
	layout.Layout(nil, fyne.NewSize(331, 230))
	if picture.Visible() || capacity.load().Lost != 1 {
		t.Fatal("cropped data visible")
	}
	layout.Layout(nil, fyne.NewSize(332, 230))
	if capacity.load().Lost != 1 {
		t.Fatal("lost resize notification")
	}
}

func TestGeometryOlderAcknowledgementPreservesIntent(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	v := newVisualControls(c, func(application.VisualProfile) {})
	v.module.SetSelected("8")
	older := v.requested
	v.grid.SetSelected("3 × 2")
	v.reconcile(snapshot{visual: older, revision: 1})
	v.colors.SetSelected(v.colors.Options[2])
	if v.requested != largeProfile() {
		t.Fatal("older acknowledgement erased pending grid", v.requested)
	}
	v.reconcile(snapshot{visual: largeProfile(), revision: 3})
	if v.active != largeProfile() || v.requested != v.active {
		t.Fatal("newest acknowledgement did not settle")
	}
	v.module.SetSelected("6")
	v.reconcile(snapshot{visual: largeProfile(), revision: 4})
	if v.module.Selected != "8" || v.requested != v.active {
		t.Fatal("failed acknowledgement did not settle")
	}
}

func TestGeometryQueueRejectionPreservesQueuedIntent(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	v := newVisualControls(c, func(application.VisualProfile) {})
	commands := make(chan command, 1)
	v.changed = func(p application.VisualProfile) { v.enqueue(commands, p) }
	v.module.SetSelected("8")
	queued := v.requested
	v.grid.SetSelected("3 × 2")
	if v.revision != 1 || v.requested != queued {
		t.Fatal("queue rejection lost accepted intent")
	}
	cmd := <-commands
	v.reconcile(snapshot{visual: cmd.visual, revision: cmd.revision})
	if v.active != queued || v.module.Selected != "8" {
		t.Fatal("queued request did not settle")
	}
}

func TestGeometryLiveDimensions(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	v := newVisualControls(c, func(application.VisualProfile) {})
	capacity := &viewportCapacity{}
	capacity.store(image.Pt(900, 700), image.Point{})
	v.refreshCapacity(capacity)
	if !strings.Contains(v.required.Text, "Available: 900 × 700") {
		t.Fatal(v.required.Text)
	}
	v.module.SetSelected("8")
	if !strings.Contains(v.required.Text, "Required: 1084 × 880") {
		t.Fatal(v.required.Text)
	}
	v.reconcile(snapshot{visual: application.DefaultVisualProfile(2), revision: 1})
	capacity.store(image.Pt(800, 600), image.Point{})
	v.refreshCapacity(capacity)
	if !strings.Contains(v.required.Text, "Required: 664 × 460") || !strings.Contains(v.required.Text, "Available: 800 × 600") {
		t.Fatal(v.required.Text)
	}
}
