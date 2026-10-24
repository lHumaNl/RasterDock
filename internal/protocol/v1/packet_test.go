package v1

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestBase45RFC(t *testing.T) {
	for plain, wire := range map[string]string{"AB": "BB8", "Hello!!": "%69 VD92EX0", "base-45": "UJCLQE7W581"} {
		if got := Encode45([]byte(plain)); got != wire {
			t.Fatalf("%q != %q", got, wire)
		}
		got, err := Decode45(wire)
		if err != nil || string(got) != plain {
			t.Fatalf("%q: %v", got, err)
		}
	}
	for _, s := range []string{"A", ":::xyz", "ZZZ", "//"} {
		if _, err := Decode45(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestPacketWireAndValidation(t *testing.T) {
	p := Packet{Kind: 2, FileID: 1, Index: 0, Count: 1, Size: 3, Payload: []byte{0, 255, 128}}
	p.Transfer[6] = 0x40
	p.Transfer[8] = 0x80
	b, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// Header independently transcribed from normative offsets; CRC tested with published Castagnoli vector below.
	want, _ := hex.DecodeString("52444b4601020000000000000000400080000000000000000000000100000000000000000000000000000001000000000000000300030038")
	if !bytes.Equal(b[:56], want) || len(b) != 63 {
		t.Fatalf("header: %x", b)
	}
	got, err := ParsePacket(b)
	if err != nil || !bytes.Equal(got.Payload, p.Payload) {
		t.Fatal(got, err)
	}
	if err := got.ValidateBlock(64); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		bad := bytes.Clone(b)
		bad[i] ^= 1
		if _, err := ParsePacket(bad); err == nil {
			t.Fatalf("corruption accepted at %d", i)
		}
	}
	if CRC([]byte("123456789")) != 0xe3069283 {
		t.Fatal("CRC32C check vector")
	}
	p.Count = 2
	if err := p.ValidateBlock(64); err == nil {
		t.Fatal("invalid count")
	}
}

// Generated independently with Python struct.pack and a bitwise reflected
// Castagnoli polynomial (0x82f63b78), not this implementation's encoder/CRC.
func TestPacketGoldenVectors(t *testing.T) {
	vectors := []string{
		"52444b46010000000000000000004000800000000000000000000000000000000000000000000000000000000000000000000000000000383c3ce82e",
		"52444b460101000000000000000040008000000000000000000000000000000000000000000000000000000100000000000000030003003800ff80c527e2b9",
		"52444b460102000000000000000040008000000000000000000000010000000000000000000000000000000100000000000000030003003800ff80e7b7e989",
	}
	for kind, vector := range vectors {
		wire, err := hex.DecodeString(vector)
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParsePacket(wire)
		if err != nil {
			t.Fatal(err)
		}
		if int(p.Kind) != kind {
			t.Fatal("kind")
		}
		encoded, err := p.MarshalBinary()
		if err != nil || !bytes.Equal(encoded, wire) {
			t.Fatal("wire mismatch", err)
		}
		if err = p.ValidateBlock(64); err != nil {
			t.Fatal(err)
		}
	}
}
