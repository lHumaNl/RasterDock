//go:build darwin && cgo

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Execute production prepare/check/post with fake OS boundaries, never live input.
func TestInputNativeSlimHotPath(t *testing.T) {
	harness := strings.Replace(inputFocusHarness, "#include \"input_darwin.m\"", slimBoundaries+"\n#include \"input_darwin.m\"", 1)
	harness = strings.Replace(harness, "int main(void) {", slimCases+"\nint main(void) {", 1)
	harness = strings.Replace(harness, "reset(); check(RD_CODE_UNKNOWN", "slim_cases(); reset(); check(RD_CODE_UNKNOWN", 1)
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "slim.m"), filepath.Join(dir, "slim")
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

const slimBoundaries = `
#include <libproc.h>
#include <sys/proc.h>
#include <unistd.h>
static int processCalls, frontCalls, creates, posts, allocationFailure;
static int loseOnDown, expireOnCreate, failProcessAt, failFrontAt, processGone;
static int permissionCalls, cgCalls, modifierCalls;
static int fake_proc(int pid, int flavor, uint64_t arg, void *buffer, int size) {
    assert(pid == 42 && flavor == PROC_PIDTBSDINFO); (void)arg;
    processCalls++;
    if (processGone == 1) return 0;
    struct proc_bsdinfo *info = buffer;
    info->pbi_status = processGone == 2 ? SZOMB : SRUN;
    info->pbi_start_tvsec = 1;
    info->pbi_start_tvusec = processCalls == failProcessAt ? 1 : 0;
    return size;
}
@interface SlimWorkspace : NSObject
+ (SlimWorkspace *)sharedWorkspace;
- (SlimWorkspace *)frontmostApplication;
- (pid_t)processIdentifier;
@end
@implementation SlimWorkspace
+ (SlimWorkspace *)sharedWorkspace { static id instance; if (!instance) instance = [self new]; return instance; }
- (SlimWorkspace *)frontmostApplication { return self; }
- (pid_t)processIdentifier { frontCalls++; return frontCalls == failFrontAt ? 43 : 42; }
@end
static bool forbidden_permission(void) { permissionCalls++; assert(!"hot-path permission query"); return false; }
static CFArrayRef forbidden_cg(CGWindowListOption option, CGWindowID window) {
    (void)option; (void)window; cgCalls++; assert(!"hot-path CG enumeration"); return NULL;
}
static CGEventFlags forbidden_flags(CGEventSourceStateID state) {
    (void)state; modifierCalls++; assert(!"hot-path modifier query"); return 0;
}
static CGEventRef create_event(CGEventSourceRef source, CGKeyCode key, bool down) {
    assert(!source && key == 1); creates++;
    if (expireOnCreate) now = deadline;
    if (creates == allocationFailure) return NULL;
    return (CGEventRef)acquire(down ? pinned : other);
}
static void set_flags(CGEventRef event, CGEventFlags flags) { assert(event && !flags); }
static void post_event(pid_t pid, CGEventRef event) {
    assert(pid == 42 && creates == 2);
    assert(CFEqual(event, posts == 0 ? pinned : other)); posts++;
    if (loseOnDown) { failProcessAt = processCalls + 1; failFrontAt = frontCalls + 1; now = deadline; }
}
static int no_sleep(useconds_t interval) { assert(interval == 8000); return 0; }
#define proc_pidinfo fake_proc
#define NSWorkspace SlimWorkspace
#define AXIsProcessTrusted forbidden_permission
#define CGPreflightPostEventAccess forbidden_permission
#define CGPreflightScreenCaptureAccess forbidden_permission
#define CGWindowListCopyWindowInfo forbidden_cg
#define CGEventSourceFlagsState forbidden_flags
#define CGEventCreateKeyboardEvent create_event
#define CGEventSetFlags set_flags
#define CGEventPostToPid post_event
#define usleep no_sleep
`

const slimCases = `
static rd_input slim_reset(void) {
    reset(); processCalls = frontCalls = creates = posts = 0;
    allocationFailure = loseOnDown = expireOnCreate = failProcessAt = failFrontAt = 0;
    processGone = 0;
    return (rd_input){.pid = 42, .start = 1000000, .axWindow = (AXUIElementRef)pinned,
        .title = (CFStringRef)title, .deadline = deadline};
}
static void slim_success(void) {
    rd_input b = slim_reset(); rd_input_diagnostic d = {0};
    assert(rd_input_prepare(&b, deadline, &d));
    assert(processCalls == 1 && frontCalls == 1 && focusCalls == 1 && titleCalls == 0);
    loseOnDown = 1; // Even loss/expiry during DOWN must release UP and succeed without postflight.
    assert(rd_input_key(&b, 1, &d));
    assert(posts == 2 && !b.deadline && outstanding == 0);
    assert(processCalls == 2 && frontCalls == 2 && focusCalls == 1 && titleCalls == 0);
    assert(!permissionCalls && !cgCalls && !modifierCalls);
    assert(!rd_input_key(&b, 1, &d) && posts == 2);
}
static void slim_prepare_failures(void) {
    for (int mode = 0; mode < 7; mode++) {
        rd_input b = slim_reset(); rd_input_diagnostic d = {0};
        if (mode == 0) failProcessAt = 1;
        if (mode == 1) failFrontAt = 1;
        if (mode == 2) focusValue = other;
        if (mode == 3) focusError = kAXErrorCannotComplete;
        if (mode == 4) now = deadline;
        if (mode == 5) processGone = 1;
        if (mode == 6) processGone = 2;
        assert(!rd_input_prepare(&b, deadline, &d) && !b.deadline);
        assert(!rd_input_key(&b, 1, &d) && !posts && !outstanding);
    }
}
static void slim_final_failures(void) {
    for (int mode = 0; mode < 5; mode++) {
        rd_input b = slim_reset(); rd_input_diagnostic d = {0};
        assert(rd_input_prepare(&b, deadline, &d));
        if (mode == 0) failProcessAt = processCalls + 1;
        if (mode == 1) failFrontAt = frontCalls + 1;
        if (mode == 2) expireOnCreate = 1;
        if (mode == 3) allocationFailure = 1;
        if (mode == 4) allocationFailure = 2;
        assert(!rd_input_key(&b, 1, &d) && !posts && !b.deadline && !outstanding);
        assert(focusCalls == 1 && creates == 2);
    }
}
static void slim_check_clears(void) {
    for (int fail = 0; fail < 2; fail++) {
        rd_input b = slim_reset(); rd_input_diagnostic d = {0};
        if (fail) focusValue = other;
        assert(rd_input_check(&b, &d) == !fail && !b.deadline);
        assert(!rd_input_key(&b, 1, &d) && !posts && !outstanding);
    }
}
static void slim_every_pair_focus(void) {
    rd_input b = slim_reset(); rd_input_diagnostic d = {0};
    assert(rd_input_prepare(&b, deadline, &d) && rd_input_key(&b, 1, &d));
    focusValue = other;
    assert(!rd_input_prepare(&b, deadline, &d) && !b.deadline);
    assert(d.code == RD_CODE_FOCUS_IDENTITY && focusCalls == 2);
    assert(!rd_input_key(&b, 1, &d) && posts == 2 && !outstanding);
}
static void slim_cases(void) {
    slim_success(); slim_prepare_failures(); slim_final_failures(); slim_check_clears();
    slim_every_pair_focus();
}
`
