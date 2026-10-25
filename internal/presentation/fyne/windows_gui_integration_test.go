//go:build integration && windows

package fyneui

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/gofrs/flock"

	"rasterdock/locales"
)

// This protocol exists only in the integration test executable. It invokes
// production widgets, never engine commands, rendered packets or synthetic capture.
type windowsGUIRequest struct {
	Source, Destination, Session *string
	Tap, SelectTitle             string
}

type windowsGUIReply struct {
	Title, Status, Session, Selected, Error string
	Options                                 []string
}

func init() { nativeGUIHelper = windowsGUIHelper }

func windowsGUIHelper() error {
	root := os.Getenv("RASTERDOCK_GUI_ROOT")
	if root == "" {
		return fmt.Errorf("missing isolated GUI root")
	}
	catalog, err := locales.Load("en")
	if err != nil {
		return err
	}
	go func() {
		// Run creates the app on the main thread. Queue all window/tree access
		// on its event thread; no OS input, resizing, scaling or focus automation.
		for fyne.CurrentApp() == nil {
			time.Sleep(10 * time.Millisecond)
		}
		var w fyne.Window
		for w == nil {
			fyne.DoAndWait(func() {
				for _, candidate := range fyne.CurrentApp().Driver().AllWindows() {
					if candidate.Content() != nil {
						w = candidate
						break
					}
				}
			})
			if w == nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
		defer fyne.Do(w.Close) // EOF uses the real close interceptor and drains workers.
		scanner := bufio.NewScanner(os.Stdin)
		encoder := json.NewEncoder(os.Stdout)
		for scanner.Scan() {
			var request windowsGUIRequest
			var reply windowsGUIReply
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				reply.Error = err.Error()
			} else {
				fyne.DoAndWait(func() { reply = windowsGUIDrive(w, request) })
			}
			if encoder.Encode(reply) != nil {
				return
			}
		}
	}()
	Run(root, catalog, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return nil
}

func windowsGUIDrive(w fyne.Window, request windowsGUIRequest) (reply windowsGUIReply) {
	// Fail explicitly when production structure changes instead of silently
	// operating a different control. Entry ordering follows the public form.
	split, ok := w.Content().(*container.Split)
	if !ok {
		reply.Error = "production content is not Split"
		return
	}
	scroll, ok := split.Leading.(*container.Scroll)
	if !ok {
		reply.Error = "production controls are not Scroll"
		return
	}
	box, ok := scroll.Content.(*fyne.Container)
	if !ok {
		reply.Error = "production controls are not VBox"
		return
	}
	var entries []*widget.Entry
	var selectWindow *widget.Select
	var buttons []*widget.Button
	var walk func(fyne.CanvasObject)
	walk = func(object fyne.CanvasObject) {
		switch o := object.(type) {
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child)
			}
		case *widget.Entry:
			entries = append(entries, o)
		case *widget.Select:
			selectWindow = o
		case *widget.Button:
			buttons = append(buttons, o)
		}
	}
	walk(box)
	if len(entries) != 3 || selectWindow == nil {
		reply.Error = "unexpected production form"
		return
	}
	status, ok := box.Objects[len(box.Objects)-1].(*widget.Label)
	if !ok {
		reply.Error = "missing production status"
		return
	}
	for i, value := range []*string{request.Source, request.Destination, request.Session} {
		if value != nil {
			entries[i].SetText(*value)
		}
	}
	if request.SelectTitle != "" {
		matches := 0
		for _, option := range selectWindow.Options {
			// The ID prefix is not guessed; the complete PID-bearing title is
			// obtained from our sender subprocess and matched exactly.
			_, title, found := strings.Cut(option, " · ")
			if found && (title == request.SelectTitle || strings.HasSuffix(title, " — "+request.SelectTitle)) {
				selectWindow.SetSelected(option)
				matches++
			}
		}
		if matches != 1 {
			reply.Error = fmt.Sprintf("owned sender title matches: %d", matches)
			return
		}
	}
	if request.Tap != "" {
		matches := 0
		for _, button := range buttons {
			if button.Text == request.Tap {
				button.OnTapped()
				matches++
			}
		}
		if matches != 1 {
			reply.Error = fmt.Sprintf("button %q matches: %d", request.Tap, matches)
			return
		}
	}
	reply.Title, reply.Status, reply.Session = w.Title(), status.Text, entries[2].Text
	reply.Selected = selectWindow.Selected
	reply.Options = append([]string(nil), selectWindow.Options...)
	return
}

type windowsGUIChild struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	replies chan windowsGUIReply
	done    chan struct{}
	err     error // read only after done closes
	closed  bool
}

func startWindowsGUI(ctx context.Context, t *testing.T, root string) *windowsGUIChild {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable) //nolint:gosec // Re-exec only os.Executable(), the owned integration test binary, without a shell.
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
	c := &windowsGUIChild{cmd: cmd, input: input, replies: make(chan windowsGUIReply, 1), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(c.replies)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var reply windowsGUIReply
			if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
				reply.Error = "non-protocol stdout: " + scanner.Text()
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
	c.call(t, windowsGUIRequest{})
	return c
}

func (c *windowsGUIChild) close(t *testing.T) {
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
		t.Errorf("GUI PID %d failed graceful close within 5s", c.cmd.Process.Pid)
	}
}

func (c *windowsGUIChild) call(t *testing.T, request windowsGUIRequest) windowsGUIReply {
	t.Helper()
	if err := json.NewEncoder(c.input).Encode(request); err != nil {
		t.Fatal(err)
	}
	select {
	case reply, ok := <-c.replies:
		if !ok {
			t.Fatal("GUI protocol closed unexpectedly")
		}
		if reply.Error != "" {
			t.Fatal(reply.Error)
		}
		if strings.HasPrefix(reply.Status, "Operation stopped:") {
			t.Fatal(reply.Status)
		}
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("GUI callback/protocol unresponsive for 5s")
	}
	return windowsGUIReply{}
}

func (c *windowsGUIChild) wait(t *testing.T, description string, limit time.Duration, predicate func(windowsGUIReply) bool) windowsGUIReply {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last windowsGUIReply
	for time.Now().Before(deadline) {
		last = c.call(t, windowsGUIRequest{})
		if predicate(last) {
			t.Logf("%s: %s", description, last.Status)
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s: title=%q status=%q session=%q option-count=%d", description, last.Title, last.Status, last.Session, len(last.Options))
	return last
}

func windowsGUIProgress(reply windowsGUIReply) (bytes int64, published, total int) {
	_, _ = fmt.Sscanf(reply.Status, "Durable bytes: %d. Published files: %d / %d.", &bytes, &published, &total)
	return
}

func windowsGUIReleased(t *testing.T, session string) {
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

// This smoke test validates the protocol and production callbacks independently
// of capture availability. It does not stand in for the real-WGC resume test.
func TestWindowsProductionGUIControls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	root := t.TempDir()
	source := filepath.Join(root, "fixture.bin")
	if err := os.WriteFile(source, make([]byte, 1800), 0600); err != nil {
		t.Fatal(err)
	}
	child := startWindowsGUI(ctx, t, filepath.Join(root, "state-root"))
	child.call(t, windowsGUIRequest{Source: &source, Tap: "Prepare"})
	child.wait(t, "prepare callback", 8*time.Second, func(r windowsGUIReply) bool { return strings.HasPrefix(r.Status, "Packet 1 / ") })
	child.call(t, windowsGUIRequest{Tap: "Next frame"})
	child.wait(t, "next callback", 3*time.Second, func(r windowsGUIReply) bool { return strings.HasPrefix(r.Status, "Packet 2 / ") })
	child.call(t, windowsGUIRequest{Tap: "Timer playback"})
	child.wait(t, "timer callback", 3*time.Second, func(r windowsGUIReply) bool {
		return strings.HasPrefix(r.Status, "Packet ") && !strings.HasPrefix(r.Status, "Packet 2 / ")
	})
	child.call(t, windowsGUIRequest{Tap: "Pause"})
	child.wait(t, "pause callback", 3*time.Second, func(r windowsGUIReply) bool { return r.Status == "Paused; saved state retained" })
	time.Sleep(600 * time.Millisecond)
	if reply := child.call(t, windowsGUIRequest{}); reply.Status != "Paused; saved state retained" {
		t.Fatalf("timer continued after pause: %+v", reply)
	}
	child.close(t)
}

// TestWindowsProductionGUIResume exercises native windows at their natural scale.
// Programmatic production callbacks are NOT manual mouse/keyboard acceptance or
// a physical monitor/DPI sweep. Only the owned sender is selected for real WGC.
func TestWindowsProductionGUIResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	t.Cleanup(cancel)
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	binary := make([]byte, 2301)
	for i := range binary {
		binary[i] = byte((i*73 + i/256) % 256)
	}
	want := map[string][]byte{"binary.bin": binary, "empty": {}, "目录/вложенный/hello.txt": []byte("Hello, мир 世界\n")}
	for name, data := range want {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	sender := startWindowsGUI(ctx, t, filepath.Join(root, "sender"))
	sender.call(t, windowsGUIRequest{Source: &source, Tap: "Prepare"})
	frame := sender.wait(t, "prepared production sender", 10*time.Second, func(r windowsGUIReply) bool { return strings.HasPrefix(r.Status, "Packet 1 / ") })
	if !strings.HasSuffix(frame.Title, fmt.Sprintf(" · Sender · PID %d", sender.cmd.Process.Pid)) {
		t.Fatalf("unexpected sender identity: %s", frame.Title)
	}
	var ordinal, packets int
	if _, err := fmt.Sscanf(frame.Status, "Packet %d / %d", &ordinal, &packets); err != nil || packets < 4 {
		t.Fatalf("fixture must span multiple packets: %s", frame.Status)
	}
	title := frame.Title
	recordOwnedGUIFrame(t, title)
	receiverRoot := filepath.Join(root, "receiver")
	receiver := startWindowsGUI(ctx, t, receiverRoot)
	selectSender := func(c *windowsGUIChild, session string) {
		c.call(t, windowsGUIRequest{Destination: &destination, Session: &session, Tap: "Refresh capture windows"})
		c.wait(t, "owned sender enumerated", 10*time.Second, func(r windowsGUIReply) bool {
			for _, option := range r.Options {
				if strings.HasSuffix(option, " · "+title) || strings.HasSuffix(option, " — "+title) {
					return true
				}
			}
			return false
		})
		c.call(t, windowsGUIRequest{SelectTitle: title, Tap: "Receive / resume"})
	}
	selectSender(receiver, "")
	progress := receiver.wait(t, "real beacon decoded", 12*time.Second, func(r windowsGUIReply) bool { return r.Session != "" && strings.HasPrefix(r.Status, "Durable bytes:") })
	session := progress.Session
	if rel, err := filepath.Rel(receiverRoot, session); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("session escaped isolated root: %s", session)
	}
	next := func() {
		ordinal = ordinal%packets + 1
		sender.call(t, windowsGUIRequest{Tap: "Next frame"})
		prefix := fmt.Sprintf("Packet %d / ", ordinal)
		sender.wait(t, "manual sender advance", 3*time.Second, func(r windowsGUIReply) bool { return strings.HasPrefix(r.Status, prefix) })
	}
	advanceAndObserve := func(before int64) windowsGUIReply {
		next()
		deadline := time.Now().Add(3 * time.Second)
		var observed windowsGUIReply
		for time.Now().Before(deadline) {
			observed = receiver.call(t, windowsGUIRequest{})
			n, p, total := windowsGUIProgress(observed)
			if n > before || (total == len(want) && p == total) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return observed
	}
	// Hold a new frame until real receiver progress or a bounded retry point,
	// not a guessed rendering sleep (race instrumentation can slow readback).
	for attempts := 0; attempts < packets; attempts++ {
		bytes, _, _ := windowsGUIProgress(progress)
		if bytes > 0 {
			break
		}
		progress = advanceAndObserve(bytes)
	}
	partial, published, total := windowsGUIProgress(progress)
	if partial <= 0 || partial >= int64(len(binary)) || (total > 0 && published == total) {
		t.Fatalf("not a partial transfer: status=%q session=%q", progress.Status, progress.Session)
	}
	receiver.call(t, windowsGUIRequest{Tap: "Pause"})
	receiver.wait(t, "responsive receive pause", 3*time.Second, func(r windowsGUIReply) bool { return r.Status == "Paused; saved state retained" })
	windowsGUIReleased(t, session)
	// Reacquire in the same GUI, then close while receiving to exercise the
	// production close interceptor rather than only an already-paused shutdown.
	receiver.call(t, windowsGUIRequest{Tap: "Receive / resume"})
	receiver.wait(t, "same-process resume", 8*time.Second, func(r windowsGUIReply) bool { n, _, _ := windowsGUIProgress(r); return n >= partial })
	receiver.close(t)
	windowsGUIReleased(t, session)
	receiver = startWindowsGUI(ctx, t, receiverRoot)
	selectSender(receiver, session)
	receiver.wait(t, "process restart retains durable progress", 12*time.Second, func(r windowsGUIReply) bool {
		n, _, _ := windowsGUIProgress(r)
		return r.Session == session && n >= partial
	})
	for attempts := 0; attempts < packets*2; attempts++ {
		progress = receiver.call(t, windowsGUIRequest{})
		_, published, total = windowsGUIProgress(progress)
		if total == len(want) && published == total {
			break
		}
		before, _, _ := windowsGUIProgress(progress)
		progress = advanceAndObserve(before)
	}
	receiver.wait(t, "all files published", 8*time.Second, func(r windowsGUIReply) bool { _, p, n := windowsGUIProgress(r); return n == len(want) && p == n })
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Test-owned temporary destination and fixed fixture names.
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(data) || sha256.Sum256(got) != sha256.Sum256(data) {
			t.Fatalf("bytes/SHA-256 mismatch: %s", name)
		}
		t.Logf("verified %s bytes=%d sha256=%x", name, len(got), sha256.Sum256(got))
	}
	receiver.close(t)
	windowsGUIReleased(t, session)
	// Timer is tested only after the controlled transfer, never racing resume.
	sender.call(t, windowsGUIRequest{Tap: "Timer playback"})
	sender.wait(t, "production timer advances", 5*time.Second, func(r windowsGUIReply) bool {
		return strings.HasPrefix(r.Status, "Packet ") && !strings.HasPrefix(r.Status, fmt.Sprintf("Packet %d / ", ordinal))
	})
	sender.call(t, windowsGUIRequest{Tap: "Pause"})
	sender.wait(t, "responsive sender pause", 3*time.Second, func(r windowsGUIReply) bool { return r.Status == "Paused; saved state retained" })
	sender.close(t)
}
