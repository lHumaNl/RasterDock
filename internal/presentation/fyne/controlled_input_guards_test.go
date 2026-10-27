package fyneui

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/control"
	v1 "rasterdock/internal/protocol/v1"
)

// This fake verifies receiver callback composition only. Native prepare/key order
// is tested against the actual inputBinding with fake hooks in platform tests.
type guardInput struct {
	sceneInput
	inSend            bool
	checks, callbacks int
	failCheck         int
	beforeCallback    func()
	keys              int
	allowSend         bool
	sentAt            time.Time
}

func (i *guardInput) Check() error {
	if i.inSend {
		return errors.New("observational check inside Send")
	}
	i.checks++
	if i.checks == i.failCheck {
		return errInjected
	}
	return nil
}

func (i *guardInput) Send(ctx context.Context, _, _ uint64, eligible func() error) error {
	return i.simulateSend(ctx, nil, eligible)
}

func (i *guardInput) SendWithRefresh(ctx context.Context, _, _ uint64, refresh func(context.Context) error, eligible func() error) error {
	return i.simulateSend(ctx, refresh, eligible)
}

func (i *guardInput) simulateSend(ctx context.Context, refresh func(context.Context) error, eligible func() error) error {
	i.inSend = true
	defer func() { i.inSend = false }()
	for range 5 {
		if refresh != nil {
			if err := refresh(ctx); err != nil {
				return err
			}
		}
		if i.beforeCallback != nil {
			i.beforeCallback()
		}
		i.callbacks++
		if err := eligible(); err != nil {
			return err
		}
		i.keys++
	}
	if i.allowSend {
		i.sentAt = time.Now()
		return nil
	}
	return errInjected // Stop after exercising a complete simulated sequence.
}

type guardCapture struct {
	*controlledScene
	ageError   error
	target     platform.ControlTarget
	capturedAt time.Time
}

func (s *guardCapture) CheckControlAge() error         { return s.ageError }
func (s *guardCapture) Target() platform.ControlTarget { return s.target }
func (s *guardCapture) Next(ctx context.Context) (platform.Frame, error) {
	f, err := s.controlledScene.Next(ctx)
	s.capturedAt = f.ControlCapturedAt
	return f, err
}

func TestControlledReceiveCallbackGuards(t *testing.T) {
	for _, failure := range []string{"command", "context", "target", "invalid-target", "clock", "margin"} {
		t.Run(failure, func(t *testing.T) { testControlledCallbackGuard(t, failure) })
	}
}

func testControlledCallbackGuard(t *testing.T, failure string) {
	t.Helper()
	s := &controlledScene{p: prepareFixture(t)}
	capture := &guardCapture{controlledScene: s, target: s.Target()}
	i := &guardInput{sceneInput: sceneInput{s}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	commands := make(chan command, 1)
	i.beforeCallback = callbackFailure(failure, capture, commands, cancel)
	root := t.TempDir()
	deps := guardDependencies(s, root, i)
	deps.controlCapture = func(uint32) (captureStream, error) { return capture, nil }
	err := runControlledReceive(ctx, root, command{window: 1, destination: t.TempDir()}, commands, func(snapshot) {}, deps)
	if err == nil || !strings.Contains(err.Error(), "control.send: control.key_") || i.keys != 0 || i.callbacks != 1 || i.checks != 3 {
		t.Fatalf("err=%v keys=%d callbacks=%d checks=%d", err, i.keys, i.callbacks, i.checks)
	}
}

func callbackFailure(failure string, capture *guardCapture, commands chan command, cancel context.CancelFunc) func() {
	return func() {
		switch failure {
		case "command":
			commands <- command{kind: "pause"}
		case "context":
			cancel()
		case "target":
			capture.target.Started++
		case "invalid-target":
			capture.target = platform.ControlTarget{}
		case "clock":
			capture.ageError = errInjected
		case "margin":
			// Still fresh without lookahead; the callback must reserve the margin.
			time.Sleep(time.Until(capture.capturedAt.Add(control.Freshness - platform.InputGuardMargin/2)))
		}
	}
}

func TestControlledReceiveRetryFullCheck(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	i := &guardInput{sceneInput: sceneInput{s}, allowSend: true}
	root := t.TempDir()
	deps := guardDependencies(s, root, i)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	emit := func(snapshot) {
		if !i.sentAt.IsZero() && time.Since(i.sentAt) > control.EchoTimeout+control.FirstBackoff {
			i.failCheck = i.checks + 1 // Planning is done; due Retry must check again.
		}
	}
	err := runControlledReceive(ctx, root, command{window: 1, destination: t.TempDir()}, nil, emit, deps)
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "control.input_check:") || i.callbacks < 5 || i.failCheck == 0 || i.checks != i.failCheck {
		t.Fatalf("err=%v callbacks=%d checks=%d", err, i.callbacks, i.checks)
	}
}

func guardDependencies(s *controlledScene, root string, input *guardInput) engineIO {
	deps := sceneDependencies(s, root)
	deps.bindInput = func(uint32) (platform.InputBinding, error) { return input, nil }
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, _, err := codec.DecodeControlled(img)
		return b, nil, err // Force a durable missing-plan command, even for a small fixture.
	}
	return deps
}

func TestControlledReceiveCallbackOmitsObservation(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	i := &guardInput{sceneInput: sceneInput{s}}
	root := t.TempDir()
	err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, guardDependencies(s, root, i))
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "control.send:") || i.checks != 3 || i.callbacks != 5 {
		t.Fatalf("err=%v observations=%d callbacks=%d", err, i.checks, i.callbacks)
	}
}

func TestControlledReceiveFullCheckpoints(t *testing.T) {
	for index, phase := range []string{"loop", "post-persistence", "Begin"} {
		t.Run(phase, func(t *testing.T) {
			s := &controlledScene{p: prepareFixture(t)}
			i := &guardInput{sceneInput: sceneInput{s}, failCheck: index + 1}
			root := t.TempDir()
			err := runControlledReceive(t.Context(), root, command{window: 1, destination: t.TempDir()}, nil, func(snapshot) {}, guardDependencies(s, root, i))
			if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "control.input_check:") || i.checks != index+1 || i.callbacks != 0 {
				t.Fatalf("err=%v observations=%d callbacks=%d", err, i.checks, i.callbacks)
			}
		})
	}
}
