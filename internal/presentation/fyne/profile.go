package fyneui

import (
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/application"
	"rasterdock/locales"
)

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
