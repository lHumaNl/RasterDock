package application_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

func TestVisualTransferWithLossAndRestart(t *testing.T) {
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
	r, err := storage.OpenReceive(filepath.Join(state, "rx"), dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	feed := func(n uint64) {
		t.Helper()
		q, e := p.Packet(n)
		if e != nil {
			t.Fatal(e)
		}
		img, e := codec.Render(p.Descriptor, q, n, n, 1)
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
	for n := uint64(0); n < p.ManifestPackets(); n++ {
		feed(n)
	}
	// Every fifth data packet is deliberately missing in the first pass.
	for n := p.PacketCount(); n > p.ManifestPackets(); n-- {
		if (n-1-p.ManifestPackets())%5 != 0 {
			feed(n - 1)
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
	r, err = storage.OpenReceive(filepath.Join(state, "rx"), dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	for n := p.ManifestPackets(); n < p.PacketCount(); n++ {
		feed(n)
		feed(n)
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "binary")) //nolint:gosec // Test-owned temporary destination.
	if err != nil || string(got) != string(want) {
		t.Fatal("byte mismatch", err)
	}
	info, err := os.Stat(filepath.Join(dest, "empty"))
	if err != nil || info.Size() != 0 {
		t.Fatal("empty file", err)
	}
}
