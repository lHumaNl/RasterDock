//go:build darwin && cgo

package platform

/*
#cgo LDFLAGS: -framework CoreMedia -framework CoreVideo
#include <stdlib.h>
#include "control_capture_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	controlNextWait  = 250 * time.Millisecond
	controlOpenWait  = 5 * time.Second
	controlCloseWait = 250 * time.Millisecond
)

type controlCaptureStream struct {
	mu                      sync.Mutex
	handle                  unsafe.Pointer
	clock                   controlClock
	generation, lastDisplay uint64
	terminal                error
	closed                  atomic.Bool
	closeOnce               sync.Once
	closeErr                error
	target                  ControlTarget
}

// Target is copied from the selected native SCWindow, never a caller label.
// It remains immutable and readable after Close; validity still requires CheckControlAge.
func (s *controlCaptureStream) Target() ControlTarget { return s.target }

// ControlCaptureCapability checks OS support and existing permission without prompting.
func ControlCaptureCapability() error {
	switch C.rd_control_capability() {
	case 0:
		return nil
	case 2:
		return ErrControlCapturePermission
	case 3:
		return ErrCaptureBusy
	default:
		return ErrControlCaptureUnsupported
	}
}

// OpenControlCapture starts a dedicated SCStream; it never falls back to screenshots.
func OpenControlCapture(id uint32) (Stream, error) {
	if id == 0 {
		return nil, ErrInvalidSource
	}
	if err := ControlCaptureCapability(); err != nil {
		return nil, err
	}
	generation, err := nextControlGeneration()
	if err != nil {
		return nil, err
	}
	return openControlStream(id, generation)
}

func openControlStream(id uint32, generation uint64) (Stream, error) {
	s := &controlCaptureStream{generation: generation}
	if err := s.checkClockLocked(); err != nil {
		return nil, err
	}
	s.handle = C.rd_control_open(C.uint32_t(id))
	if s.handle == nil {
		return nil, ErrCaptureBusy
	}
	if err := s.awaitOpen(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := s.copyTarget(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *controlCaptureStream) copyTarget() error {
	var native C.RDControlTarget
	if C.rd_control_target(s.handle, &native) == 0 { //nolint:gocritic // dupSubExpr is emitted for cgo's generated pointer check.
		return ErrControlCaptureInvalid
	}
	defer C.free(unsafe.Pointer(native.title))
	target := ControlTarget{Window: uint32(native.window), PID: int(native.pid), Started: uint64(native.started), Title: C.GoString(native.title)}
	if !target.Valid() {
		return ErrControlCaptureInvalid
	}
	s.target = target
	return nil
}

func (s *controlCaptureStream) awaitOpen() error {
	return waitControl(context.Background(), controlOpenWait, ErrFramePending, func() error {
		state := C.rd_control_state(s.handle)
		if state == C.RDControlReady {
			return s.CheckControlAge()
		}
		return controlNativeError(state)
	})
}

func controlNativeError(state C.int) error {
	switch state {
	case C.RDControlPending:
		return ErrFramePending
	case C.RDControlReady:
		return nil
	case C.RDControlResized:
		return ErrSourceResized
	case C.RDControlClosed:
		return ErrStreamClosed
	default:
		return ErrControlCaptureInvalid
	}
}

func (s *controlCaptureStream) failLocked(err error) error {
	if s.terminal == nil {
		s.terminal = err
	}
	if s.handle != nil {
		C.rd_control_revoke(s.handle)
	}
	return s.terminal
}

func (s *controlCaptureStream) checkClockLocked() error {
	ticks := C.rd_control_ticks()
	return s.clock.check(uint64(ticks.absolute0), uint64(ticks.continuous), uint64(ticks.absolute1), uint64(ticks.numerator), uint64(ticks.denominator))
}

// CheckControlAge must be called immediately before EVERY control key, including
// when reusing an older decoded frame. Go's monotonic clock alone misses sleep.
// Failure irrevocably invalidates all frames from this stream generation.
func (s *controlCaptureStream) CheckControlAge() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		return err
	}
	if err := s.checkTargetLocked(); err != nil {
		return s.failLocked(err)
	}
	return s.checkLocked()
}

func (s *controlCaptureStream) checkTargetLocked() error {
	C.rd_control_validate(s.handle)
	// WindowServer metadata calls have no native timeout. Queue them on the
	// retained owner, bound only our wait, and permanently revoke on timeout.
	return waitControl(context.Background(), controlNextWait, ErrControlCaptureInvalid, func() error {
		return controlNativeError(C.rd_control_validation_state(s.handle))
	})
}

func (s *controlCaptureStream) checkLocked() error {
	if s.closed.Load() {
		return ErrStreamClosed
	}
	if s.terminal != nil {
		return s.terminal
	}
	if err := s.checkClockLocked(); err != nil {
		return s.failLocked(err)
	}
	if err := controlNativeError(C.rd_control_state(s.handle)); err != nil && !errors.Is(err, ErrFramePending) {
		return s.failLocked(err)
	}
	return nil
}

func (s *controlCaptureStream) Next(ctx context.Context) (Frame, error) {
	if ctx == nil {
		return Frame{}, errors.New("capture: nil context")
	}
	var frame Frame
	err := waitControl(ctx, controlNextWait, ErrFramePending, func() error {
		var err error
		frame, err = s.take()
		return err
	})
	if err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func (s *controlCaptureStream) take() (Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		return Frame{}, err
	}
	var native C.RDControlFrame
	state := C.rd_control_take(s.handle, &native) //nolint:gocritic // dupSubExpr is emitted for cgo's generated pointer check.
	if err := controlNativeError(state); err != nil {
		return Frame{}, err
	}
	defer C.free(unsafe.Pointer(native.pixels))
	return s.checkedFrameLocked(native)
}

func (s *controlCaptureStream) checkedFrameLocked(native C.RDControlFrame) (Frame, error) {
	frame, err := s.convertLocked(native)
	if err != nil {
		return Frame{}, s.failLocked(err)
	}
	if err := s.checkLocked(); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

func (s *controlCaptureStream) convertLocked(native C.RDControlFrame) (Frame, error) {
	at, err := s.timestampLocked(uint64(native.display))
	if err != nil {
		return Frame{}, err
	}
	w, h := int(native.width), int(native.height)
	if native.pixels == nil || !validDimensions(w, h) {
		return Frame{}, ErrControlCaptureInvalid
	}
	pixels := C.GoBytes(unsafe.Pointer(native.pixels), C.int(w*h*bytesPerPixel))
	return Frame{Image: &image.RGBA{Pix: pixels, Stride: w * bytesPerPixel, Rect: image.Rect(0, 0, w, h)}, CapturedAt: at, ControlCapturedAt: at, ControlAgeBounded: true, Generation: s.generation}, nil
}

func (s *controlCaptureStream) timestampLocked(display uint64) (time.Time, error) {
	g0 := time.Now()
	ticks := C.rd_control_ticks()
	g1 := time.Now()
	if err := s.clock.check(uint64(ticks.absolute0), uint64(ticks.continuous), uint64(ticks.absolute1), uint64(ticks.numerator), uint64(ticks.denominator)); err != nil {
		return time.Time{}, err
	}
	if display <= s.lastDisplay {
		return time.Time{}, ErrControlCaptureInvalid
	}
	at, err := controlTimestamp(g0, g1, display, uint64(ticks.absolute1), uint64(ticks.numerator), uint64(ticks.denominator))
	s.lastDisplay = display
	return at, err
}

// Close revokes synchronously. Native ownership persists after a timeout, with a
// process-wide busy latch until the asynchronous stop and queue barrier finish.
func (s *controlCaptureStream) Close() error {
	s.closed.Store(true)
	s.closeOnce.Do(func() { s.closeErr = s.closeNative() })
	return s.closeErr
}

func (s *controlCaptureStream) closeNative() error {
	s.mu.Lock()
	handle := s.handle
	s.handle = nil
	C.rd_control_revoke(handle)
	s.mu.Unlock()
	defer C.rd_control_release(handle)
	return waitControl(context.Background(), controlCloseWait, ErrCloseTimeout, func() error {
		if C.rd_control_done(handle) != 0 {
			return nil
		}
		return ErrFramePending
	})
}
