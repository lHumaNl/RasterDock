//go:build integration && darwin

package fyneui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rasterdock/internal/application"
)

func geometryDimensions(r geometryReply) (required, available image.Point, ok bool) {
	n, err := fmt.Sscanf(r.Dimensions, "Required: %d × %d physical pixels\nAvailable: %d × %d physical pixels", &required.X, &required.Y, &available.X, &available.Y)
	return required, available, n == 4 && err == nil
}

func geometrySize(t *testing.T, columns, rows, module int) image.Point {
	t.Helper()
	p := application.DefaultVisualProfile(application.Colors8)
	p.Geometry.Columns, p.Geometry.Rows, p.Geometry.Module = columns, rows, module
	size, err := p.Size()
	if err != nil {
		t.Fatal(err)
	}
	return size
}

func (c *geometryChild) fit(t *testing.T, size image.Point) {
	t.Helper()
	c.call(t, geometryRequest{Viewport: size.Add(image.Pt(48, 48))})
	c.wait(t, "physical viewport fits requested profile", 8*time.Second, func(r geometryReply) bool {
		_, available, ok := geometryDimensions(r)
		return ok && available.X >= size.X && available.Y >= size.Y
	})
}

func geometryProfile(r geometryReply, size image.Point, grid string) bool {
	required, available, ok := geometryDimensions(r)
	return ok && required == size && available.X >= size.X && available.Y >= size.Y &&
		r.Colors == "8 colors — experimental" && r.Module == "4" && r.Grid == grid && r.Visible && r.Picture == size && strings.HasPrefix(r.Status, "Packet ")
}

// Opt-in production acceptance, not a monitor/profile matrix or benchmark.
// Two independent Run processes; the receiver obtains bytes exclusively through
// ScreenCaptureKit, codec, storage and publication. No manual focus automation.
func TestMacOSProductionGUIGeometry(t *testing.T) {
	if os.Getenv("RASTERDOCK_GUI_GEOMETRY") != "1" {
		t.Skip("set RASTERDOCK_GUI_GEOMETRY=1 for two-process native GUI acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Second)
	t.Cleanup(cancel)
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	binary := make([]byte, 128*1024)
	for offset := 0; offset < len(binary); offset += sha256.Size {
		block := sha256.Sum256([]byte(fmt.Sprintf("RasterDock geometry fixture block %d", offset/sha256.Size)))
		copy(binary[offset:], block[:])
	}
	want := map[string][]byte{"binary.bin": binary, "empty": {}, "目录/привет.txt": []byte("Hello, мир 世界\n")}
	for name, data := range want {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	sender := startGeometryGUI(ctx, t, filepath.Join(root, "sender"))
	size := geometrySize(t, 2, 2, 4)
	sender.fit(t, size)
	sender.call(t, geometryRequest{Actions: []geometryAction{{"module", "4"}, {"grid", "2 × 2"}, {"colors", "8 colors — experimental"}}})
	// Rendered bounds and a packet status must accompany selected values; an
	// optimistic selector value alone is not engine acknowledgement.
	sender.call(t, geometryRequest{Source: &source, Tap: "Prepare"})
	prepared := sender.wait(t, "prepared 8-color 2x2 production sender", 15*time.Second, func(r geometryReply) bool { return geometryProfile(r, size, "2 × 2") })
	if !strings.HasPrefix(prepared.Title, "RasterDock · ") || !strings.HasSuffix(prepared.Title, fmt.Sprintf(" · Sender · PID %d", sender.cmd.Process.Pid)) {
		t.Fatalf("unexpected sender identity: %q", prepared.Title)
	}
	title := prepared.Title
	sender.assertRenderedProfile(t, 2, 2)
	// Rapid intent changes remain under the real command queue's eight slots.
	sender.call(t, geometryRequest{Actions: []geometryAction{{"module", "6"}, {"module", "4"}, {"grid", "1 × 1"}, {"grid", "2 × 2"}, {"colors", "4 colors — experimental"}, {"colors", "8 colors — experimental"}}})
	sender.call(t, geometryRequest{Tap: "Next frame"})
	sender.wait(t, "rapid profile changes reconcile", 12*time.Second, func(r geometryReply) bool { return geometryProfile(r, size, "2 × 2") && r.Status != prepared.Status })
	sender.stays(t, "rapid changes remain reconciled", func(r geometryReply) bool { return geometryProfile(r, size, "2 × 2") })
	sender.assertRenderedProfile(t, 2, 2)
	// The doubled module must exceed measured available area, not just an
	// assumed display size. Keep the last accepted geometry and picture intact.
	before := sender.call(t, geometryRequest{})
	_, available, _ := geometryDimensions(before)
	oversize := geometrySize(t, 2, 2, 8)
	if oversize.X <= available.X && oversize.Y <= available.Y {
		t.Fatal("viewport unexpectedly fits oversized rejection probe")
	}
	sender.call(t, geometryRequest{Actions: []geometryAction{{"module", "8"}}})
	sender.wait(t, "oversized profile rejected and rolled back", 8*time.Second, func(r geometryReply) bool {
		return strings.HasPrefix(r.Status, "Required ") && r.Module == "4" && r.Grid == "2 × 2" && r.Picture == size && r.Visible
	})
	sender.call(t, geometryRequest{Tap: "Timer playback"})
	playing := sender.wait(t, "timer starts", 8*time.Second, func(r geometryReply) bool { return geometryProfile(r, size, "2 × 2") && r.Status != before.Status })
	sender.call(t, geometryRequest{Viewport: size.Sub(image.Pt(120, 120))})
	shrunk := sender.wait(t, "shrink stops playback and removes QR", 8*time.Second, func(r geometryReply) bool {
		return strings.HasPrefix(r.Status, "Required ") && (!r.Visible || r.Picture == image.Pt(1, 1))
	})
	sender.fit(t, size)
	sender.stays(t, "restore must not auto-resume", func(r geometryReply) bool { return r.Status == shrunk.Status && r.Picture == shrunk.Picture })
	sender.call(t, geometryRequest{Tap: "Timer playback"})
	sender.wait(t, "explicit resume after resize", 8*time.Second, func(r geometryReply) bool { return geometryProfile(r, size, "2 × 2") && r.Status != playing.Status })
	sender.call(t, geometryRequest{Tap: "Pause"})
	sender.wait(t, "sender paused before capture setup", 5*time.Second, geometryPaused)
	// Timer playback repeats manifest pages; no private ordinal reset is used.
	receiverRoot := filepath.Join(root, "receiver")
	receiver := startGeometryGUI(ctx, t, receiverRoot)
	receiver.call(t, geometryRequest{Destination: &destination, Tap: "Refresh capture windows"})
	receiver.wait(t, "exact PID-owned sender enumerated", 12*time.Second, func(r geometryReply) bool {
		for _, option := range r.Options {
			if geometryOwnedOption(option, title) {
				return true
			}
		}
		return false
	})
	receiver.call(t, geometryRequest{SelectTitle: title, Tap: "Receive / resume"})
	sender.call(t, geometryRequest{Tap: "Timer playback"})
	progress := receiver.wait(t, "partial durable real capture", 45*time.Second, func(r geometryReply) bool {
		n, p, total := geometryProgress(r)
		return r.Session != "" && n > 0 && n < int64(len(binary)) && (total == 0 || p < total)
	})
	partial, _, _ := geometryProgress(progress)
	session := progress.Session
	if rel, err := filepath.Rel(receiverRoot, session); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		t.Fatal("session escaped isolated receiver root")
	}
	receiver.call(t, geometryRequest{Tap: "Pause"})
	paused := receiver.wait(t, "receive pause drains capture and storage", 8*time.Second, geometryPaused)
	if paused.Session != session {
		t.Fatal("pause changed session")
	}
	geometryReleased(t, session)
	sender.call(t, geometryRequest{Tap: "Pause"})
	sender.wait(t, "sender paused for geometry change", 5*time.Second, geometryPaused)
	size = geometrySize(t, 3, 2, 4)
	sender.fit(t, size)
	sender.call(t, geometryRequest{Actions: []geometryAction{{"colors", "4 colors — experimental"}, {"grid", "3 × 2"}, {"colors", "8 colors — experimental"}}})
	sender.wait(t, "paused transfer switches to 8-color 3x2", 10*time.Second, func(r geometryReply) bool { return geometryProfile(r, size, "3 × 2") })
	sender.assertRenderedProfile(t, 3, 2)
	receiver.stays(t, "paused receiver retains session", func(r geometryReply) bool { return geometryPaused(r) && r.Session == session })
	geometryReleased(t, session)
	receiver.call(t, geometryRequest{Tap: "Receive / resume"})
	receiver.wait(t, "same-session durable progress retained", 15*time.Second, func(r geometryReply) bool {
		n, _, _ := geometryProgress(r)
		return r.Session == session && n >= partial
	})
	sender.call(t, geometryRequest{Tap: "Timer playback"})
	receiver.wait(t, "all files published through real capture", 130*time.Second, func(r geometryReply) bool {
		_, p, total := geometryProgress(r)
		return r.Session == session && total == len(want) && p == total
	})
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Fixed fixture names under test-owned destination.
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) || sha256.Sum256(got) != sha256.Sum256(data) {
			t.Fatalf("exact bytes/SHA-256 mismatch: %s", name)
		}
		t.Logf("verified %s bytes=%d sha256=%x", name, len(got), sha256.Sum256(got))
	}
	receiver.close(t)
	geometryReleased(t, session)
	sender.close(t)
}

func geometryPaused(r geometryReply) bool { return r.Status == "Paused; saved state retained" }

func (c *geometryChild) assertRenderedProfile(t *testing.T, columns, rows int) {
	t.Helper()
	r := c.call(t, geometryRequest{InspectSender: true})
	if r.RenderedProfile != [4]int{8, 4, columns, rows} {
		t.Fatalf("rendered profile differs from selected profile: %v", r.RenderedProfile)
	}
	t.Logf("sender-only rendered profile verified: %v", r.RenderedProfile)
}
