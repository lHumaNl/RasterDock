//go:build integration && darwin

package fyneui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

type geometryChild struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	replies chan geometryReply
	done    chan struct{}
	err     error // synchronized by done
	closed  bool
}

func startGeometryGUI(ctx context.Context, t *testing.T, root string) *geometryChild {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable) //nolint:gosec // Re-exec only the owned test executable, no shell.
	cmd.Env = append(os.Environ(), "RASTERDOCK_NATIVE_GUI_HELPER=1", "RASTERDOCK_GUI_ROOT="+root)
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &geometryChild{cmd: cmd, input: input, replies: make(chan geometryReply, 1), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(c.replies)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var reply geometryReply
			if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
				reply.Error = "non-protocol stdout"
			}
			select {
			case c.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { c.err = cmd.Wait(); close(c.done) }()
	t.Cleanup(func() { c.close(t) })
	c.call(t, geometryRequest{})
	return c
}

func (c *geometryChild) close(t *testing.T) {
	t.Helper()
	if c.closed {
		return
	}
	c.closed = true
	_ = c.input.Close()
	select {
	case <-c.done:
		if c.err != nil {
			t.Errorf("GUI PID %d exit: %v", c.cmd.Process.Pid, c.err)
		}
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
		t.Errorf("GUI PID %d did not close gracefully within 5s", c.cmd.Process.Pid)
	}
}

func (c *geometryChild) call(t *testing.T, request geometryRequest) geometryReply {
	t.Helper()
	if err := json.NewEncoder(c.input).Encode(request); err != nil {
		t.Fatal(err)
	}
	select {
	case reply, ok := <-c.replies:
		if !ok {
			t.Fatal("GUI protocol closed")
		}
		if reply.Error != "" {
			t.Fatal(reply.Error)
		}
		if strings.HasPrefix(reply.Status, "Operation stopped:") {
			t.Fatal(reply.Status)
		}
		return reply
	case <-time.After(8 * time.Second):
		t.Fatal("GUI protocol unresponsive for 8s")
	}
	return geometryReply{}
}

// Log only observable state, not source/destination/session paths.
func geometryEvidence(r geometryReply) string {
	status, _, _ := strings.Cut(r.Status, " Session:")
	return fmt.Sprintf("title=%q status=%q profile=%q/%s/%s dimensions=%q picture=%v visible=%v session-present=%v", r.Title, status, r.Colors, r.Module, r.Grid, r.Dimensions, r.Picture, r.Visible, r.Session != "")
}

func (c *geometryChild) wait(t *testing.T, description string, limit time.Duration, predicate func(geometryReply) bool) geometryReply {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last geometryReply
	for time.Now().Before(deadline) {
		last = c.call(t, geometryRequest{})
		if predicate(last) {
			t.Logf("%s: %s", description, geometryEvidence(last))
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout %s: %s", description, geometryEvidence(last))
	return last
}

func geometryProgress(r geometryReply) (durable int64, published, total int) {
	_, _ = fmt.Sscanf(r.Status, "Durable bytes: %d. Published files: %d / %d.", &durable, &published, &total)
	return
}

func geometryReleased(t *testing.T, session string) {
	t.Helper()
	lock := flock.New(filepath.Join(session, "owner.lock"))
	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("receive ownership not released: locked=%v err=%v", locked, err)
	}
}

// An observation interval, not a rendering delay: check every poll and fail on
// any automatic restart. Longer than several production playback ticks.
func (c *geometryChild) stays(t *testing.T, description string, predicate func(geometryReply) bool) {
	t.Helper()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		r := c.call(t, geometryRequest{})
		if !predicate(r) {
			t.Fatalf("%s: %s", description, geometryEvidence(r))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
