package fyneui

import (
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func TestProfilePausePreservesUnconsumedPicture(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "profile", profile: application.Colors4})
	h.send(command{kind: "pause"})
	// An unknown command is a barrier: pause has emitted, but nothing is drained.
	h.send(command{kind: "barrier"})
	last := h.expect("status.paused")
	if last.picture == nil {
		t.Fatal("pause dropped the unconsumed profile image")
	}
	b, _, err := codec.Decode(last.picture)
	if err != nil || b.Colors != byte(last.profile) || last.profile != application.Colors4 {
		t.Fatalf("display/profile mismatch: colors=%d profile=%d error=%v", b.Colors, last.profile, err)
	}
	h.send(command{kind: "pause"})
	assertConsumedProfilePicture(t, h.expect("status.paused").picture == nil)
}

func assertConsumedProfilePicture(t *testing.T, consumed bool) {
	t.Helper()
	if !consumed {
		t.Fatal("consumed picture retransmitted")
	}
}

func TestReceiverTransitionClearsPendingSenderPicture(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.send(receiveCommand(t))
	h.send(command{kind: "pause"})
	h.send(command{kind: "barrier"})
	s := h.expect("status.paused")
	if s.role != "receiver" || s.picture == nil || s.picture.Bounds().Dx() != 1 {
		t.Fatal("receiver retained pending sender image")
	}
}

func TestProfileResetDoesNotResurrectPendingPicture(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.send(command{kind: "open", source: t.TempDir()}) // Failed open clears prepared state.
	h.send(command{kind: "profile", profile: application.Colors4})
	h.send(command{kind: "barrier"})
	s := h.expect("status.idle")
	if s.profile != application.Colors4 || s.picture == nil || s.picture.Bounds().Dx() != 1 {
		t.Fatal("profile without prepared state retained old image")
	}
}

func TestEightProfileCoalescingKeepsLatestPicture(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "profile", profile: application.Colors4})
	h.send(command{kind: "profile", profile: application.Colors8})
	h.send(command{kind: "pause"})
	h.send(command{kind: "barrier"})
	s := h.expect("status.paused")
	if s.picture == nil || s.profile != application.Colors8 {
		t.Fatal("coalescing dropped latest eight-color picture")
	}
	b, packets, err := codec.Decode(s.picture)
	if err != nil || b.Colors != 8 || len(packets) != 3 {
		t.Fatalf("eight-color picture/profile mismatch: %v", err)
	}
	h.send(command{kind: "pause"})
	assertConsumedProfilePicture(t, h.expect("status.paused").picture == nil)
}
