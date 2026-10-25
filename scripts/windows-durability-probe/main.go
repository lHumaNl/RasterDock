//go:build windows

// Developer-only, bounded API observations. No volume or ancestor flushes.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	directoryMode        = 0700
	filesystemBufferSize = 256
	shareMode            = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
)

func main() {
	if len(os.Args) != 1 {
		if len(os.Args) == 2 && os.Args[1] == childMarker {
			if err := runKillChild(); err != nil {
				_, _ = fmt.Fprintf(os.Stdout, "ERROR: %v\n", err)
			}
		}
		os.Exit(2) // Distinct from Windows Process.Kill exit code 1; never recurse.
	}
	r := newReport()
	run(r)
	if err := writeReport(os.Stdout, r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if r.Failed {
		os.Exit(1)
	}
}

func run(r *report) {
	root, err := os.MkdirTemp("", tempPrefix)
	r.record("owned root", "os.MkdirTemp", "", "", err, true)
	if err != nil {
		return
	}
	r.OwnedRoot = root
	defer func() { r.record("cleanup owned tree", "os.RemoveAll", "", "", os.RemoveAll(root), true) }()
	environment(r, root)
	if !setup(r, root) {
		return
	}
	flushCases(r, root)
	moveCases(r, root)
	hardlinkCase(r, root)
	killCases(r, root)
}

func environment(r *report, root string) {
	v := windows.RtlGetVersion()
	r.OSVersion = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	var elevated, size uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevated)), uint32(unsafe.Sizeof(elevated)), &size) //nolint:gosec // Win32 TOKEN_ELEVATION writes one DWORD into this live uint32; explicit buffer length matches its size.
	r.record("token elevation", "GetTokenInformation(TokenElevation)", "current process token", "0", err, false)
	if err == nil && size == uint32(unsafe.Sizeof(elevated)) {
		value := elevated != 0
		r.Elevated = &value
	}
	filesystem(r, root)
}

func filesystem(r *report, root string) {
	h, err := openHandle(r, "filesystem directory", root, 0, "0", windows.FILE_FLAG_BACKUP_SEMANTICS, "FILE_FLAG_BACKUP_SEMANTICS")
	if err != nil {
		return
	}
	defer closeHandle(r, "filesystem directory", h)
	var name [filesystemBufferSize]uint16
	err = windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, nil, &name[0], uint32(len(name)))
	r.record("filesystem", "GetVolumeInformationByHandle", "0", "0", err, false)
	if err == nil {
		r.Filesystem = windows.UTF16ToString(name[:])
	}
}

func setup(r *report, root string) bool {
	for _, dir := range []string{"directory", "nested", "nested/source", "nested/source/child"} {
		err := os.Mkdir(filepath.Join(root, filepath.FromSlash(dir)), directoryMode)
		r.record("setup "+dir, "os.Mkdir", "", "", err, true)
		if err != nil {
			return false
		}
	}
	files := map[string]string{"regular": sourceData, "absent-source": sourceData,
		"occupied-source": sourceData, "occupied-target": targetData, "nested/source/child/marker": sourceData}
	for name, data := range files {
		err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), fixtureMode)
		r.record("setup "+name, "os.WriteFile", "", "", err, true)
		if err != nil {
			return false
		}
	}
	return true
}

func openHandle(r *report, name, path string, access uint32, accessName string, flags uint32, flagName string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		r.record(name, "UTF16PtrFromString", "", "", err, true)
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(p, access, shareMode, nil, windows.OPEN_EXISTING, flags, 0)
	r.record(name, "CreateFileW", accessName, "OPEN_EXISTING|FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE|"+flagName, err, false)
	return h, err
}

func closeHandle(r *report, name string, h windows.Handle) {
	r.record(name, "CloseHandle", "", "0", windows.CloseHandle(h), true)
}

func flush(r *report, name, path string, access uint32, accessName string, flags uint32, flagName string) {
	h, err := openHandle(r, name, path, access, accessName, flags, flagName)
	if err != nil {
		return
	}
	defer closeHandle(r, name, h)
	r.record(name, "FlushFileBuffers", accessName, flagName, windows.FlushFileBuffers(h), false)
}

func flushCases(r *report, root string) {
	for _, access := range []struct {
		value uint32
		name  string
	}{
		{windows.GENERIC_READ, "GENERIC_READ"}, {windows.GENERIC_WRITE, "GENERIC_WRITE"},
	} {
		flush(r, "regular "+access.name, filepath.Join(root, "regular"), access.value, access.name, windows.FILE_ATTRIBUTE_NORMAL, "FILE_ATTRIBUTE_NORMAL")
		flush(r, "directory "+access.name, filepath.Join(root, "directory"), access.value, access.name, windows.FILE_FLAG_BACKUP_SEMANTICS, "FILE_FLAG_BACKUP_SEMANTICS")
	}
}

func move(r *report, name, source, target string) error {
	from, fromErr := windows.UTF16PtrFromString(source)
	to, toErr := windows.UTF16PtrFromString(target)
	if fromErr != nil {
		r.record(name, "UTF16PtrFromString", "", "", fromErr, true)
		return fromErr
	}
	if toErr != nil {
		r.record(name, "UTF16PtrFromString", "", "", toErr, true)
		return toErr
	}
	err := windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
	r.record(name, "MoveFileExW", "N/A", "MOVEFILE_WRITE_THROUGH (no REPLACE_EXISTING or COPY_ALLOWED)", err, false)
	return err
}

func moveCases(r *report, root string) {
	source, target := filepath.Join(root, "absent-source"), filepath.Join(root, "absent-target")
	err := move(r, "absent destination", source, target)
	r.record("absent destination invariant", "read/stat verification", "", "", verifyMove(source, target, err == nil), true)
	source, target = filepath.Join(root, "occupied-source"), filepath.Join(root, "occupied-target")
	err = move(r, "occupied destination", source, target)
	if err == nil {
		r.record("occupied destination must reject move", "invariant", "", "", fmt.Errorf("move unexpectedly succeeded"), true)
	}
	r.record("no replace invariant", "read verification", "", "", verifyNoReplace(source, target), true)
	source, target = filepath.Join(root, "nested", "source"), filepath.Join(root, "nested", "target")
	err = move(r, "nested directory absent destination", source, target)
	r.record("nested move invariant", "read/stat verification", "", "",
		verifyDirectoryMove(source, target, err == nil), true)
}

func hardlinkCase(r *report, root string) {
	source, link := filepath.Join(root, "regular"), filepath.Join(root, "hardlink")
	err := os.Link(source, link)
	r.record("hardlink setup", "os.Link / CreateHardLinkW", "N/A", "0", err, false)
	if err != nil {
		return
	}
	flush(r, "hardlink regular file", link, windows.GENERIC_WRITE, "GENERIC_WRITE", windows.FILE_ATTRIBUTE_NORMAL, "FILE_ATTRIBUTE_NORMAL")
	r.record("hardlink content invariant", "read verification", "", "", verifyContent(link, sourceData), true)
	r.record("original content invariant", "read verification", "", "", verifyContent(source, sourceData), true)
}
