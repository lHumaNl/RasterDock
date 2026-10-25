package platform

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

const (
	nextTimeout  = 100 * time.Millisecond
	closeTimeout = 500 * time.Millisecond
)

type nativeStream interface {
	next() (Frame, error)
	close() error
}

type captureWorker struct {
	stop, done, ended chan struct{}
	requests          chan struct{}
	frames            chan Frame
	once              sync.Once
	terminal, cleanup error // Published by ended and done respectively.
}

func startWorker(gate chan struct{}, open func() (nativeStream, error), invalidate func()) (*captureWorker, error) {
	select {
	case gate <- struct{}{}:
	default:
		return nil, ErrCaptureBusy
	}
	w := &captureWorker{stop: make(chan struct{}), done: make(chan struct{}), ended: make(chan struct{}),
		requests: make(chan struct{}, 1), frames: make(chan Frame, 1)}
	go w.run(gate, open, invalidate)
	return w, nil
}

func (w *captureWorker) run(gate chan struct{}, open func() (nativeStream, error), invalidate func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)
	defer func() { <-gate }()
	n, err := open()
	if err == nil {
		err = w.serve(n)
	}
	w.finish(err, invalidate)
	if n != nil {
		w.cleanup = n.close()
	}
}

func (w *captureWorker) finish(err error, invalidate func()) {
	w.terminal = err
	if err != nil && !errors.Is(err, ErrStreamClosed) && invalidate != nil {
		invalidate()
	}
	close(w.ended)
}

func (w *captureWorker) serve(n nativeStream) error {
	for {
		select {
		case <-w.stop:
			return ErrStreamClosed
		case <-w.requests:
			if err := w.stateError(); err != nil {
				return err
			}
			frame, err := n.next()
			if err != nil && !errors.Is(err, ErrFramePending) {
				return err
			}
			if err == nil {
				w.publish(frame)
			}
		}
	}
}

func (w *captureWorker) publish(frame Frame) {
	// A caller timeout must not discard the only frame of a static window.
	// Keep one latest frame with its original acquisition timestamp.
	select {
	case <-w.frames:
	default:
	}
	select {
	case <-w.stop:
	case w.frames <- frame:
	}
}

func (w *captureWorker) stateError() error {
	select {
	case <-w.stop:
		return ErrStreamClosed
	default:
	}
	select {
	case <-w.ended:
		return w.terminal
	default:
		return nil
	}
}

func (w *captureWorker) Next(ctx context.Context) (Frame, error) {
	if ctx == nil {
		return Frame{}, errors.New("capture: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	if err := w.stateError(); err != nil {
		return Frame{}, err
	}
	wait, cancel := context.WithTimeout(ctx, nextTimeout)
	defer cancel()
	select {
	case w.requests <- struct{}{}:
	default:
	}
	select {
	case <-w.stop:
		return Frame{}, ErrStreamClosed
	case <-w.ended:
		return Frame{}, w.terminal
	case frame := <-w.frames:
		if err := w.stateError(); err != nil {
			return Frame{}, err
		}
		return frame, nil
	case <-wait.Done():
		return Frame{}, pendingError(ctx)
	}
}

func pendingError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrFramePending
}

func (w *captureWorker) Close() error {
	w.once.Do(func() { close(w.stop) })
	timer := time.NewTimer(closeTimeout)
	defer timer.Stop()
	select {
	case <-w.done:
		return w.cleanup
	case <-timer.C:
		return ErrCloseTimeout
	}
}
