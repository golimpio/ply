package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
)

// Keyframes returns the cached keyframe times (seconds from the start of the
// file, ascending) of an item. ok is false when none are cached.
func (s *Store) Keyframes(ctx context.Context, itemID int64) (times []float64, ok bool, err error) {
	var blob []byte
	err = s.ro.QueryRowContext(ctx, `SELECT times FROM keyframes WHERE item_id = ?`, itemID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(blob) == 0 || len(blob)%8 != 0 {
		return nil, false, nil // unreadable cache entry: treat as a miss
	}
	times = make([]float64, len(blob)/8)
	for i := range times {
		times[i] = math.Float64frombits(binary.LittleEndian.Uint64(blob[i*8:]))
	}
	return times, true, nil
}

// SetKeyframes caches the keyframe times of an item, replacing any earlier
// entry. It fails with ErrNotFound when the item no longer exists. No list shows them:
// it leaves Generation alone.
func (s *Store) SetKeyframes(ctx context.Context, itemID int64, times []float64) error {
	blob := make([]byte, 8*len(times))
	for i, t := range times {
		binary.LittleEndian.PutUint64(blob[i*8:], math.Float64bits(t))
	}
	res, err := s.execQuiet(ctx, prioHigh, `
		INSERT INTO keyframes (item_id, times) SELECT id, ? FROM items WHERE id = ?
		ON CONFLICT (item_id) DO UPDATE SET times = excluded.times`, blob, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
