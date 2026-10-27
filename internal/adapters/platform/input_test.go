package platform

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeInput(t *testing.T) *inputBinding {
	t.Helper()
	b := &inputBinding{done: make(chan struct{}), check: func() error { return nil }, key: func(uint16) error { return nil }}
	b.deadline = func(time.Duration) uint64 { return 1 }
	b.prepare = func(uint64) error { return nil }
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// Invalid titles return before entering AppKit, so this API test is headless.
func TestInputOwnFocusStateRejectsInvalidTitle(t *testing.T) {
	for _, title := range []string{"", "sender\x00suffix"} {
		focused, generation := OwnWindowFocusState(title)
		if focused || generation != 0 {
			t.Fatalf("invalid title focus state: %v/%d", focused, generation)
		}
		if OwnWindowFocused(title) {
			t.Fatal("legacy focus API accepted invalid title")
		}
	}
}

func TestInputTargetImmutableAfterClose(t *testing.T) {
	b := fakeInput(t)
	want := ControlTarget{Window: 7, PID: 23, Started: 42, Title: "bound\x00window"}
	b.target = want
	snapshot := b.Target()
	snapshot.PID = 99
	snapshot.Title = "changed"
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := b.Target(); got != want {
		t.Fatalf("target changed: %#v", got)
	}
}

func TestInputTargetConcurrentClose(t *testing.T) {
	b := fakeInput(t)
	want := ControlTarget{Window: 7, PID: 23, Started: 42, Title: "bound window"}
	b.target = want
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			_ = b.Close()
			if got := b.Target(); got != want {
				t.Errorf("target changed: %#v", got)
			}
		})
	}
	workers.Wait()
}

func TestInputSequence(t *testing.T) {
	got, err := inputSequence(1234567890, 0)
	want := []uint16{100, 18, 19, 20, 21, 23, 22, 26, 28, 25, 29, 48, 29, 36}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence %v: %v", got, err)
	}
	got, err = inputSequence(math.MaxUint64, math.MaxUint64)
	if err != nil || len(got) != 43 || len(got) > inputSequenceLimit {
		t.Fatalf("maximum sequence: %v, %v", got, err)
	}
	if _, err := inputSequence(0, 1); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
}

func TestInputEligibilityBeforeEveryKey(t *testing.T) {
	b := fakeInput(t)
	checks, keys := 0, 0
	b.key = func(uint16) error {
		keys++
		if checks != keys {
			t.Fatal("missing per-key eligibility")
		}
		return nil
	}
	err := b.Send(context.Background(), 1, 0, func() error { checks++; return nil })
	if err != nil || keys != 5 {
		t.Fatalf("keys=%d err=%v", keys, err)
	}
}

func TestInputCallbackPrepareKeyOrder(t *testing.T) {
	b := fakeInput(t)
	var events []string
	b.check = func() error { events = append(events, "observation"); return nil }
	b.prepare = func(uint64) error { events = append(events, "prepare"); return nil }
	b.key = func(uint16) error { events = append(events, "key"); return nil }
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
	err := b.Send(t.Context(), 1, 0, func() error { events = append(events, "callback"); return nil })
	want := []string{"observation"}
	for range 5 {
		want = append(want, "callback", "prepare", "key")
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func TestInputPrepareFailureAfterCallbackLatchesStop(t *testing.T) {
	b := fakeInput(t)
	callbacks, prepares, keys := 0, 0, 0
	b.prepare = func(uint64) error { prepares++; return ErrInputUnavailable }
	b.key = func(uint16) error { keys++; return nil }
	guard := func() error { callbacks++; return nil }
	if err := b.Send(t.Context(), 1, 0, guard); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if err := b.Send(t.Context(), 1, 0, guard); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
	if callbacks != 1 || prepares != 1 || keys != 0 {
		t.Fatalf("callbacks=%d prepares=%d keys=%d", callbacks, prepares, keys)
	}
}

func TestInputGuardFailureDisarms(t *testing.T) {
	b := fakeInput(t)
	keys, checks := 0, 0
	b.key = func(uint16) error { keys++; return nil }
	guard := func() error {
		checks++
		if checks == 2 {
			return ErrInputUnavailable
		}
		return nil
	}
	if err := b.Send(context.Background(), 1, 0, guard); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if keys != 1 {
		t.Fatalf("sent %d keys", keys)
	}
	if err := b.Send(context.Background(), 1, 0, guard); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputCheckFailureDisarms(t *testing.T) {
	b := fakeInput(t)
	b.check = func() error { return ErrInputUnavailable }
	if err := b.Check(); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputNativeFailureDisarms(t *testing.T) {
	b := fakeInput(t)
	b.key = func(uint16) error { return ErrInputUnavailable }
	if err := b.Send(context.Background(), 1, 0, func() error { return nil }); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputCancellationInsideGuard(t *testing.T) {
	b := fakeInput(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.key = func(uint16) error { t.Fatal("key after cancellation"); return nil }
	if err := b.Send(ctx, 1, 0, func() error { cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputCloseInsideGuard(t *testing.T) {
	b := fakeInput(t)
	b.key = func(uint16) error { t.Fatal("key after close"); return nil }
	if err := b.Send(context.Background(), 1, 0, b.Close); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputExpiredDeadline(t *testing.T) {
	b := fakeInput(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	b.key = func(uint16) error { t.Fatal("key after deadline"); return nil }
	if err := b.Send(ctx, 1, 0, func() error { t.Fatal("guard after deadline"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestInputInvalidArgumentsDisarm(t *testing.T) {
	for _, run := range []func(*inputBinding) error{
		func(b *inputBinding) error { return b.Send(nil, 1, 0, func() error { return nil }) }, //nolint:staticcheck // Deliberately verifies rejection of a missing context.
		func(b *inputBinding) error { return b.Send(context.Background(), 1, 0, nil) },
		func(b *inputBinding) error { return b.Send(context.Background(), 0, 0, func() error { return nil }) },
	} {
		b := fakeInput(t)
		if err := run(b); !errors.Is(err, ErrInputUnavailable) {
			t.Fatal(err)
		}
		if err := b.Check(); !errors.Is(err, ErrInputStopped) {
			t.Fatal(err)
		}
	}
}

func TestInputConcurrentClose(t *testing.T) {
	b := fakeInput(t)
	var releases atomic.Int32
	b.release = func() { releases.Add(1) }
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() { _ = b.Check(); _ = b.Close() })
	}
	workers.Wait()
	if releases.Load() != 1 {
		t.Fatal("release must run exactly once")
	}
}

func TestInputCloseDuringSend(t *testing.T) {
	b := fakeInput(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	b.key = func(uint16) error { close(entered); <-proceed; return nil }
	result := make(chan error, 1)
	go func() { result <- b.Send(context.Background(), 1, 0, func() error { return nil }) }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	<-b.done
	close(proceed)
	<-closed
	if err := <-result; !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputGuardMargin(t *testing.T) {
	if InputGuardMargin <= inputCheckBudget || inputCheckBudget <= 0 {
		t.Fatal("invalid guard timing contract")
	}
	if got := inputBudget(context.Background()); got != inputCheckBudget {
		t.Fatal(got)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := inputBudget(ctx); got != 0 {
		t.Fatal(got)
	}
}

func TestInputDeadlineStartsBeforeCallback(t *testing.T) {
	b := fakeInput(t)
	var clock uint64
	b.deadline = func(budget time.Duration) uint64 { return clock + uint64(budget) } //nolint:gosec // inputBudget clamps this test duration to [0, 100ms].
	b.prepare = func(deadline uint64) error {
		if clock >= deadline {
			return ErrInputUnavailable
		}
		return nil
	}
	b.key = func(uint16) error { t.Fatal("posted expired preflight"); return nil }
	guard := func() error { clock += uint64(inputCheckBudget); return nil }
	if err := b.Send(context.Background(), 1, 0, guard); !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputCancellationAfterPreflight(t *testing.T) {
	b := fakeInput(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.prepare = func(uint64) error { cancel(); return nil }
	b.key = func(uint16) error { t.Fatal("posted after preflight cancellation"); return nil }
	if err := b.Send(ctx, 1, 0, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}

func TestInputCloseDuringPreflight(t *testing.T) {
	b := fakeInput(t)
	entered, proceed := make(chan struct{}), make(chan struct{})
	b.prepare = func(uint64) error { close(entered); <-proceed; return nil }
	b.key = func(uint16) error { t.Error("posted after close during preflight"); return nil }
	result := make(chan error, 1)
	go func() { result <- b.Send(context.Background(), 1, 0, func() error { return nil }) }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = b.Close(); close(closed) }()
	<-b.done
	close(proceed)
	<-closed
	if err := <-result; !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
}
