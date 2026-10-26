package fyneui

import (
	"errors"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
	"rasterdock/locales"
)

func TestCalibrationGuardOnlySurfacesPersistentFailure(t *testing.T) {
	var guard calibrationGuard
	sequence := []error{codec.ErrCalibration, codec.ErrCalibration, nil, codec.ErrCalibration, codec.ErrCalibration, codec.ErrCalibration, errors.New("QR missing"), application.ErrProfile}
	want := []bool{false, false, false, false, false, true, false, true}
	for i, err := range sequence {
		if guard.observe(err) != want[i] {
			t.Fatalf("calibration observation %d", i)
		}
	}
}

func TestProfileSelectorDefaultAndOptions(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	var selected application.ColorProfile
	s := profileSelect(c, func(p application.ColorProfile) { selected = p })
	if len(s.Options) != 3 || s.Selected != c.Text("profile.monochrome") || selected != 0 {
		t.Fatal("default/options")
	}
	s.SetSelected(s.Options[1])
	if selected != application.Colors4 {
		t.Fatal("selection not explicit")
	}
	reflectProfile(s, application.Monochrome)
	if selected != application.Colors4 {
		t.Fatal("acknowledgement triggered switch")
	}
}

func TestEightProfileSelectorAndAcknowledgement(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	var selected application.ColorProfile
	s := profileSelect(c, func(p application.ColorProfile) { selected = p })
	s.SetSelected(s.Options[2])
	if selected != application.Colors8 || s.Selected != c.Text("profile.eight") {
		t.Fatal("eight-color selection not explicit")
	}
	selected = 0
	reflectProfile(s, application.Colors8)
	if s.Selected != s.Options[2] || selected != 0 {
		t.Fatal("eight-color acknowledgement")
	}
}

func TestEngineProfileSwitchKeepsPreparedIdentity(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	first := h.expect("status.frame")
	a, _, err := codec.Decode(first.picture)
	if err != nil || a.Colors != 2 {
		t.Fatal(err)
	}
	h.send(command{kind: "profile", profile: application.Colors4})
	second := h.expect("status.frame")
	b, p, err := codec.Decode(second.picture)
	if err != nil || b.Colors != 4 || b.Descriptor != a.Descriptor || len(p) != 2 || second.prepared != first.prepared {
		t.Fatalf("identity/profile mismatch: %v", err)
	}
}

func TestEngineRejectsSixteenColorsWithoutChangingProfile(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "profile", profile: application.Colors4})
	h.expect("status.frame")
	h.send(command{kind: "profile", profile: application.ColorProfile(16)})
	h.expect("status.error")
	h.send(command{kind: "next"})
	if s := h.expect("status.frame"); s.profile != application.Colors4 {
		t.Fatal("unsupported profile changed state")
	}
}

func TestEngineProfileSwitchPausesTimer(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "auto"})
	h.send(command{kind: "profile", profile: application.Colors4})
	h.expect("status.frame")
	// Manual navigation would return busy if the timer were still running.
	h.send(command{kind: "next"})
	h.expect("status.frame")
	h.send(command{kind: "profile", profile: application.Monochrome})
	if s := h.expect("status.frame"); s.profile != application.Monochrome {
		t.Fatal("cannot switch back")
	}
}

func TestEngineEightProfileIdentityAndPause(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	first := h.expect("status.frame")
	for _, profile := range []application.ColorProfile{application.Colors8, application.Colors4, application.Monochrome} {
		h.send(command{kind: "auto"})
		h.send(command{kind: "profile", profile: profile})
		s := h.expect("status.frame")
		b, packets, err := codec.Decode(s.picture)
		if err != nil || b.Colors != byte(profile) || b.Descriptor != h.prepared.Descriptor || s.prepared != first.prepared || uint64(len(packets)) != profile.Capacity() {
			t.Fatalf("profile %d changed prepared identity: %v", profile, err)
		}
		h.send(command{kind: "next"}) // Busy would indicate a live timer.
		h.expect("status.frame")
	}
}
