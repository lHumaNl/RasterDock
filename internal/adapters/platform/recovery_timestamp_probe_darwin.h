//go:build integration && darwin && cgo

#ifndef RD_RECOVERY_TIMESTAMP_H
#define RD_RECOVERY_TIMESTAMP_H
#include "input_darwin.h"
#define RD_TIMESTAMP_SEQUENCE_NS 2000000000ULL
#define RD_TIMESTAMP_TOTAL_NS 10000000000ULL
#define RD_TIMESTAMP_CHILD_SECONDS 15
#define RD_TIMESTAMP_INITIAL_NS 2000000000ULL
typedef struct rd_timestamp_probe rd_timestamp_probe;
typedef struct { int seen, failed; double timestamp, cutoff; } rd_timestamp_observation;
// One own-window startup snapshot, not AX-window validity or posting authority.
typedef struct {
    int sampled, main_thread, visible, key_window, self_key, app_active;
    double uptime;
} rd_timestamp_readiness;
enum { RD_TIMESTAMP_INITIAL_PENDING, RD_TIMESTAMP_INITIAL_READY,
    RD_TIMESTAMP_INITIAL_TIMEOUT, RD_TIMESTAMP_INITIAL_UNAVAILABLE };
typedef struct { rd_timestamp_readiness readiness; int status; } rd_timestamp_initial_result;
rd_timestamp_initial_result rd_timestamp_initial_wait(void);
// Stable scalar-only phases. OWNER_PIN is composite; it does not identify which
// process/window predicate refused. No additional diagnostic queries are made.
typedef enum {
    RD_TIMESTAMP_OPEN_UNKNOWN = 0, RD_TIMESTAMP_OPEN_READY = 1,
    RD_TIMESTAMP_OPEN_FIXTURE = 2, RD_TIMESTAMP_OPEN_ALREADY_ATTEMPTED = 3,
    RD_TIMESTAMP_OPEN_ALLOCATION = 4, RD_TIMESTAMP_OPEN_ARGUMENT = 5,
    RD_TIMESTAMP_OPEN_OWNER_PIN = 6, RD_TIMESTAMP_OPEN_BIND = 7,
    RD_TIMESTAMP_OPEN_TARGET_COPY = 8, RD_TIMESTAMP_OPEN_TARGET_MISMATCH = 9
} rd_timestamp_open_phase;
typedef struct {
    rd_timestamp_open_phase phase;
    rd_input_diagnostic diagnostic;
} rd_timestamp_open_result;
rd_timestamp_probe *rd_timestamp_open(int pid, uint32_t window, const char *title, rd_timestamp_open_result *out);
int rd_timestamp_send(rd_timestamp_probe *probe, unsigned slot, rd_timestamp_post *out, rd_input_diagnostic *d);
void rd_timestamp_close(rd_timestamp_probe *probe);
uint32_t rd_timestamp_monitor(uintptr_t window, const char *title);
rd_timestamp_readiness rd_timestamp_readiness_snapshot(void);
int rd_timestamp_arm(unsigned slot);
rd_timestamp_observation rd_timestamp_observe(void);
#endif
