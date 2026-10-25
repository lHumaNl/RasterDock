package platform

import (
	"errors"
	"math"
	"time"
)

// WGC occasionally supplies a compositor timestamp slightly ahead of the QPC
// sampled during readback (1.03 ms observed on Windows 11). Bound that skew;
// use local receipt, never a future CapturedAt. This is passive capture timing,
// not a freshness proof for automated input or another security decision.
const maxFutureFrameSkew = 25 * time.Millisecond

func captureTimestamp(source, observed int64, received time.Time) (time.Time, error) {
	if source <= 0 || observed <= 0 {
		return time.Time{}, errors.New("capture: invalid acquisition clock")
	}
	age := observed - source // Positive inputs make subtraction safe.
	if age < -int64(maxFutureFrameSkew/100) || age > math.MaxInt64/100 {
		return time.Time{}, errors.New("capture: acquisition clock outside mapping bounds")
	}
	if age < 0 {
		age = 0
	}
	return received.Add(-time.Duration(age) * 100), nil
}
