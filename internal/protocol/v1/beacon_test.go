package v1

import (
	"encoding/hex"
	"testing"
)

func TestBeacon(t *testing.T) {
	b := Beacon{Descriptor: Descriptor{Length: 123, Block: 64}, Colors: 2, Columns: 1, Rows: 1, Version: 8, Active: 1, Width: 456, Height: 456, GridY: 228, CellWidth: 228, CellHeight: 228, Module: 4, BeaconModule: 4}
	b.Transfer[6] = 0x40
	b.Transfer[8] = 0x80
	wire, err := b.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// Independent Python struct.pack + bitwise CRC32C fixture, normative offsets.
	const golden = "5244423101000000000000000000400080000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000007b004002010108000100000000000000000000000000000000000000000000000001c801c8000000e400e400e404040000000000000000000000000000f90a10e7"
	if hex.EncodeToString(wire) != golden {
		t.Fatalf("beacon golden mismatch: %x", wire)
	}
	if len(wire) != 128 || len("RDH1:"+Encode45(wire)) != 197 {
		t.Fatal("beacon capacity contract")
	}
	got, err := ParseBeacon(wire)
	if err != nil || got != b {
		t.Fatal(got, err)
	}
	wire[111] = 1
	if _, err = ParseBeacon(wire); err == nil {
		t.Fatal("reserved accepted")
	}
	b.Length = 0
	if _, err = b.MarshalBinary(); err == nil {
		t.Fatal("empty manifest accepted")
	}
	b.Length = 123
	b.Transfer = [16]byte{}
	if _, err = b.MarshalBinary(); err == nil {
		t.Fatal("non-UUIDv4 transfer accepted")
	}
}
