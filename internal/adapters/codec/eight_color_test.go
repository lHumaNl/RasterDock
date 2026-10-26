package codec

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"reflect"
	"testing"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func eightFixture() (v1.Descriptor, []v1.Packet) {
	d, packets := colorFixture()
	c := packets[1]
	c.FileID = 2
	c.Payload = bytes.Repeat([]byte{139}, 512)
	return d, append(packets, c)
}

func eightFrame(t *testing.T) *image.RGBA {
	t.Helper()
	d, packets := eightFixture()
	img, err := RenderPage(d, packets, application.Colors8, 0, 1, 42)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestEightFullEnvelopesAndStaleOrdinal(t *testing.T) {
	d, want := eightFixture()
	img := eightFrame(t)
	replaceBeacon(t, img, pageBeacon(d, application.Colors8, 400, 100, 42))
	b, got, err := Decode(img)
	if err != nil || !reflect.DeepEqual(got, want) || b.Descriptor != d || b.Colors != 8 || b.Ordinal != 400 {
		t.Fatalf("three full envelopes: beacon=%+v packets=%d error=%v", b, len(got), err)
	}
	if img.Bounds() != image.Rect(0, 0, 664, 460) || !fixedGeometry(b) {
		t.Fatal("geometry changed")
	}
}

func eightVectors() []color.RGBA {
	return []color.RGBA{
		{255, 255, 255, 255}, {255, 255, 0, 255}, {255, 0, 255, 255}, {255, 0, 0, 255},
		{0, 255, 255, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {0, 0, 0, 255},
	}
}

func TestEightIndependentPaletteVectors(t *testing.T) {
	img := eightFrame(t)
	for i, want := range eightVectors() {
		if got := img.RGBAAt(252+20*i, 8); got != want {
			t.Fatalf("tuple %03b: got %v want %v", i, got, want)
		}
	}
	if img.RGBAAt(400, 8) != (color.RGBA{255, 255, 255, 255}) {
		t.Fatal("strip exceeds sample/gap bounds")
	}
}

func TestEightIndependentModuleVectors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 664, 460))
	refs := []rgb{{255, 255, 255}, {255, 255, 0}, {255, 0, 255}, {255, 0, 0}, {0, 255, 255}, {0, 255, 0}, {0, 0, 255}, {0, 0, 0}}
	for i, c := range eightVectors() {
		draw.Draw(img, image.Rect(260+4*i, 56, 264+4*i, 60), image.NewUniform(c), image.Point{}, draw.Src)
	}
	planes := splitPlanes(img, captureMap{sx: 1, sy: 1}, refs, application.Colors8)
	want := [3][8]bool{{false, false, false, false, true, true, true, true}, {false, false, true, true, false, false, true, true}, {false, true, false, true, false, true, false, true}}
	for plane := range want {
		for tuple, bit := range want[plane] {
			if planes[plane].Get(tuple, 0) != bit {
				t.Fatalf("plane %d tuple %03b", plane, tuple)
			}
		}
	}
}

func TestEightBadNeighborsEachOrder(t *testing.T) {
	for bad := range 3 {
		for _, foreign := range []bool{false, true} {
			assertEightBadNeighbor(t, bad, foreign)
		}
	}
}

func assertEightBadNeighbor(t *testing.T, bad int, foreign bool) {
	t.Helper()
	d, packets := eightFixture()
	matrices, err := packetMatrices(d, packets)
	if err != nil {
		t.Fatal(err)
	}
	matrices[bad] = invalidCRCMatrix(t, packets[bad])
	if foreign {
		p := packets[bad]
		p.Transfer[0] ^= 1
		matrices[bad], err = packetMatrix(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertEightSurvivors(t, matrices, packets, bad)
}

func TestEightFinalPaddingPlanes(t *testing.T) {
	for missing := 1; missing <= 2; missing++ {
		d, packets := eightFixture()
		for i := 3 - missing; i < 3; i++ {
			packets[i] = v1.Packet{Transfer: d.Transfer}
		}
		img, err := RenderPage(d, packets, application.Colors8, 1, 2, 42)
		if err != nil {
			t.Fatal(err)
		}
		_, got, err := Decode(img)
		if err != nil || !reflect.DeepEqual(got, packets) {
			t.Fatalf("missing %d: packets=%+v error=%v", missing, got, err)
		}
	}
}

func TestEightRejectsWrongCapacityAndOversizeBlock(t *testing.T) {
	d, packets := eightFixture()
	if _, err := RenderPage(d, packets[:2], application.Colors8, 0, 1, 1); err == nil {
		t.Fatal("missing plane accepted without a padding packet")
	}
	d.Block = 513
	if _, err := RenderPage(d, packets, application.Colors8, 0, 1, 1); err == nil {
		t.Fatal("oversize block accepted")
	}
}
