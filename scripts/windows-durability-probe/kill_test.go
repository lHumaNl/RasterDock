package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKillHelper(_ *testing.T) {
	mode := os.Getenv("PROBE_TEST_CHILD")
	if mode == "" {
		return
	}
	switch mode {
	case "ready":
		_, _ = fmt.Fprintln(os.Stdout, "after-file-flush")
	case "checkpoint-exit-0":
		_, _ = fmt.Fprintln(os.Stdout, "after-file-flush")
		os.Exit(0)
	case "checkpoint-exit-2":
		_, _ = fmt.Fprintln(os.Stdout, "after-file-flush")
		os.Exit(2)
	case "wrong":
		_, _ = fmt.Fprintln(os.Stdout, "after-publication")
	case "oversize":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 8192))
	case "partial":
		_, _ = fmt.Fprint(os.Stdout, "after-file-flush")
		os.Exit(0)
	case "exit":
		os.Exit(0)
	case "failed":
		os.Exit(9)
	case "silent":
	}
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	os.Exit(0)
}

// Wait for termination BEFORE inspecting its status: no scheduler-dependent race
// with Kill. Even a valid checkpoint cannot certify an ordinary child exit.
func TestCheckpointFollowedByChildExitIsRejected(t *testing.T) {
	for _, mode := range []string{"checkpoint-exit-0", "checkpoint-exit-2"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKillHelper$") //nolint:gosec // Reexec only the test executable and fixed helper.
			cmd.Env = append(os.Environ(), "PROBE_TEST_CHILD="+mode)
			output, waitErr := cmd.Output()
			if ctx.Err() != nil || string(output) != "after-file-flush\n" || cmd.ProcessState == nil {
				t.Fatalf("fixture did not exit after checkpoint: output=%q error=%v", output, waitErr)
			}
			if verifyKilledExit(waitErr) == nil {
				t.Fatal("checkpoint followed by child-controlled exit accepted as hard kill")
			}
		})
	}
}

func TestValidateChildDirectory(t *testing.T) {
	root, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	dir := filepath.Join(root, "after-file-flush")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateChildDirectory(dir, "after-file-flush"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, checkpoint string }{
		{dir, "unknown"}, {root, "after-file-flush"}, {t.TempDir(), "after-file-flush"}, {"relative/after-file-flush", "after-file-flush"},
	} {
		if err := validateChildDirectory(tc.dir, tc.checkpoint); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateChildDirectory(dir, "after-file-flush"); err == nil {
		t.Fatal("accepted nonempty fixture")
	}
}

func TestKillAtCheckpoint(t *testing.T) {
	for _, mode := range []string{"ready", "wrong", "oversize", "partial", "exit", "failed", "silent"} {
		t.Run(mode, func(t *testing.T) {
			limit := 5 * time.Second
			if mode == "silent" {
				limit = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKillHelper$") //nolint:gosec // Reexec the test binary with a fixed helper test name.
			cmd.Env = append(os.Environ(), "PROBE_TEST_CHILD="+mode)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close() }() // Wait closes StdinPipe too.
			err = killAtCheckpoint(ctx, cmd, "after-file-flush")
			if (err == nil) != (mode == "ready") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("child not reaped")
			}
		})
	}
}
