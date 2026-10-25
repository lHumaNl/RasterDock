package fyneui

import (
	"fmt"
	"image"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

// Start small until Fyne has detected the native framebuffer scale. Creating an
// oversized window first can leave Fyne 2.8 with stale logical/GL viewport sizes
// after Windows silently caps the request.
func initialWindowSize(fyne.Size) fyne.Size { return fyne.NewSize(320, 200) }

type nativeWindowRect struct{ left, top, right, bottom int32 }

// Expand only within this window's monitor work area after Show, when physical
// scale and non-client decoration sizes are available. No system DPI is changed.
func reconcileInitialWindowSize(w fyne.Window, desired fyne.Size) error {
	native, ok := w.(driver.NativeWindow)
	if !ok {
		return nil // Headless/non-native Fyne driver has no Windows geometry.
	}
	var result error
	native.RunNative(func(ctx any) {
		window, ok := ctx.(driver.WindowsWindowContext)
		if !ok || window.HWND == 0 {
			result = fmt.Errorf("native window geometry unavailable")
			return
		}
		user32 := windows.NewLazySystemDLL("user32.dll")
		var client, outer nativeWindowRect
		success, _, err := user32.NewProc("GetClientRect").Call(window.HWND, uintptr(unsafe.Pointer(&client))) //nolint:gosec // Win32 writes a RECT into this live four-LONG struct for our own Fyne window.
		if success == 0 {
			result = fmt.Errorf("query native client size: %w", err)
			return
		}
		success, _, err = user32.NewProc("GetWindowRect").Call(window.HWND, uintptr(unsafe.Pointer(&outer))) //nolint:gosec // Live native RECT output, same owned HWND.
		if success == 0 {
			result = fmt.Errorf("query native outer size: %w", err)
			return
		}
		monitor, _, err := user32.NewProc("MonitorFromWindow").Call(window.HWND, 2) // MONITOR_DEFAULTTONEAREST
		if monitor == 0 {
			result = fmt.Errorf("query window monitor: %w", err)
			return
		}
		var info struct {
			size          uint32
			monitor, work nativeWindowRect
			flags         uint32
		}
		info.size = uint32(unsafe.Sizeof(info))
		success, _, err = user32.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info))) //nolint:gosec // MONITORINFO cbSize matches this live output buffer.
		if success == 0 {
			result = fmt.Errorf("query monitor work area: %w", err)
			return
		}
		width := info.work.right - info.work.left - (outer.right - outer.left - client.right + client.left)
		height := info.work.bottom - info.work.top - (outer.bottom - outer.top - client.bottom + client.top)
		if width <= 0 || height <= 0 {
			result = fmt.Errorf("native window has an empty client area")
			return
		}
		available, _ := physicalImageGeometry(image.Rect(0, 0, int(width), int(height)), fyne.Size{}, w.Canvas().PixelCoordinateForPosition)
		size := fyne.NewSize(min(desired.Width, available.Width), min(desired.Height, available.Height))
		if w.Canvas().Size() != size {
			w.Resize(size)
		}
	})
	return result
}
