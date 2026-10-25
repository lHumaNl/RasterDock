//go:build !darwin && !windows

package storage

import "os"

// Flush synchronizes file content; platform runtime validation is separate.
func Flush(f *os.File) error { return f.Sync() }
