package storage

import (
	"database/sql"
	"errors"
	"math"

	"rasterdock/internal/domain"
)

// NextMissing returns the first absent logical packet: manifest chunks first,
// then file chunks in verified manifest order. It requires the serialized owner.
// A false missing value with nil error means packets are complete, not published.
// The ordinal is zero when missing is false. No verification or publication is
// attempted: complete metadata without successful verification returns ErrMetadata.
// Reaccepting a manifest packet (or reopening) retries that existing transition.
func (r *Receiver) NextMissing() (ordinal uint64, missing bool, err error) {
	if r.stopped {
		return 0, false, ErrConflict
	}
	count := 1 + (r.descriptor.Length-1)/uint64(r.descriptor.Block)
	index, err := r.firstMissingChunk(0, count)
	if err != nil {
		return 0, false, err
	}
	if index < count {
		return index, true, nil
	}
	if r.manifest == nil {
		return 0, false, ErrMetadata
	}
	return r.nextMissingData(count)
}

func (r *Receiver) nextMissingData(base uint64) (uint64, bool, error) {
	if err := r.checkPlanBounds(base); err != nil {
		return 0, false, err
	}
	for _, file := range r.manifest.Files {
		// Only the in-memory recovery barrier proves publication, not a SQL flag.
		if !r.published[file.ID] && file.Chunks > 0 {
			index, err := r.firstMissingChunk(file.ID, file.Chunks)
			if err != nil {
				return 0, false, err
			}
			if index < file.Chunks {
				return base + index, true, nil
			}
		}
		base += file.Chunks
	}
	return 0, false, nil
}

func (r *Receiver) checkPlanBounds(total uint64) error {
	// Manifest validation bounds data packets, but does not include metadata.
	for _, file := range r.manifest.Files {
		if file.Chunks > math.MaxUint64-total {
			return domain.ErrManifest
		}
		total += file.Chunks
	}
	return nil
}

func (r *Receiver) firstMissingChunk(file uint32, count uint64) (index uint64, err error) {
	// The (file,idx) primary key supplies order without a bitmap or range expansion.
	rows, err := r.db.Query("SELECT idx FROM chunks WHERE file=? ORDER BY idx", file)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	return scanChunkGap(rows, count)
}

func scanChunkGap(rows *sql.Rows, count uint64) (uint64, error) {
	var next uint64
	for next < count && rows.Next() {
		var index uint64
		if err := rows.Scan(&index); err != nil {
			return 0, err
		}
		if index != next {
			return next, nil
		}
		next++
	}
	return next, rows.Err()
}
