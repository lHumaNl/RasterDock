package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

func directoryDescriptor(t *testing.T) v1.Descriptor {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return v1.Descriptor{Transfer: id, Block: 64, Length: 1}
}

func noManifestYet(io.Reader) (domain.Manifest, error) {
	return domain.Manifest{}, errors.New("no manifest expected during initialization")
}

func TestReceiveAnchorsWholeDirectoryHierarchy(t *testing.T) {
	base := t.TempDir()
	session := filepath.Join(base, "config", "state", "transfers", "transfer", "receive", "session")
	var synced []string
	r, err := openReceive(session, t.TempDir(), directoryDescriptor(t), noManifestYet, func(name string) error {
		synced = append(synced, name)
		return SyncDir(name)
	}, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	// Each entry is anchored by its parent, including the newly created config
	// directory and every remaining ancestor to the filesystem root on Unix.
	// Windows stops at the explicit caller-established base, inclusive.
	for name := filepath.Join(session, "payload"); ; name = filepath.Dir(name) {
		if !slices.Contains(synced, name) {
			t.Fatalf("directory was never synced: %s; got %v", name, synced)
		}
		if filepath.Dir(name) == name || (runtime.GOOS == "windows" && name == base) {
			break
		}
	}
	previous := -1
	for name := filepath.Join(session, "payload"); ; name = filepath.Dir(name) {
		index := slices.Index(synced, name)
		if index <= previous {
			t.Fatalf("not leaf-to-root: %v", synced)
		}
		previous = index
		if filepath.Dir(name) == name || (runtime.GOOS == "windows" && name == base) {
			break
		}
	}
}

func TestReceiveAncestorSyncFailureBlocksInitializationAndRetryResyncs(t *testing.T) {
	base := t.TempDir()
	session := filepath.Join(base, "config", "state", "transfers", "transfer", "receive", "session")
	descriptor := directoryDescriptor(t)
	destination := t.TempDir()
	failed := filepath.Join(base, "config")
	injected := errors.New("ancestor sync failed")
	r, err := openReceive(session, destination, descriptor, noManifestYet, func(name string) error {
		if name == failed {
			return injected
		}
		return SyncDir(name)
	}, base)
	if r != nil {
		_ = r.Close()
		t.Fatal("receiver returned despite ancestor durability failure")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("ancestor sync error ignored: %v", err)
	}
	if _, err = os.Stat(filepath.Join(session, "state.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata created before path durability: %v", err)
	}
	// MkdirAll has already created the directories. Retry must not infer their
	// durability merely from existence after an earlier unsuccessful attempt.
	called := false
	r, err = openReceive(session, destination, descriptor, noManifestYet, func(name string) error {
		if name == failed {
			called = true
		}
		return SyncDir(name)
	}, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("retry skipped previously created but unconfirmed ancestor")
	}
}
