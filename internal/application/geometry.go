package application

import "image"

// Fixed protocol-v1 geometry in physical pixels. Only data modules vary.
const (
	DataModules  = 97
	QuietModules = 4
	GridX        = 244
	GridY        = 40
	BeaconSize   = 228
)

// Geometry is a bounded runtime selection, never persisted with a transfer.
type Geometry struct{ Module, Columns, Rows int }

// VisualProfile keeps color and spatial capacity independent.
type VisualProfile struct {
	Colors   ColorProfile
	Geometry Geometry
}

// DefaultVisualProfile preserves the original single-cell module-four APIs.
func DefaultVisualProfile(colors ColorProfile) VisualProfile {
	return VisualProfile{Colors: colors, Geometry: Geometry{4, 1, 1}}
}

func (g Geometry) valid() bool {
	module := g.Module == 4 || g.Module == 6 || g.Module == 8
	grid := g.Columns == 1 && g.Rows == 1 || g.Columns == 2 && g.Rows == 2 || g.Columns == 3 && g.Rows == 2
	return module && grid
}

// Capacity returns zero for invalid input, before multiplication or allocation.
func (p VisualProfile) Capacity() uint64 {
	if !p.Geometry.valid() {
		return 0
	}
	return uint64(p.Geometry.Columns*p.Geometry.Rows) * p.Colors.Capacity() // #nosec G115 -- valid permits only one, four, or six cells.
}

// Pitch returns zero for unsupported geometry.
func (g Geometry) Pitch() int {
	if !g.valid() {
		return 0
	}
	return (DataModules + 2*QuietModules) * g.Module
}

// Size validates the whitelist first; every result fits protocol uint16 fields.
func (p VisualProfile) Size() (image.Point, error) {
	if p.Capacity() == 0 {
		return image.Point{}, ErrProfile
	}
	g := p.Geometry
	return image.Pt(GridX+g.Columns*g.Pitch(), max(BeaconSize, GridY+g.Rows*g.Pitch())), nil
}
