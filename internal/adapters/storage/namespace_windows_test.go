package storage

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWindowsNamespaceRequiresExplicitAnchor(t *testing.T) {
	r, err := OpenReceive(t.TempDir(), t.TempDir(), directoryDescriptor(t), noManifestYet)
	if r != nil || !errors.Is(err, ErrNamespace) {
		t.Fatalf("unanchored Windows initialization: %v", err)
	}
}

func TestWindowsNamespaceBoundsAndRetry(t *testing.T) {
	anchor := t.TempDir()
	leaf := filepath.Join(anchor, "new", "session", "payload")
	want := []string{leaf, filepath.Dir(leaf), filepath.Join(anchor, "new"), anchor}
	injected := errors.New("anchor flush failed")
	for _, fail := range []bool{true, false} {
		var got []string
		err := initializeNamespace(filepath.Dir(leaf), func(name string) error {
			got = append(got, name)
			if fail && name == anchor {
				return injected
			}
			return SyncDir(name)
		}, []string{anchor})
		if !slices.Equal(got, want) || (fail && !errors.Is(err, injected)) || (!fail && err != nil) {
			t.Fatalf("managed retry chain: %v, error %v", got, err)
		}
	}
}

func TestWindowsNamespaceCaseInsensitiveBoundary(t *testing.T) {
	base := t.TempDir()
	anchor := strings.ToUpper(base)
	var got []string
	err := initializeNamespace(filepath.Join(base, "child"), func(name string) error {
		got = append(got, name)
		return SyncDir(name)
	}, []string{anchor})
	if err != nil || !slices.Equal(got, []string{filepath.Join(anchor, "child", "payload"), filepath.Join(anchor, "child"), anchor}) {
		t.Fatalf("case-insensitive boundary escaped: %v %v", got, err)
	}
}
