//go:build integration && darwin && cgo

#include "ax_latency_probe_darwin.h"
#include <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <mach/mach_time.h>
#include <string.h>
#include <unistd.h>
#include <sys/proc.h>

static const uint64_t rd_ax_budget_ns = 2000000000ULL;
static const float rd_ax_ms_per_second = 1000.0f;

typedef struct { struct proc_bsdinfo info; char path[PROC_PIDPATHINFO_MAXSIZE]; } rd_ax_identity;

uint64_t rd_ax_now(void) {
    mach_timebase_info_data_t scale;
    mach_timebase_info(&scale);
    return (uint64_t)(((__uint128_t)mach_continuous_time() * scale.numer) / scale.denom);
}

int rd_ax_budget_expired(uint64_t elapsed) { return elapsed >= rd_ax_budget_ns; }

void rd_ax_schedule(rd_ax_batch *out) {
    const int timeouts[RD_AX_QUERIES] = {10, 10, 100, 100, 100, 10};
    memset(out, 0, sizeof(*out));
    for (int i = 0; i < RD_AX_QUERIES; i++) {
        out->samples[i].sequence = i + 1;
        out->samples[i].api = i == 3 ? RD_AX_SINGLE : RD_AX_RANGED;
        out->samples[i].timeout_ms = timeouts[i];
    }
}

static int rd_ax_process(int pid, rd_ax_identity *out) {
    memset(out, 0, sizeof(*out));
    return proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &out->info, sizeof(out->info)) == sizeof(out->info)
        && out->info.pbi_ppid == getpid() && out->info.pbi_status != SZOMB
        && proc_pidpath(pid, out->path, sizeof(out->path)) > 0;
}

static int rd_ax_window(int pid, uint32_t window) {
    CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, window);
    int owner = 0, matched = 0;
    if (list && CFArrayGetCount(list) == 1) {
        CFDictionaryRef row = CFArrayGetValueAtIndex(list, 0);
        CFNumberRef value = CFDictionaryGetValue(row, kCGWindowOwnerPID);
        matched = value && CFNumberGetValue(value, kCFNumberIntType, &owner) && owner == pid;
    }
    if (list) CFRelease(list);
    return matched;
}

static int rd_ax_same(int pid, uint32_t window, const rd_ax_identity *pin) {
    rd_ax_identity now;
    return rd_ax_process(pid, &now) && now.info.pbi_start_tvsec == pin->info.pbi_start_tvsec
        && now.info.pbi_start_tvusec == pin->info.pbi_start_tvusec
        && strcmp(now.path, pin->path) == 0 && rd_ax_window(pid, window);
}

static int rd_ax_pin(int pid, uint32_t window, rd_ax_identity *pin) {
    char parent[PROC_PIDPATHINFO_MAXSIZE] = {0};
    return pid > 0 && window != 0 && rd_ax_process(pid, pin)
        && proc_pidpath(getpid(), parent, sizeof(parent)) > 0
        && strcmp(parent, pin->path) == 0
        && (pin->info.pbi_start_tvsec || pin->info.pbi_start_tvusec)
        && rd_ax_window(pid, window);
}

static void rd_ax_query(AXUIElementRef app, rd_ax_sample *sample, uint64_t start) {
    CFTypeRef value = NULL;
    uint64_t before = rd_ax_now();
    sample->age_ns = before - start;
    if (sample->api == RD_AX_SINGLE)
        sample->query_error = AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &value);
    else
        sample->query_error = AXUIElementCopyAttributeValues(app, kAXWindowsAttribute,
            0, RD_AX_MAX_VALUES, (CFArrayRef *)&value);
    sample->elapsed_ns = rd_ax_now() - before;
    sample->value_present = value != NULL;
    sample->is_array = value && CFGetTypeID(value) == CFArrayGetTypeID();
    if (sample->is_array) sample->count = CFArrayGetCount((CFArrayRef)value);
    if (value) CFRelease(value);
}

static void rd_ax_run(int pid, uint32_t window, const rd_ax_identity *pin,
                      AXUIElementRef app, uint64_t prepared, rd_ax_batch *out) {
    uint64_t start = rd_ax_now();
    for (int i = 0; i < RD_AX_QUERIES; i++) {
        rd_ax_sample *sample = &out->samples[i];
        if (!rd_ax_same(pid, window, pin)) { out->status = RD_AX_IDENTITY; break; }
        sample->setup_error = AXUIElementSetMessagingTimeout(app,
            sample->timeout_ms / rd_ax_ms_per_second);
        if (sample->setup_error) { out->status = RD_AX_SETUP; break; }
        if (rd_ax_budget_expired(rd_ax_now() - start)) { out->status = RD_AX_DEADLINE; break; }
        rd_ax_query(app, sample, prepared);
        out->completed++;
        if (rd_ax_budget_expired(rd_ax_now() - start)) { out->status = RD_AX_DEADLINE; break; }
    }
}

void rd_ax_latency_batch(int pid, uint32_t window, uint64_t prepared, rd_ax_batch *out) {
    rd_ax_schedule(out);
    if (!AXIsProcessTrusted() || !CGPreflightScreenCaptureAccess()) {
        out->status = RD_AX_PERMISSION; return;
    }
    rd_ax_identity pin;
    if (!prepared || prepared > rd_ax_now() || !rd_ax_pin(pid, window, &pin)) {
        out->status = RD_AX_IDENTITY; return;
    }
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) { out->status = RD_AX_SETUP; return; }
    rd_ax_run(pid, window, &pin, app, prepared, out);
    CFRelease(app); // Never released by the Go watchdog while a synchronous call is active.
}
