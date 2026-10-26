//go:build integration && darwin

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices
#include <stdlib.h>
#include "keyboard_probe_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

// KeyboardProbe is integration-only, single-owner and permanently fails closed.
// It cannot bind an arbitrary PID: only a live direct child test executable with
// the opt-in helper environment and exact unique UUID window title is accepted.
type KeyboardProbe struct {
	pid       int
	window    uint32
	title     string
	identity  uint64
	stopped   bool
	remaining uint64
}

// KeyboardProbePermissions checks existing grants without prompting.
func KeyboardProbePermissions() bool { return C.rd_probe_permissions() != 0 }

// BindKeyboardProbe pins a direct child test process and its exact UUID window.
func BindKeyboardProbe(cmd *exec.Cmd, window uint32, title string, commands uint64) (*KeyboardProbe, error) {
	exe, err := os.Executable()
	if err != nil || cmd == nil || cmd.Process == nil || cmd.Path != exe || commands == 0 || commands > 512 ||
		!regexp.MustCompile(`^RasterDock-keyboard-probe-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(title) {
		return nil, errors.New("probe requires bounded own test child and UUID title")
	}
	helper, named := false, false
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "RASTERDOCK_KEYBOARD_PROBE_HELPER=") {
			helper = e == "RASTERDOCK_KEYBOARD_PROBE_HELPER=1"
		}
		if strings.HasPrefix(e, "RASTERDOCK_KEYBOARD_PROBE_TITLE=") {
			named = e == "RASTERDOCK_KEYBOARD_PROBE_TITLE="+title
		}
	}
	if !helper || !named {
		return nil, errors.New("not a keyboard probe helper")
	}
	s := C.CString(title)
	defer C.free(unsafe.Pointer(s))
	id := uint64(C.rd_probe_identity(C.int(cmd.Process.Pid), C.uint(window), s))
	if id == 0 {
		return nil, errors.New("untrusted, missing or ambiguous child window")
	}
	return &KeyboardProbe{pid: cmd.Process.Pid, window: window, title: title, identity: id, remaining: commands}, nil
}

// Check observes foreground/ownership/modifiers and permanently latches refusal.
func (p *KeyboardProbe) Check() error {
	if p == nil || p.stopped {
		return errors.New("keyboard probe stopped")
	}
	s := C.CString(p.title)
	defer C.free(unsafe.Pointer(s))
	if C.rd_probe_check(C.int(p.pid), C.uint(p.window), s, C.uint64_t(p.identity)) == 0 {
		p.stopped = true
		return errors.New("probe lost trusted foreground child/window identity or modifiers held; no refocus/retry")
	}
	return nil
}

// Send posts physical F8, digits, Tab, digits, Return. Eligibility is rechecked
// before EACH keydown, in addition to native foreground/live identity checks.
func (p *KeyboardProbe) Send(ctx context.Context, nonce, ordinal uint64, eligible func() error) error {
	if p == nil || p.stopped || p.remaining == 0 || nonce == 0 || eligible == nil {
		return errors.New("probe command refused")
	}
	p.remaining--
	digits := [...]uint16{29, 18, 19, 20, 21, 23, 22, 26, 28, 25}
	keys := []uint16{100} // macOS kVK_F8
	for _, c := range strconv.FormatUint(nonce, 10) {
		keys = append(keys, digits[c-'0'])
	}
	keys = append(keys, 48)
	for _, c := range strconv.FormatUint(ordinal, 10) {
		keys = append(keys, digits[c-'0'])
	}
	keys = append(keys, 36)
	s := C.CString(p.title)
	defer C.free(unsafe.Pointer(s))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			p.stopped = true
			return err
		}
		if err := eligible(); err != nil {
			p.stopped = true
			return err
		}
		if C.rd_probe_key(C.int(p.pid), C.uint(p.window), s, C.uint64_t(p.identity), C.ushort(key)) == 0 {
			p.stopped = true
			return errors.New("native keydown safety gate refused; sequence aborted")
		}
		select {
		case <-ctx.Done():
			p.stopped = true
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
	return nil
}

// KeyboardProbeViewerFocused only observes the calling helper's own key window.
func KeyboardProbeViewerFocused(title string) bool {
	s := C.CString(title)
	defer C.free(unsafe.Pointer(s))
	return C.rd_probe_viewer_focus(s) != 0
}
