//go:build integration && darwin && cgo

package fyneui

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver"

	"rasterdock/internal/adapters/platform"
)

const timestampProbeOptIn = "RASTERDOCK_RECOVERY_TIMESTAMP_PROBE"
const timestampBindOptIn = "RASTERDOCK_RECOVERY_TIMESTAMP_BIND"
const timestampSequenceLimit = 2 * time.Second
const timestampPostingLimit = 10 * time.Second
const timestampChildLimit = 15 * time.Second
const timestampPollInterval = 5 * time.Millisecond
const timestampFailExit = 79
const timestampRequestLimit = 1024
const timestampPairSize = 2
const timestampNonceSize = 16
const timestampSinkWidth, timestampSinkHeight = 520, 100

type timestampRequest struct {
	Arm  bool
	Slot uint
}
type timestampReply struct {
	Window        uint32
	OK            bool
	Observation   platform.RecoveryTimestampObservation
	Readiness     platform.RecoveryTimestampReadiness
	InitialStatus platform.RecoveryTimestampInitialStatus
}

// Called only from the existing TestMain GUI helper route. No second TestMain.
func recoveryTimestampGUIHelper() error {
	title := os.Getenv(platform.RecoveryTimestampTitle)
	if title == "" {
		return errors.New("missing timestamp sink title")
	}
	_ = os.Unsetenv(platform.RecoveryTimestampHelper)
	_ = os.Unsetenv("RASTERDOCK_NATIVE_GUI_HELPER")
	_ = os.Unsetenv(timestampProbeOptIn)
	_ = os.Unsetenv(timestampBindOptIn)
	a := app.New()
	w := a.NewWindow(title)
	w.SetContent(canvas.NewText("Timestamp diagnostic sink — do not type", color.White))
	w.Resize(fyne.NewSize(timestampSinkWidth, timestampSinkHeight))
	go recoveryTimestampServe(w, title)
	w.ShowAndRun()
	return nil
}

func recoveryTimestampInstall(w fyne.Window, title string) (id uint32) {
	fyne.DoAndWait(func() {
		// One initial activation of the owned sink, before monitor installation
		// and any binding. Never repeat this after loss or refusal.
		w.RequestFocus()
		native, ok := w.(driver.NativeWindow)
		if !ok {
			return
		}
		native.RunNative(func(value any) {
			context, ok := value.(driver.MacWindowContext)
			if ok {
				id = platform.InstallRecoveryTimestampMonitor(context.NSWindow, title)
			}
		})
	})
	return id
}

func recoveryTimestampServe(w fyne.Window, title string) {
	time.AfterFunc(timestampChildLimit, func() { os.Exit(timestampFailExit) })
	id := recoveryTimestampInstall(w, title)
	if id == 0 {
		os.Exit(timestampFailExit)
	}
	// Outside DoAndWait/RunNative: AppKit remains free to deliver notifications.
	readiness, status := platform.AwaitRecoveryTimestampInitial()
	encoder := json.NewEncoder(os.Stdout)
	if encoder.Encode(timestampReply{Window: id, OK: true, Readiness: readiness, InitialStatus: status}) != nil {
		os.Exit(timestampFailExit)
	}
	if status != platform.RecoveryTimestampInitialReady {
		os.Exit(timestampFailExit)
	}
	recoveryTimestampRequests(encoder)
}

func recoveryTimestampRequests(encoder *json.Encoder) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, timestampRequestLimit), timestampRequestLimit)
	for scanner.Scan() {
		var request timestampRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(timestampFailExit)
		}
		reply := recoveryTimestampDrive(request)
		if encoder.Encode(reply) != nil || !reply.OK {
			os.Exit(timestampFailExit)
		}
	}
	os.Exit(0) // Sink remains installed through process termination.
}

func recoveryTimestampDrive(request timestampRequest) (reply timestampReply) {
	fyne.DoAndWait(func() {
		if request.Arm {
			reply.OK = platform.ArmRecoveryTimestampMonitor(request.Slot)
		} else {
			reply.Observation = platform.ObserveRecoveryTimestampMonitor()
			reply.OK = !reply.Observation.Failed
		}
	})
	return reply
}

type timestampChild struct {
	cmd     *exec.Cmd
	encoder *json.Encoder
	decoder *json.Decoder
	window  uint32
}

func timestampChildEnvironment(title string) []string {
	var env []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "RASTERDOCK_") {
			env = append(env, item)
		}
	}
	return append(env, "RASTERDOCK_NATIVE_GUI_HELPER=1", platform.RecoveryTimestampHelper+"=1", platform.RecoveryTimestampTitle+"="+title)
}

func startTimestampChild(ctx context.Context, title string) (*timestampChild, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^$") //nolint:gosec // Re-exec only this test executable as the owned sink child.
	cmd.Env, cmd.Stderr = timestampChildEnvironment(title), os.Stderr
	return timestampChildPipes(cmd)
}

func timestampChildPipes(cmd *exec.Cmd) (*timestampChild, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	return &timestampChild{cmd: cmd, encoder: json.NewEncoder(in), decoder: json.NewDecoder(out)}, nil
}

func (c *timestampChild) exchange(request timestampRequest) (timestampReply, error) {
	var reply timestampReply
	if err := c.encoder.Encode(request); err != nil {
		return reply, err
	}
	if err := c.decoder.Decode(&reply); err != nil {
		return reply, err
	}
	if !reply.OK {
		return reply, errors.New("timestamp child refused; no retry/refocus")
	}
	return reply, nil
}

func (c *timestampChild) observe(deadline time.Time) (platform.RecoveryTimestampObservation, error) {
	for time.Now().Before(deadline) {
		reply, err := c.exchange(timestampRequest{})
		if err != nil {
			return reply.Observation, err
		}
		if reply.Observation.Seen {
			return reply.Observation, nil
		}
		time.Sleep(timestampPollInterval) // Observation polling never repeats a post.
	}
	return platform.RecoveryTimestampObservation{}, errors.New("missing delivery is UNQUALIFIED, not rejection")
}

func timestampPair(t *testing.T, child *timestampChild, probe *platform.RecoveryTimestampProbe, sequence uint) {
	t.Helper()
	deadline := time.Now().Add(timestampSequenceLimit)
	watchdog := time.AfterFunc(timestampSequenceLimit, func() { _ = child.cmd.Process.Kill() })
	defer watchdog.Stop()
	for slot := sequence * timestampPairSize; slot < (sequence+1)*timestampPairSize; slot++ {
		timestampSingle(t, child, probe, slot, deadline)
	}
	t.Logf("sequence=%d PASS: both deliveries observed and dropped; NOT recovery qualification", sequence)
}

func timestampSingle(t *testing.T, child *timestampChild, probe *platform.RecoveryTimestampProbe, slot uint, deadline time.Time) {
	t.Helper()
	if _, err := child.exchange(timestampRequest{Arm: true, Slot: slot}); err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(deadline) {
		t.Fatal("sequence deadline before post")
	}
	post, err := probe.Post(slot)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := child.observe(deadline)
	if err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(deadline) {
		t.Fatal("sequence deadline after delivery")
	}
	logTimestampDelivery(t, slot, post, observation)
}

func logTimestampDelivery(t *testing.T, slot uint, post platform.RecoveryTimestampPost, observation platform.RecoveryTimestampObservation) {
	t.Helper()
	t.Logf("slot=%d creation_ns=%d prepost_ns=%d NSEvent_seconds=%.9f cutoff_seconds=%.9f dropped=true %s",
		slot, post.Creation, post.Prepost, observation.Timestamp, observation.Cutoff, platform.ClassifyRecoveryTimestamp(post, observation))
}

// Lead-only after independent security review. Sanitize BEFORE TestMain; never
// run this package to perform headless tests (TestMain creates native windows).
// env -i HOME="$HOME" PATH="$PATH" TMPDIR="$TMPDIR" CGO_ENABLED=1 \
// RASTERDOCK_RECOVERY_TIMESTAMP_PROBE=1 go test -tags=integration \
// ./internal/presentation/fyne -run '^TestMacOSRecoveryTimestampProbe$' -count=1 -v -timeout=4m
func TestMacOSRecoveryTimestampProbe(t *testing.T) {
	if os.Getenv(timestampProbeOptIn) != "1" {
		t.Skip("requires approved timestamp diagnostic")
	}
	if os.Getenv(timestampBindOptIn) == "1" {
		t.Fatal("bind-only opt-in forbids the posting test")
	}
	t.Setenv(timestampProbeOptIn, "")
	for _, entry := range platform.RecoveryTimestampManifest() {
		t.Log(entry)
	}
	t.Log("Backlog UNQUALIFIED: no AX-compatible actual dispatch barrier; zero attempts. No production recovery enabled.")
	runTimestampChild(t)
}

func runTimestampChild(t *testing.T) {
	t.Helper()
	child, title := timestampOwnedChild(t)
	runTimestampBaseline(t, child, title)
}

func timestampOwnedChild(t *testing.T) (*timestampChild, string) {
	t.Helper()
	var nonce [timestampNonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("timestamp setup refused phase=nonce posts=0")
	}
	title := "RasterDock-timestamp-" + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), timestampChildLimit)
	t.Cleanup(cancel)
	child, err := startTimestampChild(ctx, title)
	if err != nil {
		t.Fatal("timestamp setup refused phase=helper_start posts=0")
	}
	t.Cleanup(func() { _ = child.cmd.Process.Kill(); _ = child.cmd.Wait() })
	return child, title
}

func runTimestampBaseline(t *testing.T, child *timestampChild, title string) {
	t.Helper()
	timestampSinkReady(t, child)
	postTimestampBaseline(t, child, title)
}

func timestampSinkReady(t *testing.T, child *timestampChild) {
	t.Helper()
	var ready timestampReply
	if err := child.decoder.Decode(&ready); err != nil || !ready.OK || ready.Window == 0 {
		t.Fatal("timestamp setup refused phase=sink_ready posts=0")
	}
	child.window = ready.Window
	logTimestampReadiness(t, ready.Readiness)
	t.Logf("initial activation handoff=%s; fixed bound=2s, no AX proof", ready.InitialStatus)
	if !ready.Readiness.ReadyForBind() || ready.InitialStatus != platform.RecoveryTimestampInitialReady {
		t.Fatal("initial sink activation incomplete; no Bind, refocus or retry")
	}
}

func logTimestampReadiness(t *testing.T, r platform.RecoveryTimestampReadiness) {
	t.Helper()
	t.Logf("startup readiness: sampled=%t main_thread=%t visible=%t key_window_is_sink=%t self_key=%t app_active=%t uptime_seconds=%.9f; initial activation gate only, no AX proof",
		r.Sampled, r.MainThread, r.Visible, r.KeyWindow, r.SelfKey, r.AppActive, r.Uptime)
}

// Lead-only after review: the separate opt-in cannot authorize the posting test.
// Sanitize BEFORE TestMain; the same native sink helper is used without any IPC
// Arm request, observation polling, Post, refocus, or retry.
// env -i HOME="$HOME" PATH="$PATH" TMPDIR="$TMPDIR" CGO_ENABLED=1 \
// RASTERDOCK_RECOVERY_TIMESTAMP_BIND=1 go test -tags=integration \
// ./internal/presentation/fyne -run '^TestMacOSRecoveryTimestampBindDiagnostic$' -count=1 -v -timeout=4m
func TestMacOSRecoveryTimestampBindDiagnostic(t *testing.T) {
	if os.Getenv(timestampBindOptIn) != "1" {
		t.Skip("requires approved zero-key setup diagnostic")
	}
	if os.Getenv(timestampProbeOptIn) == "1" {
		t.Fatal("bind and posting opt-ins are mutually exclusive")
	}
	t.Setenv(timestampBindOptIn, "")
	t.Setenv(timestampProbeOptIn, "")
	child, title := timestampOwnedChild(t)
	timestampSinkReady(t, child)
	runTimestampBindOnly(t, child, title)
}

func runTimestampBindOnly(t *testing.T, child *timestampChild, title string) {
	t.Helper()
	probe, err := platform.OpenRecoveryTimestampProbe(child.cmd, child.window, title)
	if err != nil {
		var diagnostic *platform.RecoveryTimestampOpenError
		if errors.As(err, &diagnostic) {
			t.Fatalf("timestamp bind setup posts=0: %s", diagnostic)
		}
		t.Fatal("timestamp bind setup refused phase=unknown posts=0")
	}
	probe.Close()
	t.Log("timestamp bind setup succeeded phase=ready posts=0; timestamp behavior remains UNQUALIFIED")
}

func postTimestampBaseline(t *testing.T, child *timestampChild, title string) {
	t.Helper()
	// Independent watchdog kills only our direct child. It never frees a native
	// binding while AX may still be executing; all C ownership stays synchronous.
	watchdog := time.AfterFunc(timestampPostingLimit, func() { _ = child.cmd.Process.Kill() })
	defer watchdog.Stop()
	probe, err := platform.OpenRecoveryTimestampProbe(child.cmd, child.window, title)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	for sequence := uint(0); sequence < platform.RecoveryTimestampBaselinePosts/timestampPairSize; sequence++ {
		timestampPair(t, child, probe, sequence)
	}
	if _, err := child.exchange(timestampRequest{}); err != nil {
		t.Fatal(fmt.Errorf("final sink health: %w", err))
	}
}
