//go:build integration && darwin && cgo

#import <Cocoa/Cocoa.h>
#include <time.h>
#include <unistd.h>
#include <stdatomic.h>
#include <pthread.h>
#include "recovery_timestamp_probe_darwin.h"
#include "input_check_profile_darwin.h"

struct rd_timestamp_probe {
    rd_input *binding;
    int pid, stopped;
    uint32_t window;
    uint64_t identity, start, sequence;
    unsigned next;
};
static uint64_t timestamp_now(void) { return clock_gettime_nsec_np(CLOCK_MONOTONIC_RAW); }
static atomic_int timestampOpened;

static int timestamp_bound(rd_timestamp_probe *p, const char *title, rd_timestamp_open_result *out) {
    rd_input_target target = {0};
    out->phase = RD_TIMESTAMP_OPEN_TARGET_COPY;
    if (!rd_input_copy_target(p->binding, &target, &out->diagnostic)) return 0;
    out->phase = RD_TIMESTAMP_OPEN_TARGET_MISMATCH;
    int ok = target.pid == p->pid && target.window == p->window &&
        target.started == p->identity && target.title_length >= 0 &&
        (size_t)target.title_length == strlen(title) &&
        !memcmp(target.title, title, (size_t)target.title_length);
    free(target.title);
    return ok;
}

rd_timestamp_probe *rd_timestamp_open(int pid, uint32_t window, const char *title, rd_timestamp_open_result *out) {
    // One fixed manifest per test process, including refused initial attempts.
    int attempted = atomic_exchange(&timestampOpened, 1);
    if (!out) return NULL;
    *out = (rd_timestamp_open_result){.phase = RD_TIMESTAMP_OPEN_ALREADY_ATTEMPTED};
    if (attempted) return NULL;
    out->phase = RD_TIMESTAMP_OPEN_ALLOCATION;
    rd_timestamp_probe *p = calloc(1, sizeof(*p));
    if (!p) return NULL;
    p->pid = pid; p->window = window; p->start = timestamp_now();
    out->phase = title ? RD_TIMESTAMP_OPEN_OWNER_PIN : RD_TIMESTAMP_OPEN_ARGUMENT;
    if (!title || !rd_profile_owner(pid, window, &p->identity)) { free(p); return NULL; }
    out->phase = RD_TIMESTAMP_OPEN_BIND;
    p->binding = rd_input_bind(window, &out->diagnostic); // Unmodified 500ms production Bind.
    if (!p->binding || !timestamp_bound(p, title, out)) { rd_timestamp_close(p); return NULL; }
    out->phase = RD_TIMESTAMP_OPEN_READY;
    return p;
}

static int timestamp_eligible(rd_timestamp_probe *p, unsigned slot) {
    uint64_t identity = 0, now = timestamp_now();
    if (!p || p->stopped || slot != p->next || slot >= RD_TIMESTAMP_BASELINE_POSTS) return 0;
    if (slot % 2 == 0) p->sequence = now;
    return now - p->start < RD_TIMESTAMP_TOTAL_NS && now - p->sequence < RD_TIMESTAMP_SEQUENCE_NS &&
        rd_profile_alive(p->pid, p->identity) && rd_profile_owner(p->pid, p->window, &identity) &&
        identity == p->identity;
}

int rd_timestamp_send(rd_timestamp_probe *p, unsigned slot, rd_timestamp_post *out, rd_input_diagnostic *d) {
    if (!p) return 0;
    int eligible = timestamp_eligible(p, slot);
    p->stopped = 1; // Refusal permanently invalidates this owner, including release refusal.
    if (!eligible || !out) return 0;
    uint64_t deadline = rd_input_deadline(RD_INPUT_CHECK_BUDGET_NS);
    uint64_t total = p->start + RD_TIMESTAMP_TOTAL_NS, sequence = p->sequence + RD_TIMESTAMP_SEQUENCE_NS;
    if (total < deadline) deadline = total;
    if (sequence < deadline) deadline = sequence;
    if (!rd_input_prepare(p->binding, deadline, d)) return 0;
    if (!rd_input_timestamp_post(p->binding, slot, out, d)) return 0;
    p->next++; p->stopped = 0;
    return 1;
}

void rd_timestamp_close(rd_timestamp_probe *p) {
    if (!p) return;
    rd_input_close(p->binding);
    free(p); // Only after synchronous Send returns; never from a watchdog.
}

// Process-lifetime, main-thread-only monitor state. The helper exits with its sink
// still installed: there is no path restoring an editable production form.
static NSWindow *timestampWindow;
static NSString *timestampTitle;
static id timestampMonitor;
static id timestampWindowLoss, timestampApplicationLoss;
static int timestampArmed = -1;
static unsigned timestampSeen;
static uint64_t timestampFocusGeneration;
static rd_timestamp_observation timestampObservation;
enum { timestampFailExit = 79 };

// Process-lifetime storage: callbacks cannot outlive a freed semaphore/snapshot.
// AppKit sampling stays on main; only scalar publication is shared with the waiter.
static pthread_mutex_t initialMutex = PTHREAD_MUTEX_INITIALIZER;
static dispatch_semaphore_t initialSemaphore;
static uint64_t initialDeadline;
static int initialClaimed;
static rd_timestamp_initial_result initialResult;

static int timestamp_initial_begin(void) {
    pthread_mutex_lock(&initialMutex);
    int ok = initialSemaphore == NULL;
    if (ok) {
        initialSemaphore = dispatch_semaphore_create(0);
        initialDeadline = timestamp_now() + RD_TIMESTAMP_INITIAL_NS;
        ok = initialSemaphore != NULL;
    }
    pthread_mutex_unlock(&initialMutex);
    return ok;
}

static void timestamp_initial_publish(rd_timestamp_readiness r) {
    pthread_mutex_lock(&initialMutex);
    if (initialResult.status == RD_TIMESTAMP_INITIAL_PENDING) {
        initialResult.readiness = r;
        if (timestamp_now() >= initialDeadline) initialResult.status = RD_TIMESTAMP_INITIAL_TIMEOUT;
        else if (r.sampled && r.main_thread && r.visible && r.key_window && r.self_key && r.app_active)
            initialResult.status = RD_TIMESTAMP_INITIAL_READY;
        if (initialResult.status != RD_TIMESTAMP_INITIAL_PENDING) dispatch_semaphore_signal(initialSemaphore);
    }
    pthread_mutex_unlock(&initialMutex);
}

static rd_timestamp_initial_result timestamp_initial_wait(void) {
    pthread_mutex_lock(&initialMutex);
    if (!initialSemaphore || initialClaimed) {
        pthread_mutex_unlock(&initialMutex);
        return (rd_timestamp_initial_result){.status = RD_TIMESTAMP_INITIAL_UNAVAILABLE};
    }
    initialClaimed = 1;
    uint64_t now = timestamp_now(), remaining = now < initialDeadline ? initialDeadline - now : 0;
    int pending = initialResult.status == RD_TIMESTAMP_INITIAL_PENDING;
    pthread_mutex_unlock(&initialMutex);
    if (pending) dispatch_semaphore_wait(initialSemaphore, dispatch_time(DISPATCH_TIME_NOW, (int64_t)remaining));
    pthread_mutex_lock(&initialMutex);
    if (initialResult.status == RD_TIMESTAMP_INITIAL_PENDING) initialResult.status = RD_TIMESTAMP_INITIAL_TIMEOUT;
    rd_timestamp_initial_result out = initialResult;
    pthread_mutex_unlock(&initialMutex);
    return out;
}

static void timestamp_initial_observed(NSNotification *notification) {
    (void)notification;
    if (![NSThread isMainThread]) _exit(timestampFailExit);
    timestamp_initial_publish(rd_timestamp_readiness_snapshot());
}

static int timestamp_initial_install(void) {
    if (!timestamp_initial_begin()) return 0;
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    void (^observed)(NSNotification *) = ^(NSNotification *notification) { timestamp_initial_observed(notification); };
    // Retain for process lifetime, like the fail-stop loss observers. Register
    // before the initial sample so already-completed activation cannot be missed.
    id active = [[center addObserverForName:NSApplicationDidBecomeActiveNotification
        object:NSApp queue:nil usingBlock:observed] retain];
    id key = [[center addObserverForName:NSWindowDidBecomeKeyNotification
        object:timestampWindow queue:nil usingBlock:observed] retain];
    if (!active || !key) return 0;
    timestamp_initial_observed(nil);
    return 1;
}

rd_timestamp_initial_result rd_timestamp_initial_wait(void) {
    if ([NSThread isMainThread]) return (rd_timestamp_initial_result){.status = RD_TIMESTAMP_INITIAL_UNAVAILABLE};
    return timestamp_initial_wait(); // Exactly one bounded wait, never blocks AppKit.
}

static int timestamp_focused(void) {
    rd_input_focus_state state = rd_input_own_focus_state(timestampTitle.UTF8String);
    return state.focused && state.generation == timestampFocusGeneration &&
        NSApp.keyWindow == timestampWindow && timestampWindow.visible &&
        [timestampWindow.title isEqualToString:timestampTitle];
}

static NSEvent *timestamp_drop(NSEvent *event) {
    if (timestampArmed < 0 || !timestamp_focused() || event.window != timestampWindow ||
        event.type != (timestampArmed % 2 ? NSEventTypeKeyUp : NSEventTypeKeyDown) ||
        event.keyCode != RD_TIMESTAMP_KEY || event.isARepeat) _exit(timestampFailExit);
    timestampObservation.timestamp = event.timestamp;
    timestampObservation.seen = 1;
    timestampSeen++;
    timestampArmed = -1;
    return nil; // Includes both down and up; no characters or modifier metadata read.
}

static int timestamp_watch_focus(void) {
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    void (^lost)(NSNotification *) = ^(NSNotification *notification) {
        (void)notification;
        _exit(timestampFailExit);
    };
    timestampWindowLoss = [[center addObserverForName:NSWindowDidResignKeyNotification
        object:timestampWindow queue:nil usingBlock:lost] retain];
    timestampApplicationLoss = [[center addObserverForName:NSApplicationDidResignActiveNotification
        object:NSApp queue:nil usingBlock:lost] retain];
    return timestampWindowLoss && timestampApplicationLoss;
}

uint32_t rd_timestamp_monitor(uintptr_t handle, const char *title) {
    if (![NSThread isMainThread] || timestampWindow || !handle || !title) return 0;
    NSWindow *window = (NSWindow *)handle;
    if (![NSApp.windows containsObject:window] || ![window.title isEqualToString:@(title)]) return 0;
    timestampWindow = [window retain];
    timestampTitle = [window.title copy];
    timestampFocusGeneration = rd_input_own_focus_state(title).generation;
    timestampMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:
        NSEventMaskKeyDown | NSEventMaskKeyUp | NSEventMaskFlagsChanged handler:^NSEvent *(NSEvent *event) {
            return timestamp_drop(event);
        }];
    if (!timestampMonitor || !timestamp_watch_focus() || !timestamp_initial_install()) return 0;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, RD_TIMESTAMP_CHILD_SECONDS * NSEC_PER_SEC),
        dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{ _exit(timestampFailExit); });
    return (uint32_t)window.windowNumber;
}

// Called on main at installation and on activation/key notifications only.
// Only own AppKit state is copied; no AX queries or focus actions.
rd_timestamp_readiness rd_timestamp_readiness_snapshot(void) {
    rd_timestamp_readiness out = {.main_thread = [NSThread isMainThread]};
    if (!out.main_thread || !timestampWindow || !timestampMonitor) return out;
    out.visible = timestampWindow.visible;
    out.key_window = NSApp.keyWindow == timestampWindow;
    out.self_key = timestampWindow.isKeyWindow;
    out.app_active = NSApp.isActive;
    out.uptime = NSProcessInfo.processInfo.systemUptime;
    out.sampled = 1;
    return out;
}

int rd_timestamp_arm(unsigned slot) {
    if (![NSThread isMainThread] || !timestampWindow || !timestampMonitor ||
        slot >= RD_TIMESTAMP_BASELINE_POSTS || slot != timestampSeen || timestampArmed != -1 ||
        !timestamp_focused()) return 0;
    timestampObservation = (rd_timestamp_observation){0};
    timestampObservation.cutoff = NSProcessInfo.processInfo.systemUptime;
    timestampArmed = (int)slot;
    return 1;
}

rd_timestamp_observation rd_timestamp_observe(void) {
    if (![NSThread isMainThread] || !timestampWindow || !timestamp_focused())
        return (rd_timestamp_observation){.failed = 1};
    return timestampObservation;
}
