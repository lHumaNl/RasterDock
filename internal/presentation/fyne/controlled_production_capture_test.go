//go:build integration && darwin

package fyneui

// Capture-only qualification, NOTRUN until initial Fyne activation is authorized.
// This test never binds input or sends keys; Accessibility permission is not a
// prerequisite. Screen Recording permission must already exist.
// Authorized invocation (sanitize BEFORE TestMain can select a helper):
// env -u RASTERDOCK_KEYBOARD_PROBE_HELPER -u RASTERDOCK_COLOR_BENCHMARK_HELPER \
//   -u RASTERDOCK_NATIVE_GUI_HELPER -u RASTERDOCK_NATIVE_TEST_VIEWER \
//   RASTERDOCK_CONTROLLED_CAPTURE=1 go test -tags=integration \
//   ./internal/presentation/fyne -run '^TestMacOSControlledCaptureOnly$' -count=1 -timeout=4m -v
// Three real held heartbeats qualify acquisition, not idle-frame synthesis,
// sleep/wake, resize, fault injection, keyboard delivery or two-GUI acceptance.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
)

type productionCaptureIdentity interface {
	Target() platform.ControlTarget
	CheckControlAge() error
}

func TestMacOSControlledCaptureOnly(t *testing.T) {
	if os.Getenv("RASTERDOCK_CONTROLLED_CAPTURE") != "1" {
		t.Skip("capture-only qualification requires opt-in and separate initial GUI activation authorization")
	}
	ctx := keyboardProbeContext(t, 3*time.Minute)
	must := func(phase string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("phase %s failed: %v; no refocus/retry", phase, err)
		}
	}
	must("capture capability", platform.ControlCaptureCapability())
	for _, key := range []string{"RASTERDOCK_KEYBOARD_PROBE_HELPER", "RASTERDOCK_COLOR_BENCHMARK_HELPER", "RASTERDOCK_NATIVE_GUI_HELPER", "RASTERDOCK_NATIVE_TEST_VIEWER"} {
		t.Setenv(key, "")
	}
	child, target, title := captureOnlyProductionSender(ctx, t)
	stream, err := platform.OpenControlCapture(target.ID) //nolint:contextcheck // Production open API has no context argument; Next is bounded below.
	must("capture open", err)
	t.Cleanup(func() { must("capture cleanup", stream.Close()) })
	identity, ok := stream.(productionCaptureIdentity)
	if !ok {
		t.Fatal("capture must expose immutable native target and control clock guard")
	}
	pinned := identity.Target()
	if !pinned.Valid() || pinned.Window != target.ID || pinned.PID != child.cmd.Process.Pid || pinned.Title != title {
		t.Fatal("native capture target does not match exact owned child")
	}
	// Child process identity is the live exec.Cmd; native Target adds its pinned
	// nonzero process-start identity. No arbitrary selected window is accepted.
	select {
	case <-child.done:
		t.Fatal("owned child exited before acquisition")
	default:
	}
	t.Logf("capture-only target PID=%d window=%d; no input binding or OS keys", pinned.PID, pinned.Window)
	captureOnlyStaticPage(ctx, t, stream, identity)
	if identity.Target() != pinned {
		t.Fatal("capture target changed during acquisition")
	}

	// Keep capture alive while closing only our source through EOF. The entire
	// EOF-to-revocation observation has a 3s deadline, not a per-poll deadline.
	closedAt := time.Now()
	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	must("source EOF", child.input.Close())
	for {
		_, nextErr := stream.Next(stopCtx)
		if stopCtx.Err() != nil || time.Since(closedAt) > 3*time.Second {
			t.Fatal("source closure did not revoke capture within 3s")
		}
		if nextErr != nil && !errors.Is(nextErr, platform.ErrFramePending) && identity.CheckControlAge() != nil {
			if stopCtx.Err() != nil || time.Since(closedAt) > 3*time.Second {
				t.Fatal("source revocation check exceeded the 3s bound")
			}
			break // A context timeout alone never proves native revocation.
		}
	}
	select {
	case <-child.done:
		child.close(t) // Checks normal child exit, no GUI/control request.
	case <-stopCtx.Done():
		t.Fatal("owned source did not exit normally within 3s")
	}
	if identity.Target() != pinned {
		t.Fatal("revocation changed pinned target")
	}
	// Production Close has a 250ms native teardown budget. Allow 1s wall-clock
	// scheduling slack, but require success and idempotence (no timeout masked).
	for range 2 {
		start := time.Now()
		must("capture close", stream.Close())
		if time.Since(start) > time.Second {
			t.Fatal("capture teardown exceeded 1s")
		}
	}
	if !errors.Is(identity.CheckControlAge(), platform.ErrStreamClosed) {
		t.Fatal("closed capture still exposes valid control age")
	}
	nextCtx, nextCancel := context.WithTimeout(ctx, time.Second)
	defer nextCancel()
	_, err = stream.Next(nextCtx)
	if !errors.Is(err, platform.ErrStreamClosed) {
		t.Fatal("Next after Close must report stream closed")
	}
	t.Log("capture-only qualification complete: static held page, timestamp bounds, owned target, source-close revocation, idempotent teardown; no keyboard/sleep/fault claims")
}

func captureOnlyProductionSender(ctx context.Context, t *testing.T) (*geometryChild, platform.Source, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	for name, data := range controlledProductionFiles() {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal("fixture directory failed")
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal("fixture write failed")
		}
	}
	child := startGeometryGUI(ctx, t, filepath.Join(root, "sender"))
	size := geometrySize(t, 1, 1, 4)
	child.fit(t, size)
	child.call(t, geometryRequest{Actions: []geometryAction{{"module", "4"}, {"grid", "1 × 1"}, {"colors", "8 colors — experimental"}}})
	child.call(t, geometryRequest{Source: &source, Tap: "Prepare"})
	prepared := child.wait(t, "capture-only production sender prepared", 15*time.Second, func(r geometryReply) bool {
		return geometryProfile(r, size, "1 × 1")
	})
	if !strings.HasPrefix(prepared.Title, "RasterDock · ") || !strings.HasSuffix(prepared.Title, fmt.Sprintf(" · Sender · PID %d", child.cmd.Process.Pid)) {
		t.Fatal("sender title does not identify owned child")
	}
	sources, err := platform.Sources()
	if err != nil {
		t.Fatal("owned source discovery failed")
	}
	var target platform.Source
	for _, s := range sources {
		_, title, prefixed := strings.Cut(s.Title, " — ")
		if s.Title == prepared.Title || (prefixed && title == prepared.Title) {
			if target.ID != 0 {
				t.Fatal("ambiguous owned source")
			}
			target = s
		}
	}
	if target.ID == 0 {
		t.Fatal("owned source missing")
	}
	child.call(t, geometryRequest{Tap: "Controlled viewer (allow keyboard)"})
	// No further helper requests after arming; capture is the sole observer.
	return child, target, prepared.Title
}

func captureOnlyStaticPage(ctx context.Context, t *testing.T, stream platform.Stream, identity productionCaptureIdentity) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if ctx.Err() != nil {
			t.Fatal("static held page not observed within 10s")
		}
		if err := identity.CheckControlAge(); err != nil {
			t.Fatalf("native control clock guard failed: %v", err)
		}
		frame, err := stream.Next(ctx)
		if errors.Is(err, platform.ErrFramePending) {
			continue
		}
		if err != nil {
			t.Fatalf("native acquisition failed: %v", err)
		}
		now := time.Now()
		if !frame.ControlAgeBounded || frame.Generation == 0 || frame.ControlCapturedAt.IsZero() || frame.ControlCapturedAt.After(now) || now.Sub(frame.ControlCapturedAt) > time.Second {
			t.Fatal("native acquisition lacks fresh bounded timestamp/generation")
		}
		b, _, err := codec.DecodeControlled(frame.Image)
		if err != nil {
			if !errors.Is(err, codec.ErrAmbiguousControl) {
				continue // Bounded initial rendering wait only.
			}
			t.Fatal("controlled beacon decode failed")
		}
		if b.Flags&2 == 0 || b.Epoch == 0 || b.Ordinal != 0 || b.Nonce != 0 || b.Colors != 8 || b.Columns != 1 || b.Rows != 1 || b.Module != 4 {
			t.Fatal("unexpected held/armed controlled beacon")
		}
		t.Logf("native static held page verified: ordinal=%d frame=%d generation=%d", b.Ordinal, b.Frame, frame.Generation)
		return
	}
}
