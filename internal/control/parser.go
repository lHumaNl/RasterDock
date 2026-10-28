package control

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Protocol input limits, measured from F8/F9 (not the last keystroke).
const (
	MaxInputLength = 48
	InputTimeout   = 2 * time.Second
)

// Command is an absolute page request within one viewer epoch.
type Command struct{ Nonce, Ordinal uint64 }

// Parser holds bounded partial input and O(1) replay state. Its zero value is
// ready for a new viewer epoch. Create a new Parser only when that epoch changes.
// The caller gates explicit operator authorization. Local target focus is the
// injecting receiver's responsibility, not a sender-side session condition.
type Parser struct {
	buffer        string
	started       time.Time
	last          Command
	mode          string
	pendingFinish uint64
	finishKey     string
	terminal      bool
}

// ResetInput cancels only partial input; focus loss must not erase replay state.
func (p *Parser) ResetInput() {
	p.buffer, p.mode, p.finishKey = "", "", ""
	p.started, p.pendingFinish = time.Time{}, 0
}

// Key consumes canonical physical keys F8, 0..9, Tab, Enter/Return, Escape.
// Adapters must map physical digit codes, never layout-dependent text or paste.
// count is the exclusive ordinal bound. Invalid input never consumes a nonce.
// If rendering/publication can fail, call Key on a value copy and commit that
// copy only together with the successfully published page and nonce.
func (p *Parser) Key(key string, now time.Time, count uint64) (Command, bool) {
	if p.terminal {
		return Command{}, false
	}
	if p.pendingFinish != 0 {
		p.ResetInput()
		return Command{}, false
	}
	if key == "F8" || key == "F9" {
		p.ResetInput()
		p.started, p.mode = now, key
		return Command{}, false
	}
	return p.collect(key, now, count)
}

func (p *Parser) collect(key string, now time.Time, count uint64) (Command, bool) {
	if !p.inputFresh(now) {
		p.ResetInput()
		return Command{}, false
	}
	if key == "Enter" || key == "Return" {
		if p.mode == "F9" {
			p.stageFinish(key)
			return Command{}, false
		}
		return p.finish(count)
	}
	p.appendKey(key)
	return Command{}, false
}

func (p *Parser) appendKey(key string) {
	if len(p.buffer) >= MaxInputLength || !canonicalInput(key) || (p.mode == "F9" && key == "Tab") {
		p.ResetInput()
		return
	}
	if key == "Tab" {
		key = "\t"
	}
	p.buffer += key
}

func (p *Parser) inputFresh(now time.Time) bool {
	return !p.started.IsZero() && !now.Before(p.started) && now.Sub(p.started) < InputTimeout
}

func (p *Parser) stageFinish(key string) {
	nonce, err := strconv.ParseUint(p.buffer, 10, 64)
	if err != nil || nonce == 0 || nonce <= p.last.Nonce {
		p.ResetInput()
		return
	}
	p.pendingFinish, p.finishKey = nonce, key
}

// Release commits FINISH only on the exact terminal key's matching keyup.
// Adapters must preserve Return versus keypad Enter and gate releases like keys.
// ResetInput cancels a candidate without clearing terminal or page replay state.
func (p *Parser) Release(key string, now time.Time) (nonce uint64, finished bool) {
	if p.terminal {
		return 0, false
	}
	if !p.inputFresh(now) {
		p.ResetInput()
		return 0, false
	}
	if p.pendingFinish == 0 {
		if p.mode == "F9" && (key == "Enter" || key == "Return") {
			p.ResetInput()
		}
		return 0, false
	}
	defer p.ResetInput()
	if key != p.finishKey {
		return 0, false
	}
	p.terminal = true
	return p.pendingFinish, true
}

func canonicalInput(key string) bool {
	return key == "Tab" || (len(key) == 1 && key[0] >= '0' && key[0] <= '9')
}

func (p *Parser) finish(count uint64) (Command, bool) {
	defer p.ResetInput()
	fields := strings.Split(p.buffer, "\t")
	if len(fields) != 2 || p.last.Nonce == math.MaxUint64 {
		return Command{}, false
	}
	nonce, ne := strconv.ParseUint(fields[0], 10, 64)
	ordinal, oe := strconv.ParseUint(fields[1], 10, 64)
	c := Command{nonce, ordinal}
	if ne != nil || oe != nil || nonce == 0 || ordinal >= count || nonce < p.last.Nonce {
		return Command{}, false
	}
	if nonce == p.last.Nonce && ordinal != p.last.Ordinal {
		return Command{}, false
	}
	p.last = c
	return c, true
}
