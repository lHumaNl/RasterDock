//go:build !windows

package storage_test

import (
	"os"
	"testing"
)

const rejectionLinkKind = "symlink"

const rejectionLinkMode = os.ModeSymlink

func createRejectionLink(t *testing.T, file, dir, path string, directory bool) {
	t.Helper()
	target := file
	if directory {
		target = dir
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
