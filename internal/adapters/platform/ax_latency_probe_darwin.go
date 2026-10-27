//go:build integration && darwin && cgo

package platform

/*
#cgo LDFLAGS: -framework ApplicationServices
#include "ax_latency_probe_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const axLatencyWatchdog = 5 * time.Second

var axLatencyBusy atomic.Bool

// Stable diagnostic failures never include process/window metadata.
var (
	// ErrAXLatencyPermission means existing grants were not available; no prompt occurred.
	ErrAXLatencyPermission = errors.New("ax latency: existing permissions required")
	ErrAXLatencyIdentity   = errors.New("ax latency: owned child identity rejected")
	ErrAXLatencyDeadline   = errors.New("ax latency: native query budget exceeded")
	ErrAXLatencySetup      = errors.New("ax latency: messaging timeout setup failed")
	ErrAXLatencyBusy       = errors.New("ax latency: native batch already in flight")
	ErrAXLatencyWatchdog   = errors.New("ax latency: watchdog expired; OS call cannot be cancelled; no retry")
)

// AXLatencySample contains only allowlisted scalar diagnostics, never native metadata.
type AXLatencySample struct {
	Sequence, API, TimeoutMS, SetupAXError, QueryAXError int
	ValuePresent, IsArray                                bool
	Count                                                int64
	Elapsed, SetupCompletedAge                           time.Duration
}

// AXLatencyBatch has six schedule slots; only the first Completed have query results.
type AXLatencyBatch struct {
	Completed int
	Samples   [6]AXLatencySample
}

type axLatencyResult struct {
	batch AXLatencyBatch
	err   error
}

// ProbeAXLatency performs one fixed batch, without binding input or changing focus.
// Cancellation returns early but cannot cancel an OS call. Its sole worker retains
// all native resources and the package gate until that synchronous batch returns.
func ProbeAXLatency(ctx context.Context, childPID int, window uint32, prepared uint64) (AXLatencyBatch, error) {
	if err := ctx.Err(); err != nil {
		return AXLatencyBatch{}, err
	}
	if childPID <= 0 || int64(childPID) > int64(1<<31-1) || window == 0 {
		return AXLatencyBatch{}, ErrAXLatencyIdentity
	}
	if !axLatencyBusy.CompareAndSwap(false, true) {
		return AXLatencyBatch{}, ErrAXLatencyBusy
	}
	result := make(chan axLatencyResult, 1)
	go axLatencyWorker(childPID, window, prepared, result)
	return axLatencyWait(ctx, result)
}

func axLatencyWait(ctx context.Context, result <-chan axLatencyResult) (AXLatencyBatch, error) {
	watchdog := time.NewTimer(axLatencyWatchdog)
	defer watchdog.Stop()
	select {
	case value := <-result:
		return value.batch, value.err
	case <-ctx.Done():
		return AXLatencyBatch{}, ctx.Err()
	case <-watchdog.C:
		return AXLatencyBatch{}, ErrAXLatencyWatchdog
	}
}

func axLatencyWorker(pid int, window uint32, prepared uint64, result chan<- axLatencyResult) {
	defer axLatencyBusy.Store(false)
	var native C.rd_ax_batch // Pointer-free caller-owned output; never freed by the waiter.
	C.rd_ax_latency_batch(C.int(pid), C.uint32_t(window), C.uint64_t(prepared), &native)
	result <- axLatencyResult{axLatencyCopy(native), axLatencyError(int(native.status))}
}

func axLatencyError(status int) error {
	switch status {
	case C.RD_AX_OK:
		return nil
	case C.RD_AX_PERMISSION:
		return ErrAXLatencyPermission
	case C.RD_AX_IDENTITY:
		return ErrAXLatencyIdentity
	case C.RD_AX_DEADLINE:
		return ErrAXLatencyDeadline
	default:
		return ErrAXLatencySetup
	}
}

func axLatencyCopy(native C.rd_ax_batch) AXLatencyBatch {
	out := AXLatencyBatch{Completed: int(native.completed)}
	for i, sample := range native.samples {
		out.Samples[i] = AXLatencySample{
			Sequence: int(sample.sequence), API: int(sample.api), TimeoutMS: int(sample.timeout_ms),
			SetupAXError: int(sample.setup_error), QueryAXError: int(sample.query_error),
			ValuePresent: sample.value_present != 0, IsArray: sample.is_array != 0,
			Count: int64(sample.count), Elapsed: time.Duration(sample.elapsed_ns),
			SetupCompletedAge: time.Duration(sample.age_ns),
		}
	}
	return out
}

// AXLatencyPreparation marks setup completion with the native continuous clock.
// It performs no AX/CG query and does not describe actual GUI preparation time.
func AXLatencyPreparation() uint64 { return uint64(C.rd_ax_now()) }

func axLatencyBudgetExpired(elapsed uint64) bool {
	return C.rd_ax_budget_expired(C.uint64_t(elapsed)) != 0
}

func axLatencySchedule() AXLatencyBatch {
	var native C.rd_ax_batch
	C.rd_ax_schedule(&native) // Pure initialization: no AX, CG or process calls.
	return axLatencyCopy(native)
}
