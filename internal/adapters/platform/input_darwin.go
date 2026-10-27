//go:build darwin && cgo

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices -framework Cocoa
#include <stdlib.h>
#include "input_darwin.h"
*/
import "C"

import (
	"strings"
	"time"
	"unsafe"
)

func inputAXAllowance(now, deadline uint64) uint64 {
	return uint64(C.rd_input_ax_allowance(C.uint64_t(now), C.uint64_t(deadline)))
}

func inputBindDeadline(now uint64) uint64 {
	return uint64(C.rd_input_bind_deadline(C.uint64_t(now)))
}

// InputCapability observes existing input grants; it never requests permission.
// It does not establish capture capability or authorize sending.
func InputCapability() error {
	if C.rd_input_capability() == 0 {
		return ErrInputUnavailable
	}
	return nil
}

// BindInput pins a CGWindowID, owner PID, process start time and AX window.
// A nonempty, unique, unchanged title is required for the public-API fallback.
func BindInput(sourceID uint32) (InputBinding, error) {
	return bindDarwinInput(sourceID, nil)
}

// The optional factory replaces only Check before publication; ownership and
// production gate/fail-stop behavior remain on the same binding.
func bindDarwinInput(sourceID uint32, checkFactory func(*C.rd_input) func() error) (InputBinding, error) {
	var diagnostic C.rd_input_diagnostic
	p := C.rd_input_bind(C.uint32_t(sourceID), &diagnostic)
	if p == nil {
		return nil, inputNativeResult(0, diagnostic)
	}
	target, err := copyInputTarget(p)
	if err != nil {
		C.rd_input_close(p)
		return nil, err
	}
	b := newDarwinInput(p, target)
	if checkFactory != nil {
		b.check = checkFactory(p)
	}
	return b, nil
}

func newDarwinInput(p *C.rd_input, target ControlTarget) *inputBinding {
	b := &inputBinding{done: make(chan struct{}), target: target}
	b.check = func() error {
		return inputNativeCall(func(d *C.rd_input_diagnostic) C.int { return C.rd_input_check(p, d) })
	}
	b.deadline = func(budget time.Duration) uint64 { return uint64(C.rd_input_deadline(C.uint64_t(budget))) }
	b.prepare = func(deadline uint64) error {
		return inputNativeCall(func(d *C.rd_input_diagnostic) C.int { return C.rd_input_prepare(p, C.uint64_t(deadline), d) })
	}
	b.key = func(key uint16) error {
		return inputNativeCall(func(d *C.rd_input_diagnostic) C.int { return C.rd_input_key(p, C.uint16_t(key), d) })
	}
	b.release = func() { C.rd_input_close(p) }
	return b
}

func copyInputTarget(p *C.rd_input) (ControlTarget, error) {
	var native C.rd_input_target
	var d C.rd_input_diagnostic
	if C.rd_input_copy_target(p, &native, &d) == 0 { //nolint:gocritic // cgo emits duplicate pointer checks for the native output parameter.
		return ControlTarget{}, inputNativeResult(0, d)
	}
	defer C.free(unsafe.Pointer(native.title))
	target := ControlTarget{Window: uint32(native.window), PID: int(native.pid),
		Started: uint64(native.started), Title: C.GoStringN(native.title, native.title_length)}
	if !target.Valid() {
		return ControlTarget{}, &InputDiagnosticError{Stage: InputStageTarget, Code: InputCodeTargetInvalid}
	}
	return target, nil
}

func inputNativeCall(call func(*C.rd_input_diagnostic) C.int) error {
	var d C.rd_input_diagnostic
	result := call(&d)
	return inputNativeResult(result, d)
}

func inputNativeResult(result C.int, d C.rd_input_diagnostic) error {
	if result == 0 {
		return &InputDiagnosticError{Stage: InputDiagnosticStage(d.stage), Code: InputDiagnosticCode(d.code),
			HasAXError: d.has_ax_error != 0, AXError: int(d.ax_error)}
	}
	return nil
}

// classifyInputAX exercises the native pure classifier without AX/CG calls.
// Existing diagnostics seed the output to verify first-failure preservation.
func classifyInputAX(first InputDiagnosticError, stage InputDiagnosticStage, timely bool, ax int, code InputDiagnosticCode) error {
	d := C.rd_input_diagnostic{stage: C.rd_input_stage(first.Stage), code: C.rd_input_code(first.Code), ax_error: C.int(first.AXError)}
	if first.HasAXError {
		d.has_ax_error = 1
	}
	var within C.int
	if timely {
		within = 1
	}
	result := C.rd_input_ax_result(&d, C.rd_input_stage(stage), within, C.int(ax), C.rd_input_code(code))
	return inputNativeResult(result, d)
}

// OwnWindowFocused must be combined with the GUI command-surface focus guard.
func OwnWindowFocused(title string) bool {
	focused, _ := OwnWindowFocusState(title)
	return focused
}

// OwnWindowFocusState returns current focus and a process-lifetime loss generation.
// Snapshot on explicit arm and require an unchanged generation on every check;
// regaining focus never clears a loss. Saturation permanently reports false.
// Call only while the GUI main loop is live; observations dispatch to its thread.
func OwnWindowFocusState(title string) (bool, uint64) {
	if title == "" || strings.ContainsRune(title, '\x00') {
		return false, 0
	}
	s := C.CString(title)
	defer C.free(unsafe.Pointer(s))
	state := C.rd_input_own_focus_state(s)
	return state.focused != 0, uint64(state.generation)
}
