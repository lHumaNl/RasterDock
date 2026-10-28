package platform

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestInputDiagnosticSafeFormat(t *testing.T) {
	for _, ax := range []int{0, -25204, 42} {
		e := &InputDiagnosticError{Stage: InputStageAXTitle, Code: InputCodeQuery, HasAXError: true, AXError: ax}
		want := fmt.Sprintf("native keyboard input unavailable: stage=ax_title code=query ax=%d", ax)
		if e.Error() != want {
			t.Fatalf("format: %s", e)
		}
		wrapped := errors.Join(errors.New("held"), fmt.Errorf("check: %w", e))
		var got *InputDiagnosticError
		if !errors.Is(wrapped, ErrInputUnavailable) || !errors.As(wrapped, &got) || got != e {
			t.Fatal("lost diagnostic")
		}
	}
}

func TestInputDiagnosticUnknown(t *testing.T) {
	e := &InputDiagnosticError{Stage: InputDiagnosticStage(999), Code: InputDiagnosticCode(999), AXError: -25204}
	if e.Error() != "native keyboard input unavailable: stage=unknown code=unknown" {
		t.Fatal(e)
	}
	for stage := InputStageBindSelection; stage <= InputStageTarget; stage++ {
		if stage.String() == "unknown" {
			t.Fatal("unmapped stage", stage)
		}
	}
	for code := InputCodePermission; code <= InputCodeTargetInvalid; code++ {
		if code.String() == "unknown" || strings.ContainsAny(code.String(), "/\\\n") {
			t.Fatal("unsafe code", code)
		}
	}
}

func TestInputDiagnosticCategories(t *testing.T) {
	for code := InputCodeUnknown; code <= InputCodeTargetInvalid+1; code++ {
		err := fmt.Errorf("check: %w", &InputDiagnosticError{Code: code})
		focus := code == InputCodeFrontmost || code == InputCodeFocusIdentity
		process := code == InputCodeProcessStartChanged
		if errors.Is(err, ErrInputFocusLost) != focus || errors.Is(err, ErrInputProcessGone) != process {
			t.Errorf("incorrect category for %v", code)
		}
		if !errors.Is(err, ErrInputUnavailable) {
			t.Errorf("lost compatibility for %v", code)
		}
	}
}

func TestInputDiagnosticCheckLatches(t *testing.T) {
	b := fakeInput(t)
	want := &InputDiagnosticError{Stage: InputStageLocalFirst, Code: InputCodeModifiers}
	releases, checks := 0, 0
	b.check = func() error { checks++; return want }
	b.release = func() { releases++ }
	var got *InputDiagnosticError
	if err := b.Check(); !errors.As(err, &got) || got != want || !errors.Is(err, ErrInputUnavailable) {
		t.Fatal(err)
	}
	if err := b.Check(); !errors.Is(err, ErrInputStopped) {
		t.Fatal(err)
	}
	_ = b.Close()
	if releases != 1 || checks != 1 {
		t.Fatalf("release=%d check=%d", releases, checks)
	}
}
