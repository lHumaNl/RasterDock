package platform

import (
	"context"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var _ FinishingInputBinding = (*inputBinding)(nil)

func TestInputFinishSequence(t *testing.T) {
	for nonce, want := range map[uint64][]uint16{
		1:              {101, 18, 36},
		1234567890:     {101, 18, 19, 20, 21, 23, 22, 26, 28, 25, 29, 36},
		math.MaxUint64: {101, 18, 28, 21, 21, 22, 26, 21, 21, 29, 26, 20, 26, 29, 25, 23, 23, 18, 22, 18, 23, 36},
	} {
		b := fakeInput(t)
		var keys []uint16
		b.key = func(key uint16) error { keys = append(keys, key); return nil }
		err := b.SendFinishWithRefresh(t.Context(), nonce, refreshOK, eligibleOK)
		if err != nil || !reflect.DeepEqual(keys, want) {
			t.Fatalf("nonce=%d keys=%v err=%v", nonce, keys, err)
		}
		assertFinishClosed(t, b)
	}
}

func assertFinishClosed(t *testing.T, b *inputBinding) {
	t.Helper()
	select {
	case <-b.done:
	default:
		t.Fatal("finish did not close binding")
	}
	rejectNative(t, b)
	for _, err := range []error{
		b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligibleOK),
		b.Send(t.Context(), 1, 0, eligibleOK),
		b.SendWithRefresh(t.Context(), 1, 0, refreshOK, eligibleOK),
	} {
		requireStopped(t, b, err, ErrInputStopped)
	}
}

func TestInputFinishInvalidArguments(t *testing.T) {
	for _, run := range []func(*inputBinding) error{
		func(b *inputBinding) error { return b.SendFinishWithRefresh(nil, 1, refreshOK, eligibleOK) }, //nolint:staticcheck // Tests missing context rejection.
		func(b *inputBinding) error { return b.SendFinishWithRefresh(t.Context(), 0, refreshOK, eligibleOK) },
		func(b *inputBinding) error { return b.SendFinishWithRefresh(t.Context(), 1, nil, eligibleOK) },
		func(b *inputBinding) error { return b.SendFinishWithRefresh(t.Context(), 1, refreshOK, nil) },
	} {
		b := fakeInput(t)
		rejectNative(t, b)
		requireStopped(t, b, run(b), ErrInputUnavailable)
		assertFinishClosed(t, b)
	}
}

func TestInputFinishOrder(t *testing.T) {
	b := fakeInput(t)
	var events []string
	b.deadline = func(time.Duration) uint64 { events = append(events, "deadline"); return 1 }
	b.prepare = func(uint64) error { events = append(events, "prepare"); return nil }
	b.key = func(uint16) error { events = append(events, "pair", "postflight"); return nil }
	b.release = func() { events = append(events, "release") }
	refresh := func(context.Context) error { events = append(events, "refresh"); return nil }
	eligible := func() error { events = append(events, "eligible"); return nil }
	err := b.SendFinishWithRefresh(t.Context(), 1, refresh, eligible)
	var want []string
	for range 3 {
		want = append(want, "refresh", "deadline", "eligible", "prepare", "pair", "postflight")
	}
	want = append(want, "release")
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v err=%v", events, err)
	}
}

func TestInputFinishLastPostflightFailure(t *testing.T) {
	b := fakeInput(t)
	var keys []uint16
	b.key = func(key uint16) error {
		keys = append(keys, key) // Paired posting completed before native postflight failed.
		if key == inputKeyReturn {
			return ErrInputUnavailable
		}
		return nil
	}
	err := b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligibleOK)
	requireStopped(t, b, err, ErrInputUnavailable)
	if !reflect.DeepEqual(keys, []uint16{101, 18, 36}) {
		t.Fatalf("keys=%v", keys)
	}
	assertFinishClosed(t, b)
}

func TestInputFinishEarlyFailure(t *testing.T) {
	for _, stage := range []string{"refresh", "eligible", "prepare"} {
		b := fakeInput(t)
		refresh, eligible := refreshOK, eligibleOK
		b.key = func(uint16) error { t.Error("unexpected key"); return nil }
		switch stage {
		case "refresh":
			refresh = func(context.Context) error { return ErrInputUnavailable }
		case "eligible":
			eligible = func() error { return ErrInputUnavailable }
		case "prepare":
			b.prepare = func(uint64) error { return ErrInputUnavailable }
		}
		requireStopped(t, b, b.SendFinishWithRefresh(t.Context(), 1, refresh, eligible), ErrInputUnavailable)
		assertFinishClosed(t, b)
	}
}

func TestInputFinishCancelAndCloseInRefresh(t *testing.T) {
	for _, closeBinding := range []bool{false, true} {
		b := fakeInput(t)
		rejectNative(t, b)
		ctx, cancel := context.WithCancel(t.Context())
		refresh := func(context.Context) error { cancel(); return nil }
		want := context.Canceled
		if closeBinding {
			refresh = func(context.Context) error { return b.Close() }
			want = ErrInputStopped
		}
		err := b.SendFinishWithRefresh(ctx, 1, refresh, eligibleOK)
		cancel()
		requireStopped(t, b, err, want)
		assertFinishClosed(t, b)
	}
}

func TestInputFinishRefreshBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		b.deadline = func(budget time.Duration) uint64 {
			if budget != inputCheckBudget {
				t.Errorf("native budget=%v", budget)
			}
			return 1
		}
		refresh := func(context.Context) error { time.Sleep(2 * inputCheckBudget); return nil }
		if err := b.SendFinishWithRefresh(t.Context(), 1, refresh, eligibleOK); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInputFinishSequenceDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		keys := 0
		b.key = func(uint16) error { keys++; return nil }
		var deadline time.Time
		err := b.SendFinishWithRefresh(t.Context(), 1, sequenceExpiryRefresh(t, &deadline), eligibleOK)
		requireStopped(t, b, err, context.DeadlineExceeded)
		if keys != 1 {
			t.Fatalf("keys=%d", keys)
		}
	})
}

func TestInputFinishOwnerWaitBudget(t *testing.T) {
	b := fakeInput(t)
	rejectNative(t, b)
	b.owner.Lock()
	result := make(chan error, 1)
	go func() { result <- b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligibleOK) }()
	waitInputOwners(t, 1) // The deadline exists before the owner lock attempt.
	time.Sleep(InputSequenceTimeout)
	b.owner.Unlock()
	requireStopped(t, b, <-result, context.DeadlineExceeded)
	assertFinishClosed(t, b)
}

func TestInputFinishClosesBeforeOwnerUnlock(t *testing.T) {
	for _, fail := range []bool{false, true} {
		b := fakeInput(t)
		b.release = func() {
			if b.owner.TryLock() {
				b.owner.Unlock()
				t.Error("released owner before native cleanup")
			}
		}
		runQueuedFinish(t, b, fail)
		assertFinishClosed(t, b)
	}
}

func runQueuedFinish(t *testing.T, b *inputBinding, fail bool) {
	t.Helper()
	entered, proceed := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	refresh := func(context.Context) error { close(entered); <-proceed; return nil }
	b.key = func(uint16) error { return nil }
	go func() { result <- b.SendFinishWithRefresh(t.Context(), 1, refreshOnce(refresh, fail), eligibleOK) }()
	<-entered
	queued := queueInputSends(t, b)
	waitInputOwners(t, 3)
	close(proceed)
	err := <-result
	if (fail && !errors.Is(err, ErrInputUnavailable)) || (!fail && err != nil) {
		t.Fatalf("finish error=%v", err)
	}
	for range 3 {
		requireStopped(t, b, <-queued, ErrInputStopped)
	}
}

// Mutex waits are not durable in synctest. Observe actual blocked send stacks
// rather than relying on a scheduling sleep to establish the owner queue.
func waitInputOwners(t *testing.T, want int) {
	t.Helper()
	stack := make([]byte, 1<<20)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		blocked := 0
		for _, trace := range strings.Split(string(stack[:runtime.Stack(stack, true)]), "\n\n") {
			if strings.Contains(trace, "[sync.Mutex.Lock]") && strings.Contains(trace, "(*inputBinding).Send") {
				blocked++
			}
		}
		if blocked == want {
			return
		}
		runtime.Gosched()
	}
	t.Error("send goroutines did not reach owner wait")
}

func refreshOnce(first func(context.Context) error, fail bool) func(context.Context) error {
	return func(ctx context.Context) error {
		if first != nil {
			_ = first(ctx)
			first = nil
		}
		if fail {
			return ErrInputUnavailable
		}
		return nil
	}
}

func queueInputSends(t *testing.T, b *inputBinding) <-chan error {
	t.Helper()
	result := make(chan error, 3)
	eligible := func() error { t.Error("queued send reached eligibility"); return nil }
	go func() { result <- b.Send(t.Context(), 1, 0, eligible) }()
	go func() { result <- b.SendWithRefresh(t.Context(), 1, 0, refreshOK, eligible) }()
	go func() { result <- b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligible) }()
	return result
}

func TestInputFinishEligibilityBudget(t *testing.T) {
	var clock atomic.Uint64
	b := fakeTimedInput(t, &clock)
	eligible := func() error { clock.Add(uint64(inputCheckBudget)); return nil }
	err := b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligible)
	requireStopped(t, b, err, ErrInputUnavailable)
}

func TestInputFinishGateWaitBudget(t *testing.T) {
	var clock atomic.Uint64
	b := fakeTimedInput(t, &clock)
	b.gate.Lock()
	entered, result := make(chan struct{}), make(chan error, 1)
	go func() {
		eligible := func() error { close(entered); return nil }
		result <- b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligible)
	}()
	<-entered
	clock.Add(uint64(inputCheckBudget))
	b.gate.Unlock()
	requireStopped(t, b, <-result, ErrInputUnavailable)
}

func TestInputFinishCancelAfterPrepare(t *testing.T) {
	b := fakeInput(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.prepare = func(uint64) error { cancel(); return nil }
	b.key = func(uint16) error { t.Error("key after cancellation"); return nil }
	err := b.SendFinishWithRefresh(ctx, 1, refreshOK, eligibleOK)
	requireStopped(t, b, err, context.Canceled)
	assertFinishClosed(t, b)
}

func TestInputFinishEarlierParentDeadline(t *testing.T) {
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
		err := b.SendFinishWithRefresh(ctx, 1, refresh, eligibleOK)
		requireStopped(t, b, err, context.DeadlineExceeded)
	})
}

func TestInputFinishWaitsAfterReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := fakeInput(t)
		var posted time.Time
		b.key = func(key uint16) error {
			if key == inputKeyReturn {
				posted = time.Now()
			}
			return nil
		}
		if err := b.SendFinishWithRefresh(t.Context(), 1, refreshOK, eligibleOK); err != nil {
			t.Fatal(err)
		}
		if time.Since(posted) != inputKeyInterval {
			t.Fatal("missing final key wait")
		}
	})
}
