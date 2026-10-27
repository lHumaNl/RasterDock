package fyneui

import (
	"image"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func TestControlledPreparedHeartbeatKeepsPackets(t *testing.T) {
	p := prepareFixture(t)
	space := &viewportCapacity{}
	space.store(image.Pt(3000, 2000), image.Point{})
	profile := application.DefaultVisualProfile(application.Colors8)
	for _, frame := range []uint64{1, 2} {
		s, err := renderControlledPrepared(p, profile, 0, codec.LiveState{Frame: frame, Epoch: 7, Nonce: 3, Focused: true, Armed: true}, space)
		if err != nil {
			t.Fatal(err)
		}
		b, packets, err := codec.DecodeControlled(s.picture)
		if err != nil {
			t.Fatal(err)
		}
		if b.Frame != frame || b.Ordinal != 0 || b.Nonce != 3 || b.Flags != 3 || b.Epoch != 7 || uint64(len(packets)) != profile.Capacity() {
			t.Fatalf("invalid held-page heartbeat: %+v", b)
		}
	}
	space.store(image.Pt(1, 1), image.Point{})
	if _, err := renderControlledPrepared(p, profile, 0, codec.LiveState{Frame: 3, Epoch: 7}, space); err == nil {
		t.Fatal("undersized controlled canvas accepted")
	}
}
