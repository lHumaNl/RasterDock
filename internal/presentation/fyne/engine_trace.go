package fyneui

import "rasterdock/internal/application"

// pictureIdentity follows the pixels, not the latest coalesced status.
type pictureIdentity struct {
	Frame   uint64 `json:"frame"`
	Ordinal uint64 `json:"ordinal"`
	Epoch   uint64 `json:"epoch"`
}

type engineTraceEvent struct {
	Stage   string
	Picture pictureIdentity
	Old     pictureIdentity
	Err     error
}

type engineObserver func(engineTraceEvent)

func (o engineObserver) record(stage string, picture, old pictureIdentity, err error) {
	if o != nil {
		o(engineTraceEvent{stage, picture, old, err})
	}
}

func (io engineIO) render(p *application.Prepared, profile application.VisualProfile, ordinal, frame, epoch uint64) (snapshot, error) {
	id := pictureIdentity{frame, ordinal, epoch}
	io.observer.record("render.begin", id, pictureIdentity{}, nil)
	s, err := renderPrepared(p, profile, ordinal, frame, epoch, io.viewport)
	io.observer.record("render.end", id, pictureIdentity{}, err)
	return s, err
}
