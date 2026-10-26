//go:build integration && darwin

package fyneui

// This is NOT production controlled mode. Only the data codec, immutable
// Prepared snapshot, viewport, native capture and storage receiver are production
// components. Parser, control lifecycle and beacon patching are test prototypes.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

func init() { nativeKeyboardProbeHelper = keyboardProbeViewer }

func keyboardProbeProfile() application.VisualProfile {
	return application.VisualProfile{Colors: application.Colors8, Geometry: application.Geometry{Module: 4, Columns: 2, Rows: 2}}
}

func keyboardProbeViewer() error {
	p, err := application.OpenPrepared(os.Getenv("RASTERDOCK_KEYBOARD_PROBE_PREPARED"))
	if err != nil {
		return err
	}
	title := os.Getenv("RASTERDOCK_KEYBOARD_PROBE_TITLE")
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	epoch := binary.LittleEndian.Uint64(random[:])
	if epoch == 0 {
		return errors.New("zero random epoch")
	}
	profile := keyboardProbeProfile()
	size, err := profile.Size()
	if err != nil {
		return err
	}
	a := app.New()
	w := a.NewWindow(title)
	parser := probeParser{}
	b := v1.Beacon{Descriptor: p.Descriptor, Colors: 8, Columns: 2, Rows: 2, Version: 20, Active: 4, Width: uint16(size.X), Height: uint16(size.Y), GridX: 244, GridY: 40, CellWidth: uint16(profile.Geometry.Pitch()), CellHeight: uint16(profile.Geometry.Pitch()), Module: 4, BeaconModule: 4, Epoch: epoch} // #nosec G115 -- Fixed validated module-4 2x2 geometry: 1084x880, pitch 420.
	var base *image.RGBA
	load := func() error {
		packets, e := p.VisualPage(parser.ordinal, profile)
		if e != nil {
			return e
		}
		base, e = codec.RenderVisualPage(p.Descriptor, packets, profile, parser.ordinal, 1, epoch)
		b.Phase = 1
		if packets[0].Kind == 1 {
			b.Phase = 0
		}
		return e
	}
	if err = load(); err != nil {
		return err
	}
	picture := canvas.NewImageFromImage(base)
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	viewport := newQRViewport(picture, w.Canvas())
	w.SetContent(viewport)
	logical, _ := physicalImageGeometry(base.Bounds(), fyne.Size{}, w.Canvas().PixelCoordinateForPosition)
	w.Resize(fyne.NewSize(max(720, logical.Width+80), max(520, logical.Height+80)))
	seenFocus, disarmed := false, false
	var failure error
	publish := func() {
		focused := platform.KeyboardProbeViewerFocused(title)
		if seenFocus && !focused {
			disarmed = true
			parser.active = false
		}
		if focused {
			seenFocus = true
		}
		b.Flags = 0
		if focused {
			b.Flags = 1
		}
		if focused && !disarmed && parser.nonce != math.MaxUint64 {
			b.Flags |= 2
		}
		b.Frame++
		b.Nonce = parser.nonce
		b.Ordinal = parser.ordinal
		img, e := probePatchBeacon(base, b)
		if e != nil {
			failure = e
			w.Close()
			return
		}
		picture.Image = img
		picture.Refresh()
		viewport.Refresh()
	}
	// GLFW delivers OnKeyDown before its Tab traversal early-return. A plain
	// OnTypedKey callback would silently miss Tab in Fyne 2.8.2 window.go:777.
	// This is an OS event listener, not a synthetic UI input substitute.
	desktopCanvas, ok := w.Canvas().(desktop.Canvas)
	if !ok {
		return errors.New("native desktop key listener unavailable")
	}
	desktopCanvas.SetOnKeyDown(func(e *fyne.KeyEvent) {
		if disarmed || !platform.KeyboardProbeViewerFocused(title) {
			disarmed = true
			parser.active = false
			publish()
			return
		}
		if parser.key(string(e.Name), time.Now(), p.PacketCount()) {
			if err := load(); err != nil {
				failure = err
				w.Close()
				return
			}
			// Diagnostics only: parent never reads these to decide progress/echo.
			fmt.Printf("accepted nonce=%d ordinal=%d\n", parser.nonce, parser.ordinal)
			publish()
		}
	})
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fyne.Do(func() {
					select {
					case <-done:
						return
					default:
						publish()
					}
				})
			}
		}
	}()
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); fyne.Do(w.Close) }() // EOF only, never commands.
	w.ShowAndRun()
	return failure
}

func TestNativeKeyboardProbe(t *testing.T) {
	if os.Getenv("RASTERDOCK_KEYBOARD_PROBE") != "1" {
		t.Skip("opt-in own-window OS keyboard probe; leave UUID viewer foreground, release all modifiers")
	}
	if !platform.KeyboardProbePermissions() {
		t.Fatal("AX and Screen Recording preflight required; probe never prompts or changes permissions")
	}
	ctx := keyboardProbeContext(t, 3*time.Minute)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "output")
	raw := make([]byte, 32<<10)
	for i := 0; i < len(raw); i += sha256.Size {
		block := sha256.Sum256([]byte(fmt.Sprintf("keyboard-probe-fixture-%d", i)))
		copy(raw[i:], block[:])
	}
	want := map[string][]byte{"raw.bin": raw, "empty": {}, "目录/мир.txt": []byte("keyboard-only visual probe 世界\n")}
	for name, data := range want {
		path := filepath.Join(source, name)
		must(os.MkdirAll(filepath.Dir(path), 0700))
		must(os.WriteFile(path, data, 0600))
	}
	must(os.MkdirAll(destination, 0700))
	p, err := application.Prepare(ctx, source, filepath.Join(root, "sender"), 512, storage.Flush, root)
	must(err)
	must(os.RemoveAll(source))
	p, err = application.OpenPrepared(p.Directory)
	must(err)
	id, err := domain.NewID()
	must(err)
	title := "RasterDock-keyboard-probe-" + domain.IDString(id)
	exe, err := os.Executable()
	must(err)
	cmd := exec.CommandContext(ctx, exe) //nolint:gosec // Own test executable only, never a shell.
	cmd.Env = append(os.Environ(), "RASTERDOCK_KEYBOARD_PROBE_HELPER=1", "RASTERDOCK_KEYBOARD_PROBE_TITLE="+title, "RASTERDOCK_KEYBOARD_PROBE_PREPARED="+p.Directory)
	var diagnostics bytes.Buffer
	cmd.Stdout = &diagnostics
	cmd.Stderr = &diagnostics
	stdin, err := cmd.StdinPipe()
	must(err)
	must(cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Errorf("helper: %v", e)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("helper cleanup timeout")
		}
		t.Logf("post-run helper diagnostics (never control input):\n%s", diagnostics.String())
	})
	var window uint32
	until := time.Now().Add(15 * time.Second)
	for window == 0 && time.Now().Before(until) && ctx.Err() == nil {
		sources, e := platform.Sources()
		must(e)
		for _, s := range sources {
			if s.Title == title || strings.HasSuffix(s.Title, " — "+title) {
				window = s.ID
				break
			}
		}
		if window == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if window == 0 {
		t.Fatal("own UUID viewer not found")
	}
	input, err := platform.BindKeyboardProbe(cmd, window, title, 3*(p.PacketCount()+2))
	must(err)
	t.Logf("target child PID=%d window=%d title=%s; initial Fyne activation authorized; no refocus after focus loss", cmd.Process.Pid, window, title)
	profile := keyboardProbeProfile()
	if p.ManifestPackets() > profile.Capacity() {
		t.Fatal("probe fixture must bootstrap manifest in the held initial page")
	}
	// Receipt timestamps do NOT prove native acquisition time. This probe bounds
	// call duration and locally observed age only; no production freshness claim.
	capture := func() (v1.Beacon, []v1.Packet, time.Time, error) {
		if e := ctx.Err(); e != nil {
			return v1.Beacon{}, nil, time.Time{}, e
		}
		if e := input.Check(); e != nil {
			return v1.Beacon{}, nil, time.Time{}, e
		}
		start := time.Now()
		call, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		frame, e := nativeCaptureFrame(call, window)
		if e != nil {
			return v1.Beacon{}, nil, time.Time{}, e
		}
		if time.Since(start) > time.Second || frame.CapturedAt.Before(start) {
			return v1.Beacon{}, nil, time.Time{}, errors.New("capture call/receipt bound exceeded")
		}
		b, packets, e := codec.Decode(frame.Image)
		if e == nil && (b.Colors != 8 || b.Module != 4 || b.Columns != 2 || b.Rows != 2 || b.Version != 20) {
			e = errors.New("unexpected probe geometry")
		}
		return b, packets, frame.CapturedAt, e
	}
	var gate probeGate
	var packets []v1.Packet
	until = time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		b, ps, at, e := capture()
		if e != nil {
			must(e)
		}
		if b.Descriptor != p.Descriptor {
			t.Fatal("unexpected descriptor")
		}
		if b.Epoch != 0 && b.Flags == 3 && b.Ordinal == 0 && b.Nonce == 0 {
			gate = probeGate{descriptor: b.Descriptor, epoch: b.Epoch}
			must(gate.observe(b, at, time.Now()))
			packets = ps
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if gate.epoch == 0 {
		t.Fatal("viewer was not focused/armed at explicit binding")
	}
	// Same-page hold is measured through two native captures, not helper state.
	first := gate.last
	for {
		b, ps, at, e := capture()
		must(e)
		if b.Ordinal != first.Ordinal || b.Nonce != 0 {
			t.Fatal("viewer auto advanced before keyboard command")
		}
		if b.Frame > first.Frame {
			must(gate.observe(b, at, time.Now()))
			packets = ps
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("native hold verified ordinal=%d frame=%d -> %d", first.Ordinal, first.Frame, gate.high)
	r, err := storage.OpenReceive(filepath.Join(root, "receive"), destination, gate.descriptor, application.DecodeManifest, root)
	must(err)
	defer func() { must(r.Close()) }()
	// The immutable sender is an identity oracle only, never receive payload.
	ordinals := make(map[tracePacketID]uint64)
	for n := uint64(0); n < p.PacketCount(); n++ {
		packet, e := p.Packet(n)
		must(e)
		ordinals[packetIdentity(packet)] = n
	}
	received := make(map[uint64]bool)
	accept := func(ps []v1.Packet) {
		must(r.CheckDescriptor(gate.last.Descriptor))
		for _, packet := range ps {
			e := r.Accept(packet)
			if errors.Is(e, storage.ErrMetadata) {
				continue
			}
			must(e)
			_, total, e := r.PublishedFiles()
			must(e)
			if total == uint64(len(want)) && packet.Kind != 0 {
				if n, ok := ordinals[packetIdentity(packet)]; ok {
					received[n] = true
				}
			}
		}
	}
	refresh := func() error {
		b, ps, at, e := capture()
		if e != nil {
			return e
		}
		e = gate.observe(b, at, time.Now())
		if gate.stopped {
			return e
		}
		if e == nil {
			packets = ps
		}
		return nil
	}
	exchange := probeExchange{now: time.Now, refresh: refresh, foreground: input.Check, send: input.Send,
		pause: func(d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	request := func(ordinal uint64, repeat bool) { must(exchange.request(ctx, &gate, ordinal, repeat)) }
	accept(packets) // Acceptance/flush precedes the very first command.
	// Real physical idempotent retry: send nonce=1, ordinal=0, then repeat it.
	request(0, false)
	accept(packets)
	before, err := r.DurableBytes()
	must(err)
	request(0, true)
	accept(packets)
	after, err := r.DurableBytes()
	must(err)
	if gate.last.Nonce != 1 || gate.last.Ordinal != 0 || before != after {
		t.Fatal("native repeat advanced page or inflated durable bytes")
	}
	for commands := uint64(0); uint64(len(received)) < p.PacketCount(); commands++ {
		if commands >= p.PacketCount() {
			t.Fatal("repair command budget exhausted")
		}
		missing := uint64(0)
		for received[missing] {
			missing++
		}
		request(missing, false)
		accept(packets)
		// Missing planes remain absent: next iteration requests the first gap,
		// never blindly ordinal += capacity. Echo alone never marks delivery.
	}
	must(r.Publish())
	published, total, err := r.PublishedFiles()
	must(err)
	if published != 3 || total != 3 {
		t.Fatalf("publication %d/%d", published, total)
	}
	for name, expected := range want {
		actual, e := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Private temporary destination and fixed test fixture names only.
		must(e)
		if !bytes.Equal(actual, expected) || sha256.Sum256(actual) != sha256.Sum256(expected) {
			t.Fatalf("bytes/SHA mismatch: %s", name)
		}
	}
	t.Logf("keyboard-only probe complete: packets=%d capacity=%d files=3; all bytes/SHA verified; NOT production controlled-mode or throughput evidence", p.PacketCount(), profile.Capacity())
}
