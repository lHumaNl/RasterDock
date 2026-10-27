package codec

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"reflect"
	"testing"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func TestLiveControlRoundTrips(t *testing.T) {
	for _, colors := range []application.ColorProfile{2, 4, 8} {
		for _, module := range []int{4, 6, 8} {
			p := application.VisualProfile{Colors: colors, Geometry: application.Geometry{Module: module, Columns: 2, Rows: 2}}
			for flags := byte(0); flags < 4; flags++ {
				t.Run(fmt.Sprint(colors, module, flags), func(t *testing.T) { assertLiveRoundTrip(t, p, flags) })
			}
		}
	}
}

func assertLiveRoundTrip(t *testing.T, p application.VisualProfile, flags byte) {
	t.Helper()
	d, packets := geometryFixture(p)
	state := LiveState{Frame: 13, Epoch: 17, Nonce: ^uint64(0), Focused: flags&1 != 0, Armed: flags&2 != 0}
	img, err := RenderControlledPage(d, packets, p, 23, state)
	if err != nil {
		t.Fatal(err)
	}
	b, got, err := DecodeControlled(img)
	if err != nil || !reflect.DeepEqual(got, packets) {
		t.Fatalf("packets=%d error=%v", len(got), err)
	}
	if b.Flags != flags || b.Frame != state.Frame || b.Epoch != state.Epoch || b.Nonce != state.Nonce || b.Ordinal != 23 || b.Phase != 1 {
		t.Fatalf("unexpected beacon: %+v", b)
	}
}

func TestLiveControlPhasesAndLegacy(t *testing.T) {
	for _, colors := range []application.ColorProfile{2, 4, 8} {
		p := application.DefaultVisualProfile(colors)
		for _, metadata := range []bool{false, true} {
			for _, paused := range []bool{false, true} {
				assertLivePhase(t, p, metadata, paused)
			}
		}
	}
}

func assertLivePhase(t *testing.T, p application.VisualProfile, metadata, paused bool) {
	t.Helper()
	d, packets := geometryFixture(p)
	phase := byte(1)
	if metadata {
		_, base := eightFixture()
		packets[0] = base[0]
		phase = 0
	}
	state := LiveState{Frame: 2, Epoch: 3, Paused: paused}
	img, err := RenderControlledPage(d, packets, p, 1, state)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := DecodeControlled(img)
	if paused {
		phase = 2
	}
	if err != nil || b.Phase != phase || b.Flags != 0 || b.Nonce != 0 {
		t.Fatalf("beacon=%+v error=%v", b, err)
	}
	if paused {
		return
	}
	legacy, err := RenderVisualPage(d, packets, p, 1, 2, 3)
	if err != nil || !bytes.Equal(img.Pix, legacy.Pix) {
		t.Fatal("legacy pixels changed", err)
	}
}

func TestLiveControlAmbiguousSameTransfer(t *testing.T) {
	p := application.DefaultVisualProfile(application.Monochrome)
	d, packets := geometryFixture(p)
	img, err := RenderControlledPage(d, packets, p, 0, LiveState{Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = DecodeControlled(img); err != nil {
		t.Fatal(err)
	}
	joined := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx()*2, img.Bounds().Dy()))
	draw.Draw(joined, joined.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(joined, img.Bounds(), img, image.Point{}, draw.Src)
	draw.Draw(joined, img.Bounds().Add(image.Pt(img.Bounds().Dx(), 0)), img, image.Point{}, draw.Src)
	if _, _, err = Decode(joined); err != nil {
		t.Fatal("passive decode changed", err)
	}
	if _, got, err := DecodeControlled(joined); err == nil || len(got) != 0 {
		t.Fatal("ambiguous capture accepted")
	}
}

func TestLiveControlRejectsInvalidRender(t *testing.T) {
	p := application.DefaultVisualProfile(application.Monochrome)
	d, packets := geometryFixture(p)
	if img, err := RenderControlledPage(d, packets, p, 0, LiveState{}); err == nil || img != nil {
		t.Fatal("zero epoch accepted")
	}
	if img, err := RenderControlledPage(d, nil, p, 0, LiveState{Epoch: 1}); err == nil || img != nil {
		t.Fatal("empty packets accepted")
	}
	d.Length = 0
	if img, err := RenderControlledPage(d, packets, p, 0, LiveState{Epoch: 1}); err == nil || img != nil {
		t.Fatal("invalid descriptor accepted")
	}
}

func TestLiveControlRejectsUntrustedBeacon(t *testing.T) {
	d, _ := eightFixture()
	for _, change := range []func(*v1.Beacon){func(b *v1.Beacon) { b.Epoch = 0 }, func(b *v1.Beacon) { b.GridX = 0 }, func(b *v1.Beacon) { b.Width = 65535 }} {
		img := eightFrame(t)
		b := pageBeacon(d, application.Colors8, 0, 1, 1)
		change(&b)
		replaceBeacon(t, img, b)
		if _, got, err := DecodeControlled(img); err == nil || len(got) != 0 {
			t.Fatal("untrusted beacon accepted")
		}
	}
}
