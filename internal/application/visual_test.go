package application_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

func TestVisualTransferWithLossAndRestart(t *testing.T) {
	t.Run("monochrome", func(t *testing.T) { testVisualTransfer(t, application.Monochrome) })
	t.Run("four-color", func(t *testing.T) { testVisualTransfer(t, application.Colors4) })
	t.Run("eight-color", func(t *testing.T) { testVisualTransfer(t, application.Colors8) })
}

func testVisualTransfer(t *testing.T, profile application.ColorProfile) {
	src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	want := make([]byte, 4096)
	for i := range want {
		want[i] = byte(i % 256)
	}
	if err := os.WriteFile(filepath.Join(src, "binary"), want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), src, state, 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	r, err := storage.OpenReceive(filepath.Join(state, "rx"), dest, p.Descriptor, application.DecodeManifest, state)
	if err != nil {
		t.Fatal(err)
	}
	feed := func(n uint64) {
		t.Helper()
		q, e := p.Page(n, profile)
		if e != nil {
			t.Fatal(e)
		}
		img, e := codec.RenderPage(p.Descriptor, q, profile, n, n, 1)
		if e != nil {
			t.Fatal(e)
		}
		b, packets, e := codec.Decode(img)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.CheckDescriptor(b.Descriptor); e != nil {
			t.Fatal(e)
		}
		for _, packet := range packets {
			if e = r.Accept(packet); e != nil {
				t.Fatal(e)
			}
		}
	}
	for n := uint64(0); n < p.ManifestPackets(); n += profile.Capacity() {
		feed(n)
	}
	// Lose every third data page, then replay all pages after reopening storage.
	for n := p.ManifestPackets(); n < p.PacketCount(); n += profile.Capacity() {
		if ((n-p.ManifestPackets())/profile.Capacity())%3 != 2 {
			feed(n)
		}
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dest, "binary")); !os.IsNotExist(err) {
		t.Fatal("incomplete file published", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = storage.OpenReceive(filepath.Join(state, "rx"), dest, p.Descriptor, application.DecodeManifest, state)
	if err != nil {
		t.Fatal(err)
	}
	for n := p.ManifestPackets(); n < p.PacketCount(); n += profile.Capacity() {
		feed(n)
		feed(n)
	}
	before, err := r.DurableBytes()
	if err != nil {
		t.Fatal(err)
	}
	feed(p.PacketCount() - 2) // Eight-color tail with one padding plane.
	feed(p.PacketCount() - 1) // Color tails with one or two padding planes.
	after, err := r.DurableBytes()
	if err != nil || after != before {
		t.Fatal("padding/duplicate increased progress", err)
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "binary")) //nolint:gosec // Test-owned temporary destination.
	if err != nil || string(got) != string(want) || sha256.Sum256(got) != sha256.Sum256(want) {
		t.Fatal("byte mismatch", err)
	}
	info, err := os.Stat(filepath.Join(dest, "empty"))
	if err != nil || info.Size() != 0 {
		t.Fatal("empty file", err)
	}
}
