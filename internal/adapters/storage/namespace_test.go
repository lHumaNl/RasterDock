package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNamespaceRejectsEscapeBeforeCreation(t *testing.T) {
	anchor := t.TempDir()
	outside := filepath.Join(t.TempDir(), "not-created")
	err := initializeNamespace(outside, SyncDir, []string{anchor})
	if !errors.Is(err, ErrNamespace) {
		t.Fatalf("escape accepted: %v", err)
	}
	if _, err = os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path changed: %v", err)
	}
}

func TestNamespaceRequiresExistingAnchor(t *testing.T) {
	anchor := filepath.Join(t.TempDir(), "missing")
	if err := initializeNamespace(filepath.Join(anchor, "rx"), SyncDir, []string{anchor}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing anchor accepted: %v", err)
	}
}

func TestReceiveRejectsPayloadAsNamespaceAnchorBeforeMutation(t *testing.T) {
	session := t.TempDir()
	payload := filepath.Join(session, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReceive(session, t.TempDir(), directoryDescriptor(t), noManifestYet, payload)
	if r != nil {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if !errors.Is(err, ErrNamespace) {
		t.Errorf("anchor below session accepted: %v", err)
	}
	for _, name := range []string{"owner.lock", "state.sqlite"} {
		if _, statErr := os.Lstat(filepath.Join(session, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("session mutated outside anchor: %s: %v", name, statErr)
		}
	}
}
