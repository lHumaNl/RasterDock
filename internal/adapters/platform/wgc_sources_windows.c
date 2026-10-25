//go:build windows && cgo

#include <windows.h>
#include <dwmapi.h>
#include <stdlib.h>
#include "wgc_windows.h"

typedef struct { rd_source *items; size_t count; HRESULT error; } rd_enumeration;

int32_t rd_validate(uintptr_t handle, uint32_t pid, uint32_t tid) {
    HWND hwnd = (HWND)handle;
    DWORD actual_pid = 0, actual_tid = GetWindowThreadProcessId(hwnd, &actual_pid);
    if (!IsWindow(hwnd) || !actual_tid || actual_pid != pid || actual_tid != tid)
        return RD_E_REPLACED;
    if (IsIconic(hwnd)) return RD_E_MINIMIZED;
    if (!IsWindowVisible(hwnd)) return RD_E_HIDDEN;
    DWORD cloaked = 0;
    HRESULT hr = DwmGetWindowAttribute(hwnd, DWMWA_CLOAKED, &cloaked, sizeof(cloaked));
    if (FAILED(hr)) return hr;
    return cloaked ? RD_E_HIDDEN : S_OK;
}

static BOOL CALLBACK rd_enum_window(HWND hwnd, LPARAM param) {
    rd_enumeration *e = (rd_enumeration *)param;
    rd_source source = {0};
    source.hwnd = (uintptr_t)hwnd;
    source.tid = GetWindowThreadProcessId(hwnd, (DWORD *)&source.pid);
    // Enumeration intentionally includes minimized windows; opening one fails explicitly.
    DWORD cloaked = 0;
    if (!IsWindowVisible(hwnd) || FAILED(DwmGetWindowAttribute(hwnd, DWMWA_CLOAKED, &cloaked, sizeof(cloaked))) || cloaked) return TRUE;
    if (!GetWindowTextW(hwnd, (WCHAR *)source.title, RD_TITLE_CAPACITY)) return TRUE;
    rd_source *items = realloc(e->items, (e->count + 1) * sizeof(source));
    if (!items) { e->error = E_OUTOFMEMORY; return FALSE; }
    e->items = items;
    e->items[e->count++] = source;
    return TRUE;
}

int32_t rd_sources(rd_source **items, size_t *count) {
    rd_enumeration e = {0};
    SetLastError(ERROR_SUCCESS);
    if (!EnumWindows(rd_enum_window, (LPARAM)&e) && SUCCEEDED(e.error)) e.error = HRESULT_FROM_WIN32(GetLastError());
    if (FAILED(e.error)) { free(e.items); return e.error; }
    *items = e.items;
    *count = e.count;
    return S_OK;
}
