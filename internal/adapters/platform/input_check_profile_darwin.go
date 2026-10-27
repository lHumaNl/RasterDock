//go:build integration && darwin && cgo

package platform

/*
#include "input_check_profile_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const checkProfileWatchdog = 5 * time.Second

var checkProfileBusy atomic.Bool

var (
	// ErrCheckProfileBusy prevents overlapping native batches, including after timeout.
	ErrCheckProfileBusy = errors.New("check profile: native worker still in flight")
	// ErrCheckProfileWatchdog leaves the native worker and its ownership gate alive.
	ErrCheckProfileWatchdog = errors.New("check profile: watchdog expired; OS call cannot be cancelled; no retry")
)

// CheckProfileError contains stable native enum values, never target metadata.
type CheckProfileError struct{ Status int }

func (e CheckProfileError) Error() string {
	return fmt.Sprintf("check profile: native stop=%d", e.Status)
}

// CheckProfileDiagnostic preserves the production first-failure classification.
type CheckProfileDiagnostic struct {
	Stage, Code, AXError int
	HasAXError           bool
}

// CheckProfileRecord contains disjoint measured chunks. Remaining is sampled
// before the operation, not the timeout passed to AX or a completion guarantee.
type CheckProfileRecord struct {
	Stage, Operation, AXError  int
	HasAXError                 bool
	Offset, Elapsed, Remaining time.Duration
}

// CheckProfileSample reports one actual Check, including uninstrumented work.
type CheckProfileSample struct {
	CheckStarted          time.Time
	CheckElapsed          time.Duration
	OK, Overflow          bool
	Elapsed, Unattributed time.Duration
	Diagnostic            CheckProfileDiagnostic
	Records               []CheckProfileRecord
}

// CheckProfileBatch contains only completed calls; a refused Bind has no samples.
type CheckProfileBatch struct {
	BindDiagnostic CheckProfileDiagnostic
	Samples        []CheckProfileSample
}

type checkProfileResult struct {
	batch CheckProfileBatch
	err   error
}

// ProbeInputCheckProfile owns one private native binding, never an InputBinding.
// Twelve sequential observations, no gap/retry/refocus; cold/warm is sequence
// only, not a comparative guarantee. Cancellation never frees in-flight storage.
func ProbeInputCheckProfile(ctx context.Context, childPID int, window uint32) (CheckProfileBatch, error) {
	if err := ctx.Err(); err != nil {
		return CheckProfileBatch{}, err
	}
	if childPID <= 0 || int64(childPID) > 1<<31-1 || window == 0 {
		return CheckProfileBatch{}, CheckProfileError{int(C.RD_PROFILE_IDENTITY)}
	}
	if !checkProfileBusy.CompareAndSwap(false, true) {
		return CheckProfileBatch{}, ErrCheckProfileBusy
	}
	result := make(chan checkProfileResult, 1)
	go checkProfileWorker(childPID, window, result)
	return checkProfileWait(ctx, result)
}

func checkProfileWait(ctx context.Context, result <-chan checkProfileResult) (CheckProfileBatch, error) {
	timer := time.NewTimer(checkProfileWatchdog)
	defer timer.Stop()
	select {
	case value := <-result:
		return value.batch, value.err
	case <-ctx.Done():
		return CheckProfileBatch{}, ctx.Err()
	case <-timer.C:
		return CheckProfileBatch{}, ErrCheckProfileWatchdog
	}
}

func checkProfileWorker(pid int, window uint32, result chan<- checkProfileResult) {
	defer checkProfileBusy.Store(false)
	var native C.rd_profile_batch
	C.rd_profile_run(C.int(pid), C.uint32_t(window), &native)
	var err error
	if native.status != C.RD_PROFILE_OK {
		err = CheckProfileError{int(native.status)}
	}
	result <- checkProfileResult{checkProfileCopy(native), err}
}

func checkProfileDiagnostic(d C.rd_input_diagnostic) CheckProfileDiagnostic {
	return CheckProfileDiagnostic{int(d.stage), int(d.code), int(d.ax_error), d.has_ax_error != 0}
}

func checkProfileCopy(native C.rd_profile_batch) CheckProfileBatch {
	out := CheckProfileBatch{BindDiagnostic: checkProfileDiagnostic(native.bind_diagnostic)}
	for i := 0; i < int(native.completed); i++ {
		out.Samples = append(out.Samples, checkProfileSampleCopy(native.samples[i]))
	}
	return out
}

func checkProfileSampleCopy(sample C.rd_profile_sample) CheckProfileSample {
	value := CheckProfileSample{OK: sample.timing.ok != 0, Overflow: sample.timing.overflow != 0,
		Elapsed: time.Duration(sample.timing.elapsed_ns), Diagnostic: checkProfileDiagnostic(sample.diagnostic)}
	value.Unattributed = value.Elapsed
	for j := 0; j < int(sample.timing.count); j++ {
		r := sample.timing.records[j]
		value.Records = append(value.Records, CheckProfileRecord{int(r.stage), int(r.op), int(r.ax_error),
			r.has_ax_error != 0, time.Duration(r.offset_ns), time.Duration(r.elapsed_ns), time.Duration(r.remaining_ns)})
		value.Unattributed -= time.Duration(r.elapsed_ns)
	}
	return value
}

// Pure fixture entry points exercise the actual C buffer/classifier without OS calls.
func checkProfileFixture(count int) CheckProfileSample {
	var native C.rd_profile_batch
	native.completed = 1
	s := &native.samples[0]
	s.timing.start_ns = 100
	s.timing.elapsed_ns = 100
	for i := 0; i < count; i++ {
		C.rd_input_timing_append(&s.timing, C.RD_STAGE_AX_TITLE, C.RD_TIMING_AX_QUERY,
			C.uint64_t(100+i), C.uint64_t(101+i), 110, 1, -25204)
	}
	return checkProfileCopy(native).Samples[0]
}

func checkProfileStop(code int, overflow bool) int {
	d := C.rd_input_diagnostic{code: C.rd_input_code(code)}
	var full C.int
	if overflow {
		full = 1
	}
	return int(C.rd_profile_stop(d, full))
}

func checkProfileCanStart(index uint, elapsed time.Duration) bool {
	return C.rd_profile_can_start(C.uint(index), C.uint64_t(elapsed)) != 0
}

// Held profile limits bound observations, not noncancellable native OS calls.
const (
	HeldProfileLimit   = 12
	HeldProfileTimeout = 10 * time.Second
)

// ErrHeldProfileLimit means no held completion was observed within the sample cap.
var ErrHeldProfileLimit = errors.New("held profile: sample cap reached without completion")

type heldProfileInput interface {
	Target() ControlTarget
	Check() error
	Close() error
}

// HeldCheckProfile deliberately has no Send method and never publishes its input.
// All calls are synchronous: cancellation cannot cancel an OS call or release its
// owner early. The test process timeout is the final bound on a stuck OS call.
type HeldCheckProfile struct {
	mu       sync.Mutex
	input    heldProfileInput
	guard    func() error
	attempts int
	samples  []CheckProfileSample
}

// BindHeldCheckProfile uses one production construction and validates an owned
// direct child running this executable. No private native binding or warmup.
func BindHeldCheckProfile(childPID int, window uint32) (*HeldCheckProfile, error) {
	var started C.uint64_t
	if childPID <= 0 || int64(childPID) > 1<<31-1 || window == 0 || C.rd_profile_owner(C.int(childPID), C.uint32_t(window), &started) == 0 {
		return nil, CheckProfileError{int(C.RD_PROFILE_IDENTITY)}
	}
	p := &HeldCheckProfile{samples: make([]CheckProfileSample, 0, HeldProfileLimit)}
	p.guard = func() error {
		if C.rd_profile_alive(C.int(childPID), started) == 0 {
			return CheckProfileError{int(C.RD_PROFILE_IDENTITY)}
		}
		return nil
	}
	return p.bind(window, childPID, uint64(started))
}

func (p *HeldCheckProfile) bind(window uint32, pid int, started uint64) (*HeldCheckProfile, error) {
	b, err := bindDarwinInput(window, p.checkFactory)
	if err != nil {
		return nil, err
	}
	target := b.Target()
	if !target.Valid() || target.Window != window || target.PID != pid || target.Started != started {
		return nil, errors.Join(CheckProfileError{int(C.RD_PROFILE_IDENTITY)}, b.Close())
	}
	p.input = b
	return p, nil
}

func (p *HeldCheckProfile) checkFactory(binding *C.rd_input) func() error {
	return func() error {
		var native C.rd_profile_sample
		ok := C.rd_profile_check(binding, &native)
		p.samples = append(p.samples, checkProfileSampleCopy(native))
		if ok != 0 && native.timing.overflow != 0 {
			return CheckProfileError{int(C.RD_PROFILE_OVERFLOW)}
		}
		return inputNativeResult(ok, native.diagnostic)
	}
}

// Target is the immutable production binding snapshot, never native storage.
func (p *HeldCheckProfile) Target() ControlTarget { return p.input.Target() }

// Check invokes the original serialized, fail-stop Check on this binding.
func (p *HeldCheckProfile) Check() error { return p.check(context.Background()) }

func (p *HeldCheckProfile) check(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(ctx); err != nil {
		return errors.Join(err, p.input.Close())
	}
	p.attempts++
	start, count := time.Now(), len(p.samples)
	err := p.input.Check()
	if len(p.samples) > count {
		p.samples[count].CheckStarted, p.samples[count].CheckElapsed = start, time.Since(start)
	}
	return err
}

func (p *HeldCheckProfile) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.attempts >= HeldProfileLimit {
		return ErrHeldProfileLimit
	}
	if err := p.guard(); err != nil {
		return err
	}
	return ctx.Err() // Ownership queries may themselves overrun the deadline.
}

// Close joins any in-flight Check before freeing the same native binding.
func (p *HeldCheckProfile) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.input.Close()
}

// Samples returns a detached snapshot only after any in-flight call returns.
func (p *HeldCheckProfile) Samples() []CheckProfileSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]CheckProfileSample(nil), p.samples...)
	for i := range out {
		out[i].Records = append([]CheckProfileRecord(nil), out[i].Records...)
	}
	return out
}

// Run stops on the first refusal, completion, cap or deadline. Step must perform
// only capture Next and held validation, never keys or receiver operations.
func (p *HeldCheckProfile) Run(ctx context.Context, step func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, HeldProfileTimeout)
	defer cancel()
	for {
		done, err := p.step(ctx, step)
		if done || err != nil {
			return err
		}
	}
}

func (p *HeldCheckProfile) step(ctx context.Context, next func() (bool, error)) (bool, error) {
	if err := p.check(ctx); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	done, err := next()
	if err != nil {
		return false, err
	}
	return done, ctx.Err()
}
