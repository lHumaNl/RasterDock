package fyneui

import (
	"context"
	"image"
	"runtime"
	"testing"

	"fyne.io/fyne/v2"
)

func TestPhysicalControlKeys(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS physical mapping")
	}
	for code, want := range map[int]string{100: "F8", 48: "Tab", 36: "Return", 53: "Escape", 18: "1", 0: ""} {
		if got := physicalControlKey(fyne.HardwareKey{ScanCode: code}); got != want {
			t.Fatalf("%d: %q", code, got)
		}
	}
}

func TestPublicationRejectsLostLayout(t *testing.T) {
	for _, restored := range []bool{false, true} {
		space := &viewportCapacity{}
		space.store(image.Pt(100, 100), image.Point{})
		p := newControlPublication(t.Context(), snapshot{picture: image.NewRGBA(image.Rect(0, 0, 100, 100))}, space, nil)
		space.store(image.Pt(1, 1), image.Pt(100, 100))
		if restored {
			space.store(image.Pt(100, 100), image.Pt(100, 100))
		}
		if p.layoutValid(image.Rect(0, 0, 100, 100), restored) {
			t.Fatal("lost generation acknowledged")
		}
	}
}

func TestPublicationValidAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	space := &viewportCapacity{}
	space.store(image.Pt(100, 100), image.Point{})
	p := newControlPublication(ctx, snapshot{picture: image.NewRGBA(image.Rect(0, 0, 100, 100))}, space, nil)
	if !p.layoutValid(image.Rect(0, 0, 100, 100), true) {
		t.Fatal("valid layout rejected")
	}
	if p.layoutValid(image.Rect(0, 0, 99, 100), true) || p.layoutValid(p.bounds, false) {
		t.Fatal("wrong or hidden image accepted")
	}
	cancel()
	if p.valid() {
		t.Fatal("shutdown publication remains live")
	}
}

func TestLatePublicationAfterTimeoutIsRevoked(t *testing.T) {
	events, done := make(chan snapshot, 1), make(chan error, 1)
	s := snapshot{picture: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	s.publication = newControlPublication(t.Context(), s, nil, nil)
	go func() { done <- awaitControlApply(t.Context(), s, func(s snapshot) { events <- s }) }()
	late := <-events
	if err := <-done; err == nil {
		t.Fatal("missing timeout")
	}
	if late.publication.valid() {
		t.Fatal("late callback can paint expired publication")
	}
	u := &controlUI{}
	u.authorized.Store(true)
	late.controlled = true
	u.paint(late, func() { t.Fatal("late callback painted expired frame") })
	if u.authorized.Load() {
		t.Fatal("expired epoch remains authorized")
	}
	acknowledgeControl(late)
	select {
	case <-late.applied:
		t.Fatal("expired publication acknowledged")
	default:
	}
}

func TestAppliedPublicationChecksLayoutBeforeAcknowledgement(t *testing.T) {
	for _, restore := range []bool{false, true} {
		v, io := viewerFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		u := &controlUI{focusState: func() (bool, uint64) { return true, 0 }}
		u.authorized.Store(true)
		err := v.publish(ctx, 1, 1, func(s snapshot) {
			io.viewport.store(image.Pt(1, 1), s.picture.Bounds().Size())
			if restore {
				io.viewport.store(image.Pt(3000, 2000), s.picture.Bounds().Size())
			}
			u.paint(s, func() { t.Error("painted after generation loss") })
			u.acknowledge(s, s.picture.Bounds(), restore)
			select {
			case <-s.applied:
				t.Error("acknowledged lost layout")
			default:
			}
			cancel()
		}, io)
		if err == nil || v.state.Nonce != 0 || v.ordinal != 0 || u.authorized.Load() {
			t.Fatal("lost layout committed")
		}
	}
}

func TestValidUIPublicationCommits(t *testing.T) {
	v, io := viewerFixture(t)
	u := &controlUI{focusState: func() (bool, uint64) { return true, 0 }}
	u.authorized.Store(true)
	painted := false
	err := v.publish(t.Context(), 1, 1, func(s snapshot) {
		u.paint(s, func() { painted = true })
		u.acknowledge(s, s.picture.Bounds(), true)
	}, io)
	if err != nil || !painted || v.state.Nonce != 1 || v.ordinal != 1 {
		t.Fatal("valid publication not committed", err)
	}
}

func TestQuarantineCannotExitThroughKeyboard(t *testing.T) {
	p := newPointerExit("Stopped", func() { t.Fatal("keyboard invoked pointer exit") })
	if _, ok := any(p).(fyne.Focusable); ok {
		t.Fatal("quarantine exit is focusable")
	}
	u := &controlUI{}
	u.stop()
	for _, code := range []int{100, 18, 48, 18, 36, 53} {
		u.key(&fyne.KeyEvent{Physical: fyne.HardwareKey{ScanCode: code}})
	}
	if !u.quarantined || u.authorized.Load() {
		t.Fatal("late sequence escaped quarantine")
	}
}

func TestFocusGenerationLatchesLoss(t *testing.T) {
	generation := uint64(4)
	u := &controlUI{focusState: func() (bool, uint64) { return true, generation }}
	u.focusGeneration.Store(generation)
	u.authorized.Store(true)
	if !u.focused() {
		t.Fatal("initial focus rejected")
	}
	generation++ // The window lost and regained focus between polls.
	if u.focused() || u.authorized.Load() {
		t.Fatal("loss was not latched")
	}
	generation--
	if u.focused() {
		t.Fatal("authorization silently restored")
	}
	generation += 2
	if !u.authorizeFocus() || !u.focused() {
		t.Fatal("explicit new arm did not reset baseline")
	}
}

func TestControlStopOverflowRevokes(t *testing.T) {
	commands := make(chan command, 1)
	commands <- command{kind: "control_key"}
	u := &controlUI{commands: commands}
	u.authorized.Store(true)
	u.stop()
	if u.authorized.Load() {
		t.Fatal("overflow retained authorization")
	}
}

func TestControlMailboxCarriesPictureAcknowledgement(t *testing.T) {
	events := make(chan snapshot, 1)
	ack := make(chan struct{}, 1)
	p := newControlPublication(t.Context(), snapshot{}, nil, nil)
	emitSnapshot(events, snapshot{controlled: true, applied: ack, publication: p, viewportLost: 7, picture: image.NewRGBA(image.Rect(0, 0, 1, 1)), pictureID: pictureIdentity{Frame: 1}})
	emitSnapshot(events, snapshot{key: "status.busy"})
	s := <-events
	if s.applied != ack || !s.controlled || s.pictureID.Frame != 1 || s.publication != p || s.viewportLost != 7 {
		t.Fatal("lost publication identity")
	}
	acknowledgeControl(s)
	select {
	case <-ack:
	default:
		t.Fatal("missing acknowledgement")
	}
}

func TestLayoutLossDuringPaintingRejectsAcknowledgement(t *testing.T) {
	v, io := viewerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	u := &controlUI{focusState: func() (bool, uint64) { return true, 0 }}
	u.authorized.Store(true)
	err := v.publish(ctx, 1, 1, func(s snapshot) {
		u.paint(s, func() { io.viewport.store(image.Pt(1, 1), s.picture.Bounds().Size()) })
		u.acknowledge(s, s.picture.Bounds(), false)
		select {
		case <-s.applied:
			t.Error("hidden frame acknowledged")
		default:
		}
		cancel()
	}, io)
	if err == nil || v.state.Nonce != 0 || !u.quarantined {
		t.Fatal("layout loss committed")
	}
}
