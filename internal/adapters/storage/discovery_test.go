package storage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
)

func TestDiscoveryExistingLiveAndClosedState(t *testing.T) {
	state := t.TempDir()
	p, err := application.Prepare(t.Context(), t.TempDir(), state, 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(state, "transfers", domain.IDString(p.Descriptor.Transfer), "receive", domain.IDString(id))
	dest := t.TempDir()
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	assertDiscovered(t, state, p.Directory, session, dest)
	if other, e := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, state); e == nil {
		_ = other.Close()
		t.Fatal("discovery stole ownership")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertDiscovered(t, state, p.Directory, session, dest)
}

func assertDiscovered(t *testing.T, state, sender, session, dest string) {
	t.Helper()
	before := sessionFiles(t, session)
	got, err := storage.Discover(t.Context(), state)
	if !reflect.DeepEqual(before, sessionFiles(t, session)) {
		t.Fatal("discovery changed original session files")
	}
	if err != nil || len(got.Issues) != 0 {
		t.Fatalf("discovery: %+v, %v", got, err)
	}
	if len(got.Senders) != 1 || got.Senders[0].Directory != sender {
		t.Fatalf("senders: %+v", got.Senders)
	}
	if len(got.Receivers) != 1 || got.Receivers[0].Directory != session || got.Receivers[0].Destination != dest {
		t.Fatalf("receivers: %+v", got.Receivers)
	}
}

func sessionFiles(t *testing.T, session string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(session)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(session, entry.Name())) //nolint:gosec // Test-owned session directory, never real user state.
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = sha256.Sum256(data)
	}
	return result
}

func TestDiscoveryCancellationAndEntryLimit(t *testing.T) {
	state := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := storage.Discover(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	transfers := filepath.Join(state, "transfers")
	if err := os.Mkdir(transfers, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 513; i++ {
		if err := os.Mkdir(filepath.Join(transfers, fmt.Sprint(i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.Discover(t.Context(), state); err == nil {
		t.Fatal("unbounded inventory")
	}
}

func TestDiscoveryRejectsMalformedAndLinkDescriptor(t *testing.T) {
	for _, kind := range []string{"malformed", rejectionLinkKind, "directory"} {
		t.Run(kind, func(t *testing.T) { testBadDescriptor(t, kind) })
	}
}

func testBadDescriptor(t *testing.T, kind string) {
	state := t.TempDir()
	p, err := application.Prepare(t.Context(), t.TempDir(), state, 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(p.Directory, "ready.json")
	if err := os.Remove(ready); err != nil {
		t.Fatal(err)
	}
	var checkLink func()
	switch kind {
	case "malformed":
		err = os.WriteFile(ready, []byte("broken"), 0600)
	case rejectionLinkKind:
		checkLink = rejectionLink(t, ready, false)
	case "directory":
		err = os.Mkdir(ready, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := storage.Discover(t.Context(), state)
	if err != nil || len(got.Senders) != 0 || len(got.Issues) != 1 {
		t.Fatalf("bad descriptor accepted: %+v, %v", got, err)
	}
	if checkLink != nil {
		checkLink()
	}
}

func TestDiscoveryMissingAndLinkRoot(t *testing.T) {
	state := t.TempDir()
	got, err := storage.Discover(t.Context(), state)
	if err != nil || len(got.Senders) != 0 {
		t.Fatalf("empty: %+v %v", got, err)
	}
	checkLink := rejectionLink(t, filepath.Join(state, "transfers"), true)
	if _, err := storage.Discover(t.Context(), state); err == nil {
		t.Fatalf("followed %s", rejectionLinkKind)
	}
	checkLink()
}

func TestDiscoveryRejectsLinkedStateRoot(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	checkLink := rejectionLink(t, state, true)
	if _, err := storage.Discover(t.Context(), state); err == nil {
		t.Fatalf("followed %s state root", rejectionLinkKind)
	}
	checkLink()
}

func TestDiscoveryRejectsInternalLinkedAncestor(t *testing.T) {
	state := t.TempDir()
	p, err := application.Prepare(t.Context(), t.TempDir(), state, 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	send := filepath.Dir(p.Directory)
	retained := filepath.Join(state, "retained-send")
	if err = os.Rename(send, retained); err != nil {
		t.Fatal(err)
	}
	// Keep the target within os.Root: containment alone must not replace the
	// inventory's stricter rejection of linked intermediate components.
	createRejectionLink(t, retained, retained, send, true)
	t.Cleanup(func() {
		if err := os.Remove(send); err != nil {
			t.Error(err)
		}
	})
	got, err := storage.Discover(t.Context(), state)
	if err != nil || len(got.Senders) != 0 || len(got.Issues) != 1 {
		t.Fatalf("linked ancestor accepted: %+v, %v", got, err)
	}
	if _, err = os.Stat(filepath.Join(retained, "prepared", "ready.json")); err != nil {
		t.Fatalf("retained descriptor changed: %v", err)
	}
}
