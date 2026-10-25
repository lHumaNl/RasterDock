package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const tempPrefix = "rasterdock-windows-api-probe-"

// The caller supplies a deadline and keeps stdin open until this returns.
// stdout is bounded; stderr is discarded by exec rather than accumulated.
func killAtCheckpoint(ctx context.Context, cmd *exec.Cmd, checkpoint string) error {
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }() // Wait also closes this pipe.
	if err = cmd.Start(); err != nil {
		return err
	}
	line, readErr := bufio.NewReaderSize(out, 512).ReadSlice('\n')
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait() // Always reap, including malformed output and deadline kills.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return fmt.Errorf("missing/bounded checkpoint: %w", readErr)
	}
	if string(line) != checkpoint+"\n" {
		return fmt.Errorf("unexpected checkpoint: %q", line)
	}
	if killErr != nil {
		return fmt.Errorf("hard kill: %w", killErr)
	}
	return verifyKilledExit(waitErr)
}

func verifyKilledExit(waitErr error) error {
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) || exit.Success() {
		return errors.Join(errors.New("child did not fail after hard kill"), waitErr)
	}
	if runtime.GOOS == "windows" {
		if exit.ExitCode() != 1 {
			return fmt.Errorf("unexpected child exit: %w", waitErr)
		}
	} else {
		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			return fmt.Errorf("child was not killed: %w", waitErr)
		}
	}
	return nil
}

// Only a precreated, empty, fixed child directory directly under our temp
// prefix is accepted. No environment variable activates child mode or supplies
// a path. This guards accidental invocation, not a hostile same-user process.
func validateChildDirectory(dir, checkpoint string) error {
	if checkpoint != "after-file-flush" && checkpoint != "after-publication" {
		return fmt.Errorf("unknown checkpoint")
	}
	root := filepath.Dir(dir)
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Base(dir) != checkpoint ||
		filepath.Dir(root) != filepath.Clean(os.TempDir()) ||
		!strings.HasPrefix(filepath.Base(root), tempPrefix) || filepath.Base(root) == tempPrefix {
		return fmt.Errorf("not a private probe fixture directory")
	}
	for _, path := range []string{root, dir} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture must be a real directory")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("fixture must be empty")
	}
	return nil
}
