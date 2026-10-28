package control

import (
	"errors"
	"math"
	"time"

	v1 "rasterdock/internal/protocol/v1"
)

// Protocol control timing and total send budget.
const (
	Freshness     = time.Second
	EchoTimeout   = 2 * time.Second
	FirstBackoff  = 250 * time.Millisecond
	SecondBackoff = 500 * time.Millisecond
	MaxSends      = 3
	focusArmed    = 2 // Sender focus is not keyboard authority; only armed matters.
	phasePaused   = 2
)

// Control errors distinguish an ignored observation from a stopped binding.
var (
	ErrUnbound        = errors.New("control requires explicit binding")
	ErrBindingChanged = errors.New("control descriptor, epoch, or capture generation changed")
	ErrIneligible     = errors.New("control observation is ineligible")
	ErrConflict       = errors.New("unsolicited control nonce or ordinal")
	ErrExhausted      = errors.New("control counter exhausted")
	ErrNotReady       = errors.New("persisted blocks or missing plan not ready")
	ErrPending        = errors.New("control command already pending")
	ErrSendState      = errors.New("invalid control send completion")
	ErrTimeout        = errors.New("control echo send budget exhausted")
	ErrNative         = errors.New("native input eligibility checker required")
	ErrTerminal       = errors.New("control finish already reserved")
)

// Observation must contain a protocol-validated Beacon. CapturedAt is acquisition
// time in the caller's local monotonic clock domain, never decode completion.
// AgeBounded must be established by the capture adapter; absent provenance is
// false, not fabricated. Generation identifies the selected capture source.
type Observation struct {
	Beacon     v1.Beacon
	CapturedAt time.Time
	Generation uint64
	AgeBounded bool
}

// Pending is a copy of a request's scheduling state. IssuedAt is the FIRST
// completed Enter, Deadline is the latest send's echo deadline, and RetryAt is
// its deadline plus backoff. AwaitingSent forbids another send reservation.
type Pending struct {
	Command                     Command
	IssuedAt, Deadline, RetryAt time.Time
	Attempts                    int
	AwaitingSent                bool
	firstReservation            time.Time
	baselineFrame               uint64
}

// Receiver is a synchronous, single-owner gate. Zero value is unbound. It never
// sends keys, persists data, or infers data readiness from a control echo.
type Receiver struct {
	bound      bool
	current    Observation
	cutoff     time.Time
	next       uint64
	confirmed  Command
	pending    *Pending
	reservedAt time.Time
	finishing  bool
}

// Bind explicitly authorizes a validated descriptor/epoch/source snapshot.
// now is the binding cutoff: queued captures at or before now are excluded from
// subsequent Observe calls. The supplied current snapshot alone seeds highwater
// and acquisition provenance. Do not use decode time as capture time.
// A failed bind also revokes the previous binding.
func (r *Receiver) Bind(pinned v1.Descriptor, o Observation, now time.Time) error {
	r.Stop()
	if pinned.Validate() != nil || pinned != o.Beacon.Descriptor {
		return ErrBindingChanged
	}
	if !eligible(o, now) {
		return ErrIneligible
	}
	if o.Beacon.Nonce == math.MaxUint64 || o.Beacon.Frame == math.MaxUint64 {
		return ErrExhausted
	}
	r.bound, r.current, r.cutoff = true, o, now
	r.finishing = false
	r.next = o.Beacon.Nonce + 1
	r.confirmed = Command{o.Beacon.Nonce, o.Beacon.Ordinal}
	return nil
}

// Stop revokes authorization and any pending request. Resume requires Bind.
// Call on operator revocation, input failure (including partial send), or source loss.
func (r *Receiver) Stop() { r.bound = false; r.pending = nil }

// FreshUntil returns the accepted capture's inclusive freshness boundary, or zero
// when unbound. It neither mutates state nor authorizes input; use Check with the
// actual current time. Static-page age is not an authorization deadline.
func (r *Receiver) FreshUntil() time.Time {
	if !r.bound {
		return time.Time{}
	}
	return r.current.CapturedAt.Add(Freshness)
}

func (r *Receiver) stop(err error) error { r.Stop(); return err }

func fresh(o Observation, now time.Time) bool {
	return o.AgeBounded && !o.CapturedAt.IsZero() && !now.Before(o.CapturedAt) && now.Sub(o.CapturedAt) <= Freshness
}

func eligible(o Observation, now time.Time) bool {
	return fresh(o, now) && o.Beacon.Epoch != 0 && o.Beacon.Flags&focusArmed != 0 && o.Beacon.Phase <= phasePaused
}

// Check validates the binding and finite request budget, not the age of a static
// page. Capture timestamps validate incoming evidence, never keyboard authority.
func (r *Receiver) Check(now time.Time) error {
	if !r.bound {
		return ErrUnbound
	}
	if r.current.Beacon.Flags&focusArmed == 0 {
		return r.stop(ErrIneligible)
	}
	if r.budgetExpired(now) {
		return r.stop(ErrTimeout)
	}
	return nil
}

func (r *Receiver) budgetExpired(now time.Time) bool {
	p := r.pending
	return !r.finishing && p != nil && p.Attempts >= MaxSends && !now.Before(p.Deadline)
}

// Observe validates incoming acquisitions, not the age of the previous page.
// Identity changes and conflicting same-frame content stop control. Echoes must
// exceed the first reservation's highwater/time and cannot acknowledge before Sent.
func (r *Receiver) Observe(o Observation, now time.Time) (bool, error) {
	result, err := r.ObserveFrame(o, now)
	return result.Acknowledged, err
}

// ObservationResult separates admission of packet-bearing evidence from a page
// acknowledgement. Ignored images may not advance durable receive progress.
type ObservationResult struct{ Admitted, Acknowledged bool }

// ObserveFrame reports whether this acquisition may supply packets to storage.
func (r *Receiver) ObserveFrame(o Observation, now time.Time) (ObservationResult, error) {
	if !r.bound {
		return ObservationResult{}, ErrUnbound
	}
	b, prev := o.Beacon, r.current.Beacon
	if b.Descriptor != prev.Descriptor || b.Epoch != prev.Epoch || o.Generation != r.current.Generation {
		return ObservationResult{}, r.stop(ErrBindingChanged)
	}
	if b.Frame == prev.Frame && b != prev {
		return ObservationResult{}, r.stop(ErrConflict)
	}
	if b.Frame <= prev.Frame || !o.CapturedAt.After(r.cutoff) || !fresh(o, now) {
		return ObservationResult{}, nil
	}
	if err := r.acceptable(o, now); err != nil {
		return ObservationResult{}, r.stop(err)
	}
	r.current = o
	ack, err := r.echo(o)
	return ObservationResult{Admitted: err == nil, Acknowledged: ack}, err
}

func (r *Receiver) acceptable(o Observation, now time.Time) error {
	if !eligible(o, now) {
		return ErrIneligible
	}
	if o.Beacon.Frame == math.MaxUint64 {
		return ErrExhausted
	}
	if r.budgetExpired(now) {
		return ErrTimeout
	}
	return nil
}

func (r *Receiver) echo(o Observation) (bool, error) {
	c := Command{o.Beacon.Nonce, o.Beacon.Ordinal}
	if r.pending != nil && c == r.pending.Command {
		if r.pending.AwaitingSent || r.pending.IssuedAt.IsZero() || !o.CapturedAt.After(r.pending.firstReservation) || o.Beacon.Frame <= r.pending.baselineFrame {
			return false, nil
		}
		r.confirmed, r.pending = c, nil
		if c.Nonce == math.MaxUint64 {
			return true, r.stop(ErrExhausted)
		}
		return true, nil
	}
	if c != r.confirmed {
		return false, r.stop(ErrConflict)
	}
	return false, nil
}

// Eligibility checks input availability, operator handoff, and local foreground
// target immediately before EACH send. It must not reenter Receiver. Any failure
// stops the binding. The caller must still abort if eligibility changes mid-send.
type Eligibility func() error

func (r *Receiver) beforeSend(now time.Time, check Eligibility) error {
	if err := r.Check(now); err != nil {
		return err
	}
	if check == nil {
		return r.stop(ErrNative)
	}
	if err := check(); err != nil {
		return r.stop(err)
	}
	return nil
}

// Begin reserves the first send, only with an explicit persisted/missing-plan
// ready signal for this request. ordinal must come from that validated plan.
// Execute returned Command immediately, then call Sent with Enter completion
// time, or Stop on any failure. A reservation is not a completed send.
func (r *Receiver) Begin(ordinal uint64, ready bool, now time.Time, check Eligibility) (Command, error) {
	if r.finishing {
		return Command{}, ErrTerminal
	}
	if r.pending != nil {
		return Command{}, ErrPending
	}
	if !ready {
		return Command{}, ErrNotReady
	}
	if err := r.beforeSend(now, check); err != nil {
		return Command{}, err
	}
	if r.next == 0 {
		return Command{}, r.stop(ErrExhausted)
	}
	return r.reserve(ordinal, now), nil
}

func (r *Receiver) reserve(ordinal uint64, now time.Time) Command {
	c := Command{r.next, ordinal}
	if r.next == math.MaxUint64 {
		r.next = 0
	} else {
		r.next++
	}
	r.pending = &Pending{Command: c, AwaitingSent: true, firstReservation: now, baselineFrame: r.current.Beacon.Frame}
	r.reservedAt = now
	return c
}

// Pending returns a value copy; false means no unconfirmed command.
func (r *Receiver) Pending() (Pending, bool) {
	if r.pending == nil {
		return Pending{}, false
	}
	return *r.pending, true
}

// Sent records successful Enter completion for a reserved send. The same owner
// may process observations during the send; concurrent mutations are forbidden.
// Such observations are not replayed. Production callers process captured echoes
// after Sent, allowing acquisition between Enter-down and Enter-up.
// Invalid completion stops control.
func (r *Receiver) Sent(enterCompletedAt time.Time) error {
	p := r.pending
	if !r.bound || p == nil || !p.AwaitingSent || enterCompletedAt.IsZero() || enterCompletedAt.Before(r.reservedAt) {
		return r.stop(ErrSendState)
	}
	if p.IssuedAt.IsZero() {
		p.IssuedAt = enterCompletedAt
	}
	p.Attempts++
	p.AwaitingSent = false
	p.Deadline = enterCompletedAt.Add(EchoTimeout)
	backoff := FirstBackoff
	if p.Attempts > 1 {
		backoff = SecondBackoff
	}
	p.RetryAt = p.Deadline.Add(backoff)
	return nil
}

// Retry reserves a due retry of exactly the same command. Poll while receiving
// or without new observations; no timer or automatic execution exists. ready=false
// means wait/no pending; err means stop. The third timeout exhausts the budget.
func (r *Receiver) Retry(now time.Time, check Eligibility) (command Command, ready bool, err error) {
	if r.finishing {
		return Command{}, false, ErrTerminal
	}
	if err = r.Check(now); err != nil {
		return
	}
	p := r.pending
	if p == nil || p.AwaitingSent {
		return
	}
	if now.Before(p.RetryAt) {
		return
	}
	if err = r.beforeSend(now, check); err != nil {
		return
	}
	p.AwaitingSent, r.reservedAt = true, now
	return p.Command, true, nil
}

// BeginFinish reserves a one-shot terminal nonce only after durable completion.
// Send immediately; never retry or call Sent for FINISH. Check/Observe remain
// active during sending. An old pending page is retained only for echo provenance,
// not scheduling. Stop on send failure; only an explicit Bind starts a new session.
func (r *Receiver) BeginFinish(ready bool, now time.Time, check Eligibility) (uint64, error) {
	if r.finishing {
		return 0, ErrTerminal
	}
	if !ready {
		return 0, ErrNotReady
	}
	if r.pending != nil && r.pending.AwaitingSent {
		return 0, ErrPending
	}
	if err := r.beforeSend(now, check); err != nil {
		return 0, err
	}
	if r.next == 0 {
		return 0, r.stop(ErrExhausted)
	}
	nonce := r.next
	// Unsigned wrap marks exhaustion; the terminal latch prevents reuse.
	r.next++
	r.finishing = true
	return nonce, nil
}
