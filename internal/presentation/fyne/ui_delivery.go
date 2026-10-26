package fyneui

import (
	"context"
	"time"
)

const dimensionsRefreshInterval = 200 * time.Millisecond

// Callbacks must finish synchronously: at most one UI operation is in flight,
// leaving backpressure in the engine's capacity-one latest-value mailbox.
// Cancellation waits for an in-flight callback, then leaves pending events alone.
func deliverSnapshots(ctx context.Context, events <-chan snapshot, ticks <-chan time.Time, apply func(snapshot), refresh func()) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok || ctx.Err() != nil {
				return
			}
			apply(event)
		case _, ok := <-ticks:
			if !ok {
				ticks = nil
			} else if ctx.Err() == nil {
				refresh()
			}
		}
	}
}
