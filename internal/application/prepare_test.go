package application

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
)

func TestPrepareReopenImmutable(t *testing.T) {
	src := t.TempDir()
	state := t.TempDir()
	want := bytes.Repeat([]byte{0, 255, 128, 42}, 70)
	if err := os.WriteFile(filepath.Join(src, "данные.bin"), want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(context.Background(), src, state, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPrepared(p.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Descriptor != p.Descriptor {
		t.Fatal("identity changed")
	}
	var actual []byte
	for n := reopened.ManifestPackets(); n < reopened.PacketCount(); n++ {
		q, e := reopened.Packet(n)
		if e != nil {
			t.Fatal(e)
		}
		actual = append(actual, q.Payload...)
	}
	if !bytes.Equal(actual, want) {
		t.Fatal("snapshot changed")
	}
}

func TestOpenPreparedRejectsCorruptPayload(t *testing.T) {
	src, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(context.Background(), src, state, 64, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(p.Directory, "1.bin"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenPrepared(p.Directory); err == nil {
		t.Fatal("corrupted snapshot reopened as ready")
	}
}
