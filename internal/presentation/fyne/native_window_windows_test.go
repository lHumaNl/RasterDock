//go:build integration && windows

package fyneui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"rasterdock/internal/adapters/platform"
)

func init() { nativeWindowChecks = checkOwnedWindowsCaptureLifecycle }

func checkOwnedWindowsCaptureLifecycle(t *testing.T, id uint32, pid int, title string) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	name, err := windows.UTF16PtrFromString(title)
	if err != nil {
		t.Fatal(err)
	}
	hwnd, _, _ := user32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(name))) //nolint:gosec // Live UTF-16 title pointer passed directly to synchronous Win32 lookup.
	assertOwnedWindow(t, hwnd, pid)
	show := user32.NewProc("ShowWindow")
	// These operations target only the UUID-titled subprocess created by this
	// test, never a window selected from the user's desktop by position/title alone.
	t.Cleanup(func() {
		var owner uint32
		_, _, _ = user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner))) //nolint:gosec // Win32 writes exactly one DWORD to this live uint32.
		if owner == uint32(pid) {                                                                        //nolint:gosec // PID from the owned Windows test subprocess fits DWORD.
			_, _, _ = show.Call(hwnd, 9) // SW_RESTORE
		}
	})

	stream, err := platform.OpenCapture(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stream != nil {
			if err := stream.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	// Establish a real persistent stream before testing stale-frame prevention.
	frame := awaitNativeFrame(t, stream)
	t.Logf("persistent WGC initial frame: %v", frame.Image.Bounds())
	assertOwnedWindow(t, hwnd, pid)
	_, _, _ = show.Call(hwnd, 6) // SW_MINIMIZE
	deadline := time.Now().Add(3 * time.Second)
	for {
		minimized, _, _ := user32.NewProc("IsIconic").Call(hwnd)
		if minimized != 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = stream.Close()
			t.Fatal("owned test window did not minimize")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var captureErr error
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, captureErr = stream.Next(t.Context())
		if captureErr != nil && !errors.Is(captureErr, platform.ErrFramePending) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closeErr := stream.Close()
	if captureErr == nil || errors.Is(captureErr, platform.ErrFramePending) {
		t.Fatalf("minimized window must fail rather than deliver stale pixels: %v", captureErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	assertOwnedWindow(t, hwnd, pid)
	_, _, _ = show.Call(hwnd, 9) // SW_RESTORE

	// A fresh explicit enumeration/selection is required after a terminal source
	// failure; do not assume old source tokens can silently reopen a window.
	id = selectOwnedNativeSource(t, title)
	stream, err = platform.OpenCapture(id)
	if err != nil {
		t.Fatal(err)
	}
	frame = awaitNativeFrame(t, stream)
	t.Logf("restored WGC frame: %v", frame.Image.Bounds())
	for _, delta := range []int32{80, -120} {
		resizeOwnedNativeWindow(t, hwnd, pid, delta)
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_, captureErr = stream.Next(t.Context())
			if captureErr != nil && !errors.Is(captureErr, platform.ErrFramePending) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(captureErr, platform.ErrSourceResized) {
			t.Fatalf("resize delta=%d did not report specific resize failure: %v", delta, captureErr)
		}
		if stale, err := platform.OpenCapture(id); !errors.Is(err, platform.ErrInvalidSource) {
			if stale != nil {
				_ = stale.Close()
			}
			t.Fatalf("resized source token was not retired: %v", err)
		}
		id = selectOwnedNativeSource(t, title)
		stream, err = platform.OpenCapture(id)
		if err != nil {
			t.Fatal(err)
		}
		frame = awaitNativeFrame(t, stream)
		t.Logf("reselected after resize delta=%d: %v", delta, frame.Image.Bounds())
	}
	assertOwnedWindow(t, hwnd, pid)
	posted, _, postErr := user32.NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
	if posted == 0 {
		_ = stream.Close()
		t.Fatal(postErr)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, captureErr = stream.Next(t.Context())
		if captureErr != nil && !errors.Is(captureErr, platform.ErrFramePending) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closeErr = stream.Close()
	if captureErr == nil || errors.Is(captureErr, platform.ErrFramePending) {
		t.Errorf("closed source did not report a terminal error: %v", captureErr)
	}
	if closeErr != nil {
		t.Error(closeErr)
	}
}

func resizeOwnedNativeWindow(t *testing.T, hwnd uintptr, pid int, delta int32) {
	t.Helper()
	assertOwnedWindow(t, hwnd, pid)
	var rect struct{ left, top, right, bottom int32 }
	user32 := windows.NewLazySystemDLL("user32.dll")
	ok, _, err := user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect))) //nolint:gosec // Win32 writes one RECT to this live four-LONG struct.
	if ok == 0 {
		t.Fatal(err)
	}
	width, height := rect.right-rect.left+delta, rect.bottom-rect.top+delta
	if width < 100 || height < 100 || width > 4096 || height > 4096 {
		t.Fatal("invalid owned test window resize")
	}
	const flags = 0x0002 | 0x0004 | 0x0010                                                                  // SWP_NOMOVE | SWP_NOZORDER | SWP_NOACTIVATE
	ok, _, err = user32.NewProc("SetWindowPos").Call(hwnd, 0, 0, 0, uintptr(width), uintptr(height), flags) //nolint:gosec // Width and height are explicitly bounded positive test dimensions.
	if ok == 0 {
		t.Fatal(err)
	}
}

func assertOwnedWindow(t *testing.T, hwnd uintptr, pid int) {
	t.Helper()
	var owner uint32
	thread, _, err := windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner))) //nolint:gosec // Win32 writes exactly one DWORD to this live uint32.
	if hwnd == 0 || thread == 0 || uint64(owner) != uint64(pid) {                                                                            //nolint:gosec // Test subprocess PID is nonnegative.
		t.Fatalf("window is not owned by the test subprocess: handle=%x pid=%d error=%v", hwnd, owner, err)
	}
}

func selectOwnedNativeSource(t *testing.T, title string) uint32 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sources, err := platform.Sources()
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range sources {
			if source.Title == title || strings.HasSuffix(source.Title, " — "+title) {
				return source.ID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restored owned window not enumerated")
	return 0
}

func awaitNativeFrame(t *testing.T, stream platform.Stream) platform.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		frame, err := stream.Next(ctx)
		if err == nil {
			if frame.Image == nil || frame.CapturedAt.IsZero() {
				_ = stream.Close()
				t.Fatal("missing captured pixels or acquisition time")
			}
			return frame
		}
		if !errors.Is(err, platform.ErrFramePending) {
			_ = stream.Close()
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	_ = stream.Close()
	t.Fatal("no native frame within deadline")
	return platform.Frame{}
}
