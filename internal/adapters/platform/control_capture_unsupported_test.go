//go:build !darwin || !cgo

package platform

import (
	"errors"
	"testing"
)

func TestControlCaptureUnsupported(t *testing.T) {
	if !errors.Is(ControlCaptureCapability(), ErrControlCaptureUnsupported) {
		t.Fatal("advertised control capture")
	}
	stream, err := OpenControlCapture(1)
	if stream != nil || !errors.Is(err, ErrControlCaptureUnsupported) {
		t.Fatal(stream, err)
	}
}
