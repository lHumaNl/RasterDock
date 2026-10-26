package application

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
)

func TestGeometryBounds(t *testing.T) {
	for _, module := range []int{4, 6, 8} {
		for _, grid := range []image.Point{{1, 1}, {2, 2}, {3, 2}} {
			p := VisualProfile{Colors: Colors8, Geometry: Geometry{Module: module, Columns: grid.X, Rows: grid.Y}}
			size, err := p.Size()
			if err != nil || size != image.Pt(244+grid.X*105*module, 40+grid.Y*105*module) || p.Capacity() != uint64(grid.X*grid.Y*3) { // #nosec G115 -- Test vectors contain at most six cells.
				t.Fatalf("%+v: %v %v", p, size, err)
			}
		}
	}
}

func TestGeometryRejectsBeforeArithmetic(t *testing.T) {
	for _, g := range []Geometry{{}, {4, 2, 1}, {2, 1, 1}, {8, 65535, 65535}, {int(^uint(0) >> 1), 3, 2}} {
		p := VisualProfile{Colors: Monochrome, Geometry: g}
		if _, err := p.Size(); err == nil || p.Capacity() != 0 {
			t.Fatalf("accepted %+v", g)
		}
	}
}

func TestGeometryPagePadding(t *testing.T) {
	p := pageFixture(t)
	profile := VisualProfile{Colors: Colors8, Geometry: Geometry{8, 3, 2}}
	for ordinal := uint64(0); ordinal < p.PacketCount(); ordinal++ {
		page, err := p.VisualPage(ordinal, profile)
		if err != nil || len(page) != 18 {
			t.Fatal(len(page), err)
		}
		assertEightPageIdentity(t, p, ordinal, page)
	}
}

func TestGeometryFullPageIdentity(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "payload"), make([]byte, 18*512), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), source, t.TempDir(), 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	profile := VisualProfile{Colors: Colors8, Geometry: Geometry{Module: 6, Columns: 3, Rows: 2}}
	page, err := p.VisualPage(0, profile)
	if err != nil || len(page) != 18 {
		t.Fatal(err)
	}
	assertEightPageIdentity(t, p, 0, page)
	for _, packet := range page {
		if packet.Kind == 0 {
			t.Fatal("full page padded prematurely")
		}
	}
}
