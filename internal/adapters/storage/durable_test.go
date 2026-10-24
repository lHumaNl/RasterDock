package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDurableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "payload")
	if err := WriteDurable(p, []byte("immutable")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p) //nolint:gosec // Test-owned temporary path.
	if err != nil || string(b) != "immutable" {
		t.Fatal(string(b), err)
	}
	if err := WriteDurable(p, []byte("replacement")); err == nil {
		t.Fatal("overwrote immutable payload")
	}
}
