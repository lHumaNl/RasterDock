package fyneui

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	v1 "rasterdock/internal/protocol/v1"
)

type pausedInput struct {
	sceneInput
	failure error
}

func (i pausedInput) SendWithRefresh(ctx context.Context, _, _ uint64, refresh func(context.Context) error, eligible func() error) error {
	if err := refresh(ctx); err != nil {
		return err
	}
	if err := eligible(); err != nil {
		return err
	}
	return i.failure // Simulates native failure after a partial key sequence.
}

func TestControlledPausePreservesDurableSessionAfterCleanup(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root, destination := t.TempDir(), t.TempDir()
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) {
		return pausedInput{sceneInput{s}, platform.ErrInputFocusLost}, nil
	}
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, packets, err := codec.DecodeControlled(img)
		if len(packets) > 1 {
			packets = packets[:1]
		}
		return b, packets, err
	}
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: destination}, nil, func(snapshot) {}, deps)
	var paused *receivePausedError
	if !errors.As(err, &paused) {
		t.Fatalf("not paused: %v", err)
	}
	r := paused.resume
	if r.command.session == "" || r.command.destination != destination || r.command.window != 1 || r.command.resume != nil || r.target != s.Target() || r.descriptor != s.p.Descriptor {
		t.Fatalf("lost resume identity: %+v", r)
	}
	if _, err := os.Stat(r.command.session); err != nil {
		t.Fatal(err)
	}
	if s.closes != 1 || s.inputClose != 1 || s.finishes != 0 {
		t.Fatal("pause preceded cleanup or issued finish")
	}
	assertControlledResumeCompletes(t, root, destination, s, r)
}

func assertControlledResumeCompletes(t *testing.T, root, destination string, s *controlledScene, r *receiveResume) {
	t.Helper()
	deps := sceneDependencies(s, root)
	// Do not redeliver metadata: completion requires the durable pre-pause store.
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, packets, err := codec.DecodeControlled(img)
		if len(packets) > 1 {
			packets = packets[1:]
		}
		return b, packets, err
	}
	cmd := r.command
	cmd.resume = r
	if err := runControlledReceive(t.Context(), root, cmd, nil, func(snapshot) {}, deps); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, payloadName)) //nolint:gosec // Fixed fixture basename under test-owned temporary destination.
	if err != nil || string(data) != payloadText || s.finishes != 1 {
		t.Fatalf("resume lost durable data: %v", err)
	}
}

func TestControlledProcessGoneAndGenericErrorsDoNotPause(t *testing.T) {
	for _, failure := range []error{platform.ErrInputProcessGone, errInjected, context.Canceled} {
		s := &controlledScene{p: prepareFixture(t)}
		root := t.TempDir()
		deps := sceneDependencies(s, root)
		deps.bindInput = func(uint32) (platform.InputBinding, error) { return pausedInput{sceneInput{s}, failure}, nil }
		deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
			b, _, err := codec.DecodeControlled(img)
			return b, nil, err
		}
		err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
		var paused *receivePausedError
		if !errors.Is(err, failure) || errors.As(err, &paused) {
			t.Fatalf("unexpected pause: %v", err)
		}
	}
}

func TestControlledResumeRejectsIdentityAndDescriptorChanges(t *testing.T) {
	for _, kind := range []string{"pid", "start", "window", "title", "descriptor"} {
		t.Run(kind, func(t *testing.T) {
			s := &controlledScene{p: prepareFixture(t)}
			r := &receiveResume{target: s.Target(), descriptor: s.p.Descriptor}
			switch kind {
			case "pid":
				r.target.PID++
			case "start":
				r.target.Started++
			case "window":
				r.target.Window++
			case "title":
				r.target.Title += " changed"
			case "descriptor":
				r.descriptor.Length++
			}
			root := t.TempDir()
			err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir(), resume: r}, nil, func(snapshot) {}, sceneDependencies(s, root))
			if err == nil || s.sends != 0 || s.finishes != 0 || s.closes != 1 || s.inputClose != 1 {
				t.Fatalf("unsafe resume: %v", err)
			}
		})
	}
}
