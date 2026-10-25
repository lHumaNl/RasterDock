package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/domain"
)

func TestDiscoveryRejectsInvalidReceiveDatabase(t *testing.T) {
	for _, kind := range []string{"malformed", rejectionLinkKind, "oversized"} {
		t.Run(kind, func(t *testing.T) { testInvalidReceiveDatabase(t, kind) })
	}
}

func testInvalidReceiveDatabase(t *testing.T, kind string) {
	state := t.TempDir()
	transfer, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(state, "transfers", domain.IDString(transfer), "receive", domain.IDString(transfer))
	if err := os.MkdirAll(session, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session, "state.sqlite")
	var checkLink func()
	if kind == rejectionLinkKind {
		checkLink = rejectionLink(t, path, true)
	} else {
		createBadDatabase(t, path, kind)
	}
	got, err := storage.Discover(t.Context(), state)
	if err != nil || len(got.Receivers) != 0 || len(got.Issues) != 1 {
		t.Fatalf("discovery: %+v, %v", got, err)
	}
	if checkLink != nil {
		checkLink()
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 1 {
		t.Fatalf("scratch not removed: %v, %v", entries, err)
	}
}

func createBadDatabase(t *testing.T, path, kind string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("invalid sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if kind == "oversized" {
		if err := os.Truncate(path, (32<<20)+1); err != nil {
			t.Fatal(err)
		}
	}
}
