//go:build integration && darwin

package fyneui

import (
	"os"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
)

// NOTRUN: the lead owns the single authorized activation and read-only batch.
func TestMacOSAXLatencyDiagnostic(t *testing.T) {
	if os.Getenv("RASTERDOCK_AX_LATENCY") != "1" {
		t.Skip("requires authorized own-window activation and six read-only queries")
	}
	clearInputDiagnosticEnvironment(t)
	ctx := keyboardProbeContext(t, 3*time.Minute)
	child, source, _ := captureOnlyProductionSender(ctx, t)
	setupCompleted := platform.AXLatencyPreparation()
	assertInputDiagnosticChildLive(t, child)
	batch, err := platform.ProbeAXLatency(ctx, child.cmd.Process.Pid, source.ID, setupCompleted)
	logAXLatencyBatch(t, batch)
	if err != nil {
		t.Fatalf("AX latency: %v; stop without retry/refocus", err)
	}
	if batch.Completed != len(batch.Samples) {
		t.Fatal("incomplete AX batch")
	}
}

func logAXLatencyBatch(t *testing.T, batch platform.AXLatencyBatch) {
	t.Helper()
	t.Logf("completed=%d; only completed entries contain query results; API 1=ranged17 2=single; sequential warmup confound; 60Hz hypothesis unproven", batch.Completed)
	for _, sample := range batch.Samples {
		t.Logf("seq=%d api=%d timeout_ms=%d setup_ax=%d query_elapsed_ns=%d query_ax=%d value=%t array=%t count=%d setup_completed_to_query_ns=%d",
			sample.Sequence, sample.API, sample.TimeoutMS, sample.SetupAXError, sample.Elapsed.Nanoseconds(),
			sample.QueryAXError, sample.ValuePresent, sample.IsArray, sample.Count, sample.SetupCompletedAge.Nanoseconds())
	}
}
