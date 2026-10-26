# Windows capture adapter

## Build toolchain

Windows CI explicitly selects the MSYS2 MINGW64 GCC/headers/CRT, rather than
the hosted runner's unrelated `C:/mingw64` installation. The setup action is
pinned; MSYS2 packages are updated together (not version-frozen), and their full
installed inventory participates in the Go build-cache fingerprint.
`bash scripts/check-windows-capture-sdk.sh` compiles and links the actual WGC
C sources before cache restoration. It does not run capture or require a desktop.
Locally, put MSYS2 `mingw64/bin` and `usr/bin` on PATH and set `CC` to that GCC.
Missing WinRT types/import libraries must fail this check, not disable capture.

The CGO adapter uses installed MinGW C ABI headers, Windows Graphics Capture
(`CreateForWindow`, `CreateFreeThreaded`) and hardware D3D11. There is no helper,
installation, elevation, desktop crop, PrintWindow fallback, or C++ runtime.
The OS capture border remains enabled. Cursor capture is disabled if the session
supports that property. The supported color profile is SDR BGRA8; HDR is not
tone-mapped and must not be treated as color-accurate SDR.

## Lifetime contract

- `Sources` enumerates visible, titled, noncloaked window metadata, never pixels.
- IDs are opaque process-local, nonreused tokens, not truncated HWND values.
  Full-width HWND, process and thread identity are checked before native capture,
  after readback, and after Go conversion. The WGC item is pinned and its Closed
  event invalidates an active session. Enumeration drops absent identities.
- `OpenCapture` returns before native initialization. Initialization, polling,
  D3D mapping and cleanup run on one locked OS thread. At most one native worker
  exists process-wide. No caller mutex is held across native work.
- `Next` waits at most 100 ms. `ErrFramePending` is transient. All other capture
  failures are terminal. One latest-frame mailbox preserves slow readbacks after
  a caller timeout without replaying a delivered frame or updating its timestamp.
  A closed/failed stream rejects queued pixels. This avoids discarding the only
  changed frame of a static window when race instrumentation slows conversion.
- `Close` signals cancellation and waits up to 500 ms. A hung driver cannot be
  safely interrupted. The worker retains its process-wide slot until cleanup
  finishes, even after `ErrCloseTimeout`; subsequent opens return
  `ErrCaptureBusy`, rather than starting replacement threads.
- Close the stream on terminal error or selection change. Do not treat a
  minimized, hidden, closed, replaced or resized source as a transient failure.
  Resize is rejected; the operator must select the window again.
- `Capture` is a testing convenience with a three-second first-frame deadline,
  followed by bounded cleanup. GUI code should keep a persistent `Stream`.

## Pixels and timestamps

The initial item size and each frame's physical-pixel ContentSize must match.
Texture dimensions, format, row pitch and allocation budget are validated.
Readback uses an owned staging texture, copies each RowPitch row into owned C
memory, then converts BGRA to an independent Go RGBA image. C memory and frame
interfaces are released on success and failure. CapturedAt maps WGC's acquisition
SystemRelativeTime (QPC in 100 ns units) onto Go's current clock using measured
frame age, not the moment the consumer retrieves the frame. The mapping has small
native-to-Go scheduling uncertainty and is not a security-grade clock guarantee.
Windows 11 runtime exposed a WGC timestamp 1.03 ms ahead of the sampled QPC.
Future skew up to 25 ms maps to local receipt (never a future CapturedAt);
larger skew, invalid values and duration overflow are explicit errors. This
passive capture timestamp must not be used as authorization for automated input.

## Identity and validation limits

Windows does not provide a permanent HWND generation identifier. Arbitrary
same-process/same-thread HWND reuse between enumeration and item creation cannot
be ruled out. The pinned item and Closed event prevent switching an existing
session to a newly created item, but do not remove that initial identity race.
The final validity check and delivery also cannot be atomic with external window
destruction/minimization. Protected content may be unavailable or black.

Unit tests use synthetic pixels and fake workers. Windows invalid-source tests
use only unknown tokens or a null HWND and never capture user windows. Live
occlusion, minimize, close, DPI, resize, border and pixel checks require the lead's
explicit UUID-owned viewer fixture; they are not implied by headless tests.
Race detection covers Go state, not native driver internals.

## SDK references

- https://learn.microsoft.com/en-us/windows/uwp/audio-video-camera/screen-capture
- https://learn.microsoft.com/en-us/uwp/api/windows.graphics.capture.direct3d11captureframepool.createfreethreaded
- https://learn.microsoft.com/en-us/windows/win32/api/windows.graphics.capture.interop/nf-windows-graphics-capture-interop-igraphicscaptureiteminterop-createforwindow
- https://raw.githubusercontent.com/microsoft/win32metadata/main/generation/WinSDK/RecompiledIdlHeaders/um/windows.graphics.directx.direct3d11.interop.h

The final reference supplies the small missing DXGI interface-access declaration;
all other WinRT and D3D interfaces come from installed MinGW headers. Context7
was queried, but its Windows App SDK corpus had no matching WGC documentation;
Microsoft's primary documentation and SDK header were consulted directly.
