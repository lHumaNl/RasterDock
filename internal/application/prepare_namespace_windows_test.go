package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rasterdock/internal/adapters/storage"
)

func TestWindowsPrepareCreatesStateWithinExplicitAnchor(t *testing.T) {
	anchor := t.TempDir()
	p, err := Prepare(t.Context(), t.TempDir(), filepath.Join(anchor, "new", "state"), 64, storage.Flush, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenPrepared(p.Directory); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrepareRejectsNamespaceEscape(t *testing.T) {
	state := filepath.Join(t.TempDir(), "not-created")
	if _, err := Prepare(t.Context(), t.TempDir(), state, 64, storage.Flush, t.TempDir()); err == nil {
		t.Fatal("accepted state outside anchor")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("outside state changed: %v", err)
	}
}

func TestWindowsPrepareRequiresExistingDefaultAnchor(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	if _, err := Prepare(t.Context(), t.TempDir(), state, 64, storage.Flush); !os.IsNotExist(err) {
		t.Fatalf("inferred an unestablished anchor: %v", err)
	}
}

func TestWindowsPreparationBoundaryIgnoresCase(t *testing.T) {
	anchor := t.TempDir()
	if !atPreparationBoundary(anchor, strings.ToUpper(anchor)) {
		t.Fatal("case difference would flush unrelated ancestors")
	}
}
