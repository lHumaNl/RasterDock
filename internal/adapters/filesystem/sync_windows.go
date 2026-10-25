package filesystem

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

var reOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// Sync flushes the pinned object, never a path reconstructed from File.Name.
// Go 1.26 Root.Open returns an NtCreateFile filesystem handle; it must remain
// pinned while ReOpenFile obtains flush rights. ACL/sharing failures are fatal.
func Sync(f *os.File) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	err = raw.Control(func(handle uintptr) { syncErr = syncHandle(windows.Handle(handle)) })
	runtime.KeepAlive(f)
	return errors.Join(err, syncErr)
}

func syncHandle(handle windows.Handle) error {
	// No FILE_ATTRIBUTE_* flags are legal for ReOpenFile. Backup semantics
	// permits directories; it does not grant privileges to an ordinary caller.
	reopened, err := reopenWritable(handle)
	if err != nil {
		return os.NewSyscallError("ReOpenFile", err)
	}
	return flushAndClose(reopened, windows.FlushFileBuffers, windows.CloseHandle)
}

func flushAndClose(handle windows.Handle, flush, closeHandle func(windows.Handle) error) error {
	return errors.Join(os.NewSyscallError("FlushFileBuffers", flush(handle)),
		os.NewSyscallError("CloseHandle", closeHandle(handle)))
}

func reopenWritable(handle windows.Handle) (windows.Handle, error) {
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	result, _, err := reOpenFile.Call(uintptr(handle), windows.GENERIC_WRITE, share, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if windows.Handle(result) == windows.InvalidHandle {
		return windows.InvalidHandle, err
	}
	return windows.Handle(result), nil
}
