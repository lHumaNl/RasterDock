package fyneui

import (
	"image"
	"time"

	"rasterdock/internal/adapters/platform"
	"rasterdock/internal/adapters/storage"
	"rasterdock/internal/application"
	v1 "rasterdock/internal/protocol/v1"
)

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
	ticks       <-chan time.Time
	capture     func(uint32) (platform.Frame, error)
	decode      func(image.Image) (v1.Beacon, []v1.Packet, error)
	openReceive func(string, string, v1.Descriptor) (receiveOwner, error)
}

func openReceive(session, destination string, descriptor v1.Descriptor) (receiveOwner, error) {
	r, err := storage.OpenReceive(session, destination, descriptor, application.DecodeManifest)
	if err != nil {
		return nil, err
	}
	return r, nil
}
