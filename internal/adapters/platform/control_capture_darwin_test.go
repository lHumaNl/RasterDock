//go:build darwin && cgo

package platform

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestControlTargetIsImmutableAfterClose(t *testing.T) {
	want := ControlTarget{Window: 17, PID: 23, Started: 1000001, Title: "Selected native window"}
	s := &controlCaptureStream{target: want}
	snapshot := s.Target()
	snapshot.Title = "Caller label must not replace provenance"
	// This headless fixture owns no native resource; teardown is already complete.
	s.closeOnce.Do(func() {})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Target() != want {
		t.Fatal(s.Target())
	}
	if err := s.CheckControlAge(); !errors.Is(err, ErrStreamClosed) {
		t.Fatal(err)
	}
}

func TestControlTargetConcurrentReadsAfterRevocation(t *testing.T) {
	want := ControlTarget{Window: 17, PID: 23, Started: 1000001, Title: "Selected native window"}
	s := &controlCaptureStream{target: want}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			s.closed.Store(true)
			if s.Target() != want {
				t.Error("identity changed")
			}
		})
	}
	workers.Wait()
}

// A revoked stream must reject before any native access (handle is deliberately nil).
func TestControlRevocationPrecedesNativeAccess(t *testing.T) {
	s := &controlCaptureStream{}
	s.closed.Store(true)
	if !errors.Is(s.CheckControlAge(), ErrStreamClosed) {
		t.Fatal("closed age check accepted")
	}
	frame, err := s.Next(context.Background())
	if !errors.Is(err, ErrStreamClosed) || frame.ControlAgeBounded {
		t.Fatal(frame, err)
	}
}

func TestControlCancellationPrecedesNativeAccess(t *testing.T) {
	s := &controlCaptureStream{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
