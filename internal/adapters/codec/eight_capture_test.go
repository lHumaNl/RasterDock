package codec

import (
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func assertEightSurvivors(t *testing.T, matrices []*encoder.ByteMatrix, packets []v1.Packet, bad int) {
	t.Helper()
	img := eightFrame(t)
	paintPlanes(img, matrices)
	_, got, err := Decode(img)
	want := append(append([]v1.Packet{}, packets[:bad]...), packets[bad+1:]...)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("bad plane %d: packets=%d error=%v", bad, len(got), err)
	}
}

func TestEightCapturedReferenceBoundaries(t *testing.T) {
	for _, gap := range []uint8{0, 63, 64, 255} {
		img := eightFrame(t)
		compressEightRed(img, gap)
		_, got, err := Decode(img)
		if gap < 64 && (!errors.Is(err, ErrCalibration) || len(got) != 0) {
			t.Fatalf("collapsed gap %d: %v", gap, err)
		}
		if gap >= 64 && (err != nil || len(got) != 3) {
			t.Fatalf("usable gap %d: packets=%d error=%v", gap, len(got), err)
		}
	}
}

func compressEightRed(img *image.RGBA, gap uint8) {
	for y := range img.Bounds().Dy() {
		for x := gridX; x < img.Bounds().Max.X; x++ {
			c := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{c.R / 255 * gap, c.G, c.B, 255})
		}
	}
}

func TestEightMeasuredNotPristineReferences(t *testing.T) {
	img := eightFrame(t)
	transformColorRegion(img)
	_, want := eightFixture()
	_, got, err := Decode(img)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("measured palette: packets=%d error=%v", len(got), err)
	}
}

func TestEightTranslatedScaledNonzeroOrigin(t *testing.T) {
	for _, scale := range []float64{0.75, 1, 1.25} {
		img := scaledEightCapture(t, scale)
		_, got, err := Decode(img)
		_, want := eightFixture()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("scale %v: packets=%d error=%v", scale, len(got), err)
		}
	}
}

func scaledEightCapture(t *testing.T, scale float64) *image.RGBA {
	t.Helper()
	src := eightFrame(t)
	dst := image.NewRGBA(image.Rect(-37, 23, 1000, 800))
	for y := 0; y < int(canvasHeight*scale); y++ {
		for x := 0; x < int(canvasWidth*scale); x++ {
			dst.Set(60+x, 80+y, src.At(int(float64(x)/scale), int(float64(y)/scale)))
		}
	}
	return dst
}

func TestEightUnsafeGeometryRejected(t *testing.T) {
	d, _ := eightFixture()
	img := eightFrame(t)
	b := pageBeacon(d, application.Colors8, 0, 1, 1)
	if _, err := geometry(img, b, boundaryPoints(0.749)); !errors.Is(err, ErrCalibration) {
		t.Fatal("undersampling accepted", err)
	}
	_, packets, err := Decode(img.SubImage(image.Rect(0, 0, 600, 460)))
	if !errors.Is(err, ErrCalibration) || len(packets) != 0 {
		t.Fatal("cropped eight-color capture accepted", err)
	}
}

func TestEightMixedBadNeighborsEachOrder(t *testing.T) {
	for foreign := range 3 {
		for corrupt := range 3 {
			if foreign != corrupt {
				assertEightMixedNeighbors(t, foreign, corrupt)
			}
		}
	}
}

func mixedEightMatrices(t *testing.T, foreign, corrupt int) []*encoder.ByteMatrix {
	t.Helper()
	d, packets := eightFixture()
	matrices, err := packetMatrices(d, packets)
	if err != nil {
		t.Fatal(err)
	}
	matrices[corrupt] = invalidCRCMatrix(t, packets[corrupt])
	packets[foreign].Transfer[0] ^= 1
	matrices[foreign], err = packetMatrix(packets[foreign])
	if err != nil {
		t.Fatal(err)
	}
	return matrices
}

func assertEightMixedNeighbors(t *testing.T, foreign, corrupt int) {
	t.Helper()
	img := eightFrame(t)
	d, packets := eightFixture()
	replaceBeacon(t, img, pageBeacon(d, application.Colors8, 400, 100, 42))
	paintPlanes(img, mixedEightMatrices(t, foreign, corrupt))
	_, got, err := Decode(img)
	want := []v1.Packet{packets[3-foreign-corrupt]}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign=%d CRC=%d: packets=%d error=%v", foreign, corrupt, len(got), err)
	}
}
