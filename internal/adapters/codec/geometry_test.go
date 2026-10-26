package codec

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"reflect"
	"testing"

	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func geometryFixture(p application.VisualProfile) (v1.Descriptor, []v1.Packet) {
	d, base := eightFixture()
	packets := make([]v1.Packet, p.Capacity())
	for i := range packets {
		packets[i] = base[1]
		packets[i].FileID = uint32(i + 1)
	}
	return d, packets
}

func TestGeometryRoundTrips(t *testing.T) {
	for _, module := range []int{4, 6, 8} {
		for _, grid := range []image.Point{{1, 1}, {2, 2}, {3, 2}} {
			for _, colors := range []application.ColorProfile{2, 4, 8} {
				p := application.VisualProfile{Colors: colors, Geometry: application.Geometry{Module: module, Columns: grid.X, Rows: grid.Y}}
				t.Run(fmt.Sprint(p), func(t *testing.T) { assertGeometryRoundTrip(t, p) })
			}
		}
	}
}

func assertGeometryRoundTrip(t *testing.T, p application.VisualProfile) {
	t.Helper()
	d, want := geometryFixture(p)
	img, err := RenderVisualPage(d, want, p, 999, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := p.Size()
	if img.Bounds().Size() != size {
		t.Fatal(img.Bounds(), size)
	}
	b, got, err := Decode(img)
	if err != nil || !reflect.DeepEqual(got, want) || b.Ordinal != 999 || beaconProfile(b) != p {
		t.Fatalf("packets=%d: %v", len(got), err)
	}
}

func TestGeometryIndependentModuleVectors(t *testing.T) {
	for _, module := range []int{4, 6, 8} {
		assertGridVectors(t, module)
	}
}

func assertGridVectors(t *testing.T, module int) {
	t.Helper()
	p := application.VisualProfile{Colors: 8, Geometry: application.Geometry{Module: module, Columns: 3, Rows: 2}}
	size, _ := p.Size()
	img := image.NewRGBA(image.Rectangle{Max: size})
	refs := []rgb{{255, 255, 255}, {255, 255, 0}, {255, 0, 255}, {255, 0, 0}, {0, 255, 255}, {0, 255, 0}, {0, 0, 255}, {0, 0, 0}}
	for cell := range 6 {
		for tuple, c := range eightVectors() {
			// Independent wire geometry constants, not renderer-derived coordinates.
			x, y := 244+cell%3*105*module+(tuple+4)*module, 40+cell/3*105*module+4*module
			draw.Draw(img, image.Rect(x, y, x+module, y+module), image.NewUniform(c), image.Point{}, draw.Src)
		}
	}
	planes := splitGrid(img, captureMap{sx: 1, sy: 1}, refs, p)
	for i, plane := range planes {
		for tuple := range 8 {
			if plane.Get(tuple, 0) != (tuple&(1<<(2-i%3)) != 0) {
				t.Fatalf("module=%d plane=%d tuple=%d", module, i, tuple)
			}
		}
	}
}

func TestGeometryBadCellAndPlaneIndependent(t *testing.T) {
	p := application.VisualProfile{Colors: 8, Geometry: application.Geometry{Module: 6, Columns: 3, Rows: 2}}
	d, packets := geometryFixture(p)
	img, err := RenderVisualPage(d, packets, p, 999, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	matrices, err := packetMatrices(d, packets[3:6])
	if err != nil {
		t.Fatal(err)
	}
	matrices[1] = invalidCRCMatrix(t, packets[4])
	foreign := packets[5]
	foreign.Transfer[0] ^= 1
	matrices[2], err = packetMatrix(foreign)
	if err != nil {
		t.Fatal(err)
	}
	paintCell(img, matrices, image.Pt(874, 40), 6)
	draw.Draw(img, image.Rect(244, 670, 874, 1300), image.NewUniform(color.White), image.Point{}, draw.Src)
	want := append([]v1.Packet{}, packets[:4]...)
	want = append(want, packets[6:9]...)
	want = append(want, packets[12:]...)
	_, got, err := Decode(img)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("survivors=%d want=%d err=%v", len(got), len(want), err)
	}
}

func TestGeometryTranslatedScaled(t *testing.T) {
	for _, module := range []int{4, 6, 8} {
		p := application.VisualProfile{Colors: 8, Geometry: application.Geometry{Module: module, Columns: 2, Rows: 2}}
		d, want := geometryFixture(p)
		src, err := RenderVisualPage(d, want, p, 0, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, scale := range []float64{0.75, 1.25} {
			dst := geometryCapture(src, scale)
			_, got, err := Decode(dst)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("module=%d scale=%g packets=%d err=%v", module, scale, len(got), err)
			}
		}
	}
}

func geometryCapture(src image.Image, scale float64) *image.RGBA {
	width, height := int(float64(src.Bounds().Dx())*scale), int(float64(src.Bounds().Dy())*scale)
	dst := image.NewRGBA(image.Rect(-37, 23, width+100, height+120))
	for y := range height {
		for x := range width {
			dst.Set(60+x, 80+y, src.At(int(float64(x)/scale), int(float64(y)/scale)))
		}
	}
	return dst
}

func TestGeometryMalformedBeacon(t *testing.T) {
	d, _ := eightFixture()
	original := pageBeacon(d, application.Colors8, 0, 1, 1)
	changes := []func(*v1.Beacon){func(b *v1.Beacon) { b.GridX = 0 }, func(b *v1.Beacon) { b.Module = 255 }, func(b *v1.Beacon) { b.Columns = 255 }, func(b *v1.Beacon) { b.Width = 65535 }, func(b *v1.Beacon) { b.Active = 0 }, func(b *v1.Beacon) { b.CellHeight++ }, func(b *v1.Beacon) { b.Rows = 2 }, func(b *v1.Beacon) { b.Version = 40 }}
	for _, change := range changes {
		b := original
		change(&b)
		if fixedGeometry(b) {
			t.Fatalf("accepted %+v", b)
		}
	}
}

func TestGeometryLegacyIdenticalPixels(t *testing.T) {
	// SHA-256 of raw RGBA Pix, generated by RenderPage in an isolated git archive
	// of b69efb9bc2de1def46163ac0d345d023cd8d9b63 (pre-geometry), not this renderer.
	// Fixture: colorFixture's data packet repeated with FileID=1..capacity,
	// ordinal=1, frame=2, epoch=3. Covers these default frames, not every payload.
	goldens := map[application.ColorProfile]string{
		2: "30dedd5946cab03478a3bed2582b5c41b9191e021d5b1e15a2c065d81d20b462",
		4: "db59f8152ca7a907431cbff013ba135b12a3942ea9466b47a8f55a02a678f4f5",
		8: "2de2b9e43a44eb61727ce5abb67e6fc4865678bfa077d0109f315ab8db98b054",
	}
	for colors, golden := range goldens {
		p := application.DefaultVisualProfile(colors)
		d, packets := geometryFixture(p)
		current, err := RenderVisualPage(d, packets, p, 1, 2, 3)
		if err != nil {
			t.Fatal(err)
		}
		if current.Bounds() != image.Rect(0, 0, 664, 460) || fmt.Sprintf("%x", sha256.Sum256(current.Pix)) != golden {
			t.Fatal("legacy pixels changed", err)
		}
	}
}

func TestGeometryFinalPaddingCells(t *testing.T) {
	p := application.VisualProfile{Colors: 8, Geometry: application.Geometry{Module: 4, Columns: 3, Rows: 2}}
	for _, remaining := range []int{1, 2, 3, 4, 17} {
		d, packets := geometryFixture(p)
		for i := remaining; i < len(packets); i++ {
			packets[i] = v1.Packet{Transfer: d.Transfer}
		}
		img, err := RenderVisualPage(d, packets, p, 0, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		_, got, err := Decode(img)
		if err != nil || !reflect.DeepEqual(got, packets) {
			t.Fatalf("tail=%d got=%d err=%v", remaining, len(got), err)
		}
	}
}

func TestGeometryDecodeRejectsOverlappingAndHugeBeacon(t *testing.T) {
	d, _ := eightFixture()
	for _, change := range []func(*v1.Beacon){func(b *v1.Beacon) { b.GridX = 0 }, func(b *v1.Beacon) { b.Width = 65535; b.Height = 65535 }} {
		img := eightFrame(t)
		b := pageBeacon(d, application.Colors8, 0, 1, 1)
		change(&b)
		replaceBeacon(t, img, b)
		if _, packets, err := Decode(img); err == nil || len(packets) != 0 {
			t.Fatal("malformed geometry decoded packets", err)
		}
	}
}

func TestGeometryRenderRejectsUnboundedProfile(t *testing.T) {
	d, packets := eightFixture()
	p := application.VisualProfile{Colors: 8, Geometry: application.Geometry{Module: int(^uint(0) >> 1), Columns: 3, Rows: 2}}
	if img, err := RenderVisualPage(d, packets, p, 0, 1, 1); err == nil || img != nil {
		t.Fatal("unbounded geometry allocated canvas")
	}
}

func TestGeometryBlockInvalidPacketIndependent(t *testing.T) {
	for _, colors := range []application.ColorProfile{2, 4, 8} {
		p := application.VisualProfile{Colors: colors, Geometry: application.Geometry{Module: 4, Columns: 2, Rows: 2}}
		d, packets := geometryFixture(p)
		img, err := RenderVisualPage(d, packets, p, 999, 1, 42)
		if err != nil {
			t.Fatal(err)
		}
		bad := packets[1]
		bad.Count++ // Structurally valid and CRC-valid, but inconsistent with B=512.
		raw, err := bad.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = v1.ParsePacket(raw); err != nil {
			t.Fatal(err)
		}
		if bad.ValidateBlock(d.Block) == nil {
			t.Fatal("fixture is block-valid")
		}
		packets[1] = bad
		repaintUnchecked(t, img, packets, p)
		_, got, err := Decode(img)
		want := append(append([]v1.Packet{}, packets[:1]...), packets[2:]...)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("colors=%d survivors=%d want=%d err=%v", colors, len(got), len(want), err)
		}
	}
}

func repaintUnchecked(t *testing.T, img *image.RGBA, packets []v1.Packet, p application.VisualProfile) {
	t.Helper()
	capacity := len(packets) / (p.Geometry.Columns * p.Geometry.Rows)
	for cell := 0; cell < p.Geometry.Columns*p.Geometry.Rows; cell++ {
		matrices := make([]*encoder.ByteMatrix, capacity)
		for plane := range capacity {
			var err error
			matrices[plane], err = packetMatrix(packets[cell*capacity+plane])
			if err != nil {
				t.Fatal(err)
			}
		}
		paintCell(img, matrices, image.Pt(gridX+cell%p.Geometry.Columns*p.Geometry.Pitch(), gridY+cell/p.Geometry.Columns*p.Geometry.Pitch()), p.Geometry.Module)
	}
}

func TestGeometryLegacyMonoFiltersBlockInvalidPacket(t *testing.T) {
	p := application.DefaultVisualProfile(application.Monochrome)
	d, packets := geometryFixture(p)
	img, err := RenderVisualPage(d, packets, p, 999, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	packets[0].Payload = packets[0].Payload[:1]
	repaintUnchecked(t, img, packets, p)
	_, got, err := Decode(img)
	if err != nil || len(got) != 0 {
		t.Fatal("legacy mono returned block-invalid packet", len(got), err)
	}
}
