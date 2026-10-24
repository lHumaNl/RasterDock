package platform

/*
#cgo CFLAGS: -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework ScreenCaptureKit -framework Cocoa -framework CoreGraphics
#include <stdlib.h>
char* rd_sources(char** error);
unsigned char* rd_capture(unsigned int id, int* width, int* height, char** error);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"image"
	"time"
	"unsafe"
)

// Sources asks ScreenCaptureKit for shareable windows; the OS may request permission.
func Sources() ([]Source, error) {
	var message *C.char
	p := C.rd_sources(&message)
	if message != nil {
		defer C.free(unsafe.Pointer(message))
		return nil, errors.New(C.GoString(message))
	}
	if p == nil {
		return nil, errors.New("ScreenCaptureKit: permission denied or no shareable content")
	}
	defer C.free(unsafe.Pointer(p))
	var sources []Source
	err := json.Unmarshal([]byte(C.GoString(p)), &sources)
	return sources, err
}

// Capture obtains one immutable window image directly from ScreenCaptureKit.
func Capture(id uint32) (Frame, error) {
	var w, h C.int
	var message *C.char
	p := C.rd_capture(C.uint(id), &w, &h, &message)
	at := time.Now()
	if message != nil {
		defer C.free(unsafe.Pointer(message))
		return Frame{}, errors.New(C.GoString(message))
	}
	if p == nil {
		return Frame{}, errors.New("ScreenCaptureKit: empty image")
	}
	defer C.free(unsafe.Pointer(p))
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > 64<<20 {
		return Frame{}, errors.New("capture exceeds pixel budget")
	}
	pixels := C.GoBytes(unsafe.Pointer(p), w*h*4)
	return Frame{Image: &image.RGBA{Pix: pixels, Stride: int(w) * 4, Rect: image.Rect(0, 0, int(w), int(h))}, CapturedAt: at}, nil
}
