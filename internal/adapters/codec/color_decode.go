package codec

import (
	"image"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func decodeColor(img image.Image, b v1.Beacon, points []gozxing.ResultPoint) (v1.Beacon, []v1.Packet, error) {
	m, err := geometry(img, b, points)
	if err != nil {
		return b, nil, err
	}
	refs, err := capturedPalette(img, m, application.ColorProfile(b.Colors))
	if err != nil {
		return b, nil, err
	}
	planes := splitGrid(img, m, refs, beaconProfile(b))
	var packets []v1.Packet
	for _, plane := range planes {
		p, err := decodePlane(plane)
		// Filter block-invalid packets here, before storage's terminal validation.
		// Neither frame ordinal nor a neighbor's failure invalidates a valid packet.
		if err == nil && p.Transfer == b.Transfer && p.ValidateBlock(b.Block) == nil {
			packets = append(packets, p)
		}
	}
	return b, packets, nil
}

func splitPlanes(img image.Image, m captureMap, refs []rgb, profile application.ColorProfile) []*gozxing.BitMatrix {
	return splitCell(img, m, refs, profile, image.Pt(gridX, gridY), moduleSize)
}

func splitGrid(img image.Image, m captureMap, refs []rgb, p application.VisualProfile) []*gozxing.BitMatrix {
	var planes []*gozxing.BitMatrix
	g := p.Geometry
	for cell := 0; cell < g.Columns*g.Rows; cell++ {
		origin := image.Pt(gridX+cell%g.Columns*g.Pitch(), gridY+cell/g.Columns*g.Pitch())
		planes = append(planes, splitCell(img, m, refs, p.Colors, origin, g.Module)...)
	}
	return planes
}

func splitCell(img image.Image, m captureMap, refs []rgb, profile application.ColorProfile, origin image.Point, module int) []*gozxing.BitMatrix {
	planes := make([]*gozxing.BitMatrix, profile.Capacity())
	for i := range planes {
		planes[i], _ = gozxing.NewBitMatrix(dataModules, dataModules) // Fixed positive dimensions.
	}
	for y := range dataModules {
		for x := range dataModules {
			cx := float64(origin.X+(x+quietModules)*module) + float64(module)/2
			cy := float64(origin.Y+(y+quietModules)*module) + float64(module)/2
			tuple := nearest(m.sample(img, cx, cy), refs)
			for i, plane := range planes {
				if tuple&(1<<(len(planes)-1-i)) != 0 {
					plane.Set(x, y)
				}
			}
		}
	}
	return planes
}

func decodePlane(matrix *gozxing.BitMatrix) (v1.Packet, error) {
	result, err := decoder.NewDecoder().DecodeWithoutHint(matrix)
	if err != nil {
		return v1.Packet{}, err
	}
	text := result.GetText()
	if !strings.HasPrefix(text, "RD1:") {
		return v1.Packet{}, v1.ErrPacket
	}
	raw, err := v1.Decode45(text[4:])
	if err != nil {
		return v1.Packet{}, err
	}
	return v1.ParsePacket(raw)
}
