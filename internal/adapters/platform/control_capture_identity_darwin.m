#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>
#import <libproc.h>
#import <sys/proc.h>
#import "control_capture_darwin.h"

static const uint64_t RDMicrosecondsPerSecond = 1000000;

uint64_t rd_control_process_start(int32_t pid) {
    struct proc_bsdinfo info = {0};
    if (pid <= 0 || pid == getpid() ||
        proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info)) != sizeof(info)) return 0;
    if (info.pbi_status == SZOMB || (info.pbi_flags & PROC_FLAG_INEXIT)) return 0;
    if (info.pbi_start_tvusec >= RDMicrosecondsPerSecond ||
        info.pbi_start_tvsec > (UINT64_MAX-info.pbi_start_tvusec)/RDMicrosecondsPerSecond) return 0;
    return info.pbi_start_tvsec * RDMicrosecondsPerSecond + info.pbi_start_tvusec;
}

static BOOL rd_control_metadata_matches(NSDictionary *item, RDControlTarget target) {
    NSString *title = [NSString stringWithUTF8String:target.title];
    return [item[(id)kCGWindowNumber] unsignedIntValue] == target.window &&
        [item[(id)kCGWindowOwnerPID] intValue] == target.pid &&
        [item[(id)kCGWindowName] isKindOfClass:[NSString class]] &&
        [item[(id)kCGWindowName] isEqualToString:title] &&
        [item[(id)kCGWindowIsOnscreen] boolValue] && [item[(id)kCGWindowLayer] intValue] == 0;
}

int rd_control_identity_matches(RDControlTarget target) {
    if (!target.window || !target.started || !target.title || !target.title[0]) return 0;
    if (rd_control_process_start(target.pid) != target.started) return 0;
    // Metadata only, not capture. WindowServer has no timeout API: callers run
    // this on the owned serial queue, never in a blocking Go/native state query.
    NSArray *items = (NSArray *)CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, target.window);
    BOOL matches = items.count == 1 && rd_control_metadata_matches(items.firstObject,target);
    [items release];
    // Bracket metadata lookup to reject process exit/reuse during the lookup.
    return matches && rd_control_process_start(target.pid) == target.started;
}
