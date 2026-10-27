package control

import (
	"errors"
	"math"
	"testing"
	"time"
)

func heartbeat(t *testing.T, r *Receiver, o *Observation, at time.Time) {
	t.Helper()
	// Model uninterrupted acquisition rather than reviving an expired binding.
	for next := o.CapturedAt.Add(Freshness); next.Before(at); next = o.CapturedAt.Add(Freshness) {
		heartbeat(t, r, o, next)
	}
	o.Beacon.Frame++
	o.CapturedAt = at
	if _, err := r.Observe(*o, at); err != nil {
		t.Fatal(err)
	}
}

func reserveRetry(t *testing.T) (*Receiver, Observation, Pending) {
	t.Helper()
	r, o := bound(t)
	begin(t, r, start)
	sent(t, r, start)
	p, _ := r.Pending()
	heartbeat(t, r, &o, p.RetryAt)
	if _, ready, err := r.Retry(p.RetryAt, allow); !ready || err != nil {
		t.Fatal(ready, err)
	}
	p, _ = r.Pending()
	return r, o, p
}

func TestRetryReservationEchoRefreshesWithoutAcknowledging(t *testing.T) {
	r, o, reserved := reserveRetry(t)
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = reserved.Command.Nonce, reserved.Command.Ordinal
	o.CapturedAt = o.CapturedAt.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	assertFreshUntil(t, r, o.CapturedAt.Add(Freshness))
	if p, ok := r.Pending(); !ok || p != reserved {
		t.Fatal("reservation changed", p)
	}
	assertRetryEchoAfterSent(t, r, o, reserved)
}

func assertRetryEchoAfterSent(t *testing.T, r *Receiver, o Observation, reserved Pending) {
	t.Helper()
	sent(t, r, o.CapturedAt)
	if p, ok := r.Pending(); !ok || p.Attempts != reserved.Attempts+1 || p.IssuedAt != reserved.IssuedAt || p.AwaitingSent {
		t.Fatal("send lost first issuance or attempts", p)
	}
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	o.Beacon.Frame--
	assertObservation(t, r, o, o.CapturedAt, false, nil)
	o.Beacon.Frame += 2
	assertObservation(t, r, o, o.CapturedAt, true, nil)
}

func TestRetryReservationStillValidatesObservations(t *testing.T) {
	for _, change := range []struct {
		mutate func(*Observation)
		err    error
	}{
		{func(o *Observation) { o.Beacon.Nonce += MaxSends }, ErrConflict},
		{func(o *Observation) { o.Beacon.Flags = 0 }, ErrIneligible},
		{func(o *Observation) { o.Beacon.Epoch++ }, ErrBindingChanged},
		{func(o *Observation) { o.Generation++ }, ErrBindingChanged},
		{func(o *Observation) { o.Beacon.Length++ }, ErrBindingChanged},
	} {
		r, o, _ := reserveRetry(t)
		o.Beacon.Frame++
		o.CapturedAt = o.CapturedAt.Add(time.Millisecond)
		change.mutate(&o)
		assertObservation(t, r, o, o.CapturedAt, false, change.err)
		assertFreshUntil(t, r, time.Time{})
	}
}

func TestRetryDelaysAndBudget(t *testing.T) {
	r, o := bound(t)
	c := begin(t, r, start)
	sent(t, r, start)
	for _, delay := range []time.Duration{FirstBackoff, SecondBackoff} {
		p, _ := r.Pending()
		assertRetryBoundary(t, r, &o, c, p.Deadline.Add(delay))
	}
	p, _ := r.Pending()
	if p.Attempts != MaxSends || !p.IssuedAt.Equal(start) {
		t.Fatal(p)
	}
	heartbeat(t, r, &o, p.Deadline.Add(-time.Nanosecond))
	if _, _, err := r.Retry(p.Deadline, allow); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
}

func sent(t *testing.T, r *Receiver, at time.Time) {
	t.Helper()
	if err := r.Sent(at); err != nil {
		t.Fatal(err)
	}
}

func assertRetryBoundary(t *testing.T, r *Receiver, o *Observation, c Command, due time.Time) {
	t.Helper()
	heartbeat(t, r, o, due.Add(-time.Nanosecond))
	if _, ready, err := r.Retry(due.Add(-time.Nanosecond), allow); ready || err != nil {
		t.Fatal(ready, err)
	}
	got, ready, err := r.Retry(due, allow)
	if got != c || !ready || err != nil {
		t.Fatal(got, ready, err)
	}
	if _, again, _ := r.Retry(due, allow); again {
		t.Fatal("duplicate reservation")
	}
	sent(t, r, due)
}

func TestLateFirstEchoAfterRetry(t *testing.T) {
	r, o := bound(t)
	c := begin(t, r, start)
	sent(t, r, start)
	p, _ := r.Pending()
	heartbeat(t, r, &o, p.RetryAt.Add(-time.Millisecond))
	if _, ready, err := r.Retry(p.RetryAt, allow); !ready || err != nil {
		t.Fatal(ready, err)
	}
	sent(t, r, p.RetryAt)
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
	// Capture precedes the second Enter but follows the first and passes highwater.
	o.CapturedAt = p.RetryAt.Add(-time.Microsecond)
	assertObservation(t, r, o, p.RetryAt, true, nil)
	if _, pending := r.Pending(); pending {
		t.Fatal("echo did not release request")
	}
}

func TestNativeCheckerEverySend(t *testing.T) {
	r, o := bound(t)
	if _, err := r.Begin(2, true, start, nil); !errors.Is(err, ErrNative) {
		t.Fatal(err)
	}
	r, o = bound(t)
	begin(t, r, start)
	sent(t, r, start)
	p, _ := r.Pending()
	heartbeat(t, r, &o, p.RetryAt)
	denied := errors.New("foreground changed")
	if _, ready, err := r.Retry(p.RetryAt, func() error { return denied }); ready || !errors.Is(err, denied) {
		t.Fatal(ready, err)
	}
	if err := r.Check(p.RetryAt); !errors.Is(err, ErrUnbound) {
		t.Fatal(err)
	}
}

func TestUnsolicitedConflict(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, c := range []Command{{0, 7}, {9, 2}, {1, 7}} {
			r, o := bound(t)
			if pending {
				begin(t, r, start)
				sent(t, r, start)
			}
			o.Beacon.Frame++
			o.CapturedAt = start.Add(time.Millisecond)
			o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
			if _, err := r.Observe(o, o.CapturedAt); !errors.Is(err, ErrConflict) {
				t.Fatal(c, pending, err)
			}
		}
	}
}

func TestReceiverExhaustion(t *testing.T) {
	r, o := boundNearExhaustion(t)
	c := begin(t, r, start)
	sent(t, r, start)
	if c.Nonce != math.MaxUint64 {
		t.Fatal(c)
	}
	o.Beacon.Frame++
	o.Beacon.Nonce, o.Beacon.Ordinal = c.Nonce, c.Ordinal
	o.CapturedAt = start.Add(time.Millisecond)
	assertObservation(t, r, o, o.CapturedAt, true, ErrExhausted)
	if _, err := r.Begin(3, true, o.CapturedAt, allow); !errors.Is(err, ErrUnbound) {
		t.Fatal(err)
	}
}

func boundNearExhaustion(t *testing.T) (*Receiver, Observation) {
	t.Helper()
	o := observation(start)
	o.Beacon.Nonce = math.MaxUint64
	var r Receiver
	if err := r.Bind(o.Beacon.Descriptor, o, start); !errors.Is(err, ErrExhausted) {
		t.Fatal(err)
	}
	o.Beacon.Nonce--
	if err := r.Bind(o.Beacon.Descriptor, o, start); err != nil {
		t.Fatal(err)
	}
	return &r, o
}

func TestPhaseAndProvenance(t *testing.T) {
	for phase := byte(0); phase <= 3; phase++ {
		o := observation(start)
		o.Beacon.Phase = phase
		var r Receiver
		if err := r.Bind(o.Beacon.Descriptor, o, start); (err == nil) != (phase <= 2) {
			t.Fatal(phase, err)
		}
	}
	o := observation(start)
	o.AgeBounded = false
	var r Receiver
	if err := r.Bind(o.Beacon.Descriptor, o, start); !errors.Is(err, ErrIneligible) {
		t.Fatal(err)
	}
}
