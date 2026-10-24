package storage

import (
	"os"
	"syscall"
)

// Flush requests F_FULLFSYNC on macOS, not only a kernel-cache flush.
func Flush(f *os.File) error {
	if err := f.Sync(); err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), 51, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
