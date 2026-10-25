package fyneui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
)

func TestEnginePrepareCreatesStateWithinConfiguredRoot(t *testing.T) {
	h := newHarness(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "source"), []byte("first preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	h.send(command{kind: "prepare", source: source})
	got := h.expect("status.frame")
	if got.role != "sender" || got.prepared == "" || got.picture == nil {
		t.Fatalf("first preparation failed: %+v", got)
	}
}

func TestAnchoredReceiveCreatesManagedSessionAndRejectsEscape(t *testing.T) {
	prepared := prepareFixture(t)
	root := t.TempDir()
	open := anchoredReceive(root)
	session := filepath.Join(root, "state", "new-session")
	r, err := open(session, t.TempDir(), prepared.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-session")
	r, err = open(outside, t.TempDir(), prepared.Descriptor)
	if r != nil {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Error("owner returned outside configured root")
	}
	if !errors.Is(err, storage.ErrNamespace) {
		t.Fatalf("escape accepted: %v", err)
	}
	if _, err = os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path mutated: %v", err)
	}
}
