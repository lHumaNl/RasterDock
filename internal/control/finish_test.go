package control

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func stageFinish(t *testing.T, p *Parser, text, terminal string) {
	t.Helper()
	for _, key := range append(append([]string{"F9"}, strings.Split(text, "")...), terminal) {
		if key == "\t" {
			key = "Tab"
		}
		if _, ok := p.Key(key, start, 9); ok {
			t.Fatal("finish returned page")
		}
	}
}

func TestFinishMatchingRelease(t *testing.T) {
	for _, key := range []string{"Enter", "Return"} {
		var p Parser
		stageFinish(t, &p, "1", key)
		if n, ok := p.Release(key, start); !ok || n != 1 {
			t.Fatal(n, ok)
		}
		p.ResetInput()
		if _, ok := enter(&p, "2\t0", start, 9); ok {
			t.Fatal("terminal page")
		}
		stageFinish(t, &p, "3", key)
		if _, ok := p.Release(key, start); ok {
			t.Fatal("terminal revived")
		}
	}
}

func TestFinishInvalidAndRestart(t *testing.T) {
	for _, text := range []string{"", "0", "1", "2", "3\t0", "+3", "18446744073709551616", strings.Repeat("0", MaxInputLength) + "3"} {
		var p Parser
		enter(&p, "2\t0", start, 9)
		stageFinish(t, &p, text, "Return")
		if _, ok := p.Release("Return", start); ok {
			t.Fatal("accepted", text)
		}
		stageFinish(t, &p, "3", "Return")
		if _, ok := p.Release("Return", start); !ok {
			t.Fatal("restart rejected", text)
		}
	}
}

func TestFinishInterrupted(t *testing.T) {
	for _, key := range []string{"Return", "Enter", "F9", "F8", "1", "Escape", "Tab"} {
		var p Parser
		stageFinish(t, &p, "1", "Return")
		p.Key(key, start, 9)
		if _, ok := p.Release("Return", start); ok {
			t.Fatal("interruption accepted", key)
		}
	}
}

func TestFinishReleaseBoundaries(t *testing.T) {
	for _, delay := range []time.Duration{-1, InputTimeout - 1, InputTimeout} {
		var p Parser
		stageFinish(t, &p, "1", "Enter")
		if _, ok := p.Release("Enter", start.Add(delay)); ok != (delay == InputTimeout-1) {
			t.Fatal(delay, ok)
		}
	}
	var p Parser
	stageFinish(t, &p, "1", "Return")
	p.Release("Enter", start)
	if _, ok := p.Release("Return", start); ok {
		t.Fatal("mismatch survived")
	}
	stageFinish(t, &p, "1", "Return")
	p.ResetInput()
	if _, ok := p.Release("Return", start); ok {
		t.Fatal("reset survived")
	}
}

func TestFinishCollectionReleases(t *testing.T) {
	var p Parser
	for _, key := range []string{"F9", "1"} {
		p.Key(key, start, 9)
		if _, ok := p.Release(key, start); ok {
			t.Fatal("early commit")
		}
	}
	if _, ok := p.Release("Return", start); ok {
		t.Fatal("no down")
	}
	stageFinish(t, &p, strings.Repeat("0", MaxInputLength-1)+"1", "Return")
	if _, ok := p.Release("Return", start); !ok {
		t.Fatal("length boundary")
	}
}

func assertTerminal(t *testing.T, r *Receiver) {
	t.Helper()
	if _, err := r.Begin(0, true, start, allow); !errors.Is(err, ErrTerminal) {
		t.Fatal(err)
	}
	if _, _, err := r.Retry(start, allow); !errors.Is(err, ErrTerminal) {
		t.Fatal(err)
	}
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrTerminal) {
		t.Fatal(err)
	}
}

func TestFinishReservation(t *testing.T) {
	r, o := bound(t)
	if n, err := r.BeginFinish(true, start, allow); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	assertTerminal(t, r)
	o.Beacon.Frame++
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	if err := r.Check(o.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if r.FreshUntil() != o.CapturedAt.Add(Freshness) {
		t.Fatal("not refreshed")
	}
	assertTerminal(t, r)
	r.Stop()
	assertTerminal(t, r)
	if err := r.Bind(o.Beacon.Descriptor, o, o.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginFinish(true, o.CapturedAt, allow); err != nil {
		t.Fatal(err)
	}
}

func TestFinishPendingEcho(t *testing.T) {
	r, o := bound(t)
	c := begin(t, r, start)
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if err := r.Sent(start); err != nil {
		t.Fatal(err)
	}
	if n, err := r.BeginFinish(true, start, allow); n != c.Nonce+1 || err != nil {
		t.Fatal(n, err)
	}
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, true, nil)
	assertTerminal(t, r)
}

func TestFinishReservationFailures(t *testing.T) {
	r, _ := bound(t)
	if _, err := r.BeginFinish(false, start, allow); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	if n, err := r.BeginFinish(true, start, allow); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	r, _ = bound(t)
	if _, err := r.BeginFinish(true, start, nil); !errors.Is(err, ErrNative) {
		t.Fatal(err)
	}
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrUnbound) {
		t.Fatal(err)
	}
	r, _ = bound(t)
	r.next = 0
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrExhausted) {
		t.Fatal(err)
	}
	r, _ = bound(t)
	r.next = math.MaxUint64
	if n, err := r.BeginFinish(true, start, allow); n != math.MaxUint64 || err != nil {
		t.Fatal(n, err)
	}
	assertTerminal(t, r)
}

func TestFinishExpiredBudgetRefused(t *testing.T) {
	r, _ := bound(t)
	begin(t, r, start)
	if err := r.Sent(start); err != nil {
		t.Fatal(err)
	}
	r.pending.Attempts, r.pending.Deadline = MaxSends, start
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	if _, err := r.BeginFinish(true, start, allow); !errors.Is(err, ErrUnbound) {
		t.Fatal(err)
	}
}

func TestFinishPendingOnlyProvenance(t *testing.T) {
	r, o := bound(t)
	begin(t, r, start)
	if err := r.Sent(start); err != nil {
		t.Fatal(err)
	}
	r.pending.Attempts, r.pending.Deadline = MaxSends, start.Add(time.Millisecond)
	if _, err := r.BeginFinish(true, start, allow); err != nil {
		t.Fatal(err)
	}
	o.Beacon.Frame++
	o.CapturedAt = start.Add(2 * time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	if err := r.Check(o.CapturedAt); err != nil {
		t.Fatal(err)
	}
	assertTerminal(t, r)
	// Terminal delivery is one-shot and independent of static-page age.
	if err := r.Check(o.CapturedAt.Add(Freshness + 1)); err != nil {
		t.Fatal(err)
	}
	assertObservation(t, r, o, o.CapturedAt, false, nil)
}

func TestFinishNoPageConversion(t *testing.T) {
	var p Parser
	if _, ok := enter(&p, "1\t0", start, 9); !ok {
		t.Fatal("page rejected")
	}
	if _, ok := p.Release("Enter", start); ok {
		t.Fatal("page became finish")
	}
	stageFinish(t, &p, "2", "Return")
	if _, ok := p.Key("Return", start, 9); ok {
		t.Fatal("repeat became page")
	}
	if _, ok := p.Release("Return", start); ok {
		t.Fatal("repeat replaced candidate")
	}
}
