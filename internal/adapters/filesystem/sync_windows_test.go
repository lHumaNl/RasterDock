package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSyncReadOnlyHandles(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "file"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, root.Close()) })
	for _, name := range []string{"file", "."} {
		t.Run(name, func(t *testing.T) {
			file, err := root.Open(name)
			requireNoError(t, err)
			requireNoError(t, Sync(file))
			requireNoError(t, file.Close())
		})
	}
}

func TestSyncClosedAndInvalidHandles(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	requireNoError(t, err)
	requireNoError(t, f.Close())
	if err = Sync(f); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed handle: %v", err)
	}
	if err = syncHandle(windows.InvalidHandle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("invalid handle: %v", err)
	}
}

func TestSyncReadOnlyObjectDenied(t *testing.T) {
	name := filepath.Join(t.TempDir(), "readonly")
	requireNoError(t, os.WriteFile(name, []byte("immutable"), 0600))
	requireNoError(t, os.Chmod(name, 0400))
	t.Cleanup(func() { requireNoError(t, os.Chmod(name, 0600)) })
	f, err := os.Open(name) //nolint:gosec // Test-owned temporary path.
	requireNoError(t, err)
	defer func() { requireNoError(t, f.Close()) }()
	if err = Sync(f); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("read-only object must deny write reopening: %v", err)
	}
}

func TestFlushAndClosePropagatesBothFailures(t *testing.T) {
	flushErr, closeErr := errors.New("flush"), errors.New("close")
	closed := false
	err := flushAndClose(windows.InvalidHandle,
		func(windows.Handle) error { return flushErr },
		func(windows.Handle) error { closed = true; return closeErr })
	if !closed || !errors.Is(err, flushErr) || !errors.Is(err, closeErr) {
		t.Fatalf("lost error or close: %v", err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncDirOpenError(t *testing.T) {
	err := SyncDir(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open error: %v", err)
	}
}

func TestSyncKeepsPinnedObjectAfterNameReplacement(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	requireNoError(t, err)
	defer func() { requireNoError(t, root.Close()) }()
	f, err := root.OpenFile("original", os.O_CREATE|os.O_RDWR, 0600)
	requireNoError(t, err)
	requireNoError(t, f.Close())
	f, err = root.Open("original")
	requireNoError(t, err)
	defer func() { requireNoError(t, f.Close()) }()
	want, err := f.Stat()
	requireNoError(t, err)
	requireNoError(t, root.Rename("original", "retained"))
	// The old name is absent, so reopening by File.Name cannot succeed.
	requireNoError(t, Sync(f))
	replacement, err := root.Create("original")
	requireNoError(t, err)
	requireNoError(t, replacement.Close())
	assertReopenedIdentity(t, f, want)
	requireNoError(t, Sync(f))
}

func TestSyncPropagatesSharingViolation(t *testing.T) {
	name := filepath.Join(t.TempDir(), "exclusive-reader")
	requireNoError(t, os.WriteFile(name, []byte("payload"), 0600))
	wide, err := windows.UTF16PtrFromString(name)
	requireNoError(t, err)
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	requireNoError(t, err)
	f := os.NewFile(uintptr(handle), name)
	defer func() { requireNoError(t, f.Close()) }()
	if err = Sync(f); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("write reopening sharing error: %v", err)
	}
}

func TestSyncDirectoryKeepsPinnedObjectAfterNameReplacement(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	requireNoError(t, err)
	defer func() { requireNoError(t, root.Close()) }()
	requireNoError(t, root.Mkdir("original", 0700))
	f, err := root.Open("original")
	requireNoError(t, err)
	defer func() { requireNoError(t, f.Close()) }()
	want, err := f.Stat()
	requireNoError(t, err)
	requireNoError(t, root.Rename("original", "retained"))
	requireNoError(t, root.Mkdir("original", 0700))
	assertReopenedIdentity(t, f, want)
	requireNoError(t, Sync(f))
}

func assertReopenedIdentity(t *testing.T, f *os.File, want os.FileInfo) {
	t.Helper()
	// Success alone cannot detect flushing the replacement directory. Compare
	// the reopened handle's identity with the original pinned object as well.
	raw, err := f.SyscallConn()
	requireNoError(t, err)
	var reopened windows.Handle
	var reopenErr error
	requireNoError(t, raw.Control(func(handle uintptr) {
		reopened, reopenErr = reopenWritable(windows.Handle(handle))
	}))
	requireNoError(t, reopenErr)
	reopenedFile := os.NewFile(uintptr(reopened), "pinned-object-test")
	defer func() { requireNoError(t, reopenedFile.Close()) }()
	got, err := reopenedFile.Stat()
	requireNoError(t, err)
	if !os.SameFile(want, got) {
		t.Fatal("write reopening selected a different object")
	}
}

func TestSyncDirectoryPropagatesACLDenial(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir) //nolint:gosec // Test-owned directory.
	requireNoError(t, err)
	defer func() { requireNoError(t, f.Close()) }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	requireNoError(t, err)
	sid := user.User.Sid.String()
	t.Cleanup(func() { setDirectoryTestACL(t, dir, "D:P(A;OICI;FA;;;"+sid+")") })
	setDirectoryTestACL(t, dir, "D:P(A;;GRGX;;;"+sid+")")
	if err = Sync(f); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("directory write reopening ACL error: %v", err)
	}
}

func setDirectoryTestACL(t *testing.T, dir, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	requireNoError(t, err)
	dacl, _, err := sd.DACL()
	requireNoError(t, err)
	requireNoError(t, windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
}

func TestSyncDirectoryPropagatesSharingViolation(t *testing.T) {
	wide, err := windows.UTF16PtrFromString(t.TempDir())
	requireNoError(t, err)
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	requireNoError(t, err)
	f := os.NewFile(uintptr(handle), "exclusive-directory-reader")
	defer func() { requireNoError(t, f.Close()) }()
	if err = Sync(f); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("directory write reopening sharing error: %v", err)
	}
}

func TestClosedRawControlErrorIsNotNormalizedByGo(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-control")
	requireNoError(t, err)
	raw, err := f.SyscallConn()
	requireNoError(t, err)
	requireNoError(t, f.Close())
	called := false
	controlErr := raw.Control(func(uintptr) { called = true })
	if called || controlErr == nil {
		t.Fatalf("closed Control: callback=%v error=%v", called, controlErr)
	}
	// Log typed evidence rather than matching the internal poll error's text.
	t.Logf("RawConn.Control: %T %v; matches os.ErrClosed=%v",
		controlErr, controlErr, errors.Is(controlErr, os.ErrClosed))
	normalized := normalizeControlError(f, controlErr)
	if !errors.Is(normalized, controlErr) || !errors.Is(normalized, os.ErrClosed) {
		t.Fatalf("normalization must retain both causes: %v", normalized)
	}
	if err := f.Sync(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("File.Sync must normalize the closed error: %v", err)
	}
}
