package platform

import (
	"errors"
	"math"
	"math/bits"
	"sync/atomic"
	"time"
)

const controlClockBracket = 5 * time.Millisecond

var (
	// ErrControlCaptureUnsupported excludes passive-only operating systems.
	ErrControlCaptureUnsupported = errors.New("capture: acquisition-bounded control capture unsupported")
	// ErrControlCapturePermission requires permission granted outside this API.
	ErrControlCapturePermission = errors.New("capture: screen recording permission required (no prompt issued)")
	// ErrControlClock irrevocably revokes the stream's timestamp provenance.
	ErrControlClock = errors.New("capture: control clock invalid or discontinuous; reopen capture")
	// ErrControlCaptureInvalid rejects incomplete or unverifiable native samples.
	ErrControlCaptureInvalid = errors.New("capture: control stream lost valid frame provenance")
	controlGeneration        atomic.Uint64
)

func nextControlGeneration() (uint64, error) {
	for {
		old := controlGeneration.Load()
		if old == math.MaxUint64 {
			return 0, ErrControlCaptureInvalid
		}
		if controlGeneration.CompareAndSwap(old, old+1) {
			return old + 1, nil
		}
	}
}

func controlTickAge(ticks, num, den uint64) (time.Duration, error) {
	if num == 0 || den == 0 {
		return 0, ErrControlClock
	}
	hi, lo := bits.Mul64(ticks, num)
	if hi >= den {
		return 0, ErrControlClock
	}
	q, r := bits.Div64(hi, lo, den)
	if q > math.MaxInt64 || (q == math.MaxInt64 && r != 0) {
		return 0, ErrControlClock
	}
	if r != 0 {
		q++
	}
	return time.Duration(q), nil // #nosec G115 -- q plus ceiling was checked against MaxInt64 above.
}

func controlTimestamp(g0, g1 time.Time, display, host, num, den uint64) (time.Time, error) {
	if g1.Sub(g0) < 0 || g1.Sub(g0) > controlClockBracket || display == 0 || display > host {
		return time.Time{}, ErrControlClock
	}
	age, err := controlTickAge(host-display, num, den)
	if err != nil || age > time.Duration(math.MaxInt64)-controlClockBracket {
		return time.Time{}, ErrControlClock
	}
	// An offset change smaller than the native sampling interval can be hidden.
	// Charge the entire allowed interval as age rather than making it fresh.
	return g0.Add(-age - controlClockBracket), nil
}

type controlClock struct {
	initialized, failed          bool
	low, high                    uint64
	lastAbsolute, lastContinuous uint64
	num, den                     uint64
}

func (c *controlClock) check(a0, continuous, a1, num, den uint64) error {
	low, high, err := controlOffset(a0, continuous, a1, num, den)
	if c.failed || err != nil || !c.ordered(a0, continuous, num, den) {
		c.failed = true
		return ErrControlClock
	}
	if c.initialized && (low > c.high || high < c.low) {
		c.failed = true
		return ErrControlClock
	}
	if !c.initialized {
		c.low, c.high, c.initialized = low, high, true
	}
	c.low, c.high = max(c.low, low), min(c.high, high)
	c.lastAbsolute, c.lastContinuous, c.num, c.den = a1, continuous, num, den
	return nil
}

func (c *controlClock) ordered(a0, continuous, num, den uint64) bool {
	return !c.initialized || (a0 >= c.lastAbsolute && continuous >= c.lastContinuous && num == c.num && den == c.den)
}

func controlOffset(a0, continuous, a1, num, den uint64) (uint64, uint64, error) {
	if a0 == 0 || a1 < a0 || continuous < a0 {
		return 0, 0, ErrControlClock
	}
	span, err := controlTickAge(a1-a0, num, den)
	if err != nil || span > controlClockBracket {
		return 0, 0, ErrControlClock
	}
	var low uint64
	if continuous > a1 {
		low = continuous - a1
	}
	return low, continuous - a0, nil
}
