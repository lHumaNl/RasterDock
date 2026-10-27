package control

import (
	"errors"
	"testing"
	"time"

	v1 "rasterdock/internal/protocol/v1"
)

func observation(at time.Time) Observation {
	d := v1.Descriptor{Length: 64, Block: 64}
	d.Transfer[6], d.Transfer[8] = 0x40, 0x80
	return Observation{Beacon: v1.Beacon{Descriptor: d, Epoch: 1, Frame: 10, Flags: 3}, CapturedAt: at, Generation: 1, AgeBounded: true}
}

func bound(t *testing.T) (*Receiver, Observation) {
	t.Helper()
	o := observation(start)
	r := &Receiver{}
	if err := r.Bind(o.Beacon.Descriptor, o, start); err != nil {
		t.Fatal(err)
	}
	return r, o
}

func allow() error { return nil }

func begin(t *testing.T, r *Receiver, now time.Time) Command {
	t.Helper()
	c, err := r.Begin(2, true, now, allow)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOldFramesCannotRefresh(t *testing.T) {
	r, o := bound(t)
	o.Beacon.Flags = 0
	assertObservation(t, r, o, start, false, nil)
	o.Beacon.Frame++
	assertObservation(t, r, o, start.Add(time.Millisecond), false, nil)
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, false, ErrIneligible)
	o.Beacon.Frame++
	o.Beacon.Flags = 3
	assertObservation(t, r, o, o.CapturedAt, false, ErrUnbound)
}

func assertObservation(t *testing.T, r *Receiver, o Observation, now time.Time, want bool, wantErr error) {
	t.Helper()
	got, err := r.Observe(o, now)
	if got != want || !errors.Is(err, wantErr) {
		t.Fatalf("Observe = %v, %v; want %v, %v", got, err, want, wantErr)
	}
}

func TestBindingChangesStop(t *testing.T) {
	for _, change := range []func(*Observation){
		func(o *Observation) { o.Beacon.Epoch++ },
		func(o *Observation) { o.Generation++ },
		func(o *Observation) { o.Beacon.Length++ },
	} {
		r, o := bound(t)
		change(&o)
		if _, err := r.Observe(o, start); !errors.Is(err, ErrBindingChanged) {
			t.Fatal(err)
		}
		if err := r.Check(start); !errors.Is(err, ErrUnbound) {
			t.Fatal(err)
		}
	}
}

func TestCaptureFreshness(t *testing.T) {
	for _, at := range []time.Time{{}, start.Add(time.Nanosecond), start.Add(-Freshness - time.Nanosecond)} {
		o := observation(at)
		var r Receiver
		if err := r.Bind(o.Beacon.Descriptor, o, start); err == nil {
			t.Fatal("invalid capture accepted", at)
		}
	}
}

func TestStaleCaptureDoesNotRefresh(t *testing.T) {
	r, o := bound(t)
	o.Beacon.Frame++
	o.CapturedAt = start.Add(-time.Second)
	assertObservation(t, r, o, start.Add(time.Millisecond), false, nil)
	if err := r.Check(start.Add(Freshness)); err != nil {
		t.Fatal(err)
	}
	if err := r.Check(start.Add(Freshness + time.Nanosecond)); err == nil {
		t.Fatal("stale frame refreshed freshness")
	}
}

func TestPendingEchoAndReadiness(t *testing.T) {
	r, o := bound(t)
	if _, err := r.Begin(2, false, start, allow); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	c := begin(t, r, start)
	if _, err := r.Begin(3, true, start, allow); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	confirmAfterEnter(t, r, &o, c)
	if _, err := r.Begin(3, false, o.CapturedAt, allow); !errors.Is(err, ErrNotReady) {
		t.Fatal("echo implied persistence")
	}
}

func confirmAfterEnter(t *testing.T, r *Receiver, o *Observation, c Command) {
	t.Helper()
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, *o, o.CapturedAt, false, nil)
	if err := r.Sent(o.CapturedAt); err != nil {
		t.Fatal(err)
	}
	o.Beacon.Frame++
	assertObservation(t, r, *o, o.CapturedAt, false, nil)
	o.Beacon.Frame++
	o.CapturedAt = o.CapturedAt.Add(time.Millisecond)
	assertObservation(t, r, *o, o.CapturedAt, true, nil)
}

func TestOldArmedFrameCannotRefreshOrConflict(t *testing.T) {
	r, o := bound(t)
	o.Beacon.Frame--
	o.Beacon.Nonce = 99
	o.CapturedAt = start.Add(Freshness)
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	if err := r.Check(o.CapturedAt.Add(time.Nanosecond)); !errors.Is(err, ErrIneligible) {
		t.Fatal(err)
	}
}

func TestInvalidSendCompletion(t *testing.T) {
	for _, at := range []time.Time{{}, start.Add(-time.Nanosecond)} {
		r, _ := bound(t)
		begin(t, r, start)
		if err := r.Sent(at); !errors.Is(err, ErrSendState) {
			t.Fatal(err)
		}
	}
}

func assertFreshUntil(t *testing.T, r *Receiver, want time.Time) {
	t.Helper()
	before := *r
	if got := r.FreshUntil(); !got.Equal(want) {
		t.Fatalf("FreshUntil = %v; want %v", got, want)
	}
	if *r != before {
		t.Fatal("FreshUntil mutated receiver")
	}
}

func TestFreshUntilLifecycle(t *testing.T) {
	assertFreshUntil(t, &Receiver{}, time.Time{})
	r, o := bound(t)
	assertFreshUntil(t, r, start.Add(Freshness))
	heartbeat(t, r, &o, start.Add(Freshness))
	assertFreshUntil(t, r, o.CapturedAt.Add(Freshness))
	r.Stop()
	assertFreshUntil(t, r, time.Time{})
}

func TestFreshUntilIgnoresRejectedCaptures(t *testing.T) {
	for name, change := range ignoredCaptures() {
		t.Run(name, func(t *testing.T) {
			r, o := bound(t)
			o.Beacon.Frame++
			o.CapturedAt = start.Add(time.Millisecond)
			change(&o)
			assertObservation(t, r, o, start.Add(time.Millisecond), false, nil)
			assertFreshUntil(t, r, start.Add(Freshness))
		})
	}
}

func ignoredCaptures() map[string]func(*Observation) {
	return map[string]func(*Observation){
		"old":       func(o *Observation) { o.Beacon.Frame -= 2 },
		"duplicate": func(o *Observation) { o.Beacon.Frame-- },
		"prebind":   func(o *Observation) { o.CapturedAt = start },
		"stale":     func(o *Observation) { o.CapturedAt = start.Add(-Freshness) },
		"unbounded": func(o *Observation) { o.AgeBounded = false },
		"zero":      func(o *Observation) { o.CapturedAt = time.Time{} },
		"future":    func(o *Observation) { o.CapturedAt = start.Add(Freshness) },
	}
}

func TestObserveCannotReviveExpiredBinding(t *testing.T) {
	for _, newer := range []bool{false, true} {
		r, o := bound(t)
		if newer {
			o.Beacon.Frame++
		}
		o.CapturedAt = start.Add(Freshness + time.Nanosecond)
		assertObservation(t, r, o, o.CapturedAt, false, ErrIneligible)
		assertFreshUntil(t, r, time.Time{})
		assertObservation(t, r, o, o.CapturedAt, false, ErrUnbound)
	}
}

func TestExpiredBindingStillReportsIdentityChange(t *testing.T) {
	r, o := bound(t)
	o.Beacon.Epoch++
	assertObservation(t, r, o, start.Add(Freshness+time.Nanosecond), false, ErrBindingChanged)
	assertFreshUntil(t, r, time.Time{})
}

func TestFirstReservationEchoRetainsPending(t *testing.T) {
	r, o := bound(t)
	c := begin(t, r, start)
	reserved, _ := r.Pending()
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	assertFreshUntil(t, r, o.CapturedAt.Add(Freshness))
	if p, ok := r.Pending(); !ok || p != reserved {
		t.Fatal("first reservation changed", p)
	}
}
