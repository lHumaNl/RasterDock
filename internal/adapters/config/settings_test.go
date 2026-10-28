package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSenderSettingsDefaultsAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "settings.yaml")
	s, err := LoadSettings(path)
	if err != nil || s.Language != "system" || s.TimerCycles != 3 || s.IdleTimeoutSeconds != 0 {
		t.Fatal(s, err)
	}
	if err = SaveSenderSettings(path, 7, 60); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSettings(path)
	if err != nil || s.TimerCycles != 7 || s.IdleTimeoutSeconds != 60 || s.Language != "system" {
		t.Fatal(s, err)
	}
	if err = SaveSenderSettings(path, 1, 0); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSettings(path)
	if err != nil || s.TimerCycles != 1 || s.IdleTimeoutSeconds != 0 {
		t.Fatal(s, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary file leak", entries, err)
	}
}

func TestSenderSettingsPreserveLanguageAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	before := "# My preferences\nlanguage: 'ru' # Keep Russian\n# Cycle count\ntimer_cycles: 5 # passes\nidle_timeout_seconds: 20 # wait\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSenderSettings(path, MaxTimerCycles, MaxIdleTimeoutSeconds); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(path)
	if err != nil || s.Language != "ru" || s.TimerCycles != MaxTimerCycles || s.IdleTimeoutSeconds != MaxIdleTimeoutSeconds {
		t.Fatal(s, err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // Test-owned preferences path.
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# My preferences", "language: 'ru'", "# Keep Russian", "# Cycle count", "# passes", "# wait"} {
		if !strings.Contains(string(b), text) {
			t.Fatal("lost user content", text, string(b))
		}
	}
}

func TestOldSettingsKeepSenderDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	for _, text := range []string{"language: en\n", "{}\n"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := LoadSettings(path)
		if err != nil || s.TimerCycles != 3 || s.IdleTimeoutSeconds != 0 {
			t.Fatal(s, err)
		}
	}
}

func TestInvalidSettingsNeverOverwritten(t *testing.T) {
	for _, text := range []string{
		"", "# empty\n", "null", "[]", "foo", "language: xx", "language: 42", "language: null",
		"timer_cycles: 0", "timer_cycles: -1", "timer_cycles: 1000001", "timer_cycles: 1.5",
		"timer_cycles: '3'", "timer_cycles: null", "timer_cycles: true", "timer_cycles: 18446744073709551616",
		"idle_timeout_seconds: -1", "idle_timeout_seconds: 86401", "idle_timeout_seconds: null",
		"unknown: 3", "timer_cycles: 3\ntimer_cycles: 4", "language: ru\nlanguage: en",
		"language: &a ru", "language: *a", "&root {language: ru}", "timer_cycles: &n 3",
		"language: ru\n---\nlanguage: en", "[", "language: ru\n<<: {timer_cycles: 3}",
		strings.Repeat("#", (1<<20)+1),
	} {
		t.Run(text[:min(len(text), 50)], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSettings(path); err == nil {
				t.Fatal("accepted invalid settings")
			}
			if err := SaveSenderSettings(path, 4, 10); err == nil {
				t.Fatal("overwrote invalid settings")
			}
			b, err := os.ReadFile(path) //nolint:gosec // Test-owned preferences path.
			if err != nil || string(b) != text {
				t.Fatal("changed invalid file", err)
			}
		})
	}
}

func TestSaveSenderSettingsRejectsInvalidValuesAndSpecialPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	for _, pair := range [][2]uint64{{0, 0}, {1000001, 0}, {3, 86401}} {
		if err := SaveSenderSettings(path, pair[0], pair[1]); err == nil {
			t.Fatal("accepted invalid values", pair)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid save created file", err)
	}
	if err := SaveSenderSettings(t.TempDir(), 3, 0); err == nil {
		t.Fatal("accepted directory")
	}
	target := filepath.Join(t.TempDir(), "original.yaml")
	if err := os.WriteFile(target, []byte("language: en\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks unavailable", err)
	}
	if err := SaveSenderSettings(path, 3, 0); err == nil {
		t.Fatal("accepted symlink")
	}
	b, err := os.ReadFile(target) //nolint:gosec // Test-owned symlink target.
	if err != nil || string(b) != "language: en\n" {
		t.Fatal("changed symlink target", err)
	}
}
