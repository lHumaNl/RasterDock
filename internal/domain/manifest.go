// Package domain contains transfer invariants independent of GUI and storage.
package domain

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"
	"unicode/utf8"
)

// ErrManifest denotes invalid or unsupported metadata.
var ErrManifest = errors.New("invalid manifest")

// File describes the original bytes and the encoded stream.
type File struct {
	Type        string `json:"type"`
	ID          uint32 `json:"file_id"`
	Path        string `json:"path"`
	SourceSize  uint64 `json:"source_size"`
	SourceHash  string `json:"source_sha256"`
	Encoding    string `json:"encoding"`
	EncodedSize uint64 `json:"encoded_size"`
	EncodedHash string `json:"encoded_sha256"`
	Chunks      uint64 `json:"chunk_count"`
}

// Manifest is a validated collection in wire order.
type Manifest struct {
	TransferID string
	BlockSize  uint16
	Files      []File
}
type boundary struct {
	Type       string `json:"type"`
	Format     int    `json:"format,omitempty"`
	TransferID string `json:"transfer_id,omitempty"`
	BlockSize  uint16 `json:"block_size,omitempty"`
	FileCount  uint64 `json:"file_count"`
	Packets    uint64 `json:"data_packet_count"`
}

// NewID creates an RFC 4122 UUIDv4.
func NewID() ([16]byte, error) {
	var id [16]byte
	_, err := rand.Read(id[:])
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return id, err
}

// IDString formats UUID bytes without changing their wire order.
func IDString(id [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

// ParseID accepts canonical lowercase UUIDv4.
func ParseID(s string) ([16]byte, error) {
	var id [16]byte
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return id, ErrManifest
	}
	copy(id[:], b)
	if IDString(id) != s || id[6]>>4 != 4 || id[8]>>6 != 2 {
		return id, ErrManifest
	}
	return id, nil
}

// ValidPath rejects nonportable names instead of silently renaming them.
func ValidPath(p string) bool {
	if !utf8.ValidString(p) || p == "." || p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.ContainsAny(p, "\\:\x00<>\"|?*") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return false
		}
	}
	return true
}

func hashValid(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}

// Validate verifies paths, identities, sizes, hashes and aggregate bounds.
func (m Manifest) Validate() error {
	if _, err := ParseID(m.TransferID); err != nil {
		return err
	}
	if m.BlockSize < 64 || m.BlockSize > 2048 || len(m.Files) > 100000 {
		return ErrManifest
	}
	previous := ""
	seen := map[string]bool{}
	directories := map[string]bool{}
	var total uint64
	for i, f := range m.Files {
		if f.ID != uint32(i+1) || !ValidPath(f.Path) || f.Path <= previous || !hashValid(f.SourceHash) || !hashValid(f.EncodedHash) || f.SourceSize > math.MaxInt64-1 || f.EncodedSize > math.MaxInt64-1 {
			return ErrManifest
		}
		key := strings.ToLower(f.Path)
		if seen[key] || directories[key] {
			return ErrManifest
		}
		seen[key] = true
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return ErrManifest
			}
			directories[parent] = true
		}
		previous = f.Path
		n := uint64(0)
		if f.EncodedSize > 0 {
			n = 1 + (f.EncodedSize-1)/uint64(m.BlockSize)
		}
		if n != f.Chunks || total > math.MaxUint64-n {
			return ErrManifest
		}
		total += n
		if f.Encoding != "raw" && f.Encoding != "zstd" {
			return ErrManifest
		}
		if f.Encoding == "raw" && (f.SourceSize != f.EncodedSize || f.SourceHash != f.EncodedHash) {
			return ErrManifest
		}
		if f.SourceSize == 0 && f.Encoding != "raw" {
			return ErrManifest
		}
	}
	return nil
}

// Encode writes the normative JSON Lines sequence.
func (m Manifest) Encode() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	var total uint64
	for _, f := range m.Files {
		total += f.Chunks
	}
	if err := e.Encode(boundary{Type: "header", Format: 1, TransferID: m.TransferID, BlockSize: m.BlockSize, FileCount: uint64(len(m.Files)), Packets: total}); err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		f.Type = "file"
		if err := e.Encode(f); err != nil {
			return nil, err
		}
	}
	if err := e.Encode(boundary{Type: "end", FileCount: uint64(len(m.Files)), Packets: total}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ReadManifest bounds decompressed input and each record before parsing.
func ReadManifest(r io.Reader) (Manifest, error) {
	var m Manifest
	lr := &io.LimitedReader{R: r, N: (128 << 20) + 1}
	s := bufio.NewScanner(lr)
	s.Buffer(make([]byte, 4096), 64<<10)
	if !s.Scan() {
		return m, ErrManifest
	}
	var h boundary
	if err := json.Unmarshal(s.Bytes(), &h); err != nil {
		return m, err
	}
	if h.Type != "header" || h.Format != 1 || h.FileCount > 100000 {
		return m, ErrManifest
	}
	m.TransferID = h.TransferID
	m.BlockSize = h.BlockSize
	var total uint64
	for range h.FileCount {
		if !s.Scan() {
			return m, ErrManifest
		}
		var f File
		if err := json.Unmarshal(s.Bytes(), &f); err != nil {
			return m, err
		}
		if f.Type != "file" || total > math.MaxUint64-f.Chunks {
			return m, ErrManifest
		}
		total += f.Chunks
		m.Files = append(m.Files, f)
	}
	if !s.Scan() {
		return m, ErrManifest
	}
	var end boundary
	if err := json.Unmarshal(s.Bytes(), &end); err != nil {
		return m, err
	}
	if end.Type != "end" || end.FileCount != h.FileCount || end.Packets != total || h.Packets != total || s.Scan() || s.Err() != nil || lr.N <= 0 {
		return m, ErrManifest
	}
	return m, m.Validate()
}
