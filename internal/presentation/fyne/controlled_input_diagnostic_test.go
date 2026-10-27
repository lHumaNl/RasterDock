//go:build integration && darwin

package fyneui

// NOTRUN: requires NEW authorization for initial real GUI activation and read-only
// AX/CG checks. Existing grants only: no permission prompt, refocus or retry.
// Sanitize all four helper flags BEFORE TestMain, not merely inside this test:
// env -u RASTERDOCK_KEYBOARD_PROBE_HELPER -u RASTERDOCK_COLOR_BENCHMARK_HELPER \
//   -u RASTERDOCK_NATIVE_GUI_HELPER -u RASTERDOCK_NATIVE_TEST_VIEWER \
//   RASTERDOCK_CONTROLLED_INPUT_DIAGNOSTIC=1 go test -tags=integration \
//   ./internal/presentation/fyne -run '^TestMacOSControlledInputDiagnostic$' -count=1 -timeout=4m -v
// No Send, receive engine, capture stream, injected keys or helper control calls
// after setup. Success proves only one Bind/Target/Check, not keyboard delivery.

import (
	"errors"
	"os"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
)

func TestMacOSControlledInputDiagnostic(t *testing.T) {
	if os.Getenv("RASTERDOCK_CONTROLLED_INPUT_DIAGNOSTIC") != "1" {
		t.Skip("requires opt-in and new initial GUI activation/read-only AX authorization")
	}
	clearInputDiagnosticEnvironment(t)
	ctx := keyboardProbeContext(t, 3*time.Minute) // Child EOF runs before cancellation.
	inputDiagnosticMust(t, "capability", platform.InputCapability())
	child, source, title := captureOnlyProductionSender(ctx, t)
	assertInputDiagnosticChildLive(t, child)
	binding, err := platform.BindInput(source.ID)
	inputDiagnosticMust(t, "bind", err)
	t.Cleanup(func() { inputDiagnosticMust(t, "close", binding.Close()) })
	pinned := binding.Target()
	if !pinned.Valid() || pinned.Window != source.ID || pinned.PID != child.cmd.Process.Pid || pinned.Title != title {
		t.Fatal("input target does not match exact owned child")
	}
	assertInputDiagnosticChildLive(t, child)
	inputDiagnosticMust(t, "check", binding.Check())
	inputDiagnosticMust(t, "close", binding.Close())
}

func assertInputDiagnosticChildLive(t *testing.T, child *geometryChild) {
	t.Helper()
	if child.cmd.Process == nil {
		t.Fatal("owned child process missing")
	}
	select {
	case <-child.done:
		t.Fatal("owned child exited")
	default:
	}
}

func inputDiagnosticMust(t *testing.T, phase string, err error) {
	t.Helper()
	if err == nil {
		t.Logf("input diagnostic %s passed; zero keys", phase)
		return
	}
	var diagnostic *platform.InputDiagnosticError
	if errors.As(err, &diagnostic) {
		t.Fatalf("input diagnostic %s: %s; stop, no retry/refocus", phase, diagnostic)
	}
	t.Fatalf("input diagnostic %s failed without typed reason; stop, no retry/refocus", phase)
}

func clearInputDiagnosticEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"RASTERDOCK_KEYBOARD_PROBE_HELPER", "RASTERDOCK_COLOR_BENCHMARK_HELPER",
		"RASTERDOCK_NATIVE_GUI_HELPER", "RASTERDOCK_NATIVE_TEST_VIEWER",
		"RASTERDOCK_CONTROLLED_PRODUCTION", "RASTERDOCK_CONTROLLED_CAPTURE",
		"RASTERDOCK_KEYBOARD_PROBE", "RASTERDOCK_GUI_GEOMETRY",
		"RASTERDOCK_COLOR_BENCHMARK", "RASTERDOCK_GRID_BENCHMARK",
		"RASTERDOCK_FRAME_TRACE", "RASTERDOCK_CONTROLLED_INPUT_DIAGNOSTIC",
	} {
		t.Setenv(key, "")
	}
}
