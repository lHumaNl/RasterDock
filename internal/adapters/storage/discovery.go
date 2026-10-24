package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

// Discovery is a bounded inventory, not a validation or ownership claim.
type Discovery struct {
	Senders   []SavedTransfer
	Receivers []SavedReceive
	Issues    []string
}

// SavedTransfer identifies a ready descriptor; opening still validates all payloads.
type SavedTransfer struct{ Transfer, Directory string }

// SavedReceive contains pinned form defaults, not a claim that the session is available.
type SavedReceive struct{ Transfer, Session, Directory, Destination string }

// Bounds include unrelated entries so arbitrary directories cannot cause unbounded work.
const discoveryEntries = 512
const descriptorBytes = 4096

type inventory struct {
	root      *os.Root
	state     string
	remaining int
	result    Discovery
}

// Discover reads only the fixed transfer/send/prepared and transfer/receive/session levels.
// Existing metadata is reused; payloads are never opened or reconciled.
func Discover(ctx context.Context, state string) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, err
	}
	root, err := discoveryRoot(state)
	if errors.Is(err, os.ErrNotExist) {
		return Discovery{}, nil
	}
	if err != nil {
		return Discovery{}, err
	}
	defer func() { _ = root.Close() }()
	scan := inventory{root: root, state: state, remaining: discoveryEntries}
	return scan.scan(ctx)
}

func discoveryRoot(state string) (*os.Root, error) {
	if info, err := os.Lstat(state); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symlink state root is not discoverable")
	}
	return os.OpenRoot(state)
}

func (s *inventory) scan(ctx context.Context) (Discovery, error) {
	names, err := s.directories("transfers")
	if err != nil {
		return s.result, err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return s.result, err
		}
		s.transfer(ctx, name)
	}
	return s.result, ctx.Err()
}

func (s *inventory) directories(path string) ([]string, error) {
	f, err := checkedOpen(s.root, path, true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(s.remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > s.remaining {
		return nil, errors.New("discovery entry limit reached; use manual folder selection")
	}
	s.remaining -= len(entries)
	return s.uuidDirectories(path, entries), nil
}

func (s *inventory) uuidDirectories(path string, entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if _, err := domain.ParseID(entry.Name()); err != nil {
			continue
		}
		if !entry.IsDir() {
			s.issue(filepath.Join(path, entry.Name()), errors.New("expected a non-symlink session directory"))
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func (s *inventory) issue(path string, err error) {
	if err != nil {
		s.result.Issues = append(s.result.Issues, fmt.Sprintf("%s: %v", path, err))
	}
}

func (s *inventory) transfer(ctx context.Context, id string) {
	prepared := filepath.Join("transfers", id, "send", "prepared")
	if err := s.sender(id, prepared); !errors.Is(err, os.ErrNotExist) {
		s.issue(prepared, err)
	}
	parent := filepath.Join("transfers", id, "receive")
	sessions, err := s.directories(parent)
	s.issue(parent, err)
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			s.issue(parent, err)
			return
		}
		s.receiver(ctx, id, session, filepath.Join(parent, session))
	}
}

func (s *inventory) sender(id, dir string) error {
	f, err := checkedOpen(s.root, filepath.Join(dir, "ready.json"), false)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, descriptorBytes+1))
	if err != nil {
		return err
	}
	if err := validateDiscoveryDescriptor(raw, id); err != nil {
		return err
	}
	s.result.Senders = append(s.result.Senders, SavedTransfer{id, filepath.Join(s.state, dir)})
	return nil
}

func validateDiscoveryDescriptor(raw []byte, id string) error {
	var d v1.Descriptor
	if len(raw) > descriptorBytes {
		return errors.New("descriptor exceeds discovery limit")
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if domain.IDString(d.Transfer) != id {
		return ErrConflict
	}
	return nil
}

func (s *inventory) receiver(ctx context.Context, id, session, dir string) {
	destination, err := readReceiveSummary(ctx, s.root, s.state, dir, id)
	if err != nil {
		s.issue(dir, err)
		return
	}
	s.result.Receivers = append(s.result.Receivers, SavedReceive{id, session, filepath.Join(s.state, dir), destination})
}

// Reject symlinks at every relative component, including SQLite sidecars.
// OpenRoot also prevents a concurrent replacement from escaping the state root.
func checkedOpen(root *os.Root, path string, directory bool) (*os.File, error) {
	if err := checkDiscoveryPath(root, path, directory); err != nil {
		return nil, err
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		_ = f.Close()
		return nil, errors.Join(err, errors.New("unexpected state file type"))
	}
	return f, nil
}

func checkDiscoveryPath(root *os.Root, path string, directory bool) error {
	for part := path; part != "."; part = filepath.Dir(part) {
		info, err := root.Lstat(part)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink state is not discoverable")
		}
		if part == path && ((directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular())) {
			return errors.New("unexpected state file type")
		}
	}
	return nil
}
