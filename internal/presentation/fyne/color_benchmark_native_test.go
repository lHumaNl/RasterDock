//go:build integration && darwin

package fyneui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
)

// Deliberately not a Go Benchmark: one fixed, bounded nine-trial experiment,
// never testing.B calibration, and never enabled by ordinary integration runs.
func TestNativeColorBenchmark(t *testing.T) {
	if os.Getenv("RASTERDOCK_COLOR_BENCHMARK") != "1" {
		t.Skip("opt in with RASTERDOCK_COLOR_BENCHMARK=1; macOS desktop required")
	}
	runNativeThroughputBenchmark(t, false)
}

func TestNativeGridBenchmark(t *testing.T) {
	if os.Getenv("RASTERDOCK_GRID_BENCHMARK") != "1" {
		t.Skip("opt in with RASTERDOCK_GRID_BENCHMARK=1; macOS desktop required")
	}
	runNativeThroughputBenchmark(t, true)
}

func runNativeThroughputBenchmark(t *testing.T, grids bool) {
	t.Helper()
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				t.Fatal("throughput measurements must not use -race")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	data, order := colorBenchmarkFixture(), colorBenchmarkOrder()
	if grids {
		data, order = gridBenchmarkFixture(), gridBenchmarkOrder()
	}
	if err := os.WriteFile(filepath.Join(source, "payload.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(ctx, source, filepath.Join(root, "prepared"), 512, storage.Flush, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Files) != 1 || p.Manifest.Files[0].Encoding != "raw" || p.Manifest.Files[0].SourceSize != uint64(len(data)) {
		t.Fatal("benchmark requires one uncompressed source file of the configured size")
	}
	// No source tree is available to either engine; all nine trials reuse this snapshot.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	traceDir := ""
	if os.Getenv("RASTERDOCK_FRAME_TRACE") == "1" {
		traceDir, err = os.MkdirTemp("", "rasterdock-frame-trace-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("persistent frame traces: %s", traceDir)
	}
	for repeat, row := range order {
		for position, selection := range row {
			colors, cells := selection, 0
			name := fmt.Sprintf("repeat%d_position%d_colors%d", repeat+1, position+1, colors)
			if grids {
				colors, cells = 8, selection
				name = fmt.Sprintf("repeat%d_position%d_cells%d", repeat+1, position+1, cells)
			}
			if !t.Run(name, func(t *testing.T) {
				trial, stop := context.WithTimeout(ctx, 180*time.Second)
				defer stop()
				cmd := exec.CommandContext(trial, exe) // Only this test executable; no shell.
				cmd.Env = append(os.Environ(), "RASTERDOCK_COLOR_BENCHMARK_HELPER=1",
					"RASTERDOCK_FRAME_TRACE_DIR="+traceDir,
					"RASTERDOCK_COLOR_BENCHMARK_PREPARED="+p.Directory,
					"RASTERDOCK_COLOR_BENCHMARK_ROOT="+t.TempDir(),
					"RASTERDOCK_COLOR_BENCHMARK_TRIAL="+name,
					fmt.Sprintf("RASTERDOCK_COLOR_BENCHMARK_COLORS=%d", colors),
					fmt.Sprintf("RASTERDOCK_COLOR_BENCHMARK_CELLS=%d", cells))
				out, err := cmd.CombinedOutput()
				t.Logf("%s", out)
				if trial.Err() != nil || ctx.Err() != nil {
					t.Fatalf("benchmark deadline expired: trial=%v batch=%v", trial.Err(), ctx.Err())
				}
				if err != nil {
					t.Fatalf("incomplete/failed trial (no successful partial rate): %v", err)
				}
			}) {
				return
			}
		}
	}
}
