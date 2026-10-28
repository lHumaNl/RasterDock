package fyneui

import (
	"context"
	"image"
	"math"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/application"
	"rasterdock/locales"
)

type command struct {
	cycles                             uint64
	idleTimeout                        time.Duration
	resume                             *receiveResume
	controlOwner                       uint64
	revision                           uint64
	kind, source, destination, session string
	window                             uint32
	profile                            application.ColorProfile
	visual                             application.VisualProfile
	physicalKey                        string
	observed                           time.Time
}

type snapshot struct {
	canResume                  bool
	closeApplication           bool
	controlOwner               uint64
	finishedControl            bool
	revision                   uint64
	profile                    application.ColorProfile
	visual                     application.VisualProfile
	role, key                  string
	params                     map[string]interface{}
	picture                    image.Image
	pictureID                  pictureIdentity
	sources                    []platform.Source
	session, prepared          string
	controlled, releaseControl bool
	applied                    chan struct{}
	publication                *controlPublication
	viewportLost               uint64
}

type controlUI struct {
	readIdleTimeout func() (time.Duration, error)
	ctx             context.Context
	owner           uint64
	restore         func()
	window          fyne.Window
	catalog         *locales.Catalog
	commands        chan<- command
	authorized      atomic.Bool
	pending         atomic.Bool // Remains latched until the old engine owner releases its epoch.
	immersive       fyne.CanvasObject
	active          bool
	clear           func()
	quarantine      fyne.CanvasObject
	quarantined     bool
	requestClose    func()
}

func (u *controlUI) authorizedNow() bool {
	// Local target focus is exclusively the receiver/input adapter's job.
	return u.authorized.Load()
}

func (u *controlUI) poll() {
	if u.active && !u.authorized.Load() {
		u.stop()
	}
}

func (u *controlUI) stop() {
	u.authorized.Store(false)
	u.enterQuarantine()
	if u.clear != nil {
		u.clear()
	}
	select {
	case u.commands <- command{kind: "pause"}:
	default:
	}
}

// Hardware codes are macOS virtual key codes, independent of keyboard layout.
// Other backends remain unavailable until their physical mapping is qualified.
func physicalControlKey(code fyne.HardwareKey) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	keys := map[int]string{100: "F8", 101: "F9", 48: "Tab", 36: "Return", 53: "Escape",
		29: "0", 18: "1", 19: "2", 20: "3", 21: "4", 23: "5", 22: "6", 26: "7", 28: "8", 25: "9",
		82: "0", 83: "1", 84: "2", 85: "3", 86: "4", 87: "5", 88: "6", 89: "7", 91: "8", 92: "9", 76: "Enter"}
	return keys[code.ScanCode]
}

func (u *controlUI) key(event *fyne.KeyEvent) {
	key := physicalControlKey(event.Physical)
	if (key == "F8" || key == "F9") && !u.authorized.Load() {
		u.enterQuarantine()
		return
	}
	u.controlKey(event, "control_key")
}

func (u *controlUI) keyUp(event *fyne.KeyEvent) {
	u.controlKey(event, "control_key_up")
}

func (u *controlUI) controlKey(event *fyne.KeyEvent, kind string) {
	if !u.active || !u.authorized.Load() {
		return
	}
	key := physicalControlKey(event.Physical)
	if key == "Escape" {
		u.stop()
		return
	}
	if u.window.Canvas().Focused() != nil {
		return
	}
	select {
	case u.commands <- command{kind: kind, controlOwner: u.owner, physicalKey: key, observed: time.Now()}:
	default:
		u.stop() // A missing key must never leave a command continuation armed.
	}
}

func (u *controlUI) arm() {
	var idleTimeout time.Duration
	if u.readIdleTimeout != nil {
		var err error
		idleTimeout, err = u.readIdleTimeout()
		if err != nil {
			dialog.ShowError(err, u.window)
			return
		}
	}
	if u.owner == math.MaxUint64 {
		return
	}
	if _, ok := u.window.Canvas().(desktop.Canvas); !ok || runtime.GOOS != "darwin" {
		return
	}
	if u.pending.Swap(true) {
		return
	}
	u.window.SetTitle(windowTitle(u.catalog, "sender", os.Getpid()))
	if !u.authorize() {
		u.pending.Store(false)
		return
	}
	u.owner++
	select {
	case u.commands <- command{kind: "control_show", controlOwner: u.owner, idleTimeout: idleTimeout}:
	default:
		u.authorized.Store(false)
		u.pending.Store(false)
	}
}

func (u *controlUI) authorize() bool {
	u.authorized.Store(true)
	return true
}

func (u *controlUI) install(viewport fyne.CanvasObject) {
	// Instructions belong to the pre-arm form, not the physical QR area. A
	// footer can hide an otherwise fitting canvas when switching at high DPI.
	u.immersive = viewport
	u.quarantine = newPointerExit(u.catalog.Text("control.quarantine"), u.exitQuarantine)
	if c, ok := u.window.Canvas().(desktop.Canvas); ok {
		c.SetOnKeyDown(u.key)
		c.SetOnKeyUp(u.keyUp)
	}
}

func (u *controlUI) apply(s snapshot) {
	if s.releaseControl {
		if s.controlOwner != u.owner || !u.pending.Load() {
			return
		}
		finished := s.finishedControl && (u.ctx == nil || u.ctx.Err() == nil) &&
			u.window != nil && u.window.Canvas().Focused() == nil && u.authorizedNow()
		u.authorized.Store(false)
		u.pending.Store(false)
		if u.clear != nil {
			u.clear()
		}
		if finished && u.restore != nil {
			u.active, u.quarantined = false, false
			u.window.Canvas().Unfocus()
			u.restore()
			u.window.Canvas().Unfocus()
			return
		}
		u.enterQuarantine()
	}
	want := s.controlled && u.authorized.Load()
	if want == u.active {
		return
	}
	u.active = want
	if want {
		u.window.Canvas().Unfocus()
		u.window.SetContent(u.immersive)
	} else {
		u.enterQuarantine()
	}
}

func (u *controlUI) enterQuarantine() {
	u.active, u.quarantined = false, true
	if u.window == nil {
		return
	}
	u.window.Canvas().Unfocus()
	u.window.SetContent(u.quarantine)
}

func (u *controlUI) exitQuarantine() {
	if u.pending.Load() {
		return
	}
	u.window.Canvas().Unfocus()
	if u.requestClose != nil {
		u.requestClose()
	}
}

// Deliberately not Focusable: a late Tab/Enter sequence cannot leave the sink.
// Fyne does not deliver canvas physical callbacks when ordinary fields have
// focus. This abort path closes the window; only validated terminal keyup may
// restore the ordinary form through the separate owner-scoped completion path.
type pointerExit struct {
	widget.BaseWidget
	text string
	exit func()
}

func newPointerExit(text string, exit func()) *pointerExit {
	p := &pointerExit{text: text, exit: exit}
	p.ExtendBaseWidget(p)
	return p
}

func (p *pointerExit) CreateRenderer() fyne.WidgetRenderer {
	label := widget.NewLabel(p.text)
	label.Wrapping = fyne.TextWrapWord
	return widget.NewSimpleRenderer(label)
}

func (p *pointerExit) Tapped(*fyne.PointEvent) { p.exit() }

func (u *controlUI) paint(s snapshot, paint func()) {
	if s.controlled && (!s.publication.valid() || !s.publication.spaceValid() || !u.authorizedNow()) {
		s.publication.revoke()
		u.stop()
		return
	}
	paint()
}

func acknowledgeControl(s snapshot) {
	if s.publication != nil && !s.publication.valid() {
		return
	}
	if s.applied != nil {
		select {
		case s.applied <- struct{}{}:
		default:
		}
	}
}

func (u *controlUI) guard(s snapshot) snapshot {
	if s.controlled && (!s.publication.valid() || !u.authorizedNow()) {
		s.publication.revoke()
		u.stop()
		s.picture = image.NewRGBA(image.Rect(0, 0, 1, 1))
		s.applied = nil // Rejected publication must time out, not commit.
	}
	return s
}

func (u *controlUI) acknowledge(s snapshot, bounds image.Rectangle, visible bool) {
	if s.controlled && (!s.publication.layoutValid(bounds, visible) || !u.authorizedNow()) {
		s.publication.revoke()
		u.stop()
		return
	}
	acknowledgeControl(s)
}

func controlledReceiveButton(c *locales.Catalog, w fyne.Window, send func(string)) *widget.Button {
	return widget.NewButton(c.Text("action.control_receive"), func() {
		if err := platform.InputCapability(); err != nil {
			dialog.ShowError(err, w)
			return
		}
		if err := platform.ControlCaptureCapability(); err != nil {
			dialog.ShowError(err, w)
			return
		}
		dialog.ShowConfirm(c.Text("action.control_receive"), c.Text("control.confirm"), func(ok bool) {
			if ok {
				send("control_receive")
			}
		}, w)
	})
}
