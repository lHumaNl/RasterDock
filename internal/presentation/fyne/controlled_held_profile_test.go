//go:build integration && darwin && cgo

package fyneui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
)

// NOTRUN by implementation owner. Lead authorization and security review required.
// Sanitize BEFORE TestMain (which otherwise honors inherited helper switches):
//
//	env -i HOME="$HOME" PATH="$PATH" TMPDIR="$TMPDIR" CGO_ENABLED=1 \
//	  RASTERDOCK_HELD_CHECK_PROFILE=1 go test -tags=integration \
//	  ./internal/presentation/fyne -run '^TestMacOSControlledHeldCheckProfile$' -count=1 -timeout=4m -v
//
// No receiver, Send, keys, refocus, warmup, sleep or retry. Context is 10s for
// held work, not OS cancellation: synchronous native owners survive until return.
func TestMacOSControlledHeldCheckProfile(t *testing.T) {
	if os.Getenv("RASTERDOCK_HELD_CHECK_PROFILE") != "1" {
		t.Skip("requires authorized zero-key capture-active Check diagnostic")
	}
	clearHeldProfileEnvironment(t)
	ctx := keyboardProbeContext(t, 3*time.Minute)
	inputDiagnosticMust(t, "input capability", platform.InputCapability())
	inputDiagnosticMust(t, "capture capability", platform.ControlCaptureCapability())
	child, source, title := captureOnlyProductionSender(ctx, t)
	assertInputDiagnosticChildLive(t, child)
	ctx, cancel := context.WithTimeout(ctx, platform.HeldProfileTimeout)
	defer cancel()
	profile := bindOwnedHeldProfile(t, child, source.ID, title)
	defer func() { inputDiagnosticMust(t, "input close", profile.Close()) }()
	runCapturedHeldProfile(ctx, t, profile, source.ID)
}

func clearHeldProfileEnvironment(t *testing.T) {
	t.Helper()
	clearInputDiagnosticEnvironment(t)
	for _, key := range []string{"RASTERDOCK_HELD_CHECK_PROFILE", "RASTERDOCK_INPUT_CHECK_PROFILE", "RASTERDOCK_AX_LATENCY"} {
		t.Setenv(key, "")
	}
}

func bindOwnedHeldProfile(t *testing.T, child *geometryChild, window uint32, title string) *platform.HeldCheckProfile {
	t.Helper()
	profile, err := platform.BindHeldCheckProfile(child.cmd.Process.Pid, window)
	inputDiagnosticMust(t, "held.bind", err)
	pinned := profile.Target()
	if !pinned.Valid() || pinned.PID != child.cmd.Process.Pid || pinned.Window != window || pinned.Title != title {
		_ = profile.Close()
		t.Fatal("held binding target differs from owned child")
	}
	return profile
}

type heldProfileCapture struct {
	stream platform.Stream
	held   controlledHeldState
	steps  []heldProfileStep
}

type heldProfileStep struct {
	Next, Decode time.Duration
	Decoded      bool
}

func runCapturedHeldProfile(ctx context.Context, t *testing.T, profile *platform.HeldCheckProfile, window uint32) {
	t.Helper()
	if err := ctx.Err(); err != nil {
		t.Fatal("held deadline expired before capture open")
	}
	stream, err := platform.OpenControlCapture(window) //nolint:contextcheck // Native open has no context; ownership stays synchronous.
	ready := time.Now()
	if err != nil {
		inputDiagnosticMust(t, "held.capture_open", err)
	}
	defer func() { inputDiagnosticMust(t, "held.capture_close", stream.Close()) }()
	capture := heldProfileCapture{stream: stream, steps: make([]heldProfileStep, 0, platform.HeldProfileLimit)}
	err = profile.Run(ctx, func() (bool, error) { return capture.step(ctx) })
	logHeldProfile(t, ready, profile.Samples(), capture.steps)
	logHeldProfileStop(t, err)
	inputDiagnosticMust(t, "held.sample_completion", err)
	t.Log("diagnostic PASS: valid static held observation; zero keys; NOT transfer/input-delivery evidence")
}

func logHeldProfileStop(t *testing.T, err error) {
	t.Helper()
	var diagnostic platform.CheckProfileError
	switch {
	case errors.As(err, &diagnostic):
		t.Logf("held stop: native profile status=%d", diagnostic.Status)
	case errors.Is(err, platform.ErrHeldProfileLimit):
		t.Log("held stop: sample cap; no held completion")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		t.Log("held stop: context ended; OS calls cannot be cancelled")
	}
}

func (p *heldProfileCapture) step(ctx context.Context) (bool, error) {
	start := time.Now()
	frame, err := p.stream.Next(ctx)
	sample := heldProfileStep{Next: time.Since(start)}
	defer func() { p.steps = append(p.steps, sample) }()
	if errors.Is(err, platform.ErrFramePending) && ctx.Err() == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.held.observe(frame, func(elapsed time.Duration) {
		sample.Decode, sample.Decoded = elapsed, true
	})
}

func logHeldProfile(t *testing.T, ready time.Time, samples []platform.CheckProfileSample, steps []heldProfileStep) {
	t.Helper()
	nextCount, decodeCount := 0, 0
	for i, sample := range samples {
		var step heldProfileStep
		priorNext, priorDecode := nextCount, decodeCount
		if i < len(steps) {
			step = steps[i]
			nextCount++
			if step.Decoded {
				decodeCount++
			}
		}
		t.Logf("checkindex=%d since_capture_ready_ns=%d check_wall_ns=%d prior_next=%d prior_decode=%d next_count=%d decode_count=%d next_ns=%d decode_ns=%d ok=%t overflow=%t native_ns=%d unattributed_ns=%d diagnostic=%+v",
			i+1, sample.CheckStarted.Sub(ready), sample.CheckElapsed, priorNext, priorDecode, nextCount, decodeCount, step.Next, step.Decode, sample.OK, sample.Overflow, sample.Elapsed, sample.Unattributed, sample.Diagnostic)
		for _, record := range sample.Records {
			t.Logf("checkindex=%d record=%+v", i+1, record)
		}
	}
}
