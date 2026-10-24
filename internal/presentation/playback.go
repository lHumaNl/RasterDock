// Package presentation contains headless presentation models.
package presentation

// Playback repeats metadata during each data cycle without changing packet identities.
type Playback struct{ metadata, total, cursor, repeated, since uint64 }

// NewPlayback starts with the entire manifest.
func NewPlayback(metadata, total uint64) *Playback {
	return &Playback{metadata: metadata, total: total}
}

// Next returns the next logical packet ordinal.
func (p *Playback) Next() uint64 {
	if p.total == 0 {
		return 0
	}
	if p.cursor >= p.metadata && p.since == 10 {
		n := p.repeated % p.metadata
		p.repeated++
		p.since = 0
		return n
	}
	n := p.cursor
	p.cursor++
	if n >= p.metadata {
		p.since++
	}
	if p.cursor == p.total {
		p.cursor = 0
		p.since = 0
	}
	return n
}
