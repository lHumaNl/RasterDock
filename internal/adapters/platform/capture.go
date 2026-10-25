// Package platform owns OS screen-capture resources. It never sends keyboard input.
package platform

import (
	"context"
	"errors"
	"image"
	"time"
)

// Source is a window selected explicitly by the operator.
type Source struct {
	ID    uint32 `json:"id"`
	Title string `json:"title"`
}

// Frame owns copied RGBA pixels. Windows timestamps acquisition using WGC QPC time;
// the legacy Darwin adapter timestamps local receipt.
type Frame struct {
	Image      *image.RGBA
	CapturedAt time.Time
}

// Stream owns a capture session. Call Close even after a terminal Next error.
type Stream interface {
	Next(context.Context) (Frame, error)
	Close() error
}

var (
	// ErrFramePending means no fresh frame is available within the bounded wait.
	ErrFramePending = errors.New("capture: no fresh frame yet")
	// ErrCaptureBusy prevents replacement workers while a previous one is active.
	ErrCaptureBusy = errors.New("capture: previous native worker is still active")
	// ErrCloseTimeout reports native teardown that outlives the caller's wait.
	ErrCloseTimeout = errors.New("capture: native worker teardown timed out")
	// ErrStreamClosed indicates a closed capture stream.
	ErrStreamClosed = errors.New("capture: stream closed")
	// ErrInvalidSource rejects unknown or retired source selection tokens.
	ErrInvalidSource = errors.New("capture: invalid or expired source token")
	// ErrSourceResized requires explicit reselection after a source size change.
	ErrSourceResized = errors.New("capture: source resized; select it again")
)
