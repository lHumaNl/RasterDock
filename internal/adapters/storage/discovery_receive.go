package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const discoveryDatabaseBytes = 32 << 20

// SQLite readers may create/update shared-memory sidecars even with mode=ro.
// Query a bounded private DB/WAL copy instead: never touch the live owner's lock,
// journal, payload or recovery path. Concurrent checkpoints may make the copy
// unreadable; that is a refreshable discovery issue, never a reason to repair it.
func readReceiveSummary(ctx context.Context, root *os.Root, state, dir, transfer string) (string, error) {
	scratch, err := os.MkdirTemp(state, ".discovery-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	for _, name := range []string{"state.sqlite", "state.sqlite-wal"} {
		err = copyDiscoveryFile(ctx, root, filepath.Join(dir, name), filepath.Join(scratch, name))
		if name == "state.sqlite-wal" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
	}
	return queryReceiveSummary(ctx, filepath.Join(scratch, "state.sqlite"), transfer)
}

func copyDiscoveryFile(ctx context.Context, root *os.Root, source, target string) error {
	in, err := checkedOpen(root, source, false)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Fixed SQLite filename in a private, application-root temporary directory.
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, io.LimitReader(contextReader{ctx, in}, discoveryDatabaseBytes+1))
	closeErr := out.Close()
	if n > discoveryDatabaseBytes {
		return errors.New("receive metadata exceeds discovery limit; use manual session path")
	}
	return errors.Join(copyErr, closeErr)
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func queryReceiveSummary(ctx context.Context, path, transfer string) (string, error) {
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	return queryPinnedSession(ctx, db, transfer)
}

func queryPinnedSession(ctx context.Context, db *sql.DB, transfer string) (string, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return "", err
	}
	if version != 1 {
		return "", errors.New("unsupported receive schema")
	}
	var raw []byte
	var destination string
	err := db.QueryRowContext(ctx, "SELECT descriptor,destination FROM session WHERE id=1 AND length(descriptor)<=? AND length(destination)<=?", descriptorBytes, descriptorBytes).Scan(&raw, &destination)
	if err != nil {
		return "", err
	}
	return validateReceiveSummary(raw, destination, transfer)
}

func validateReceiveSummary(raw []byte, destination, transfer string) (string, error) {
	if err := validateDiscoveryDescriptor(raw, transfer); err != nil {
		return "", err
	}
	if !validPinnedDestination(destination) {
		return "", ErrConflict
	}
	return destination, nil
}

func validPinnedDestination(destination string) bool {
	return filepath.IsAbs(destination) && filepath.Clean(destination) == destination && !strings.ContainsRune(destination, '\x00')
}
