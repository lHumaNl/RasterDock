package fyneui

import (
	"bytes"
	"testing"
)

func TestGridBenchmarkPlan(t *testing.T) {
	order := gridBenchmarkOrder()
	for position := range 3 {
		seen := map[int]bool{}
		for _, row := range order {
			seen[row[position]] = true
		}
		if !seen[1] || !seen[4] || !seen[6] {
			t.Fatal("each grid must occupy every order position")
		}
	}
	for _, cells := range []int{1, 4, 6} {
		profile, err := gridBenchmarkProfile(cells)
		if err != nil || profile.Colors != 8 || profile.Geometry.Module != 4 || profile.Capacity() != map[int]uint64{1: 3, 4: 12, 6: 18}[cells] {
			t.Fatalf("grid %d: %+v %v", cells, profile, err)
		}
	}
	for _, cells := range []int{0, 2, 7, 1000000} {
		if _, err := gridBenchmarkProfile(cells); err == nil {
			t.Fatal("unsupported benchmark grid accepted")
		}
	}
}

func TestGridBenchmarkFixture(t *testing.T) {
	a, b := gridBenchmarkFixture(), gridBenchmarkFixture()
	if len(a) != 512*1024 || !bytes.Equal(a, b) {
		t.Fatal("grid benchmark requires deterministic 512 KiB input")
	}
	if !bytes.Equal(a[:colorBenchmarkBytes], colorBenchmarkFixture()) {
		t.Fatal("fixture seed changed between benchmark sizes")
	}
}
