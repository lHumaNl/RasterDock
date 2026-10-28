package fyneui

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func largeProfile() application.VisualProfile {
	return application.VisualProfile{Colors: application.Colors8, Geometry: application.Geometry{Module: 8, Columns: 3, Rows: 2}}
}

func TestGeometryEngineFitRollbackPending(t *testing.T) {
	h := newHarness(t)
	h.viewport.store(image.Pt(664, 460), image.Point{})
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.send(command{kind: "profile", visual: largeProfile()})
	h.send(command{kind: "barrier"})
	s := h.expect("profile.fit")
	if s.visual != application.DefaultVisualProfile(application.Monochrome) || s.picture == nil {
		t.Fatal("rejected profile lost previous acknowledgement/pending image")
	}
	if s.params["width"] != 2764 || s.params["availableWidth"] != 664 {
		t.Fatal(s.params)
	}
	b, _, err := codec.Decode(s.picture)
	if err != nil || b.Module != 4 || b.Columns != 1 {
		t.Fatal(b, err)
	}
}

func TestGeometryEngineFailedReadRollback(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.send(command{kind: "barrier"})
	manifest := filepath.Join(h.prepared.Directory, "manifest.zst")
	if err := os.Rename(manifest, manifest+".unavailable"); err != nil {
		t.Fatal(err)
	}
	h.send(command{kind: "profile", visual: largeProfile()})
	h.send(command{kind: "barrier"})
	s := h.expect("status.error")
	if s.visual != application.DefaultVisualProfile(application.Monochrome) || s.picture == nil {
		t.Fatal("failed read committed geometry or discarded pending pixels")
	}
}

func TestGeometryEngineCoalescingIdentity(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "profile", visual: largeProfile()})
	h.send(command{kind: "pause"})
	h.send(command{kind: "barrier"})
	s := h.expect("status.paused")
	b, packets, err := codec.Decode(s.picture)
	if err != nil || s.visual != largeProfile() || b.Module != 8 || b.Columns != 3 || b.Rows != 2 || len(packets) != 18 || b.Descriptor != h.prepared.Descriptor {
		t.Fatal("full profile acknowledgement mismatch", err)
	}
}

func TestGeometryEngineResizePausesUntilExplicitResume(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "auto"})
	h.send(command{kind: "barrier"})
	h.expect("status.busy")
	h.viewport.store(image.Pt(600, 400), image.Pt(664, 460))
	h.ticks <- time.Now()
	h.send(command{kind: "barrier"})
	s := h.expect("profile.fit")
	if s.picture == nil || s.picture.Bounds().Size() != image.Pt(1, 1) {
		t.Fatal("cropped data retained")
	}
	h.send(command{kind: "auto"})
	h.expect("profile.fit")
	h.viewport.store(image.Pt(664, 460), image.Pt(1, 1))
	h.ticks <- time.Now()
	h.send(command{kind: "barrier"})
	select {
	case s := <-h.events:
		t.Fatal("restoration resumed automatically", s.key)
	default:
	}
	h.send(command{kind: "auto"})
	h.ticks <- time.Now()
	h.expect("status.timer_frame")
}

func TestGeometryEngineTransientResizeStillPauses(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "auto"})
	h.send(command{kind: "barrier"})
	h.expect("status.busy")
	h.viewport.store(image.Pt(100, 100), image.Pt(664, 460))
	h.viewport.store(image.Pt(1000, 1000), image.Pt(664, 460))
	h.ticks <- time.Now()
	h.send(command{kind: "barrier"})
	if s := h.expect("status.paused"); s.picture == nil || s.picture.Bounds().Dx() != 1 {
		t.Fatal("transient crop was ignored")
	}
	h.send(command{kind: "next"})
	h.expect("status.frame")
}

func TestGeometryUnknownViewportNeverFits(t *testing.T) {
	if checkFit(application.DefaultVisualProfile(application.Monochrome), nil) == nil {
		t.Fatal("unknown viewport claimed fit")
	}
}

func TestGeometryExplicitRecoveryBeforeTick(t *testing.T) {
	for _, kind := range []string{"next", "profile"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.send(command{kind: "open", source: h.prepared.Directory})
			h.expect("status.frame")
			h.viewport.store(image.Pt(100, 100), image.Pt(664, 460))
			h.viewport.store(image.Pt(3000, 2000), image.Pt(664, 460))
			h.send(command{kind: kind, visual: largeProfile()})
			h.expect("status.frame")
			h.ticks <- time.Now()
			h.send(command{kind: "barrier"})
			select {
			case s := <-h.events:
				t.Fatal("successful recovery blanked", s.key)
			default:
			}
			h.viewport.store(image.Pt(100, 100), image.Pt(664, 460))
			h.viewport.store(image.Pt(3000, 2000), image.Pt(664, 460))
			h.ticks <- time.Now()
			h.expect("status.paused")
		})
	}
}

func TestGeometryRevisionsSurviveFailureLegacyAndCoalescing(t *testing.T) {
	h := newHarness(t)
	h.send(command{kind: "open", source: h.prepared.Directory})
	h.expect("status.frame")
	h.send(command{kind: "profile", visual: largeProfile(), revision: 1})
	h.send(command{kind: "profile", visual: application.VisualProfile{Colors: 3}, revision: 2})
	h.send(command{kind: "barrier"})
	s := h.expect("status.error")
	if s.revision != 2 || s.visual != largeProfile() || s.picture == nil {
		t.Fatal("failed revision lost acknowledgement", s)
	}
	h.send(command{kind: "profile", profile: application.Colors4})
	h.send(command{kind: "pause"})
	h.send(command{kind: "barrier"})
	s = h.expect("status.paused")
	want := largeProfile()
	want.Colors = application.Colors4
	if s.revision != 2 || s.visual != want || s.picture == nil {
		t.Fatal("legacy command/coalescing lost state", s)
	}
}
