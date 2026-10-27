package fyneui

import (
	"context"
	"image"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func viewerFixture(t *testing.T) (*controlledViewer, engineIO) {
	t.Helper()
	io := engineIO{viewport: &viewportCapacity{}, viewerFocused: func() bool { return true }}
	io.viewport.store(image.Pt(3000, 2000), image.Point{})
	v, err := newControlledViewer(prepareFixture(t), application.DefaultVisualProfile(application.Monochrome), 0, io)
	if err != nil {
		t.Fatal(err)
	}
	return v, io
}

func viewerKeys(t *testing.T, v *controlledViewer, io engineIO, keys []string, emit func(snapshot)) {
	t.Helper()
	for _, key := range keys {
		if err := v.key(t.Context(), command{physicalKey: key, observed: time.Now()}, emit, io); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlledViewerHeldHeartbeatAndReplay(t *testing.T) {
	v, io := viewerFixture(t)
	var frames []snapshot
	emit := func(s snapshot) { frames = append(frames, s); acknowledgeControl(s) }
	if err := v.publish(t.Context(), 0, 0, emit, io); err != nil {
		t.Fatal(err)
	}
	keys := []string{"F8", "1", "Tab", "1", "Return"}
	viewerKeys(t, v, io, keys, emit)
	viewerKeys(t, v, io, keys, emit)
	if err := v.publish(t.Context(), v.ordinal, v.state.Nonce, emit, io); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 4 {
		t.Fatal(len(frames))
	}
	for i, s := range frames {
		assertControlledFrame(t, s, uint64(i+1), v.state.Epoch, uint64(min(i, 1)))
	}
}

func assertControlledFrame(t *testing.T, s snapshot, frame, epoch, nonce uint64) {
	t.Helper()
	b, _, err := codec.DecodeControlled(s.picture)
	if err != nil {
		t.Fatal(err)
	}
	if b.Frame != frame || b.Epoch != epoch || b.Nonce != nonce || b.Ordinal != nonce || b.Flags != 3 {
		t.Fatalf("unexpected live beacon: %+v", b)
	}
}

func TestControlledViewerInvalidAndRenderFailureDoNotCommit(t *testing.T) {
	v, io := viewerFixture(t)
	emit := func(s snapshot) { acknowledgeControl(s) }
	viewerKeys(t, v, io, []string{"F8", "1", "Tab", "-", "Return"}, emit)
	if v.state.Nonce != 0 {
		t.Fatal("invalid command consumed nonce")
	}
	viewerKeys(t, v, io, []string{"F8", "1", "Tab", "1"}, emit)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := v.key(ctx, command{physicalKey: "Return", observed: time.Now()}, func(snapshot) {}, io)
	if err == nil || v.state.Nonce != 0 || v.state.Frame != 0 {
		t.Fatal("failed publication committed", err)
	}
	viewerKeys(t, v, io, []string{"Return"}, emit)
	if v.state.Nonce != 1 {
		t.Fatal("failed render consumed parser nonce")
	}
}

func TestControlledViewerFocusAndFitStop(t *testing.T) {
	for _, reason := range []string{"focus", "fit", "lost"} {
		t.Run(reason, func(t *testing.T) {
			v, io := viewerFixture(t)
			switch reason {
			case "focus":
				io.viewerFocused = func() bool { return false }
			case "fit":
				io.viewport.store(image.Pt(1, 1), image.Point{})
			case "lost":
				io.viewport.value.Store(&viewportState{Size: image.Pt(3000, 2000), Lost: 1})
			}
			if err := v.publish(t.Context(), 0, 0, func(snapshot) { t.Fatal("published after loss") }, io); err == nil {
				t.Fatal("loss ignored")
			}
		})
	}
}

func TestControlledViewerMissingPreparedReleases(t *testing.T) {
	var released bool
	err := runControlledViewer(t.Context(), nil, application.VisualProfile{}, 0, nil, func(s snapshot) { released = s.releaseControl && s.picture != nil }, engineIO{})
	if err == nil || !released {
		t.Fatal("missing source did not restore UI")
	}
}

func TestControlledViewerWaitsForAppliedCanvas(t *testing.T) {
	v, io := viewerFixture(t)
	events, done := make(chan snapshot, 1), make(chan error, 1)
	go func() { done <- v.publish(t.Context(), 1, 1, func(s snapshot) { events <- s }, io) }()
	s := <-events
	if v.state.Frame != 0 || v.state.Nonce != 0 || v.ordinal != 0 {
		t.Fatal("committed before UI apply")
	}
	acknowledgeControl(s)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v.state.Frame != 1 || v.state.Nonce != 1 || v.ordinal != 1 {
		t.Fatal("missing committed publication")
	}
}

func TestControlledViewerFrameExhaustion(t *testing.T) {
	v, io := viewerFixture(t)
	v.state.Frame = math.MaxUint64
	if err := v.publish(t.Context(), 0, 0, func(snapshot) { t.Fatal("published wrapped frame") }, io); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestControlledViewerFocusLossEndsLoop(t *testing.T) {
	v, io := viewerFixture(t)
	ticks := make(chan time.Time, 1)
	var focused atomic.Bool
	io.ticks, io.viewerFocused = ticks, focused.Load
	done := make(chan error, 1)
	go func() { done <- v.loop(t.Context(), nil, func(snapshot) { t.Error("published after focus loss") }, io) }()
	ticks <- time.Now()
	if err := <-done; err == nil {
		t.Fatal("focus loss did not stop viewer")
	}
	focused.Store(true)
	ticks <- time.Now() // No owner remains to auto-resume after focus is restored.
}

func TestControlledViewerOtherModesDisarm(t *testing.T) {
	for _, kind := range []string{"auto", "receive", "profile", "next", "pause"} {
		v, io := viewerFixture(t)
		commands := make(chan command, 1)
		commands <- command{kind: kind}
		if err := v.loop(t.Context(), commands, func(snapshot) { t.Fatal("other mode raced viewer") }, io); err != nil {
			t.Fatal(err)
		}
	}
}
