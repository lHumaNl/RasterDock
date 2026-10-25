package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

// This is production Prepare/Receive/Publish, not the diagnostic probe. It also
// runs under the lead's non-admin harness; no privilege-based skip is permitted.
func TestWindowsStorageUnderProtectedParent(t *testing.T) {
	parent, work := protectedNamespace(t)
	assertProtectedParent(t, parent)
	source, destination := filepath.Join(work, "source"), filepath.Join(work, "destination")
	mustStorage(t, os.Mkdir(source, 0700))
	mustStorage(t, os.Mkdir(destination, 0700))
	mustStorage(t, os.WriteFile(filepath.Join(source, "payload"), []byte("verified"), 0600))
	p, err := application.Prepare(t.Context(), source, work, 64, storage.Flush)
	mustStorage(t, err)
	session := filepath.Join(work, "managed", "receive", "session")
	receiveAndReopen(t, p, session, destination, work)
	data, err := os.ReadFile(filepath.Join(destination, "payload")) //nolint:gosec // Test-owned namespace.
	mustStorage(t, err)
	if string(data) != "verified" {
		t.Fatalf("output: %q", data)
	}
}

func protectedNamespace(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	work := filepath.Join(parent, "work")
	mustStorage(t, os.Mkdir(work, 0700))
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	mustStorage(t, err)
	sid := user.User.Sid.String()
	setTestACL(t, work, "D:P(A;OICI;FA;;;"+sid+")")
	t.Cleanup(func() { setTestACL(t, parent, "D:P(A;OICI;FA;;;"+sid+")") })
	setTestACL(t, parent, "D:P(A;;GRGX;;;"+sid+")")
	return parent, work
}

func setTestACL(t *testing.T, name, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	mustStorage(t, err)
	dacl, _, err := sd.DACL()
	mustStorage(t, err)
	mustStorage(t, windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil))
}

func assertProtectedParent(t *testing.T, parent string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(parent, "forbidden"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Test-owned ACL fixture.
	if err == nil {
		mustStorage(t, f.Close())
		t.Fatal("protected parent allowed creation")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("create: %v", err)
	}
	if err = storage.SyncDir(parent); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("protected parent flush: %v", err)
	}
}

func receiveAndReopen(t *testing.T, p *application.Prepared, session, dest, anchor string) {
	t.Helper()
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, anchor)
	mustStorage(t, err)
	for n := uint64(0); n < p.PacketCount(); n++ {
		packet, err := p.Packet(n)
		mustStorage(t, err)
		mustStorage(t, r.Accept(packet))
	}
	mustStorage(t, r.Publish())
	mustStorage(t, r.Close())
	r, err = storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, anchor)
	mustStorage(t, err)
	mustStorage(t, r.Publish())
	mustStorage(t, r.Close())
}

func mustStorage(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsReadOnlyPublicationFailsExplicitly(t *testing.T) {
	p, session, dest := publicationFixture(t)
	target := filepath.Join(dest, "a")
	mustStorage(t, os.WriteFile(target, []byte("verified"), 0600))
	mustStorage(t, os.Chmod(target, 0400))
	t.Cleanup(func() { mustStorage(t, os.Chmod(target, 0600)) })
	r, err := storage.OpenReceive(session, dest, p.Descriptor, application.DecodeManifest, filepath.Dir(session))
	mustStorage(t, err)
	defer func() { mustStorage(t, r.Close()) }()
	if err = r.Publish(); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("read-only target acknowledged: %v", err)
	}
	count, _, err := r.PublishedFiles()
	mustStorage(t, err)
	if count != 0 {
		t.Fatal("failed flush marked published")
	}
	mustStorage(t, os.Chmod(target, 0600))
	mustStorage(t, r.Publish())
}
