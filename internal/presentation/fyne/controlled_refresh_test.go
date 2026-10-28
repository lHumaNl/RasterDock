package fyneui

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/control"
	v1 "rasterdock/internal/protocol/v1"
)

type refreshCapture struct {
	calls int
	next  func(context.Context) (platform.Frame, error)
}

func (s *refreshCapture) Next(ctx context.Context) (platform.Frame, error) {
	s.calls++
	return s.next(ctx)
}
func (*refreshCapture) Close() error { return nil }

func refreshGate(t *testing.T, age time.Duration) (*control.Receiver, v1.Beacon) {
	t.Helper()
	b := v1.Beacon{Descriptor: prepareFixture(t).Descriptor, Frame: 1, Epoch: 9, Flags: 3}
	g := &control.Receiver{}
	now := time.Now()
	if err := g.Bind(b.Descriptor, control.Observation{Beacon: b, CapturedAt: now.Add(-age), AgeBounded: true, Generation: 1}, now); err != nil {
		t.Fatal(err)
	}
	return g, b
}

func refreshFrame() platform.Frame {
	return platform.Frame{ControlCapturedAt: time.Now(), ControlAgeBounded: true, Generation: 1}
}

func TestControlledRefreshSkipsAmpleFreshness(t *testing.T) {
	g, _ := refreshGate(t, 0)
	s := &refreshCapture{next: func(context.Context) (platform.Frame, error) {
		t.Fatal("unnecessary capture")
		return platform.Frame{}, nil
	}}
	checks := 0
	err := refreshControlledObservation(t.Context(), g, s, nil, func() error { checks++; return nil })
	if err != nil || checks != 1 || s.calls != 0 {
		t.Fatalf("skip: %v checks=%d captures=%d", err, checks, s.calls)
	}
}

func TestControlledRefreshAcceptsNewHeartbeat(t *testing.T) {
	g, b := refreshGate(t, 500*time.Millisecond)
	old := g.FreshUntil()
	b.Frame++
	s := &refreshCapture{next: func(context.Context) (platform.Frame, error) { return refreshFrame(), nil }}
	err := refreshControlledObservation(t.Context(), g, s, func(image.Image) (v1.Beacon, []v1.Packet, error) {
		return b, []v1.Packet{{Kind: 2}}, nil // Data is intentionally ignored here.
	}, func() error { return nil })
	if err != nil || s.calls != 1 || !g.FreshUntil().After(old) {
		t.Fatalf("refresh: %v captures=%d", err, s.calls)
	}
}

func TestLegacyRefreshAcceptsNewAcquisitionAfterStaticPage(t *testing.T) {
	g, b := refreshGate(t, 700*time.Millisecond)
	b.Frame++
	s := &refreshCapture{next: func(context.Context) (platform.Frame, error) { return refreshFrame(), nil }}
	err := refreshControlledObservation(t.Context(), g, s, func(image.Image) (v1.Beacon, []v1.Packet, error) {
		time.Sleep(350 * time.Millisecond) // Only incoming acquisition age matters.
		return b, nil, nil
	}, func() error { return nil })
	if err != nil || g.FreshUntil().IsZero() {
		t.Fatalf("valid acquisition rejected: %v", err)
	}
}

func TestControlledRefreshRejectsAmbiguityAndIdentityChange(t *testing.T) {
	for _, kind := range []string{"ambiguous", "generation", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			g, b := refreshGate(t, 500*time.Millisecond)
			b.Frame++
			s := &refreshCapture{next: func(context.Context) (platform.Frame, error) {
				f := refreshFrame()
				if kind == "generation" {
					f.Generation++
				}
				return f, nil
			}}
			err := refreshControlledObservation(t.Context(), g, s, func(image.Image) (v1.Beacon, []v1.Packet, error) {
				if kind == "ambiguous" {
					return b, nil, codec.ErrAmbiguousControl
				}
				return b, nil, nil
			}, func() error {
				if kind == "revoked" && s.calls > 0 {
					return context.Canceled
				}
				return nil
			})
			if err == nil || !g.FreshUntil().IsZero() {
				t.Fatalf("invalid refresh accepted: %v", err)
			}
		})
	}
}

func TestControlledRefreshCannotRenewFromDuplicateOrDecodeFailure(t *testing.T) {
	for _, kind := range []string{"duplicate", "decode"} {
		t.Run(kind, func(t *testing.T) {
			g, b := refreshGate(t, 800*time.Millisecond)
			s := &refreshCapture{next: func(ctx context.Context) (platform.Frame, error) {
				timer := time.NewTimer(10 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return platform.Frame{}, ctx.Err()
				case <-timer.C:
					return refreshFrame(), nil
				}
			}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := refreshControlledObservation(ctx, g, s, func(image.Image) (v1.Beacon, []v1.Packet, error) {
				if kind == "decode" {
					return b, nil, errInjected
				}
				return b, nil, nil
			}, func() error { return nil })
			if err == nil || !g.FreshUntil().IsZero() || s.calls == 0 {
				t.Fatalf("invalid observation renewed authority: %v calls=%d", err, s.calls)
			}
		})
	}
}

type slowRefreshingInput struct {
	sceneInput
	keys int
}

func (i *slowRefreshingInput) SendWithRefresh(ctx context.Context, nonce, ordinal uint64, refresh func(context.Context) error, eligible func() error) error {
	ctx, cancel := context.WithTimeout(ctx, platform.InputSequenceTimeout)
	defer cancel()
	for range 8 {
		want, missing, err := i.scene.receiver.NextMissing()
		if err != nil || !missing {
			return errors.New("expected missing packets before input")
		}
		if err := refresh(ctx); err != nil {
			return err
		}
		got, stillMissing, err := i.scene.receiver.NextMissing()
		if err != nil || !stillMissing || got != want {
			return errors.New("refresh persisted or advanced data")
		}
		if err := eligible(); err != nil {
			return err
		}
		i.keys++
		timer := time.NewTimer(160 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return i.Send(ctx, nonce, ordinal, eligible)
}

func TestControlledReceiveLongSequenceWithoutPerKeyCapture(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	i := &slowRefreshingInput{sceneInput: sceneInput{s}}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) { return i, nil }
	first := true
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, packets, err := codec.DecodeControlled(img)
		if first {
			first = false
			packets = nil
		}
		return b, packets, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	complete := false
	err := runControlledReceive(ctx, root, command{window: 1, destination: t.TempDir()}, nil, func(s snapshot) { complete = complete || s.key == "control.complete" }, deps)
	if err != nil || !complete || i.keys != 8 || s.sends != 1 || s.frame != 2 {
		t.Fatalf("long sequence: %v complete=%v keys=%d commands=%d frames=%d", err, complete, i.keys, s.sends, s.frame)
	}
}

type legacyInputOnly struct{ platform.InputBinding }

func TestControlledReceiveRefusesMissingRefreshCapability(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) { return legacyInputOnly{sceneInput{s}}, nil }
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
	if err == nil || s.sends != 0 || s.frame != 0 || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("missing refresh capability accepted: %v frames=%d sends=%d", err, s.frame, s.sends)
	}
}
