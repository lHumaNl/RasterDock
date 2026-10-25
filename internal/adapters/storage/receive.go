package storage

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"github.com/gofrs/flock"
	_ "github.com/mattn/go-sqlite3" // Registers the statically linked SQLite driver.

	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

// ErrConflict stops a session without replacing previously accepted data.
var ErrConflict = errors.New("receive identity or destination conflict")

// ErrMetadata means data cannot yet be acknowledged durably.
var ErrMetadata = errors.New("verified manifest required")

// Receiver has one serialized owner. Fault is an optional deterministic test hook.
type Receiver struct {
	db               *sql.DB
	lock             *flock.Flock
	dir, destination string
	descriptor       v1.Descriptor
	manifest         *domain.Manifest
	decode           func(io.Reader) (domain.Manifest, error)
	stopped          bool
	published        map[uint32]bool
	Fault            func(string) error
}

// OpenReceive pins the bootstrap descriptor and reconciles stored bytes before accepting data.
// Windows requires one existing private anchor containing dir; see ErrNamespace.
// Destination is also caller-established. Unix retains whole-ancestor barriers.
func OpenReceive(dir, destination string, d v1.Descriptor, decode func(io.Reader) (domain.Manifest, error), anchor ...string) (*Receiver, error) {
	return openReceive(dir, destination, d, decode, SyncDir, anchor...)
}

// syncDirectory is injected at the filesystem durability boundary for tests.
func openReceive(dir, destination string, d v1.Descriptor, decode func(io.Reader) (domain.Manifest, error), syncDirectory func(string) error, anchor ...string) (*Receiver, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if err = initializeNamespace(dir, syncDirectory, anchor); err != nil {
		return nil, err
	}
	r := &Receiver{dir: dir, destination: destination, descriptor: d, decode: decode, lock: flock.New(filepath.Join(dir, "owner.lock")), published: make(map[uint32]bool)}
	locked, err := r.lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("receive session already owned")
	}
	success := false
	defer func() {
		if !success {
			_ = r.Close()
		}
	}()
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "state.sqlite"), RawQuery: "_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_cache_size=-2048"}
	r.db, err = sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	r.db.SetMaxOpenConns(1)
	if _, err = r.db.Exec("PRAGMA fullfsync=ON; PRAGMA checkpoint_fullfsync=ON;"); err != nil {
		return nil, err
	}
	for pragma, want := range map[string]int{"synchronous": 2, "foreign_keys": 1, "fullfsync": 1, "checkpoint_fullfsync": 1} {
		var got int
		if err = r.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			return nil, err
		}
		if got != want {
			return nil, fmt.Errorf("pragma %s = %d", pragma, got)
		}
	}
	var version int
	if err = r.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version > 1 {
		return nil, errors.New("unsupported receive schema")
	}
	if version == 0 {
		_, err = r.db.Exec(`BEGIN IMMEDIATE;
CREATE TABLE session(id INTEGER PRIMARY KEY CHECK(id=1), descriptor BLOB NOT NULL, destination TEXT NOT NULL, manifest_verified INTEGER NOT NULL DEFAULT 0);
CREATE TABLE chunks(file INTEGER NOT NULL, idx INTEGER NOT NULL, length INTEGER NOT NULL, hash BLOB NOT NULL, crc INTEGER NOT NULL, PRIMARY KEY(file,idx));
CREATE TABLE files(id INTEGER PRIMARY KEY, metadata BLOB NOT NULL);
CREATE TABLE publication(file INTEGER PRIMARY KEY, target TEXT NOT NULL, temp TEXT NOT NULL, temp_identity TEXT NOT NULL, expected_size INTEGER NOT NULL, expected_hash TEXT NOT NULL, published INTEGER NOT NULL DEFAULT 0);
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);
INSERT INTO schema_migrations VALUES(1);
PRAGMA user_version=1; COMMIT;`)
		if err != nil {
			return nil, err
		}
	}
	pinned, _ := json.Marshal(d)
	var existing []byte
	var target string
	err = r.db.QueryRow("SELECT descriptor,destination FROM session WHERE id=1").Scan(&existing, &target)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = r.db.Exec("INSERT INTO session(id,descriptor,destination) VALUES(1,?,?)", pinned, destination)
	} else if err == nil && (!bytes.Equal(existing, pinned) || target != destination) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if err = syncDirectory(dir); err != nil {
		return nil, err
	}
	if err = r.recoverChunks(); err != nil {
		return nil, err
	}
	if err = r.verifyManifest(); err != nil && !errors.Is(err, ErrMetadata) {
		return nil, err
	}
	success = true
	return r, nil
}

// Close releases SQLite and the OS-held session lock.
func (r *Receiver) Close() error {
	var err error
	if r.db != nil {
		err = r.db.Close()
	}
	if r.lock != nil {
		err = errors.Join(err, r.lock.Close())
	}
	return err
}
func (r *Receiver) fault(stage string) error {
	if r.Fault != nil {
		return r.Fault(stage)
	}
	return nil
}
func (r *Receiver) payload(id uint32) string {
	return filepath.Join(r.dir, "payload", fmt.Sprintf("%d.bin", id))
}

// CheckDescriptor stops this owner on incompatible bootstrap information.
func (r *Receiver) CheckDescriptor(d v1.Descriptor) error {
	if r.stopped || d != r.descriptor {
		r.stopped = true
		return ErrConflict
	}
	return nil
}

// Accept flushes payload before committing its chunk record. Missing metadata never produces progress.
func (r *Receiver) Accept(p v1.Packet) error {
	if r.stopped {
		return ErrConflict
	}
	if p.Transfer != r.descriptor.Transfer {
		return ErrConflict
	}
	if err := p.ValidateBlock(r.descriptor.Block); err != nil {
		return err
	}
	if p.Kind == 0 {
		return nil
	}
	if p.Kind == 1 {
		if p.Size != r.descriptor.Length {
			return ErrConflict
		}
	} else {
		if r.manifest == nil {
			return ErrMetadata
		}
		if int(p.FileID) > len(r.manifest.Files) {
			return v1.ErrPacket
		}
		f := r.manifest.Files[p.FileID-1]
		if p.Size != f.EncodedSize || p.Count != f.Chunks {
			return v1.ErrPacket
		}
	}
	hash := sha256.Sum256(p.Payload)
	var old []byte
	err := r.db.QueryRow("SELECT hash FROM chunks WHERE file=? AND idx=?", p.FileID, p.Index).Scan(&old)
	if err == nil {
		if !bytes.Equal(old, hash[:]) {
			r.stopped = true
			return ErrConflict
		}
		// The bytes may already be durable while verification/reconciliation failed.
		// A repeated manifest packet must be able to retry that transition.
		if p.Kind == 1 && r.manifest == nil {
			return r.verifyAvailableManifest()
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	f, err := os.OpenFile(r.payload(p.FileID), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(p.Payload, int64(p.Index*uint64(r.descriptor.Block))) //nolint:gosec // Validated against bounded manifest size and block count above.
	if err == nil {
		err = Flush(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = SyncDir(filepath.Join(r.dir, "payload")); err != nil {
		return err
	}
	if err = r.fault("payload-flushed"); err != nil {
		return err
	}
	if _, err = r.db.Exec("INSERT INTO chunks(file,idx,length,hash,crc) VALUES(?,?,?,?,?)", p.FileID, p.Index, len(p.Payload), hash[:], v1.CRC(p.Payload)); err != nil {
		return err
	}
	if p.Kind == 1 {
		return r.verifyAvailableManifest()
	}
	return nil
}

func (r *Receiver) verifyAvailableManifest() error {
	err := r.verifyManifest()
	if errors.Is(err, ErrMetadata) {
		return nil
	}
	return err
}

func (r *Receiver) verifyManifest() error {
	var count uint64
	if err := r.db.QueryRow("SELECT count(*) FROM chunks WHERE file=0").Scan(&count); err != nil {
		return err
	}
	if count != 1+(r.descriptor.Length-1)/uint64(r.descriptor.Block) {
		return ErrMetadata
	}
	f, err := os.Open(r.payload(0))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()                                      // Read-only manifest.
	b, err := io.ReadAll(io.LimitReader(f, int64(r.descriptor.Length)+1)) //nolint:gosec // Descriptor length is capped at 64 MiB on open.
	if err != nil {
		return err
	}
	if uint64(len(b)) != r.descriptor.Length || sha256.Sum256(b) != r.descriptor.Hash {
		return ErrConflict
	}
	m, err := r.decode(bytes.NewReader(b))
	if err != nil {
		return err
	}
	if m.TransferID != domain.IDString(r.descriptor.Transfer) || m.BlockSize != r.descriptor.Block {
		return ErrConflict
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Cleanup after error, or ErrTxDone after successful commit.
	for _, f := range m.Files {
		b, e := json.Marshal(f)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT OR REPLACE INTO files(id,metadata) VALUES(?,?)", f.ID, b); e != nil {
			return e
		}
	}
	if _, err = tx.Exec("UPDATE session SET manifest_verified=1 WHERE id=1"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.manifest = &m
	// Both startup and late metadata restoration must cross the same recovery
	// barrier before data acknowledgements and publication counts become visible.
	if err = r.reconcilePublished(); err != nil {
		r.manifest = nil
		clear(r.published)
		return err
	}
	return nil
}

func (r *Receiver) recoverChunks() error {
	if _, err := r.db.Exec("UPDATE session SET manifest_verified=0 WHERE id=1"); err != nil {
		return err
	}
	rows, err := r.db.Query("SELECT file,idx,length,hash FROM chunks")
	if err != nil {
		return err
	}
	type key struct {
		file  uint32
		index uint64
	}
	var bad []key
	for rows.Next() {
		var k key
		var length int
		var hash []byte
		if err = rows.Scan(&k.file, &k.index, &length, &hash); err != nil {
			_ = rows.Close()
			return err
		}
		valid := length > 0 && length <= int(r.descriptor.Block) && k.index <= (^uint64(0)>>1)/uint64(r.descriptor.Block)
		if valid {
			f, e := os.Open(r.payload(k.file))
			if e != nil {
				valid = false
			} else {
				b := make([]byte, length)
				_, e = f.ReadAt(b, int64(k.index*uint64(r.descriptor.Block))) //nolint:gosec // Explicit offset bound checked above.
				_ = f.Close()
				h := sha256.Sum256(b)
				valid = e == nil && bytes.Equal(h[:], hash)
			}
		}
		if !valid {
			bad = append(bad, k)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, k := range bad {
		if _, err = r.db.Exec("DELETE FROM chunks WHERE file=? AND idx=?", k.file, k.index); err != nil {
			return err
		}
	}
	return nil
}

// DurableBytes excludes metadata and unverified dependencies.
func (r *Receiver) DurableBytes() (uint64, error) {
	if r.manifest == nil {
		return 0, nil
	}
	var n uint64
	err := r.db.QueryRow("SELECT coalesce(sum(length),0) FROM chunks WHERE file>0").Scan(&n)
	return n, err
}

// PublishedFiles distinguishes durable file publication from chunk progress.
func (r *Receiver) PublishedFiles() (uint64, uint64, error) {
	if r.manifest == nil {
		return 0, 0, nil
	}
	var n uint64
	err := r.db.QueryRow("SELECT count(*) FROM publication WHERE published=1").Scan(&n)
	return n, uint64(len(r.manifest.Files)), err //nolint:gosec // Slice length is nonnegative and capped at 100000 files.
}

func checkFile(root *os.Root, name string, f domain.File) bool {
	if f.SourceSize > math.MaxInt64-1 {
		return false
	}
	in, err := root.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = in.Close() }() // Read-only verification input.
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(f.SourceSize) {
		return false
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(in, int64(f.SourceSize)+1))
	return err == nil && n >= 0 && uint64(n) == f.SourceSize && hex.EncodeToString(h.Sum(nil)) == f.SourceHash
}

func (r *Receiver) reconcilePublished() error {
	root, err := os.OpenRoot(r.destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // Directory handle; mutation durability is checked explicitly.
	for _, f := range r.manifest.Files {
		if _, err = r.recoverPublication(root, f); err != nil {
			return err
		}
	}
	return nil
}

// Publish verifies each complete file and atomically links it without overwriting an existing target.
func (r *Receiver) Publish() error {
	if r.stopped {
		return ErrConflict
	}
	if r.manifest == nil {
		return ErrMetadata
	}
	root, err := os.OpenRoot(r.destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // Directory handle; mutation durability is checked explicitly.
	for _, f := range r.manifest.Files {
		if r.published[f.ID] {
			continue
		}
		// Verified publication temps and existing targets do not require payload.
		if recovered, e := r.recoverPublication(root, f); e != nil {
			return e
		} else if recovered {
			continue
		}
		var count uint64
		if err = r.db.QueryRow("SELECT count(*) FROM chunks WHERE file=?", f.ID).Scan(&count); err != nil {
			return err
		}
		if count != f.Chunks {
			continue
		}
		if err = r.publishFile(root, f); err != nil {
			return err
		}
	}
	return nil
}

func (r *Receiver) publishFile(root *os.Root, f domain.File) error {
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	temp := ".rasterdock-" + domain.IDString(id) + ".tmp"
	if checkFile(root, f.Path, f) {
		in, e := root.Open(f.Path)
		if e != nil {
			return e
		}
		identity, e := fileIdentity(in)
		e = errors.Join(e, in.Close())
		if e != nil {
			return e
		}
		if err = r.recordIntent(f, "", identity); err != nil {
			return err
		}
		if err = r.finishPublication(root, f); err != nil {
			return err
		}
		r.published[f.ID] = true
		return nil
	}
	if _, err = root.Lstat(f.Path); err == nil {
		return ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = root.MkdirAll(path.Dir(f.Path), 0700); err != nil {
		return err
	}
	identity, err := r.preparePublication(root, f, temp)
	if err != nil {
		return err
	}
	if err = syncRootDir(root, "."); err != nil {
		return err
	}
	return r.linkPublication(root, f, publicationIntent{target: f.Path, temp: temp, identity: identity, hash: f.SourceHash, size: f.SourceSize})
}
