package platform

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var _ RefreshingInputBinding = (*inputBinding)(nil)

func refreshOK(context.Context) error { return nil }
func eligibleOK() error               { return nil }

func TestInputRefreshOrder(t *testing.T) {
	b := fakeInput(t)
	var events []string
	b.deadline = func(time.Duration) uint64 { events = append(events, "deadline"); return 1 }
	b.prepare = func(uint64) error { events = append(events, "prepare"); return nil }
	b.key = func(uint16) error { events = append(events, "key"); return nil }
	refresh := func(context.Context) error { events = append(events, "refresh"); return nil }
	eligible := func() error { events = append(events, "eligible"); return nil }
	err := b.SendWithRefresh(t.Context(), 1, 0, refresh, eligible)
	var want []string
	for range 5 {
		want = append(want, "refresh", "deadline", "eligible", "prepare", "key")
	}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func rejectNative(t *testing.T, b *inputBinding) {
	t.Helper()
	b.prepare = func(uint64) error { t.Error("unexpected native prepare"); return nil }
	b.key = func(uint16) error { t.Error("unexpected key"); return nil }
}

func requireStopped(t *testing.T, b *inputBinding, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error=%v want=%v", err, want)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatalf("not latched: %v", err)
	}
}

func TestInputRefreshRejectsNil(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	err := b.SendWithRefresh(t.Context(), 1, 0, nil, eligibleOK)
	requireStopped(t, b, err, ErrInputUnavailable)
}

func TestInputRefreshFailure(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	refresh := func(context.Context) error { return ErrInputUnavailable }
	err := b.SendWithRefresh(t.Context(), 1, 0, refresh, eligibleOK)
	requireStopped(t, b, err, ErrInputUnavailable)
}

func TestInputRefreshCancel(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	refresh := func(context.Context) error { cancel(); return nil }
	err := b.SendWithRefresh(ctx, 1, 0, refresh, eligibleOK)
	requireStopped(t, b, err, context.Canceled)
}

func TestInputRefreshClose(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	refresh := func(context.Context) error { return b.Close() }
	err := b.SendWithRefresh(t.Context(), 1, 0, refresh, eligibleOK)
	requireStopped(t, b, err, ErrInputStopped)
}

func TestInputRefreshSlowDoesNotConsumeNativeBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		b.deadline = func(budget time.Duration) uint64 {
			if budget != inputCheckBudget {
				t.Errorf("native budget=%v", budget)
			}
			return 1
		}
		refresh := func(context.Context) error { time.Sleep(2 * inputCheckBudget); return nil }
		if err := b.SendWithRefresh(t.Context(), 1, 0, refresh, eligibleOK); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInputRefreshWholeSequenceDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		keys := 0
		b.key = func(uint16) error { keys++; return nil }
		var deadline time.Time
		refresh := sequenceExpiryRefresh(t, &deadline)
		err := b.SendWithRefresh(t.Context(), 1, 0, refresh, eligibleOK)
		requireStopped(t, b, err, context.DeadlineExceeded)
		if keys != 1 {
			t.Fatalf("keys=%d", keys)
		}
	})
}

func sequenceExpiryRefresh(t *testing.T, first *time.Time) func(context.Context) error {
	t.Helper()
	return func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing sequence deadline")
		}
		if first.IsZero() {
			*first = deadline
			if time.Until(deadline) != InputSequenceTimeout {
				t.Fatal("wrong sequence budget")
			}
			return nil
		}
		if deadline != *first {
			t.Fatal("renewed sequence deadline")
		}
		time.Sleep(time.Until(deadline))
		return nil
	}
}

func TestInputRefreshEarlierParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		rejectNative(t, b)
		ctx, cancel := context.WithTimeout(t.Context(), inputCheckBudget)
		defer cancel()
		refresh := func(child context.Context) error {
			got, _ := child.Deadline()
			want, _ := ctx.Deadline()
			if got != want {
				t.Error("parent deadline not preserved")
			}
			<-child.Done()
			return nil
		}
		err := b.SendWithRefresh(ctx, 1, 0, refresh, eligibleOK)
		requireStopped(t, b, err, context.DeadlineExceeded)
	})
}

func fakeTimedInput(t *testing.T, clock *atomic.Uint64) *inputBinding {
	t.Helper()
	b := fakeInput(t)
	b.deadline = func(budget time.Duration) uint64 {
		return clock.Load() + uint64(budget) //nolint:gosec // inputBudget is clamped to [0, 100ms].
	}
	b.prepare = func(deadline uint64) error {
		if clock.Load() >= deadline {
			return ErrInputUnavailable
		}
		return nil
	}
	b.key = func(uint16) error { t.Error("key after native deadline"); return nil }
	return b
}

func TestInputRefreshEligibilityConsumesNativeBudget(t *testing.T) {
	var clock atomic.Uint64
	b := fakeTimedInput(t, &clock)
	eligible := func() error { clock.Add(uint64(inputCheckBudget)); return nil }
	err := b.SendWithRefresh(t.Context(), 1, 0, refreshOK, eligible)
	requireStopped(t, b, err, ErrInputUnavailable)
}

func TestInputRefreshGateWaitConsumesNativeBudget(t *testing.T) {
	var clock atomic.Uint64
	b := fakeTimedInput(t, &clock)
	b.gate.Lock()
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		eligible := func() error { close(entered); return nil }
		result <- b.SendWithRefresh(t.Context(), 1, 0, refreshOK, eligible)
	}()
	<-entered
	clock.Add(uint64(inputCheckBudget))
	b.gate.Unlock()
	requireStopped(t, b, <-result, ErrInputUnavailable)
}

func TestInputRefreshCloseWhileCallbackBlocked(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	entered, proceed := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		refresh := func(context.Context) error { close(entered); <-proceed; return nil }
		result <- b.SendWithRefresh(t.Context(), 1, 0, refresh, eligibleOK)
	}()
	<-entered
	_ = b.Close()
	close(proceed)
	requireStopped(t, b, <-result, ErrInputStopped)
}

func TestInputRefreshOwnerWaitConsumesParentBudget(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	b.owner.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), inputKeyInterval)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		refresh := func(context.Context) error { t.Error("refresh after owner-wait expiry"); return nil }
		result <- b.SendWithRefresh(ctx, 1, 0, refresh, eligibleOK)
	}()
	<-ctx.Done()
	b.owner.Unlock()
	requireStopped(t, b, <-result, context.DeadlineExceeded)
}

func TestInputLegacySendHasNoNewSequenceDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		eligible := func() error { time.Sleep(InputSequenceTimeout); return nil }
		if err := b.Send(t.Context(), 1, 0, eligible); err != nil {
			t.Fatal(err)
		}
	})
}
