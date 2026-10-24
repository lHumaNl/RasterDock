package codec

import (
	"bytes"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	v1 "rasterdock/internal/protocol/v1"
)

func TestMonochromeFrame(t *testing.T) {
	d := v1.Descriptor{Length: 512, Block: 512}
	d.Transfer[6] = 0x40
	d.Transfer[8] = 0x80
	p := v1.Packet{Kind: 1, Transfer: d.Transfer, Count: 1, Size: 512, Payload: bytes.Repeat([]byte{255}, 512)}
	img, err := Render(d, p, 0, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	b, packets, err := Decode(img)
	if err != nil {
		t.Fatal(err)
	}
	if b.Descriptor != d || b.Frame != 1 || b.Epoch != 42 || len(packets) != 1 || !bytes.Equal(packets[0].Payload, p.Payload) {
		t.Fatal("frame mismatch")
	}
	// Optional developer artifact export for an independent native QR decoder.
	if dir := os.Getenv("RASTERDOCK_TEST_ARTIFACTS"); dir != "" {
		f, e := os.Create(filepath.Join(dir, "mono.png")) //nolint:gosec // Explicit developer-only test artifact directory.
		if e != nil {
			t.Fatal(e)
		}
		if e = png.Encode(f, img); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
		bw, e := b.MarshalBinary()
		if e != nil {
			t.Fatal(e)
		}
		pw, e := p.MarshalBinary()
		if e != nil {
			t.Fatal(e)
		}
		payload, e := json.Marshal([]string{"RDH1:" + v1.Encode45(bw), "RD1:" + v1.Encode45(pw)})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "mono.json"), payload, 0600); e != nil { //nolint:gosec // Explicit developer-only test artifact directory.
			t.Fatal(e)
		}
	}
}
