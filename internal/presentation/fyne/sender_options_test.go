package fyneui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"rasterdock/internal/adapters/config"
	"rasterdock/locales"
)

func TestSenderOptionsDefaultsAndValidation(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	o := newSenderOptions(c)
	if n, err := o.cycleLimit(); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if d, err := o.timeout(); err != nil || d != 0 {
		t.Fatal(d, err)
	}
	o.cycles.SetText("2")
	o.idle.SetText("60")
	if n, err := o.cycleLimit(); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if d, err := o.timeout(); err != nil || d != time.Minute {
		t.Fatal(d, err)
	}
	for _, value := range []string{"", "0", "-1", "abc", "1.5", "1000001"} {
		o.cycles.SetText(value)
		if _, err := o.cycleLimit(); err == nil {
			t.Fatal("invalid cycles", value)
		}
	}
	for _, value := range []string{"", "-1", "abc", "1.5", "86401"} {
		o.idle.SetText(value)
		if _, err := o.timeout(); err == nil {
			t.Fatal("invalid timeout", value)
		}
	}
}

func TestSenderOptionsPersistWithoutStartingMode(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	o := newPersistentSenderOptions(c, path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("initialization wrote defaults", err)
	}
	o.cycles.SetText("12")
	o.idle.SetText("45")
	// Reconstruct exactly the same controls used on application launch.
	reopened := newPersistentSenderOptions(c, path)
	if reopened.cycles.Text != "12" || reopened.idle.Text != "45" {
		t.Fatal("lost edits after reopening")
	}
	o.cycles.SetText("")
	o.idle.SetText("60") // The other valid field still saves while cycles is incomplete.
	o.idle.SetText("86401")
	s, err := config.LoadSettings(path)
	if err != nil || s.TimerCycles != 12 || s.IdleTimeoutSeconds != 60 {
		t.Fatal(s, err)
	}
	if !o.persistenceError.Hidden {
		t.Fatal("successful save shows an error")
	}
}

func TestSenderOptionsShowPersistenceFailureAndRecover(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	o := newPersistentSenderOptions(c, path)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	o.cycles.SetText("9")
	if o.persistenceError.Hidden || o.persistenceError.Text == "" {
		t.Fatal("save failure was hidden")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	o.idle.SetText("30")
	s, err := config.LoadSettings(path)
	if err != nil || s.TimerCycles != 9 || s.IdleTimeoutSeconds != 30 {
		t.Fatal("failed edit not retried", s, err)
	}
	if !o.persistenceError.Hidden {
		t.Fatal("error not cleared after successful retry")
	}
}

func TestSenderOptionsNeverOverwriteMalformedFile(t *testing.T) {
	c, err := locales.Load("en")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	text := "language: ru\nunknown: value\n"
	if err = os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	o := newPersistentSenderOptions(c, path)
	if o.persistenceError.Hidden {
		t.Fatal("load failure was hidden")
	}
	o.cycles.SetText("4")
	b, err := os.ReadFile(path) //nolint:gosec // Test-owned preferences path.
	if err != nil || string(b) != text || o.persistenceError.Hidden {
		t.Fatal("malformed preferences overwritten", err)
	}
}
