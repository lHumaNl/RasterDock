package codec

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/makiuchi-d/gozxing"

	"rasterdock/internal/application"
)

func TestPaletteSeparationBoundary(t *testing.T) {
	for _, gap := range []uint8{63, 64} {
		img := colorFrame(t)
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := gridX; x < img.Bounds().Dx(); x++ {
				c := img.RGBAAt(x, y)
				img.SetRGBA(x, y, color.RGBA{c.R / 255 * gap, c.G, c.B, 255})
			}
		}
		_, packets, err := Decode(img)
		if gap == 63 && (!errors.Is(err, ErrCalibration) || len(packets) != 0) {
			t.Fatalf("below gap: %v", err)
		}
		if gap == 64 && (err != nil || len(packets) != 2) {
			t.Fatalf("at gap: %v packets=%d", err, len(packets))
		}
	}
}

func boundaryPoints(scale float64) []gozxing.ResultPoint {
	return []gozxing.ResultPoint{
		gozxing.NewResultPoint(finderOrigin*scale, (finderOrigin+finderSpacing)*scale),
		gozxing.NewResultPoint(finderOrigin*scale, finderOrigin*scale),
		gozxing.NewResultPoint((finderOrigin+finderSpacing)*scale, finderOrigin*scale),
		gozxing.NewResultPoint(alignmentCenter*scale, alignmentCenter*scale),
	}
}

func TestMinimumScaleBoundary(t *testing.T) {
	d, _ := colorFixture()
	b := pageBeacon(d, application.Colors4, 0, 1, 1)
	for _, scale := range []float64{0.749, 0.75} {
		_, err := geometry(colorFrame(t), b, boundaryPoints(scale))
		if scale < 0.75 && !errors.Is(err, ErrCalibration) {
			t.Fatalf("below scale: %v", err)
		}
		if scale == 0.75 && err != nil {
			t.Fatalf("at scale: %v", err)
		}
	}
}

func TestNonzeroOriginSampling(t *testing.T) {
	src := colorFrame(t)
	origin := image.Pt(-37, 53)
	dst := image.NewRGBA(src.Bounds().Add(origin))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	d, _ := colorFixture()
	m, err := geometry(dst, pageBeacon(d, application.Colors4, 0, 1, 1), boundaryPoints(1))
	if err != nil {
		t.Fatal(err)
	}
	if m.sample(dst, gridX+paletteSize/2, paletteSize/2) != (rgb{255, 255, 255}) {
		t.Fatal("origin omitted from sample")
	}
	_, packets, err := Decode(dst)
	if err != nil || len(packets) != 2 {
		t.Fatalf("translated bounds: %v packets=%d", err, len(packets))
	}
}

func TestMinimumScaleCaptureDecodes(t *testing.T) {
	src := colorFrame(t)
	const scale = 0.75
	origin := image.Pt(17, 23)
	dst := image.NewRGBA(image.Rect(0, 0, int(canvasWidth*scale), int(canvasHeight*scale)).Add(origin))
	for y := range dst.Bounds().Dy() {
		for x := range dst.Bounds().Dx() {
			dst.Set(origin.X+x, origin.Y+y, src.At(int(float64(x)/scale), int(float64(y)/scale)))
		}
	}
	_, packets, err := Decode(dst)
	if err != nil || len(packets) != 2 {
		t.Fatalf("minimum capture scale: %v packets=%d", err, len(packets))
	}
}
