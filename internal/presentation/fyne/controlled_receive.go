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

// One owner serializes capture, persistence, planning and keyboard requests.
// No source Prepared object or sender packet identities enter this path.
func runControlledReceive(ctx context.Context, root string, cmd command, commands <-chan command, emit func(snapshot), io engineIO) (err error) {
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
	defer func() { err = errors.Join(err, stream.Close()) }()
	input, err := io.bindInput(cmd.window)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	refreshing, ok := input.(platform.RefreshingInputBinding)
	if !ok {
		return errors.New("input adapter cannot refresh control observations between keys")
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
		if err := checkCapture(); err != nil {
			return err
		}
		if err := input.Check(); err != nil {
			return fmt.Errorf("control.input_check: %w", err)
		}
		return nil
	}
	var receiver plannedReceiveOwner
	defer func() {
		if receiver != nil {
			err = errors.Join(err, receiver.Close())
		}
	}()
	gate := &control.Receiver{}
	defer gate.Stop()
	bound := false
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
		}
		capture, captureErr := nextControlledFrame(ctx, stream)
		if errors.Is(captureErr, platform.ErrFramePending) {
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
			// A decode failure never renews the gate. Its original timestamp expires.
			if !bound {
				return fmt.Errorf("control bootstrap decode: %w", decodeErr)
			}
			continue
		}
		observation := control.Observation{Beacon: beacon, CapturedAt: capture.ControlCapturedAt, Generation: capture.Generation, AgeBounded: capture.ControlAgeBounded}
		if capture.Generation == 0 {
			return errors.New("capture generation unavailable for control")
		}
		if !bound {
			if err = gate.Bind(beacon.Descriptor, observation, time.Now()); err != nil {
				return err
			}
			bound = true
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
			if _, err = gate.Observe(observation, time.Now()); err != nil {
				return err
			}
		}
		if err = receiver.CheckDescriptor(beacon.Descriptor); err != nil {
			return err
		}
		if err = persistControlledPackets(receiver, packets); err != nil {
			return err
		}
		if err = check(); err != nil {
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
			emit(snapshot{key: "control.complete", session: cmd.session})
			return nil
		}
		var request control.Command
		ready := true
		if _, pending := gate.Pending(); pending {
			request, ready, err = gate.Retry(time.Now(), check)
		} else {
			// The plan was computed after every Accept/flush and Publish above.
			request, err = gate.Begin(ordinal, true, time.Now(), check)
		}
		if err != nil {
			return err
		}
		if ready {
			if err = refreshing.SendWithRefresh(ctx, request.Nonce, request.Ordinal, func(refreshCtx context.Context) error {
				if err := refreshControlledObservation(refreshCtx, gate, stream, io.controlDecode, checkCapture); err != nil {
					return fmt.Errorf("control.key_refresh: %w", err)
				}
				return nil
			}, func() error {
				if err := gate.Check(time.Now().Add(platform.InputGuardMargin)); err != nil {
					return fmt.Errorf("control.key_gate: %w", err)
				}
				if err := checkCapture(); err != nil {
					return fmt.Errorf("control.key_capture: %w", err)
				}
				return nil
			}); err != nil {
				return fmt.Errorf("control.send: %w", err)
			}
			if err = gate.Sent(time.Now()); err != nil {
				return err
			}
		}
	}
}

// Control capture implementations must honor context cancellation. The short
// lease keeps command revocation and gate expiry serviced by the same owner.
func nextControlledFrame(ctx context.Context, stream captureStream) (platform.Frame, error) {
	lease, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
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
