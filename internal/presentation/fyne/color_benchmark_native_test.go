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
	if err := os.WriteFile(filepath.Join(source, "payload.bin"), colorBenchmarkFixture(), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := application.Prepare(ctx, source, filepath.Join(root, "prepared"), 512, storage.Flush, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Manifest.Files) != 1 || p.Manifest.Files[0].Encoding != "raw" || p.Manifest.Files[0].SourceSize != colorBenchmarkBytes {
		t.Fatal("benchmark requires one uncompressed 128 KiB source file")
	}
	// No source tree is available to either engine; all nine trials reuse this snapshot.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for repeat, order := range colorBenchmarkOrder() {
		for position, profile := range order {
			name := fmt.Sprintf("repeat%d_position%d_colors%d", repeat+1, position+1, profile)
			if !t.Run(name, func(t *testing.T) {
				trial, stop := context.WithTimeout(ctx, 180*time.Second)
				defer stop()
				cmd := exec.CommandContext(trial, exe) // Only this test executable; no shell.
				cmd.Env = append(os.Environ(), "RASTERDOCK_COLOR_BENCHMARK_HELPER=1",
					"RASTERDOCK_COLOR_BENCHMARK_PREPARED="+p.Directory,
					"RASTERDOCK_COLOR_BENCHMARK_ROOT="+t.TempDir(),
					"RASTERDOCK_COLOR_BENCHMARK_TRIAL="+name,
					fmt.Sprintf("RASTERDOCK_COLOR_BENCHMARK_COLORS=%d", profile))
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
