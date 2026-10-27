//go:build darwin && cgo

package platform

import (
	"errors"
	"testing"
)

func TestInputAXAllowanceUsesRemainingSharedBudget(t *testing.T) {
	const ms uint64 = 1_000_000
	for _, tc := range []struct {
		name                string
		now, deadline, want uint64
	}{
		{"initial", 0, 100 * ms, 100 * ms},
		{"after_first_query", 74 * ms, 100 * ms, 26 * ms},
		{"near_expiry", 96 * ms, 100 * ms, 4 * ms},
		{"at_expiry", 100 * ms, 100 * ms, 0},
		{"after_expiry", 101 * ms, 100 * ms, 0},
		{"unset", 0, 0, 0},
		{"cap_defense", 0, 500 * ms, 100 * ms},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputAXAllowance(tc.now, tc.deadline); got != tc.want {
				t.Fatalf("allowance=%d want %d", got, tc.want)
			}
		})
	}
}

func TestInputBindDeadline(t *testing.T) {
	const budget uint64 = 500_000_000
	for _, tc := range []struct {
		name      string
		now, want uint64
	}{
		{"exact_budget", 1_000_000_000, 1_000_000_000 + budget},
		{"overflow", ^uint64(0) - budget + 1, 0},
		{"boundary", ^uint64(0) - budget, ^uint64(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputBindDeadline(tc.now); got != tc.want {
				t.Fatalf("deadline=%d want %d", got, tc.want)
			}
		})
	}
}

func TestInputDiagnosticNativeAXClassification(t *testing.T) {
	for _, stage := range []InputDiagnosticStage{InputStageAXWindows, InputStageAXTitle, InputStageFocusedWindow} {
		for _, code := range []InputDiagnosticCode{InputCodeUnknown, InputCodeAXTimeoutSetup, InputCodeQuery, InputCodeNull, InputCodeType, InputCodeWindowLimit} {
			for _, timely := range []bool{false, true} {
				for _, ax := range []int{0, -25204} {
					checkNativeAXClassification(t, stage, code, timely, ax)
				}
			}
		}
	}
}

func checkNativeAXClassification(t *testing.T, stage InputDiagnosticStage, code InputDiagnosticCode, timely bool, ax int) {
	t.Helper()
	err := classifyInputAX(InputDiagnosticError{}, stage, timely, ax, code)
	want := code
	if !timely {
		want = InputCodeBudget
	}
	if want == InputCodeUnknown {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var got *InputDiagnosticError
	if !errors.As(err, &got) || got.Stage != stage || got.Code != want || !got.HasAXError || got.AXError != ax {
		t.Fatalf("classification: %v", err)
	}
}

func TestInputDiagnosticNativeFirstFailure(t *testing.T) {
	for _, first := range []InputDiagnosticError{
		{Stage: InputStageAXTitle, Code: InputCodeQuery, HasAXError: true, AXError: -25204},
		{Stage: InputStageLocalFirst, Code: InputCodeModifiers},
	} {
		err := classifyInputAX(first, InputStageFocusedWindow, false, 0, InputCodeNull)
		var got *InputDiagnosticError
		if !errors.As(err, &got) || *got != first {
			t.Fatalf("first failure replaced: %v", err)
		}
	}
}
