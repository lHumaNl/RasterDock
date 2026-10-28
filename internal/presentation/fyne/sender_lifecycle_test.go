package fyneui

import (
	"errors"
	"testing"
	"time"
)

func TestStaticSenderTicksDoNotPublishHeartbeat(t *testing.T) {
	v, deps := viewerFixture(t)
	ticks := make(chan time.Time, 3)
	commands := make(chan command)
	deps.ticks = ticks
	done := make(chan error, 1)
	go func() {
		done <- v.loop(t.Context(), commands, func(snapshot) { t.Error("static page republished without command") }, deps)
	}()
	for range 3 {
		ticks <- time.Now()
	}
	commands <- command{kind: "pause"}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v.state.Frame != 0 {
		t.Fatal("frame changed on idle ticks")
	}
}

func TestSenderIdleTimeoutOffAndValidCommandReset(t *testing.T) {
	v, deps := viewerFixture(t)
	v.lastCommandAt = time.Now().Add(-time.Hour)
	if err := v.waiting(t.Context(), deps); err != nil {
		t.Fatal("default timeout must be disabled", err)
	}
	v.idleTimeout = time.Minute
	if err := v.waiting(t.Context(), deps); !errors.Is(err, errSenderIdleTimeout) {
		t.Fatal(err)
	}
	old := v.lastCommandAt
	viewerKeys(t, v, deps, []string{"F8", "junk"}, func(snapshot) { t.Fatal("invalid command rendered") })
	if !v.lastCommandAt.Equal(old) {
		t.Fatal("invalid input renewed timeout")
	}
	viewerKeys(t, v, deps, []string{"F8", "1", "Tab", "0", "Return"}, func(s snapshot) { acknowledgeControl(s) })
	if !v.lastCommandAt.After(old) || v.waiting(t.Context(), deps) != nil {
		t.Fatal("valid command failed to renew timer")
	}
}

func TestTimerClosesAfterThreeCyclesAndFinalAppliedDwell(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "auto"}) // Zero means the public default of three cycles.
	var last snapshot
	for n := uint64(0); n < h.prepared.PacketCount()*3; n++ {
		h.ticks <- time.Now()
		last = h.expect("status.timer_frame")
	}
	if last.applied == nil || last.params["cycle"] != uint64(3) {
		t.Fatal("missing third-cycle final publication")
	}
	// No UI acknowledgement: another tick must not close or render another page.
	h.ticks <- time.Now()
	h.send(command{kind: "next"})
	h.expect("status.busy") // Serialization barrier after the unacknowledged tick.
	select {
	case <-h.done:
		t.Fatal("closed before final page was applied")
	default:
	}
	acknowledgeControl(last)
	h.ticks <- time.Now() // Observe application; allow one further whole tick.
	h.send(command{kind: "next"})
	h.expect("status.busy")
	h.ticks <- time.Now()
	h.expect("sender.cycles_closed")
	select {
	case <-h.done:
	case <-time.After(testTimeout):
		t.Fatal("timer did not exit engine")
	}
}
