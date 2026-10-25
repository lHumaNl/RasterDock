//go:build !windows || !cgo

package platform

import (
	"context"
	"sync/atomic"
)

type legacyStream struct {
	id     uint32
	closed atomic.Bool
}

// OpenCapture preserves the existing one-shot adapter on non-Windows systems.
func OpenCapture(id uint32) (Stream, error) { return &legacyStream{id: id}, nil }

func (s *legacyStream) Next(ctx context.Context) (Frame, error) {
	if s.closed.Load() {
		return Frame{}, ErrStreamClosed
	}
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	return Capture(s.id)
}

func (s *legacyStream) Close() error { s.closed.Store(true); return nil }
