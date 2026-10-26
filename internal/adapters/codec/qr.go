// Package codec renders complete immutable visual frames.
package codec

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"strings"

	"github.com/makiuchi-d/gozxing"
	multiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

// Render uses four physical pixels per module and four modules of quiet zone.
func Render(d v1.Descriptor, p v1.Packet, ordinal, frame, epoch uint64) (*image.RGBA, error) {
	return RenderPage(d, []v1.Packet{p}, application.Monochrome, ordinal, frame, epoch)
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
	var points []gozxing.ResultPoint
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
			points = result.GetResultPoints()
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
	if beacon.Colors == byte(application.Colors4) || beacon.Colors == byte(application.Colors8) {
		return decodeColor(img, beacon, points)
	}
	if beacon.Colors != 2 {
		return beacon, nil, application.ErrProfile
	}
	// A torn capture can contain a valid QR from a different transfer.
	packets = slices.DeleteFunc(packets, func(p v1.Packet) bool { return p.Transfer != beacon.Transfer })
	return beacon, packets, nil
}
