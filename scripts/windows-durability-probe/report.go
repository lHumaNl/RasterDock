// Package main collects developer-only Windows API observations, not durability proof.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

const (
	sourceData  = "probe source payload\n"
	targetData  = "protected destination payload\n"
	fixtureMode = 0600
)

type operation struct {
	Name      string  `json:"name"`
	API       string  `json:"api"`
	Access    string  `json:"access"`
	Flags     string  `json:"flags"`
	Success   bool    `json:"success"`
	Critical  bool    `json:"setup_or_invariant"`
	ErrorCode *uint64 `json:"windows_error_code,omitempty"`
	Error     string  `json:"error,omitempty"`
}

type report struct {
	GOOS       string      `json:"goos"`
	GOARCH     string      `json:"goarch"`
	OSVersion  string      `json:"os_version,omitempty"`
	Elevated   *bool       `json:"token_elevated,omitempty"`
	Filesystem string      `json:"filesystem,omitempty"`
	OwnedRoot  string      `json:"owned_temp_directory,omitempty"`
	Limitation string      `json:"limitation"`
	Failed     bool        `json:"setup_or_invariant_failed"`
	Operations []operation `json:"operations"`
}

func newReport() *report {
	return &report{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Limitation: "Developer-only API and process-kill observations, not acceptance proof, not production storage acceptance or Receiver recovery evidence. Process kill is not OS crash, reboot, VM reset, or power-loss evidence. Power-loss tests remain required.",
		Operations: []operation{}}
}

func (r *report) record(name, api, access, flags string, err error, critical bool) {
	op := operation{Name: name, API: api, Access: access, Flags: flags, Success: err == nil, Critical: critical}
	if err != nil {
		op.Error = err.Error()
		var errno syscall.Errno
		if errors.As(err, &errno) {
			code := uint64(errno)
			op.ErrorCode = &code
		}
		r.Failed = r.Failed || critical
	}
	r.Operations = append(r.Operations, op)
}

func writeReport(w io.Writer, r *report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r)
}

func verifyContent(path, want string) error {
	got, err := os.ReadFile(path) //nolint:gosec // Only fixed fixture names beneath the probe-owned temporary directory (or test temporary directories).
	if err != nil {
		return err
	}
	if string(got) != want {
		return fmt.Errorf("content invariant failed: %s", path)
	}
	return nil
}

func verifyNoReplace(source, target string) error {
	return errors.Join(verifyContent(source, sourceData), verifyContent(target, targetData))
}

func verifyMove(source, target string, moved bool) error {
	present, absent := source, target
	if moved {
		present, absent = target, source
	}
	_, err := os.Lstat(absent)
	if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(fmt.Errorf("expected absent path %s", absent), err)
	}
	return verifyContent(present, sourceData)
}

func verifyDirectoryMove(source, target string, moved bool) error {
	present, absent := source, target
	if moved {
		present, absent = target, source
	}
	_, err := os.Lstat(absent)
	if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(fmt.Errorf("expected absent directory %s", absent), err)
	}
	return verifyContent(filepath.Join(present, "child", "marker"), sourceData)
}
