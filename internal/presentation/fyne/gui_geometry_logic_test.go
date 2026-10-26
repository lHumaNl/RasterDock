package fyneui

import (
	"strings"
	"testing"
)

// Sources on macOS prefixes the complete native title with its owning app.
// Match that complete title, including the PID, never a substring/window index.
func geometryOwnedOption(option, ownedTitle string) bool {
	if ownedTitle == "" {
		return false
	}
	_, title, ok := strings.Cut(option, " · ")
	return ok && (title == ownedTitle || strings.HasSuffix(title, " — "+ownedTitle))
}

func TestGeometryOwnedOption(t *testing.T) {
	title := "RasterDock · dev · Sender · PID 123"
	for _, tt := range []struct {
		name, option string
		want         bool
	}{
		{"native title", "17 · " + title, true},
		{"macOS application prefix", "17 · rasterdock.test — " + title, true},
		{"other PID", "17 · rasterdock.test — " + title + "4", false},
		{"title substring", "17 · prefix " + title, false},
		{"title suffix", "17 · " + title + " extra", false},
		{"missing source ID delimiter", title, false},
		{"no source", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := geometryOwnedOption(tt.option, title); got != tt.want {
				t.Fatalf("owned option=%v want=%v", got, tt.want)
			}
		})
	}
	if geometryOwnedOption("17 · app — ", "") {
		t.Fatal("empty owned title matched")
	}
}
