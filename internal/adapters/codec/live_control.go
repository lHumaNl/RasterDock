package codec

import (
	"errors"
	"image"

	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

const (
	liveFocused = 1 << iota
	liveArmed
)

const (
	phaseMetadata = iota
	phaseData
	phasePaused
)

// LiveState is a caller-owned snapshot; nonce generation and lifecycle remain
// the caller's responsibility. Epoch must be nonzero for controlled frames.
type LiveState struct {
	Frame, Epoch, Nonce    uint64
	Focused, Armed, Paused bool
}

// RenderControlledPage renders a fresh v1 canvas with live control state.
// Pausing changes only the beacon phase, not the supplied packet grid.
func RenderControlledPage(d v1.Descriptor, packets []v1.Packet, profile application.VisualProfile, ordinal uint64, state LiveState) (*image.RGBA, error) {
	if state.Epoch == 0 {
		return nil, v1.ErrPacket
	}
	return renderVisualPage(d, packets, profile, ordinal, state)
}

func liveBeacon(d v1.Descriptor, first v1.Packet, profile application.VisualProfile, ordinal uint64, state LiveState) v1.Beacon {
	b := visualBeacon(d, profile, ordinal, state.Frame, state.Epoch)
	b.Nonce = state.Nonce
	if state.Focused {
		b.Flags |= liveFocused
	}
	if state.Armed {
		b.Flags |= liveArmed
	}
	if first.Kind == 1 {
		b.Phase = phaseMetadata
	}
	if state.Paused {
		b.Phase = phasePaused
	}
	return b
}

// DecodeControlled requires exactly one valid beacon detection, even when
// multiple canvases carry identical bytes. Passive Decode retains its policy.
func DecodeControlled(img image.Image) (v1.Beacon, []v1.Packet, error) {
	return decode(img, true)
}

// ErrAmbiguousControl requires operator reselection, not an input retry.
var ErrAmbiguousControl = errors.New("multiple beacons in controlled capture")
