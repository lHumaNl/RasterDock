//go:build darwin && cgo

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Compile production code with its OS query boundary replaced. CF strings model
// validated AX identities, not real AX identity semantics. No live APIs execute.
func TestInputNativePinnedFocusPolicy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "focus.m")
	if err := os.WriteFile(source, []byte(inputFocusHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "focus")
	runFocusCommand(t, "clang", "-fblocks", "-I", include, source, "-framework", "Cocoa", "-framework", "ApplicationServices", "-o", binary)
	runFocusCommand(t, binary)
}

func runFocusCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	// #nosec G204 -- Only clang and the test-owned temporary executable are run.
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, output)
	}
}

func TestInputFocusPolicyRetainsBindAndCGGuards(t *testing.T) {
	data, err := os.ReadFile("input_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	for _, guard := range []string{
		"b->axWindow = unique_ax_window(b, app, deadline, d)",
		"static const CFIndex maxAXWindows = 16;",
		"if (!title) { matches = 0; break; }",
		"if (matches != 1) return refuse(d, RD_STAGE_CG_UNIQUE, RD_CODE_NOT_UNIQUE);",
		"!local_gate(b, deadline, d, RD_STAGE_LOCAL_FIRST) || !owned_unique_window(b, deadline, d) || !focused_window(b, deadline, d)",
		"!local_gate(b, deadline, d, RD_STAGE_LOCAL_SECOND)",
	} {
		if !strings.Contains(string(data), guard) {
			t.Errorf("missing preserved guard: %s", guard)
		}
	}
}

const inputFocusHarness = `
#import <Cocoa/Cocoa.h>
#import <ApplicationServices/ApplicationServices.h>
#include <time.h>
#include <assert.h>
#include "input_darwin.h"
static uint64_t now = 1;
static const uint64_t deadline = 100;
static CFTypeRef app, pinned, other, title, changed, wrongType;
static CFTypeRef focusValue, titleValue;
static int focusCalls, titleCalls, outstanding;
static AXError focusError, titleError;
static int lateStage, lateCleanup;
static CFTypeRef acquire(CFTypeRef value) {
    if (value) { outstanding++; CFRetain(value); }
    return value;
}
static void release_value(CFTypeRef value) {
    assert(value && outstanding > 0); outstanding--; CFRelease(value);
    if (lateCleanup) now = deadline;
}
static AXUIElementRef create_app(pid_t pid) {
    assert(pid == 42); return (AXUIElementRef)acquire(app);
}
static uint64_t fake_clock(clockid_t clock) { (void)clock; return now; }
static AXError timeout_set(AXUIElementRef element, float seconds) {
    assert(element && seconds > 0); return kAXErrorSuccess;
}
static AXError query(AXUIElementRef element, CFStringRef attribute, CFTypeRef *out) {
    if (CFEqual(attribute, kAXFocusedWindowAttribute)) {
        assert(CFEqual(element, app)); focusCalls++;
        *out = acquire(focusValue);
        if (lateStage == RD_STAGE_FOCUSED_WINDOW) now = deadline;
        return focusError;
    }
    assert(CFEqual(attribute, kAXTitleAttribute));
    assert(CFEqual(element, pinned)); titleCalls++;
    *out = acquire(titleValue);
    if (lateStage == RD_STAGE_AX_TITLE) now = deadline;
    return titleError;
}
static AXError forbidden_windows(AXUIElementRef e, CFStringRef a, CFIndex i, CFIndex n, CFArrayRef *out) {
    (void)e; (void)a; (void)i; (void)n; (void)out;
    assert(!"Post-bind AXWindows enumeration is forbidden"); return kAXErrorFailure;
}
#define AXUIElementCreateApplication create_app
#define AXUIElementGetTypeID CFStringGetTypeID
#define AXUIElementSetMessagingTimeout timeout_set
#define AXUIElementCopyAttributeValue query
#define AXUIElementCopyAttributeValues forbidden_windows
#define clock_gettime_nsec_np fake_clock
#define CFRelease release_value
#include "input_darwin.m"
#undef CFRelease
static void reset(void) {
    assert(outstanding == 0);
    now = 1; focusCalls = titleCalls = 0;
    focusValue = pinned; titleValue = title;
    focusError = titleError = kAXErrorSuccess;
    lateStage = lateCleanup = 0;
}
static void check(rd_input_code code, rd_input_stage stage, int titles, rd_input_diagnostic initial) {
    rd_input b = {.pid = 42, .axWindow = (AXUIElementRef)pinned, .title = (CFStringRef)title};
    rd_input_diagnostic first = initial;
    int ok = focused_window(&b, deadline, &initial);
    assert(ok == (code == RD_CODE_UNKNOWN));
    assert(initial.code == code && initial.stage == stage);
    if (first.code != RD_CODE_UNKNOWN) {
        assert(initial.has_ax_error == first.has_ax_error && initial.ax_error == first.ax_error);
    }
    assert(focusCalls == 1 && titleCalls == titles && outstanding == 0);
    assert(CFGetRetainCount(app) == 1 && CFGetRetainCount(pinned) == 1);
    assert(CFGetRetainCount(other) == 1 && CFGetRetainCount(title) == 1);
    assert(CFGetRetainCount(changed) == 1 && CFGetRetainCount(wrongType) == 1);
}
static void failure_cases(rd_input_stage stage) {
    rd_input_code codes[] = {RD_CODE_NULL, RD_CODE_TYPE, RD_CODE_QUERY, RD_CODE_BUDGET};
    for (unsigned i = 0; i < sizeof(codes)/sizeof(codes[0]); i++) {
        reset();
        CFTypeRef value = codes[i] == RD_CODE_NULL ? NULL : codes[i] == RD_CODE_TYPE ? wrongType :
            stage == RD_STAGE_AX_TITLE ? title : pinned;
        if (stage == RD_STAGE_AX_TITLE) { titleValue = value; titleError = codes[i] == RD_CODE_QUERY ? kAXErrorCannotComplete : 0; }
        else { focusValue = value; focusError = codes[i] == RD_CODE_QUERY ? kAXErrorCannotComplete : 0; }
        if (codes[i] == RD_CODE_BUDGET) lateStage = stage;
        check(codes[i], stage, stage == RD_STAGE_AX_TITLE, (rd_input_diagnostic){0});
    }
}
static CFStringRef string(const char *s) {
    return CFStringCreateWithCString(NULL, s, kCFStringEncodingUTF8);
}
int main(void) {
    app = string("application identity"); pinned = string("pinned identity"); other = string("other identity");
    title = string("bound title"); changed = string("changed title");
    wrongType = CFArrayCreateMutable(NULL, 0, &kCFTypeArrayCallBacks);
    reset(); check(RD_CODE_UNKNOWN, RD_STAGE_UNKNOWN, 1, (rd_input_diagnostic){0});
    reset(); focusValue = other; check(RD_CODE_FOCUS_IDENTITY, RD_STAGE_FOCUSED_WINDOW, 0, (rd_input_diagnostic){0});
    reset(); titleValue = changed; check(RD_CODE_TITLE, RD_STAGE_AX_TITLE, 1, (rd_input_diagnostic){0});
    failure_cases(RD_STAGE_FOCUSED_WINDOW); failure_cases(RD_STAGE_AX_TITLE);
    reset(); lateCleanup = 1; check(RD_CODE_BUDGET, RD_STAGE_FOCUSED_WINDOW, 1, (rd_input_diagnostic){0});
    reset(); focusValue = other;
    check(RD_CODE_QUERY, RD_STAGE_AX_TITLE, 0, (rd_input_diagnostic){RD_STAGE_AX_TITLE, RD_CODE_QUERY, 1, kAXErrorCannotComplete});
    CFRelease(app); CFRelease(pinned); CFRelease(other); CFRelease(title); CFRelease(changed); CFRelease(wrongType);
    return 0;
}
`
