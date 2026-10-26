// Package presentation contains headless presentation models.
package presentation

import "rasterdock/internal/application"

const metadataInterval = 10 // Data pages, not logical packets.

// Playback repeats metadata during each data cycle without changing packet identities.
type Playback struct{ metadata, total, cursor, repeated, since, capacity uint64 }

// NewPlayback starts with the entire manifest.
func NewPlayback(metadata, total uint64) *Playback {
	return &Playback{metadata: metadata, total: total, capacity: 1}
}

// NewPagePlayback counts physical pages and allows a page to cross manifest/data.
func NewPagePlayback(metadata, total uint64, profile application.ColorProfile) (*Playback, error) {
	if profile.Capacity() == 0 {
		return nil, application.ErrProfile
	}
	return &Playback{metadata: metadata, total: total, capacity: profile.Capacity()}, nil
}

// Next returns the first logical packet ordinal of the next page.
func (p *Playback) Next() uint64 {
	if p.total == 0 {
		return 0
	}
	if p.metadata > 0 && p.cursor >= p.metadata && p.since >= metadataInterval {
		n := p.repeated % p.metadata
		p.repeated += p.capacity
		if p.repeated >= p.metadata {
			p.repeated = 0
		}
		p.since = 0
		return n
	}
	n := p.cursor
	p.advance()
	return n
}

func (p *Playback) advance() {
	p.cursor += p.capacity
	if p.cursor > p.metadata {
		p.since++
	}
	if p.cursor >= p.total {
		p.cursor = 0
		p.since = 0
	}
}
