//go:build integration && darwin && cgo

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryTimestampOpenError(t *testing.T) {
	_, err := OpenRecoveryTimestampProbe(nil, 0, "private-title-must-not-escape")
	var opened *RecoveryTimestampOpenError
	if !errors.As(fmt.Errorf("setup: %w", err), &opened) || opened.Phase != RecoveryTimestampOpenFixture {
		t.Fatalf("missing typed fixture phase: %v", err)
	}
	if strings.Contains(err.Error(), "private-title") {
		t.Fatal("metadata leaked")
	}
	cause := InputDiagnosticError{Stage: InputStageAXTitle, Code: InputCodeQuery, HasAXError: true, AXError: -25204}
	wrapped := fmt.Errorf("setup: %w", &RecoveryTimestampOpenError{Phase: RecoveryTimestampOpenBind, Diagnostic: cause})
	var native *InputDiagnosticError
	if !errors.As(wrapped, &native) || *native != cause {
		t.Fatal("first native cause lost")
	}
}

func TestRecoveryTimestampOpenFake(t *testing.T) {
	body := timestampSource(t, "recovery_timestamp_probe_darwin.m", "struct rd_timestamp_probe", "static int timestamp_eligible")
	closeBody := timestampSource(t, "recovery_timestamp_probe_darwin.m", "void rd_timestamp_close", "// Process-lifetime")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "open.c"), filepath.Join(dir, "open")
	if err := os.WriteFile(source, []byte(timestampOpenPrefix+body+closeBody+timestampOpenMain), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-std=c11", "-Wall", "-Wextra", "-Werror", "-fsanitize=address", "-I", include, source, "-o", binary)
	runFocusCommand(t, binary)
}

const timestampOpenPrefix = `
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "recovery_timestamp_probe_darwin.h"
#include <assert.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
struct rd_input { int unused; };
static rd_input binding;
static int mode, owners, binds, copies, closes, live;
static uint64_t fake_clock(clockid_t clock) { (void)clock; return 1; }
static void *fake_calloc(size_t n, size_t size) {
    if (mode == 1) return NULL;
    void *p = calloc(n, size); assert(p); live++; return p;
}
static void fake_free(void *p) { if (p) { live--; free(p); } }
static int rd_profile_owner(int pid, uint32_t window, uint64_t *started) {
    assert(pid == 42 && window == 7); owners++; *started = 9; return mode != 3;
}
static void first_cause(rd_input_diagnostic *d) {
    assert(d->code == RD_CODE_UNKNOWN);
    *d = (rd_input_diagnostic){RD_STAGE_AX_TITLE, RD_CODE_QUERY, 1, -25204};
}
rd_input *rd_input_bind(uint32_t window, rd_input_diagnostic *d) {
    assert(window == 7 && owners == 1); binds++;
    if (mode == 4) { first_cause(d); return NULL; } return &binding;
}
int rd_input_copy_target(const rd_input *b, rd_input_target *out, rd_input_diagnostic *d) {
    assert(b == &binding && binds == 1); copies++;
    if (mode == 5) { first_cause(d); return 0; }
    char *title = malloc(3); assert(title); memcpy(title, "abc", 3); live++;
    *out = (rd_input_target){.pid=42,.window=7,.started=9,.title=title,.title_length=3};
    if (mode == 6) out->pid++;
    if (mode == 7) out->window++;
    if (mode == 8) out->started++;
    if (mode == 9) title[0] = 'x';
    return 1;
}
void rd_input_close(rd_input *b) { if (b) { assert(b == &binding); closes++; } }
#define calloc fake_calloc
#define free fake_free
#define clock_gettime_nsec_np fake_clock
`

const timestampOpenMain = `
int main(void) {
    const rd_timestamp_open_phase expected[] = {RD_TIMESTAMP_OPEN_READY,
        RD_TIMESTAMP_OPEN_ALLOCATION, RD_TIMESTAMP_OPEN_ARGUMENT, RD_TIMESTAMP_OPEN_OWNER_PIN,
        RD_TIMESTAMP_OPEN_BIND, RD_TIMESTAMP_OPEN_TARGET_COPY, RD_TIMESTAMP_OPEN_TARGET_MISMATCH,
        RD_TIMESTAMP_OPEN_TARGET_MISMATCH, RD_TIMESTAMP_OPEN_TARGET_MISMATCH, RD_TIMESTAMP_OPEN_TARGET_MISMATCH};
    for (mode = 0; mode < 10; mode++) {
        atomic_store(&timestampOpened, 0); owners = binds = copies = closes = 0;
        assert(live == 0); rd_timestamp_open_result result = {0};
        rd_timestamp_probe *p = rd_timestamp_open(42, 7, mode == 2 ? NULL : "abc", &result);
        assert(result.phase == expected[mode] && (p != NULL) == (mode == 0));
        assert(owners == (mode == 1 || mode == 2 ? 0 : 1));
        assert(binds == (mode >= 1 && mode <= 3 ? 0 : 1));
        assert(copies == (mode >= 1 && mode <= 4 ? 0 : 1));
        if (mode == 4 || mode == 5) {
            assert(result.diagnostic.stage == RD_STAGE_AX_TITLE && result.diagnostic.code == RD_CODE_QUERY);
            assert(result.diagnostic.has_ax_error && result.diagnostic.ax_error == -25204);
        } else assert(result.diagnostic.code == RD_CODE_UNKNOWN);
        rd_timestamp_close(p);
        assert(live == 0 && closes == (mode == 0 || mode >= 5 ? 1 : 0));
        int priorOwners = owners;
        assert(!rd_timestamp_open(42, 7, "abc", &result));
        assert(result.phase == RD_TIMESTAMP_OPEN_ALREADY_ATTEMPTED && owners == priorOwners && live == 0);
    }
    return 0;
}
`

func TestRecoveryTimestampManifest(t *testing.T) {
	manifest := RecoveryTimestampManifest()
	if len(manifest) != 6 || RecoveryTimestampBaselinePosts != 6 {
		t.Fatal("manifest budget changed")
	}
	for i, entry := range manifest {
		if strings.Contains(entry, "UNQUALIFIED") != (i >= 3) {
			t.Fatal("backlog must not masquerade as measured")
		}
	}
}

func TestRecoveryTimestampClassification(t *testing.T) {
	for _, tc := range []struct {
		pre   uint64
		stamp float64
		seen  bool
		want  string
	}{
		{0, 0, false, "UNQUALIFIED:missing-or-failed"},
		{0, 0, true, "observed:zero-or-invalid"},
		{0, 20, true, "observed:rewritten"},
		{1, 20, true, "observed:rewritten"},
		{1, 1e-9, true, "observed:before-cutoff"},
		{20e9, 20, true, "observed:at-or-after-cutoff"},
	} {
		got := ClassifyRecoveryTimestamp(RecoveryTimestampPost{Prepost: tc.pre}, RecoveryTimestampObservation{Seen: tc.seen, Timestamp: tc.stamp, Cutoff: 10})
		if got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func TestRecoveryTimestampClosedOwnership(t *testing.T) {
	p := &RecoveryTimestampProbe{}
	p.Close()
	p.Close()
	if _, err := p.Post(0); err == nil {
		t.Fatal("closed binding posted")
	}
	if recoveryTimestampChild(nil, "") {
		t.Fatal("nil child accepted")
	}
}

func TestRecoveryTimestampSetterRewrite(t *testing.T) {
	for _, slot := range []uint{2, 3, 4, 5} {
		post := RecoveryTimestampPost{Prepost: 99, Slot: slot}
		o := RecoveryTimestampObservation{Seen: true, Timestamp: 99}
		if got := ClassifyRecoveryTimestamp(post, o); got != "observed:rewritten-before-post" {
			t.Fatalf("slot=%d: %s", slot, got)
		}
	}
}

func timestampSource(t *testing.T, path, first, last string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // Fixed repository fixture paths supplied only by these tests.
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start, end := strings.Index(text, first), strings.Index(text, last)
	if start < 0 || end <= start {
		t.Fatal("native fixture extraction failed")
	}
	return text[start:end]
}

// The exact production functions compile against a fake C boundary. This binary
// links no Cocoa/CG/AX framework and cannot post an event or inspect the desktop.
func TestRecoveryTimestampNativeFake(t *testing.T) {
	gate := timestampSource(t, "input_darwin.m", "static int final_post_gate", "int rd_input_key")
	probe := timestampSource(t, "input_darwin.m", "static CGEventRef timestamp_event", "#endif\n\n// Main-thread")
	worker := timestampSource(t, "recovery_timestamp_probe_darwin.m", "static int timestamp_eligible", "void rd_timestamp_close")
	text := timestampFakePrefix + gate + probe + timestampFakeWorker + worker + timestampFakeMain
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "timestamp.c"), filepath.Join(dir, "timestamp")
	if err := os.WriteFile(source, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-std=c11", "-Wall", "-Wextra", "-Werror", "-I", include, source, "-o", binary)
	runFocusCommand(t, binary)
}

func TestRecoveryTimestampDropFake(t *testing.T) {
	drop := timestampSource(t, "recovery_timestamp_probe_darwin.m", "static NSEvent *timestamp_drop", "static int timestamp_watch_focus")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "drop.m"), filepath.Join(dir, "drop")
	if err := os.WriteFile(source, []byte(timestampDropPrefix+drop+timestampDropMain), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-fblocks", "-Wall", "-Wextra", "-Werror", "-I", include, source, "-framework", "Cocoa", "-o", binary)
	runFocusCommand(t, binary)
}

// The production target copy is length-delimited, not NUL-terminated. ASan
// ensures identity comparison cannot read past an exact-size title allocation.
func TestRecoveryTimestampBoundTitle(t *testing.T) {
	bound := timestampSource(t, "recovery_timestamp_probe_darwin.m", "static int timestamp_bound", "rd_timestamp_probe *rd_timestamp_open")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "title.c"), filepath.Join(dir, "title")
	if err := os.WriteFile(source, []byte(timestampTitlePrefix+bound+timestampTitleMain), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-fsanitize=address", "-I", include, source, "-o", binary)
	runFocusCommand(t, binary)
}

const timestampTitlePrefix = `
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "recovery_timestamp_probe_darwin.h"
#include <assert.h>
#include <stdlib.h>
#include <string.h>
struct rd_timestamp_probe { rd_input *binding; int pid; uint32_t window; uint64_t identity; };
static int extra;
int rd_input_copy_target(const rd_input *b, rd_input_target *out, rd_input_diagnostic *d) {
    (void)b; (void)d;
    char *title = malloc(3 + extra); memcpy(title, "abc", 3);
    if (extra) title[3] = 'X';
    *out = (rd_input_target){.pid=42,.window=7,.started=9,.title=title,.title_length=3};
    return 1;
}
`

const timestampTitleMain = `
int main(void) {
    rd_timestamp_probe p = {.pid=42,.window=7,.identity=9};
    rd_timestamp_open_result result = {0};
    assert(timestamp_bound(&p, "abc", &result));
    assert(!timestamp_bound(&p, "ab", &result));
    assert(!timestamp_bound(&p, "abcd", &result));
    assert(!timestamp_bound(&p, "abd", &result));
    extra = 1;
    assert(timestamp_bound(&p, "abc", &result));
    assert(!timestamp_bound(&p, "abcX", &result));
    p.pid++; assert(!timestamp_bound(&p, "abc", &result));
    return 0;
}
`

const timestampFakePrefix = `
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "input_darwin.h"
#include <assert.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>
struct rd_input { int pid; uint64_t deadline; unsigned timestampSlot; int timestampStopped; };
typedef struct { uint64_t stamp; bool down; } Event;
typedef Event *CGEventRef;
static uint64_t now = 1;
static int localOK = 1, late, posts, allocations, releases, allocationFail, localCalls;
static const unsigned keyHoldMicroseconds = 8000;
static int refuse(rd_input_diagnostic *d, rd_input_stage s, rd_input_code c) {
    if (d && !d->code) { d->stage = s; d->code = c; } return 0;
}
static int local_gate(rd_input *b, uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    assert(b && b->deadline == 0 && stage == RD_STAGE_PRE_POST); localCalls++;
    if (!localOK) return refuse(d, stage, RD_CODE_FRONTMOST);
    if (late) now = deadline;
    return 1;
}
static int in_budget(uint64_t deadline) { return deadline && now < deadline; }
static CGEventRef CGEventCreateKeyboardEvent(void *source, unsigned key, bool down) {
    assert(!source && key == RD_TIMESTAMP_KEY); allocations++;
    if (allocations == allocationFail) return NULL;
    Event *e = malloc(sizeof(*e)); assert(e); *e = (Event){now, down}; return e;
}
static void CGEventSetFlags(CGEventRef e, unsigned flags) { assert(e && flags == 0); }
static uint64_t CGEventGetTimestamp(CGEventRef e) { return e->stamp; }
static void CGEventSetTimestamp(CGEventRef e, uint64_t stamp) { e->stamp = stamp; }
static void CGEventPostToPid(int pid, CGEventRef e) { assert(pid == 42 && e); posts++; }
static void CFRelease(CGEventRef e) { assert(e); releases++; free(e); }
static void usleep(unsigned delay) { assert(delay == keyHoldMicroseconds); }
int rd_input_check(rd_input *b, rd_input_diagnostic *d) { (void)b; (void)d; return 1; }
`

const timestampFakeWorker = `
#define RD_TIMESTAMP_TOTAL_NS 10000000000ULL
#define RD_TIMESTAMP_SEQUENCE_NS 2000000000ULL
typedef struct { rd_input *binding; int pid, stopped; uint32_t window;
    uint64_t identity, start, sequence; unsigned next; } rd_timestamp_probe;
static int ownerOK = 1, prepareOK = 1, aliveCalls, ownerCalls, prepareCalls;
static uint64_t timestamp_now(void) { return now; }
static int rd_profile_alive(int pid, uint64_t identity) {
    assert(pid == 42 && identity == 7); aliveCalls++; return ownerOK;
}
static int rd_profile_owner(int pid, uint32_t window, uint64_t *identity) {
    assert(pid == 42 && window == 8); ownerCalls++; *identity = 7; return ownerOK;
}
uint64_t rd_input_deadline(uint64_t budget) { assert(budget == RD_INPUT_CHECK_BUDGET_NS); return now + budget; }
int rd_input_prepare(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    assert(aliveCalls == prepareCalls + 1 && ownerCalls == prepareCalls + 1);
    prepareCalls++; b->deadline = deadline;
    if (!prepareOK) return refuse(d, RD_STAGE_PREPARE, RD_CODE_QUERY);
    return 1;
}
static void reset(void) {
    now = 1; localOK = ownerOK = prepareOK = 1;
    late = posts = allocations = releases = allocationFail = localCalls = 0;
    aliveCalls = ownerCalls = prepareCalls = 0;
}
static rd_timestamp_probe owner(rd_input *b) {
    return (rd_timestamp_probe){.binding=b, .pid=42, .window=8, .identity=7, .start=1};
}
`

const timestampFakeMain = `
static void production_equivalence(void) {
    for (int mode = 0; mode < 5; mode++) {
        reset(); rd_input b = {.pid=42, .deadline=100}; rd_input_diagnostic d = {0};
        if (mode == 1) localOK = 0;
        if (mode == 2) late = 1;
        if (mode >= 3) allocationFail = mode - 2;
        assert(post_key(&b, RD_TIMESTAMP_KEY, &d) == (mode == 0));
        assert(b.deadline == 0 && posts == (mode == 0 ? 2 : 0));
        assert(allocations == 2 && releases == (mode >= 3 ? 1 : 2));
        assert(localCalls == (mode >= 3 ? 0 : 1));
        if (mode == 1) assert(d.code == RD_CODE_FRONTMOST);
        if (mode == 2) assert(d.code == RD_CODE_BUDGET);
        if (mode >= 3) assert(d.code == RD_CODE_ALLOCATION);
    }
}
static void manifest(void) {
    reset(); rd_input b = {.pid=42}; rd_timestamp_probe p = owner(&b);
    rd_timestamp_post out = {0}; rd_input_diagnostic d = {0};
    for (unsigned slot = 0; slot < RD_TIMESTAMP_BASELINE_POSTS; slot++) {
        assert(rd_timestamp_send(&p, slot, &out, &d));
        assert(posts == (int)slot+1 && prepareCalls == posts && localCalls == posts);
        assert(out.creation == now && out.prepost == (slot < 4 ? 1 : 0));
        assert(b.deadline == 0 && releases == allocations);
    }
    assert(!rd_timestamp_send(&p, 6, &out, &d) && p.stopped && posts == 6);
}
static void abort_release(void) {
    for (int failure = 0; failure < 5; failure++) {
        reset(); rd_input b = {.pid=42}; rd_timestamp_probe p = owner(&b);
        rd_timestamp_post out; rd_input_diagnostic d = {0};
        assert(rd_timestamp_send(&p, 0, &out, &d));
        if (failure == 0) prepareOK = 0;
        if (failure == 1) ownerOK = 0;
        if (failure == 2) localOK = 0;
        if (failure == 3) late = 1;
        if (failure == 4) now += RD_TIMESTAMP_SEQUENCE_NS;
        assert(!rd_timestamp_send(&p, 1, &out, &d));
        assert(p.stopped && posts == 1 && allocations == releases);
        ownerOK = prepareOK = localOK = 1; late = 0;
        assert(!rd_timestamp_send(&p, 1, &out, &d) && posts == 1);
    }
}
static void factory_bounds(void) {
    reset(); rd_input b = {.pid=42, .deadline=100}; rd_timestamp_post out; rd_input_diagnostic d = {0};
    assert(!rd_input_timestamp_post(&b, 1, &out, &d));
    assert(b.deadline == 0 && b.timestampStopped && posts == 0 && allocations == 0);
    b.deadline = 100;
    assert(!rd_input_timestamp_post(&b, 0, &out, &d) && posts == 0);
    reset(); b = (rd_input){.pid=42, .deadline=100}; allocationFail = 1;
    assert(!rd_input_timestamp_post(&b, 0, &out, &d));
    assert(b.timestampStopped && b.deadline == 0 && posts == 0 && releases == 0);
    reset(); b = (rd_input){.pid=42}; rd_timestamp_probe p = owner(&b);
    now += RD_TIMESTAMP_TOTAL_NS;
    assert(!rd_timestamp_send(&p, 0, &out, &d) && posts == 0 && prepareCalls == 0);
}
int main(void) { production_equivalence(); manifest(); abort_release(); factory_bounds(); return 0; }
`

const timestampDropPrefix = `
#import <Cocoa/Cocoa.h>
#include <assert.h>
#include <setjmp.h>
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "recovery_timestamp_probe_darwin.h"
// A scalar-only fake: none of NSEvent's OS-backed getters can execute.
@interface TimestampFakeEvent : NSObject {
@public id window; NSUInteger type; unsigned short keyCode; BOOL isARepeat; double timestamp;
}
@end
@implementation TimestampFakeEvent
- (id)window { return window; }
- (NSUInteger)type { return type; }
- (unsigned short)keyCode { return keyCode; }
- (BOOL)isARepeat { return isARepeat; }
- (double)timestamp { return timestamp; }
@end
#define NSEvent TimestampFakeEvent
static id timestampWindow;
static int timestampArmed, focusOK = 1;
static unsigned timestampSeen;
static rd_timestamp_observation timestampObservation;
enum { timestampFailExit = 79 };
static jmp_buf escape;
static int timestamp_focused(void) { return focusOK; }
static void fail_exit(int code) { assert(code == timestampFailExit); longjmp(escape, 1); }
#define _exit fail_exit
`

const timestampDropMain = `
int main(void) {
    @autoreleasepool {
        timestampWindow = [NSObject new];
        TimestampFakeEvent *e = [TimestampFakeEvent new];
        e->window = timestampWindow; e->keyCode = RD_TIMESTAMP_KEY; e->timestamp = 123;
        for (int slot = 0; slot < RD_TIMESTAMP_BASELINE_POSTS; slot++) {
            timestampArmed = slot; e->type = slot % 2 ? NSEventTypeKeyUp : NSEventTypeKeyDown;
            assert(timestamp_drop(e) == nil && timestampObservation.seen);
            assert(timestampObservation.timestamp == 123 && timestampSeen == (unsigned)slot+1);
        }
        for (int invalid = 0; invalid < 6; invalid++) {
            timestampArmed = 0; e->type = NSEventTypeKeyDown; e->keyCode = RD_TIMESTAMP_KEY;
            e->window = timestampWindow; e->isARepeat = NO; focusOK = 1;
            if (invalid == 0) timestampArmed = -1;
            if (invalid == 1) e->type = NSEventTypeFlagsChanged;
            if (invalid == 2) e->keyCode++;
            if (invalid == 3) e->window = nil;
            if (invalid == 4) e->isARepeat = YES;
            if (invalid == 5) focusOK = 0;
            if (!setjmp(escape)) { timestamp_drop(e); assert(!"unexpected event was not fail-stop"); }
            assert(timestampSeen == RD_TIMESTAMP_BASELINE_POSTS);
        }
        [e release]; [timestampWindow release];
    }
    return 0;
}
`
