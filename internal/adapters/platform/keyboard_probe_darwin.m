//go:build integration && darwin

#import <Cocoa/Cocoa.h>
#import <ApplicationServices/ApplicationServices.h>
#include <libproc.h>
#include <unistd.h>
#include <string.h>
#include "keyboard_probe_darwin.h"

int rd_probe_permissions(void) {
    return AXIsProcessTrusted() && CGPreflightScreenCaptureAccess();
}

static uint64_t child_identity(int pid) {
    struct proc_bsdinfo info = {0};
    char parent_path[PROC_PIDPATHINFO_MAXSIZE] = {0}, child_path[PROC_PIDPATHINFO_MAXSIZE] = {0};
    if (proc_pidpath(getpid(), parent_path, sizeof(parent_path)) <= 0 ||
        proc_pidpath(pid, child_path, sizeof(child_path)) <= 0 || strcmp(parent_path, child_path)) return 0;
    if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info)) != sizeof(info) ||
        info.pbi_ppid != getpid()) return 0;
    return info.pbi_start_tvsec * 1000000ULL + info.pbi_start_tvusec;
}

static int owned_window(int pid, unsigned int window, NSString *title) {
    NSArray *windows = CFBridgingRelease(CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID));
    int matches = 0, selected = 0;
    for (NSDictionary *w in windows) {
        if ([w[(id)kCGWindowOwnerPID] intValue] != pid ||
            ![w[(id)kCGWindowName] isEqualToString:title]) continue;
        matches++;
        if ([w[(id)kCGWindowNumber] unsignedIntValue] == window &&
            [w[(id)kCGWindowLayer] intValue] == 0) selected = 1;
    }
    return selected && matches == 1;
}

uint64_t rd_probe_identity(int pid, unsigned int window, const char *title) {
    @autoreleasepool {
        if (!rd_probe_permissions() || !owned_window(pid, window, @(title))) return 0;
        return child_identity(pid);
    }
}

int rd_probe_check(int pid, unsigned int window, const char *title, uint64_t identity) {
    @autoreleasepool {
        if (!identity || child_identity(pid) != identity || !rd_probe_permissions()) return 0;
        CGEventFlags modifiers = CGEventSourceFlagsState(kCGEventSourceStateCombinedSessionState);
        if (modifiers & (kCGEventFlagMaskShift | kCGEventFlagMaskControl | kCGEventFlagMaskAlternate |
                         kCGEventFlagMaskCommand | kCGEventFlagMaskAlphaShift | kCGEventFlagMaskSecondaryFn)) return 0;
        if (NSWorkspace.sharedWorkspace.frontmostApplication.processIdentifier != pid ||
            !owned_window(pid, window, @(title))) return 0;
        AXUIElementRef app = AXUIElementCreateApplication(pid);
        CFTypeRef focused = NULL, name = NULL;
        AXError e = AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, &focused);
        if (e == kAXErrorSuccess && focused)
            e = AXUIElementCopyAttributeValue((AXUIElementRef)focused, kAXTitleAttribute, &name);
        int ok = e == kAXErrorSuccess && name && CFGetTypeID(name) == CFStringGetTypeID() &&
            [(__bridge NSString *)name isEqualToString:@(title)];
        if (name) CFRelease(name);
        if (focused) CFRelease(focused);
        CFRelease(app);
        return ok;
    }
}

int rd_probe_key(int pid, unsigned int window, const char *title, uint64_t identity, unsigned short key) {
    // Allocate the matched pair BEFORE the last safety check. No global posting.
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, key, true);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, key, false);
    if (!down || !up) { if (down) CFRelease(down); if (up) CFRelease(up); return 0; }
    CGEventSetFlags(down, 0); CGEventSetFlags(up, 0);
    if (!rd_probe_check(pid, window, title, identity)) { CFRelease(down); CFRelease(up); return 0; }
    // Focus can still race this post: this is deliberately NOT an atomic guarantee.
    CGEventPostToPid(pid, down);
    CGEventPostToPid(pid, up); // Always release even if focus changes after keydown.
    CFRelease(down); CFRelease(up);
    return 1;
}

int rd_probe_viewer_focus(const char *title) {
    __block int focused = 0;
    void (^check)(void) = ^{ focused = NSApp.isActive && [NSApp.keyWindow.title isEqualToString:@(title)]; };
    if (NSThread.isMainThread) check(); else dispatch_sync(dispatch_get_main_queue(), check);
    return focused;
}
