//go:build integration && windows

package fyneui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/internal/adapters/codec"
)

// Optional diagnostics capture only the owned sender selected by its exact
// PID-bearing title, never a desktop screenshot or another application's pixels.
func recordOwnedGUIFrame(t *testing.T, title string) {
	t.Helper()
	dir := os.Getenv("RASTERDOCK_TEST_ARTIFACTS")
	if dir == "" {
		return
	}
	id := selectOwnedNativeSource(t, title)
	frame, err := nativeCaptureFrame(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	b, packets, decodeErr := codec.Decode(frame.Image)
	t.Logf("owned GUI frame=%v beacon=%dx%d packets=%d decode=%v", frame.Image.Bounds(), b.Width, b.Height, len(packets), decodeErr)
	if err := os.MkdirAll(dir, 0700); err != nil { //nolint:gosec // Explicit opt-in developer artifact directory, never a path from transfer input.
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "owned-gui-"+t.Name()+".png")) //nolint:gosec // Explicit developer artifact directory and test-owned sender pixels only.
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := png.Encode(f, frame.Image)
	closeErr := f.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatalf("write owned GUI diagnostic: encode=%v close=%v", encodeErr, closeErr)
	}
}
