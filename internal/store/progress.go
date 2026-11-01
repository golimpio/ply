package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Thresholds for resume state.
const (
	// NotStartedSec: a position before this counts as "not started" and
	// leaves no progress row (unless the item is watched).
	NotStartedSec = 30
	// WatchedFraction: at or past this share of the duration an item is watched.
	WatchedFraction = 0.9
	// WatchedTailSec: within this many seconds of the end an item is watched
	// too, provided at least half of it has played (a 90 s clip is not
	// "watched" at second 5).
	WatchedTailSec = 120
)

// Progress is the resume state of an item.
type Progress struct {
	PositionSec float64
	DurationSec float64
	Watched     bool
	UpdatedAt   int64 // unix nanoseconds, so back-to-back updates still order
}

// IsWatched reports whether a position counts as having seen the item.
func IsWatched(positionSec, durationSec float64) bool {
	if durationSec <= 0 {
		return false
	}
	if positionSec >= WatchedFraction*durationSec {
		return true
	}
	return durationSec-positionSec <= WatchedTailSec && positionSec >= durationSec/2
}

// SetProgress records where playback of an item stopped and returns the
// resulting state. A position before NotStartedSec that is not "watched"
// deletes the row and returns nil. It returns ErrNotFound when it would have
// to store a row for an item that does not exist.
func (s *Store) SetProgress(ctx context.Context, itemID int64, positionSec, durationSec float64) (*Progress, error) {
	watched := IsWatched(positionSec, durationSec)
	if positionSec < NotStartedSec && !watched {
		_, err := s.db.ExecContext(ctx, `DELETE FROM progress WHERE item_id = ?`, itemID)
		return nil, err
	}
	p := Progress{PositionSec: positionSec, DurationSec: durationSec, Watched: watched, UpdatedAt: time.Now().UnixNano()}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at)
		SELECT id, ?, ?, ?, ? FROM items WHERE id = ?
		ON CONFLICT (item_id) DO UPDATE SET position_sec = excluded.position_sec,
			duration_sec = excluded.duration_sec, watched = excluded.watched, updated_at = excluded.updated_at`,
		p.PositionSec, p.DurationSec, p.Watched, p.UpdatedAt, itemID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return &p, nil
}

// Continue returns the started, unfinished items, most recently played first,
// with their streams and progress.
func (s *Store) Continue(ctx context.Context, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_id FROM progress WHERE watched = 0
		ORDER BY updated_at DESC, item_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // the pool has one connection: finish before the next query

	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntry(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue // removed since the first query
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// getProgress returns the progress of one item, or nil.
func (s *Store) getProgress(ctx context.Context, itemID int64) (*Progress, error) {
	var p Progress
	err := s.db.QueryRowContext(ctx, `
		SELECT position_sec, duration_sec, watched, updated_at FROM progress WHERE item_id = ?`, itemID).
		Scan(&p.PositionSec, &p.DurationSec, &p.Watched, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// allProgress returns every progress row keyed by item id.
func (s *Store) allProgress(ctx context.Context) (map[int64]*Progress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT item_id, position_sec, duration_sec, watched, updated_at FROM progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]*Progress{}
	for rows.Next() {
		var (
			id int64
			p  Progress
		)
		if err := rows.Scan(&id, &p.PositionSec, &p.DurationSec, &p.Watched, &p.UpdatedAt); err != nil {
			return nil, err
		}
		m[id] = &p
	}
	return m, rows.Err()
}
