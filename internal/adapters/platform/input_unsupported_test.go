//go:build !darwin || !cgo

package platform

import (
	"errors"
	"testing"
)

func TestInputUnsupported(t *testing.T) {
	if !errors.Is(InputCapability(), ErrInputUnavailable) {
		t.Fatal("capability did not fail closed")
	}
	if binding, err := BindInput(1); binding != nil || !errors.Is(err, ErrInputUnavailable) {
		t.Fatal("binding did not fail closed")
	}
	if OwnWindowFocused("sender") {
		t.Fatal("unsupported focus must be false")
	}
	if focused, generation := OwnWindowFocusState("sender"); focused || generation != 0 {
		t.Fatalf("unsupported focus state must be false/zero: %v/%d", focused, generation)
	}
}
