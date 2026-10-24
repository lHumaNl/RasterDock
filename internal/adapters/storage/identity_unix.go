//go:build darwin || linux

package storage

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported file identity")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
