package control

import (
	"strings"
	"testing"
	"time"
)

var start = time.Unix(100, 0)

func enter(p *Parser, text string, at time.Time, count uint64) (Command, bool) {
	p.Key("F8", at, count)
	for _, key := range strings.Split(text, "") {
		if key == "\t" {
			key = "Tab"
		}
		p.Key(key, at, count)
	}
	return p.Key("Enter", at, count)
}

func TestParserReplay(t *testing.T) {
	var p Parser
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"1\t2", true}, {"1\t2", true}, {"1\t3", false},
		{"0\t2", false}, {"2\t9", false}, {"2\t3", true}, {"1\t2", false},
	} {
		if _, ok := enter(&p, tc.text, start, 9); ok != tc.ok {
			t.Errorf("%q: %v", tc.text, ok)
		}
	}
}

func TestParserInvalid(t *testing.T) {
	for _, text := range []string{"", "1", "\t1", "1\t", "1\t2\t3", "-1\t2", "1\t+2", "18446744073709551616\t1", "1\t18446744073709551616", strings.Repeat("0", 48) + "1\t2"} {
		var p Parser
		if _, ok := enter(&p, text, start, 9); ok {
			t.Errorf("accepted %q", text)
		}
		if _, ok := enter(&p, "1\t2", start, 9); !ok {
			t.Errorf("invalid input consumed nonce: %q", text)
		}
	}
}

func TestParserResetAndTimeout(t *testing.T) {
	var p Parser
	enter(&p, "1\t2", start, 9)
	p.ResetInput()
	if _, ok := enter(&p, "1\t3", start, 9); ok {
		t.Fatal("reset erased replay state")
	}
	for _, key := range []string{"F8", "2", "Tab", "3"} {
		p.Key(key, start, 9)
	}
	if _, ok := p.Key("Return", start.Add(InputTimeout), 9); ok {
		t.Fatal("accepted timed out input")
	}
	if _, ok := enter(&p, "2\t3", start.Add(InputTimeout), 9); !ok {
		t.Fatal("timeout consumed nonce")
	}
}

func TestParserExhaustion(t *testing.T) {
	var p Parser
	if _, ok := enter(&p, "18446744073709551615\t0", start, 1); !ok {
		t.Fatal("max nonce rejected")
	}
	if _, ok := enter(&p, "18446744073709551615\t0", start, 1); ok {
		t.Fatal("exhaustion not latched")
	}
}

func TestParserCanonicalKeysAndCancel(t *testing.T) {
	for _, key := range []string{"Escape", "\t", "Digit1", "Numpad1", "12", "a"} {
		var p Parser
		for _, prefix := range []string{"F8", "1"} {
			p.Key(prefix, start, 9)
		}
		p.Key(key, start, 9)
		p.Key("Tab", start, 9)
		p.Key("2", start, 9)
		if _, ok := p.Key("Enter", start, 9); ok {
			t.Fatal("accepted noncanonical/cancelled input", key)
		}
	}
}

func TestParserLengthCountAndRestart(t *testing.T) {
	var p Parser
	text := strings.Repeat("0", MaxInputLength-len("1\t2")) + "1\t2"
	if _, ok := enter(&p, text, start, 3); !ok {
		t.Fatal("48 character command rejected")
	}
	if _, ok := enter(&p, "2\t0", start, 0); ok {
		t.Fatal("empty transfer accepted")
	}
	for _, key := range []string{"F8", "9", "F8", "2", "Tab", "0"} {
		p.Key(key, start, 3)
	}
	if c, ok := p.Key("Return", start, 3); !ok || c.Nonce != 2 {
		t.Fatal(c, ok)
	}
}
