package platform

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeNative struct {
	read    func() (Frame, error)
	release func() error
}

func (n *fakeNative) next() (Frame, error) { return n.read() }
func (n *fakeNative) close() error         { return n.release() }

func TestWorkerTerminalErrorSurvivesBlockedCleanup(t *testing.T) {
	gate, release := make(chan struct{}, 1), make(chan struct{})
	fatal := errors.New("device lost")
	var invalidated atomic.Bool
	n := &fakeNative{read: func() (Frame, error) { return Frame{}, fatal }, release: func() error { <-release; return nil }}
	s, err := startWorker(gate, func() (nativeStream, error) { return n, nil }, func() { invalidated.Store(true) })
	requireError(t, err, nil)
	defer close(release)
	requireNextError(t, s, fatal)
	<-s.ended
	requireNextError(t, s, fatal)
	if !invalidated.Load() {
		t.Fatal("token not invalidated before cleanup")
	}
	requireError(t, s.Close(), ErrCloseTimeout)
}

func delayedNative(release, started chan struct{}) *fakeNative {
	var calls atomic.Int32
	return &fakeNative{read: func() (Frame, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return Frame{CapturedAt: time.Unix(1, 0)}, nil
		}
		return Frame{}, ErrFramePending
	}, release: func() error { return nil }}
}

func TestWorkerDeliversSlowFrameOnceWithoutBlockingCaller(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	n := delayedNative(release, started)
	s, err := startWorker(make(chan struct{}, 1), func() (nativeStream, error) { return n, nil }, nil)
	requireError(t, err, nil)
	requireNextError(t, s, ErrFramePending)
	<-started
	close(release)
	defer func() { requireError(t, s.Close(), nil) }()
	// Next timing out must not discard the only frame of an unchanged window.
	// Its original acquisition time must survive, and it must be delivered once.
	if f, err := s.Next(context.Background()); err != nil || !f.CapturedAt.Equal(time.Unix(1, 0)) {
		t.Fatal(f, err)
	}
	if f, err := s.Next(context.Background()); !errors.Is(err, ErrFramePending) || !f.CapturedAt.IsZero() {
		t.Fatal("frame was replayed", f, err)
	}
}

func TestTokenExhaustionAndChangedOwner(t *testing.T) {
	r := newTokenRegistry()
	a, _ := r.register(windowIdentity{handle: 5, pid: 1, tid: 2})
	b, _ := r.register(windowIdentity{handle: 5, pid: 2, tid: 2})
	if a == b {
		t.Fatal("changed process reused token")
	}
	r.next = uint64(^uint32(0))
	if _, err := r.register(windowIdentity{handle: 9}); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
}

func TestWorkerTerminalStateRejectsQueuedPixels(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "source-failed", true: "stream-closed"}[closed], func(t *testing.T) {
			fatal := errors.New("source invalidated")
			s := &captureWorker{stop: make(chan struct{}), ended: make(chan struct{}),
				requests: make(chan struct{}, 1), frames: make(chan Frame, 1), terminal: fatal}
			s.frames <- Frame{CapturedAt: time.Unix(1, 0)}
			want := fatal
			if closed {
				close(s.stop)
				want = ErrStreamClosed
			} else {
				close(s.ended)
			}
			requireNextError(t, s, want)
		})
	}
}

func TestWorkerCloseDuringReadRejectsLateFrameAndRetainsGate(t *testing.T) {
	gate := make(chan struct{}, 1)
	readStarted, readRelease := make(chan struct{}), make(chan struct{})
	cleanupStarted, cleanupRelease := make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	n := &fakeNative{read: func() (Frame, error) {
		close(readStarted)
		<-readRelease
		return Frame{CapturedAt: time.Unix(1, 0)}, nil
	}, release: func() error {
		closes.Add(1)
		close(cleanupStarted)
		<-cleanupRelease
		return nil
	}}
	s, err := startWorker(gate, func() (nativeStream, error) { return n, nil }, nil)
	requireError(t, err, nil)
	requireNextError(t, s, ErrFramePending)
	waitDone(t, readStarted)
	requireError(t, s.Close(), ErrCloseTimeout)
	requireNextError(t, s, ErrStreamClosed)
	_, err = startWorker(gate, nil, nil)
	requireError(t, err, ErrCaptureBusy)
	close(readRelease)
	waitDone(t, cleanupStarted)
	requireNextError(t, s, ErrStreamClosed)
	_, err = startWorker(gate, nil, nil)
	requireError(t, err, ErrCaptureBusy)
	close(cleanupRelease)
	waitDone(t, s.done)
	requireError(t, s.Close(), nil)
	if closes.Load() != 1 {
		t.Fatalf("native cleanup count=%d", closes.Load())
	}
	next, err := startWorker(gate, func() (nativeStream, error) {
		return &fakeNative{read: func() (Frame, error) { return Frame{}, ErrFramePending }, release: func() error { return nil }}, nil
	}, nil)
	requireError(t, err, nil)
	requireError(t, next.Close(), nil)
}
