package v1

import "encoding/binary"

// Descriptor is immutable for the lifetime of a receive session.
type Descriptor struct {
	Transfer [16]byte
	Hash     [32]byte
	Length   uint64
	Block    uint16
}

// Validate checks bootstrap bounds before creating receive state.
func (d Descriptor) Validate() error {
	if d.Transfer[6]>>4 != 4 || d.Transfer[8]>>6 != 2 || d.Length == 0 || d.Length > 64<<20 || d.Block < 64 || d.Block > 2048 {
		return ErrPacket
	}
	return nil
}

// Beacon describes a complete visual canvas.
type Beacon struct {
	Descriptor
	Flags, Colors, Columns, Rows, Version              byte
	Active                                             uint16
	Frame, Ordinal, Nonce                              uint64
	Width, Height, GridX, GridY, CellWidth, CellHeight uint16
	Module, BeaconModule, Phase                        byte
	Epoch                                              uint64
}

func (b Beacon) valid() bool {
	if b.Validate() != nil {
		return false
	}
	return b.Length > 0 && b.Length <= 64<<20 && b.Block >= 64 && b.Block <= 2048 && b.Flags < 4 && (b.Colors == 2 || b.Colors == 4 || b.Colors == 8) && b.Columns > 0 && b.Columns <= 16 && b.Rows > 0 && b.Rows <= 16 && b.Version >= 1 && b.Version <= 40 && b.Active > 0 && b.Active <= 64 && int(b.Active) <= int(b.Columns)*int(b.Rows) && b.Module >= 2 && b.BeaconModule == 4 && b.Phase <= 2 && b.Width >= 228 && b.Height >= 228 && int(b.GridX)+int(b.Columns)*int(b.CellWidth) <= int(b.Width) && int(b.GridY)+int(b.Rows)*int(b.CellHeight) <= int(b.Height) && int(b.CellWidth) >= (17+4*int(b.Version)+8)*int(b.Module) && int(b.CellHeight) >= (17+4*int(b.Version)+8)*int(b.Module)
}

// MarshalBinary emits exactly 128 bytes.
func (b Beacon) MarshalBinary() ([]byte, error) {
	if !b.valid() {
		return nil, ErrPacket
	}
	p := make([]byte, 128)
	copy(p, "RDB1")
	p[4] = 1
	p[5] = b.Flags
	copy(p[8:], b.Transfer[:])
	copy(p[24:], b.Hash[:])
	binary.BigEndian.PutUint64(p[56:], b.Length)
	binary.BigEndian.PutUint16(p[64:], b.Block)
	p[66] = b.Colors
	p[67] = b.Columns
	p[68] = b.Rows
	p[69] = b.Version
	binary.BigEndian.PutUint16(p[70:], b.Active)
	for i, n := range []uint64{b.Frame, b.Ordinal, b.Nonce} {
		binary.BigEndian.PutUint64(p[72+i*8:], n)
	}
	for i, n := range []uint16{b.Width, b.Height, b.GridX, b.GridY, b.CellWidth, b.CellHeight} {
		binary.BigEndian.PutUint16(p[96+i*2:], n)
	}
	p[108] = b.Module
	p[109] = b.BeaconModule
	p[110] = b.Phase
	binary.BigEndian.PutUint64(p[112:], b.Epoch)
	binary.BigEndian.PutUint32(p[124:], CRC(p[:124]))
	return p, nil
}

// ParseBeacon validates reserved fields and bounded geometry before allocation.
func ParseBeacon(p []byte) (Beacon, error) {
	var b Beacon
	if len(p) != 128 || string(p[:4]) != "RDB1" || p[4] != 1 || p[6] != 0 || p[7] != 0 || p[111] != 0 || binary.BigEndian.Uint32(p[120:]) != 0 || CRC(p[:124]) != binary.BigEndian.Uint32(p[124:]) {
		return b, ErrPacket
	}
	b.Flags = p[5]
	copy(b.Transfer[:], p[8:24])
	copy(b.Hash[:], p[24:56])
	b.Length = binary.BigEndian.Uint64(p[56:])
	b.Block = binary.BigEndian.Uint16(p[64:])
	b.Colors = p[66]
	b.Columns = p[67]
	b.Rows = p[68]
	b.Version = p[69]
	b.Active = binary.BigEndian.Uint16(p[70:])
	b.Frame = binary.BigEndian.Uint64(p[72:])
	b.Ordinal = binary.BigEndian.Uint64(p[80:])
	b.Nonce = binary.BigEndian.Uint64(p[88:])
	b.Width = binary.BigEndian.Uint16(p[96:])
	b.Height = binary.BigEndian.Uint16(p[98:])
	b.GridX = binary.BigEndian.Uint16(p[100:])
	b.GridY = binary.BigEndian.Uint16(p[102:])
	b.CellWidth = binary.BigEndian.Uint16(p[104:])
	b.CellHeight = binary.BigEndian.Uint16(p[106:])
	b.Module = p[108]
	b.BeaconModule = p[109]
	b.Phase = p[110]
	b.Epoch = binary.BigEndian.Uint64(p[112:])
	if !b.valid() {
		return Beacon{}, ErrPacket
	}
	return b, nil
}
