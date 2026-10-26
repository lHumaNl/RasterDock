package codec

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func colorFixture() (v1.Descriptor, []v1.Packet) {
	d := v1.Descriptor{Length: 512, Block: 512}
	d.Transfer[6], d.Transfer[8] = 0x40, 0x80
	a := v1.Packet{Kind: 1, Transfer: d.Transfer, Count: 1, Size: 512, Payload: bytes.Repeat([]byte{255}, 512)}
	b := v1.Packet{Kind: 2, Transfer: d.Transfer, FileID: 1, Count: 1, Size: 512, Payload: bytes.Repeat([]byte{71}, 512)}
	return d, []v1.Packet{a, b}
}

func colorFrame(t *testing.T) *image.RGBA {
	t.Helper()
	d, p := colorFixture()
	img, err := RenderPage(d, p, application.Colors4, 0, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestFourColorFullPackets(t *testing.T) {
	img := colorFrame(t)
	d, want := colorFixture()
	b, got, err := Decode(img)
	if err != nil || b.Descriptor != d || b.Colors != 4 || len(got) != 2 {
		t.Fatalf("beacon=%+v packets=%d error=%v", b, len(got), err)
	}
	for i := range want {
		if !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Fatal("plane order/payload")
		}
	}
}

func TestForeignPlanePreservesNeighbor(t *testing.T) {
	for foreign := range 2 {
		img := colorFrame(t)
		d, packets := colorFixture()
		matrices, err := packetMatrices(d, packets)
		if err != nil {
			t.Fatal(err)
		}
		packets[foreign].Transfer[0] ^= 1
		matrices[foreign], err = packetMatrix(packets[foreign])
		if err != nil {
			t.Fatal(err)
		}
		paintPlanes(img, matrices)
		_, got, err := Decode(img)
		if err != nil || len(got) != 1 || got[0].Kind != packets[1-foreign].Kind {
			t.Fatalf("foreign plane %d: packets=%d error=%v", foreign, len(got), err)
		}
	}
}

func TestMonochromeForeignPacketFiltered(t *testing.T) {
	d, packets := colorFixture()
	img, err := Render(d, packets[0], 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.Transfer[0] ^= 1
	replaceBeacon(t, img, pageBeacon(d, application.Monochrome, 0, 1, 1))
	_, got, err := Decode(img)
	if err != nil || len(got) != 0 {
		t.Fatalf("foreign mono packet accepted: %d %v", len(got), err)
	}
}

func TestPaletteOrderIndependentVectors(t *testing.T) {
	img := colorFrame(t)
	want := []color.RGBA{{255, 255, 255, 255}, {255, 0, 0, 255}, {0, 255, 255, 255}, {0, 0, 0, 255}}
	for i, c := range want {
		if got := img.RGBAAt(252+20*i, 8); got != c {
			t.Fatalf("tuple %02b: %v", i, got)
		}
	}
}

func TestTranslatedCapture(t *testing.T) {
	src := colorFrame(t)
	dst := image.NewRGBA(image.Rect(0, 0, 900, 700))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Gray{Y: 110}), image.Point{}, draw.Src)
	draw.Draw(dst, src.Bounds().Add(image.Pt(83, 97)), src, image.Point{}, draw.Src)
	_, p, err := Decode(dst)
	if err != nil || len(p) != 2 {
		t.Fatalf("packets=%d error=%v", len(p), err)
	}
}

func TestCorruptPlanePreservesNeighbor(t *testing.T) {
	img := colorFrame(t)
	for y := 56; y < 444; y++ {
		for x := 260; x < 648; x++ {
			c := img.RGBAAt(x, y)
			c.R = 255
			img.SetRGBA(x, y, c)
		}
	}
	_, p, err := Decode(img)
	if err != nil || len(p) != 1 || p[0].Kind != 2 {
		t.Fatalf("packets=%+v error=%v", p, err)
	}
}

func TestCollapsedPaletteRejected(t *testing.T) {
	img := colorFrame(t)
	draw.Draw(img, image.Rect(244, 0, 320, 16), image.NewUniform(color.White), image.Point{}, draw.Src)
	_, _, err := Decode(img)
	if err == nil {
		t.Fatal("collapsed palette accepted")
	}
}

func TestTransformedPaletteReferences(t *testing.T) {
	img := colorFrame(t)
	transformColorRegion(img)
	refs, err := capturedPalette(img, captureMap{sx: 1, sy: 1}, application.Colors4)
	if err != nil {
		t.Fatal(err)
	}
	gap := math.Sqrt(distance(refs[0], refs[2]))
	t.Logf("transformed minimum palette gap %.2f RGB units (threshold %.0f)", gap, minimumPaletteGap)
	if gap != 127 {
		t.Fatal(gap)
	}
	_, p, err := Decode(img)
	if err != nil || len(p) != 2 {
		t.Fatalf("packets=%d error=%v", len(p), err)
	}
}

func transformColorRegion(img *image.RGBA) {
	// Channel permutation plus compression/offset defeats pristine-channel thresholds.
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 244; x < img.Bounds().Dx(); x++ {
			c := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{30 + c.G/2, 40 + c.B/2, 50 + c.R/2, 255})
		}
	}
}

func TestIndependentModuleVectors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 664, 460))
	vectors := []color.RGBA{{255, 255, 255, 255}, {255, 0, 0, 255}, {0, 255, 255, 255}, {0, 0, 0, 255}}
	refs := []rgb{{255, 255, 255}, {255, 0, 0}, {0, 255, 255}, {0, 0, 0}}
	for i, c := range vectors {
		draw.Draw(img, image.Rect(260+4*i, 56, 264+4*i, 60), image.NewUniform(c), image.Point{}, draw.Src)
	}
	planes := splitPlanes(img, captureMap{sx: 1, sy: 1}, refs, application.Colors4)
	a, b := []bool{false, false, true, true}, []bool{false, true, false, true}
	for i := range vectors {
		if planes[0].Get(i, 0) != a[i] || planes[1].Get(i, 0) != b[i] {
			t.Fatalf("tuple %02b", i)
		}
	}
}

func TestScaledWindowCapture(t *testing.T) {
	src := colorFrame(t)
	const scale = 1.25
	dst := image.NewRGBA(image.Rect(17, 23, 1000, 700))
	for y := 0; y < 575; y++ {
		for x := 0; x < 830; x++ {
			dst.Set(60+x, 80+y, src.At(int(float64(x)/scale), int(float64(y)/scale)))
		}
	}
	_, p, err := Decode(dst)
	if err != nil || len(p) != 2 {
		t.Fatalf("packets=%d error=%v", len(p), err)
	}
}

func TestCroppedColorFrameRejected(t *testing.T) {
	img := colorFrame(t)
	_, p, err := Decode(img.SubImage(image.Rect(0, 0, 600, 460)))
	if err == nil || len(p) != 0 {
		t.Fatal("cropped geometry accepted", err)
	}
}

func TestUnsupportedTransformsRejected(t *testing.T) {
	d, _ := colorFixture()
	b := pageBeacon(d, application.Colors4, 0, 1, 1)
	for _, points := range [][]gozxing.ResultPoint{
		{gozxing.NewResultPoint(30, 198), gozxing.NewResultPoint(30, 30), gozxing.NewResultPoint(198, 40)},
		{gozxing.NewResultPoint(30, 198), gozxing.NewResultPoint(30, 30), gozxing.NewResultPoint(198, 30), gozxing.NewResultPoint(180, 180)},
	} {
		if _, err := geometry(colorFrame(t), b, points); err == nil {
			t.Fatal("unsupported transform accepted")
		}
	}
}

func TestFinalPaddingIsValidQR(t *testing.T) {
	d, p := colorFixture()
	p[1] = v1.Packet{Transfer: d.Transfer}
	img, err := RenderPage(d, p, application.Colors4, 1, 2, 42)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := Decode(img)
	if err != nil || len(got) != 2 || got[1].Kind != 0 || got[1].Size != 0 || len(got[1].Payload) != 0 {
		t.Fatalf("padding: %+v %v", got, err)
	}
}

func TestUnavailableRenderProfile(t *testing.T) {
	d, p := colorFixture()
	for _, profile := range []application.ColorProfile{0, 3, 16} {
		if _, err := RenderPage(d, p, profile, 0, 1, 1); err == nil {
			t.Fatalf("accepted %d", profile)
		}
	}
}

func invalidCRCMatrix(t *testing.T, p v1.Packet) *encoder.ByteMatrix {
	t.Helper()
	raw, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	qr, err := encoder.Encoder_encode("RD1:"+v1.Encode45(raw), decoder.ErrorCorrectionLevel_M, map[gozxing.EncodeHintType]interface{}{gozxing.EncodeHintType_QR_VERSION: 20})
	if err != nil {
		t.Fatal(err)
	}
	return qr.GetMatrix()
}

func TestPlaneCRCIndependentOfNeighbor(t *testing.T) {
	img := colorFrame(t)
	d, p := colorFixture()
	matrices, err := packetMatrices(d, p)
	if err != nil {
		t.Fatal(err)
	}
	matrices[0] = invalidCRCMatrix(t, p[0])
	paintPlanes(img, matrices)
	_, got, err := Decode(img)
	if err != nil || len(got) != 1 || got[0].Kind != 2 {
		t.Fatalf("neighbor rejected: %d %v", len(got), err)
	}
}

func replaceBeacon(t *testing.T, img *image.RGBA, b v1.Beacon) {
	t.Helper()
	raw, err := b.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	draw.Draw(img, image.Rect(0, 0, 228, 228), image.NewUniform(color.White), image.Point{}, draw.Src)
	if err = paint(img, "RDH1:"+v1.Encode45(raw), 8, image.Point{}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleBeaconDoesNotRejectValidPackets(t *testing.T) {
	img := colorFrame(t)
	d, _ := colorFixture()
	replaceBeacon(t, img, pageBeacon(d, application.Colors4, 400, 100, 42))
	b, p, err := Decode(img)
	if err != nil || b.Ordinal != 400 || len(p) != 2 {
		t.Fatalf("stale ordinal rejected packets: %v", err)
	}
}

func TestUnsupportedBeaconGeometryAndProfile(t *testing.T) {
	d, _ := colorFixture()
	for _, profile := range []application.ColorProfile{application.Colors4, 8} {
		img := colorFrame(t)
		b := pageBeacon(d, profile, 0, 1, 42)
		b.GridY = 39
		replaceBeacon(t, img, b)
		_, p, err := Decode(img)
		if err == nil || len(p) != 0 {
			t.Fatal("unsupported beacon accepted")
		}
	}
}
