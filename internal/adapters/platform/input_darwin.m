//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <sys/proc.h>
#include <unistd.h>
#include <time.h>
#include <limits.h>
#include "input_darwin.h"

struct rd_input {
    pid_t pid;
    uint32_t window;
    uint64_t start;
    CFStringRef title;
    AXUIElementRef axWindow;
    uint64_t deadline;
#ifdef RD_RECOVERY_TIMESTAMP_PROBE
    unsigned timestampSlot;
    int timestampStopped;
#endif
};
static const useconds_t keyHoldMicroseconds = 8000;
// Let AX use the remaining shared preflight budget, not an independent 10ms
// cutoff. Native measurements exceeded 10ms even after successful warm queries.
static const uint64_t axMessageBudgetNS = RD_INPUT_CHECK_BUDGET_NS;
static const NSUInteger maxCGWindows = 4096;
static const CFIndex maxAXWindows = 16;
static const double nanosecondsPerSecond = 1000000000.0;

static int refuse(rd_input_diagnostic *d, rd_input_stage stage, rd_input_code code) {
    if (d && d->code == RD_CODE_UNKNOWN) *d = (rd_input_diagnostic){stage, code, 0, 0};
    return 0;
}

// Pure classification: preserve the existing budget-first decision and retain
// the numeric AX return even when that query returned after the deadline.
int rd_input_ax_result(rd_input_diagnostic *d, rd_input_stage stage,
    int timely, int ax_error, rd_input_code result_code) {
    rd_input_code code = !timely ? RD_CODE_BUDGET : result_code;
    if (code == RD_CODE_UNKNOWN) return 1;
    if (d && d->code == RD_CODE_UNKNOWN) {
        refuse(d, stage, code);
        d->has_ax_error = 1;
        d->ax_error = ax_error;
    }
    return 0;
}

// Called only by BindInput before publication; the caller owns the title copy.
int rd_input_copy_target(const rd_input *b, rd_input_target *target, rd_input_diagnostic *d) {
    if (!b || !b->title || !target) return refuse(d, RD_STAGE_TARGET, RD_CODE_TARGET_INVALID);
    CFIndex length = CFStringGetLength(b->title);
    CFIndex capacity = CFStringGetMaximumSizeForEncoding(length, kCFStringEncodingUTF8);
    if (capacity <= 0 || capacity > INT_MAX) return refuse(d, RD_STAGE_TARGET, RD_CODE_UTF8);
    char *title = malloc((size_t)capacity);
    if (!title) return refuse(d, RD_STAGE_TARGET, RD_CODE_ALLOCATION);
    CFIndex used = 0;
    CFIndex converted = CFStringGetBytes(b->title, CFRangeMake(0, length), kCFStringEncodingUTF8,
        0, false, (UInt8 *)title, capacity, &used);
    if (converted != length || used <= 0) { free(title); return refuse(d, RD_STAGE_TARGET, RD_CODE_UTF8); }
    *target = (rd_input_target){b->window, b->pid, b->start, title, (int)used};
    return 1;
}

// Unlike CLOCK_UPTIME_RAW/mach_absolute_time, this deadline clock includes sleep.
// A suspended preflight must expire before it can post after wake.
static uint64_t input_now(void) { return clock_gettime_nsec_np(CLOCK_MONOTONIC_RAW); }

static _Thread_local rd_input_timing *inputTiming;
void rd_input_timing_attach(rd_input_timing *buffer) { inputTiming = buffer; }
void rd_input_timing_append(rd_input_timing *buffer, rd_input_stage stage,
    rd_input_timing_op op, uint64_t before, uint64_t after, uint64_t deadline,
    int has_ax_error, int ax_error) {
    if (!buffer) return;
    if (buffer->count >= RD_INPUT_TIMING_CAPACITY) { buffer->overflow = 1; return; }
    buffer->records[buffer->count++] = (rd_input_timing_record){stage, op,
        before - buffer->start_ns, after - before, before < deadline ? deadline - before : 0,
        has_ax_error, ax_error};
}
static uint64_t timing_begin(void) { return inputTiming ? input_now() : 0; }
static void timing_end(rd_input_stage stage, rd_input_timing_op op,
    uint64_t before, uint64_t deadline, int hasAX, int error) {
    if (inputTiming) rd_input_timing_append(inputTiming, stage, op, before,
        input_now(), deadline, hasAX, error);
}

uint64_t rd_input_deadline(uint64_t budget_ns) {
    uint64_t budget = MIN(budget_ns, RD_INPUT_CHECK_BUDGET_NS);
    return input_now() + budget;
}

uint64_t rd_input_bind_deadline(uint64_t now) {
    if (now > UINT64_MAX - RD_INPUT_BIND_BUDGET_NS) return 0;
    return now + RD_INPUT_BIND_BUDGET_NS;
}

static int in_budget(uint64_t deadline) { return deadline && input_now() < deadline; }

// Pure shared-budget calculation; subsequent queries do not restart the clock.
uint64_t rd_input_ax_allowance(uint64_t now, uint64_t deadline) {
    return now >= deadline ? 0 : MIN(axMessageBudgetNS, deadline - now);
}

// AX timeouts apply per element, not recursively to windows returned by an app.
// Set one before EVERY query and never start another query after the total budget.
static int ax_ready(AXUIElementRef element, uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    uint64_t now = input_now();
    if (!element) return refuse(d, stage, RD_CODE_NULL);
    if (now >= deadline) return refuse(d, stage, RD_CODE_BUDGET);
    uint64_t remaining = rd_input_ax_allowance(now, deadline);
    float seconds = (float)(remaining / nanosecondsPerSecond);
    if (seconds <= 0) return refuse(d, stage, RD_CODE_BUDGET);
    uint64_t before = timing_begin();
    AXError err = AXUIElementSetMessagingTimeout(element, seconds);
    timing_end(stage, RD_TIMING_TIMEOUT, before, deadline, 1, err);
    if (err != kAXErrorSuccess) return rd_input_ax_result(d, stage, 1, err, RD_CODE_AX_TIMEOUT_SETUP);
    return rd_input_ax_result(d, stage, in_budget(deadline), err, RD_CODE_UNKNOWN);
}

int rd_input_capability(void) {
    return AXIsProcessTrusted() && CGPreflightPostEventAccess();
}

static uint64_t process_start(pid_t pid) {
    struct proc_bsdinfo info = {0};
    if (pid <= 0 || pid == getpid() ||
        proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info)) != sizeof(info) || info.pbi_status == SZOMB) return 0;
    return info.pbi_start_tvsec * 1000000ULL + info.pbi_start_tvusec;
}

static NSArray *window_list(uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    // CG has no timeout API. Reject late returns; do not claim a hard wall-clock
    // completion bound if WindowServer or the OS scheduler itself stalls.
    if (!in_budget(deadline)) { refuse(d, stage, RD_CODE_BUDGET); return nil; }
    uint64_t before = timing_begin();
    NSArray *windows = CFBridgingRelease(CGWindowListCopyWindowInfo(kCGWindowListOptionAll, kCGNullWindowID));
    timing_end(stage, RD_TIMING_CG_LIST, before, deadline, 0, 0);
    if (!in_budget(deadline)) { refuse(d, stage, RD_CODE_BUDGET); return nil; }
    if (windows.count > maxCGWindows) { refuse(d, stage, RD_CODE_CG_LIST_LIMIT); return nil; }
    if (!windows) refuse(d, stage, RD_CODE_NULL);
    return windows;
}

static NSDictionary *selected_window(uint32_t window, uint64_t deadline, rd_input_diagnostic *d) {
    for (NSDictionary *item in window_list(deadline, d, RD_STAGE_BIND_SELECTION)) {
        if (!in_budget(deadline)) { refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_BUDGET); return nil; }
        if ([item[(id)kCGWindowNumber] unsignedIntValue] == window) return item;
    }
    refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_MISSING);
    return nil;
}

static int owned_unique_window(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    unsigned matches = 0;
    BOOL selected = NO;
    for (NSDictionary *item in window_list(deadline, d, RD_STAGE_CG_UNIQUE)) {
        if (!in_budget(deadline)) return refuse(d, RD_STAGE_CG_UNIQUE, RD_CODE_BUDGET);
        if ([item[(id)kCGWindowOwnerPID] intValue] != b->pid ||
            ![item[(id)kCGWindowName] isEqualToString:(__bridge NSString *)b->title]) continue;
        matches++;
        if ([item[(id)kCGWindowNumber] unsignedIntValue] == b->window &&
            [item[(id)kCGWindowLayer] intValue] == 0 &&
            [item[(id)kCGWindowIsOnscreen] boolValue]) selected = YES;
    }
    // This is the original composite selected/matching/visible predicate, not
    // evidence of which component changed. Do not inspect additional CG fields.
    if (!selected) return refuse(d, RD_STAGE_CG_UNIQUE, RD_CODE_VISIBILITY);
    if (matches != 1) return refuse(d, RD_STAGE_CG_UNIQUE, RD_CODE_NOT_UNIQUE);
    return in_budget(deadline) || refuse(d, RD_STAGE_CG_UNIQUE, RD_CODE_BUDGET);
}

static CFTypeRef ax_value(AXUIElementRef element, CFStringRef attribute, CFTypeID type,
    uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    CFTypeRef value = NULL;
    if (!ax_ready(element, deadline, d, stage)) return NULL;
    uint64_t before = timing_begin();
    AXError err = AXUIElementCopyAttributeValue(element, attribute, &value);
    timing_end(stage, RD_TIMING_AX_QUERY, before, deadline, 1, err);
    int timely = in_budget(deadline);
    rd_input_code code = RD_CODE_UNKNOWN;
    if (timely) code = err != kAXErrorSuccess ? RD_CODE_QUERY : !value ? RD_CODE_NULL :
        CFGetTypeID(value) != type ? RD_CODE_TYPE : RD_CODE_UNKNOWN;
    if (rd_input_ax_result(d, stage, timely, err, code)) return value;
    if (value) CFRelease(value);
    return NULL;
}

static CFArrayRef ax_windows(AXUIElementRef app, uint64_t deadline, rd_input_diagnostic *d) {
    CFArrayRef windows = NULL;
    if (!ax_ready(app, deadline, d, RD_STAGE_AX_WINDOWS)) return NULL;
    uint64_t before = timing_begin();
    AXError err = AXUIElementCopyAttributeValues(app, kAXWindowsAttribute, 0, maxAXWindows + 1, &windows);
    timing_end(RD_STAGE_AX_WINDOWS, RD_TIMING_AX_QUERY, before, deadline, 1, err);
    int timely = in_budget(deadline);
    rd_input_code code = RD_CODE_UNKNOWN;
    if (timely) code = err != kAXErrorSuccess ? RD_CODE_QUERY : !windows ? RD_CODE_NULL :
        CFArrayGetCount(windows) > maxAXWindows ? RD_CODE_WINDOW_LIMIT : RD_CODE_UNKNOWN;
    if (rd_input_ax_result(d, RD_STAGE_AX_WINDOWS, timely, err, code)) return windows;
    if (windows) CFRelease(windows);
    return NULL;
}

// Public AX does not guarantee a CGWindowID attribute. Do not use private SPI or
// guess by PID: Bind requires uniqueness in BOTH CG's full list and AX's window
// list, then retains that AX element. This does not guarantee permanent identity.
static CFStringRef window_title(AXUIElementRef window, uint64_t deadline, rd_input_diagnostic *d) {
    if (!window || CFGetTypeID(window) != AXUIElementGetTypeID()) {
        refuse(d, RD_STAGE_AX_WINDOWS, !window ? RD_CODE_NULL : RD_CODE_TYPE); return NULL;
    }
    return ax_value(window, kAXTitleAttribute, CFStringGetTypeID(), deadline, d, RD_STAGE_AX_TITLE);
}

static AXUIElementRef unique_ax_window(rd_input *b, AXUIElementRef app, uint64_t deadline, rd_input_diagnostic *d) {
    CFArrayRef windows = ax_windows(app, deadline, d);
    if (!windows) return NULL;
    AXUIElementRef result = NULL;
    unsigned matches = 0;
    for (CFIndex i = 0; i < CFArrayGetCount(windows); i++) {
        AXUIElementRef window = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
        CFStringRef title = window_title(window, deadline, d);
        if (!title) { matches = 0; break; } // An unreadable window cannot prove uniqueness.
        if (title && CFEqual(title, b->title)) { matches++; result = window; }
        if (title) CFRelease(title);
    }
    if (matches == 1 && in_budget(deadline)) CFRetain(result); else {
        refuse(d, RD_STAGE_AX_WINDOWS, matches != 1 ? RD_CODE_NOT_UNIQUE : RD_CODE_BUDGET);
        result = NULL;
    }
    CFRelease(windows);
    return result;
}

static int focused_window(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    AXUIElementRef app = AXUIElementCreateApplication(b->pid);
    if (!app) return refuse(d, RD_STAGE_FOCUSED_WINDOW, RD_CODE_ALLOCATION);
    AXUIElementRef focused = (AXUIElementRef)ax_value(app, kAXFocusedWindowAttribute, AXUIElementGetTypeID(), deadline, d, RD_STAGE_FOCUSED_WINDOW);
    int ok = 0;
    // Bind alone resolves title/CG uniqueness. Live title changes are allowed;
    // every pair still requires equality with the retained exact AX window.
    if (focused && CFEqual(focused, b->axWindow)) ok = 1;
    else if (focused) refuse(d, RD_STAGE_FOCUSED_WINDOW, RD_CODE_FOCUS_IDENTITY);
    if (focused) CFRelease(focused);
    CFRelease(app);
    if (!ok) return 0; // Query/identity failure already recorded its first cause.
    return in_budget(deadline) || refuse(d, RD_STAGE_FOCUSED_WINDOW, RD_CODE_BUDGET);
}

void rd_input_close(rd_input *b) {
    if (!b) return;
    if (b->title) CFRelease(b->title);
    if (b->axWindow) CFRelease(b->axWindow);
    free(b);
}

static int bind_identity(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    if (!b->start) return refuse(d, RD_STAGE_IDENTITY, RD_CODE_MISSING);
    if (!b->axWindow) return 0; // The AX operation already recorded its first failure.
    if (!owned_unique_window(b, deadline, d)) return 0;
    if (process_start(b->pid) != b->start) return refuse(d, RD_STAGE_IDENTITY, RD_CODE_PROCESS_START_CHANGED);
    return in_budget(deadline) || refuse(d, RD_STAGE_IDENTITY, RD_CODE_BUDGET);
}

static rd_input *bind_window(uint32_t window, uint64_t deadline, rd_input_diagnostic *d) {
    NSDictionary *item = selected_window(window, deadline, d);
    NSString *title = item[(id)kCGWindowName];
    if (![title isKindOfClass:NSString.class] || title.length == 0) { refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_TITLE); return NULL; }
    rd_input *b = calloc(1, sizeof(*b));
    if (!b) { refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_ALLOCATION); return NULL; }
    b->pid = [item[(id)kCGWindowOwnerPID] intValue];
    b->window = window;
    b->start = process_start(b->pid);
    b->title = CFStringCreateCopy(kCFAllocatorDefault, (__bridge CFStringRef)title);
    if (!b->title) { refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_ALLOCATION); rd_input_close(b); return NULL; }
    AXUIElementRef app = AXUIElementCreateApplication(b->pid);
    if (app) { b->axWindow = unique_ax_window(b, app, deadline, d); CFRelease(app); }
    else refuse(d, RD_STAGE_AX_WINDOWS, RD_CODE_ALLOCATION);
    if (!bind_identity(b, deadline, d)) {
        rd_input_close(b); return NULL;
    }
    return b;
}

rd_input *rd_input_bind(uint32_t window, rd_input_diagnostic *d) {
    @autoreleasepool {
        uint64_t deadline = rd_input_bind_deadline(input_now());
        if (!deadline) return refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_BUDGET), NULL;
        if (!window) { refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_TARGET_INVALID); return NULL; }
        if (!rd_input_capability() || !CGPreflightScreenCaptureAccess()) {
            refuse(d, RD_STAGE_BIND_SELECTION, RD_CODE_PERMISSION); return NULL;
        }
        return bind_window(window, deadline, d);
    }
}

static int local_gate_impl(rd_input *b, uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    if (!b) return refuse(d, stage, RD_CODE_TARGET_INVALID);
    if (!in_budget(deadline)) return refuse(d, stage, RD_CODE_BUDGET);
    if (process_start(b->pid) != b->start) return refuse(d, stage, RD_CODE_PROCESS_START_CHANGED);
    if (NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier != b->pid) return refuse(d, stage, RD_CODE_FRONTMOST);
    return in_budget(deadline) || refuse(d, stage, RD_CODE_BUDGET);
}

static int local_gate(rd_input *b, uint64_t deadline, rd_input_diagnostic *d, rd_input_stage stage) {
    uint64_t before = timing_begin();
    int ok = local_gate_impl(b, deadline, d, stage);
    timing_end(stage, RD_TIMING_LOCAL, before, deadline, 0, 0);
    return ok;
}

int rd_input_prepare(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    @autoreleasepool {
        if (!b) return refuse(d, RD_STAGE_PREPARE, RD_CODE_TARGET_INVALID);
        b->deadline = 0;
        if (!in_budget(deadline)) return refuse(d, RD_STAGE_PREPARE, RD_CODE_BUDGET);
        if (!local_gate(b, deadline, d, RD_STAGE_LOCAL_FIRST) || !focused_window(b, deadline, d)) return 0;
        b->deadline = deadline;
        return 1;
    }
}

int rd_input_check(rd_input *b, rd_input_diagnostic *d) {
    if (inputTiming) inputTiming->start_ns = input_now();
    int ok = rd_input_prepare(b, rd_input_deadline(RD_INPUT_CHECK_BUDGET_NS), d);
    if (b) b->deadline = 0; // Observational checks never authorize a later post.
    if (inputTiming) { inputTiming->elapsed_ns = input_now() - inputTiming->start_ns; inputTiming->ok = ok; }
    return ok;
}

static int final_post_gate(rd_input *b, uint64_t deadline, rd_input_diagnostic *d) {
    return local_gate(b, deadline, d, RD_STAGE_PRE_POST) &&
        (in_budget(deadline) || refuse(d, RD_STAGE_PRE_POST, RD_CODE_BUDGET));
}

static int post_key(rd_input *b, uint16_t key, rd_input_diagnostic *d) {
    if (!b) return refuse(d, RD_STAGE_PRE_POST, RD_CODE_TARGET_INVALID);
    uint64_t deadline = b->deadline;
    b->deadline = 0; // A successful preflight authorizes at most one pair.
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, key, true);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, key, false);
    if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return refuse(d, RD_STAGE_PRE_POST, RD_CODE_ALLOCATION); }
    CGEventSetFlags(down, 0); CGEventSetFlags(up, 0);
    // Focus and posting cannot be atomic. PID posting avoids global delivery but
    // cannot promise application handling, hotkey suppression, or receipt (void API).
    if (!final_post_gate(b, deadline, d)) { CFRelease(down); CFRelease(up); return 0; }
    CGEventPostToPid(b->pid, down);
    usleep(keyHoldMicroseconds);
    CGEventPostToPid(b->pid, up); // Paired release even after cancellation/focus loss.
    CFRelease(down); CFRelease(up);
    return 1; // Posting is not delivery acknowledgement; no postflight OS queries.
}

int rd_input_key(rd_input *b, uint16_t key, rd_input_diagnostic *d) {
    @autoreleasepool { return post_key(b, key, d); }
}

#ifdef RD_RECOVERY_TIMESTAMP_PROBE
static CGEventRef timestamp_event(unsigned slot, rd_timestamp_post *out) {
    CGEventRef event = CGEventCreateKeyboardEvent(NULL, RD_TIMESTAMP_KEY, slot % 2 == 0);
    if (!event) return NULL;
    out->creation = CGEventGetTimestamp(event);
    CGEventSetFlags(event, 0);
    // One nanosecond after boot is deliberately old, not a protocol timestamp.
    if (slot / 2 == 1) CGEventSetTimestamp(event, 1);
    if (slot / 2 == 2) CGEventSetTimestamp(event, 0);
    out->prepost = CGEventGetTimestamp(event);
    return event;
}

int rd_input_timestamp_post(rd_input *b, unsigned slot, rd_timestamp_post *out, rd_input_diagnostic *d) {
    if (!b) return refuse(d, RD_STAGE_PRE_POST, RD_CODE_TARGET_INVALID);
    uint64_t deadline = b->deadline;
    b->deadline = 0; // Consume before validation/allocation, including failed attempts.
    if (b->timestampStopped || !out || slot != b->timestampSlot || slot >= RD_TIMESTAMP_BASELINE_POSTS) {
        b->timestampStopped = 1;
        return refuse(d, RD_STAGE_PRE_POST, RD_CODE_TARGET_INVALID);
    }
    b->timestampStopped = 1;
    CGEventRef event = timestamp_event(slot, out);
    if (!event) return refuse(d, RD_STAGE_PRE_POST, RD_CODE_ALLOCATION);
    int ok = final_post_gate(b, deadline, d);
    if (ok) CGEventPostToPid(b->pid, event);
    CFRelease(event);
    if (ok) { b->timestampSlot++; b->timestampStopped = 0; }
    return ok;
}
#endif

// Main-thread-owned, process-lifetime state. Exactly two observer tokens and their
// copied blocks are retained under MRC; no per-window registration or event tap.
static uint64_t ownFocusGeneration = 0;
static id ownWindowLossObserver = nil;
static id ownApplicationLossObserver = nil;
static BOOL ownFocusObserversInstalled = NO;

static void latch_own_focus_loss(void) {
    void (^latch)(void) = ^{
        if (ownFocusGeneration != UINT64_MAX) ownFocusGeneration++;
    };
    if (NSThread.isMainThread) latch(); else dispatch_sync(dispatch_get_main_queue(), latch);
}

static void install_own_focus_observers(void) {
    if (ownFocusObserversInstalled) return;
    ownFocusObserversInstalled = YES;
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    void (^lost)(NSNotification *) = ^(NSNotification *notification) {
        (void)notification;
        latch_own_focus_loss();
    };
    // A nil queue delivers synchronously on the posting thread, so a loss and
    // regain between polls cannot erase the generation change.
    ownWindowLossObserver = [[center addObserverForName:NSWindowDidResignKeyNotification
        object:nil queue:nil usingBlock:lost] retain];
    ownApplicationLossObserver = [[center addObserverForName:NSApplicationDidResignActiveNotification
        object:nil queue:nil usingBlock:lost] retain];
    if (!ownWindowLossObserver || !ownApplicationLossObserver) ownFocusGeneration = UINT64_MAX;
}

static rd_input_focus_state own_focus_on_main(const char *title) {
    install_own_focus_observers(); // Must precede the first explicit-arm snapshot.
    int focused = ownFocusGeneration != UINT64_MAX && NSApp.isActive && NSApp.keyWindow.isKeyWindow &&
        [NSApp.keyWindow.title isEqualToString:@(title)] &&
        NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier == getpid();
    return (rd_input_focus_state){focused, ownFocusGeneration};
}

rd_input_focus_state rd_input_own_focus_state(const char *title) {
    @autoreleasepool {
        if (!title || !*title) return (rd_input_focus_state){0, 0};
        __block rd_input_focus_state state = {0, 0};
        void (^check)(void) = ^{ state = own_focus_on_main(title); };
        if (NSThread.isMainThread) check(); else dispatch_sync(dispatch_get_main_queue(), check);
        return state;
    }
}
