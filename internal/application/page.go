package application

import (
	"errors"

	v1 "rasterdock/internal/protocol/v1"
)

// ColorProfile changes visual capacity, never the prepared descriptor or packet identities.
type ColorProfile byte

// Supported runtime-only profiles; no persisted schema is added.
const (
	Monochrome ColorProfile = 2
	Colors4    ColorProfile = 4 // Experimental until native channel calibration is validated.
	Colors8    ColorProfile = 8 // Experimental; requires independently distinguishable RGB references.
)

// ErrProfile rejects unavailable profiles without changing prepared state.
var ErrProfile = errors.New("unsupported visual profile (only monochrome and experimental four/eight colors are available)")

// Capacity is the number of independent QR planes in a physical page.
func (p ColorProfile) Capacity() uint64 {
	switch p {
	case Monochrome:
		return 1
	case Colors4:
		return 2
	case Colors8:
		return 3
	default:
		return 0
	}
}

// Page reads consecutive logical packets, filling the final plane with valid padding.
func (p *Prepared) Page(ordinal uint64, profile ColorProfile) ([]v1.Packet, error) {
	capacity := profile.Capacity()
	if capacity == 0 {
		return nil, ErrProfile
	}
	if ordinal >= p.PacketCount() {
		return nil, v1.ErrPacket
	}
	packets := make([]v1.Packet, capacity)
	for i := range packets {
		packet, err := p.packetOrPadding(ordinal + uint64(i))
		if err != nil {
			return nil, err
		}
		packets[i] = packet
	}
	return packets, nil
}

func (p *Prepared) packetOrPadding(ordinal uint64) (v1.Packet, error) {
	if ordinal >= p.PacketCount() {
		return v1.Packet{Transfer: p.Descriptor.Transfer}, nil
	}
	return p.Packet(ordinal)
}
