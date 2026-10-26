package codec

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

// Fixed v1 experimental geometry; these are physical pixels, not GUI points.
const (
	canvasWidth   = 664
	canvasHeight  = 460
	gridX         = 244
	gridY         = 40
	cellSize      = 420
	moduleSize    = 4
	quietModules  = 4
	dataVersion   = 20
	beaconVersion = 8
	dataModules   = 17 + 4*dataVersion
	maxBlock      = 512
	paletteSize   = 16
	paletteGap    = 4
)

func pageBeacon(d v1.Descriptor, profile application.ColorProfile, ordinal, frame, epoch uint64) v1.Beacon {
	return v1.Beacon{Descriptor: d, Colors: byte(profile), Columns: 1, Rows: 1, Version: dataVersion, Active: 1, Frame: frame, Ordinal: ordinal, Width: canvasWidth, Height: canvasHeight, GridX: gridX, GridY: gridY, CellWidth: cellSize, CellHeight: cellSize, Module: moduleSize, BeaconModule: moduleSize, Epoch: epoch, Phase: 1}
}

// RenderPage owns its output. Every plane is a complete independently valid QR.
func RenderPage(d v1.Descriptor, packets []v1.Packet, profile application.ColorProfile, ordinal, frame, epoch uint64) (*image.RGBA, error) {
	if profile.Capacity() == 0 || uint64(len(packets)) != profile.Capacity() || d.Block > maxBlock {
		return nil, application.ErrProfile
	}
	b := pageBeacon(d, profile, ordinal, frame, epoch)
	if packets[0].Kind == 1 {
		b.Phase = 0
	}
	canvas, err := beaconCanvas(b)
	if err != nil {
		return nil, err
	}
	matrices, err := packetMatrices(d, packets)
	if err != nil {
		return nil, err
	}
	paintPlanes(canvas, matrices)
	paintPalette(canvas, profile)
	return canvas, nil
}

func beaconCanvas(b v1.Beacon) (*image.RGBA, error) {
	raw, err := b.MarshalBinary()
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if err = paint(canvas, "RDH1:"+v1.Encode45(raw), beaconVersion, image.Point{}); err != nil {
		return nil, err
	}
	return canvas, nil
}

func packetMatrices(d v1.Descriptor, packets []v1.Packet) ([]*encoder.ByteMatrix, error) {
	matrices := make([]*encoder.ByteMatrix, len(packets))
	for i, p := range packets {
		if p.Transfer != d.Transfer {
			return nil, v1.ErrPacket
		}
		if err := p.ValidateBlock(d.Block); err != nil {
			return nil, err
		}
		matrix, err := packetMatrix(p)
		if err != nil {
			return nil, err
		}
		matrices[i] = matrix
	}
	return matrices, nil
}

func packetMatrix(p v1.Packet) (*encoder.ByteMatrix, error) {
	raw, err := p.MarshalBinary()
	if err != nil {
		return nil, err
	}
	qr, err := encoder.Encoder_encode("RD1:"+v1.Encode45(raw), decoder.ErrorCorrectionLevel_M, map[gozxing.EncodeHintType]interface{}{gozxing.EncodeHintType_QR_VERSION: dataVersion})
	if err != nil {
		return nil, err
	}
	return qr.GetMatrix(), nil
}

func planeColor(a, b, c int8) color.RGBA {
	value := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if a == 1 {
		value.R = 0
	}
	if b == 1 {
		value.G = 0
	}
	if c == 1 {
		value.B = 0
	}
	return value
}

func paintPlanes(dst *image.RGBA, matrices []*encoder.ByteMatrix) {
	for y := range dataModules {
		for x := range dataModules {
			a := matrices[0].Get(x, y)
			b := a
			if len(matrices) >= 2 {
				b = matrices[1].Get(x, y)
			}
			c := b
			if len(matrices) == 3 {
				c = matrices[2].Get(x, y)
			}
			origin := image.Pt(gridX+(x+quietModules)*moduleSize, gridY+(y+quietModules)*moduleSize)
			draw.Draw(dst, image.Rectangle{Min: origin, Max: origin.Add(image.Pt(moduleSize, moduleSize))}, image.NewUniform(planeColor(a, b, c)), image.Point{}, draw.Src)
		}
	}
}

func paintPalette(dst *image.RGBA, profile application.ColorProfile) {
	// Ascending binary tuples, a most significant, c least significant.
	// The strip begins at (244,0): beacon's 228px square plus 16px clearance.
	for i := 0; i < int(profile); i++ {
		x := gridX + i*(paletteSize+paletteGap)
		draw.Draw(dst, image.Rect(x, 0, x+paletteSize, paletteSize), image.NewUniform(paletteColor(i, profile)), image.Point{}, draw.Src)
	}
}

func paletteColor(tuple int, profile application.ColorProfile) color.RGBA {
	switch profile {
	case application.Colors8:
		return planeColor(int8(tuple>>2&1), int8(tuple>>1&1), int8(tuple&1))
	case application.Colors4:
		return planeColor(int8(tuple>>1&1), int8(tuple&1), int8(tuple&1))
	case application.Monochrome:
		return planeColor(int8(tuple&1), int8(tuple&1), int8(tuple&1))
	default:
		return color.RGBA{}
	}
}
