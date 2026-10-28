package fyneui

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"time"

	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

type captureStream interface {
	Next(context.Context) (platform.Frame, error)
	Close() error
}

func engine(ctx context.Context, root string, commands <-chan command, events chan snapshot, logger *slog.Logger, capacity *viewportCapacity, viewerAuthorized func() bool, viewerRevoke func()) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	runEngine(ctx, root, commands, events, logger, engineIO{
		viewport: capacity, ticks: tick.C, openCapture: func(id uint32) (captureStream, error) { return platform.OpenCapture(id) },
		decode: codec.Decode, openReceive: anchoredReceive(root),
		controlDecode: codec.DecodeControlled, bindInput: platform.BindInput,
		controlCapture: func(id uint32) (captureStream, error) { return platform.OpenControlCapture(id) },
		controlDelay:   3 * time.Second, viewerAuthorized: viewerAuthorized,
		viewerRevoke: viewerRevoke,
	})
}

type receiveOwner interface {
	Close() error
	CheckDescriptor(v1.Descriptor) error
	Accept(v1.Packet) error
	Publish() error
	DurableBytes() (uint64, error)
	PublishedFiles() (uint64, uint64, error)
}

// Per-engine boundaries keep tests headless without mutable package-level hooks.
type engineIO struct {
	controlCapture   func(uint32) (captureStream, error)
	controlDecode    func(image.Image) (v1.Beacon, []v1.Packet, error)
	bindInput        func(uint32) (platform.InputBinding, error)
	controlDelay     time.Duration
	viewerAuthorized func() bool
	viewerRevoke     func()
	observer         engineObserver
	viewport         *viewportCapacity
	ticks            <-chan time.Time
	openCapture      func(uint32) (captureStream, error)
	decode           func(image.Image) (v1.Beacon, []v1.Packet, error)
	openReceive      func(string, string, v1.Descriptor) (receiveOwner, error)
}

type fitError struct{ required, available image.Point }

func (e *fitError) Error() string {
	return fmt.Sprintf("required %dx%d physical pixels, available %dx%d; enlarge the window or select a smaller module/grid", e.required.X, e.required.Y, e.available.X, e.available.Y)
}

func checkFit(profile application.VisualProfile, viewport *viewportCapacity) error {
	required, err := profile.Size()
	if err != nil {
		return err
	}
	available := viewport.load().Size
	if required.X > available.X || required.Y > available.Y {
		return &fitError{required, available}
	}
	return nil
}

func renderPrepared(p *application.Prepared, profile application.VisualProfile, ordinal, frame, epoch uint64, viewport *viewportCapacity) (snapshot, error) {
	if err := checkFit(profile, viewport); err != nil {
		return snapshot{}, err
	}
	packets, err := p.VisualPage(ordinal, profile)
	if err != nil {
		return snapshot{}, err
	}
	img, err := codec.RenderVisualPage(p.Descriptor, packets, profile, ordinal, frame, epoch)
	if err != nil {
		return snapshot{}, err
	}
	if err = checkFit(profile, viewport); err != nil {
		return snapshot{}, err
	}
	return snapshot{key: "status.frame", params: map[string]interface{}{"ordinal": ordinal + 1, "total": p.PacketCount()}, picture: img, pictureID: pictureIdentity{frame, ordinal, epoch}, prepared: p.Directory}, nil
}

func emitSnapshot(events chan snapshot, s snapshot) {
	emitObservedSnapshot(events, s, nil)
}

func emitObservedSnapshot(events chan snapshot, s snapshot, observer engineObserver) {
	select {
	case events <- s:
		observer.record("mailbox.enqueue", s.pictureID, pictureIdentity{}, nil)
		return
	default:
	}
	select {
	case pending := <-events:
		s.closeApplication = s.closeApplication || pending.closeApplication
		if !s.releaseControl && pending.releaseControl && (s.controlOwner == 0 || s.controlOwner == pending.controlOwner) {
			s.releaseControl, s.finishedControl, s.controlOwner = true, pending.finishedControl, pending.controlOwner
		}
		if s.picture == nil && s.visual == pending.visual && s.role == pending.role {
			s.picture = pending.picture
			s.pictureID = pending.pictureID
			s.applied = pending.applied
			s.controlled = pending.controlled
			s.publication = pending.publication
			s.viewportLost = pending.viewportLost
			if s.controlled {
				s.controlOwner = pending.controlOwner
			}
		}
		observer.record("mailbox.replace", s.pictureID, pending.pictureID, nil)
	default:
	}
	select {
	case events <- s:
		observer.record("mailbox.enqueue", s.pictureID, pictureIdentity{}, nil)
	default:
		observer.record("mailbox.drop", s.pictureID, pictureIdentity{}, nil)
	}
}

// anchoredReceive binds the engine's caller-established private state namespace.
// Sessions outside that namespace are rejected, not assigned an inferred anchor.
func anchoredReceive(anchor string) func(string, string, v1.Descriptor) (receiveOwner, error) {
	return func(session, destination string, descriptor v1.Descriptor) (receiveOwner, error) {
		r, err := storage.OpenReceive(session, destination, descriptor, application.DecodeManifest, anchor)
		if err != nil {
			return nil, err
		}
		return r, nil
	}
}
