//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const childMarker = "--private-process-kill-fixture-v1"

func killCases(r *report, root string) {
	for _, checkpoint := range []string{"after-file-flush", "after-publication"} {
		dir := filepath.Join(root, checkpoint)
		err := os.Mkdir(dir, directoryMode)
		r.record(checkpoint+" setup", "os.Mkdir", "", "parent-owned empty fixture", err, true)
		if err != nil {
			continue
		}
		api, flags := "FlushFileBuffers", "GENERIC_WRITE; exclusive create"
		if checkpoint == "after-publication" {
			api, flags = "FlushFileBuffers + MoveFileExW", "MOVEFILE_WRITE_THROUGH (no REPLACE_EXISTING or COPY_ALLOWED)"
		}
		err = launchKillChild(dir, checkpoint)
		r.record(checkpoint, api+" / checkpoint pipe / Process.Kill / Wait", "writable file", flags, err, true)
		if err == nil {
			r.record(checkpoint+" after death invariant", "read/stat verification", "", "synthetic bytes and source/target names", verifyMove(filepath.Join(dir, "source"), filepath.Join(dir, "target"), checkpoint == "after-publication"), true)
		}
	}
}

func launchKillChild(dir, checkpoint string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, childMarker) //nolint:gosec // Reexec only this developer executable; no caller-selected command or args.
	request := checkpoint + "\n" + dir + "\n"
	if len(request) > 1024 || strings.ContainsAny(dir, "\r\n") {
		return fmt.Errorf("fixture request too long or invalid")
	}
	childInput, input, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = childInput.Close() }()
	cmd.Stdin = childInput
	// A tiny pipe write is done before Start; no unbounded copy goroutine or
	// environment-controlled child switch. Reject excessively long inputs.
	if _, err = io.WriteString(input, request); err != nil {
		return err
	}
	return killAtCheckpoint(ctx, cmd, checkpoint)
}

func runKillChild() error {
	in := bufio.NewReaderSize(os.Stdin, 2048)
	checkpointLine, err := in.ReadSlice('\n')
	if err != nil {
		return err
	}
	checkpoint := strings.TrimSuffix(string(checkpointLine), "\n")
	dirLine, err := in.ReadSlice('\n')
	if err != nil {
		return err
	}
	dir := strings.TrimSuffix(string(dirLine), "\n")
	if err = validateChildDirectory(dir, checkpoint); err != nil {
		return err
	}
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	f, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fixtureMode) //nolint:gosec // Fixed filename under validated private, empty fixture.
	if err != nil {
		return fmt.Errorf("exclusive create: %w", err)
	}
	defer func() { _ = f.Close() }() // A successful child is killed with this handle open.
	if _, err = io.WriteString(f, sourceData); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err = windows.FlushFileBuffers(windows.Handle(f.Fd())); err != nil {
		return fmt.Errorf("FlushFileBuffers: %w", err)
	}
	if checkpoint == "after-publication" {
		if err = f.Close(); err != nil {
			return err
		}
		from, err := windows.UTF16PtrFromString(source)
		if err != nil {
			return err
		}
		to, err := windows.UTF16PtrFromString(target)
		if err != nil {
			return err
		}
		if err = windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
			return fmt.Errorf("MoveFileExW: %w", err)
		}
	}
	// Only signal AFTER the successful API call. Flush checkpoint leaves the
	// writable handle open, so killing the child skips orderly file close.
	if _, err = fmt.Fprintln(os.Stdout, checkpoint); err != nil {
		return err
	}
	_, err = in.ReadByte() // Parent keeps pipe open and never sends release.
	return fmt.Errorf("unexpected release/EOF instead of hard kill: %w", err)
}
