package fyneui

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/config"
	"rasterdock/locales"
)

const defaultTimerCycles = config.DefaultTimerCycles

// Options are captured when a mode starts, not changed underneath a live owner.
type senderOptions struct {
	cycles, idle     *widget.Entry
	catalog          *locales.Catalog
	persistenceError *widget.Label
}

func newSenderOptions(c *locales.Catalog) *senderOptions {
	o := &senderOptions{cycles: widget.NewEntry(), idle: widget.NewEntry(), catalog: c, persistenceError: widget.NewLabel("")}
	o.persistenceError.Wrapping = fyne.TextWrapWord
	o.persistenceError.Hide()
	o.cycles.SetText(strconv.FormatUint(config.DefaultTimerCycles, 10))
	o.idle.SetText("0")
	return o
}

func newPersistentSenderOptions(c *locales.Catalog, path string) *senderOptions {
	o := newSenderOptions(c)
	settings, err := config.LoadSettings(path)
	if err != nil {
		settings = config.DefaultSettings()
		o.showPersistenceError("sender.settings_load_failed", err)
	}
	// Initialization never writes: attach callbacks only after loading both fields.
	o.cycles.SetText(strconv.FormatUint(settings.TimerCycles, 10))
	o.idle.SetText(strconv.FormatUint(settings.IdleTimeoutSeconds, 10))
	save := func() {
		if err := config.SaveSenderSettings(path, settings.TimerCycles, settings.IdleTimeoutSeconds); err != nil {
			o.showPersistenceError("sender.settings_save_failed", err)
			return
		}
		o.persistenceError.Hide()
	}
	// Each valid field updates independently; incomplete input retains its last
	// valid value. A later edit retries both pending values after a write failure.
	o.cycles.OnChanged = func(string) {
		if n, err := o.cycleLimit(); err == nil {
			settings.TimerCycles = n
			save()
		}
	}
	o.idle.OnChanged = func(string) {
		if timeout, err := o.timeout(); err == nil {
			settings.IdleTimeoutSeconds = uint64(timeout / time.Second) //nolint:gosec // timeout validates 0..86400 seconds before conversion.
			save()
		}
	}
	return o
}

func (o *senderOptions) showPersistenceError(key string, err error) {
	o.persistenceError.SetText(o.catalog.Text(key, map[string]interface{}{"reason": err.Error()}))
	o.persistenceError.Show()
}

func (o *senderOptions) object() fyne.CanvasObject {
	return container.NewVBox(widget.NewLabel(o.catalog.Text("sender.cycles")), o.cycles,
		widget.NewLabel(o.catalog.Text("sender.idle_timeout")), o.idle, o.persistenceError)
}

func (o *senderOptions) cycleLimit() (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(o.cycles.Text), 10, 64)
	if err != nil || n == 0 || n > config.MaxTimerCycles {
		return 0, errors.New(o.catalog.Text("sender.invalid_cycles"))
	}
	return n, nil
}

func (o *senderOptions) timeout() (time.Duration, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(o.idle.Text), 10, 64)
	if err != nil || n > config.MaxIdleTimeoutSeconds {
		return 0, errors.New(o.catalog.Text("sender.invalid_timeout"))
	}
	return time.Duration(n) * time.Second, nil
}
