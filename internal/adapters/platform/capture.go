// Package platform owns OS screen-capture resources. It never sends keyboard input.
package platform

import (
	"image"
	"time"
)

// Source is a window selected explicitly by the operator.
type Source struct {
	ID    uint32 `json:"id"`
	Title string `json:"title"`
}

// Frame owns copied RGBA pixels; CapturedAt is local receive time before decoding.
// It is not suitable for controlled input freshness in this initial adapter.
type Frame struct {
	Image      *image.RGBA
	CapturedAt time.Time
}
