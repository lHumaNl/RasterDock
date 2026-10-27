package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/application"
	"rasterdock/internal/domain"
)

func planFixture(t *testing.T) (*Receiver, *application.Prepared) {
	t.Helper()
	src, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	for i := range 10 {
		var data []byte
		for block := 0; i > 0 && block < 6; block++ {
			h := sha256.Sum256(fmt.Appendf(nil, "%d/%d", i, block))
			data = append(data, h[:]...)
		}
		planMust(t, os.WriteFile(filepath.Join(src, fmt.Sprintf("%02d", i)), data, 0600))
	}
	p, err := application.Prepare(context.Background(), src, state, 64, Flush)
	planMust(t, err)
	r, err := OpenReceive(filepath.Join(state, "rx"), dest, p.Descriptor, application.DecodeManifest, state)
	planMust(t, err)
	t.Cleanup(func() { planMust(t, r.Close()) })
	return r, p
}

func planMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func planAccept(t *testing.T, r *Receiver, p *application.Prepared, ordinal uint64) {
	t.Helper()
	packet, err := p.Packet(ordinal)
	planMust(t, err)
	planMust(t, r.Accept(packet))
}

func planRange(t *testing.T, r *Receiver, p *application.Prepared, start, end uint64) {
	t.Helper()
	for n := start; n < end; n++ {
		planAccept(t, r, p, n)
	}
}

func planWant(t *testing.T, r *Receiver, want uint64, missing bool, wantErr error) {
	t.Helper()
	got, more, err := r.NextMissing()
	if got != want || more != missing || !errors.Is(err, wantErr) {
		t.Fatalf("NextMissing() = (%d, %t, %v), want (%d, %t, %v)", got, more, err, want, missing, wantErr)
	}
}

func planReopen(t *testing.T, r *Receiver) *Receiver {
	t.Helper()
	planMust(t, r.Close())
	next, err := OpenReceive(r.dir, r.destination, r.descriptor, r.decode, filepath.Dir(r.dir))
	planMust(t, err)
	t.Cleanup(func() { planMust(t, next.Close()) })
	return next
}

func TestNextMissingManifestPagesAndRestart(t *testing.T) {
	r, p := planFixture(t)
	if p.ManifestPackets() <= 8 {
		t.Fatal("fixture must span multiple eight-packet pages")
	}
	planWant(t, r, 0, true, nil)
	for n := p.ManifestPackets(); n > 1; n-- {
		if n-1 != 8 {
			planAccept(t, r, p, n-1)
		}
	}
	planWant(t, r, 0, true, nil)
	planAccept(t, r, p, 0)
	planWant(t, r, 8, true, nil)
	r = planReopen(t, r)
	planWant(t, r, 8, true, nil)
	planAccept(t, r, p, 8)
	planWant(t, r, p.ManifestPackets(), true, nil)
}

func TestNextMissingDataOrderDuplicatesAndEmptyFiles(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.ManifestPackets())
	start := p.ManifestPackets()
	planWant(t, r, start, true, nil)
	planAccept(t, r, p, start+2)
	planAccept(t, r, p, start+2)
	planWant(t, r, start, true, nil)
	planAccept(t, r, p, start)
	planWant(t, r, start+1, true, nil)
	planAccept(t, r, p, start+1)
	planWant(t, r, start+p.Manifest.Files[1].Chunks, true, nil)
	planRange(t, r, p, start+3, p.PacketCount())
	planWant(t, r, 0, false, nil)
	count, _, err := r.PublishedFiles()
	planMust(t, err)
	if count != 0 {
		t.Fatal("query must not publish files")
	}
}

func TestNextMissingUnverifiedManifest(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.ManifestPackets())
	// Models a failed reconciliation after chunks and verification SQL committed.
	r.manifest = nil
	planWant(t, r, 0, false, ErrMetadata)
	if r.manifest != nil {
		t.Fatal("query must not retry verification")
	}
	planAccept(t, r, p, 0)
	planWant(t, r, p.ManifestPackets(), true, nil)
}

func TestNextMissingRecoveryDropsCorruptChunk(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.PacketCount())
	planMust(t, r.Close())
	planMust(t, os.WriteFile(r.payload(2), []byte("corrupt"), 0600))
	r = planReopen(t, r)
	planWant(t, r, p.ManifestPackets(), true, nil)
}

func TestNextMissingPublishedWithoutPayload(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.PacketCount())
	planMust(t, r.Publish())
	planMust(t, r.Close())
	planMust(t, os.Remove(r.payload(2)))
	r = planReopen(t, r)
	planWant(t, r, 0, false, nil)
	planMust(t, r.Close())
	planMust(t, os.Remove(filepath.Join(r.destination, p.Manifest.Files[1].Path)))
	r = planReopen(t, r)
	planWant(t, r, p.ManifestPackets(), true, nil)
}

func TestNextMissingStoppedAndDatabaseError(t *testing.T) {
	r, _ := planFixture(t)
	changed := r.descriptor
	changed.Hash[0] ^= 1
	if err := r.CheckDescriptor(changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	planWant(t, r, 0, false, ErrConflict)
	r.stopped = false
	planMust(t, r.Close())
	if _, _, err := r.NextMissing(); err == nil {
		t.Fatal("closed database error lost")
	}
}

func TestNextMissingHugeSparseAndOverflow(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.ManifestPackets())
	// Synthetic verified metadata tests arithmetic without allocating payload.
	r.manifest.Files = []domain.File{{ID: 1, Chunks: math.MaxInt64}}
	planWant(t, r, p.ManifestPackets(), true, nil)
	r.published[1] = true
	r.manifest.Files = append(r.manifest.Files, domain.File{ID: 2, Chunks: math.MaxUint64})
	planWant(t, r, 0, false, domain.ErrManifest)
}

func TestNextMissingOnlyEmptyFiles(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.ManifestPackets())
	r.manifest.Files = r.manifest.Files[:1]
	planWant(t, r, 0, false, nil)
	r.manifest.Files = nil
	planWant(t, r, 0, false, nil)
}

func TestNextMissingVerificationFailureDoesNotComplete(t *testing.T) {
	r, p := planFixture(t)
	decodeErr := errors.New("decode failed")
	r.decode = func(io.Reader) (domain.Manifest, error) { return domain.Manifest{}, decodeErr }
	planRange(t, r, p, 0, p.ManifestPackets()-1)
	packet, err := p.Packet(p.ManifestPackets() - 1)
	planMust(t, err)
	if err = r.Accept(packet); !errors.Is(err, decodeErr) {
		t.Fatal(err)
	}
	planWant(t, r, 0, false, ErrMetadata)
	r.decode = application.DecodeManifest
	planAccept(t, r, p, 0)
	planWant(t, r, p.ManifestPackets(), true, nil)
}

func TestNextMissingPayloadWithoutSQLIsMissing(t *testing.T) {
	r, p := planFixture(t)
	faultErr := errors.New("payload flushed before SQL")
	r.Fault = func(string) error { return faultErr }
	packet, err := p.Packet(0)
	planMust(t, err)
	if err = r.Accept(packet); !errors.Is(err, faultErr) {
		t.Fatal(err)
	}
	planWant(t, r, 0, true, nil)
	r = planReopen(t, r)
	planWant(t, r, 0, true, nil)
}

func TestNextMissingManifestLossBeforePublishedRecovery(t *testing.T) {
	r, p := planFixture(t)
	planRange(t, r, p, 0, p.PacketCount())
	planMust(t, r.Publish())
	planMust(t, r.Close())
	planMust(t, os.Remove(r.payload(0)))
	planMust(t, os.Remove(r.payload(2)))
	r = planReopen(t, r)
	planWant(t, r, 0, true, nil)
	planRange(t, r, p, 0, p.ManifestPackets())
	planWant(t, r, 0, false, nil)
}

func TestNextMissingConflictingDuplicateStopsPlan(t *testing.T) {
	r, p := planFixture(t)
	planAccept(t, r, p, 0)
	packet, err := p.Packet(0)
	planMust(t, err)
	packet.Payload[0] ^= 1
	if err = r.Accept(packet); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	planWant(t, r, 0, false, ErrConflict)
}

func TestNextMissingChunkScanError(t *testing.T) {
	r, _ := planFixture(t)
	_, err := r.db.Exec("INSERT INTO chunks(file,idx,length,hash,crc) VALUES(0,-1,1,x'00',0)")
	planMust(t, err)
	if _, _, err = r.NextMissing(); err == nil {
		t.Fatal("negative chunk index scan error lost")
	}
}
