//go:build integration && darwin && cgo

package platform

/*
#cgo CFLAGS: -DRD_RECOVERY_TIMESTAMP_PROBE
#include <stdlib.h>
#include "recovery_timestamp_probe_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sync"
	"unsafe"
)

// RecoveryTimestampHelper selects the isolated integration sink child.
const RecoveryTimestampHelper = "RASTERDOCK_RECOVERY_TIMESTAMP_HELPER"

// RecoveryTimestampTitle carries the exact randomly named owned window title.
const RecoveryTimestampTitle = "RASTERDOCK_RECOVERY_TIMESTAMP_TITLE"

// RecoveryTimestampBaselinePosts caps the fixed three-pair baseline manifest.
const RecoveryTimestampBaselinePosts = 6

// RecoveryTimestampManifest is a copy: backlog entries are never attempted.
func RecoveryTimestampManifest() [6]string {
	return [6]string{"default/baseline", "old/baseline", "zero/baseline", "default/backlog:UNQUALIFIED", "old/backlog:UNQUALIFIED", "zero/backlog:UNQUALIFIED"}
}

// RecoveryTimestampProbe owns one synchronous native binding. Close waits for any
// in-flight native call; cancellation must kill the child, never free the binding.
type RecoveryTimestampProbe struct {
	mu     sync.Mutex
	native *C.rd_timestamp_probe
}

// RecoveryTimestampPost records the event timestamps before native delivery.
type RecoveryTimestampPost struct {
	Creation, Prepost uint64
	Slot              uint
}

// RecoveryTimestampObservation contains only the expected event's scalar timing.
type RecoveryTimestampObservation struct {
	Seen, Failed      bool
	Timestamp, Cutoff float64
}

// RecoveryTimestampOpenPhase identifies the existing predicate that stopped setup.
// OwnerPin and Fixture are composite checks, not diagnoses of their subpredicates.
type RecoveryTimestampOpenPhase int

// Stable phases mirror the integration-only native result; no target metadata.
const (
	RecoveryTimestampOpenUnknown          RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_UNKNOWN
	RecoveryTimestampOpenReady            RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_READY
	RecoveryTimestampOpenFixture          RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_FIXTURE
	RecoveryTimestampOpenAlreadyAttempted RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_ALREADY_ATTEMPTED
	RecoveryTimestampOpenAllocation       RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_ALLOCATION
	RecoveryTimestampOpenArgument         RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_ARGUMENT
	RecoveryTimestampOpenOwnerPin         RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_OWNER_PIN
	RecoveryTimestampOpenBind             RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_BIND
	RecoveryTimestampOpenTargetCopy       RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_TARGET_COPY
	RecoveryTimestampOpenTargetMismatch   RecoveryTimestampOpenPhase = C.RD_TIMESTAMP_OPEN_TARGET_MISMATCH
)

func (p RecoveryTimestampOpenPhase) String() string {
	names := [...]string{"unknown", "ready", "direct_child_fixture", "already_attempted", "allocation", "argument", "owner_pin", "bind", "target_copy", "target_mismatch"}
	if p < 0 || int(p) >= len(names) {
		return names[0]
	}
	return names[p]
}

// RecoveryTimestampOpenError preserves the first native failure without logging
// titles, paths, or a guessed explanation for a composite identity refusal.
type RecoveryTimestampOpenError struct {
	Phase      RecoveryTimestampOpenPhase
	Diagnostic InputDiagnosticError
}

func (e *RecoveryTimestampOpenError) Error() string {
	return fmt.Sprintf("timestamp open refused phase=%s stage=%s code=%s has_ax_error=%t ax_error=%d",
		e.Phase, e.Diagnostic.Stage, e.Diagnostic.Code, e.Diagnostic.HasAXError, e.Diagnostic.AXError)
}

func (e *RecoveryTimestampOpenError) Unwrap() error { return &e.Diagnostic }

func timestampOpenError(result C.rd_timestamp_open_result) error {
	d := result.diagnostic
	return &RecoveryTimestampOpenError{Phase: RecoveryTimestampOpenPhase(result.phase), Diagnostic: InputDiagnosticError{
		Stage: InputDiagnosticStage(d.stage), Code: InputDiagnosticCode(d.code), HasAXError: d.has_ax_error != 0, AXError: int(d.ax_error),
	}}
}

func recoveryTimestampChild(cmd *exec.Cmd, title string) bool {
	exe, err := os.Executable()
	return err == nil && cmd != nil && cmd.Process != nil && cmd.Path == exe &&
		regexp.MustCompile(`^RasterDock-timestamp-[0-9a-f]{32}$`).MatchString(title) &&
		slices.Contains(cmd.Env, RecoveryTimestampHelper+"=1") &&
		slices.Contains(cmd.Env, RecoveryTimestampTitle+"="+title)
}

// OpenRecoveryTimestampProbe binds once to the exact owned sink child.
func OpenRecoveryTimestampProbe(cmd *exec.Cmd, window uint32, title string) (*RecoveryTimestampProbe, error) {
	if !recoveryTimestampChild(cmd, title) {
		return nil, &RecoveryTimestampOpenError{Phase: RecoveryTimestampOpenFixture}
	}
	text := C.CString(title)
	defer C.free(unsafe.Pointer(text))
	var result C.rd_timestamp_open_result
	p := C.rd_timestamp_open(C.int(cmd.Process.Pid), C.uint32_t(window), text, &result)
	if p == nil {
		return nil, timestampOpenError(result)
	}
	return &RecoveryTimestampProbe{native: p}, nil
}

// Post sends the next fixed slot only after full native and final-post guards.
func (p *RecoveryTimestampProbe) Post(slot uint) (RecoveryTimestampPost, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out C.rd_timestamp_post
	var diagnostic C.rd_input_diagnostic
	if slot >= RecoveryTimestampBaselinePosts {
		C.rd_timestamp_close(p.native)
		p.native = nil
		return RecoveryTimestampPost{}, errors.New("invalid timestamp slot; permanently closed")
	}
	if C.rd_timestamp_send(p.native, C.uint(slot), &out, &diagnostic) == 0 {
		return RecoveryTimestampPost{}, fmt.Errorf("timestamp preflight/post refused stage=%d code=%d; terminate child, no release cleanup", diagnostic.stage, diagnostic.code)
	}
	return RecoveryTimestampPost{uint64(out.creation), uint64(out.prepost), slot}, nil
}

// Close waits for any native call before releasing the private binding.
func (p *RecoveryTimestampProbe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	C.rd_timestamp_close(p.native)
	p.native = nil
}

// InstallRecoveryTimestampMonitor must run on the native main thread, with the
// exact own NSWindow handle obtained via Fyne's NativeWindow.RunNative callback.
func InstallRecoveryTimestampMonitor(window uintptr, title string) uint32 {
	text := C.CString(title)
	defer C.free(unsafe.Pointer(text))
	return uint32(C.rd_timestamp_monitor(C.uintptr_t(window), text))
}

// RecoveryTimestampReadiness contains own-window startup scalars, not AX proof.
// KeyWindow means NSApp.keyWindow is the sink; SelfKey is its isKeyWindow getter.
type RecoveryTimestampReadiness struct {
	Sampled, MainThread, Visible, KeyWindow, SelfKey, AppActive bool
	Uptime                                                      float64
}

// ReadyForBind verifies only initial own-window presentation and focus, not AX
// availability or posting authority. A false result must not trigger refocus.
func (r RecoveryTimestampReadiness) ReadyForBind() bool {
	return r.Sampled && r.MainThread && r.Visible && r.KeyWindow && r.SelfKey && r.AppActive
}

// ReadRecoveryTimestampReadiness snapshots once after monitor installation in
// the same native main-thread callback. It never gates, waits, or changes focus.
func ReadRecoveryTimestampReadiness() RecoveryTimestampReadiness {
	return recoveryTimestampReadiness(C.rd_timestamp_readiness_snapshot())
}

func recoveryTimestampReadiness(r C.rd_timestamp_readiness) RecoveryTimestampReadiness {
	return RecoveryTimestampReadiness{
		Sampled: r.sampled != 0, MainThread: r.main_thread != 0, Visible: r.visible != 0,
		KeyWindow: r.key_window != 0, SelfKey: r.self_key != 0, AppActive: r.app_active != 0,
		Uptime: float64(r.uptime),
	}
}

// RecoveryTimestampInitialStatus describes the single bounded activation handoff.
type RecoveryTimestampInitialStatus int

// The immutable handoff never authorizes a later refocus or a Bind retry.
const (
	RecoveryTimestampInitialPending     RecoveryTimestampInitialStatus = C.RD_TIMESTAMP_INITIAL_PENDING
	RecoveryTimestampInitialReady       RecoveryTimestampInitialStatus = C.RD_TIMESTAMP_INITIAL_READY
	RecoveryTimestampInitialTimeout     RecoveryTimestampInitialStatus = C.RD_TIMESTAMP_INITIAL_TIMEOUT
	RecoveryTimestampInitialUnavailable RecoveryTimestampInitialStatus = C.RD_TIMESTAMP_INITIAL_UNAVAILABLE
)

func (s RecoveryTimestampInitialStatus) String() string {
	names := [...]string{"pending", "ready", "timeout", "unavailable"}
	if s < 0 || int(s) >= len(names) {
		return "unavailable"
	}
	return names[s]
}

// AwaitRecoveryTimestampInitial waits once on the helper worker, never on the
// AppKit main thread. The fixed two-second deadline starts during installation.
func AwaitRecoveryTimestampInitial() (RecoveryTimestampReadiness, RecoveryTimestampInitialStatus) {
	r := C.rd_timestamp_initial_wait()
	return recoveryTimestampReadiness(r.readiness), RecoveryTimestampInitialStatus(r.status)
}

// ArmRecoveryTimestampMonitor selects the next expected event on the main thread.
func ArmRecoveryTimestampMonitor(slot uint) bool {
	return slot < RecoveryTimestampBaselinePosts && C.rd_timestamp_arm(C.uint(slot)) != 0
}

// ObserveRecoveryTimestampMonitor reads sink status on the main thread.
func ObserveRecoveryTimestampMonitor() RecoveryTimestampObservation {
	o := C.rd_timestamp_observe()
	return RecoveryTimestampObservation{o.seen != 0, o.failed != 0, float64(o.timestamp), float64(o.cutoff)}
}

// ClassifyRecoveryTimestamp describes observations, not safety or backlog rejection.
// Missing events must never be classified as rejected timestamps.
func ClassifyRecoveryTimestamp(post RecoveryTimestampPost, o RecoveryTimestampObservation) string {
	if o.Failed || !o.Seen {
		return "UNQUALIFIED:missing-or-failed"
	}
	if (post.Slot/2 == 1 && post.Prepost != 1) || (post.Slot/2 == 2 && post.Prepost != 0) {
		return "observed:rewritten-before-post"
	}
	if o.Timestamp <= 0 || math.IsNaN(o.Timestamp) || math.IsInf(o.Timestamp, 0) {
		return "observed:zero-or-invalid"
	}
	if post.Prepost == 0 || (post.Prepost == 1 && o.Timestamp != 0.000000001) {
		return "observed:rewritten"
	}
	if o.Timestamp < o.Cutoff {
		return "observed:before-cutoff"
	}
	return "observed:at-or-after-cutoff"
}
