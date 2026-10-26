package application

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rasterdock/internal/adapters/storage"
	v1 "rasterdock/internal/protocol/v1"
)

func pageFixture(t *testing.T) *Prepared {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "binary"), make([]byte, 1536), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), src, t.TempDir(), 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPageSwitchPreservesEveryPacket(t *testing.T) {
	p := pageFixture(t)
	descriptor := p.Descriptor
	for n := uint64(0); n < p.PacketCount(); n++ {
		mono, err := p.Page(n, Monochrome)
		if err != nil {
			t.Fatal(err)
		}
		color, err := p.Page(n, Colors4)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(mono[0], color[0]) || p.Descriptor != descriptor {
			t.Fatal("profile changed logical identity")
		}
	}
}

func TestPageEndPaddingAndInvalidInput(t *testing.T) {
	p := pageFixture(t)
	page, err := p.Page(p.PacketCount()-1, Colors4)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := page[1].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	padding, err := v1.ParsePacket(raw)
	if err != nil || padding.Kind != 0 || padding.Transfer != p.Descriptor.Transfer || padding.Count != 0 {
		t.Fatal("invalid padding", err)
	}
}

func TestPageInvalidInput(t *testing.T) {
	p := pageFixture(t)
	if _, err := p.Page(p.PacketCount(), Colors4); err == nil {
		t.Fatal("out of range")
	}
	if _, err := p.Page(0, ColorProfile(16)); err == nil {
		t.Fatal("sixteen colors enabled")
	}
}

func TestEightPageIdentityAndTails(t *testing.T) {
	p := pageFixture(t)
	descriptor := p.Descriptor
	if Colors8.Capacity() != 3 {
		t.Fatal("eight-color capacity")
	}
	for n := uint64(0); n < p.PacketCount(); n++ {
		page, err := p.Page(n, Colors8)
		if err != nil || len(page) != 3 {
			t.Fatal("eight-color page", err)
		}
		assertEightPageIdentity(t, p, n, page)
	}
	if p.Descriptor != descriptor {
		t.Fatal("profile changed descriptor")
	}
}

func assertEightPageIdentity(t *testing.T, p *Prepared, n uint64, page []v1.Packet) {
	t.Helper()
	for i, packet := range page {
		want := v1.Packet{Transfer: p.Descriptor.Transfer}
		if n+uint64(i) < p.PacketCount() {
			var err error
			want, err = p.Packet(n + uint64(i))
			if err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(packet, want) {
			t.Fatalf("ordinal %d plane %d changed identity/padding", n, i)
		}
	}
}

func TestProfileCapacityWhitelist(t *testing.T) {
	for n := 0; n <= 255; n++ {
		profile := ColorProfile(n) // Exhaust the byte-sized profile domain.
		want := map[ColorProfile]uint64{Monochrome: 1, Colors4: 2, Colors8: 3}[profile]
		if got := profile.Capacity(); got != want {
			t.Fatalf("profile %d: got %d want %d", n, got, want)
		}
	}
}
