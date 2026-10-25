package storage

import (
	"os"

	"rasterdock/internal/adapters/filesystem"
)

// Flush obtains write access to the same object before FlushFileBuffers.
func Flush(f *os.File) error { return filesystem.Sync(f) }
