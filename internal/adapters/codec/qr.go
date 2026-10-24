// Package codec renders complete immutable monochrome frames.
package codec

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"github.com/makiuchi-d/gozxing"
	multiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	v1 "rasterdock/internal/protocol/v1"
)

// Render uses four physical pixels per module and four modules of quiet zone.
func Render(d v1.Descriptor, p v1.Packet, ordinal, frame, epoch uint64) (*image.RGBA, error) {
	if d.Block > 512 {
		return nil, errors.New("initial monochrome profile supports blocks up to 512 bytes")
	}
	wire, err := p.MarshalBinary()
	if err != nil {
		return nil, err
	}
	b := v1.Beacon{Descriptor: d, Colors: 2, Columns: 1, Rows: 1, Version: 20, Active: 1, Frame: frame, Ordinal: ordinal, Width: 664, Height: 460, GridX: 244, GridY: 40, CellWidth: 420, CellHeight: 420, Module: 4, BeaconModule: 4, Epoch: epoch, Phase: 1}
	if p.Kind == 1 {
		b.Phase = 0
	}
	beacon, err := b.MarshalBinary()
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, int(b.Width), int(b.Height)))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if err = paint(canvas, "RDH1:"+v1.Encode45(beacon), 8, image.Point{}); err != nil {
		return nil, err
	}
	if err = paint(canvas, "RD1:"+v1.Encode45(wire), 20, image.Pt(244, 40)); err != nil {
		return nil, err
	}
	draw.Draw(canvas, image.Rect(244, 0, 260, 16), image.NewUniform(color.Black), image.Point{}, draw.Src)
	return canvas, nil
}

func paint(dst *image.RGBA, text string, version int, origin image.Point) error {
	qr, err := encoder.Encoder_encode(text, decoder.ErrorCorrectionLevel_M, map[gozxing.EncodeHintType]interface{}{gozxing.EncodeHintType_QR_VERSION: version})
	if err != nil {
		return err
	}
	m := qr.GetMatrix()
	for y := 0; y < m.GetHeight(); y++ {
		for x := 0; x < m.GetWidth(); x++ {
			if m.Get(x, y) == 1 {
				r := image.Rect(origin.X+(x+4)*4, origin.Y+(y+4)*4, origin.X+(x+5)*4, origin.Y+(y+5)*4)
				draw.Draw(dst, r, image.NewUniform(color.Black), image.Point{}, draw.Src)
			}
		}
	}
	return nil
}

// Decode owns a fresh reader and returns independently CRC-validated packets.
func Decode(img image.Image) (v1.Beacon, []v1.Packet, error) {
	var beacon v1.Beacon
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return beacon, nil, err
	}
	results, err := multiqr.NewQRCodeMultiReader().DecodeMultiple(bitmap, map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true})
	if err != nil {
		return beacon, nil, err
	}
	var packets []v1.Packet
	found := false
	for _, result := range results {
		text := result.GetText()
		switch {
		case strings.HasPrefix(text, "RDH1:"):
			raw, e := v1.Decode45(text[5:])
			if e != nil {
				continue
			}
			b, e := v1.ParseBeacon(raw)
			if e != nil {
				continue
			}
			if found && b.Descriptor != beacon.Descriptor {
				return beacon, nil, errors.New("multiple transfers in capture")
			}
			beacon = b
			found = true
		case strings.HasPrefix(text, "RD1:"):
			raw, e := v1.Decode45(text[4:])
			if e != nil {
				continue
			}
			p, e := v1.ParsePacket(raw)
			if e == nil {
				packets = append(packets, p)
			}
		}
	}
	if !found {
		return beacon, nil, errors.New("RasterDock beacon not found")
	}
	return beacon, packets, nil
}
