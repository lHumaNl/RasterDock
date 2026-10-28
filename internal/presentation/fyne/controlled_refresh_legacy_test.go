package fyneui

// Legacy heartbeat-refresh fixture. Production event-driven input never invokes
// this helper; retained only to exercise historical capture rejection fixtures.

import (
	"context"
	"errors"
	"image"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/control"
	v1 "rasterdock/internal/protocol/v1"
)

const controlRefreshHeadroom = 600 * time.Millisecond

var errControlRefreshDeadline = errors.New("control heartbeat refresh deadline expired")

// Legacy helper retained for acquisition regression tests only. Production
// receiver key callbacks never call this helper or consume capture frames.
func refreshControlledObservation(ctx context.Context, gate *control.Receiver, stream captureStream, decode func(image.Image) (v1.Beacon, []v1.Packet, error), checkCapture func() error) (err error) {
	defer func() {
		if err != nil {
			gate.Stop()
		}
	}()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = checkCapture(); err != nil {
			return err
		}
		now := time.Now()
		if err = gate.Check(now); err != nil {
			return err
		}
		until := gate.FreshUntil()
		if until.Sub(now) > controlRefreshHeadroom {
			return nil
		}
		deadline := now.Add(250 * time.Millisecond)
		if limit := until.Add(-platform.InputGuardMargin); limit.Before(deadline) {
			deadline = limit
		}
		if !deadline.After(now) {
			return errControlRefreshDeadline
		}
		lease, cancel := context.WithDeadline(ctx, deadline)
		frame, captureErr := stream.Next(lease)
		leaseErr := lease.Err()
		cancel()
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = checkCapture(); err != nil {
			return err
		}
		if err = gate.Check(time.Now()); err != nil {
			return err
		}
		if leaseErr != nil || errors.Is(captureErr, platform.ErrFramePending) {
			continue
		}
		if captureErr != nil {
			return captureErr
		}
		beacon, _, decodeErr := decode(frame.Image)
		// Recheck the OLD observation after decoding. Never revive expired consent.
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = checkCapture(); err != nil {
			return err
		}
		if err = gate.Check(time.Now()); err != nil {
			return err
		}
		if errors.Is(decodeErr, codec.ErrAmbiguousControl) {
			return decodeErr
		}
		if decodeErr != nil {
			continue
		}
		_, err = gate.Observe(control.Observation{Beacon: beacon, CapturedAt: frame.ControlCapturedAt,
			Generation: frame.Generation, AgeBounded: frame.ControlAgeBounded}, time.Now())
		if err != nil {
			return err
		}
	}
}
