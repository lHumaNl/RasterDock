package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.yaml")
	s, err := LoadSettings(p)
	if err != nil || s.Language != "system" {
		t.Fatal(s, err)
	}
	if err = os.WriteFile(p, []byte("language: ru\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSettings(p)
	if err != nil || s.Language != "ru" {
		t.Fatal(s, err)
	}
	if err = os.WriteFile(p, []byte("language: invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSettings(p); err == nil {
		t.Fatal("unsupported explicit language")
	}
}

func TestConfig(t *testing.T) {
	c, err := ParseJob([]byte("source: ./input\n"), "/work/jobs/job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != filepath.FromSlash("/work/jobs/input") {
		t.Fatal(c.Source)
	}
	for _, s := range []string{"source: a\nunknown: 1", "source: a\nsource: b", "source: 42", "source: &a foo\n", "source: a\n---\nsource: b"} {
		if _, err := ParseJob([]byte(s), "/job.yaml"); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
