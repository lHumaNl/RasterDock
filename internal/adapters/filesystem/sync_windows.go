package filesystem

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var reOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// Sync flushes the pinned object, never a path reconstructed from File.Name.
// Go 1.26 Root.Open returns an NtCreateFile filesystem handle; it must remain
// pinned while reopening obtains flush rights. ACL/sharing failures are fatal.
func Sync(f *os.File) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	err = raw.Control(func(handle uintptr) { syncErr = syncHandle(windows.Handle(handle)) })
	runtime.KeepAlive(f)
	return errors.Join(normalizeControlError(f, err), syncErr)
}

func normalizeControlError(f *os.File, err error) error {
	if err != nil {
		// RawControl exposes poll.ErrFileClosing rather than os.ErrClosed.
		// File.Stat on Windows queries the raw handle and does not normalize
		// closure. File.Sync uses the poll lock and the public error mapping.
		// Only probe after Control failed; preserve both failures if it fails.
		if syncErr := f.Sync(); syncErr != nil {
			return errors.Join(err, syncErr)
		}
	}
	return err
}

func syncHandle(handle windows.Handle) error {
	reopened, err := reopenWritable(handle)
	if err != nil {
		return err
	}
	return flushAndClose(reopened, windows.FlushFileBuffers, windows.CloseHandle)
}

func flushAndClose(handle windows.Handle, flush, closeHandle func(windows.Handle) error) error {
	return errors.Join(os.NewSyscallError("FlushFileBuffers", flush(handle)),
		os.NewSyscallError("CloseHandle", closeHandle(handle)))
}

func reopenWritable(handle windows.Handle) (windows.Handle, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return windows.InvalidHandle, os.NewSyscallError("GetFileInformationByHandle", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return reopenWritableDirectory(handle)
	}
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	result, _, err := reOpenFile.Call(uintptr(handle), windows.GENERIC_WRITE, share, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if windows.Handle(result) == windows.InvalidHandle {
		return windows.InvalidHandle, os.NewSyscallError("ReOpenFile", err)
	}
	return windows.Handle(result), nil
}

func reopenWritableDirectory(handle windows.Handle) (windows.Handle, error) {
	// Go 1.26's OBJECT_ATTRIBUTES.init maps Openat(root, ".") to an empty
	// NT name: reopen root itself, not its former path or a child named ".".
	name := windows.NTUnicodeString{}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: handle, ObjectName: &name}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	const options = windows.FILE_DIRECTORY_FILE | windows.FILE_SYNCHRONOUS_IO_NONALERT
	var reopened windows.Handle
	// FILE_GENERIC_WRITE is the mapped GENERIC_WRITE mask (including SYNCHRONIZE).
	// READ_ATTRIBUTES also permits identity inspection of the reopened object.
	// Native directory opens need no backup intent; ordinary ACL checks apply.
	err := windows.NtCreateFile(&reopened, windows.FILE_GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES, &attrs,
		&windows.IO_STATUS_BLOCK{}, nil, 0, share, windows.FILE_OPEN, options, 0, 0)
	var status windows.NTStatus
	if errors.As(err, &status) {
		err = status.Errno()
	}
	if err != nil {
		return windows.InvalidHandle, os.NewSyscallError("NtCreateFile", err)
	}
	return reopened, nil
}
