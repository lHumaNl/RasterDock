package fyneui

import (
	"context"
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"rasterdock/internal/adapters/platform"
)

func TestFinishViewerRequiresRelease(t *testing.T) {
	v, io := viewerFixture(t)
	viewerKeys(t, v, io, []string{"F9", "1", "Return"}, func(snapshot) { t.Fatal("finish rendered a page") })
	if err := v.keyUp(t.Context(), command{physicalKey: "Return", observed: time.Now()}, io); !errors.Is(err, errViewerFinished) {
		t.Fatalf("missing terminal release: %v", err)
	}
}

func TestFinishReceiveSavedBeforeOneTerminalAttempt(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := &controlledScene{p: prepareFixture(t)}
		if fail {
			s.finishErr = errInjected
		}
		root := t.TempDir()
		last := snapshot{}
		err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(v snapshot) { last = v }, sceneDependencies(s, root))
		want := "control.complete"
		if fail {
			want = "control.complete_finish_unconfirmed"
		}
		if err != nil || last.key != want || s.finishes != 1 || s.finishNonce == 0 || s.sends != 0 || s.inputClose != 1 {
			t.Fatalf("finish result: %v key=%s attempts=%d nonce=%d", err, last.key, s.finishes, s.finishNonce)
		}
	}
}

type finishlessInput struct {
	platform.RefreshingInputBinding
}

func TestFinishReceiveMissingCapabilityStillSaved(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) { return finishlessInput{sceneInput{s}}, nil }
	last := snapshot{}
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(v snapshot) { last = v }, deps)
	if err != nil || last.key != "control.complete_finish_unconfirmed" || s.finishes != 0 {
		t.Fatal("saved data misreported", err, last.key)
	}
}

func TestFinishViewerCancelAndFocusWin(t *testing.T) {
	for _, failure := range []string{"cancel", "focus", "owner"} {
		t.Run(failure, func(t *testing.T) {
			v, io := viewerFixture(t)
			viewerKeys(t, v, io, []string{"F9", "1", "Return"}, func(snapshot) {})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := command{physicalKey: "Return", observed: time.Now()}
			if failure == "cancel" {
				cancel()
			}
			if failure == "focus" {
				io.viewerAuthorized = func() bool { return false }
			}
			if failure == "owner" {
				cmd.controlOwner = 99
			}
			if err := v.keyUp(ctx, cmd, io); errors.Is(err, errViewerFinished) {
				t.Fatal("unsafe terminal release")
			}
		})
	}
}

func TestFinishUIRestoresOnlyCurrentAuthorizedOwner(t *testing.T) {
	for _, failure := range []string{"", "cancel", "focus", "owner", "duplicate", "abort"} {
		t.Run(failure, func(t *testing.T) {
			form := widget.NewEntry()
			form.SetText("preserved")
			w := test.NewWindow(form)
			defer w.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			u := &controlUI{window: w, owner: 7, ctx: ctx, restore: func() { w.SetContent(form) }}
			u.immersive = widget.NewLabel("held")
			u.quarantine = newPointerExit("stopped", func() {})
			u.pending.Store(true)
			u.authorized.Store(true)
			u.active = true
			w.SetContent(u.immersive)
			s := snapshot{releaseControl: true, finishedControl: true, controlOwner: 7}
			switch failure {
			case "cancel":
				cancel()
			case "focus":
				unexpected := widget.NewEntry()
				w.SetContent(unexpected)
				w.Canvas().Focus(unexpected) // Focused widget, not OS window focus.
			case "owner":
				s.controlOwner--
			case "duplicate":
				u.pending.Store(false)
			case "abort":
				s.finishedControl = false
			}
			u.apply(s)
			if (w.Content() == form) != (failure == "") {
				t.Fatal("incorrect restoration", failure)
			}
			if failure == "" && (u.authorized.Load() || u.pending.Load() || u.active || u.quarantined || w.Canvas().Focused() != nil || form.Text != "preserved") {
				t.Fatal("restored control retained authorization or changed values")
			}
		})
	}
}

func TestFinishMailboxKeepsReleaseOwnerTogether(t *testing.T) {
	for _, next := range []snapshot{{key: "status.busy"}, {releaseControl: true, controlOwner: 8}} {
		events := make(chan snapshot, 1)
		emitSnapshot(events, snapshot{releaseControl: true, finishedControl: true, controlOwner: 7})
		emitSnapshot(events, next)
		got := <-events
		if next.releaseControl {
			if got.controlOwner != 8 || got.finishedControl {
				t.Fatal("old completion attached to new owner")
			}
		} else if !got.releaseControl || !got.finishedControl || got.controlOwner != 7 {
			t.Fatal("lost completion release")
		}
	}
}

func TestFinishOldReleaseCannotDisarmRearmedViewer(t *testing.T) {
	w := test.NewWindow(widget.NewLabel("new owner"))
	defer w.Close()
	u := &controlUI{window: w, owner: 8, active: true, restore: func() { t.Fatal("restored wrong owner") }}
	u.authorized.Store(true)
	u.pending.Store(true)
	u.apply(snapshot{releaseControl: true, finishedControl: true, controlOwner: 7})
	if !u.authorized.Load() || !u.pending.Load() || !u.active {
		t.Fatal("old owner disarmed new viewer")
	}
}

type finishCloseFailure struct{ *controlledScene }

func (s finishCloseFailure) Close() error { s.closes++; return errInjected }

func TestFinishCleanupFailurePreservesSavedOutcome(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.controlCapture = func(uint32) (captureStream, error) { return finishCloseFailure{s}, nil }
	last := snapshot{}
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(v snapshot) { last = v }, deps)
	if err != nil || last.key != "control.complete_finish_unconfirmed" || s.finishes != 1 {
		t.Fatal("cleanup misreported saved files", err, last.key)
	}
}
