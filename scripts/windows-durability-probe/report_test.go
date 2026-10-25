package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDiagnosticFailureDoesNotFailReport(t *testing.T) {
	r := newReport()
	r.record("flush", "FlushFileBuffers", "GENERIC_READ", "0", fmt.Errorf("flush: %w", syscall.Errno(5)), false)
	if r.Failed || r.Operations[0].Success || *r.Operations[0].ErrorCode != 5 {
		t.Fatalf("unexpected classification: %+v", r)
	}
	r.record("setup", "os.WriteFile", "", "", errors.New("setup failed"), true)
	if !r.Failed {
		t.Fatal("setup failure must fail report")
	}
}

func TestReportIncludesCaveat(t *testing.T) {
	var out bytes.Buffer
	if err := writeReport(&out, newReport()); err != nil {
		t.Fatal(err)
	}
	for _, caveat := range []string{"not acceptance proof", "not production storage acceptance or Receiver recovery evidence", "Process kill is not OS crash, reboot, VM reset, or power-loss evidence"} {
		if !strings.Contains(out.String(), caveat) {
			t.Fatalf("missing evidence limitation %q", caveat)
		}
	}
}

func TestVerifyNoReplace(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	writeFixture(t, source, sourceData)
	writeFixture(t, target, targetData)
	if err := verifyNoReplace(source, target); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, target, sourceData)
	if err := verifyNoReplace(source, target); err == nil {
		t.Fatal("overwritten target went undetected")
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), fixtureMode); err != nil {
		t.Fatal(err)
	}
}

func TestInvariantFailures(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := verifyNoReplace(source, target); err == nil {
		t.Fatal("missing files went undetected")
	}
	writeFixture(t, source, sourceData)
	writeFixture(t, target, sourceData)
	if err := verifyMove(source, target, true); err == nil {
		t.Fatal("surviving source went undetected")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestReportWriteFailure(t *testing.T) {
	if err := writeReport(failingWriter{}, newReport()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost output error: %v", err)
	}
}

func TestVerifyMove(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte(sourceData), fixtureMode); err != nil {
		t.Fatal(err)
	}
	if err := verifyMove(source, target, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	if err := verifyMove(source, target, true); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryMoveRejectsLeftoverRoot(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			root := t.TempDir()
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			present, absent := source, target
			if moved {
				present, absent = target, source
			}
			if err := os.MkdirAll(filepath.Join(present, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(present, "child", "marker"), []byte(sourceData), fixtureMode); err != nil {
				t.Fatal(err)
			}
			if err := verifyDirectoryMove(source, target, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(absent, 0700); err != nil {
				t.Fatal(err)
			}
			if err := verifyDirectoryMove(source, target, moved); err == nil {
				t.Fatal("empty leftover directory incorrectly accepted")
			}
		})
	}
}
