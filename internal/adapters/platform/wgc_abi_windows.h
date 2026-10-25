#ifndef RD_WGC_ABI_H
#define RD_WGC_ABI_H
#define COBJMACROS
#define WIDL_using_Windows_Graphics_Capture
#define WIDL_using_Windows_Graphics_DirectX_Direct3D11
#define WIDL_using_Windows_Foundation
#include <windows.h>
#include <roapi.h>
#include <winstring.h>
#include <d3d11.h>
#include <d3d11_4.h>
#include <dxgi.h>
#include <windows.graphics.capture.h>
#include <windows.graphics.capture.interop.h>
#include <windows.graphics.directx.direct3d11.h>
#include "wgc_windows.h"

typedef __x_ABI_CWindows_CGraphics_CSizeInt32 rd_size;
typedef __x_ABI_CWindows_CFoundation_CTimeSpan rd_timespan;
typedef __FITypedEventHandler_2_Windows__CGraphics__CCapture__CGraphicsCaptureItem_IInspectable rd_closed_interface;

// Microsoft SDK windows.graphics.directx.direct3d11.interop.h, C ABI equivalent.
typedef struct rd_dxgi_access rd_dxgi_access;
typedef struct {
    HRESULT (STDMETHODCALLTYPE *QueryInterface)(rd_dxgi_access *, REFIID, void **);
    ULONG (STDMETHODCALLTYPE *AddRef)(rd_dxgi_access *);
    ULONG (STDMETHODCALLTYPE *Release)(rd_dxgi_access *);
    HRESULT (STDMETHODCALLTYPE *GetInterface)(rd_dxgi_access *, REFIID, void **);
} rd_dxgi_access_vtbl;
struct rd_dxgi_access { const rd_dxgi_access_vtbl *lpVtbl; };
extern const GUID rd_iid_dxgi_access;
HRESULT WINAPI CreateDirect3D11DeviceFromDXGIDevice(IDXGIDevice *, IInspectable **);

typedef struct { rd_closed_interface iface; LONG refs; LONG closed; } rd_closed_handler;
struct rd_capture {
    HWND hwnd;
    DWORD pid, tid;
    ID3D11Device *device;
    ID3D11DeviceContext *context;
    ID3D11Texture2D *staging;
    IDirect3DDevice *winrt_device;
    IGraphicsCaptureItem *item;
    IDirect3D11CaptureFramePool *pool;
    IGraphicsCaptureSession *session;
    rd_closed_handler *handler;
    EventRegistrationToken closed_token;
    rd_size size;
    INT64 last_time;
    BOOL initialized, subscribed;
    const char *operation;
};

#define RD_RELEASE(p) do { if (p) { (p)->lpVtbl->Release(p); (p) = NULL; } } while (0)
HRESULT rd_factory(const WCHAR *name, REFIID iid, void **out);
HRESULT rd_close_object(void *object);
HRESULT rd_watch_closed(rd_capture *capture);
void rd_unwatch_closed(rd_capture *capture);
HRESULT rd_setup_device(rd_capture *capture);
HRESULT rd_setup_pool(rd_capture *capture);
HRESULT rd_read_frame(rd_capture *capture, IDirect3D11CaptureFrame *frame, rd_frame *out);
HRESULT rd_check_capture(rd_capture *capture);
HRESULT rd_timestamp(INT64 stamp, rd_frame *out);
#endif
