package fyneui

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"sync/atomic"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
	"rasterdock/internal/control"
	"rasterdock/internal/domain"
)

const controlApplyTimeout = time.Second

var errViewerStopped = errors.New("controlled viewer authorization or viewport lost")

// A publication is a revocable lease, not permission for a queued callback to
// paint indefinitely. Layout eligibility is rechecked on the UI thread.
type controlPublication struct {
	ctx      context.Context
	deadline time.Time
	revoked  atomic.Bool
	viewport *viewportCapacity
	lost     uint64
	bounds   image.Rectangle
	onRevoke func()
}

func newControlPublication(ctx context.Context, s snapshot, viewport *viewportCapacity, revoke func()) *controlPublication {
	p := &controlPublication{ctx: ctx, deadline: time.Now().Add(controlApplyTimeout), viewport: viewport, lost: s.viewportLost, onRevoke: revoke}
	if s.picture != nil {
		p.bounds = s.picture.Bounds()
	}
	return p
}

func (p *controlPublication) revoke() {
	if p != nil && !p.revoked.Swap(true) && p.onRevoke != nil {
		p.onRevoke()
	}
}

func (p *controlPublication) valid() bool {
	if p == nil {
		return false
	}
	if p.ctx.Err() != nil || !time.Now().Before(p.deadline) {
		p.revoke()
	}
	return !p.revoked.Load()
}

func (p *controlPublication) layoutValid(bounds image.Rectangle, visible bool) bool {
	if !p.valid() || !visible || bounds != p.bounds {
		return false
	}
	return p.spaceValid()
}

func (p *controlPublication) spaceValid() bool {
	if p == nil || p.viewport == nil {
		return false
	}
	space, required := p.viewport.load(), p.bounds.Size()
	return space.Lost == p.lost && required.X <= space.Size.X && required.Y <= space.Size.Y
}

type controlledViewer struct {
	prepared      *application.Prepared
	profile       application.VisualProfile
	ordinal, lost uint64
	state         codec.LiveState
	parser        control.Parser
	started       time.Time
}

func newControlledViewer(p *application.Prepared, profile application.VisualProfile, ordinal uint64, io engineIO) (*controlledViewer, error) {
	if p == nil || ordinal >= p.PacketCount() {
		return nil, errors.New("open or prepare a transfer first")
	}
	id, err := domain.NewID()
	if err != nil {
		return nil, err
	}
	epoch := binary.BigEndian.Uint64(id[:8])
	if epoch == 0 {
		return nil, errors.New("invalid viewer epoch")
	}
	return &controlledViewer{prepared: p, profile: profile, ordinal: ordinal, lost: io.viewport.load().Lost,
		started: time.Now(), state: codec.LiveState{Epoch: epoch, Focused: true, Armed: true}}, nil
}

func awaitControlApply(ctx context.Context, s snapshot, emit func(snapshot)) error {
	if s.publication == nil {
		s.publication = newControlPublication(ctx, s, nil, nil)
	}
	s.applied = make(chan struct{}, 1)
	emit(s)
	timer := time.NewTimer(controlApplyTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.publication.revoke()
		return ctx.Err()
	case <-timer.C:
		s.publication.revoke()
		return errors.New("controlled canvas application timed out")
	case <-s.applied:
		if !s.publication.valid() {
			return errViewerStopped
		}
		return nil
	}
}

func (v *controlledViewer) eligible(io engineIO) error {
	if io.viewerFocused == nil || !io.viewerFocused() || io.viewport.load().Lost != v.lost {
		return errViewerStopped
	}
	return checkFit(v.profile, io.viewport)
}

func (v *controlledViewer) publish(ctx context.Context, ordinal, nonce uint64, emit func(snapshot), io engineIO) error {
	next, err := v.nextState(nonce, io)
	if err != nil {
		return err
	}
	s, err := renderControlledPrepared(v.prepared, v.profile, ordinal, next, io.viewport)
	if err != nil {
		return err
	}
	if err = v.eligible(io); err != nil {
		return err
	}
	s.controlled = true
	s.viewportLost = v.lost
	s.publication = newControlPublication(ctx, s, io.viewport, io.viewerRevoke)
	if err = awaitControlApply(ctx, s, emit); err != nil {
		return err
	}
	v.state, v.ordinal = next, ordinal
	return nil
}

func (v *controlledViewer) nextState(nonce uint64, io engineIO) (codec.LiveState, error) {
	if err := v.eligible(io); err != nil {
		return codec.LiveState{}, err
	}
	if v.state.Frame == math.MaxUint64 {
		return codec.LiveState{}, errors.New("viewer frame exhausted")
	}
	next := v.state
	next.Frame++
	next.Nonce = nonce
	return next, nil
}

func (v *controlledViewer) key(ctx context.Context, cmd command, emit func(snapshot), io engineIO) error {
	if cmd.observed.Before(v.started) {
		return nil
	}
	if err := v.eligible(io); err != nil {
		return err
	}
	next := v.parser
	// Replay state is transactional with the rendered, UI-applied publication.
	request, accepted := next.Key(cmd.physicalKey, cmd.observed, v.prepared.PacketCount())
	if accepted {
		if err := v.publish(ctx, request.Ordinal, request.Nonce, emit, io); err != nil {
			return err
		}
	}
	v.parser = next
	if accepted && request.Nonce == math.MaxUint64 {
		return errors.New("viewer nonce exhausted")
	}
	return nil
}

func runControlledViewer(ctx context.Context, p *application.Prepared, profile application.VisualProfile, ordinal uint64, commands <-chan command, emit func(snapshot), io engineIO) error {
	defer emit(snapshot{key: "status.paused", releaseControl: true, picture: image.NewRGBA(image.Rect(0, 0, 1, 1))})
	v, err := newControlledViewer(p, profile, ordinal, io)
	if err != nil {
		return err
	}
	if err = v.openSurface(ctx, emit, io); err != nil {
		return err
	}
	if err = v.publish(ctx, ordinal, 0, emit, io); err != nil {
		return err
	}
	return v.loop(ctx, commands, emit, io)
}

func (v *controlledViewer) openSurface(ctx context.Context, emit func(snapshot), io engineIO) error {
	s := snapshot{controlled: true, key: "control.armed", viewportLost: v.lost, picture: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	s.publication = newControlPublication(ctx, s, io.viewport, io.viewerRevoke)
	return awaitControlApply(ctx, s, emit)
}

func (v *controlledViewer) loop(ctx context.Context, commands <-chan command, emit func(snapshot), io engineIO) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd, ok := <-commands:
			if !ok || cmd.kind != "control_key" {
				return nil
			}
			if err := v.key(ctx, cmd, emit, io); err != nil {
				return err
			}
		case <-io.ticks:
			if err := v.publish(ctx, v.ordinal, v.state.Nonce, emit, io); err != nil {
				return err
			}
		}
	}
}
