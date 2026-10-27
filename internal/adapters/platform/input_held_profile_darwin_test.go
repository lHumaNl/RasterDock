//go:build integration && darwin && cgo

package platform

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func heldProfileFake(check func() error) *HeldCheckProfile {
	b := &inputBinding{done: make(chan struct{}), check: check}
	return &HeldCheckProfile{input: b, guard: func() error { return nil }}
}

func TestHeldProfileFirstRefusal(t *testing.T) {
	want := errors.New("refused")
	calls := 0
	p := heldProfileFake(func() error { calls++; return want })
	err := p.Run(context.Background(), func() (bool, error) { t.Fatal("step after refusal"); return false, nil })
	if !errors.Is(err, want) || calls != 1 || !errors.Is(p.Check(), ErrInputStopped) {
		t.Fatal("first refusal did not permanently stop same binding")
	}
	if _, ok := any(p).(InputBinding); ok {
		t.Fatal("profile exposes Send")
	}
}

func TestHeldProfileCapAndCancellation(t *testing.T) {
	calls := 0
	p := heldProfileFake(func() error { calls++; return nil })
	err := p.Run(context.Background(), func() (bool, error) { return false, nil })
	if !errors.Is(err, ErrHeldProfileLimit) || calls != HeldProfileLimit {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = heldProfileFake(func() error { t.Fatal("check after cancellation"); return nil })
	if err = p.Run(ctx, func() (bool, error) { return true, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHeldProfileSameBindingOrder(t *testing.T) {
	var events []string
	p := heldProfileFake(func() error { events = append(events, "check"); return nil })
	b := p.input.(*inputBinding)
	b.release = func() { events = append(events, "close") }
	err := p.Run(context.Background(), func() (bool, error) {
		events = append(events, "next", "decode")
		return len(events) == 6, nil
	})
	if err != nil || p.input != b || p.Close() != nil {
		t.Fatal("binding changed or operation failed")
	}
	if !reflect.DeepEqual(events, []string{"check", "next", "decode", "check", "next", "decode", "close"}) {
		t.Fatal(events)
	}
}

func TestHeldProfileDeadlineDuringGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := heldProfileFake(func() error { t.Fatal("Check after guard cancelled context"); return nil })
	p.guard = func() error { cancel(); return nil }
	if err := p.Run(ctx, func() (bool, error) { t.Fatal("step after deadline"); return true, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHeldProfileExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	p := heldProfileFake(func() error { t.Fatal("Check after deadline"); return nil })
	if err := p.Run(ctx, func() (bool, error) { return true, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHeldProfileCancellationDuringCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := heldProfileFake(func() error { cancel(); return nil })
	err := p.Run(ctx, func() (bool, error) { t.Fatal("Next after cancellation"); return true, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHeldProfileGuardRefusalAndInvalidTarget(t *testing.T) {
	p := heldProfileFake(func() error { t.Fatal("Check after guard refusal"); return nil })
	want := errors.New("owned child gone")
	p.guard = func() error { return want }
	if !errors.Is(p.Check(), want) || !p.input.(*inputBinding).stopped.Load() {
		t.Fatal("guard failure must close the same input")
	}
	if profile, err := BindHeldCheckProfile(0, 0); err == nil || profile != nil {
		t.Fatal("invalid target accepted")
	}
}

func TestHeldProfileSamplesAreDetached(t *testing.T) {
	p := heldProfileFake(func() error { return nil })
	p.samples = []CheckProfileSample{{Records: []CheckProfileRecord{{Stage: 1}}}}
	samples := p.Samples()
	samples[0].Records[0].Stage = 2
	if p.Samples()[0].Records[0].Stage != 1 {
		t.Fatal("sample snapshot aliases owned records")
	}
}

func TestHeldProfileCloseWaitsForCheck(t *testing.T) {
	entered, finish, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p := heldProfileFake(func() error { close(entered); <-finish; return nil })
	p.input.(*inputBinding).release = func() {
		select {
		case <-finish:
		default:
			t.Error("released during native call")
		}
	}
	go func() { defer close(returned); _ = p.Check() }()
	<-entered
	closed := make(chan struct{})
	go func() { defer close(closed); _ = p.Close() }()
	close(finish)
	<-returned
	<-closed
}
