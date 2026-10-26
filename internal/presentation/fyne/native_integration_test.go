//go:build integration && (darwin || windows)

package fyneui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	"rasterdock/internal/domain"
	v1 "rasterdock/internal/protocol/v1"
)

// Only the developer test executable understands these environment variables.
// TestMain, not a testing goroutine, owns the native application's main thread.
var nativeGUIHelper func() error
var nativeWindowChecks func(*testing.T, uint32, int, string)
var nativeColorBenchmarkHelper func() error

func TestMain(m *testing.M) {
	if os.Getenv("RASTERDOCK_COLOR_BENCHMARK_HELPER") == "1" {
		if nativeColorBenchmarkHelper == nil {
			fmt.Fprintln(os.Stderr, "color benchmark requires macOS")
			os.Exit(1)
		}
		if err := nativeColorBenchmarkHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("RASTERDOCK_NATIVE_GUI_HELPER") == "1" {
		if nativeGUIHelper == nil {
			fmt.Fprintln(os.Stderr, "native GUI helper unavailable on this platform")
			os.Exit(1)
		}
		if err := nativeGUIHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("RASTERDOCK_NATIVE_TEST_VIEWER") == "1" {
		if err := nativeTestViewer(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	// Capture runs in a GUI process in production. Initialize the native driver
	// on the main thread here too; a bare Go test process has no CGS connection.
	a := app.New()
	w := a.NewWindow("RasterDock native test receiver (hidden)")
	code := m.Run()
	w.Close()
	os.Exit(code)
}

func nativeTestViewer() error {
	colors := application.Monochrome
	if os.Getenv("RASTERDOCK_NATIVE_TEST_COLORS") == "4" {
		colors = application.Colors4
	}
	if os.Getenv("RASTERDOCK_NATIVE_TEST_COLORS") == "8" {
		colors = application.Colors8
	}
	profile := application.DefaultVisualProfile(colors)
	if value := os.Getenv("RASTERDOCK_NATIVE_TEST_GEOMETRY"); value != "" {
		if _, err := fmt.Sscanf(value, "%d,%d,%d", &profile.Geometry.Module, &profile.Geometry.Columns, &profile.Geometry.Rows); err != nil {
			return err
		}
	}
	if _, err := profile.Size(); err != nil {
		return err
	}
	p, err := application.OpenPrepared(os.Getenv("RASTERDOCK_NATIVE_TEST_PREPARED"))
	if err != nil {
		return err
	}
	packets, err := p.VisualPage(0, profile)
	if err != nil {
		return err
	}
	img, err := codec.RenderVisualPage(p.Descriptor, packets, profile, 0, 1, 1)
	if err != nil {
		return err
	}
	a := app.New()
	w := a.NewWindow(os.Getenv("RASTERDOCK_NATIVE_TEST_TITLE"))
	picture := canvas.NewImageFromImage(img)
	picture.FillMode = canvas.ImageFillOriginal
	picture.ScaleMode = canvas.ImageScalePixels
	viewport := newQRViewport(picture, w.Canvas())
	w.SetContent(viewport)
	// Test-owned viewer only: request room for the chosen physical canvas on
	// both Retina and 1x displays. Production never silently grows/shrinks a profile.
	logical, _ := physicalImageGeometry(img.Bounds(), fyne.Size{}, w.Canvas().PixelCoordinateForPosition)
	w.Resize(fyne.NewSize(max(720, logical.Width+80), max(520, logical.Height+80)))
	go func() {
		defer fyne.Do(w.Close)
		frameID := uint64(1)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			n, e := strconv.ParseUint(scanner.Text(), 10, 64)
			if e != nil {
				return
			}
			packets, e := p.VisualPage(n, profile)
			if e != nil {
				return
			}
			frameID++
			img, e := codec.RenderVisualPage(p.Descriptor, packets, profile, n, frameID, 1)
			if e != nil {
				return
			}
			fyne.DoAndWait(func() { picture.Image = img; picture.Refresh(); viewport.Refresh() })
		}
	}()
	w.ShowAndRun()
	return nil
}

func TestNativeVisualTransfer(t *testing.T) {
	testNativeVisualTransfer(t, application.DefaultVisualProfile(application.Monochrome))
}

func TestNativeFourColorTransfer(t *testing.T) {
	testNativeVisualTransfer(t, application.DefaultVisualProfile(application.Colors4))
}

func TestNativeEightColorTransfer(t *testing.T) {
	testNativeVisualTransfer(t, application.DefaultVisualProfile(application.Colors8))
}

func TestNativeGeometryTransfer(t *testing.T) {
	for _, profile := range []application.VisualProfile{
		{Colors: application.Monochrome, Geometry: application.Geometry{Module: 8, Columns: 1, Rows: 1}},
		{Colors: application.Colors4, Geometry: application.Geometry{Module: 6, Columns: 1, Rows: 1}},
		{Colors: application.Colors4, Geometry: application.Geometry{Module: 4, Columns: 2, Rows: 2}},
		{Colors: application.Colors8, Geometry: application.Geometry{Module: 4, Columns: 3, Rows: 2}},
	} {
		t.Run(fmt.Sprintf("colors%d_module%d_grid%dx%d", profile.Colors, profile.Geometry.Module, profile.Geometry.Columns, profile.Geometry.Rows), func(t *testing.T) {
			testNativeVisualTransfer(t, profile)
		})
	}
}

func testNativeVisualTransfer(t *testing.T, profile application.VisualProfile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	t.Cleanup(cancel)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	// Every fixture has at least three pages, including a wholly omitted data
	// page and a later partial page. Bound the test allocation before conversion.
	capacity := profile.Capacity()
	if capacity == 0 || capacity > 18 {
		t.Fatal("unsupported native test capacity")
	}
	binaryFixture := make([]byte, max(uint64(2349), (2*capacity-1)*512+301))
	for i := range binaryFixture {
		binaryFixture[i] = byte(i % 256)
	}
	want := map[string][]byte{"binary.bin": binaryFixture, "empty": {}, "目录/вложенный/hello.txt": []byte("Hello, мир 世界\n")}
	for name, data := range want {
		path := filepath.Join(source, name)
		must(os.MkdirAll(filepath.Dir(path), 0700))
		must(os.WriteFile(path, data, 0600))
	}
	must(os.MkdirAll(destination, 0700))
	p, err := application.Prepare(ctx, source, filepath.Join(root, "sender"), 512, storage.Flush, root)
	must(err)
	// The viewer relies exclusively on the reopened immutable snapshot.
	must(os.RemoveAll(source))
	p, err = application.OpenPrepared(p.Directory)
	must(err)
	id, err := domain.NewID()
	must(err)
	title := "RasterDock-native-test-" + domain.IDString(id)
	executable, err := os.Executable()
	must(err)
	cmd := exec.CommandContext(ctx, executable) //nolint:gosec // Only re-exec os.Executable(), the current developer test binary; no shell or external executable.
	cmd.Env = append(os.Environ(), "RASTERDOCK_NATIVE_TEST_VIEWER=1", "RASTERDOCK_NATIVE_TEST_PREPARED="+p.Directory, "RASTERDOCK_NATIVE_TEST_TITLE="+title)
	cmd.Env = append(cmd.Env, fmt.Sprintf("RASTERDOCK_NATIVE_TEST_COLORS=%d", profile.Colors),
		fmt.Sprintf("RASTERDOCK_NATIVE_TEST_GEOMETRY=%d,%d,%d", profile.Geometry.Module, profile.Geometry.Columns, profile.Geometry.Rows))
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	stdin, err := cmd.StdinPipe()
	must(err)
	must(cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("viewer exit: %v; %s", err, diagnostics.String())
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("viewer failed to close gracefully")
		}
	})
	var window uint32
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		sources, e := platform.Sources()
		if e != nil {
			t.Fatalf("native enumeration (Screen Recording permission required): %v", e)
		}
		for _, s := range sources {
			// Sources prefixes the native title with the owning application.
			if s.Title == title || strings.HasSuffix(s.Title, " — "+title) {
				window = s.ID
				break
			}
		}
		if window != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if window == 0 {
		t.Fatal("own UUID-titled viewer not enumerated within 10s; desktop and Screen Recording required")
	}
	t.Logf("host=%s/%s %s profile=%+v own-window=%d manifest=%d packets=%d", runtime.GOOS, runtime.GOARCH, runtime.Version(), profile, window, p.ManifestPackets(), p.PacketCount())
	var receiver *storage.Receiver
	t.Cleanup(func() {
		if receiver != nil {
			if e := receiver.Close(); e != nil {
				t.Error(e)
			}
		}
	})
	session := filepath.Join(root, "receiver")
	var descriptor v1.Descriptor
	// Only captured packets/descriptors enter the receiver. Render is solely a
	// geometry oracle, never a decoding input or receive source.
	capture := func(n uint64) (v1.Beacon, []v1.Packet) {
		t.Helper()
		_, e := fmt.Fprintln(stdin, n)
		must(e)
		until := time.Now().Add(5 * time.Second)
		var last error
		for time.Now().Before(until) && ctx.Err() == nil {
			frame, e := nativeCaptureFrame(ctx, window)
			if e != nil {
				t.Fatalf("own-window native capture: %v", e)
			}
			if frame.CapturedAt.IsZero() {
				t.Fatal("missing capture timestamp")
			}
			b, packets, e := codec.Decode(frame.Image)
			last = e
			if e == nil && b.Transfer == p.Descriptor.Transfer && b.Ordinal == n && b.Colors == byte(profile.Colors) && uint64(len(packets)) == profile.Capacity() {
				size, sizeErr := profile.Size()
				must(sizeErr)
				g := profile.Geometry
				if int(b.Width) != size.X || int(b.Height) != size.Y || int(b.Module) != g.Module || b.BeaconModule != 4 || b.Version != 20 || int(b.Columns) != g.Columns || int(b.Rows) != g.Rows || int(b.Active) != g.Columns*g.Rows || b.GridX != 244 || b.GridY != 40 || int(b.CellWidth) != g.Pitch() || int(b.CellHeight) != g.Pitch() {
					t.Fatalf("decoded geometry differs from the fixed native test profile: %+v", b)
				}
				expectedPackets, e := p.VisualPage(n, profile)
				must(e)
				expected, e := codec.RenderVisualPage(p.Descriptor, expectedPackets, profile, n, b.Frame, 1)
				must(e)
				offset, ok := nativeExactTranslation(frame.Image, expected)
				if !ok {
					// WindowServer's opening animation may already be decodable
					// but still interpolated. Wait for exact pixels, not a sleep
					// guessed to outlast the animation, within the same deadline.
					last = fmt.Errorf("capture %v lacks exact 1:1 physical-pixel canvas", frame.Image.Bounds())
				} else {
					t.Logf("ordinal=%d capture=%v exact-canvas-offset=%v integer-scale=1 module=%d physical pixels", n, frame.Image.Bounds(), offset, b.Module)
					return b, packets
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("ordinal %d not captured/decoded within 5s: %v", n, last)
		return v1.Beacon{}, nil
	}
	accept := func(n uint64) {
		t.Helper()
		b, packets := capture(n)
		if receiver == nil {
			descriptor = b.Descriptor
			receiver, err = storage.OpenReceive(session, destination, descriptor, application.DecodeManifest, root)
			must(err)
		}
		must(receiver.CheckDescriptor(b.Descriptor))
		for _, packet := range packets {
			must(receiver.Accept(packet))
		}
	}
	for n := uint64(0); n < p.ManifestPackets(); n += profile.Capacity() {
		accept(n)
	}
	accept(0)
	must(receiver.Publish())
	// Delay the first data packet; receive later data in reverse order.
	missing := ((p.ManifestPackets() + profile.Capacity() - 1) / profile.Capacity()) * profile.Capacity()
	if p.PacketCount() <= missing+profile.Capacity() {
		t.Fatal("fixture needs at least two data packets")
	}
	lastPage := ((p.PacketCount() - 1) / profile.Capacity()) * profile.Capacity()
	for n := lastPage; n > missing; n -= profile.Capacity() {
		accept(n)
	}
	must(receiver.Publish())
	published, total, err := receiver.PublishedFiles()
	must(err)
	if total != 3 || published >= total {
		t.Fatalf("missing packet falsely completed publication: %d/%d", published, total)
	}
	before, err := receiver.DurableBytes()
	must(err)
	must(receiver.Close())
	receiver = nil
	receiver, err = storage.OpenReceive(session, destination, descriptor, application.DecodeManifest, root)
	must(err)
	after, err := receiver.DurableBytes()
	must(err)
	if before == 0 || before != after {
		t.Fatalf("durability before=%d after=%d", before, after)
	}
	accept(lastPage)
	after, err = receiver.DurableBytes()
	must(err)
	if before != after {
		t.Fatal("duplicate inflated durable progress")
	}
	accept(missing)
	must(receiver.Publish())
	must(receiver.Publish())
	published, total, err = receiver.PublishedFiles()
	must(err)
	if published != 3 || total != 3 {
		t.Fatalf("publication=%d/%d, want 3/3", published, total)
	}
	for name, expected := range want {
		actual, e := os.ReadFile(filepath.Join(destination, name)) //nolint:gosec // Test-owned temporary destination and fixed fixture paths.
		must(e)
		if !bytes.Equal(actual, expected) || sha256.Sum256(actual) != sha256.Sum256(expected) {
			t.Fatalf("bytes/hash differ: %s", name)
		}
	}
	t.Logf("restart durable bytes=%d; missing packet repaired; publication=%d/%d; all bytes/SHA256 verified", before, published, total)
	// A clean pass measures useful source bytes separately from deliberate loss
	// and restart. This small-fixture harness rate includes capture polling and
	// the geometry oracle; it is not a production throughput or speedup claim.
	for trial := range 3 {
		measureRoot := t.TempDir()
		measureDest := filepath.Join(measureRoot, "output")
		must(os.Mkdir(measureDest, 0700))
		measured, e := storage.OpenReceive(filepath.Join(measureRoot, "session"), measureDest, descriptor, application.DecodeManifest, measureRoot)
		must(e)
		t.Cleanup(func() { _ = measured.Close() })
		started := time.Now()
		for n := uint64(0); n < p.PacketCount(); n += profile.Capacity() {
			b, packets := capture(n)
			must(measured.CheckDescriptor(b.Descriptor))
			for _, packet := range packets {
				must(measured.Accept(packet))
			}
		}
		must(measured.Publish())
		elapsed := time.Since(started)
		var sourceBytes int
		for name, expected := range want {
			actual, readErr := os.ReadFile(filepath.Join(measureDest, name)) //nolint:gosec // Fixed fixture paths in private temporary output.
			must(readErr)
			if !bytes.Equal(actual, expected) || sha256.Sum256(actual) != sha256.Sum256(expected) {
				t.Fatalf("measurement output differs: %s", name)
			}
			sourceBytes += len(expected)
		}
		must(measured.Close())
		t.Logf("clean-pass profile=%+v trial=%d verified-source-bytes=%d elapsed=%s useful-bytes/s=%.1f (geometry fixture harness, not comparable production throughput)", profile, trial+1, sourceBytes, elapsed, float64(sourceBytes)/elapsed.Seconds())
	}
	if nativeWindowChecks != nil {
		nativeWindowChecks(t, window, cmd.Process.Pid, title)
	}
}

func nativeCaptureFrame(parent context.Context, id uint32) (frame platform.Frame, err error) {
	stream, err := platform.OpenCapture(id)
	if err != nil {
		return frame, err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		frame, err = stream.Next(ctx)
		if !errors.Is(err, platform.ErrFramePending) {
			return frame, err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		}
	}
	return platform.Frame{}, ctx.Err()
}

func TestNativeExactTranslationOracle(t *testing.T) {
	want := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			i := want.PixOffset(x, y)
			copy(want.Pix[i:i+4], []byte{byte(15 + x*60), byte(15 + y*70), byte(x + y), 255})
		}
	}
	got := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			got.SetRGBA(5+x, 7+y, want.RGBAAt(x, y))
		}
	}
	if offset, ok := nativeExactTranslation(got, want); !ok || offset != image.Pt(5, 7) {
		t.Fatalf("exact translation rejected: %v %v", offset, ok)
	}
	got.Pix[got.PixOffset(8, 9)] ^= 1
	if _, ok := nativeExactTranslation(got, want); ok {
		t.Fatal("one-pixel corruption accepted")
	}
	expanded := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			expanded.SetRGBA(x, y, want.RGBAAt(x/2, y/2))
		}
	}
	if _, ok := nativeExactTranslation(expanded, want); ok {
		t.Fatal("2x replication accepted as 1:1")
	}
}

// Exact translated RGBA equality proves four physical pixels per module,
// including all quiet zones, without rescaling or thresholding approximations.
func nativeExactTranslation(got, want *image.RGBA) (image.Point, bool) {
	width, height := want.Bounds().Dx(), want.Bounds().Dy()
	for y := got.Rect.Min.Y; y+height <= got.Rect.Max.Y; y++ {
		row := got.Pix[got.PixOffset(got.Rect.Min.X, y):got.PixOffset(got.Rect.Max.X, y)]
		needle := want.Pix[:width*4]
		for start := 0; start+len(needle) <= len(row); {
			i := bytes.Index(row[start:], needle)
			if i < 0 {
				break
			}
			i += start
			if i%4 == 0 {
				x := got.Rect.Min.X + i/4
				match := true
				for dy := 1; dy < height; dy++ {
					a, b := got.PixOffset(x, y+dy), want.PixOffset(0, dy)
					if !bytes.Equal(got.Pix[a:a+width*4], want.Pix[b:b+width*4]) {
						match = false
						break
					}
				}
				if match {
					return image.Pt(x, y), true
				}
			}
			start = i + 1
		}
	}
	return image.Point{}, false
}
