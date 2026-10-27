//go:build integration && darwin && cgo

package fyneui

import (
	"os"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
)

// Lead-only, NOT RUN by the implementation owner. Sanitize BEFORE TestMain:
//
//	env -i HOME="$HOME" PATH="$PATH" TMPDIR="$TMPDIR" CGO_ENABLED=1 \
//	  RASTERDOCK_INPUT_CHECK_PROFILE=1 go test -tags=integration \
//	  ./internal/presentation/fyne -run '^TestMacOSControlledCheckProfile$' -count=1 -v
//
// No capture stream, receiver, helper commands after arm, or input Send.
func TestMacOSControlledCheckProfile(t *testing.T) {
	if os.Getenv("RASTERDOCK_INPUT_CHECK_PROFILE") != "1" {
		t.Skip("requires authorized read-only full Check profile")
	}
	clearInputDiagnosticEnvironment(t)
	t.Setenv("RASTERDOCK_AX_LATENCY", "")
	t.Setenv("RASTERDOCK_INPUT_CHECK_PROFILE", "")
	ctx := keyboardProbeContext(t, 3*time.Minute)
	child, source, _ := captureOnlyProductionSender(ctx, t)
	assertInputDiagnosticChildLive(t, child)
	batch, err := platform.ProbeInputCheckProfile(ctx, child.cmd.Process.Pid, source.ID)
	logCheckProfile(t, batch)
	if err != nil {
		t.Fatalf("read-only Check profile: %v; no retry/refocus", err)
	}
	if len(batch.Samples) != 12 {
		t.Fatal("incomplete measurement schedule")
	}
}

func logCheckProfile(t *testing.T, batch platform.CheckProfileBatch) {
	t.Helper()
	accepted := 0
	t.Logf("bind=%+v; operations: 1=timeout 2=AX-query 3=CG-list 4=capability 5=local", batch.BindDiagnostic)
	for i, sample := range batch.Samples {
		if sample.OK {
			accepted++
		}
		t.Logf("seq=%d ok=%t overflow=%t total_ns=%d unattributed_ns=%d diagnostic=%+v",
			i+1, sample.OK, sample.Overflow, sample.Elapsed, sample.Unattributed, sample.Diagnostic)
		for _, record := range sample.Records {
			t.Logf("seq=%d record=%+v", i+1, record)
		}
	}
	t.Logf("measured=%d accepted=%d refused=%d; PASS means measurement complete, NOT input validation; sequential cold/warm confounded",
		len(batch.Samples), accepted, len(batch.Samples)-accepted)
}
