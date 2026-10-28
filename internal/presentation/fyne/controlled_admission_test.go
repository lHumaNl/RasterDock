package fyneui

import (
	"context"
	"errors"
	"image"
	"testing"
	"testing/synctest"
	"time"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/control"
	v1 "rasterdock/internal/protocol/v1"
)

type ignoredFrameStream struct {
	*controlledScene
	kind  string
	calls int
}

func (s *ignoredFrameStream) Next(ctx context.Context) (platform.Frame, error) {
	if err := ctx.Err(); err != nil {
		return platform.Frame{}, err
	}
	time.Sleep(100 * time.Millisecond)
	s.calls++
	at, bounded := time.Now(), true
	if s.calls > 1 {
		if s.kind == "stale" {
			at = at.Add(-2 * time.Second)
		}
		if s.kind == "unbounded" {
			bounded = false
		}
	}
	return platform.Frame{ControlCapturedAt: at, ControlAgeBounded: bounded, Generation: 1}, nil
}

func TestIgnoredAcquisitionsCannotPersistMissingPackets(t *testing.T) {
	for _, kind := range []string{"stale", "unbounded", "lower-frame"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &controlledScene{p: prepareFixture(t)}
				stream := &ignoredFrameStream{controlledScene: s, kind: kind}
				root := t.TempDir()
				deps := sceneDependencies(s, root)
				deps.controlCapture = func(uint32) (captureStream, error) { return stream, nil }
				metadata, err := s.p.Packet(0)
				if err != nil {
					t.Fatal(err)
				}
				data, err := s.p.Packet(1)
				if err != nil {
					t.Fatal(err)
				}
				deps.controlDecode = func(image.Image) (v1.Beacon, []v1.Packet, error) {
					b := v1.Beacon{Descriptor: s.p.Descriptor, Frame: 1, Epoch: 9, Flags: 2}
					if stream.calls == 1 {
						return b, []v1.Packet{metadata}, nil
					}
					b.Frame, b.Nonce, b.Ordinal = 2, s.nonce, 1
					if kind == "lower-frame" {
						b.Frame = 0
					}
					return b, []v1.Packet{data}, nil
				}
				accepted := 0
				open := deps.openReceive
				deps.openReceive = func(dir, dest string, d v1.Descriptor) (receiveOwner, error) {
					r, err := open(dir, dest, d)
					if err == nil {
						s.receiver.afterAccept = func() { accepted++ }
					}
					return r, err
				}
				completed := false
				start := time.Now()
				err = runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil,
					func(v snapshot) {
						completed = completed || v.key == "control.complete" || v.key == "control.complete_finish_unconfirmed"
					}, deps)
				if !errors.Is(err, control.ErrTimeout) || accepted != 1 || s.finishes != 0 || completed || s.sends != control.MaxSends || time.Since(start) > 9*time.Second {
					t.Fatalf("ignored capture advanced data or extended budget: %v accept=%d finish=%d sends=%d elapsed=%s", err, accepted, s.finishes, s.sends, time.Since(start))
				}
			})
		})
	}
}

type closeFailInput struct{ pausedInput }

func (i closeFailInput) Close() error { _ = i.sceneInput.Close(); return errInjected }

type closeFailStore struct{ plannedReceiveOwner }

func (r closeFailStore) Close() error { return errors.Join(r.plannedReceiveOwner.Close(), errInjected) }

func TestCleanupFailurePreventsFocusResume(t *testing.T) {
	for _, kind := range []string{"input", "capture", "store"} {
		s := &controlledScene{p: prepareFixture(t)}
		root := t.TempDir()
		deps := sceneDependencies(s, root)
		deps.bindInput = func(uint32) (platform.InputBinding, error) {
			i := pausedInput{sceneInput{s}, platform.ErrInputFocusLost}
			if kind == "input" {
				return closeFailInput{i}, nil
			}
			return i, nil
		}
		deps.controlDecode = func(image.Image) (v1.Beacon, []v1.Packet, error) {
			return v1.Beacon{Descriptor: s.p.Descriptor, Frame: s.frame, Epoch: 9, Flags: 2}, nil, nil
		}
		if kind == "capture" {
			deps.controlCapture = func(uint32) (captureStream, error) { return finishCloseFailure{s}, nil }
		}
		if kind == "store" {
			open := deps.openReceive
			deps.openReceive = func(dir, dest string, d v1.Descriptor) (receiveOwner, error) {
				r, err := open(dir, dest, d)
				if err != nil {
					return nil, err
				}
				return closeFailStore{r.(plannedReceiveOwner)}, nil
			}
		}
		err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
		var paused *receivePausedError
		if errors.As(err, &paused) || !errors.Is(err, platform.ErrInputFocusLost) || !errors.Is(err, errInjected) || s.finishes != 0 {
			t.Fatal("cleanup error offered Resume", kind, err)
		}
	}
}
