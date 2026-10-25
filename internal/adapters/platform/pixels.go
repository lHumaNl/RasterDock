package platform

import (
	"errors"
	"image"
)

const (
	maxCaptureDimension = 16384
	maxCapturePixels    = 64 << 20
	bytesPerPixel       = 4
)

func validDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= maxCaptureDimension && h <= maxCaptureDimension && int64(w)*int64(h) <= maxCapturePixels
}

func copyBGRA(src []byte, w, h, pitch int) (*image.RGBA, error) {
	if !validDimensions(w, h) || pitch < w*bytesPerPixel || len(src) < w*bytesPerPixel {
		return nil, errors.New("capture: invalid pixel dimensions or row pitch")
	}
	// Divide rather than multiplying an untrusted pitch, which could overflow.
	if h-1 > (len(src)-w*bytesPerPixel)/pitch {
		return nil, errors.New("capture: invalid pixel dimensions or row pitch")
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			s, d := y*pitch+x*bytesPerPixel, y*out.Stride+x*bytesPerPixel
			copy(out.Pix[d:d+bytesPerPixel], []byte{src[s+2], src[s+1], src[s], src[s+3]})
		}
	}
	return out, nil
}
