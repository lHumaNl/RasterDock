//go:build integration && darwin && cgo

#ifndef RD_AX_LATENCY_PROBE_H
#define RD_AX_LATENCY_PROBE_H
#include <stdint.h>
enum { RD_AX_QUERIES = 6, RD_AX_MAX_VALUES = 17, RD_AX_RANGED = 1, RD_AX_SINGLE = 2 };
enum { RD_AX_OK, RD_AX_PERMISSION, RD_AX_IDENTITY, RD_AX_DEADLINE, RD_AX_SETUP };
typedef struct {
    int sequence, api, timeout_ms, setup_error, query_error;
    int value_present, is_array;
    int64_t count;
    uint64_t elapsed_ns, age_ns;
} rd_ax_sample;
typedef struct { int status, completed; rd_ax_sample samples[RD_AX_QUERIES]; } rd_ax_batch;
void rd_ax_schedule(rd_ax_batch *out);
uint64_t rd_ax_now(void);
int rd_ax_budget_expired(uint64_t elapsed);
void rd_ax_latency_batch(int pid, uint32_t window, uint64_t prepared, rd_ax_batch *out);
#endif
