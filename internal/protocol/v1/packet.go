// Package v1 implements RasterDock's normative binary visual protocol.
package v1

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:"

// ErrPacket indicates malformed or unsupported wire data.
var ErrPacket = errors.New("invalid protocol v1 packet")

// Encode45 implements RFC 9285 without whitespace normalization.
func Encode45(b []byte) string {
	var s strings.Builder
	for len(b) > 0 {
		n := int(b[0])
		width := 2
		if len(b) > 1 {
			n = n*256 + int(b[1])
			width = 3
			b = b[2:]
		} else {
			b = b[1:]
		}
		for range width {
			s.WriteByte(alphabet[n%45])
			n /= 45
		}
	}
	return s.String()
}

// Decode45 rejects non-alphabet characters and overflowing groups.
func Decode45(s string) ([]byte, error) {
	if len(s)%3 == 1 {
		return nil, ErrPacket
	}
	out := make([]byte, 0, len(s)*2/3)
	for len(s) > 0 {
		width := min(3, len(s))
		n, m := 0, 1
		for i := range width {
			v := strings.IndexByte(alphabet, s[i])
			if v < 0 {
				return nil, ErrPacket
			}
			n += v * m
			m *= 45
		}
		if width == 3 {
			if n > 65535 {
				return nil, ErrPacket
			}
			out = append(out, byte(n/256), byte(n%256))
		} else {
			if n > 255 {
				return nil, ErrPacket
			}
			out = append(out, byte(n))
		}
		s = s[width:]
	}
	return out, nil
}

// CRC is the Castagnoli checksum used by packets and beacons.
func CRC(b []byte) uint32 { return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)) }

// Packet owns its payload; Index is distinct from a viewer frame ID.
type Packet struct {
	Kind               byte
	Transfer           [16]byte
	FileID             uint32
	Index, Count, Size uint64
	Payload            []byte
}

func (p Packet) valid() bool {
	if p.Transfer[6]>>4 != 4 || p.Transfer[8]>>6 != 2 {
		return false
	}
	if p.Kind > 2 || len(p.Payload) > 2048 {
		return false
	}
	if p.Kind == 0 {
		return p.FileID == 0 && p.Index == 0 && p.Count == 0 && p.Size == 0 && len(p.Payload) == 0
	}
	return (p.Kind != 1 || p.FileID == 0) && (p.Kind != 2 || p.FileID > 0) && p.Count > 0 && p.Index < p.Count && p.Size > 0 && len(p.Payload) > 0
}

// ValidateBlock checks offsets without overflowing a multiplication.
func (p Packet) ValidateBlock(block uint16) error {
	if !p.valid() || block < 64 || block > 2048 {
		return ErrPacket
	}
	if p.Kind == 0 {
		return nil
	}
	b := uint64(block)
	if p.Count != 1+(p.Size-1)/b {
		return ErrPacket
	}
	expected := b
	if p.Index == p.Count-1 {
		expected = 1 + (p.Size-1)%b
	}
	if uint64(len(p.Payload)) != expected {
		return ErrPacket
	}
	return nil
}

// MarshalBinary serializes the fixed 56-byte header and trailing CRC32C.
func (p Packet) MarshalBinary() ([]byte, error) {
	if !p.valid() {
		return nil, ErrPacket
	}
	b := make([]byte, 60+len(p.Payload))
	copy(b, "RDKF")
	b[4] = 1
	b[5] = p.Kind
	copy(b[8:24], p.Transfer[:])
	binary.BigEndian.PutUint32(b[24:], p.FileID)
	binary.BigEndian.PutUint64(b[28:], p.Index)
	binary.BigEndian.PutUint64(b[36:], p.Count)
	binary.BigEndian.PutUint64(b[44:], p.Size)
	binary.BigEndian.PutUint16(b[52:], uint16(len(p.Payload))) //nolint:gosec // valid() bounds payload to 2048 bytes.
	binary.BigEndian.PutUint16(b[54:], 56)
	copy(b[56:], p.Payload)
	binary.BigEndian.PutUint32(b[len(b)-4:], CRC(b[:len(b)-4]))
	return b, nil
}

// ParsePacket returns a packet with a private payload copy.
func ParsePacket(b []byte) (Packet, error) {
	var p Packet
	if len(b) < 60 || string(b[:4]) != "RDKF" || b[4] != 1 || b[6] != 0 || b[7] != 0 || binary.BigEndian.Uint16(b[54:]) != 56 || int(binary.BigEndian.Uint16(b[52:])) != len(b)-60 || CRC(b[:len(b)-4]) != binary.BigEndian.Uint32(b[len(b)-4:]) {
		return p, ErrPacket
	}
	p.Kind = b[5]
	copy(p.Transfer[:], b[8:24])
	p.FileID = binary.BigEndian.Uint32(b[24:])
	p.Index = binary.BigEndian.Uint64(b[28:])
	p.Count = binary.BigEndian.Uint64(b[36:])
	p.Size = binary.BigEndian.Uint64(b[44:])
	p.Payload = append([]byte(nil), b[56:len(b)-4]...)
	if !p.valid() {
		return Packet{}, ErrPacket
	}
	return p, nil
}
