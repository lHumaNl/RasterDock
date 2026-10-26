package fyneui

import (
	"image"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func decodeMixedTransfer(d v1.Descriptor, p v1.Packet, first bool) (v1.Beacon, []v1.Packet, error) {
	valid, err := codec.RenderPage(d, []v1.Packet{p, p}, application.Colors4, 0, 1, 1)
	if err != nil {
		return v1.Beacon{}, nil, err
	}
	d.Transfer[0] ^= 1
	p.Transfer = d.Transfer
	foreign, err := codec.RenderPage(d, []v1.Packet{p, p}, application.Colors4, 0, 1, 1)
	if err != nil {
		return v1.Beacon{}, nil, err
	}
	splicePlane(valid, foreign, first)
	return codec.Decode(valid)
}

func splicePlane(dst, src *image.RGBA, first bool) {
	// The data grid starts after the 228px beacon and 16px clearance.
	const gridStart = 244
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := gridStart; x < dst.Bounds().Dx(); x++ {
			a, b := dst.RGBAAt(x, y), src.RGBAAt(x, y)
			if first {
				a.R = b.R
			} else {
				a.G, a.B = b.G, b.B
			}
			dst.SetRGBA(x, y, a)
		}
	}
}

func TestEngineForeignPlaneContinuesReceiving(t *testing.T) {
	for _, fault := range []string{"foreign-first", "foreign-last"} {
		t.Run(fault, func(t *testing.T) {
			h := newHarness(t)
			cmd := receiveCommand(t)
			h.start(cmd)
			h.tick(fault)
			h.expect("status.receiving")
			h.tick("")
			h.assertDestination(cmd)
			h.stop()
			if len(h.owners) != 1 {
				t.Fatal("foreign plane replaced receiver")
			}
		})
	}
}

func TestEngineSameTransferInvalidPacketStillFails(t *testing.T) {
	h := newHarness(t)
	h.start(receiveCommand(t))
	h.tick("invalid-same-transfer")
	h.expect("status.error")
	h.stop()
	if len(h.owners) != 1 || h.owners[0].closes != 1 {
		t.Fatal("invalid packet did not terminate receiver")
	}
}
