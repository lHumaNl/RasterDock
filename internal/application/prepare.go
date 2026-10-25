// Package application implements GUI-independent transfer use cases.
package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/klauspost/compress/zstd"

	"rasterdock/internal/adapters/filesystem"
	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

// Prepared is an immutable snapshot loaded from the sender's directory.
type Prepared struct {
	Directory  string
	Descriptor v1.Descriptor
	Manifest   domain.Manifest
}

// Prepare copies regular files, detects ordinary source changes and publishes a ready descriptor last.
// The caller owns the engine and supplies its platform's durable flush operation.
// On Windows state (or the optional containing anchor) must be an established
// private namespace. Its parent binding is outside this operation's boundary.
func Prepare(ctx context.Context, source, state string, block uint16, flush func(*os.File) error, anchors ...string) (*Prepared, error) {
	if block < 64 || block > 2048 {
		return nil, v1.ErrPacket
	}
	if runtime.GOOS == "windows" {
		var err error
		state, err = filepath.Abs(state)
		if err != nil {
			return nil, err
		}
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	root := source
	var names []string
	switch {
	case info.Mode().IsRegular():
		root = filepath.Dir(source)
		names = []string{filepath.Base(source)}
	case info.IsDir():
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("unsupported source: %s", p)
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			names = append(names, filepath.ToSlash(rel))
			if len(names) > 100000 {
				return domain.ErrManifest
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, domain.ErrManifest
	}
	sort.Strings(names)
	id, err := domain.NewID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(state, "transfers", domain.IDString(id), "send", "prepared")
	boundary, err := preparationAnchor(state, anchors)
	if err != nil {
		return nil, err
	}
	if err = makePreparedDirectory(dir, boundary); err != nil {
		return nil, err
	}
	p := &Prepared{Directory: dir, Descriptor: v1.Descriptor{Transfer: id, Block: block}, Manifest: domain.Manifest{TransferID: domain.IDString(id), BlockSize: block}}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sourceRoot.Close() }() // Read-only directory handle.
	for i, name := range names {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !domain.ValidPath(name) {
			return nil, domain.ErrManifest
		}
		f, e := copySource(ctx, sourceRoot, name, filepath.Join(dir, fmt.Sprintf("%d.bin", i+1)), flush)
		if e != nil {
			return nil, e
		}
		f.ID = uint32(i + 1)
		f.Path = name
		if f.EncodedSize > 0 {
			f.Chunks = 1 + (f.EncodedSize-1)/uint64(block)
		}
		p.Manifest.Files = append(p.Manifest.Files, f)
	}
	raw, err := p.Manifest.Encode()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf, zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(8<<20))
	if err != nil {
		return nil, err
	}
	if _, err = zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	p.Descriptor.Length = uint64(buf.Len()) //nolint:gosec // Buffer lengths cannot be negative.
	p.Descriptor.Hash = sha256.Sum256(buf.Bytes())
	if p.Descriptor.Length > 64<<20 {
		return nil, domain.ErrManifest
	}
	if err = writeNew(filepath.Join(dir, "manifest.zst"), buf.Bytes(), flush); err != nil {
		return nil, err
	}
	desc, err := json.Marshal(p.Descriptor)
	if err != nil {
		return nil, err
	}
	if err = writeNew(filepath.Join(dir, "ready.json"), desc, flush); err != nil {
		return nil, err
	}
	// Every newly created ancestor must be durable before readiness is reported.
	for path := dir; ; path = filepath.Dir(path) {
		if err = syncDirectory(path); err != nil {
			return nil, err
		}
		if atPreparationBoundary(path, boundary) || filepath.Dir(path) == path {
			break
		}
	}
	return p, nil
}

func copySource(ctx context.Context, root *os.Root, name, dest string, flush func(*os.File) error) (domain.File, error) {
	var result domain.File
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return result, errors.Join(err, domain.ErrManifest)
	}
	in, err := root.Open(name)
	if err != nil {
		return result, err
	}
	defer func() { _ = in.Close() }() // Read-only source; all reads checked.
	before, err := in.Stat()
	if err != nil {
		return result, err
	}
	if !os.SameFile(info, before) {
		return result, domain.ErrManifest
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Internally generated UUID/file-ID path, not a source pathname.
	if err != nil {
		return result, err
	}
	defer func() { _ = out.Close() }() // Cleanup on failure; successful close checked below.
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		read, e := in.Read(buf)
		if read > 0 {
			written, werr := io.MultiWriter(out, h).Write(buf[:read])
			n += int64(written)
			if werr != nil {
				return result, werr
			}
		}
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return result, e
		}
	}
	after, err := in.Stat()
	if err != nil {
		return result, err
	}
	current, err := root.Lstat(name)
	if err != nil {
		return result, err
	}
	if !os.SameFile(before, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || n != before.Size() {
		return result, errors.New("source changed during preparation")
	}
	if err = flush(out); err != nil {
		return result, err
	}
	if err = out.Close(); err != nil {
		return result, err
	}
	result.SourceSize = uint64(n)
	result.EncodedSize = uint64(n)
	result.SourceHash = hex.EncodeToString(h.Sum(nil))
	result.EncodedHash = result.SourceHash
	result.Encoding = "raw"
	return result, nil
}

func writeNew(path string, b []byte, flush func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Internally generated sender state path.
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = flush(f)
	}
	return errors.Join(err, f.Close())
}
func syncDirectory(path string) error {
	return filesystem.SyncDir(path)
}

// OpenPrepared revalidates descriptor and manifest; it never reads the original source.
func OpenPrepared(dir string) (*Prepared, error) {
	b, err := os.ReadFile(filepath.Join(dir, "ready.json")) //nolint:gosec // User explicitly selects a local saved transfer directory.
	if err != nil {
		return nil, err
	}
	p := &Prepared{Directory: dir}
	if err = json.Unmarshal(b, &p.Descriptor); err != nil {
		return nil, err
	}
	if p.Descriptor.Length == 0 || p.Descriptor.Length > 64<<20 {
		return nil, domain.ErrManifest
	}
	f, err := os.Open(filepath.Join(dir, "manifest.zst")) //nolint:gosec // Fixed filename inside selected saved transfer.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // Read-only manifest.
	b, err = io.ReadAll(io.LimitReader(f, int64(p.Descriptor.Length)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(b)) != p.Descriptor.Length || sha256.Sum256(b) != p.Descriptor.Hash {
		return nil, domain.ErrManifest
	}
	p.Manifest, err = DecodeManifest(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if p.Manifest.TransferID != domain.IDString(p.Descriptor.Transfer) || p.Manifest.BlockSize != p.Descriptor.Block {
		return nil, domain.ErrManifest
	}
	for _, file := range p.Manifest.Files {
		if err = verifyPreparedFile(dir, file); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func verifyPreparedFile(dir string, file domain.File) error {
	in, err := os.Open(filepath.Join(dir, fmt.Sprintf("%d.bin", file.ID))) //nolint:gosec // Numeric file ID, never manifest destination path.
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, in)
	err = errors.Join(copyErr, in.Close())
	if err != nil {
		return err
	}
	if n < 0 || uint64(n) != file.EncodedSize || hex.EncodeToString(hash.Sum(nil)) != file.EncodedHash {
		return errors.New("saved transfer payload corrupted")
	}
	return nil
}

// DecodeManifest bounds zstd memory and decompressed JSON Lines.
func DecodeManifest(r io.Reader) (domain.Manifest, error) {
	z, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(8<<20), zstd.WithDecoderMaxMemory(16<<20))
	if err != nil {
		return domain.Manifest{}, err
	}
	defer z.Close()
	return domain.ReadManifest(z)
}

// ManifestPackets is the first file-data ordinal.
func (p *Prepared) ManifestPackets() uint64 {
	return 1 + (p.Descriptor.Length-1)/uint64(p.Descriptor.Block)
}

// PacketCount includes metadata and nonempty file streams.
func (p *Prepared) PacketCount() uint64 {
	n := p.ManifestPackets()
	for _, f := range p.Manifest.Files {
		n += f.Chunks
	}
	return n
}

// Packet reads only the requested block from the immutable snapshot.
func (p *Prepared) Packet(ordinal uint64) (v1.Packet, error) {
	q := v1.Packet{Transfer: p.Descriptor.Transfer, Kind: 1, Count: p.ManifestPackets(), Size: p.Descriptor.Length, Index: ordinal}
	name := "manifest.zst"
	if ordinal >= q.Count {
		ordinal -= q.Count
		q.Kind = 2
		found := false
		for _, f := range p.Manifest.Files {
			if ordinal < f.Chunks {
				q.FileID = f.ID
				q.Size = f.EncodedSize
				q.Count = f.Chunks
				q.Index = ordinal
				name = fmt.Sprintf("%d.bin", f.ID)
				found = true
				break
			}
			ordinal -= f.Chunks
		}
		if !found {
			return q, v1.ErrPacket
		}
	}
	size := uint64(p.Descriptor.Block)
	offset := q.Index * size
	if left := q.Size - offset; left < size {
		size = left
	}
	f, err := os.Open(filepath.Join(p.Directory, name))
	if err != nil {
		return q, err
	}
	defer func() { _ = f.Close() }() // Read-only payload.
	q.Payload = make([]byte, size)
	_, err = f.ReadAt(q.Payload, int64(offset)) //nolint:gosec // Offset is below the validated manifest stream size (MaxInt64-1).
	return q, err
}
