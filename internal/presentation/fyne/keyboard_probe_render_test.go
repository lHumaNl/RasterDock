package fyneui

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

// Test-only lifecycle metadata patch; production data pixels are unchanged.
func probePatchBeacon(base *image.RGBA, b v1.Beacon) (*image.RGBA, error) {
	raw, err := b.MarshalBinary()
	if err != nil {
		return nil, err
	}
	qr, err := encoder.Encoder_encode("RDH1:"+v1.Encode45(raw), decoder.ErrorCorrectionLevel_M, map[gozxing.EncodeHintType]interface{}{gozxing.EncodeHintType_QR_VERSION: 8})
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(base.Bounds())
	draw.Draw(out, out.Bounds(), base, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(0, 0, 228, 228), image.NewUniform(color.White), image.Point{}, draw.Src)
	m := qr.GetMatrix()
	for y := 0; y < m.GetHeight(); y++ {
		for x := 0; x < m.GetWidth(); x++ {
			if m.Get(x, y) == 1 {
				draw.Draw(out, image.Rect((x+4)*4, (y+4)*4, (x+5)*4, (y+5)*4), image.NewUniform(color.Black), image.Point{}, draw.Src)
			}
		}
	}
	return out, nil
}

func TestKeyboardProbeBeaconPatchPreservesProductionPlanes(t *testing.T) {
	d := v1.Descriptor{Length: 1, Block: 512}
	d.Transfer[6] = 0x40
	d.Transfer[8] = 0x80
	profile := application.VisualProfile{Colors: application.Colors8, Geometry: application.Geometry{Module: 4, Columns: 2, Rows: 2}}
	packets := make([]v1.Packet, profile.Capacity())
	for i := range packets {
		packets[i] = v1.Packet{Transfer: d.Transfer, Kind: 2, FileID: 1, Index: uint64(i), Count: uint64(len(packets)), Size: uint64(len(packets)) * 512, Payload: bytes.Repeat([]byte{byte(i)}, 512)}
	}
	base, err := codec.RenderVisualPage(d, packets, profile, 0, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := codec.Decode(base)
	if err != nil {
		t.Fatal(err)
	}
	b.Flags = 3
	b.Nonce = 42
	b.Frame = 99
	patched, err := probePatchBeacon(base, b)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < base.Bounds().Dy(); y++ {
		for x := 0; x < base.Bounds().Dx(); x++ {
			if x >= 228 || y >= 228 {
				if base.RGBAAt(x, y) != patched.RGBAAt(x, y) {
					t.Fatal("patch modified data/palette")
				}
			}
		}
	}
	got, decoded, err := codec.Decode(patched)
	if err != nil || got != b || len(decoded) != len(packets) {
		t.Fatalf("patched decode b=%+v count=%d err=%v", got, len(decoded), err)
	}
	for i, p := range decoded {
		a, _ := p.MarshalBinary()
		w, _ := packets[i].MarshalBinary()
		if !bytes.Equal(a, w) {
			t.Fatalf("plane %d changed", i)
		}
	}
}
