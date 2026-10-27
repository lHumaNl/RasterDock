//go:build integration && darwin && cgo

package platform

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryTimestampReadinessFakeGetters(t *testing.T) {
	body := timestampSource(t, "recovery_timestamp_probe_darwin.m", "rd_timestamp_readiness rd_timestamp_readiness_snapshot", "int rd_timestamp_arm")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "readiness.m"), filepath.Join(dir, "readiness")
	if err := os.WriteFile(source, []byte(timestampReadinessPrefix+body+timestampReadinessMain), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-Wall", "-Wextra", "-Werror", "-I", include, source, "-framework", "Foundation", "-o", binary)
	runFocusCommand(t, binary)
}

func TestRecoveryTimestampInitialNotificationState(t *testing.T) {
	body := timestampSource(t, "recovery_timestamp_probe_darwin.m", "static pthread_mutex_t initialMutex", "static void timestamp_initial_observed")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "initial.c"), filepath.Join(dir, "initial")
	if err := os.WriteFile(source, []byte(timestampInitialPrefix+body+timestampInitialMain), 0o600); err != nil {
		t.Fatal(err)
	}
	include, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-std=c11", "-Wall", "-Wextra", "-Werror", "-I", include, source, "-o", binary)
	runFocusCommand(t, binary)
}

func TestRecoveryTimestampInitialLossNotifications(t *testing.T) {
	body := timestampSource(t, "recovery_timestamp_probe_darwin.m", "static int timestamp_watch_focus", "uint32_t rd_timestamp_monitor")
	dir := t.TempDir()
	source, binary := filepath.Join(dir, "loss.m"), filepath.Join(dir, "loss")
	if err := os.WriteFile(source, []byte(timestampInitialLossPrefix+body+timestampInitialLossMain), 0o600); err != nil {
		t.Fatal(err)
	}
	runFocusCommand(t, "clang", "-fblocks", "-Wall", "-Wextra", "-Werror", source, "-framework", "Foundation", "-o", binary)
	runFocusCommand(t, binary)
}

func TestRecoveryTimestampInitialWaitPlacement(t *testing.T) {
	path := "../../presentation/fyne/recovery_timestamp_probe_test.go"
	install := timestampSource(t, path, "func recoveryTimestampInstall", "func recoveryTimestampServe")
	serve := timestampSource(t, path, "func recoveryTimestampServe", "func recoveryTimestampRequests")
	if strings.Contains(install, "AwaitRecoveryTimestampInitial") || strings.Contains(serve, "fyne.DoAndWait(") ||
		strings.Contains(serve, ".RunNative(") || strings.Count(serve, "AwaitRecoveryTimestampInitial()") != 1 {
		t.Fatal("initial wait must run exactly once outside the main-thread callback")
	}
	wait := timestampSource(t, "recovery_timestamp_probe_darwin.m", "rd_timestamp_initial_result rd_timestamp_initial_wait", "static int timestamp_focused")
	if !strings.Contains(wait, "if ([NSThread isMainThread]) return") {
		t.Fatal("main-thread wait must refuse")
	}
}

const timestampInitialLossPrefix = `
#import <Foundation/Foundation.h>
#include <assert.h>
#include <setjmp.h>
#define NSWindowDidResignKeyNotification @"fixture-window-loss"
#define NSApplicationDidResignActiveNotification @"fixture-app-loss"
static id timestampWindow, fakeApp, timestampWindowLoss, timestampApplicationLoss;
#define NSApp fakeApp
enum { timestampFailExit = 79 };
static jmp_buf lost;
static int losses;
static void fake_exit(int code) { assert(code == timestampFailExit); losses++; longjmp(lost, 1); }
#define _exit fake_exit
`

const timestampInitialLossMain = `
int main(void) {
    @autoreleasepool {
        timestampWindow = [NSObject new]; fakeApp = [NSObject new];
        id unrelated = [NSObject new];
        assert(timestamp_watch_focus());
        NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
        [center postNotificationName:NSWindowDidResignKeyNotification object:unrelated];
        assert(losses == 0);
        if (!setjmp(lost)) {
            [center postNotificationName:NSWindowDidResignKeyNotification object:timestampWindow];
            assert(!"own window loss must terminate, never refocus");
        }
        assert(losses == 1);
        if (!setjmp(lost)) {
            [center postNotificationName:NSApplicationDidResignActiveNotification object:fakeApp];
            assert(!"application loss must terminate, never refocus");
        }
        assert(losses == 2);
        // Production deliberately retains all observer/signal storage until exit.
        [unrelated release];
    }
    return 0;
}
`

const timestampInitialPrefix = `
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "recovery_timestamp_probe_darwin.h"
#include <pthread.h>
#include <assert.h>
#include <string.h>
typedef int *dispatch_semaphore_t;
typedef uint64_t dispatch_time_t;
#define DISPATCH_TIME_NOW 0
static int signals, waits, semaphoreStorage, waitEvent;
static uint64_t now = 1;
static uint64_t timestamp_now(void) { return now; }
static dispatch_semaphore_t dispatch_semaphore_create(long value) { assert(value == 0); return &semaphoreStorage; }
static long dispatch_semaphore_signal(dispatch_semaphore_t s) { assert(s == &semaphoreStorage); signals++; return 0; }
static dispatch_time_t dispatch_time(dispatch_time_t base, int64_t delta) {
    assert(base == 0 && delta >= 0 && delta <= 2000000000LL); return now + delta;
}
static long dispatch_semaphore_wait(dispatch_semaphore_t s, dispatch_time_t deadline);
`

const timestampInitialMain = `
static rd_timestamp_readiness ready = {.sampled=1,.main_thread=1,.visible=1,.key_window=1,.self_key=1,.app_active=1,.uptime=12};
static long dispatch_semaphore_wait(dispatch_semaphore_t s, dispatch_time_t deadline) {
    assert(s == &semaphoreStorage && deadline == initialDeadline); waits++;
    if (waitEvent) { now++; timestamp_initial_publish(ready); return 0; }
    now = deadline; return 1;
}
static void reset_initial(void) {
    initialSemaphore = NULL; initialClaimed = 0; initialResult = (rd_timestamp_initial_result){0};
    now = 1; signals = waits = waitEvent = 0;
    assert(timestamp_initial_begin());
    assert(initialDeadline == 2000000001ULL && RD_TIMESTAMP_INITIAL_NS == 2000000000ULL);
}
static void notification_orders(void) {
    for (int appFirst = 0; appFirst < 2; appFirst++) {
        reset_initial(); rd_timestamp_readiness partial = ready;
        if (appFirst) partial.key_window = partial.self_key = 0; else partial.app_active = 0;
        timestamp_initial_publish(partial);
        assert(initialResult.status == RD_TIMESTAMP_INITIAL_PENDING && signals == 0);
        timestamp_initial_publish(ready);
        rd_timestamp_initial_result result = timestamp_initial_wait();
        assert(result.status == RD_TIMESTAMP_INITIAL_READY && result.readiness.uptime == 12 && signals == 1);
        assert(waits == 0); timestamp_initial_publish(partial);
        assert(initialResult.readiness.app_active == 1 && initialResult.readiness.key_window == 1 && signals == 1);
        assert(timestamp_initial_wait().status == RD_TIMESTAMP_INITIAL_UNAVAILABLE);
    }
}
static void timeouts_and_late_events(void) {
    reset_initial(); rd_timestamp_readiness partial = ready; partial.app_active = 0;
    timestamp_initial_publish(partial);
    rd_timestamp_initial_result result = timestamp_initial_wait();
    assert(result.status == RD_TIMESTAMP_INITIAL_TIMEOUT && !result.readiness.app_active && waits == 1);
    timestamp_initial_publish(ready);
    assert(initialResult.status == RD_TIMESTAMP_INITIAL_TIMEOUT && !initialResult.readiness.app_active && signals == 0);
    reset_initial(); now = initialDeadline; timestamp_initial_publish(ready);
    assert(timestamp_initial_wait().status == RD_TIMESTAMP_INITIAL_TIMEOUT);
}
int main(void) {
    notification_orders(); timeouts_and_late_events();
    reset_initial(); timestamp_initial_publish(ready);
    assert(timestamp_initial_wait().status == RD_TIMESTAMP_INITIAL_READY && waits == 0);
    reset_initial(); waitEvent = 1;
    assert(timestamp_initial_wait().status == RD_TIMESTAMP_INITIAL_READY && waits == 1 && signals == 1);
    assert(initialSemaphore == &semaphoreStorage); // No release/free or late-callback dangling pointer.
    return 0;
}
`

const timestampReadinessPrefix = `
#import <Foundation/Foundation.h>
#define RD_RECOVERY_TIMESTAMP_PROBE
#include "recovery_timestamp_probe_darwin.h"
#include <assert.h>
static int flags, onMain = 1, reads;
@interface TimestampReadinessFake : NSObject
@property(readonly) BOOL visible, isKeyWindow, isActive;
@property(readonly) id keyWindow;
@property(readonly) double systemUptime;
+ (BOOL)isMainThread;
+ (TimestampReadinessFake *)processInfo;
@end
static TimestampReadinessFake *fixture, *timestampWindow;
static int timestampMonitor = 1;
@implementation TimestampReadinessFake
+ (BOOL)isMainThread { return onMain; }
+ (TimestampReadinessFake *)processInfo { return fixture; }
- (BOOL)visible { reads++; return (flags & 1) != 0; }
- (BOOL)isKeyWindow { reads++; return (flags & 2) != 0; }
- (BOOL)isActive { reads++; return (flags & 4) != 0; }
- (id)keyWindow { reads++; return (flags & 8) ? timestampWindow : nil; }
- (double)systemUptime { reads++; return 321.5; }
@end
#define NSThread TimestampReadinessFake
#define NSProcessInfo TimestampReadinessFake
#define NSApp fixture
`

const timestampReadinessMain = `
int main(void) {
    @autoreleasepool {
        fixture = [TimestampReadinessFake new]; timestampWindow = fixture;
        for (flags = 0; flags < 16; flags++) {
            reads = 0;
            rd_timestamp_readiness r = rd_timestamp_readiness_snapshot();
            assert(r.sampled && r.main_thread && reads == 5);
            assert(r.visible == !!(flags & 1) && r.self_key == !!(flags & 2));
            assert(r.app_active == !!(flags & 4) && r.key_window == !!(flags & 8));
            assert(r.uptime == 321.5 && timestampMonitor == 1);
        }
        for (int missing = 0; missing < 3; missing++) {
            onMain = missing != 0; timestampWindow = missing == 1 ? nil : fixture;
            timestampMonitor = missing != 2; reads = 0;
            rd_timestamp_readiness r = rd_timestamp_readiness_snapshot();
            assert(!r.sampled && !r.visible && !r.self_key && !r.app_active && !r.key_window);
            assert(r.main_thread == onMain && r.uptime == 0 && reads == 0);
        }
        [fixture release];
    }
    return 0;
}
`

// Parse, never execute, the Fyne test package: its TestMain initializes native UI.
func TestRecoveryTimestampBindOnlyCallGraph(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../presentation/fyne/recovery_timestamp_probe_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions[function.Name.Name] = function
		}
	}
	visitTimestampBindFunction(t, functions, make(map[string]bool), "TestMacOSRecoveryTimestampBindDiagnostic")
}

func visitTimestampBindFunction(t *testing.T, functions map[string]*ast.FuncDecl, visited map[string]bool, name string) {
	t.Helper()
	if visited[name] {
		return
	}
	visited[name] = true
	function := functions[name]
	if function == nil {
		t.Fatalf("missing zero-key entry: %s", name)
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		checkTimestampBindCall(t, functions, visited, call)
		return true
	})
}

func checkTimestampBindCall(t *testing.T, functions map[string]*ast.FuncDecl, visited map[string]bool, call *ast.CallExpr) {
	t.Helper()
	switch target := call.Fun.(type) {
	case *ast.Ident:
		if functions[target.Name] != nil {
			visitTimestampBindFunction(t, functions, visited, target.Name)
		}
	case *ast.SelectorExpr:
		switch target.Sel.Name {
		case "Post", "ArmRecoveryTimestampMonitor", "Encode", "exchange", "observe", "RequestFocus":
			t.Fatalf("zero-key setup reaches forbidden operation %s", target.Sel.Name)
		}
	}
}

func TestRecoveryTimestampOptInsAreSeparate(t *testing.T) {
	path := "../../presentation/fyne/recovery_timestamp_probe_test.go"
	posting := timestampSource(t, path, "func TestMacOSRecoveryTimestampProbe", "func runTimestampChild")
	bind := timestampSource(t, path, "func TestMacOSRecoveryTimestampBindDiagnostic", "func runTimestampBindOnly")
	for _, pair := range [][2]string{{posting, "timestampBindOptIn"}, {bind, "timestampProbeOptIn"}} {
		if !strings.Contains(pair[0], "if os.Getenv("+pair[1]+") == \"1\" {") {
			t.Fatalf("missing conflicting opt-in refusal for %s", pair[1])
		}
	}
}

func TestRecoveryTimestampInitialActivationGate(t *testing.T) {
	for bits := 0; bits < 64; bits++ {
		r := RecoveryTimestampReadiness{Sampled: bits&1 != 0, MainThread: bits&2 != 0,
			Visible: bits&4 != 0, KeyWindow: bits&8 != 0, SelfKey: bits&16 != 0, AppActive: bits&32 != 0}
		if r.ReadyForBind() != (bits == 63) {
			t.Fatalf("unexpected readiness for flags %d", bits)
		}
	}
	path := "../../presentation/fyne/recovery_timestamp_probe_test.go"
	install := timestampSource(t, path, "func recoveryTimestampInstall", "func recoveryTimestampServe")
	if strings.Count(install, "w.RequestFocus()") != 1 || strings.Index(install, "w.RequestFocus()") > strings.Index(install, "platform.InstallRecoveryTimestampMonitor") {
		t.Fatal("initial activation must occur once before monitor installation")
	}
	ready := timestampSource(t, path, "func timestampSinkReady", "func logTimestampReadiness")
	if !strings.Contains(ready, "!ready.Readiness.ReadyForBind()") || !strings.Contains(ready, "t.Fatal(") {
		t.Fatal("incomplete initial activation must stop before Bind")
	}
}
