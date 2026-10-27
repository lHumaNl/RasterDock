//go:build integration && darwin && cgo

#include "input_check_profile_darwin.h"
#include <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <sys/proc.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

typedef struct { struct proc_bsdinfo info; char path[PROC_PIDPATHINFO_MAXSIZE]; } profile_identity;
static uint64_t profile_now(void) { return clock_gettime_nsec_np(CLOCK_MONOTONIC_RAW); }

static int profile_process(int pid, profile_identity *out) {
    memset(out, 0, sizeof(*out));
    return pid > 0 && proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &out->info, sizeof(out->info)) == sizeof(out->info)
        && out->info.pbi_ppid == getpid() && out->info.pbi_status != SZOMB
        && proc_pidpath(pid, out->path, sizeof(out->path)) > 0;
}

static int profile_window(int pid, uint32_t window) {
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

static int profile_pin(int pid, uint32_t window, profile_identity *pin) {
    char parent[PROC_PIDPATHINFO_MAXSIZE] = {0};
    return window && profile_process(pid, pin)
        && proc_pidpath(getpid(), parent, sizeof(parent)) > 0 && !strcmp(parent, pin->path)
        && (pin->info.pbi_start_tvsec || pin->info.pbi_start_tvusec) && profile_window(pid, window);
}

static int profile_same(int pid, const profile_identity *pin) {
    profile_identity now;
    return profile_process(pid, &now) && !strcmp(now.path, pin->path)
        && now.info.pbi_start_tvsec == pin->info.pbi_start_tvsec
        && now.info.pbi_start_tvusec == pin->info.pbi_start_tvusec;
}

static int profile_bound(rd_input *binding, int pid, uint32_t window, const profile_identity *pin) {
    rd_input_target target = {0};
    rd_input_diagnostic diagnostic = {0};
    if (!rd_input_copy_target(binding, &target, &diagnostic)) return 0;
    int ok = target.pid == pid && target.window == window &&
        target.started == pin->info.pbi_start_tvsec * 1000000ULL + pin->info.pbi_start_tvusec;
    free(target.title); // Private verification only: no title or path leaves this worker.
    return ok;
}

int rd_profile_stop(rd_input_diagnostic d, int overflow) {
    if (overflow) return RD_PROFILE_OVERFLOW;
    if (d.code == RD_CODE_AX_TIMEOUT_SETUP || d.code == RD_CODE_PERMISSION ||
        d.code == RD_CODE_ALLOCATION || d.code == RD_CODE_MODIFIERS)
        return RD_PROFILE_SETUP;
    if (d.code == RD_CODE_PROCESS_START_CHANGED || d.code == RD_CODE_FOCUS_IDENTITY ||
        d.code == RD_CODE_TITLE || d.code == RD_CODE_VISIBILITY || d.code == RD_CODE_NOT_UNIQUE ||
        d.code == RD_CODE_TARGET_INVALID || d.code == RD_CODE_FRONTMOST) return RD_PROFILE_IDENTITY;
    return RD_PROFILE_OK; // Query refusals are observations, not controller retries.
}

int rd_profile_can_start(unsigned index, uint64_t elapsed) {
    return index < RD_PROFILE_SAMPLES && elapsed < RD_PROFILE_BUDGET_NS;
}

int rd_profile_owner(int pid, uint32_t window, uint64_t *started) {
    profile_identity pin;
    if (!profile_pin(pid, window, &pin)) return 0;
    *started = pin.info.pbi_start_tvsec * 1000000ULL + pin.info.pbi_start_tvusec;
    return 1;
}

int rd_profile_alive(int pid, uint64_t started) {
    profile_identity pin;
    char parent[PROC_PIDPATHINFO_MAXSIZE] = {0};
    return profile_process(pid, &pin) && proc_pidpath(getpid(), parent, sizeof(parent)) > 0
        && !strcmp(parent, pin.path)
        && started == pin.info.pbi_start_tvsec * 1000000ULL + pin.info.pbi_start_tvusec;
}

int rd_profile_check(rd_input *binding, rd_profile_sample *out) {
    memset(out, 0, sizeof(*out));
    rd_input_timing_attach(&out->timing);
    int ok = rd_input_check(binding, &out->diagnostic);
    rd_input_timing_attach(NULL); // Same C call/thread, including a refused Check.
    return ok;
}

static void profile_checks(rd_input *binding, int pid, const profile_identity *pin,
    uint64_t start, rd_profile_batch *out) {
    for (int i = 0; i < RD_PROFILE_SAMPLES; i++) {
        if (!rd_profile_can_start(i, profile_now() - start)) { out->status = RD_PROFILE_DEADLINE; break; }
        if (!profile_same(pid, pin)) { out->status = RD_PROFILE_IDENTITY; break; }
        if (!rd_profile_can_start(i, profile_now() - start)) { out->status = RD_PROFILE_DEADLINE; break; }
        rd_profile_sample *sample = &out->samples[i];
        rd_input_timing_attach(&sample->timing);
        rd_input_check(binding, &sample->diagnostic);
        rd_input_timing_attach(NULL);
        out->completed++;
        out->status = rd_profile_stop(sample->diagnostic, sample->timing.overflow);
        if (out->status) break;
        if (profile_now() - start >= RD_PROFILE_BUDGET_NS) { out->status = RD_PROFILE_DEADLINE; break; }
    }
}

void rd_profile_run(int pid, uint32_t window, rd_profile_batch *out) {
    memset(out, 0, sizeof(*out));
    uint64_t start = profile_now(); // Includes ownership checks and the one production Bind.
    profile_identity pin;
    if (!AXIsProcessTrusted() || !CGPreflightScreenCaptureAccess()) { out->status = RD_PROFILE_SETUP; return; }
    if (!profile_pin(pid, window, &pin)) { out->status = RD_PROFILE_IDENTITY; return; }
    if (profile_now() - start >= RD_PROFILE_BUDGET_NS) { out->status = RD_PROFILE_DEADLINE; return; }
    rd_input *binding = rd_input_bind(window, &out->bind_diagnostic);
    if (!binding) { out->status = RD_PROFILE_BIND; return; }
    if (!profile_bound(binding, pid, window, &pin)) out->status = RD_PROFILE_IDENTITY;
    else profile_checks(binding, pid, &pin, start, out);
    rd_input_close(binding); // Sole native worker owns the binding through every synchronous return.
}
