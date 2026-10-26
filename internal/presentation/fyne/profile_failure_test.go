package fyneui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func TestFailedProfileSwitchPreservesAcknowledgedImage(t *testing.T) {
	for _, profile := range []application.ColorProfile{application.Colors4, application.Colors8} {
		for _, pending := range []bool{false, true} {
			for _, pause := range []bool{false, true} {
				t.Run(fmt.Sprintf("colors%d/pending%t/pause%t", profile, pending, pause), func(t *testing.T) {
					h := newHarness(t)
					h.send(command{kind: "open", source: h.prepared.Directory})
					h.send(command{kind: "barrier"})
					if !pending {
						h.expect("status.frame")
					}
					manifest := filepath.Join(h.prepared.Directory, "manifest.zst")
					if err := os.Rename(manifest, manifest+".unavailable"); err != nil {
						t.Fatal(err)
					}
					h.send(command{kind: "profile", profile: profile})
					h.send(command{kind: "barrier"})
					key := "status.error"
					if pause {
						h.send(command{kind: "pause"})
						h.send(command{kind: "barrier"})
						key = "status.paused"
					}
					s := h.expect(key)
					if s.profile != application.Monochrome {
						t.Fatal("failed switch acknowledged new profile")
					}
					if pending {
						if s.picture == nil {
							t.Fatal("failed switch dropped pending image")
						}
						b, _, err := codec.Decode(s.picture)
						if err != nil || b.Colors != byte(s.profile) {
							t.Fatalf("image/profile mismatch: %+v, %v", b, err)
						}
					} else if s.picture != nil {
						t.Fatal("failed switch replaced consumed image")
					}
				})
			}
		}
	}
}
