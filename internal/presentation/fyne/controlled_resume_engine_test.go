package fyneui

import (
	"context"
	"image"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	v1 "rasterdock/internal/protocol/v1"
)

func TestResumeEngineUsesPinnedSessionNotEditedForm(t *testing.T) {
	s := &controlledScene{p: prepareFixture(t)}
	root, destination, editedDestination := t.TempDir(), t.TempDir(), t.TempDir()
	deps := sceneDependencies(s, root)
	binds := 0
	deps.bindInput = func(id uint32) (platform.InputBinding, error) {
		if id != 1 {
			t.Error("resume retargeted edited window")
		}
		binds++
		if binds == 1 {
			return pausedInput{sceneInput{s}, platform.ErrInputFocusLost}, nil
		}
		return sceneInput{s}, nil
	}
	deps.controlDecode = func(img image.Image) (v1.Beacon, []v1.Packet, error) {
		b, packets, err := codec.DecodeControlled(img)
		if len(packets) > 1 {
			if binds == 1 {
				packets = packets[:1]
			} else {
				packets = packets[1:]
			}
		}
		return b, packets, err
	}
	ctx, cancel := context.WithCancel(t.Context())
	commands, events := make(chan command, 1), make(chan snapshot, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runEngine(ctx, root, commands, events, slog.New(slog.NewTextHandler(io.Discard, nil)), deps)
	}()
	t.Cleanup(func() { cancel(); <-done })
	wait := func(key string) snapshot {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case v := <-events:
				if v.key == "status.error" {
					t.Fatalf("engine error: %v", v.params)
				}
				if v.key == key {
					return v
				}
			case <-timer.C:
				t.Fatal("missing state", key)
			}
		}
	}
	commands <- command{kind: "control_receive", window: 1, destination: destination}
	paused := wait("control.paused")
	if !paused.canResume || paused.session == "" {
		t.Fatal("missing resumable durable state")
	}
	commands <- command{kind: "control_resume", window: 999, destination: editedDestination, session: "not-the-session"}
	complete := wait("control.complete")
	if complete.session != paused.session {
		t.Fatal("resume created another session")
	}
	cancel()
	<-done
	if binds != 2 || s.finishes != 1 || s.closes != 2 || s.inputClose != 2 {
		t.Fatal("unexpected lifecycle", binds, s.finishes, s.closes, s.inputClose)
	}
	data, err := os.ReadFile(filepath.Join(destination, payloadName)) //nolint:gosec // Test-owned fixed fixture path.
	if err != nil || string(data) != payloadText {
		t.Fatal("durable resume output", err)
	}
	entries, err := os.ReadDir(editedDestination)
	if err != nil || len(entries) != 0 {
		t.Fatal("resume used edited destination", err)
	}
}
