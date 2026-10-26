//go:build integration && darwin

package fyneui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

func init() { nativeColorBenchmarkHelper = runColorBenchmarkHelper }

// Main-thread GUI, with both unmodified production engines on workers. The
// viewer stays blank throughout setup: even a color manifest page can carry
// source data, so the configured sender's initial picture is discarded.
func runColorBenchmarkHelper() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cells := os.Getenv("RASTERDOCK_COLOR_BENCHMARK_CELLS")
	switch cells {
	case "", "0", "1", "4", "6":
	default:
		return fmt.Errorf("unsupported benchmark cell count %q", cells)
	}
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	title := "RasterDock-color-benchmark-" + domain.IDString(id)
	a := app.New()
	w := a.NewWindow(title)
	picture := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	capacity := &viewportCapacity{}
	viewport := newQRViewport(picture, w.Canvas(), capacity)
	w.SetContent(viewport)
	w.Resize(fyne.NewSize(720, 520))
	if cells != "" && cells != "0" {
		// Same test-window area for every grid, never resize per selected grid.
		w.Resize(fyne.NewSize(900, 650))
	}
	result := make(chan error, 1)
	go func() {
		result <- colorBenchmarkTrial(ctx, title, w, picture, viewport, capacity)
		fyne.Do(w.Close)
	}()
	w.ShowAndRun()
	cancel() // Manual window closure must stop both engines too.
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("benchmark engines did not stop after GUI closure")
	}
}

func colorBenchmarkTrial(parent context.Context, title string, w fyne.Window, picture *canvas.Image, viewport fyne.CanvasObject, capacity *viewportCapacity) (err error) {
	setupStart := time.Now()
	trace, finishTrace, traceErr := benchmarkFrameTrace()
	if traceErr != nil {
		return traceErr
	}
	defer func() { err = errors.Join(err, finishTrace()) }()
	ctx, cancel := context.WithTimeout(parent, 170*time.Second)
	defer cancel()
	root := os.Getenv("RASTERDOCK_COLOR_BENCHMARK_ROOT")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0700); err != nil { //nolint:gosec // Private test root passed by this test's parent process, not product input.
		return err
	}
	n, err := strconv.ParseUint(os.Getenv("RASTERDOCK_COLOR_BENCHMARK_COLORS"), 10, 8)
	if err != nil || (n != 2 && n != 4 && n != 8) {
		return fmt.Errorf("invalid profile %q", os.Getenv("RASTERDOCK_COLOR_BENCHMARK_COLORS"))
	}
	profile := application.DefaultVisualProfile(application.ColorProfile(n))
	gridMode := false
	if value := os.Getenv("RASTERDOCK_COLOR_BENCHMARK_CELLS"); value != "" && value != "0" {
		cells, parseErr := strconv.Atoi(value)
		if parseErr != nil || n != 8 {
			return fmt.Errorf("grid benchmark requires a valid cell count and eight colors")
		}
		profile, err = gridBenchmarkProfile(cells)
		if err != nil {
			return err
		}
		gridMode = true
	}
	var window uint32
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		sources, e := platform.Sources()
		if e != nil {
			return e
		}
		for _, source := range sources {
			if source.Title == title || strings.HasSuffix(source.Title, " — "+title) {
				window = source.ID
				break
			}
		}
		if window != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if window == 0 {
		return fmt.Errorf("own UUID window not enumerated")
	}
	var scale float32
	fyne.DoAndWait(func() { scale = w.Canvas().Scale() })
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	var workers sync.WaitGroup
	startEngine := func(io engineIO) (chan command, chan snapshot) {
		io = trace.wrap(io)
		commands, events := make(chan command, 8), make(chan snapshot, 1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			io.ticks = ticker.C
			runEngine(ctx, root, commands, events, logger, io)
		}()
		return commands, events
	}
	// Joined cancellation executes production stop(), closing receiver and stream.
	defer func() {
		cancel()
		workers.Wait()
		if logs.Len() != 0 {
			err = errors.Join(err, fmt.Errorf("engine diagnostics: %s", logs.String()))
		}
	}()
	send, sent := startEngine(engineIO{viewport: capacity})
	wait := func(events <-chan snapshot, key string) error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case s := <-events:
				if s.key == "status.error" {
					return fmt.Errorf("engine: %v", s.params)
				}
				if s.key == key {
					return nil
				}
			}
		}
	}
	if gridMode {
		// A common viewport must fit the largest grid even in a 1-cell trial.
		largest, e := gridBenchmarkProfile(6)
		if e != nil {
			return e
		}
		if e = checkFit(largest, capacity); e != nil {
			return e
		}
	}
	send <- command{kind: "profile", visual: profile}
	if err := wait(sent, "status.idle"); err != nil {
		return err
	}
	send <- command{kind: "open", source: os.Getenv("RASTERDOCK_COLOR_BENCHMARK_PREPARED")}
	if err := wait(sent, "status.frame"); err != nil {
		return err
	} // discard initial data-bearing picture
	var measured atomic.Bool
	var decoded, packets, failures atomic.Uint64
	var decodeCalls, decodeNanos, decodeMaxNanos, partialFrames atomic.Uint64
	var opens atomic.Uint64
	var captureWidth, captureHeight atomic.Int64
	receive, received := startEngine(engineIO{
		openCapture: func(id uint32) (captureStream, error) {
			opens.Add(1)
			return platform.OpenCapture(id)
		},
		decode: func(img image.Image) (v1.Beacon, []v1.Packet, error) {
			captureWidth.Store(int64(img.Bounds().Dx()))
			captureHeight.Store(int64(img.Bounds().Dy()))
			decodeStart := time.Now()
			b, p, e := codec.Decode(img)
			decodeTime := time.Since(decodeStart)
			if measured.Load() {
				decodeCalls.Add(1)
				if decodeTime > 0 {
					duration := uint64(decodeTime)
					decodeNanos.Add(duration)
					decodeMaxNanos.Store(max(decodeMaxNanos.Load(), duration))
				}
				if e != nil {
					failures.Add(1)
				} else {
					decoded.Add(1)
					packets.Add(uint64(len(p)))
					if uint64(len(p)) < profile.Capacity() {
						partialFrames.Add(1)
					}
				}
			}
			return b, p, e
		},
		openReceive: anchoredReceive(root),
	})
	receive <- command{kind: "receive", window: window, destination: destination, session: filepath.Join(root, "receive")}
	if err := wait(received, "status.waiting"); err != nil {
		return err
	}
	// READY means OpenCapture returned, not that a data frame was prewarmed.
	// First-frame latency remains measured. No transfer pixels have been shown.
	setupSeconds := time.Since(setupStart).Seconds()
	start := time.Now()
	trace.add(frameTraceRecord{Stage: "measurement.begin"})
	measured.Store(true)
	send <- command{kind: "auto"}
	var applied atomic.Uint64
	senderErrors := startBenchmarkDelivery(ctx, &workers, sent, func(s snapshot) {
		if s.picture == nil {
			return
		}
		fyne.DoAndWait(func() {
			trace.add(frameTraceRecord{Stage: "ui.apply.begin", Picture: s.pictureID})
			picture.Image = s.picture
			picture.Refresh()
			viewport.Refresh()
			applied.Add(1)
			trace.add(frameTraceRecord{Stage: "ui.apply.complete", Picture: s.pictureID})
		})
	})
	var completed uint64
	var elapsed time.Duration
loop:
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("incomplete transfer: %w", ctx.Err())
		case err := <-senderErrors:
			return err
		case s := <-received:
			if s.key == "status.error" {
				return fmt.Errorf("receiver: %v", s.params)
			}
			if s.key == "status.receiving" {
				published, publishedOK := s.params["published"].(uint64)
				total, totalOK := s.params["total"].(uint64)
				count, countOK := s.params["bytes"].(uint64)
				if !publishedOK || !totalOK || !countOK {
					return fmt.Errorf("unexpected receiver statistics types: %T/%T/%T", s.params["published"], s.params["total"], s.params["bytes"])
				}
				if total == 1 && published == total {
					elapsed = time.Since(start)
					trace.add(frameTraceRecord{Stage: "measurement.end"})
					completed = count
					break loop
				}
			}
		}
	}
	measured.Store(false)
	cancel()
	workers.Wait()
	if err := trace.check(); err != nil {
		return err
	}
	if logs.Len() != 0 {
		return fmt.Errorf("engine diagnostics: %s", logs.String())
	}
	if opens.Load() != 1 {
		return fmt.Errorf("expected exactly one capture stream, got %d", opens.Load())
	}
	got, err := os.ReadFile(filepath.Join(destination, "payload.bin")) //nolint:gosec // Fixed fixture name beneath test-parent-owned destination.
	if err != nil {
		return err
	}
	want := colorBenchmarkFixture()
	if gridMode {
		want = gridBenchmarkFixture()
	}
	if !bytes.Equal(got, want) || sha256.Sum256(got) != sha256.Sum256(want) || completed != uint64(len(want)) {
		return fmt.Errorf("published bytes/hash/durable count mismatch: file=%d durable=%d", len(got), completed)
	}
	host, _ := os.Hostname()
	record := map[string]interface{}{
		"frame_trace_enabled": trace != nil, "trace_timing": "when enabled, in-memory instrumentation overhead included; JSONL dump after cleanup excluded",
		"trial": os.Getenv("RASTERDOCK_COLOR_BENCHMARK_TRIAL"), "colors": n,
		"module": profile.Geometry.Module, "columns": profile.Geometry.Columns, "rows": profile.Geometry.Rows,
		"packets_per_page": profile.Capacity(), "grid_benchmark": gridMode,
		"capture_width": captureWidth.Load(), "capture_height": captureHeight.Load(),
		"host": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(),
		"gomaxprocs": runtime.GOMAXPROCS(0), "canvas_scale": scale, "window": window,
		"source_bytes": len(want), "completed_bytes": completed, "sha256": fmt.Sprintf("%x", sha256.Sum256(got)),
		"elapsed_seconds": elapsed.Seconds(), "source_bytes_per_second": float64(len(want)) / elapsed.Seconds(),
		"setup_seconds_excluded": setupSeconds, "target_engine_fps": 5, "ui_delivery_mode": "event-driven",
		"production_dimensions_refresh_ms": dimensionsRefreshInterval.Milliseconds(),
		"ui_images_applied":                applied.Load(), "ui_images_applied_per_second": float64(applied.Load()) / elapsed.Seconds(),
		"decoded_frames": decoded.Load(), "decoded_packets_including_repeats": packets.Load(), "decode_failures": failures.Load(),
		"decoded_frames_per_second": float64(decoded.Load()) / elapsed.Seconds(), "capture_stream_opens": opens.Load(),
		"decode_mean_ms": float64(decodeNanos.Load()) / float64(max(uint64(1), decodeCalls.Load())) / 1e6,
		"decode_max_ms":  float64(decodeMaxNanos.Load()) / 1e6, "decode_calls": decodeCalls.Load(),
		"partial_decoded_frames": partialFrames.Load(),
		"timing":                 "blank until auto; capture-open excluded; first-frame through published-completion included; verification excluded",
		"cadence_caveat":         "UI applications are not compositor presentations; decoded frames may repeat; engines share one process",
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	fmt.Printf("COLOR_BENCHMARK %s\n", data)
	return nil
}

// Join this single delivery worker with the engines before trace dumping. The
// fixture has no dimensions label; only production needs the refresh callback.
func startBenchmarkDelivery(ctx context.Context, workers *sync.WaitGroup, events <-chan snapshot, apply func(snapshot)) <-chan error {
	errors := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		deliverSnapshots(ctx, events, nil, func(s snapshot) {
			if s.key == "status.error" {
				select {
				case errors <- fmt.Errorf("sender: %v", s.params):
				default:
				}
				return
			}
			apply(s)
		}, nil)
	}()
	return errors
}
