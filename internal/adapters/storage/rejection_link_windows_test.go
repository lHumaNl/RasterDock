package storage_test

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const rejectionLinkKind = "junction"

// Go 1.23+ reports mount-point reparse points as irregular, not symlinks.
// createRejectionLink additionally verifies the actual mount-point reparse tag.
const rejectionLinkMode = os.ModeIrregular

// A mount-point reparse point requires no symlink privilege or Developer Mode.
// Use DeviceIoControl directly: no shell interpolation or system configuration.
func createRejectionLink(t *testing.T, _, dir, path string, _ bool) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	}()
	// SubstituteName is an NT path; PrintName is the ordinary absolute path.
	nt := `\??\` + dir
	if strings.HasPrefix(dir, `\\`) {
		nt = `\??\UNC\` + strings.TrimPrefix(dir, `\\`)
	}
	substitute, err := windows.UTF16FromString(nt)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	// REPARSE_DATA_BUFFER: 8-byte header, 8-byte mount-point offsets,
	// then two NUL-terminated UTF-16 names. Offsets are relative to PathBuffer.
	data := make([]byte, 16+2*(len(substitute)+len(printName)))
	if len(data) > windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE {
		t.Fatal("junction fixture path too long")
	}
	binary.LittleEndian.PutUint32(data[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(data)-8))             //nolint:gosec // Entire buffer is bounded above by MAXIMUM_REPARSE_DATA_BUFFER_SIZE (16 KiB).
	binary.LittleEndian.PutUint16(data[10:12], uint16(2*(len(substitute)-1))) //nolint:gosec // Name length is bounded by the checked 16 KiB buffer.
	binary.LittleEndian.PutUint16(data[12:14], uint16(2*len(substitute)))     //nolint:gosec // Name offset is bounded by the checked 16 KiB buffer.
	binary.LittleEndian.PutUint16(data[14:16], uint16(2*(len(printName)-1)))  //nolint:gosec // Name length is bounded by the checked 16 KiB buffer.
	for i, v := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[16+2*i:], v)
	}
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &returned, nil); err != nil { //nolint:gosec // Buffer length checked against the 16 KiB reparse limit above.
		t.Fatal(err)
	}
	buf := make([]byte, windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE, &returned, nil); err != nil {
		t.Fatal(err)
	}
	if returned < 8 || binary.LittleEndian.Uint32(buf[:4]) != windows.IO_REPARSE_TAG_MOUNT_POINT {
		t.Fatal("fixture is not a mount-point reparse point")
	}
}
