package fyneui

import (
	"rasterdock/internal/adapters/codec"
	"rasterdock/internal/application"
)

func renderControlledPrepared(p *application.Prepared, profile application.VisualProfile, ordinal uint64, state codec.LiveState, viewport *viewportCapacity) (snapshot, error) {
	if err := checkFit(profile, viewport); err != nil {
		return snapshot{}, err
	}
	packets, err := p.VisualPage(ordinal, profile)
	if err != nil {
		return snapshot{}, err
	}
	img, err := codec.RenderControlledPage(p.Descriptor, packets, profile, ordinal, state)
	if err != nil {
		return snapshot{}, err
	}
	if err = checkFit(profile, viewport); err != nil {
		return snapshot{}, err
	}
	return snapshot{key: "status.frame", params: map[string]interface{}{"ordinal": ordinal + 1, "total": p.PacketCount()}, picture: img, pictureID: pictureIdentity{state.Frame, ordinal, state.Epoch}, prepared: p.Directory}, nil
}
