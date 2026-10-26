package fyneui

import (
	"fmt"
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/application"
	"rasterdock/locales"
)

type visualControls struct {
	colors, module, grid *widget.Select
	required             *widget.Label
	active               application.VisualProfile
	requested            application.VisualProfile
	previous             application.VisualProfile
	revision             uint64
	available            image.Point
	catalog              *locales.Catalog
	changed              func(application.VisualProfile)
}

func newVisualControls(c *locales.Catalog, changed func(application.VisualProfile)) *visualControls {
	v := &visualControls{catalog: c, changed: changed, active: application.DefaultVisualProfile(application.Monochrome), required: widget.NewLabel("")}
	v.colors = profileSelect(c, func(p application.ColorProfile) { v.request(func(pv *application.VisualProfile) { pv.Colors = p }) })
	v.module = widget.NewSelect([]string{"4", "6", "8"}, func(s string) {
		v.request(func(p *application.VisualProfile) { p.Geometry.Module = map[string]int{"4": 4, "6": 6, "8": 8}[s] })
	})
	v.grid = widget.NewSelect([]string{"1 × 1", "2 × 2", "3 × 2"}, func(s string) {
		v.request(func(p *application.VisualProfile) {
			g := map[string][2]int{"1 × 1": {1, 1}, "2 × 2": {2, 2}, "3 × 2": {3, 2}}[s]
			p.Geometry.Columns, p.Geometry.Rows = g[0], g[1]
		})
	})
	v.reflect(v.active)
	return v
}

func (v *visualControls) request(change func(*application.VisualProfile)) {
	p := v.requested
	change(&p)
	v.previous = v.requested
	v.requested = p
	v.revision++
	v.dimensions()
	v.changed(p)
}

// Rejections never entered the engine queue; retain the last queued intent.
func (v *visualControls) reject() {
	v.revision--
	v.requested = v.previous
	v.display(v.requested)
}

func (v *visualControls) enqueue(commands chan<- command, profile application.VisualProfile) bool {
	select {
	case commands <- command{kind: "profile", visual: profile, revision: v.revision}:
		return true
	default:
		v.reject()
		return false
	}
}

// Every snapshot carries the last processed revision, including failures and
// coalesced status updates. Older acknowledgements update reality, not intent.
func (v *visualControls) reconcile(s snapshot) {
	v.active = s.visual
	if s.revision >= v.revision {
		v.reflect(s.visual)
	}
}

func (v *visualControls) object() fyne.CanvasObject {
	return container.NewVBox(v.colors, widget.NewLabel(v.catalog.Text("profile.module")), v.module, widget.NewLabel(v.catalog.Text("profile.grid")), v.grid, v.required)
}

func setSelection(s *widget.Select, value string) {
	callback := s.OnChanged
	s.OnChanged = nil
	s.SetSelected(value)
	s.OnChanged = callback
}

func (v *visualControls) reflect(p application.VisualProfile) {
	v.active = p
	v.requested = p
	v.display(p)
}

func (v *visualControls) display(p application.VisualProfile) {
	reflectProfile(v.colors, p.Colors)
	setSelection(v.module, fmt.Sprint(p.Geometry.Module))
	setSelection(v.grid, fmt.Sprintf("%d × %d", p.Geometry.Columns, p.Geometry.Rows))
	v.dimensions()
}

// Called only on the UI thread, including when the engine has no new event.
func (v *visualControls) refreshCapacity(capacity *viewportCapacity) {
	if size := capacity.load().Size; size != v.available {
		v.available = size
		v.dimensions()
	}
}

func (v *visualControls) dimensions() {
	size, _ := v.requested.Size()
	params := map[string]interface{}{"width": size.X, "height": size.Y, "availableWidth": v.available.X, "availableHeight": v.available.Y}
	v.required.SetText(v.catalog.Text("profile.required", params) + "\n" + v.catalog.Text("profile.available", params))
}

func profileSelect(c *locales.Catalog, changed func(application.ColorProfile)) *widget.Select {
	options := []string{c.Text("profile.monochrome"), c.Text("profile.four"), c.Text("profile.eight")}
	s := widget.NewSelect(options, nil)
	s.SetSelected(options[0])
	s.OnChanged = func(value string) {
		profile := application.Monochrome
		if value == options[1] {
			profile = application.Colors4
		}
		if value == options[2] {
			profile = application.Colors8
		}
		changed(profile)
	}
	return s
}

func reflectProfile(s *widget.Select, profile application.ColorProfile) {
	index := 0
	if profile == application.Colors4 {
		index = 1
	}
	if profile == application.Colors8 {
		index = 2
	}
	callback := s.OnChanged
	s.OnChanged = nil // Engine acknowledgement must not enqueue another switch.
	s.SetSelected(s.Options[index])
	s.OnChanged = callback
}
