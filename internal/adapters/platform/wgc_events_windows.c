//go:build windows && cgo

#include "wgc_abi_windows.h"
#include <stdlib.h>

static HRESULT STDMETHODCALLTYPE rd_event_query(rd_closed_interface *self, REFIID iid, void **out) {
    *out = NULL;
    if (!IsEqualIID(iid, &IID_IUnknown) && !IsEqualIID(iid, &IID_IAgileObject) && !IsEqualIID(iid, &IID___FITypedEventHandler_2_Windows__CGraphics__CCapture__CGraphicsCaptureItem_IInspectable)) return E_NOINTERFACE;
    *out = self;
    self->lpVtbl->AddRef(self);
    return S_OK;
}

static ULONG STDMETHODCALLTYPE rd_event_addref(rd_closed_interface *self) {
    return InterlockedIncrement(&((rd_closed_handler *)self)->refs);
}

static ULONG STDMETHODCALLTYPE rd_event_release(rd_closed_interface *self) {
    LONG refs = InterlockedDecrement(&((rd_closed_handler *)self)->refs);
    if (!refs) free(self);
    return refs;
}

static HRESULT STDMETHODCALLTYPE rd_event_invoke(rd_closed_interface *self, IGraphicsCaptureItem *sender, IInspectable *args) {
    (void)sender; (void)args;
    InterlockedExchange(&((rd_closed_handler *)self)->closed, TRUE);
    return S_OK;
}

static __FITypedEventHandler_2_Windows__CGraphics__CCapture__CGraphicsCaptureItem_IInspectableVtbl rd_event_vtbl = {
    rd_event_query, rd_event_addref, rd_event_release, rd_event_invoke
};

HRESULT rd_watch_closed(rd_capture *c) {
    c->handler = calloc(1, sizeof(*c->handler));
    if (!c->handler) return E_OUTOFMEMORY;
    c->handler->iface.lpVtbl = &rd_event_vtbl;
    c->handler->refs = 1;
    HRESULT hr = c->item->lpVtbl->add_Closed(c->item, &c->handler->iface, &c->closed_token);
    c->subscribed = SUCCEEDED(hr);
    return hr;
}

void rd_unwatch_closed(rd_capture *c) {
    if (c->subscribed) c->item->lpVtbl->remove_Closed(c->item, c->closed_token);
    if (c->handler) rd_event_release(&c->handler->iface);
    c->handler = NULL;
}

HRESULT rd_check_capture(rd_capture *c) {
    if (c->handler && InterlockedCompareExchange(&c->handler->closed, FALSE, FALSE)) return RD_E_CLOSED;
    return rd_validate((uintptr_t)c->hwnd, c->pid, c->tid);
}

int32_t rd_status(rd_capture *c) { return rd_check_capture(c); }
