package platform

import (
	"math"
	"testing"
	"time"
)

func TestCaptureTimestampBounds(t *testing.T) {
	received := time.Unix(100, 0)
	for _, tc := range []struct {
		name             string
		source, observed int64
		age              time.Duration
		valid            bool
	}{
		{"past-frame", 10_000_000, 20_000_000, time.Second, true},
		{"same-time", 10_000_000, 10_000_000, 0, true},
		{"observed-Windows-skew", 9743589878818, 9743589868526, 0, true},
		{"future-boundary", 10_250_000, 10_000_000, 0, true},
		{"future-outside-bound", 10_250_001, 10_000_000, 0, false},
		{"missing-time", 0, 10_000_000, 0, false},
		{"negative-time", -1, 10_000_000, 0, false},
		{"invalid-observation", 1, -1, 0, false},
		{"duration-overflow", 1, math.MaxInt64, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := captureTimestamp(tc.source, tc.observed, received)
			if tc.valid {
				if err != nil || !got.Equal(received.Add(-tc.age)) || got.After(received) {
					t.Fatalf("time=%v error=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid acquisition clock accepted")
			}
		})
	}
}
