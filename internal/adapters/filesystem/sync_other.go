//go:build !windows

package filesystem

import "os"

// Sync preserves the platform's ordinary filesystem barrier.
func Sync(f *os.File) error { return f.Sync() }
