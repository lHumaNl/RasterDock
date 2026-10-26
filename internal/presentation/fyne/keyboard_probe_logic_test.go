package fyneui

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "rasterdock/internal/protocol/v1"
)

// Register cancellation before child cleanup callbacks. testing runs cleanups
// in reverse order, so EOF/graceful child exit precedes CommandContext killing.
func keyboardProbeContext(t *testing.T, limit time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	t.Cleanup(cancel)
	return ctx
}

func TestKeyboardProbeContextSurvivesChildCleanup(t *testing.T) {
	var ctx context.Context
	t.Run("child", func(t *testing.T) {
		ctx = keyboardProbeContext(t, time.Minute)
		t.Cleanup(func() {
			if err := ctx.Err(); err != nil {
				t.Errorf("context killed child before graceful cleanup: %v", err)
			}
		})
	})
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test context was not canceled after child cleanup")
	}
}

// Prototype only. Physical key names come from pinned Fyne 2.8.2's GLFW
// key mapping (both Key0 and KeyKP0 map to "0"), NEVER OnTypedRune.
type probeParser struct {
	nonce, ordinal uint64
	buffer         string
	started        time.Time
	active         bool
}

func (p *probeParser) key(key string, now time.Time, count uint64) bool {
	if key == "F8" {
		p.buffer = ""
		p.started = now
		p.active = p.nonce != math.MaxUint64
		return false
	}
	if !p.active {
		return false
	}
	if now.Sub(p.started) >= 2*time.Second || key == "Escape" {
		p.active = false
		p.buffer = ""
		return false
	}
	if key == "Return" || key == "Enter" {
		p.active = false
		parts := strings.Split(p.buffer, "\t")
		if len(parts) != 2 {
			return false
		}
		n, e := strconv.ParseUint(parts[0], 10, 64)
		o, f := strconv.ParseUint(parts[1], 10, 64)
		if e != nil || f != nil || n == 0 || o >= count || n < p.nonce || (n == p.nonce && o != p.ordinal) {
			return false
		}
		p.nonce, p.ordinal = n, o
		return true
	}
	switch {
	case key == "Tab" && !strings.Contains(p.buffer, "\t") && p.buffer != "":
		p.buffer += "\t"
	case len(key) == 1 && key[0] >= '0' && key[0] <= '9':
		p.buffer += key
	default:
		p.active = false
	}
	if len(p.buffer) > 48 {
		p.active = false
		p.buffer = ""
	}
	return false
}

type probeGate struct {
	descriptor     v1.Descriptor
	epoch, high    uint64
	last           v1.Beacon
	at             time.Time
	stopped        bool
	pending        bool
	nonce, ordinal uint64
	issued         time.Time
	attempts       int
}

func (g *probeGate) observe(b v1.Beacon, at, now time.Time) error {
	if b.Descriptor != g.descriptor || b.Epoch == 0 || b.Epoch != g.epoch || b.Flags != 3 {
		g.stopped = true
		return errors.New("binding/focus/armed lost")
	}
	if b.Frame <= g.high || at.IsZero() || at.After(now) || now.Sub(at) > time.Second {
		return errors.New("stale observation")
	}
	if g.high != 0 && ((!g.pending && b.Nonce != g.last.Nonce) ||
		(g.pending && ((b.Nonce != g.last.Nonce && b.Nonce != g.nonce) || (b.Nonce == g.nonce && b.Ordinal != g.ordinal)))) {
		g.stopped = true
		return errors.New("conflicting controller command")
	}
	g.high, g.last, g.at = b.Frame, b, at
	return nil
}
func (g *probeGate) eligible(now time.Time, foreground func() error) error {
	if g.stopped || g.at.IsZero() || now.Sub(g.at) > time.Second || g.last.Flags != 3 {
		g.stopped = true
		return errors.New("control ineligible")
	}
	if err := foreground(); err != nil {
		g.stopped = true
		return err
	}
	return nil
}
func (g *probeGate) begin(ordinal uint64, acceptedOrMissingDetermined bool) error {
	if g.stopped || g.pending || !acceptedOrMissingDetermined || g.last.Nonce == math.MaxUint64 {
		return errors.New("cannot advance")
	}
	g.pending = true
	g.nonce = g.last.Nonce + 1
	g.ordinal = ordinal
	g.issued = time.Time{}
	g.attempts = 0
	return nil
}
func (g *probeGate) sent(now time.Time) error {
	if !g.pending || g.attempts >= 3 || g.stopped {
		return errors.New("send budget exhausted")
	}
	g.attempts++
	if g.issued.IsZero() {
		g.issued = now
	}
	return nil
}
func (g *probeGate) echo() bool {
	if !g.pending || g.issued.IsZero() || !g.at.After(g.issued) || g.last.Nonce != g.nonce || g.last.Ordinal != g.ordinal {
		return false
	}
	g.pending = false
	return true
}

// The same bounded exchange runs against native input or a deterministic fake.
// All callbacks are serialized; only one request can be outstanding.
type probeExchange struct {
	now        func() time.Time
	pause      func(time.Duration) error
	refresh    func() error
	foreground func() error
	send       func(context.Context, uint64, uint64, func() error) error
}

func (x probeExchange) request(ctx context.Context, g *probeGate, ordinal uint64, repeat bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := x.refresh(); err != nil {
		return err
	}
	if err := g.eligible(x.now(), x.foreground); err != nil {
		return err
	}
	if repeat {
		if g.pending || g.last.Nonce == 0 || g.last.Nonce == math.MaxUint64 || ordinal != g.last.Ordinal {
			return errors.New("invalid explicit idempotent repeat")
		}
		g.pending = true
		g.nonce = g.last.Nonce
		g.ordinal = ordinal
		g.issued = time.Time{}
		g.attempts = 0
	} else if err := g.begin(ordinal, true); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := x.pause(time.Duration(attempt) * 250 * time.Millisecond); err != nil {
				return err
			}
			if err := x.refresh(); err != nil {
				return err
			}
			// A late first-attempt echo remains eligible after backoff.
			if g.echo() {
				return nil
			}
		}
		if err := g.eligible(x.now(), x.foreground); err != nil {
			return err
		}
		if err := x.send(ctx, g.nonce, g.ordinal, func() error { return g.eligible(x.now(), x.foreground) }); err != nil {
			g.stopped = true
			return err
		}
		if err := g.sent(x.now()); err != nil {
			return err
		}
		until := x.now().Add(2 * time.Second)
		for x.now().Before(until) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := x.refresh(); err != nil {
				return err
			}
			if g.echo() {
				return nil
			}
			if err := g.eligible(x.now(), x.foreground); err != nil {
				return err
			}
			if err := x.pause(40 * time.Millisecond); err != nil {
				return err
			}
		}
	}
	g.stopped = true
	return errors.New("no captured matching echo after three total sends")
}

func TestKeyboardProbeExchangeRetryAndRefusal(t *testing.T) {
	for _, scenario := range []string{"second-attempt-echo", "exhausted", "foreground-loss", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Unix(100, 0)
			g := probeGate{epoch: 7}
			b := v1.Beacon{Epoch: 7, Flags: 3, Frame: 1}
			var sends [][2]uint64
			var delays []time.Duration
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			x := probeExchange{
				now:   func() time.Time { return now },
				pause: func(d time.Duration) error { delays = append(delays, d); now = now.Add(d); return ctx.Err() },
				foreground: func() error {
					if scenario == "foreground-loss" && len(sends) > 0 {
						return errors.New("lost foreground")
					}
					return nil
				},
				refresh: func() error {
					now = now.Add(time.Millisecond)
					b.Frame++
					if scenario == "second-attempt-echo" && len(sends) == 2 {
						b.Nonce = 1
						b.Ordinal = 4
					}
					return g.observe(b, now, now)
				},
				send: func(_ context.Context, n, o uint64, eligible func() error) error {
					if err := eligible(); err != nil {
						return err
					}
					sends = append(sends, [2]uint64{n, o})
					if scenario == "cancel" {
						cancel()
					}
					return nil
				},
			}
			err := x.request(ctx, &g, 4, false)
			want := map[string]int{"second-attempt-echo": 2, "exhausted": 3, "foreground-loss": 1, "cancel": 1}[scenario]
			if len(sends) != want {
				t.Fatalf("sends=%v want %d err=%v", sends, want, err)
			}
			for _, command := range sends {
				if command != [2]uint64{1, 4} {
					t.Fatalf("retry mutated command %v", command)
				}
			}
			if (err == nil) != (scenario == "second-attempt-echo") {
				t.Fatalf("result %v", err)
			}
			if scenario == "exhausted" {
				var backoffs []time.Duration
				for _, d := range delays {
					if d > 40*time.Millisecond {
						backoffs = append(backoffs, d)
					}
				}
				if len(backoffs) != 2 || backoffs[0] != 250*time.Millisecond || backoffs[1] != 500*time.Millisecond {
					t.Fatalf("backoffs=%v", backoffs)
				}
			}
		})
	}
}

func TestKeyboardProbeParser(t *testing.T) {
	now := time.Unix(100, 0)
	command := func(p *probeParser, n, o string) bool {
		p.key("F8", now, 100)
		for _, c := range n {
			p.key(string(c), now, 100)
		}
		p.key("Tab", now, 100)
		for _, c := range o {
			p.key(string(c), now, 100)
		}
		return p.key("Return", now, 100)
	}
	var p probeParser
	if !command(&p, "1", "12") {
		t.Fatal("valid command refused")
	}
	if !command(&p, "1", "12") {
		t.Fatal("idempotent command refused")
	}
	for _, c := range [][2]string{{"0", "0"}, {"1", "13"}, {"1", "100"}, {"18446744073709551616", "1"}, {"2", "18446744073709551616"}, {"", "1"}, {"2", "-1"}, {strings.Repeat("0", 49) + "2", "1"}} {
		if command(&p, c[0], c[1]) || p.nonce != 1 || p.ordinal != 12 {
			t.Fatalf("accepted invalid %v", c)
		}
	}
	if !command(&p, "2", "0") || command(&p, "1", "12") {
		t.Fatal("stale nonce")
	}
	p.key("F8", now, 100)
	p.key("3", now, 100)
	p.key("Tab", now, 100)
	p.key("1", now, 100)
	if p.key("Return", now.Add(2*time.Second), 100) {
		t.Fatal("timeout accepted")
	}
	p.key("F8", now, 100)
	p.key("3", now, 100)
	p.key("Escape", now, 100)
	if p.key("Return", now, 100) {
		t.Fatal("cancel accepted")
	}
	if !command(&p, "18446744073709551615", "1") || command(&p, "18446744073709551615", "1") {
		t.Fatal("max nonce must disarm")
	}
}

func TestKeyboardProbeController(t *testing.T) {
	now := time.Unix(100, 0)
	b := v1.Beacon{Epoch: 7, Flags: 3, Frame: 1}
	g := probeGate{epoch: 7}
	if err := g.observe(b, now, now); err != nil {
		t.Fatal(err)
	}
	if g.begin(12, false) == nil {
		t.Fatal("advanced before acceptance/missing determination")
	}
	if err := g.begin(4, true); err != nil {
		t.Fatal(err)
	} // partial decode requests first missing, not next page
	if g.begin(12, true) == nil {
		t.Fatal("two pending commands")
	}
	for i := 0; i < 3; i++ {
		if err := g.sent(now.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
		if g.nonce != 1 || g.ordinal != 4 || g.issued != now {
			t.Fatal("retry changed absolute command/issued_at")
		}
	}
	if g.sent(now) == nil {
		t.Fatal("fourth send allowed")
	}
	b.Frame = 2
	b.Nonce = 1
	b.Ordinal = 4
	if err := g.observe(b, now, now); err != nil {
		t.Fatal(err)
	}
	if g.echo() {
		t.Fatal("pre-Enter capture confirmed")
	}
	b.Frame = 3
	later := now.Add(time.Millisecond)
	if err := g.observe(b, later, later); err != nil || !g.echo() {
		t.Fatal("fresh matching echo refused", err)
	}
	if g.observe(b, later, later) == nil {
		t.Fatal("duplicate frame eligible")
	}
	if g.eligible(later, func() error { return errors.New("other app") }) == nil || !g.stopped {
		t.Fatal("foreground loss not latched")
	}
	for _, mutate := range []func(*v1.Beacon){func(b *v1.Beacon) { b.Epoch++ }, func(b *v1.Beacon) { b.Flags = 1 }, func(b *v1.Beacon) { b.Hash[0]++ }} {
		x := probeGate{epoch: 7}
		bad := b
		mutate(&bad)
		if x.observe(bad, later, later) == nil || !x.stopped {
			t.Fatal("binding mismatch not stopped")
		}
	}
}
