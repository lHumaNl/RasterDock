package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"rasterdock/locales"
)

func TestInvalidSettingsStillLaunchGUIWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(home, ".config", "rasterdock")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.yaml")
	before := "language: ru\ntimer_cycles: invalid\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	launched := false
	err := runWithGUI(func(gotRoot string, catalog *locales.Catalog, logger *slog.Logger) {
		launched = true
		if gotRoot != root || catalog == nil || logger == nil {
			t.Fatal("missing usable GUI dependencies")
		}
	})
	if err != nil || !launched {
		t.Fatal("preferences error prevented inline GUI reporting", err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // Test-owned settings file.
	if err != nil || string(b) != before {
		t.Fatal("startup overwrote malformed settings", err)
	}
}
