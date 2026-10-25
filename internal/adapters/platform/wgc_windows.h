#ifndef RD_WGC_H
#define RD_WGC_H
#include <stdint.h>
#include <stddef.h>

enum { RD_TITLE_CAPACITY = 512, RD_MAX_DIMENSION = 16384, RD_MAX_PIXELS = 64 << 20 };
// Private FACILITY_ITF failures distinguish terminal source conditions.
#define RD_E_MINIMIZED ((int32_t)0x80040201)
#define RD_E_RESIZED ((int32_t)0x80040202)
#define RD_E_CLOSED ((int32_t)0x80040203)
#define RD_E_REPLACED ((int32_t)0x80040204)
#define RD_E_HIDDEN ((int32_t)0x80040205)
typedef struct { uintptr_t hwnd; uint32_t pid, tid; uint16_t title[RD_TITLE_CAPACITY]; } rd_source;
typedef struct rd_capture rd_capture;
typedef struct { unsigned char *pixels; int width, height, pitch; int64_t source_time_100ns, observed_time_100ns; } rd_frame;
int32_t rd_sources(rd_source **items, size_t *count);
int32_t rd_validate(uintptr_t hwnd, uint32_t pid, uint32_t tid);
int32_t rd_open(uintptr_t hwnd, uint32_t pid, uint32_t tid, rd_capture **out);
int32_t rd_next(rd_capture *capture, rd_frame *out);
int32_t rd_status(rd_capture *capture);
const char *rd_operation(rd_capture *capture);
int32_t rd_close(rd_capture *capture);
#endif
