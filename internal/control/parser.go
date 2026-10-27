package control

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Protocol input limits, measured from F8 (not the last keystroke).
const (
	MaxInputLength = 48
	InputTimeout   = 2 * time.Second
)

// Command is an absolute page request within one viewer epoch.
type Command struct{ Nonce, Ordinal uint64 }

// Parser holds bounded partial input and O(1) replay state. Its zero value is
// ready for a new viewer epoch. Create a new Parser only when that epoch changes.
// The caller must gate keys on viewer focus and explicit operator authorization.
type Parser struct {
	buffer  string
	started time.Time
	last    Command
}

// ResetInput cancels only partial input; focus loss must not erase replay state.
func (p *Parser) ResetInput() { p.buffer = ""; p.started = time.Time{} }

// Key consumes canonical physical keys F8, 0..9, Tab, Enter/Return, Escape.
// Adapters must map physical digit codes, never layout-dependent text or paste.
// count is the exclusive ordinal bound. Invalid input never consumes a nonce.
// If rendering/publication can fail, call Key on a value copy and commit that
// copy only together with the successfully published page and nonce.
func (p *Parser) Key(key string, now time.Time, count uint64) (Command, bool) {
	if key == "F8" {
		p.ResetInput()
		p.started = now
		return Command{}, false
	}
	if p.started.IsZero() || now.Before(p.started) || now.Sub(p.started) >= InputTimeout {
		p.ResetInput()
		return Command{}, false
	}
	if key == "Enter" || key == "Return" {
		return p.finish(count)
	}
	p.appendKey(key)
	return Command{}, false
}

func (p *Parser) appendKey(key string) {
	if len(p.buffer) >= MaxInputLength || !canonicalInput(key) {
		p.ResetInput()
		return
	}
	if key == "Tab" {
		key = "\t"
	}
	p.buffer += key
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
