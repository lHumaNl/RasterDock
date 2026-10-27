#ifndef RD_INPUT_DARWIN_H
#define RD_INPUT_DARWIN_H
#include <stdint.h>
typedef struct rd_input rd_input;
typedef enum {
    RD_STAGE_UNKNOWN, RD_STAGE_BIND_SELECTION, RD_STAGE_CG_UNIQUE,
    RD_STAGE_AX_WINDOWS, RD_STAGE_AX_TITLE, RD_STAGE_FOCUSED_WINDOW,
    RD_STAGE_IDENTITY, RD_STAGE_LOCAL_FIRST, RD_STAGE_LOCAL_SECOND,
    RD_STAGE_PREPARE, RD_STAGE_PRE_POST, RD_STAGE_TARGET
} rd_input_stage;
typedef enum {
    RD_CODE_UNKNOWN, RD_CODE_PERMISSION, RD_CODE_BUDGET, RD_CODE_ALLOCATION,
    RD_CODE_CG_LIST_LIMIT, RD_CODE_MISSING, RD_CODE_TITLE, RD_CODE_NOT_UNIQUE,
    RD_CODE_VISIBILITY, RD_CODE_PROCESS_START_CHANGED, RD_CODE_AX_TIMEOUT_SETUP,
    RD_CODE_QUERY, RD_CODE_NULL, RD_CODE_TYPE, RD_CODE_WINDOW_LIMIT,
    RD_CODE_FOCUS_IDENTITY, RD_CODE_FRONTMOST, RD_CODE_MODIFIERS,
    RD_CODE_UTF8, RD_CODE_TARGET_INVALID
} rd_input_code;
// Zero-initialized, caller-owned, per call. First failure wins, including cleanup.
typedef struct {
    rd_input_stage stage;
    rd_input_code code;
    int has_ax_error;
    int ax_error;
} rd_input_diagnostic;
// Opt-in, thread-local attachment to caller-owned scalar storage. No OS queries.
#define RD_INPUT_TIMING_CAPACITY 32
typedef enum {
    RD_TIMING_TIMEOUT = 1, RD_TIMING_AX_QUERY, RD_TIMING_CG_LIST,
    RD_TIMING_CAPABILITY, RD_TIMING_LOCAL
} rd_input_timing_op;
typedef struct {
    rd_input_stage stage;
    rd_input_timing_op op;
    uint64_t offset_ns, elapsed_ns, remaining_ns;
    int has_ax_error, ax_error;
} rd_input_timing_record;
typedef struct {
    uint64_t start_ns, elapsed_ns;
    unsigned count;
    int overflow, ok;
    rd_input_timing_record records[RD_INPUT_TIMING_CAPACITY];
} rd_input_timing;
void rd_input_timing_attach(rd_input_timing *buffer);
void rd_input_timing_append(rd_input_timing *buffer, rd_input_stage stage,
    rd_input_timing_op op, uint64_t before, uint64_t after, uint64_t deadline,
    int has_ax_error, int ax_error);
int rd_input_ax_result(rd_input_diagnostic *d, rd_input_stage stage,
    int timely, int ax_error, rd_input_code result_code);
typedef struct {
    uint32_t window;
    int pid;
    uint64_t started;
    char *title;
    int title_length;
} rd_input_target;
int rd_input_copy_target(const rd_input *binding, rd_input_target *target, rd_input_diagnostic *d);
#define RD_INPUT_CHECK_BUDGET_NS 100000000ULL
#define RD_INPUT_BIND_BUDGET_NS 500000000ULL
uint64_t rd_input_deadline(uint64_t budget_ns);
uint64_t rd_input_bind_deadline(uint64_t now);
uint64_t rd_input_ax_allowance(uint64_t now, uint64_t deadline);
int rd_input_capability(void);
rd_input *rd_input_bind(uint32_t window, rd_input_diagnostic *d);
int rd_input_check(rd_input *binding, rd_input_diagnostic *d);
int rd_input_prepare(rd_input *binding, uint64_t deadline, rd_input_diagnostic *d);
int rd_input_key(rd_input *binding, uint16_t key, rd_input_diagnostic *d);
#ifdef RD_RECOVERY_TIMESTAMP_PROBE
// Integration-only fixed baseline factory; never accepts a PID, key, or raw event.
#define RD_TIMESTAMP_BASELINE_POSTS 6
#define RD_TIMESTAMP_KEY 18
typedef struct { uint64_t creation, prepost; } rd_timestamp_post;
int rd_input_timestamp_post(rd_input *binding, unsigned slot, rd_timestamp_post *out, rd_input_diagnostic *d);
#endif
void rd_input_close(rd_input *binding);
typedef struct {
    int focused;
    uint64_t generation;
} rd_input_focus_state;
rd_input_focus_state rd_input_own_focus_state(const char *title);
#endif
