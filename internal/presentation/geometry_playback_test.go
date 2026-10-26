package presentation

import (
	"testing"

	"rasterdock/internal/application"
)

func TestGeometryPlaybackEveryCapacity(t *testing.T) {
	for _, grid := range [][2]int{{1, 1}, {2, 2}, {3, 2}} {
		for _, color := range []application.ColorProfile{2, 4, 8} {
			profile := application.VisualProfile{Colors: color, Geometry: application.Geometry{Module: 4, Columns: grid[0], Rows: grid[1]}}
			for metadata := uint64(1); metadata <= 2*profile.Capacity()+1; metadata++ {
				assertVisualPlayback(t, profile, metadata, metadata+31*profile.Capacity())
				assertVisualPlayback(t, profile, metadata, metadata)
			}
		}
	}
}

func assertVisualPlayback(t *testing.T, profile application.VisualProfile, metadata, total uint64) {
	t.Helper()
	playback, err := NewVisualPlayback(metadata, total, profile)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[uint64]bool)
	capacity := profile.Capacity()
	next, since, repeated := uint64(0), 0, uint64(0)
	for range 3 * total {
		want := next
		if since == 10 && next >= metadata {
			want = repeated
			repeated = (repeated + capacity) % ((metadata + capacity - 1) / capacity * capacity)
			since = 0
		} else {
			next += capacity
			if next > metadata {
				since++
			}
			if next >= total {
				next = 0
				since = 0
			}
		}
		got := playback.Next()
		if got != want {
			t.Fatalf("profile=%+v metadata=%d got=%d want=%d", profile, metadata, got, want)
		}
		for offset := uint64(0); offset < capacity && got+offset < total; offset++ {
			seen[got+offset] = true
		}
	}
	if uint64(len(seen)) != total {
		t.Fatal("skipped packets")
	}
}
