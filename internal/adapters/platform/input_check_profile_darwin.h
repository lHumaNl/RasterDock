//go:build integration && darwin && cgo

#ifndef RD_INPUT_CHECK_PROFILE_H
#define RD_INPUT_CHECK_PROFILE_H
#include "input_darwin.h"
#define RD_PROFILE_SAMPLES 12
#define RD_PROFILE_BUDGET_NS 3000000000ULL
enum { RD_PROFILE_OK, RD_PROFILE_IDENTITY, RD_PROFILE_BIND, RD_PROFILE_DEADLINE,
    RD_PROFILE_SETUP, RD_PROFILE_OVERFLOW };
typedef struct { rd_input_timing timing; rd_input_diagnostic diagnostic; } rd_profile_sample;
typedef struct {
    int status, completed;
    rd_input_diagnostic bind_diagnostic;
    rd_profile_sample samples[RD_PROFILE_SAMPLES];
} rd_profile_batch;
int rd_profile_stop(rd_input_diagnostic diagnostic, int overflow);
int rd_profile_can_start(unsigned index, uint64_t elapsed);
void rd_profile_run(int pid, uint32_t window, rd_profile_batch *out);
int rd_profile_owner(int pid, uint32_t window, uint64_t *started);
int rd_profile_alive(int pid, uint64_t started);
int rd_profile_check(rd_input *binding, rd_profile_sample *out);
#endif
