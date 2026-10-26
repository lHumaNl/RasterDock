package presentation

import (
	"testing"

	"rasterdock/internal/application"
)

func TestColorPlaybackCountsPages(t *testing.T) {
	p, err := NewPagePlayback(3, 30, application.Colors4)
	if err != nil {
		t.Fatal(err)
	}
	// The second initial page crosses manifest/data; every ordinal is still shown.
	want := []uint64{0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, 0, 22, 24, 26, 28, 0, 2}
	for i, n := range want {
		if got := p.Next(); got != n {
			t.Fatalf("page %d: got %d want %d", i, got, n)
		}
	}
}

func TestPlaybackRejectsUnavailableProfile(t *testing.T) {
	if _, err := NewPagePlayback(1, 2, application.ColorProfile(16)); err == nil {
		t.Fatal("sixteen colors enabled")
	}
}

func TestPagePlaybackInitialManifestAndNoSkips(t *testing.T) {
	for metadata := uint64(1); metadata <= 5; metadata++ {
		for _, profile := range []application.ColorProfile{application.Monochrome, application.Colors4, application.Colors8} {
			assertPlaybackCoverage(t, metadata, metadata+25, profile)
			assertPlaybackCoverage(t, metadata, metadata, profile)
		}
	}
}

func assertPlaybackCoverage(t *testing.T, metadata, total uint64, profile application.ColorProfile) {
	t.Helper()
	p, err := NewPagePlayback(metadata, total, profile)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[uint64]bool)
	for page := uint64(0); page < 2*total; page++ {
		n := p.Next()
		if page*profile.Capacity() < metadata && n != page*profile.Capacity() {
			t.Fatal("initial manifest skipped")
		}
		for offset := uint64(0); offset < profile.Capacity() && n+offset < total; offset++ {
			seen[n+offset] = true
		}
	}
	if uint64(len(seen)) != total {
		t.Fatalf("profile %d metadata %d skipped packets", profile, metadata)
	}
}

func TestPlaybackRepeatsMetadata(t *testing.T) {
	p := NewPlayback(2, 100)
	if p.Next() != 0 || p.Next() != 1 {
		t.Fatal("metadata must lead")
	}
	for i := uint64(2); i < 12; i++ {
		if n := p.Next(); n != i {
			t.Fatal(n)
		}
	}
	if p.Next() != 0 {
		t.Fatal("metadata not repeated after ten data pages")
	}
	empty := NewPlayback(2, 2)
	for i := range 10 {
		if empty.Next() != uint64(i%2) {
			t.Fatal("empty transfer cycle")
		}
	}
}

func TestEightPlaybackMetadataRecurrence(t *testing.T) {
	for metadata := uint64(1); metadata <= 7; metadata++ {
		assertEightRecurrence(t, metadata)
	}
}

func assertEightRecurrence(t *testing.T, metadata uint64) {
	t.Helper()
	p, err := NewPagePlayback(metadata, 300, application.Colors8)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range eightRecurrence(metadata) {
		if got := p.Next(); got != want {
			t.Fatalf("metadata %d: got %d want %d", metadata, got, want)
		}
	}
}

// A boundary page is a data page too; inserted metadata never skips the data cursor.
func eightRecurrence(metadata uint64) []uint64 {
	var ordinals []uint64
	next, dataPages, repeated := uint64(0), 0, uint64(0)
	for range 80 {
		want := next
		if dataPages == 10 {
			want, repeated, dataPages = repeated, (repeated+3)%((metadata+2)/3*3), 0
		} else {
			next += 3
			if next > metadata {
				dataPages++
			}
		}
		ordinals = append(ordinals, want)
	}
	return ordinals
}
