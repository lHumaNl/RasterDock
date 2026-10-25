package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path"

	"github.com/klauspost/compress/zstd"

	"rasterdock/internal/adapters/filesystem"
	"rasterdock/internal/domain"
)

func (r *Receiver) recordIntent(f domain.File, temp, identity string) error {
	_, err := r.db.Exec("INSERT INTO publication(file,target,temp,temp_identity,expected_size,expected_hash,published) VALUES(?,?,?,?,?,?,0) ON CONFLICT(file) DO UPDATE SET published=0,temp=excluded.temp,temp_identity=excluded.temp_identity", f.ID, f.Path, temp, identity, f.SourceSize, f.SourceHash)
	return err
}

func (r *Receiver) preparePublication(root *os.Root, f domain.File, temp string) (identity string, err error) {
	out, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	identity, err = fileIdentity(out)
	if err != nil {
		return "", err
	}
	// Register ownership before assembly, so write/hash failures leave a tracked
	// temp. Recovery never trusts intent alone: it rechecks identity and content.
	if err = r.recordIntent(f, temp, identity); err != nil {
		return "", err
	}
	if err = r.fault("publication-intent"); err != nil {
		return "", err
	}
	if err = r.assemble(out, f); err != nil {
		return "", err
	}
	return identity, Flush(out)
}

func (r *Receiver) assemble(out *os.File, f domain.File) error {
	if f.SourceSize > math.MaxInt64-1 || f.EncodedSize > math.MaxInt64-1 {
		return domain.ErrManifest
	}
	var reader io.Reader = bytes.NewReader(nil)
	if f.EncodedSize > 0 {
		in, err := os.Open(r.payload(f.ID))
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }() // Read-only encoded payload.
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(in, int64(f.EncodedSize)+1))
		if err != nil {
			return err
		}
		if n < 0 || uint64(n) != f.EncodedSize || hex.EncodeToString(h.Sum(nil)) != f.EncodedHash {
			return ErrConflict
		}
		if _, err = in.Seek(0, io.SeekStart); err != nil {
			return err
		}
		reader = in
	}
	if f.Encoding == "zstd" {
		z, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(8<<20), zstd.WithDecoderMaxMemory(16<<20))
		if err != nil {
			return err
		}
		defer z.Close()
		reader = z
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(reader, int64(f.SourceSize)+1))
	if err != nil {
		return err
	}
	if n < 0 || uint64(n) != f.SourceSize || hex.EncodeToString(h.Sum(nil)) != f.SourceHash {
		return ErrConflict
	}
	return nil
}

func syncRootDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(filesystem.Sync(f), f.Close())
}

func (r *Receiver) finishPublication(root *os.Root, f domain.File) error {
	// Verify again after publication, flush the actual target and every ancestor.
	if !checkFile(root, f.Path, f) {
		return ErrConflict
	}
	file, err := root.Open(f.Path)
	if err != nil {
		return err
	}
	err = errors.Join(Flush(file), file.Close())
	if err != nil {
		return err
	}
	for p := path.Dir(f.Path); ; p = path.Dir(p) {
		if err = syncRootDir(root, p); err != nil {
			return err
		}
		if p == "." {
			break
		}
	}
	if err = r.fault("publication-directory"); err != nil {
		return err
	}
	_, err = r.db.Exec("UPDATE publication SET published=1 WHERE file=?", f.ID)
	if err != nil {
		return err
	}
	return r.fault("publication-committed")
}
