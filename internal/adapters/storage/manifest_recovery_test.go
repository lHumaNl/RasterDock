package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

func loseManifestAndPayload(t *testing.T, session string) {
	t.Helper()
	for _, name := range []string{"0.bin", "1.bin"} {
		if err := os.Remove(filepath.Join(session, "payload", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func retransmitManifest(t *testing.T, r *storage.Receiver, p *application.Prepared) {
	t.Helper()
	for n := uint64(0); n < p.ManifestPackets(); n++ {
		packet, err := p.Packet(n)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Accept(packet); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestRestorationReconcilesPublications(t *testing.T) {
	for _, stage := range []string{"publication-flushed", "publication-committed"} {
		t.Run(stage, func(t *testing.T) {
			p, session, dest := publicationFixture(t)
			faultPublication(t, p, session, dest, stage)
			loseManifestAndPayload(t, session)
			if stage == "publication-committed" {
				if err := os.Remove(filepath.Join(dest, "a")); err != nil {
					t.Fatal(err)
				}
				names, err := filepath.Glob(filepath.Join(dest, ".rasterdock-*.tmp"))
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range names {
					if err = os.Remove(name); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, filepath.Dir(session))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := r.Close(); e != nil {
					t.Error(e)
				}
			}()
			files, total, err := r.PublishedFiles()
			if err != nil || files != 0 || total != 0 {
				t.Fatal("progress exposed before manifest", files, total, err)
			}
			retransmitManifest(t, r, p)
			if err = r.Publish(); err != nil {
				t.Fatal(err)
			}
			files, total, err = r.PublishedFiles()
			want := uint64(0)
			if stage == "publication-flushed" {
				want = 1
			}
			if err != nil || files != want || total != 1 {
				t.Fatalf("publication not reconciled after manifest restoration: got %d/%d, want %d/1: %v", files, total, want, err)
			}
			if want == 1 {
				b, e := os.ReadFile(filepath.Join(dest, "a")) //nolint:gosec // Test-owned temporary destination.
				if e != nil || string(b) != "verified" {
					t.Fatal("verified temp not published", e)
				}
			} else if _, e := os.Stat(filepath.Join(dest, "a")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("missing payload published", e)
			}
		})
	}
}

func TestManifestRestorationRetriesReconciliationFailure(t *testing.T) {
	p, session, dest := publicationFixture(t)
	faultPublication(t, p, session, dest, "publication-flushed")
	loseManifestAndPayload(t, session)
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, filepath.Dir(session))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := r.Close(); e != nil {
			t.Error(e)
		}
	}()
	injected := errors.New("directory sync boundary")
	r.Fault = func(stage string) error {
		if stage == "publication-directory" {
			return injected
		}
		return nil
	}
	for n := uint64(0); n < p.ManifestPackets(); n++ {
		packet, e := p.Packet(n)
		if e != nil {
			t.Fatal(e)
		}
		e = r.Accept(packet)
		if n+1 == p.ManifestPackets() {
			if !errors.Is(e, injected) {
				t.Fatalf("recovery failure not propagated: %v", e)
			}
		} else if e != nil {
			t.Fatal(e)
		}
	}
	files, total, err := r.PublishedFiles()
	if err != nil || files != 0 || total != 0 {
		t.Fatal("failed recovery exposed ready manifest", files, total, err)
	}
	r.Fault = nil
	last, err := p.Packet(p.ManifestPackets() - 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Accept(last); err != nil {
		t.Fatal(err)
	}
	files, total, err = r.PublishedFiles()
	if err != nil || files != 1 || total != 1 {
		t.Fatal("duplicate manifest did not retry recovery", files, total, err)
	}
}
