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
	send <- command{kind: "profile", profile: application.ColorProfile(n)}
	if err := wait(sent, "status.idle"); err != nil {
		return err
	}
	send <- command{kind: "open", source: os.Getenv("RASTERDOCK_COLOR_BENCHMARK_PREPARED")}
	if err := wait(sent, "status.frame"); err != nil {
		return err
	} // discard initial data-bearing picture
	var measured atomic.Bool
	var decoded, packets, failures atomic.Uint64
	var opens atomic.Uint64
	receive, received := startEngine(engineIO{
		openCapture: func(id uint32) (captureStream, error) {
			opens.Add(1)
			return platform.OpenCapture(id)
		},
		decode: func(img image.Image) (v1.Beacon, []v1.Packet, error) {
			b, p, e := codec.Decode(img)
			if measured.Load() {
				if e != nil {
					failures.Add(1)
				} else {
					decoded.Add(1)
					packets.Add(uint64(len(p)))
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
	measured.Store(true)
	send <- command{kind: "auto"}
	// Match production's separate 200ms mailbox-to-UI poll as well as engine ticks.
	uiTick := time.NewTicker(200 * time.Millisecond)
	defer uiTick.Stop()
	var applied uint64
	var completed uint64
	var elapsed time.Duration
loop:
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("incomplete transfer: %w", ctx.Err())
		case <-uiTick.C:
			select {
			case s := <-sent:
				if s.key == "status.error" {
					return fmt.Errorf("sender: %v", s.params)
				}
				if s.picture != nil {
					fyne.DoAndWait(func() { picture.Image = s.picture; picture.Refresh(); viewport.Refresh() })
					applied++
				}
			default:
			}
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
					completed = count
					break loop
				}
			}
		}
	}
	measured.Store(false)
	cancel()
	workers.Wait()
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
	if !bytes.Equal(got, want) || sha256.Sum256(got) != sha256.Sum256(want) || completed != colorBenchmarkBytes {
		return fmt.Errorf("published bytes/hash/durable count mismatch: file=%d durable=%d", len(got), completed)
	}
	host, _ := os.Hostname()
	record := map[string]interface{}{
		"trial": os.Getenv("RASTERDOCK_COLOR_BENCHMARK_TRIAL"), "colors": n,
		"host": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(),
		"gomaxprocs": runtime.GOMAXPROCS(0), "canvas_scale": scale, "window": window,
		"source_bytes": len(want), "completed_bytes": completed, "sha256": fmt.Sprintf("%x", sha256.Sum256(got)),
		"elapsed_seconds": elapsed.Seconds(), "source_bytes_per_second": float64(len(want)) / elapsed.Seconds(),
		"setup_seconds_excluded": setupSeconds, "target_engine_fps": 5, "target_ui_poll_fps": 5,
		"ui_images_applied": applied, "ui_images_applied_per_second": float64(applied) / elapsed.Seconds(),
		"decoded_frames": decoded.Load(), "decoded_packets_including_repeats": packets.Load(), "decode_failures": failures.Load(),
		"decoded_frames_per_second": float64(decoded.Load()) / elapsed.Seconds(), "capture_stream_opens": opens.Load(),
		"timing":         "blank until auto; capture-open excluded; first-frame through published-completion included; verification excluded",
		"cadence_caveat": "UI applications are not compositor presentations; decoded frames may repeat; engines share one process",
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	fmt.Printf("COLOR_BENCHMARK %s\n", data)
	return nil
}
