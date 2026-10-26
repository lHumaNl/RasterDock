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
	planes := splitPlanes(img, m, refs, application.ColorProfile(b.Colors))
	var packets []v1.Packet
	for _, plane := range planes {
		p, err := decodePlane(plane)
		// Neither frame ordinal nor a neighbor's failure invalidates a valid packet.
		if err == nil && p.Transfer == b.Transfer {
			packets = append(packets, p)
		}
	}
	return b, packets, nil
}

func splitPlanes(img image.Image, m captureMap, refs []rgb, profile application.ColorProfile) []*gozxing.BitMatrix {
	planes := make([]*gozxing.BitMatrix, profile.Capacity())
	for i := range planes {
		planes[i], _ = gozxing.NewBitMatrix(dataModules, dataModules) // Fixed positive dimensions.
	}
	for y := range dataModules {
		for x := range dataModules {
			cx := float64(gridX+(x+quietModules)*moduleSize) + moduleSize/2.0
			cy := float64(gridY+(y+quietModules)*moduleSize) + moduleSize/2.0
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
