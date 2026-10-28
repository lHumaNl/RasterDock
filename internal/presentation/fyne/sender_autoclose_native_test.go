//go:build integration && darwin && cgo

package fyneui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Owned GUI only; no input bindings, key events, capture, or focus actions.
func TestMacOSSenderAutoClose(t *testing.T) {
	if os.Getenv("RASTERDOCK_SENDER_AUTOCLOSE") != "1" {
		t.Skip("requires owned GUI auto-close opt-in")
	}
	t.Setenv("RASTERDOCK_SENDER_AUTOCLOSE", "")
	for _, mode := range []string{"default_three_cycles", "configured_one_cycle", "idle_timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx := keyboardProbeContext(t, 30*time.Second)
			source := filepath.Join(t.TempDir(), "payload.txt")
			if err := os.WriteFile(source, []byte("timer fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			child := startGeometryGUI(ctx, t, t.TempDir())
			child.fit(t, geometrySize(t, 1, 1, 4))
			child.call(t, geometryRequest{Source: &source, Tap: "Prepare"})
			child.wait(t, "sender prepared", 10*time.Second, func(r geometryReply) bool { return strings.HasPrefix(r.Status, "Packet ") })
			action := "Timer playback"
			request := geometryRequest{}
			if mode == "configured_one_cycle" {
				n := "1"
				request.TimerCycles = &n
			}
			if mode == "idle_timeout" {
				n := "1"
				request.IdleSeconds = &n
				action = "Controlled viewer (allow keyboard)"
			}
			child.call(t, request)
			start := time.Now()
			child.call(t, geometryRequest{Tap: action})
			select {
			case <-child.done:
				if child.err != nil {
					t.Fatal(child.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("application did not close automatically")
			}
			child.close(t)
			t.Logf("normal application exit after %s; no keyboard input", time.Since(start))
		})
	}
}
