package platform

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Input capability and latched authorization failures never trigger fallback input.
var (
	ErrInputUnavailable = errors.New("native keyboard input unavailable")
	ErrInputStopped     = errors.New("native keyboard input permanently stopped")
)

// InputBinding is an explicitly selected target, not authorization to control it.
// Callers must independently gate capture freshness and operator consent.
type InputBinding interface {
	Target() ControlTarget
	Check() error
	// Send runs eligibility, then a fresh native preflight before EVERY key.
	// Eligibility need not duplicate observational Check: native authorization is
	// never cached. The original per-key deadline starts before eligibility, and
	// includes callback/lock wait time. Native postflight and paired release remain
	// required; an error can follow partial input and is not proof of zero keys.
	Send(context.Context, uint64, uint64, func() error) error
	Close() error
}

// RefreshingInputBinding adds explicit refresh before each key's native deadline.
// It does not automatically renew capture freshness or authorize input. Refresh
// runs synchronously outside the native gate, serialized with Send and other
// refresh callbacks. Callbacks must honor the context and must not reenter either
// send method; they may call Close. Errors can follow partial input.
type RefreshingInputBinding interface {
	InputBinding
	// SendWithRefresh requires both callbacks. One whole-sequence deadline covers
	// owner wait, refresh, eligibility and native work; it is never renewed per key.
	SendWithRefresh(context.Context, uint64, uint64, func(context.Context) error, func() error) error
}

const (
	// InputSequenceTimeout matches the control protocol's two-second sequence
	// limit without importing control core. Legacy Send retains its caller budget.
	InputSequenceTimeout = 2 * time.Second
	// InputGuardMargin is the required capture-expiry lookahead in EVERY Send
	// callback: gate.Check(time.Now().Add(InputGuardMargin)). The adapter rejects
	// preflight results after inputCheckBudget, including callback/lock wait time.
	// This is not a real-time OS scheduling or atomic focus/post guarantee.
	InputGuardMargin          = 150 * time.Millisecond
	inputCheckBudget          = 100 * time.Millisecond
	inputSequenceLimit        = 48
	inputKeyF8         uint16 = 100
	inputKeyTab        uint16 = 48
	inputKeyReturn     uint16 = 36
	inputKeyInterval          = 10 * time.Millisecond
)

type inputBinding struct {
	target   ControlTarget // Immutable after construction; independent of native lifetime.
	owner    sync.Mutex
	gate     sync.Mutex
	stopped  atomic.Bool
	done     chan struct{}
	once     sync.Once
	check    func() error
	deadline func(time.Duration) uint64
	prepare  func(uint64) error
	key      func(uint16) error
	release  func()
}

// Target returns the pinned provenance, including after Close. Callers must also
// Check live identity; this snapshot alone does not authorize input.
func (b *inputBinding) Target() ControlTarget { return b.target }

func inputSequence(nonce, ordinal uint64) ([]uint16, error) {
	if nonce == 0 {
		return nil, ErrInputUnavailable
	}
	digits := [...]uint16{29, 18, 19, 20, 21, 23, 22, 26, 28, 25}
	keys := []uint16{inputKeyF8}
	for _, digit := range strconv.FormatUint(nonce, 10) {
		keys = append(keys, digits[digit-'0'])
	}
	keys = append(keys, inputKeyTab)
	for _, digit := range strconv.FormatUint(ordinal, 10) {
		keys = append(keys, digits[digit-'0'])
	}
	keys = append(keys, inputKeyReturn)
	if len(keys) > inputSequenceLimit {
		return nil, ErrInputUnavailable
	}
	return keys, nil
}

func (b *inputBinding) stop(err error) error {
	_ = b.Close()
	return err
}

// Close cancels waits immediately and joins any in-flight native check/key pair.
// A pair already in progress is always released; cancellation cannot retract it.
func (b *inputBinding) Close() error {
	b.stopped.Store(true)
	b.once.Do(func() { close(b.done) })
	b.gate.Lock()
	defer b.gate.Unlock()
	if b.release != nil {
		b.release()
		b.release = nil
	}
	return nil
}

func (b *inputBinding) native(call func() error) error {
	b.gate.Lock()
	defer b.gate.Unlock()
	if b.stopped.Load() {
		return ErrInputStopped
	}
	err := call()
	if err != nil {
		b.stopped.Store(true)
	}
	return err
}

func (b *inputBinding) Check() error {
	err := b.native(b.check)
	if err != nil {
		return b.stop(err)
	}
	return nil
}

func (b *inputBinding) Send(ctx context.Context, nonce, ordinal uint64, eligible func() error) error {
	return b.send(ctx, nonce, ordinal, nil, eligible)
}

func (b *inputBinding) SendWithRefresh(ctx context.Context, nonce, ordinal uint64, refresh func(context.Context) error, eligible func() error) error {
	if ctx == nil || refresh == nil {
		return b.stop(ErrInputUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, InputSequenceTimeout)
	defer cancel()
	return b.send(ctx, nonce, ordinal, refresh, eligible)
}

func (b *inputBinding) send(ctx context.Context, nonce, ordinal uint64, refresh func(context.Context) error, eligible func() error) error {
	b.owner.Lock()
	defer b.owner.Unlock()
	if b.stopped.Load() {
		return ErrInputStopped
	}
	if ctx == nil || eligible == nil {
		return b.stop(ErrInputUnavailable)
	}
	keys, err := inputSequence(nonce, ordinal)
	if err != nil {
		return b.stop(err)
	}
	return b.sendSequence(ctx, keys, refresh, eligible)
}

func (b *inputBinding) sendSequence(ctx context.Context, keys []uint16, refresh func(context.Context) error, eligible func() error) error {
	for _, key := range keys {
		if err := b.refresh(ctx, refresh); err != nil {
			return b.stop(err)
		}
		if err := b.sendKey(ctx, key, eligible); err != nil {
			return b.stop(err)
		}
	}
	return nil
}

func (b *inputBinding) refresh(ctx context.Context, refresh func(context.Context) error) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	if refresh != nil {
		return refresh(ctx)
	}
	return nil
}

func (b *inputBinding) sendKey(ctx context.Context, key uint16, eligible func() error) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	deadline := b.deadline(inputBudget(ctx))
	if err := eligible(); err != nil {
		return err
	}
	return b.postKey(ctx, key, deadline)
}

func inputBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(0, min(inputCheckBudget, time.Until(deadline)))
	}
	return inputCheckBudget
}

func (b *inputBinding) ready(ctx context.Context) error {
	if b.stopped.Load() {
		return ErrInputStopped
	}
	// Timer delivery may lag the deadline; never authorize work in that gap.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func (b *inputBinding) postKey(ctx context.Context, key uint16, deadline uint64) error {
	err := b.native(func() error {
		if err := b.ready(ctx); err != nil {
			return err
		}
		if err := b.prepare(deadline); err != nil {
			return err
		}
		if err := b.ready(ctx); err != nil {
			return err
		}
		return b.key(key) // No remote AX query between this cancellation check and post.
	})
	if err != nil {
		return err
	}
	return b.waitKey(ctx)
}

func (b *inputBinding) waitKey(ctx context.Context) error {
	timer := time.NewTimer(inputKeyInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return ErrInputStopped
	case <-timer.C:
		return b.ready(ctx)
	}
}
