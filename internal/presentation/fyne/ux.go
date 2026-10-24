package fyneui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/buildinfo"
	"rasterdock/locales"
)

type transientApp struct {
	fyne.App
	preferences fyne.Preferences
}

func (a *transientApp) Preferences() fyne.Preferences { return a.preferences }
func (a *transientApp) UniqueID() string              { return "rasterdock" }

// Fyne 2.8.2 installs no persistence when both ID and metadata ID are empty.
// Do not use New(): it probes local metadata. Its provider is internal-only,
// and accessing it logs a missing-ID warning after driver initialization.
// Override Preferences without ever accessing that provider or assigning a
// persistent ID. Application settings/state remain in the configured root.
func newTransientApp() fyne.App {
	base := app.NewWithID("")
	wrapped := &transientApp{App: base, preferences: &memoryPreferences{values: make(map[string]any)}}
	fyne.SetCurrentApp(wrapped)
	return wrapped
}

func windowTitle(c *locales.Catalog, role string, pid int) string {
	return fmt.Sprintf("%s · %s · %s · PID %d", c.Text("app.title"), buildinfo.Version, c.Text("role."+role), pid)
}

// Avoid NewSelect: its constructor translates a toolkit placeholder before a
// caller can replace it. The application catalog supplies the initial value.
func localizedSelect(c *locales.Catalog) *widget.Select {
	selectWidget := &widget.Select{PlaceHolder: c.Text("select.placeholder")}
	selectWidget.ExtendBaseWidget(selectWidget)
	return selectWidget
}

func savedPath(c *locales.Catalog, clipboard fyne.Clipboard) (*widget.Label, fyne.CanvasObject) {
	label := widget.NewLabel("")
	label.Truncation = fyne.TextTruncateEllipsis
	copyButton := widget.NewButton(c.Text("action.copy_path"), func() { clipboard.SetContent(label.Text) })
	return label, container.NewBorder(nil, nil, nil, copyButton, label)
}
