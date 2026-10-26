package fyneui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func benchmarkFrameTrace() (*frameTrace, func() error, error) {
	if os.Getenv("RASTERDOCK_FRAME_TRACE") != "1" {
		return nil, func() error { return nil }, nil
	}
	dir, trial := os.Getenv("RASTERDOCK_FRAME_TRACE_DIR"), os.Getenv("RASTERDOCK_COLOR_BENCHMARK_TRIAL")
	// Parent creates the private directory and test-owned name. Never accept a
	// path component or overwrite an earlier diagnostic artifact.
	if !filepath.IsAbs(dir) || !regexp.MustCompile(`^repeat[1-3]_position[1-3]_(cells[146]|colors[248])$`).MatchString(trial) {
		return nil, nil, errors.New("frame trace requires parent-owned directory and trial name")
	}
	t := newFrameTrace(frameTraceLimit)
	return t, func() error {
		path := filepath.Join(dir, trial+".jsonl")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // Validated test-owned basename under parent-created private directory.
		if err != nil {
			return err
		}
		err = errors.Join(t.dump(f), f.Close())
		fmt.Printf("FRAME_TRACE %s\n", path)
		return err
	}, nil
}
