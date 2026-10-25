package storage_test

import (
	"os"
	"path/filepath"
	"testing"
)

// rejectionLink uses a real symlink on Unix and an unprivileged directory
// junction on Windows, including at file-shaped paths. It does not test Windows
// file-symlink creation. All targets and markers are private test-owned paths.
func rejectionLink(t *testing.T, path string, directory bool) func() {
	t.Helper()
	targetDir := t.TempDir()
	marker := filepath.Join(targetDir, "marker")
	const contents = "reparse target must remain intact"
	if err := os.WriteFile(marker, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	createRejectionLink(t, marker, targetDir, path, directory)
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := marker
	if directory || rejectionLinkKind == "junction" {
		expected = targetDir
	}
	if target != expected {
		t.Fatalf("%s points to %q, want %q", rejectionLinkKind, target, expected)
	}
	check := func() {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&rejectionLinkMode == 0 {
			t.Fatalf("%s fixture is not a real link: %v, %v", rejectionLinkKind, info, err)
		}
		if got, err := os.Readlink(path); err != nil || got != target {
			t.Fatalf("%s changed: %q, %v", rejectionLinkKind, got, err)
		}
		if got, err := os.ReadFile(marker); err != nil || string(got) != contents { //nolint:gosec // Private test-owned marker.
			t.Fatalf("link target marker changed: %q, %v", got, err)
		}
	}
	check()
	t.Logf("verified real %s fixture", rejectionLinkKind)
	return check
}
