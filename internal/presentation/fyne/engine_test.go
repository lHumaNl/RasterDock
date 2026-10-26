package fyneui

import (
	"context"
	"errors"
	"image"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

const testTimeout = 10 * time.Second
const payloadName = "payload.txt"
const payloadText = "receiver ownership regression"

var errInjected = errors.New("injected receive failure")

func TestAnchoredReceiveRejectsSessionOutsideEngineNamespace(t *testing.T) {
	p := prepareFixture(t)
	open := anchoredReceive(t.TempDir())
	r, err := open(t.TempDir(), t.TempDir(), p.Descriptor)
	if r != nil || !errors.Is(err, storage.ErrNamespace) {
		t.Fatalf("session outside engine namespace accepted: %v", err)
	}
}

type observedReceiver struct {
	receiveOwner
	fault  *string
	closes int
}

func (r *observedReceiver) Close() error {
	r.closes++
	err := r.receiveOwner.Close()
	if *r.fault == "close" {
		return errors.Join(err, errInjected)
	}
	return err
}

func (r *observedReceiver) DurableBytes() (uint64, error) {
	if *r.fault == "bytes" {
		return 0, errInjected
	}
	return r.receiveOwner.DurableBytes()
}

func (r *observedReceiver) PublishedFiles() (uint64, uint64, error) {
	if *r.fault == "files" {
		return 0, 0, errInjected
	}
	return r.receiveOwner.PublishedFiles()
}

func (r *observedReceiver) Publish() error {
	if *r.fault == "publish" {
		return errInjected
	}
	return r.receiveOwner.Publish()
}

type engineHarness struct {
	t        *testing.T
	commands chan command
	events   chan snapshot
	ticks    chan time.Time
	steps    chan string
	done     chan struct{}
	cancel   context.CancelFunc
	prepared *application.Prepared
	owners   []*observedReceiver
	streams  []*observedCapture
	fault    string // Only the serialized engine accesses this value.
}

func prepareFixture(t *testing.T) *application.Prepared {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, payloadName), []byte(payloadText), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(t.Context(), source, t.TempDir(), 512, storage.Flush)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newHarness(t *testing.T) *engineHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h := &engineHarness{t: t, commands: make(chan command), events: make(chan snapshot, 1),
		ticks: make(chan time.Time), steps: make(chan string, 1), done: make(chan struct{}),
		cancel: cancel, prepared: prepareFixture(t)}
	deps := engineIO{ticks: h.ticks, openCapture: h.openCapture, decode: h.decode, openReceive: h.open}
	root := t.TempDir()
	go func() {
		defer close(h.done)
		runEngine(ctx, root, h.commands, h.events, slog.New(slog.NewTextHandler(io.Discard, nil)), deps)
	}()
	t.Cleanup(h.stop)
	return h
}

type observedCapture struct {
	h      *engineHarness
	closes int
}

func (h *engineHarness) openCapture(uint32) (captureStream, error) {
	stream := &observedCapture{h: h}
	h.streams = append(h.streams, stream)
	return stream, nil
}

func (s *observedCapture) Next(context.Context) (platform.Frame, error) {
	return s.h.capture(0)
}

func (s *observedCapture) Close() error {
	s.closes++
	if s.h.fault == "capture-close" {
		return errInjected
	}
	return nil
}

func TestReceivePauseClosesCaptureBeforeFirstFrame(t *testing.T) {
	h := newHarness(t)
	h.start(receiveCommand(t))
	h.send(command{kind: "pause"})
	h.expect("status.paused")
	h.stop()
	if len(h.streams) != 1 || h.streams[0].closes != 1 {
		t.Fatalf("capture stream must be opened and closed once: %v", h.streams)
	}
}

func (h *engineHarness) capture(uint32) (platform.Frame, error) {
	h.fault = <-h.steps
	if h.fault == "pending" {
		return platform.Frame{}, platform.ErrFramePending
	}
	if h.fault == "capture" {
		return platform.Frame{}, errInjected
	}
	return platform.Frame{}, nil
}

func (h *engineHarness) decode(image.Image) (v1.Beacon, []v1.Packet, error) {
	if h.fault == "decode" {
		return v1.Beacon{}, nil, errInjected
	}
	var packets []v1.Packet
	for n := uint64(0); n < h.prepared.PacketCount(); n++ {
		p, err := h.prepared.Packet(n)
		if err != nil {
			return v1.Beacon{}, nil, err
		}
		packets = append(packets, p)
	}
	if h.fault == "foreign-first" || h.fault == "foreign-last" {
		return decodeMixedTransfer(h.prepared.Descriptor, packets[0], h.fault == "foreign-first")
	}
	if h.fault == "invalid-same-transfer" {
		packets[0].Size++
	}
	return v1.Beacon{Descriptor: h.prepared.Descriptor}, packets, nil
}

func (h *engineHarness) open(session, destination string, d v1.Descriptor) (receiveOwner, error) {
	r, err := anchoredReceive(session)(session, destination, d)
	if err != nil {
		return nil, err
	}
	owner := &observedReceiver{receiveOwner: r, fault: &h.fault}
	h.owners = append(h.owners, owner)
	return owner, nil
}

func (h *engineHarness) stop() {
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(testTimeout):
		h.t.Fatal("engine did not stop")
	}
}

func (h *engineHarness) send(cmd command) {
	h.t.Helper()
	select {
	case h.commands <- cmd:
	case <-time.After(testTimeout):
		h.t.Fatal("engine did not accept command")
	}
}

func (h *engineHarness) expect(key string) snapshot {
	h.t.Helper()
	select {
	case event := <-h.events:
		if event.key != key {
			h.t.Fatalf("status = %q, want %q: %v", event.key, key, event.params)
		}
		return event
	case <-time.After(testTimeout):
		h.t.Fatalf("missing status %q", key)
		return snapshot{}
	}
}

func (h *engineHarness) tick(fault string) {
	h.t.Helper()
	h.steps <- fault
	select {
	case h.ticks <- time.Time{}:
	case <-time.After(testTimeout):
		h.t.Fatal("engine did not accept tick")
	}
}

func receiveCommand(t *testing.T) command {
	t.Helper()
	return command{kind: "receive", window: 1, session: t.TempDir(), destination: t.TempDir()}
}

func (h *engineHarness) start(cmd command) {
	h.send(cmd)
	h.expect("status.waiting")
}

func (h *engineHarness) assertReleased(cmd command) {
	h.t.Helper()
	if h.owners[0].closes != 1 {
		h.t.Errorf("old receiver closed %d times, want 1", h.owners[0].closes)
	}
	if len(h.streams) == 0 || h.streams[0].closes != 1 {
		h.t.Error("old capture stream was not closed exactly once")
	}
	r, err := anchoredReceive(cmd.session)(cmd.session, cmd.destination, h.prepared.Descriptor)
	if err != nil {
		h.t.Errorf("old session lock not released: %v", err)
		return
	}
	if err := r.Close(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *engineHarness) assertDestination(cmd command) {
	h.t.Helper()
	event := h.expect("status.receiving")
	if event.session != cmd.session {
		h.t.Errorf("session = %q, want %q", event.session, cmd.session)
	}
	data, err := os.ReadFile(filepath.Join(cmd.destination, payloadName))
	if err != nil || string(data) != payloadText {
		h.t.Errorf("destination payload = %q, error = %v", data, err)
	}
}

func TestReceiveFailureReleasesOwnerBeforeRestart(t *testing.T) {
	for _, fault := range []string{"capture", "bytes", "files", "publish"} {
		t.Run(fault, func(t *testing.T) { testReceiveRestart(t, fault) })
	}
}

func testReceiveRestart(t *testing.T, fault string) {
	h := newHarness(t)
	a, b := receiveCommand(t), receiveCommand(t)
	h.start(a)
	h.tick("")
	h.assertDestination(a)
	h.tick(fault)
	h.expect("status.error")
	h.start(b) // Synchronizes with completion of the preceding terminal transition.
	h.assertReleased(a)
	h.tick("")
	h.assertDestination(b)
	h.stop()
	if len(h.owners) != 2 || h.owners[len(h.owners)-1].closes != 1 {
		t.Error("restart must open a fresh receiver and shutdown must close it once")
	}
}

func TestReceivePauseAndShutdownReleaseOwner(t *testing.T) {
	for _, transition := range []string{"pause", "shutdown", "close"} {
		t.Run(transition, func(t *testing.T) { testReceiveStop(t, transition) })
	}
}

func TestReceiveDecodeMissKeepsCurrentOwner(t *testing.T) {
	h := newHarness(t)
	a := receiveCommand(t)
	h.start(a)
	h.tick("")
	h.assertDestination(a)
	h.tick("decode")
	h.send(receiveCommand(t))
	h.expect("status.busy")
	h.tick("")
	h.assertDestination(a)
	h.stop()
	if len(h.owners) != 1 || h.owners[0].closes != 1 {
		t.Error("decode miss or busy command changed receiver ownership")
	}
}

func TestReceivePendingCaptureKeepsOwnerUntilPause(t *testing.T) {
	h := newHarness(t)
	a := receiveCommand(t)
	h.start(a)
	h.tick("")
	h.assertDestination(a)
	h.tick("pending")
	h.send(receiveCommand(t))
	h.expect("status.busy")
	h.tick("")
	h.assertDestination(a)
	h.send(command{kind: "pause"})
	h.expect("status.paused")
	h.stop()
	h.assertReleased(a)
	if len(h.owners) != 1 || len(h.streams) != 1 {
		t.Fatal("pending frame reopened the capture or receiver")
	}
}

func TestReceiveCaptureCloseFailureStillReleasesStorage(t *testing.T) {
	h := newHarness(t)
	a := receiveCommand(t)
	h.start(a)
	h.tick("capture-close")
	h.assertDestination(a)
	h.send(command{kind: "pause"})
	h.expect("status.error")
	h.stop()
	h.assertReleased(a)
}

func TestReceiveInvalidDestinationDoesNotOpenCapture(t *testing.T) {
	h := newHarness(t)
	cmd := receiveCommand(t)
	cmd.destination = filepath.Join(cmd.destination, "absent")
	h.send(cmd)
	h.expect("status.error")
	h.stop()
	if len(h.streams) != 0 {
		t.Fatal("capture started before validating destination")
	}
}

type cancellationCapture struct {
	entered chan struct{}
	closed  chan struct{}
}

func (s *cancellationCapture) Next(ctx context.Context) (platform.Frame, error) {
	close(s.entered)
	<-ctx.Done()
	return platform.Frame{}, ctx.Err()
}

func (s *cancellationCapture) Close() error {
	close(s.closed)
	return nil
}

func TestEngineShutdownCancelsPendingCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	commands, events := make(chan command, 1), make(chan snapshot, 1)
	ticks, done := make(chan time.Time, 1), make(chan struct{})
	stream := &cancellationCapture{entered: make(chan struct{}), closed: make(chan struct{})}
	root := t.TempDir()
	go func() {
		defer close(done)
		runEngine(ctx, root, commands, events, slog.New(slog.NewTextHandler(io.Discard, nil)), engineIO{
			ticks: ticks, openCapture: func(uint32) (captureStream, error) { return stream, nil },
		})
	}()
	commands <- receiveCommand(t)
	select {
	case event := <-events:
		if event.key != "status.waiting" {
			t.Fatalf("unexpected initial status: %+v", event)
		}
	case <-time.After(testTimeout):
		t.Fatal("receive did not start")
	}
	ticks <- time.Now()
	select {
	case <-stream.entered:
	case <-time.After(testTimeout):
		t.Fatal("capture did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("shutdown blocked in capture")
	}
	select {
	case <-stream.closed:
	default:
		t.Fatal("shutdown did not close capture stream")
	}
}

func testReceiveStop(t *testing.T, transition string) {
	h := newHarness(t)
	a := receiveCommand(t)
	h.start(a)
	h.tick(transition)
	h.assertDestination(a)
	if transition != "shutdown" {
		h.send(command{kind: "pause"})
		key := "status.paused"
		if transition == "close" {
			key = "status.error"
		}
		h.expect(key)
	}
	h.stop()
	h.assertReleased(a)
}
