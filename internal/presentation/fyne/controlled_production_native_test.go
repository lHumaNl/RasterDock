//go:build integration && darwin

package fyneui

// NOTRUN until separately authorized: this test opens/initially activates one
// production sender GUI and sends real OS keys ONLY to that owned child.
// After review/authorization, run (never inherit TestMain helper switches):
// env -u RASTERDOCK_KEYBOARD_PROBE_HELPER -u RASTERDOCK_COLOR_BENCHMARK_HELPER \
//   -u RASTERDOCK_NATIVE_GUI_HELPER -u RASTERDOCK_NATIVE_TEST_VIEWER \
//   RASTERDOCK_CONTROLLED_PRODUCTION=1 go test -tags=integration \
//   ./internal/presentation/fyne -run '^TestMacOSControlledProduction$' -count=1 -timeout=4m -v
// Release modifiers; leave the child foreground. No refocus or whole-test retry.
// This is two processes, NOT two GUI acceptance: receiver is the actual engine,
// not its GUI button/confirmation or 3-second focus handoff. No throughput claim.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

func TestMacOSControlledProduction(t *testing.T) {
	if os.Getenv("RASTERDOCK_CONTROLLED_PRODUCTION") != "1" {
		t.Skip("opt-in production controlled sender/receiver native acceptance; requires separate OS-key and initial activation authorization")
	}
	ctx := keyboardProbeContext(t, 3*time.Minute) // Cancel registered before child EOF cleanup.
	started := time.Now()
	must := func(phase string, err error) {
		t.Helper()
		if err != nil {
			// Do not expose storage/session paths from wrapped errors.
			t.Fatalf("phase %s failed: %v; stop, do not refocus/retry", phase, err)
		}
	}
	must("input capability", platform.InputCapability())
	must("capture capability", platform.ControlCaptureCapability())
	for _, key := range []string{"RASTERDOCK_KEYBOARD_PROBE_HELPER", "RASTERDOCK_COLOR_BENCHMARK_HELPER", "RASTERDOCK_NATIVE_GUI_HELPER", "RASTERDOCK_NATIVE_TEST_VIEWER"} {
		t.Setenv(key, "") // Parent invocation must also sanitize before TestMain.
	}
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "output")
	want := controlledProductionFiles()
	for name, data := range want {
		path := filepath.Join(source, name)
		must("fixture directory", os.MkdirAll(filepath.Dir(path), 0700))
		must("fixture write", os.WriteFile(path, data, 0600))
	}
	must("destination", os.MkdirAll(destination, 0700))
	senderRoot := filepath.Join(root, "sender")
	child := startGeometryGUI(ctx, t, senderRoot)
	// Three planes per page makes this small fixture a multi-page manifest.
	// 1x1 avoids oversized desktop windows; this is not a geometry matrix.
	size := geometrySize(t, 1, 1, 4)
	child.fit(t, size)
	child.call(t, geometryRequest{Actions: []geometryAction{{"module", "4"}, {"grid", "1 × 1"}, {"colors", "8 colors — experimental"}}})
	child.call(t, geometryRequest{Source: &source, Tap: "Prepare"})
	prepared := child.wait(t, "production prepared", 15*time.Second, func(r geometryReply) bool {
		return geometryProfile(r, size, "1 × 1")
	})
	if !strings.HasPrefix(prepared.Title, "RasterDock · ") || !strings.HasSuffix(prepared.Title, fmt.Sprintf(" · Sender · PID %d", child.cmd.Process.Pid)) {
		t.Fatal("sender title does not identify owned child")
	}
	// Qualification ONLY: never supply Prepared, descriptors, packets or plans
	// to the receiver. All receiver decisions derive from captured pixels/store.
	paths, err := filepath.Glob(filepath.Join(senderRoot, "state", "transfers", "*", "send", "prepared"))
	must("prepared discovery", err)
	if len(paths) != 1 {
		t.Fatal("expected exactly one prepared fixture")
	}
	p, err := application.OpenPrepared(paths[0])
	must("fixture qualification", err)
	profile := application.DefaultVisualProfile(application.Colors8)
	if p.ManifestPackets() <= profile.Capacity() {
		t.Fatal("fixture does not span multiple manifest pages")
	}
	t.Logf("fixture files=%d manifest-packets=%d total-packets=%d page-capacity=%d", len(want), p.ManifestPackets(), p.PacketCount(), profile.Capacity())
	var target platform.Source
	sources, err := platform.Sources()
	must("owned source discovery", err)
	for _, s := range sources {
		// Sources prefixes the app name; compare the complete window title.
		_, title, ok := strings.Cut(s.Title, " — ")
		if (ok && title == prepared.Title) || s.Title == prepared.Title {
			if target.ID != 0 {
				t.Fatal("ambiguous owned child window")
			}
			target = s
		}
	}
	if target.ID == 0 {
		t.Fatal("owned child window missing")
	}
	guard := func(id uint32) error {
		if id != target.ID || child.cmd.Process == nil {
			return errors.New("target is not owned child")
		}
		select {
		case <-child.done:
			return errors.New("owned child exited")
		default:
		}
		list, e := platform.Sources()
		if e != nil {
			return e
		}
		for _, s := range list {
			if s == target {
				return nil
			}
		}
		return errors.New("owned source identity changed")
	}
	must("owned source", guard(target.ID))
	t.Logf("target child PID=%d window=%d; no automatic refocus", child.cmd.Process.Pid, target.ID)
	child.call(t, geometryRequest{Tap: "Controlled viewer (allow keyboard)"})
	// No helper control after arming. After receiver/input cleanup only read-only
	// restoration inspection is allowed, followed by EOF shutdown.
	must("native static held page", controlledProductionHold(ctx, target.ID, guard))
	commands := 0
	deps := engineIO{
		controlDecode: codec.DecodeControlled,
		controlCapture: func(id uint32) (captureStream, error) {
			if e := guard(id); e != nil {
				return nil, e
			}
			return platform.OpenControlCapture(id)
		},
		bindInput: func(id uint32) (platform.InputBinding, error) {
			if e := guard(id); e != nil {
				return nil, e
			}
			binding, e := platform.BindInput(id) // Pins native PID/start-time/AX identity.
			if e != nil {
				return nil, e
			}
			pinned := binding.Target()
			if !pinned.Valid() || pinned.Window != target.ID || pinned.PID != child.cmd.Process.Pid || pinned.Title != prepared.Title {
				return nil, errors.Join(errors.New("native input target is not the owned child"), binding.Close())
			}
			return &controlledProductionInput{InputBinding: binding, sent: &commands, logf: t.Logf}, nil
		},
		openReceive: anchoredReceive(root),
		// Keep a truly static page idle beyond the old one-second heartbeat age
		// before opening a new capture. This is not a focus action or two-GUI handoff.
		controlDelay: 1500 * time.Millisecond,
	}
	complete := false
	var published, total uint64
	err = runControlledReceive(ctx, root, command{window: target.ID, destination: destination}, nil, func(s snapshot) {
		if s.key == "status.receiving" {
			published, _ = s.params["published"].(uint64)
			total, _ = s.params["total"].(uint64)
		}
		complete = complete || s.key == "control.complete" || s.key == "control.complete_finish_unconfirmed"
		if s.key == "control.complete_finish_unconfirmed" {
			t.Log("files saved; terminal delivery or cleanup unconfirmed (no retry)")
		}
	}, deps)
	must("production receive", err)
	if !complete || commands == 0 || published != uint64(len(want)) || total != published {
		t.Fatalf("incomplete: complete=%v commands=%d published=%d/%d", complete, commands, published, total)
	}
	for name, expected := range want {
		actual, e := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Fixed fixture paths under t.TempDir.
		must("published read", e)
		if !bytes.Equal(actual, expected) || sha256.Sum256(actual) != sha256.Sum256(expected) {
			t.Fatal("published bytes/SHA mismatch")
		}
	}
	// Read-only helper inspection is allowed only AFTER receive/input ownership
	// has ended. It never drives completion or supplies a protocol acknowledgement.
	awaitControlledRestoration(t, child)
	child.close(t) // Normal EOF, not a page/control channel.
	t.Logf("production acceptance complete: completed-command-attempts=%d files=%d duration=%s; no decode oracle, NOT throughput/two-GUI evidence", commands, published, time.Since(started))
}

func awaitControlledRestoration(t *testing.T, child *geometryChild) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := child.call(t, geometryRequest{InspectRestored: true})
		if r.Restored && r.Unfocused {
			t.Log("sender settings restored unfocused after terminal Enter release")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("terminal command did not restore sender settings; no retry")
}

type controlledProductionInput struct {
	platform.InputBinding
	sent     *int
	attempts int
	logf     func(string, ...interface{})
}

func (i *controlledProductionInput) SendFinishWithRefresh(ctx context.Context, nonce uint64, refresh func(context.Context) error, eligible func() error) error {
	input, ok := i.InputBinding.(platform.FinishingInputBinding)
	if !ok {
		return errors.New("native binding lacks terminal completion capability")
	}
	i.logf("terminal attempt BEGIN nonce=%d; last sequence, no retry", nonce)
	err := input.SendFinishWithRefresh(ctx, nonce, refresh, eligible)
	if err != nil {
		i.logf("terminal attempt unconfirmed: %v; no retry", err)
	} else {
		i.logf("terminal attempt sent; remote restoration checked separately")
	}
	return err
}

func (i *controlledProductionInput) Send(ctx context.Context, nonce, ordinal uint64, eligible func() error) error {
	return i.attempt(nonce, ordinal, func() error { return i.InputBinding.Send(ctx, nonce, ordinal, eligible) })
}

func (i *controlledProductionInput) SendWithRefresh(ctx context.Context, nonce, ordinal uint64, refresh func(context.Context) error, eligible func() error) error {
	input, ok := i.InputBinding.(platform.RefreshingInputBinding)
	if !ok {
		return errors.New("native binding lacks bounded command capability")
	}
	return i.attempt(nonce, ordinal, func() error { return input.SendWithRefresh(ctx, nonce, ordinal, refresh, eligible) })
}

func (i *controlledProductionInput) attempt(nonce, ordinal uint64, send func() error) error {
	i.attempts++
	i.logf("command attempt BEGIN attempt=%d nonce=%d ordinal=%d", i.attempts, nonce, ordinal)
	if err := send(); err != nil {
		i.logf("command attempt FAILED attempt=%d nonce=%d ordinal=%d; keys/receipt unknown (failure may follow partial input)", i.attempts, nonce, ordinal)
		return err
	}
	*i.sent++ // Completed Send attempts only, not key counts or remote receipts.
	i.logf("command attempt COMPLETED attempt=%d nonce=%d ordinal=%d", i.attempts, nonce, ordinal)
	return nil
}

func controlledProductionFiles() map[string][]byte {
	want := map[string][]byte{"empty": {}, "目录/мир.txt": []byte("production controlled 世界\n")}
	for n := range 50 {
		data := make([]byte, 768)
		for offset := 0; offset < len(data); offset += sha256.Size {
			block := sha256.Sum256([]byte(fmt.Sprintf("controlled-production-%d-%d", n, offset)))
			copy(data[offset:], block[:])
		}
		want[fmt.Sprintf("file-%03d.bin", n)] = data
	}
	return want
}

func controlledProductionHold(ctx context.Context, id uint32, guard func(uint32) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = guard(id); err != nil {
		return fmt.Errorf("held.guard: %w", err)
	}
	input, err := platform.BindInput(id)
	if err != nil {
		return fmt.Errorf("held.bind: %w", err)
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	stream, err := platform.OpenControlCapture(id) //nolint:contextcheck // Production open API has no context parameter; Next uses the bounded context below.
	if err != nil {
		return fmt.Errorf("held.capture_open: %w", err)
	}
	// Successful Close is required before production opens its new generation.
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("held.capture_close: %w", closeErr))
		}
	}()
	var held controlledHeldState
	for {
		if err = input.Check(); err != nil {
			return fmt.Errorf("held.check: %w", err) // Never wait/retry a failed focus/native guard.
		}
		frame, e := stream.Next(ctx)
		if errors.Is(e, platform.ErrFramePending) && ctx.Err() == nil {
			continue
		}
		if e != nil {
			return fmt.Errorf("held.next: %w", e)
		}
		if done, e := held.observe(frame, nil); done || e != nil {
			return e
		}
	}
}

type controlledHeldState struct {
	first      v1.Beacon
	generation uint64
}

// Shared by the production held check and zero-key diagnostic; decoded payloads
// and helper state never substitute for captured page evidence.
func (s *controlledHeldState) observe(frame platform.Frame, decoded func(time.Duration)) (bool, error) {
	if !frame.ControlAgeBounded || frame.Generation == 0 || frame.ControlCapturedAt.IsZero() || time.Since(frame.ControlCapturedAt) < 0 || time.Since(frame.ControlCapturedAt) > time.Second {
		return false, errors.New("unbounded/stale native acquisition")
	}
	start := time.Now()
	b, _, err := codec.DecodeControlled(frame.Image)
	if decoded != nil {
		decoded(time.Since(start))
	}
	if err != nil {
		if s.first.Epoch == 0 && !errors.Is(err, codec.ErrAmbiguousControl) {
			return false, nil // Bounded initial render wait, never helper state.
		}
		return false, fmt.Errorf("held.decode: %w", err)
	}
	return s.accept(b, frame.Generation)
}

func (s *controlledHeldState) accept(b v1.Beacon, generation uint64) (bool, error) {
	if b.Flags&2 == 0 || b.Epoch == 0 || b.Ordinal != 0 || b.Nonce != 0 {
		return false, errors.New("viewer not armed/held")
	}
	if b.Colors != 8 || b.Columns != 1 || b.Rows != 1 || b.Module != 4 {
		return false, errors.New("unexpected native controlled profile")
	}
	if s.first.Epoch == 0 {
		s.first, s.generation = b, generation
		return true, nil // One valid held page; no periodic heartbeat is required.
	}
	if b.Epoch != s.first.Epoch || b.Descriptor != s.first.Descriptor || generation != s.generation || b.Frame < s.first.Frame {
		return false, errors.New("held stream identity/regression")
	}
	return b.Frame > s.first.Frame, nil
}
