package fyneui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/control"
	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

type plannedReceiveOwner interface {
	receiveOwner
	NextMissing() (uint64, bool, error)
}

const (
	controlDecodeFailureLimit    = 5
	controlInitialCaptureTimeout = 2 * time.Second
	controlCapturePollInterval   = 250 * time.Millisecond
)

// One owner serializes capture, persistence, planning and keyboard requests.
// No source Prepared object or sender packet identities enter this path.
func runControlledReceive(ctx context.Context, root string, cmd command, commands <-chan command, emit func(snapshot), io engineIO) (err error) {
	saved := false
	cleanupFailed := false
	closeOwner := func(closeErr error) {
		if closeErr != nil {
			cleanupFailed = true
			err = errors.Join(err, closeErr)
		}
	}
	var pinnedTarget platform.ControlTarget
	var pinnedDescriptor v1.Descriptor
	if cmd.resume != nil {
		pinnedTarget, pinnedDescriptor = cmd.resume.target, cmd.resume.descriptor
	}
	defer func() {
		if !saved && !cleanupFailed && errors.Is(err, platform.ErrInputFocusLost) && !errors.Is(err, platform.ErrInputProcessGone) && pinnedTarget.Valid() {
			cmd.resume = nil
			err = &receivePausedError{resume: &receiveResume{command: cmd, target: pinnedTarget, descriptor: pinnedDescriptor}, cause: err}
		}
	}()
	// This runs after all owner cleanup. A terminal-send or cleanup failure must
	// never misreport already verified/published files as an incomplete transfer.
	defer func() {
		if !saved {
			return
		}
		key := "control.complete"
		params := map[string]interface{}{}
		if err != nil {
			key = "control.complete_finish_unconfirmed"
			params["reason"] = err.Error()
		}
		emit(snapshot{key: key, params: params, session: cmd.session})
		err = nil
	}()
	if io.controlCapture == nil || io.controlDecode == nil || io.bindInput == nil {
		return errors.New("controlled capture/input capability unavailable")
	}
	if cmd.window == 0 || cmd.destination == "" {
		return errors.New("select capture window and destination")
	}
	info, err := os.Stat(cmd.destination)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("destination is not a directory")
	}
	emit(snapshot{key: "control.switch_target", picture: image.NewRGBA(image.Rect(0, 0, 1, 1))})
	if err = controlArmDelay(ctx, commands, io.controlDelay); err != nil {
		return err
	}
	stream, err := io.controlCapture(cmd.window)
	if err != nil {
		return err
	}
	defer func() { closeOwner(stream.Close()) }()
	input, err := io.bindInput(cmd.window)
	if err != nil {
		return err
	}
	defer func() { closeOwner(input.Close()) }()
	pinnedTarget = input.Target()
	if cmd.resume != nil && pinnedTarget != cmd.resume.target {
		return errors.New("resume input target identity changed")
	}
	refreshing, ok := input.(platform.RefreshingInputBinding)
	if !ok {
		return errors.New("input adapter cannot run bounded serialized control commands")
	}
	clock, ok := stream.(interface {
		CheckControlAge() error
		Target() platform.ControlTarget
	})
	if !ok {
		return errors.New("capture cannot validate control clock continuity")
	}
	checkCapture := func() error {
		if err := controlContinue(ctx, commands); err != nil {
			return err
		}
		if target := clock.Target(); !target.Valid() || target != input.Target() {
			return errors.New("capture and input target identities differ; select the source again")
		}
		return clock.CheckControlAge()
	}
	check := func() error {
		if err := controlContinue(ctx, commands); err != nil {
			return err
		}
		// Classify process/focus loss before capture visibility errors can mask it
		// (for example when the operator minimizes the selected client window).
		if err := input.Check(); err != nil {
			return fmt.Errorf("control.input_check: %w", err)
		}
		return checkCapture()
	}
	var receiver plannedReceiveOwner
	defer func() {
		if receiver != nil {
			closeOwner(receiver.Close())
		}
	}()
	gate := &control.Receiver{}
	defer gate.Stop()
	// Native input owns the live process/focus checks. Never capture, decode or
	// query window titles between key events.
	eligible := func() error {
		return controlContinue(ctx, commands)
	}
	refresh := func(refreshCtx context.Context) error {
		if err := refreshCtx.Err(); err != nil {
			return err
		}
		return eligible()
	}
	bound := false
	decodeFailures := 0
	initialDeadline := time.Now().Add(controlInitialCaptureTimeout)
	send := func(request control.Command) error {
		if err := refreshing.SendWithRefresh(ctx, request.Nonce, request.Ordinal, refresh, eligible); err != nil {
			return fmt.Errorf("control.send: %w", err)
		}
		return gate.Sent(time.Now())
	}
	for {
		if err = controlContinue(ctx, commands); err != nil {
			return err
		}
		if err = check(); err != nil {
			return err
		}
		if bound {
			if err = gate.Check(time.Now()); err != nil {
				return err
			}
			request, ready, retryErr := gate.Retry(time.Now(), check)
			if retryErr != nil {
				return retryErr
			}
			if ready {
				if err = send(request); err != nil {
					return err
				}
			}
		}
		capture, captureErr := nextControlledFrame(ctx, stream)
		if errors.Is(captureErr, platform.ErrFramePending) {
			if _, pending := gate.Pending(); !pending && !time.Now().Before(initialDeadline) {
				return errors.New("control initial capture deadline expired")
			}
			continue
		}
		if captureErr != nil {
			return captureErr
		}
		beacon, packets, decodeErr := io.controlDecode(capture.Image)
		if decodeErr != nil {
			if errors.Is(decodeErr, codec.ErrAmbiguousControl) {
				return decodeErr
			}
			decodeFailures++
			if decodeFailures >= controlDecodeFailureLimit {
				return fmt.Errorf("control consecutive decode failures: %w", decodeErr)
			}
			continue
		}
		decodeFailures = 0
		observation := control.Observation{Beacon: beacon, CapturedAt: capture.ControlCapturedAt, Generation: capture.Generation, AgeBounded: capture.ControlAgeBounded}
		if capture.Generation == 0 {
			return errors.New("capture generation unavailable for control")
		}
		if !bound {
			if cmd.resume != nil && cmd.resume.descriptor != (v1.Descriptor{}) && beacon.Descriptor != cmd.resume.descriptor {
				return errors.New("resume descriptor changed")
			}
			if err = gate.Bind(beacon.Descriptor, observation, time.Now()); err != nil {
				return err
			}
			bound = true
			pinnedDescriptor = beacon.Descriptor
			if cmd.session == "" {
				id, e := domain.NewID()
				if e != nil {
					return e
				}
				cmd.session = filepath.Join(root, "state", "transfers", domain.IDString(beacon.Transfer), "receive", domain.IDString(id))
			}
			owner, e := io.openReceive(cmd.session, cmd.destination, beacon.Descriptor)
			if e != nil {
				return e
			}
			receiver, ok = owner.(plannedReceiveOwner)
			if !ok {
				return errors.Join(errors.New("receiver missing durable planner"), owner.Close())
			}
		} else {
			if err = checkCapture(); err != nil {
				return err
			}
			if err = gate.Check(time.Now()); err != nil {
				return err
			}
			admission, observeErr := gate.ObserveFrame(observation, time.Now())
			if observeErr != nil {
				return observeErr
			}
			if !admission.Admitted {
				continue
			}
		}
		if err = receiver.CheckDescriptor(beacon.Descriptor); err != nil {
			return err
		}
		if err = persistControlledPackets(receiver, packets); err != nil {
			return err
		}
		ordinal, missing, planErr := receiver.NextMissing()
		if planErr != nil {
			return planErr
		}
		published, total, e := receiver.PublishedFiles()
		if e != nil {
			return e
		}
		bytes, e := receiver.DurableBytes()
		if e != nil {
			return e
		}
		emit(snapshot{key: "status.receiving", params: map[string]interface{}{"bytes": bytes, "path": cmd.session, "published": published, "total": total}, session: cmd.session})
		if !missing {
			if published != total {
				return errors.New("all packets present but publication incomplete")
			}
			saved = true
			return finishControlledReceive(ctx, input, gate, check, refresh, eligible)
		}
		if err = check(); err != nil {
			return err
		}
		if _, pending := gate.Pending(); pending {
			continue // Retry scheduling happens before capture, even for ignored frames.
		}
		// The plan was computed after every Accept/flush and Publish above.
		request, err := gate.Begin(ordinal, true, time.Now(), check)
		if err != nil {
			return err
		}
		if err = send(request); err != nil {
			return err
		}
	}
}

func finishControlledReceive(ctx context.Context, input platform.InputBinding, gate *control.Receiver, check control.Eligibility, refresh func(context.Context) error, eligible func() error) error {
	terminal, ok := input.(platform.FinishingInputBinding)
	if !ok {
		return errors.New("input adapter cannot send terminal completion")
	}
	nonce, err := gate.BeginFinish(true, time.Now(), check)
	if err != nil {
		return err
	}
	// Deliberately no Sent/Retry or terminal ACK loop. This is the last input
	// attempt even when native sending cannot confirm delivery.
	return terminal.SendFinishWithRefresh(ctx, nonce, refresh, eligible)
}

// Control capture implementations must honor context cancellation. The short
// lease keeps command revocation and gate expiry serviced by the same owner.
func nextControlledFrame(ctx context.Context, stream captureStream) (platform.Frame, error) {
	lease, cancel := context.WithTimeout(ctx, controlCapturePollInterval)
	defer cancel()
	frame, err := stream.Next(lease)
	if ctx.Err() != nil {
		return platform.Frame{}, ctx.Err()
	}
	if lease.Err() != nil {
		return platform.Frame{}, platform.ErrFramePending
	}
	return frame, err
}

func persistControlledPackets(receiver receiveOwner, packets []v1.Packet) error {
	for _, packet := range packets {
		if err := receiver.Accept(packet); err != nil && !errors.Is(err, storage.ErrMetadata) {
			return err
		}
	}
	if err := receiver.Publish(); err != nil && !errors.Is(err, storage.ErrMetadata) {
		return err
	}
	return nil
}

func controlArmDelay(ctx context.Context, commands <-chan command, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-commands:
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

func controlContinue(ctx context.Context, commands <-chan command) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-commands:
		// Any operator command revokes input. A new operation needs a fresh click.
		return context.Canceled
	default:
		return nil
	}
}
