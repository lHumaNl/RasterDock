package platform

import (
	"context"
	"errors"
	"time"
)

const controlPollInterval = 5 * time.Millisecond

func waitControl(ctx context.Context, bound time.Duration, timeout error, poll func() error) error {
	timer, tick := time.NewTimer(bound), time.NewTicker(controlPollInterval)
	defer timer.Stop()
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := poll(); !errors.Is(err, ErrFramePending) {
			return controlPollResult(ctx, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return timeout
		case <-tick.C:
		}
	}
}

func controlPollResult(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	return err
}
