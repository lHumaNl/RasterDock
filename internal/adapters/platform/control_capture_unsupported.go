//go:build !darwin || !cgo

package platform

// ControlCaptureCapability never advertises passive capture as control-safe.
func ControlCaptureCapability() error { return ErrControlCaptureUnsupported }

// OpenControlCapture has no screenshot or other passive fallback.
func OpenControlCapture(id uint32) (Stream, error) { return nil, ErrControlCaptureUnsupported }
