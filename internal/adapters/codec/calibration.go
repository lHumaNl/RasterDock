package codec

import (
	"errors"
	"image"
	"math"

	"github.com/makiuchi-d/gozxing"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

// ErrCalibration is actionable, unlike transient missing-QR decode errors.
var ErrCalibration = errors.New("experimental color calibration incompatible: use monochrome or check capture size and color settings")

// Experimental SDR metric, not a protocol requirement: Euclidean RGB separation
// must be >=64/255. Scale must preserve >=3 captured pixels/module; rotation,
// skew and perspective are not supported. Tolerances absorb finder rounding only.
const (
	minimumPaletteGap = 64.0
	minimumScale      = 0.75
	maximumScale      = 4.0
	axisTolerance     = 0.01
	scaleTolerance    = 0.02
	finderOrigin      = 30.0
	finderSpacing     = 168.0
	alignmentCenter   = 186.0
)

type captureMap struct{ x, y, sx, sy float64 }
type rgb [3]float64

func fixedGeometry(b v1.Beacon) bool {
	p := beaconProfile(b)
	size, err := p.Size()
	return err == nil && fixedGrid(b) && b.Version == dataVersion && int(b.Width) == size.X && int(b.Height) == size.Y && b.BeaconModule == moduleSize && b.Block <= maxBlock
}

func fixedGrid(b v1.Beacon) bool {
	g := beaconProfile(b).Geometry
	return g.Pitch() != 0 && int(b.Active) == g.Columns*g.Rows && b.GridX == gridX && b.GridY == gridY && int(b.CellWidth) == g.Pitch() && int(b.CellHeight) == g.Pitch()
}

func beaconProfile(b v1.Beacon) application.VisualProfile {
	return application.VisualProfile{Colors: application.ColorProfile(b.Colors), Geometry: application.Geometry{Module: int(b.Module), Columns: int(b.Columns), Rows: int(b.Rows)}}
}

func geometry(img image.Image, b v1.Beacon, p []gozxing.ResultPoint) (captureMap, error) {
	if !fixedGeometry(b) || len(p) < 3 {
		return captureMap{}, ErrCalibration
	}
	tl, tr, bl := p[1], p[2], p[0]
	sx, sy := (tr.GetX()-tl.GetX())/finderSpacing, (bl.GetY()-tl.GetY())/finderSpacing
	if sx < minimumScale || sy < minimumScale || sx > maximumScale || sy > maximumScale || math.Abs(sx-sy) > scaleTolerance*sx {
		return captureMap{}, ErrCalibration
	}
	if math.Abs(tr.GetY()-tl.GetY()) > axisTolerance*finderSpacing*sy || math.Abs(bl.GetX()-tl.GetX()) > axisTolerance*finderSpacing*sx {
		return captureMap{}, ErrCalibration
	}
	m := captureMap{tl.GetX() - finderOrigin*sx + float64(img.Bounds().Min.X), tl.GetY() - finderOrigin*sy + float64(img.Bounds().Min.Y), sx, sy}
	if !m.inCanvas(img.Bounds(), int(b.Width), int(b.Height)) || !m.alignmentValid(p, img.Bounds().Min) {
		return captureMap{}, ErrCalibration
	}
	return m, nil
}

func (m captureMap) inCanvas(bounds image.Rectangle, width, height int) bool {
	return m.x >= float64(bounds.Min.X)-0.5 && m.y >= float64(bounds.Min.Y)-0.5 && m.x+float64(width)*m.sx <= float64(bounds.Max.X)+0.5 && m.y+float64(height)*m.sy <= float64(bounds.Max.Y)+0.5
}

func (m captureMap) alignmentValid(points []gozxing.ResultPoint, origin image.Point) bool {
	if len(points) < 4 {
		return false
	}
	dx := points[3].GetX() + float64(origin.X) - (m.x + alignmentCenter*m.sx)
	dy := points[3].GetY() + float64(origin.Y) - (m.y + alignmentCenter*m.sy)
	return math.Abs(dx) <= m.sx && math.Abs(dy) <= m.sy
}

func (m captureMap) sample(img image.Image, x, y float64) rgb {
	var out rgb
	// Four samples inside the central half of a source module avoid boundary bleed.
	for _, dy := range []float64{-0.5, 0.5} {
		for _, dx := range []float64{-0.5, 0.5} {
			r, g, b, _ := img.At(int(math.Floor(m.x+(x+dx)*m.sx)), int(math.Floor(m.y+(y+dy)*m.sy))).RGBA()
			out[0] += float64(r) / 257 / 4
			out[1] += float64(g) / 257 / 4
			out[2] += float64(b) / 257 / 4
		}
	}
	return out
}

func distance(a, b rgb) float64 {
	dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dr*dr + dg*dg + db*db
}

func capturedPalette(img image.Image, m captureMap, profile application.ColorProfile) ([]rgb, error) {
	if profile.Capacity() == 0 {
		return nil, application.ErrProfile
	}
	refs := make([]rgb, int(profile))
	for i := range refs {
		refs[i] = m.sample(img, float64(gridX+i*(paletteSize+paletteGap)+paletteSize/2), paletteSize/2)
	}
	for i, reference := range refs {
		for j, other := range refs {
			if j < i && distance(reference, other) < minimumPaletteGap*minimumPaletteGap {
				return refs, ErrCalibration
			}
		}
	}
	return refs, nil
}

func nearest(value rgb, refs []rgb) int {
	best := 0
	for i := 1; i < len(refs); i++ {
		if distance(value, refs[i]) < distance(value, refs[best]) {
			best = i
		}
	}
	return best
}
