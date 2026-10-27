package platform

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestControlClockUnsleptMachine(t *testing.T) {
	var clock controlClock
	if err := clock.check(100, 101, 102, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := clock.check(200, 201, 202, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestControlClockRejectsRegressionAndInvalidSamples(t *testing.T) {
	for _, sample := range [][5]uint64{{0, 1, 2, 1, 1}, {200, 210, 199, 1, 1}, {200, 199, 202, 1, 1}, {200, 210, 202, 0, 1}, {200, 210, 202, 1, 0}, {200, 210, 202, 2, 1}, {50, 60, 52, 1, 1}, {200, 210, 10000000, 1, 1}} {
		var clock controlClock
		if err := clock.check(100, 110, 102, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := clock.check(sample[0], sample[1], sample[2], sample[3], sample[4]); err == nil {
			t.Fatal(sample)
		}
	}
}

func TestControlPollCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitControl(ctx, time.Second, ErrFramePending, func() error { t.Fatal("polled canceled context"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	err := waitControl(context.Background(), time.Millisecond, ErrCloseTimeout, func() error { return ErrFramePending })
	if !errors.Is(err, ErrCloseTimeout) {
		t.Fatal(err)
	}
}

func TestControlPollRejectsFrameAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := waitControl(ctx, time.Second, ErrFramePending, func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestControlTicksCeiling(t *testing.T) {
	for _, tc := range []struct {
		ticks, num, den uint64
		want            time.Duration
	}{
		{1, 125, 3, 42}, {3, 125, 3, 125}, {0, 1, 1, 0},
		{math.MaxUint64, 1, math.MaxUint64, 1},
	} {
		got, err := controlTickAge(tc.ticks, tc.num, tc.den)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
}

func TestControlTicksInvalid(t *testing.T) {
	for _, tc := range [][3]uint64{{1, 0, 1}, {1, 1, 0}, {math.MaxUint64, math.MaxUint64, 1}, {math.MaxInt64, 2, 1}} {
		if _, err := controlTickAge(tc[0], tc[1], tc[2]); err == nil {
			t.Fatal(tc)
		}
	}
}

func TestControlTimestampNeverFreshens(t *testing.T) {
	g0 := time.Now()
	at, err := controlTimestamp(g0, g0.Add(time.Millisecond), 10, 20, 1, 1)
	if err != nil || at.After(g0.Add(-10*time.Nanosecond)) {
		t.Fatal(at, err)
	}
	if at.Sub(g0) >= 0 {
		t.Fatal("lost conservative bound")
	}
	if at == at.Round(0) { //nolint:staticcheck // Compare representation: Round(0) strips monotonic provenance; Equal would hide its loss.
		t.Fatal("monotonic clock lost")
	}
	for _, tc := range [][2]uint64{{0, 20}, {21, 20}} {
		if _, err := controlTimestamp(g0, g0, tc[0], tc[1], 1, 1); err == nil {
			t.Fatal(tc)
		}
	}
}

func TestControlOldFrameRemainsBeforeEnter(t *testing.T) {
	enter := time.Now()
	receipt := enter.Add(time.Second)
	at, err := controlTimestamp(receipt, receipt.Add(time.Millisecond), 1, 2000000001, 1, 1)
	if err != nil || !at.Before(enter) {
		t.Fatal(at, err)
	}
	if _, err := controlTimestamp(receipt, receipt, 1, math.MaxInt64, 1, 1); err == nil {
		t.Fatal("duration cushion overflow")
	}
}

func TestControlConcurrentGenerations(t *testing.T) {
	const count = 32
	values := make(chan uint64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			value, err := nextControlGeneration()
			if err != nil {
				t.Error(err)
			}
			values <- value
		})
	}
	wg.Wait()
	close(values)
	assertControlGenerationsUnique(t, values)
}

func assertControlGenerationsUnique(t *testing.T, values <-chan uint64) {
	t.Helper()
	seen := make(map[uint64]bool)
	for value := range values {
		if value == 0 || seen[value] {
			t.Fatal(value)
		}
		seen[value] = true
	}
}

func TestControlTimestampBracket(t *testing.T) {
	now := time.Now()
	for _, delta := range []time.Duration{-1, controlClockBracket + 1} {
		if _, err := controlTimestamp(now, now.Add(delta), 1, 2, 1, 1); err == nil {
			t.Fatal(delta)
		}
	}
}

func TestControlClockDiscontinuityLatches(t *testing.T) {
	var clock controlClock
	if err := clock.check(100, 110, 102, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := clock.check(200, 210, 202, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := clock.check(300, 320, 302, 1, 1); err == nil {
		t.Fatal("sleep accepted")
	}
	if err := clock.check(400, 410, 402, 1, 1); err == nil {
		t.Fatal("revived")
	}
}

func TestControlGenerationAndPassiveDefaults(t *testing.T) {
	a, err := nextControlGeneration()
	b, other := nextControlGeneration()
	if err != nil || other != nil || a == 0 || b <= a {
		t.Fatal(a, b, err, other)
	}
	var frame Frame
	if frame.ControlAgeBounded || frame.Generation != 0 || !frame.ControlCapturedAt.IsZero() {
		t.Fatal(frame)
	}
}
