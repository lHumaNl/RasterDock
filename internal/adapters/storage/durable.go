// Package storage implements durable local payload and metadata storage.
package storage

import (
	"errors"
	"os"
	"path/filepath"

	"rasterdock/internal/adapters/filesystem"
)

// WriteDurable creates a new immutable file and synchronizes its directory.
func WriteDurable(p string, b []byte) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Caller-owned local state path, never a manifest path.
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = Flush(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return SyncDir(filepath.Dir(p))
}

// SyncDir makes newly created or published names durable.
func SyncDir(p string) error {
	return filesystem.SyncDir(p)
}

// mkdirAllDurable anchors the complete directory chain before a receiver can
// commit state. Existing directories are synced too: existence does not prove
// that a prior failed initialization (or another creator) persisted their names.
// With no caller-supplied established durable ancestor, the filesystem root is
// the only safe stopping boundary. This runs once per open, not per chunk.
func mkdirAllDurable(name string, syncDirectory func(string) error) error {
	abs, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	for current := abs; ; current = filepath.Dir(current) {
		if err = syncDirectory(current); err != nil {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
