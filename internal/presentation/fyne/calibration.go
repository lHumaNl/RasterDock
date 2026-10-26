package fyneui

import (
	"errors"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

// A torn page can briefly invalidate calibration. Three consecutive incompatible
// beacon-bearing captures stop reception with one actionable error, not log spam.
const calibrationFailureLimit = 3

type calibrationGuard struct{ failures int }

func (g *calibrationGuard) observe(err error) bool {
	if errors.Is(err, application.ErrProfile) {
		return true
	}
	if !errors.Is(err, codec.ErrCalibration) {
		g.failures = 0
		return false
	}
	g.failures++
	return g.failures >= calibrationFailureLimit
}
