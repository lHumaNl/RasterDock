// Package filesystem provides shared platform filesystem barriers.
package filesystem

import (
	"errors"
	"os"
)

// SyncDir flushes a caller-owned directory, including errors closing its handle.
func SyncDir(name string) error {
	f, err := os.Open(name) //nolint:gosec // Caller-owned namespace, not a manifest path.
	if err != nil {
		return err
	}
	return errors.Join(Sync(f), f.Close())
}
