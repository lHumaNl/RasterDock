#ifndef RD_CONTROL_CAPTURE_H
#define RD_CONTROL_CAPTURE_H
#include <stdint.h>
#include <stddef.h>

enum { RDControlPending, RDControlReady, RDControlInvalid, RDControlResized, RDControlClosed };
typedef struct { uint64_t absolute0, continuous, absolute1; uint32_t numerator, denominator; } RDControlTicks;
typedef struct { unsigned char *pixels; size_t width, height; uint64_t display; } RDControlFrame;
// Getter returns an owned title copy; caller frees it. Native identity never mutates.
typedef struct { uint32_t window; int32_t pid; uint64_t started; char *title; } RDControlTarget;
uint64_t rd_control_process_start(int32_t pid);
int rd_control_identity_matches(RDControlTarget target);
int rd_control_target(void *handle, RDControlTarget *target);
void rd_control_validate(void *handle);
int rd_control_validation_state(void *handle);
int rd_control_capability(void);
void *rd_control_open(uint32_t window);
int rd_control_state(void *handle);
int rd_control_take(void *handle, RDControlFrame *frame);
void rd_control_revoke(void *handle);
int rd_control_done(void *handle);
void rd_control_release(void *handle);
RDControlTicks rd_control_ticks(void);
#endif
