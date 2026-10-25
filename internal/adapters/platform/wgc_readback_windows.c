//go:build windows && cgo

#include "wgc_abi_windows.h"
#include <stdlib.h>
#include <string.h>

static HRESULT rd_texture(IDirect3D11CaptureFrame *frame, ID3D11Texture2D **texture) {
    IDirect3DSurface *surface = NULL;
    rd_dxgi_access *access = NULL;
    HRESULT hr = frame->lpVtbl->get_Surface(frame, &surface);
    if (SUCCEEDED(hr)) hr = surface->lpVtbl->QueryInterface(surface, &rd_iid_dxgi_access, (void **)&access);
    if (SUCCEEDED(hr)) hr = access->lpVtbl->GetInterface(access, &IID_ID3D11Texture2D, (void **)texture);
    RD_RELEASE(access); RD_RELEASE(surface);
    return hr;
}

static HRESULT rd_staging(rd_capture *c, ID3D11Texture2D *texture) {
    D3D11_TEXTURE2D_DESC desc;
    ID3D11Texture2D_GetDesc(texture, &desc);
    if (desc.Width != (UINT)c->size.Width || desc.Height != (UINT)c->size.Height || desc.Format != DXGI_FORMAT_B8G8R8A8_UNORM || desc.SampleDesc.Count != 1 || desc.ArraySize != 1 || desc.MipLevels != 1) return E_BOUNDS;
    if (c->staging) return S_OK;
    desc.Usage = D3D11_USAGE_STAGING;
    desc.BindFlags = 0; desc.MiscFlags = 0;
    desc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
    return ID3D11Device_CreateTexture2D(c->device, &desc, NULL, &c->staging);
}

static HRESULT rd_copy_mapped(rd_capture *c, D3D11_MAPPED_SUBRESOURCE *mapped, rd_frame *out) {
    size_t pitch = (size_t)c->size.Width * 4;
    if (mapped->RowPitch < pitch) return E_BOUNDS;
    out->pixels = malloc(pitch * c->size.Height);
    if (!out->pixels) return E_OUTOFMEMORY;
    out->width = c->size.Width; out->height = c->size.Height; out->pitch = (int)pitch;
    for (int y = 0; y < out->height; y++) memcpy(out->pixels + y * pitch, (unsigned char *)mapped->pData + (size_t)y * mapped->RowPitch, pitch);
    return S_OK;
}

static HRESULT rd_read_texture(rd_capture *c, ID3D11Texture2D *texture, rd_frame *out) {
    c->operation = "prepare staging texture";
    HRESULT hr = rd_staging(c, texture);
    if (FAILED(hr)) return hr;
    ID3D11DeviceContext_CopyResource(c->context, (ID3D11Resource *)c->staging, (ID3D11Resource *)texture);
    D3D11_MAPPED_SUBRESOURCE mapped = {0};
    c->operation = "map staging texture";
    hr = ID3D11DeviceContext_Map(c->context, (ID3D11Resource *)c->staging, 0, D3D11_MAP_READ, 0, &mapped);
    if (FAILED(hr)) return hr;
    hr = rd_copy_mapped(c, &mapped, out);
    ID3D11DeviceContext_Unmap(c->context, (ID3D11Resource *)c->staging, 0);
    return hr;
}

static HRESULT rd_frame_metadata(rd_capture *c, IDirect3D11CaptureFrame *frame, rd_timespan *stamp) {
    rd_size size;
    HRESULT hr = frame->lpVtbl->get_ContentSize(frame, &size);
    // Reject resize rather than exposing clipped or undefined pixels.
    if (SUCCEEDED(hr) && (size.Width != c->size.Width || size.Height != c->size.Height)) return RD_E_RESIZED;
    if (SUCCEEDED(hr)) hr = frame->lpVtbl->get_SystemRelativeTime(frame, stamp);
    if (SUCCEEDED(hr) && stamp->Duration <= c->last_time) return S_FALSE;
    return hr;
}

HRESULT rd_timestamp(INT64 stamp, rd_frame *out) {
    LARGE_INTEGER counter, frequency;
    if (!QueryPerformanceCounter(&counter) || !QueryPerformanceFrequency(&frequency)) return E_FAIL;
    // Avoid multiplying uptime by a large integer before division.
    INT64 now = (counter.QuadPart / frequency.QuadPart) * 10000000 + (counter.QuadPart % frequency.QuadPart) * 10000000 / frequency.QuadPart;
    out->source_time_100ns = stamp;
    out->observed_time_100ns = now;
    return S_OK;
}

HRESULT rd_read_frame(rd_capture *c, IDirect3D11CaptureFrame *frame, rd_frame *out) {
    rd_timespan stamp = {0};
    ID3D11Texture2D *texture = NULL;
    c->operation = "read frame metadata";
    HRESULT hr = rd_frame_metadata(c, frame, &stamp);
    if (hr != S_OK) return hr;
    c->operation = "resolve frame texture";
    hr = rd_texture(frame, &texture);
    if (SUCCEEDED(hr)) hr = rd_read_texture(c, texture, out);
    RD_RELEASE(texture);
    if (SUCCEEDED(hr)) c->last_time = stamp.Duration;
    return hr;
}
