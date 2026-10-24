package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

func TestReceiveResumeAndPublish(t *testing.T) {
	src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	want := make([]byte, 300)
	for i := range want {
		want[i] = byte(i % 256)
	}
	if err := os.WriteFile(filepath.Join(src, "file.bin"), want, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), src, state, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "receiver")
	r, err := storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest); e == nil {
		_ = other.Close()
		t.Fatal("two owners")
	}
	q, _ := p.Packet(p.ManifestPackets())
	if err = r.Accept(q); !errors.Is(err, storage.ErrMetadata) {
		t.Fatalf("data before metadata: %v", err)
	}
	for n := p.ManifestPackets(); n > 0; n-- {
		q, e := p.Packet(n - 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Accept(q); e != nil {
			t.Fatal(e)
		}
	}
	q, _ = p.Packet(p.ManifestPackets())
	if err = r.Accept(q); err != nil {
		t.Fatal(err)
	}
	if err = r.Accept(q); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}()
	for n := p.PacketCount(); n > p.ManifestPackets(); n-- {
		q, e := p.Packet(n - 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Accept(q); e != nil {
			t.Fatal(e)
		}
	}
	if err = r.Publish(); err != nil {
		t.Fatal(err)
	}
	files, total, err := r.PublishedFiles()
	if err != nil || files != 1 || total != 1 {
		t.Fatal(files, total, err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "file.bin")) //nolint:gosec // Test-owned temporary destination.
	if err != nil || string(b) != string(want) {
		t.Fatal("output mismatch", err)
	}
	count, err := r.DurableBytes()
	if err != nil || count != uint64(len(want)) {
		t.Fatal(count, err)
	}
}

func TestFaultBeforeMetadataCommit(t *testing.T) {
	src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), src, state, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "receiver")
	r, err := storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	r.Fault = func(stage string) error {
		if stage == "payload-flushed" {
			return errors.New("injected failure")
		}
		return nil
	}
	q, _ := p.Packet(0)
	if err = r.Accept(q); err == nil {
		t.Fatal("fault ignored")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}()
	if err = r.Accept(q); err != nil {
		t.Fatal(err)
	}
	changed := p.Descriptor
	changed.Hash[0] ^= 1
	if err = r.CheckDescriptor(changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatal(err)
	}
}

func TestPublicationRecoveryFaults(t *testing.T) {
	for _, stage := range []string{"publication-intent", "publication-flushed", "publication-linked", "publication-directory"} {
		t.Run(stage, func(t *testing.T) {
			src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(src, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "nested", "a"), []byte("verified"), 0600); err != nil {
				t.Fatal(err)
			}
			p, err := application.Prepare(context.Background(), src, state, 64, storage.Flush)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(state, "rx")
			r, err := storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
			if err != nil {
				t.Fatal(err)
			}
			for n := uint64(0); n < p.PacketCount(); n++ {
				q, e := p.Packet(n)
				if e != nil {
					t.Fatal(e)
				}
				if e = r.Accept(q); e != nil {
					t.Fatal(e)
				}
			}
			r.Fault = func(point string) error {
				if point == stage {
					return errors.New("simulated crash boundary")
				}
				return nil
			}
			if err = r.Publish(); err == nil {
				t.Fatal("fault ignored")
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			r, err = storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Publish(); err != nil {
				t.Fatal(err)
			}
			if err = r.Close(); err != nil {
				t.Fatal(err)
			}
			b, e := os.ReadFile(filepath.Join(dest, "nested", "a")) //nolint:gosec // Test-owned temporary destination.
			if e != nil || string(b) != "verified" {
				t.Fatal(string(b), e)
			}
			temps, e := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
			if e != nil {
				t.Fatal(e)
			}
			if len(temps) != 0 {
				t.Fatalf("recorded temporary files leaked after recovery: %v", temps)
			}
		})
	}
}

func TestReceiveCorruptionConflictAndDestinationSafety(t *testing.T) {
	src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("verified"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(context.Background(), src, state, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "rx")
	r, err := storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(0); n < p.PacketCount(); n++ {
		q, e := p.Packet(n)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Accept(q); e != nil {
			t.Fatal(e)
		}
	}
	q, err := p.Packet(p.ManifestPackets())
	if err != nil {
		t.Fatal(err)
	}
	q.Payload[0] ^= 1
	if err = r.Accept(q); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("duplicate conflict", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "payload", "1.bin"), []byte("damaged!"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = storage.OpenReceive(dir, dest, p.Descriptor, application.DecodeManifest)
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.DurableBytes()
	if err != nil || n != 0 {
		t.Fatal("corrupt chunk retained", n, err)
	}
	q, err = p.Packet(p.ManifestPackets())
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Accept(q); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dest, "a"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.Publish(); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("overwrote conflicting file", err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
}
