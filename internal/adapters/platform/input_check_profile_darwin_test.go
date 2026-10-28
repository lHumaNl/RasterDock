//go:build integration && darwin && cgo

package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInputCheckProfileScheduleBounds(t *testing.T) {
	for _, test := range []struct {
		index   uint
		elapsed time.Duration
		want    bool
	}{
		{0, 0, true}, {11, 3*time.Second - 1, true}, {12, 0, false},
		{0, 3 * time.Second, false}, {11, 3*time.Second + 1, false},
	} {
		if got := checkProfileCanStart(test.index, test.elapsed); got != test.want {
			t.Fatalf("%+v: %t", test, got)
		}
	}
}

// Reuse the existing production-focused policy harness's fake AX boundaries.
// No live AX/CG calls: only the fixture's first successful focused query is timed.
func TestInputCheckProfileNativeStageOrder(t *testing.T) {
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "timing.m"), filepath.Join(dir, "timing")
	needle := "reset(); check(RD_CODE_UNKNOWN, RD_STAGE_UNKNOWN, 0, (rd_input_diagnostic){0});"
	harness := strings.Replace(inputFocusHarness, needle, checkProfileOrderFixture, 1)
	if harness == inputFocusHarness {
		t.Fatal("fixture insertion point missing")
	}
	if err := os.WriteFile(source, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-fblocks", "-I", include, source, "-framework", "Cocoa", "-framework", "ApplicationServices", "-o", binary)
	runFocusCommand(t, binary)
}

const checkProfileOrderFixture = `
rd_input_timing timing = {.start_ns = 1};
rd_input_timing_attach(&timing);
reset(); check(RD_CODE_UNKNOWN, RD_STAGE_UNKNOWN, 0, (rd_input_diagnostic){0});
rd_input_timing_attach(NULL);
assert(timing.count == 2 && !timing.overflow);
for (unsigned i = 0; i < timing.count; i++) {
    assert(timing.records[i].stage == RD_STAGE_FOCUSED_WINDOW);
    assert(timing.records[i].op == (i % 2 ? RD_TIMING_AX_QUERY : RD_TIMING_TIMEOUT));
    assert(timing.records[i].has_ax_error && timing.records[i].ax_error == 0);
    assert(timing.records[i].offset_ns == 0 && timing.records[i].remaining_ns == 99);
}
`

func TestInputCheckProfileBufferBounds(t *testing.T) {
	for _, count := range []int{0, 1, 32, 33} {
		sample := checkProfileFixture(count)
		if len(sample.Records) != min(count, 32) || sample.Overflow != (count > 32) {
			t.Fatalf("count=%d: %+v", count, sample)
		}
		for i, r := range sample.Records {
			if r.Stage != 4 || r.Operation != 2 || !r.HasAXError || r.AXError != -25204 ||
				r.Offset != time.Duration(i) || r.Elapsed != 1 || r.Remaining != time.Duration(max(10-i, 0)) {
				t.Fatalf("record %d: %+v", i, r)
			}
		}
		if sample.Unattributed != time.Duration(100-min(count, 32)) {
			t.Fatal("incorrect unattributed total")
		}
	}
}

func TestInputCheckProfileStopClassification(t *testing.T) {
	for _, test := range []struct{ code, want int }{{0, 0}, {2, 0}, {11, 0}, {12, 0},
		{1, 4}, {3, 4}, {10, 4}, {17, 4}, {9, 1}, {15, 1}, {16, 1}, {6, 1}, {8, 1}, {7, 1}, {19, 1}} {
		if got := checkProfileStop(test.code, false); got != test.want {
			t.Errorf("code=%d got=%d want=%d", test.code, got, test.want)
		}
		if got := checkProfileStop(test.code, true); got != 5 {
			t.Errorf("overflow must dominate: %d", got)
		}
	}
}

func TestInputCheckProfileWaitDoesNotReleaseWorkerGate(t *testing.T) {
	checkProfileBusy.Store(true)
	defer checkProfileBusy.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := checkProfileWait(ctx, make(chan checkProfileResult))
	if !errors.Is(err, context.Canceled) || !checkProfileBusy.Load() {
		t.Fatal("waiter released in-flight gate")
	}
	_, err = ProbeInputCheckProfile(context.Background(), 1, 1)
	if !errors.Is(err, ErrCheckProfileBusy) {
		t.Fatal("busy probe must not start native work")
	}
}

func TestInputCheckProfileInvalidTarget(t *testing.T) {
	_, err := ProbeInputCheckProfile(context.Background(), 0, 0)
	var diagnostic CheckProfileError
	if !errors.As(err, &diagnostic) || diagnostic.Status != 1 {
		t.Fatalf("untyped invalid target: %v", err)
	}
}

func TestInputHeldProfileSameCallAttachment(t *testing.T) {
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "held.m"), filepath.Join(dir, "held")
	harness := strings.Replace(inputFocusHarness, "#undef CFRelease", "#undef CFRelease\n"+heldProfileNativeFixture, 1)
	harness = strings.Replace(harness, "int main(void) {", "int main(void) { held_profile_fixture();", 1)
	if err := os.WriteFile(source, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-fblocks", "-I", include, source, "-framework", "Cocoa", "-framework", "ApplicationServices", "-o", binary)
	runFocusCommand(t, binary)
}

// Only the Check boundary is fake. The actual integration C wrapper and actual
// thread-local timing implementation run; no AX/CG/process queries are executed.
const heldProfileNativeFixture = `
static rd_input fixture_binding;
static int fixture_ok;
static int fixture_check(rd_input *binding, rd_input_diagnostic *d) {
    assert(binding == &fixture_binding && inputTiming != NULL);
    for (unsigned i = 0; i <= RD_INPUT_TIMING_CAPACITY; i++)
        rd_input_timing_append(inputTiming, RD_STAGE_AX_TITLE, RD_TIMING_AX_QUERY, 1, 2, 100, 1, 0);
    inputTiming->ok = fixture_ok;
    d->code = fixture_ok ? RD_CODE_UNKNOWN : RD_CODE_BUDGET;
    return fixture_ok;
}
#define rd_input_check fixture_check
#include "input_check_profile_darwin.m"
#undef rd_input_check
static void held_profile_fixture(void) {
    for (fixture_ok = 0; fixture_ok <= 1; fixture_ok++) {
        rd_profile_sample sample;
        memset(&sample, 0xff, sizeof(sample));
        assert(rd_profile_check(&fixture_binding, &sample) == fixture_ok);
        assert(inputTiming == NULL);
        assert(sample.timing.count == RD_INPUT_TIMING_CAPACITY && sample.timing.overflow);
        assert(sample.diagnostic.code == (fixture_ok ? RD_CODE_UNKNOWN : RD_CODE_BUDGET));
    }
}
`
