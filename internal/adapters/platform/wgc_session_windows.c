//go:build windows && cgo

#include <initguid.h>
#include "wgc_abi_windows.h"
#include <stdlib.h>

const GUID rd_iid_dxgi_access = {0xa9b3d012, 0x3df2, 0x4ee3, {0xb8, 0xd1, 0x86, 0x95, 0xf4, 0x57, 0xd3, 0xc1}};

HRESULT rd_factory(const WCHAR *name, REFIID iid, void **out) {
    HSTRING str = NULL;
    HRESULT hr = WindowsCreateString(name, (UINT32)wcslen(name), &str);
    if (SUCCEEDED(hr)) hr = RoGetActivationFactory(str, iid, out);
    WindowsDeleteString(str);
    return hr;
}

HRESULT rd_close_object(void *object) {
    if (!object) return S_OK;
    IUnknown *unknown = object;
    IClosable *closable = NULL;
    HRESULT hr = IUnknown_QueryInterface(unknown, &IID_IClosable, (void **)&closable);
    if (SUCCEEDED(hr)) hr = closable->lpVtbl->Close(closable);
    RD_RELEASE(closable);
    return hr;
}

static HRESULT rd_supported(void) {
    IGraphicsCaptureSessionStatics *statics = NULL;
    boolean supported = FALSE;
    HRESULT hr = rd_factory(L"Windows.Graphics.Capture.GraphicsCaptureSession", &IID_IGraphicsCaptureSessionStatics, (void **)&statics);
    if (SUCCEEDED(hr)) hr = statics->lpVtbl->IsSupported(statics, &supported);
    RD_RELEASE(statics);
    return FAILED(hr) ? hr : supported ? S_OK : E_NOTIMPL;
}

static HRESULT rd_create_item(rd_capture *c) {
    IGraphicsCaptureItemInterop *interop = NULL;
    HRESULT hr = rd_factory(L"Windows.Graphics.Capture.GraphicsCaptureItem", &IID_IGraphicsCaptureItemInterop, (void **)&interop);
    if (SUCCEEDED(hr)) hr = interop->lpVtbl->CreateForWindow(interop, c->hwnd, &IID_IGraphicsCaptureItem, (void **)&c->item);
    RD_RELEASE(interop);
    if (SUCCEEDED(hr)) hr = c->item->lpVtbl->get_Size(c->item, &c->size);
    if (SUCCEEDED(hr) && (c->size.Width <= 0 || c->size.Height <= 0 || c->size.Width > RD_MAX_DIMENSION || c->size.Height > RD_MAX_DIMENSION || (INT64)c->size.Width * c->size.Height > RD_MAX_PIXELS)) hr = E_BOUNDS;
    if (SUCCEEDED(hr)) hr = rd_watch_closed(c);
    return hr;
}

static HRESULT rd_initialize(rd_capture *c) {
    HRESULT hr = RoInitialize(RO_INIT_MULTITHREADED);
    c->initialized = SUCCEEDED(hr);
    if (SUCCEEDED(hr)) hr = rd_check_capture(c);
    if (SUCCEEDED(hr)) hr = rd_supported();
    if (SUCCEEDED(hr)) hr = rd_create_item(c);
    if (SUCCEEDED(hr)) hr = rd_setup_device(c);
    if (SUCCEEDED(hr)) hr = rd_setup_pool(c);
    return hr;
}

int32_t rd_open(uintptr_t hwnd, uint32_t pid, uint32_t tid, rd_capture **out) {
    rd_capture *c = calloc(1, sizeof(*c));
    if (!c) return E_OUTOFMEMORY;
    c->hwnd = (HWND)hwnd; c->pid = pid; c->tid = tid;
    HRESULT hr = rd_initialize(c);
    // Ownership transfers even on failure, so teardown stays on the same worker.
    *out = c;
    return hr;
}

int32_t rd_close(rd_capture *c) {
    if (!c) return S_OK;
    HRESULT hr = rd_close_object(c->session);
    HRESULT pool_hr = rd_close_object(c->pool);
    rd_unwatch_closed(c);
    RD_RELEASE(c->session); RD_RELEASE(c->pool); RD_RELEASE(c->item);
    RD_RELEASE(c->staging); RD_RELEASE(c->winrt_device);
    RD_RELEASE(c->context); RD_RELEASE(c->device);
    if (c->initialized) RoUninitialize();
    free(c);
    return FAILED(hr) ? hr : pool_hr;
}
