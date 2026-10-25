//go:build windows && cgo

package platform

/*
#cgo CFLAGS: -std=c11 -Wall -Wextra -Werror
#cgo LDFLAGS: -ld3d11 -ldxgi -lruntimeobject -ldwmapi -lole32 -luuid
#include <stdlib.h>
#include "wgc_windows.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var windowsTokens = newTokenRegistry()
var windowsWorkerGate = make(chan struct{}, 1)
var enumerationMu sync.Mutex

const firstFrameTimeout = 3 * time.Second
const firstFramePoll = 10 * time.Millisecond

type windowsCapture struct{ pointer *C.rd_capture }

// Sources enumerates metadata only; it never opens a capture session.
func Sources() ([]Source, error) {
	enumerationMu.Lock()
	defer enumerationMu.Unlock()
	var items *C.rd_source
	var count C.size_t
	if err := nativeError("enumerate windows", C.rd_sources(&items, &count)); err != nil {
		return nil, err
	}
	defer C.free(unsafe.Pointer(items))
	return registerSources(unsafe.Slice(items, int(count)))
}

func registerSources(items []C.rd_source) ([]Source, error) {
	sources := make([]Source, 0, len(items))
	seen := make(map[windowIdentity]bool, len(items))
	for _, item := range items {
		identity := windowIdentity{uintptr(item.hwnd), uint32(item.pid), uint32(item.tid)}
		id, err := windowsTokens.register(identity)
		if err != nil {
			return nil, err
		}
		seen[identity] = true
		title := syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&item.title[0])), C.RD_TITLE_CAPACITY))
		sources = append(sources, Source{ID: id, Title: title})
	}
	windowsTokens.retain(seen)
	return sources, nil
}

// OpenCapture returns promptly; all COM and driver work belongs to one OS thread.
func OpenCapture(id uint32) (Stream, error) {
	identity, ok := windowsTokens.lookup(id)
	if !ok {
		return nil, ErrInvalidSource
	}
	return startWorker(windowsWorkerGate, func() (nativeStream, error) { return openWindows(identity) }, func() { windowsTokens.invalidate(id) })
}

func openWindows(identity windowIdentity) (nativeStream, error) {
	var pointer *C.rd_capture
	hr := C.rd_open(C.uintptr_t(identity.handle), C.uint32_t(identity.pid), C.uint32_t(identity.tid), &pointer)
	return &windowsCapture{pointer}, nativeError("open WGC (requires supported Windows and SDR content)", hr)
}

func (s *windowsCapture) next() (Frame, error) {
	var raw C.rd_frame
	hr := C.rd_next(s.pointer, &raw)
	received := time.Now()
	defer C.free(unsafe.Pointer(raw.pixels))
	if hr == 1 {
		return Frame{}, ErrFramePending
	}
	if err := nativeError(C.GoString(C.rd_operation(s.pointer)), hr); err != nil {
		return Frame{}, fmt.Errorf("%w; source_time_100ns=%d observed_time_100ns=%d", err, int64(raw.source_time_100ns), int64(raw.observed_time_100ns))
	}
	at, err := captureTimestamp(int64(raw.source_time_100ns), int64(raw.observed_time_100ns), received)
	if err != nil {
		return Frame{}, err
	}
	return s.copyFrame(raw, at)
}

func (s *windowsCapture) copyFrame(raw C.rd_frame, at time.Time) (Frame, error) {
	if raw.pixels == nil || !validDimensions(int(raw.width), int(raw.height)) || raw.pitch != raw.width*bytesPerPixel {
		return Frame{}, errors.New("capture: invalid native pixel buffer")
	}
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(raw.pixels)), int(raw.pitch)*int(raw.height))
	im, err := copyBGRA(pixels, int(raw.width), int(raw.height), int(raw.pitch))
	if err != nil {
		return Frame{}, err
	}
	// Conversion may be expensive: revalidate before publishing owned pixels.
	if err = nativeError("validate captured source", C.rd_status(s.pointer)); err != nil {
		return Frame{}, err
	}
	return Frame{Image: im, CapturedAt: at}, err
}

func (s *windowsCapture) close() error { return nativeError("close WGC", C.rd_close(s.pointer)) }

func nativeError(operation string, hr C.int32_t) error {
	if hr >= 0 {
		return nil
	}
	if hr == C.RD_E_RESIZED {
		return fmt.Errorf("%s: %w (HRESULT 0x%08X)", operation, ErrSourceResized, uint32(hr))
	}
	return fmt.Errorf("capture: %s: %s (HRESULT 0x%08X)", operation, nativeReason(hr), uint32(hr))
}

func nativeReason(hr C.int32_t) string {
	switch hr {
	case C.RD_E_MINIMIZED:
		return "source minimized"
	case C.RD_E_RESIZED:
		return "source resized; select it again"
	case C.RD_E_CLOSED:
		return "capture item closed"
	case C.RD_E_REPLACED:
		return "source closed, replaced, or invalid"
	case C.RD_E_HIDDEN:
		return "source hidden or cloaked"
	default:
		return "native capture unavailable, unsupported, or device failure"
	}
}

// Capture is a bounded convenience for tests; GUI callers must keep a Stream.
func Capture(id uint32) (frame Frame, err error) {
	stream, err := OpenCapture(id)
	if err != nil {
		return Frame{}, err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), firstFrameTimeout)
	defer cancel()
	return waitFirstFrame(ctx, stream)
}

func waitFirstFrame(ctx context.Context, stream Stream) (Frame, error) {
	ticker := time.NewTicker(firstFramePoll)
	defer ticker.Stop()
	for {
		frame, err := stream.Next(ctx)
		if !errors.Is(err, ErrFramePending) {
			return frame, err
		}
		select {
		case <-ctx.Done():
			return Frame{}, fmt.Errorf("capture: first frame: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
