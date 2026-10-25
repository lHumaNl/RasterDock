//go:build windows && cgo

#include "wgc_abi_windows.h"

enum { RD_FRAME_BUFFERS = 2 };

static HRESULT rd_protect_context(rd_capture *c) {
    ID3D11Multithread *multithread = NULL;
    HRESULT hr = ID3D11DeviceContext_QueryInterface(c->context, &IID_ID3D11Multithread, (void **)&multithread);
    // WGC uses the device asynchronously; protect the immediate context too.
    if (SUCCEEDED(hr)) multithread->lpVtbl->SetMultithreadProtected(multithread, TRUE);
    RD_RELEASE(multithread);
    return hr;
}

HRESULT rd_setup_device(rd_capture *c) {
    IDXGIDevice *dxgi = NULL;
    IInspectable *inspectable = NULL;
    HRESULT hr = D3D11CreateDevice(NULL, D3D_DRIVER_TYPE_HARDWARE, NULL, D3D11_CREATE_DEVICE_BGRA_SUPPORT, NULL, 0, D3D11_SDK_VERSION, &c->device, NULL, &c->context);
    if (SUCCEEDED(hr)) hr = rd_protect_context(c);
    if (SUCCEEDED(hr)) hr = ID3D11Device_QueryInterface(c->device, &IID_IDXGIDevice, (void **)&dxgi);
    if (SUCCEEDED(hr)) hr = CreateDirect3D11DeviceFromDXGIDevice(dxgi, &inspectable);
    if (SUCCEEDED(hr)) hr = IInspectable_QueryInterface(inspectable, &IID_IDirect3DDevice, (void **)&c->winrt_device);
    RD_RELEASE(inspectable); RD_RELEASE(dxgi);
    return hr;
}

static HRESULT rd_start_session(rd_capture *c) {
    IGraphicsCaptureSession2 *cursor = NULL;
    HRESULT hr = c->pool->lpVtbl->CreateCaptureSession(c->pool, c->item, &c->session);
    if (FAILED(hr)) return hr;
    hr = c->session->lpVtbl->QueryInterface(c->session, &IID_IGraphicsCaptureSession2, (void **)&cursor);
    if (SUCCEEDED(hr)) hr = cursor->lpVtbl->put_IsCursorCaptureEnabled(cursor, FALSE);
    else if (hr == E_NOINTERFACE) hr = S_OK;
    RD_RELEASE(cursor);
    // Do not request borderless access or change IsBorderRequired: retain OS border.
    if (SUCCEEDED(hr)) hr = c->session->lpVtbl->StartCapture(c->session);
    return hr;
}

HRESULT rd_setup_pool(rd_capture *c) {
    IDirect3D11CaptureFramePoolStatics2 *statics = NULL;
    HRESULT hr = rd_factory(L"Windows.Graphics.Capture.Direct3D11CaptureFramePool", &IID_IDirect3D11CaptureFramePoolStatics2, (void **)&statics);
    if (SUCCEEDED(hr)) hr = statics->lpVtbl->CreateFreeThreaded(statics, c->winrt_device, (__x_ABI_CWindows_CGraphics_CDirectX_CDirectXPixelFormat)DXGI_FORMAT_B8G8R8A8_UNORM, RD_FRAME_BUFFERS, c->size, &c->pool);
    RD_RELEASE(statics);
    if (SUCCEEDED(hr)) hr = rd_start_session(c);
    return hr;
}

static HRESULT rd_latest_frame(rd_capture *c, IDirect3D11CaptureFrame **out) {
    for (int i = 0; i < RD_FRAME_BUFFERS; i++) {
        IDirect3D11CaptureFrame *next = NULL;
        HRESULT hr = c->pool->lpVtbl->TryGetNextFrame(c->pool, &next);
        if (FAILED(hr)) { rd_close_object(next); RD_RELEASE(next); return hr; }
        if (!next) break;
        rd_close_object(*out); RD_RELEASE(*out);
        *out = next;
    }
    return *out ? S_OK : S_FALSE;
}

int32_t rd_next(rd_capture *c, rd_frame *out) {
    IDirect3D11CaptureFrame *frame = NULL;
    c->operation = "validate source";
    HRESULT hr = rd_check_capture(c);
    if (SUCCEEDED(hr)) { c->operation = "poll frame pool"; hr = rd_latest_frame(c, &frame); }
    if (hr == S_OK) hr = rd_read_frame(c, frame, out);
    HRESULT close_hr = rd_close_object(frame);
    RD_RELEASE(frame);
    if (SUCCEEDED(hr) && FAILED(close_hr)) hr = close_hr;
    if (SUCCEEDED(hr)) { HRESULT valid = rd_check_capture(c); if (FAILED(valid)) hr = valid; }
    if (hr == S_OK) { c->operation = "map acquisition timestamp"; hr = rd_timestamp(c->last_time, out); }
    return hr;
}

const char *rd_operation(rd_capture *c) { return c->operation ? c->operation : "capture"; }
