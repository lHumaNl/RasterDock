package fyneui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

type controlledScene struct {
	p                         *application.Prepared
	ordinal, nonce, frame     uint64
	sends, closes, inputClose int
	receiver                  *controlledStore
	failAccept, unbounded     bool
	betweenKeys               func()
}

type controlledStore struct {
	*storage.Receiver
	fail        bool
	afterAccept func()
}

func (r *controlledStore) Accept(p v1.Packet) error {
	if r.fail {
		return errInjected
	}
	err := r.Receiver.Accept(p)
	if r.afterAccept != nil {
		r.afterAccept()
	}
	return err
}

func (s *controlledScene) Next(ctx context.Context) (platform.Frame, error) {
	if err := ctx.Err(); err != nil {
		return platform.Frame{}, err
	}
	s.frame++
	profile := application.DefaultVisualProfile(application.Colors8)
	packets, err := s.p.VisualPage(s.ordinal, profile)
	if err != nil {
		return platform.Frame{}, err
	}
	img, err := codec.RenderControlledPage(s.p.Descriptor, packets, profile, s.ordinal, codec.LiveState{Frame: s.frame, Epoch: 9, Nonce: s.nonce, Focused: true, Armed: true})
	at := time.Now()
	return platform.Frame{Image: img, CapturedAt: at, ControlCapturedAt: at, ControlAgeBounded: !s.unbounded, Generation: 1}, err
}

func (s *controlledScene) Close() error           { s.closes++; return nil }
func (s *controlledScene) CheckControlAge() error { return nil }
func (s *controlledScene) Target() platform.ControlTarget {
	return platform.ControlTarget{Window: 1, PID: 42, Started: 1, Title: "test-window"}
}

type sceneInput struct{ scene *controlledScene }

func (i sceneInput) SendWithRefresh(ctx context.Context, nonce, ordinal uint64, refresh func(context.Context) error, eligible func() error) error {
	if err := refresh(ctx); err != nil {
		return err
	}
	return i.Send(ctx, nonce, ordinal, eligible)
}

func (i sceneInput) Target() platform.ControlTarget { return i.scene.Target() }
func (i sceneInput) Check() error                   { return nil }
func (i sceneInput) Close() error                   { i.scene.inputClose++; return nil }
func (i sceneInput) Send(ctx context.Context, nonce, ordinal uint64, eligible func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := eligible(); err != nil {
		return err
	}
	s := i.scene
	if s.betweenKeys != nil {
		s.betweenKeys()
		if err := eligible(); err != nil {
			return err
		}
	}
	want, missing, err := s.receiver.NextMissing()
	if err != nil || !missing || want != ordinal {
		return fmt.Errorf("input preceded durable planning: want %d missing %v; got %d: %w", want, missing, ordinal, err)
	}
	s.sends++
	s.nonce, s.ordinal = nonce, ordinal
	return nil
}

func TestControlledReceiveMultiPageManifestAndPartialPlane(t *testing.T) {
	root, source, destination := t.TempDir(), t.TempDir(), t.TempDir()
	want := make(map[string][]byte)
	for i := range 40 {
		name := fmt.Sprintf("file-%03d.bin", i)
		want[name] = bytes.Repeat([]byte{byte(i)}, 700)
		if err := os.WriteFile(filepath.Join(source, name), want[name], 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := application.Prepare(t.Context(), source, t.TempDir(), 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	if p.ManifestPackets() <= 3 {
		t.Fatal("fixture must span multiple manifest pages")
	}
	s := &controlledScene{p: p}
	deps := sceneDependencies(s, root)
	first := true
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, packets, e := codec.DecodeControlled(img)
		if first && e == nil {
			first = false
			packets = packets[1:] // Missing metadata plane must be requested again.
		}
		return b, packets, e
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	complete := false
	err = runControlledReceive(ctx, root, command{window: 1, destination: destination}, nil, func(s snapshot) {
		complete = complete || s.key == "control.complete"
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || s.sends < 2 || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("incomplete lifecycle: complete=%v sends=%d capture closes=%d input closes=%d", complete, s.sends, s.closes, s.inputClose)
	}
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Names are generated test-owned basenames under t.TempDir.
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("output %s mismatch: %v", name, err)
		}
	}
}

func sceneDependencies(s *controlledScene, root string) engineIO {
	return engineIO{
		controlCapture: func(uint32) (captureStream, error) { return s, nil },
		controlDecode:  codec.DecodeControlled,
		bindInput:      func(uint32) (platform.InputBinding, error) { return sceneInput{s}, nil },
		openReceive: func(dir, destination string, d v1.Descriptor) (receiveOwner, error) {
			r, err := storage.OpenReceive(dir, destination, d, application.DecodeManifest, root)
			if err != nil {
				return nil, err
			}
			s.receiver = &controlledStore{Receiver: r, fail: s.failAccept}
			return s.receiver, nil
		},
	}
}

func TestControlledReceiveRefusesUnstoredOrUnboundedFrame(t *testing.T) {
	for _, failure := range []string{"storage", "timestamp"} {
		t.Run(failure, func(t *testing.T) {
			s := &controlledScene{p: prepareFixture(t), failAccept: failure == "storage", unbounded: failure == "timestamp"}
			root := t.TempDir()
			err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, sceneDependencies(s, root))
			if err == nil || s.sends != 0 || s.closes != 1 || s.inputClose != 1 {
				t.Fatalf("unsafe failure: err=%v sends=%d closes=%d/%d", err, s.sends, s.closes, s.inputClose)
			}
			if failure == "storage" && !errors.Is(err, errInjected) {
				t.Fatalf("lost storage failure: %v", err)
			}
		})
	}
}

func TestControlledReceiveCancellationDuringHandoffDoesNotOpenTarget(t *testing.T) {
	commands := make(chan command, 1)
	commands <- command{kind: "pause"}
	deps := engineIO{
		controlDelay: time.Hour,
		controlCapture: func(uint32) (captureStream, error) {
			t.Fatal("capture opened after handoff cancellation")
			return nil, errInjected
		},
		controlDecode: codec.DecodeControlled,
		bindInput: func(uint32) (platform.InputBinding, error) {
			t.Fatal("input bound after handoff cancellation")
			return nil, errInjected
		},
	}
	err := runControlledReceive(t.Context(), t.TempDir(), command{window: 1, destination: t.TempDir()}, commands, func(snapshot) {}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handoff cancellation: %v", err)
	}
}

func TestControlledReceiveAmbiguityRequiresReselection(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.controlDecode = func(image.Image) (v1.Beacon, []v1.Packet, error) {
		return v1.Beacon{}, nil, codec.ErrAmbiguousControl
	}
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
	if !errors.Is(err, codec.ErrAmbiguousControl) || s.sends != 0 || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("ambiguous target accepted: %v sends=%d closes=%d/%d", err, s.sends, s.closes, s.inputClose)
	}
}

func TestControlledReceiveRevocationDuringPersistence(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	commands := make(chan command, 1)
	deps := sceneDependencies(s, root)
	open := deps.openReceive
	deps.openReceive = func(dir, destination string, d v1.Descriptor) (receiveOwner, error) {
		r, err := open(dir, destination, d)
		if err == nil {
			s.receiver.afterAccept = func() {
				select {
				case commands <- command{kind: "pause"}:
				default:
				}
			}
		}
		return r, err
	}
	completed := false
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, commands, func(s snapshot) { completed = completed || s.key == "control.complete" }, deps)
	if !errors.Is(err, context.Canceled) || s.sends != 0 || completed {
		t.Fatalf("revocation ignored: %v sends=%d complete=%v", err, s.sends, completed)
	}
}

type waitingControlCapture struct {
	*controlledScene
	onWait func()
}

func (s *waitingControlCapture) Next(ctx context.Context) (platform.Frame, error) {
	if s.onWait != nil {
		s.onWait()
	}
	<-ctx.Done()
	return platform.Frame{}, ctx.Err()
}

func TestControlledReceiveCaptureWaitServicesRevocation(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	commands := make(chan command, 1)
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	stream := &waitingControlCapture{controlledScene: s, onWait: func() { commands <- command{kind: "pause"} }}
	deps.controlCapture = func(uint32) (captureStream, error) { return stream, nil }
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := runControlledReceive(ctx, root, command{window: 1, destination: t.TempDir()}, commands, func(snapshot) {}, deps)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("capture wait withheld revocation: %v after %v closes=%d/%d", err, time.Since(start), s.closes, s.inputClose)
	}
}

type staleControlCapture struct{ *controlledScene }

func (s *staleControlCapture) Next(ctx context.Context) (platform.Frame, error) {
	if s.frame == 0 {
		return s.controlledScene.Next(ctx)
	}
	<-ctx.Done()
	return platform.Frame{}, ctx.Err()
}

func TestControlledReceiveBoundThenStalledCaptureExpires(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.controlCapture = func(uint32) (captureStream, error) { return &staleControlCapture{s}, nil }
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, _, err := codec.DecodeControlled(img)
		return b, nil, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := runControlledReceive(ctx, root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second || s.sends != 1 || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("stale capture retained control: %v elapsed=%v sends=%d closes=%d/%d", err, time.Since(start), s.sends, s.closes, s.inputClose)
	}
}

type mismatchedInput struct{ sceneInput }

func (i mismatchedInput) Target() platform.ControlTarget {
	target := i.sceneInput.Target()
	target.Started++
	return target
}

func TestControlledReceiveRejectsReusedWindowIdentity(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) { return mismatchedInput{sceneInput{s}}, nil }
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, deps)
	if err == nil || s.frame != 0 || s.sends != 0 || s.closes != 1 || s.inputClose != 1 {
		t.Fatalf("reused source identity accepted: %v frames=%d sends=%d closes=%d/%d", err, s.frame, s.sends, s.closes, s.inputClose)
	}
}

func TestControlledReceiveRevocationBetweenKeys(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	commands := make(chan command, 1)
	s.betweenKeys = func() { commands <- command{kind: "pause"} }
	root := t.TempDir()
	deps := sceneDependencies(s, root)
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, _, err := codec.DecodeControlled(img)
		return b, nil, err // Valid echo is not proof that any payload was saved.
	}
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, commands, func(snapshot) {}, deps)
	if !errors.Is(err, context.Canceled) || s.sends != 0 {
		t.Fatalf("continued after mid-send revocation: %v sends=%d", err, s.sends)
	}
}
