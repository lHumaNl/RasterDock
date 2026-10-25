//go:build windows && cgo

package platform

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWindowsUnknownSource(t *testing.T) {
	if _, err := OpenCapture(0); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if _, err := Capture(0); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
}

func TestWindowsInvalidHandleFailsBeforeCapture(t *testing.T) {
	// A null HWND is never a capture target; no user window is opened by this test.
	id, err := windowsTokens.register(windowIdentity{})
	requireError(t, err, nil)
	s, err := OpenCapture(id)
	requireError(t, err, nil)
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = waitFirstFrame(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
	requireError(t, s.Close(), nil)
	if _, ok := windowsTokens.lookup(id); ok {
		t.Fatal("terminal failure did not invalidate token")
	}
}
