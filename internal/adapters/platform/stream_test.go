package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBGRAConversionOwnsPixels(t *testing.T) {
	src := []byte{1, 2, 3, 255, 99, 99, 99, 99, 4, 5, 6, 255}
	im, err := copyBGRA(src, 1, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	src[0] = 90
	if im.Pix[0] != 3 || im.Pix[2] != 1 || im.Pix[4] != 6 {
		t.Fatal(im.Pix)
	}
}

func TestBGRARejectsInvalidLayout(t *testing.T) {
	for _, v := range [][3]int{{0, 1, 4}, {1, -1, 4}, {16385, 1, 65540}, {8193, 8193, 32772}, {2, 1, 4}, {1, 2, 8}, {1, 3, int(^uint(0) >> 1)}} {
		if _, err := copyBGRA(make([]byte, 8), v[0], v[1], v[2]); err == nil {
			t.Fatal(v)
		}
	}
}

func TestTokenIdentityAndInvalidation(t *testing.T) {
	r := newTokenRegistry()
	a := windowIdentity{handle: uintptr(1)<<40 | 7, pid: 8, tid: 9}
	b := windowIdentity{handle: 7, pid: 8, tid: 9}
	x, _ := r.register(a)
	y, _ := r.register(b)
	if x == y || x == 0 {
		t.Fatal(x, y)
	}
	r.invalidate(x)
	z, _ := r.register(a)
	if z == x {
		t.Fatal("token reused")
	}
	if _, ok := r.lookup(x); ok {
		t.Fatal("invalidated token survived")
	}
}

func TestWorkerPendingAndCloseTimeout(t *testing.T) {
	gate := make(chan struct{}, 1)
	blocked := make(chan struct{})
	s, err := startWorker(gate, func() (nativeStream, error) { <-blocked; return nil, errors.New("stopped") }, nil)
	requireError(t, err, nil)
	requireNextError(t, s, ErrFramePending)
	requireError(t, s.Close(), ErrCloseTimeout)
	_, err = startWorker(gate, nil, nil)
	requireError(t, err, ErrCaptureBusy)
	close(blocked)
	waitDone(t, s.done)
	requireError(t, s.Close(), nil)
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker leaked")
	}
}

func requireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func requireNextError(t *testing.T, s Stream, want error) {
	t.Helper()
	_, err := s.Next(context.Background())
	requireError(t, err, want)
}
