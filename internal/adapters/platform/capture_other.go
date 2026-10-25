//go:build (!darwin && !windows) || (windows && !cgo)

package platform

import "errors"

// Sources reports unavailable adapters instead of pretending to support capture.
func Sources() ([]Source, error) {
	return nil, errors.New("native capture not implemented on this platform")
}

// Capture reports unavailable adapters; no external screenshot helper is used.
func Capture(_ uint32) (Frame, error) {
	return Frame{}, errors.New("native capture not implemented on this platform")
}
