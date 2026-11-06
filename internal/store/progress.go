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

// Progress is the watch state of one file: where playback stopped, and whether
// and how often the file was watched. docs/DESIGN.md ("Watch state") says what
// belongs to the file and what to the film or the show.
//
// A file has a row while it is started (a position of NotStartedSec or more) or
// watched; with no row it is neither.
type Progress struct {
	PositionSec float64
	DurationSec float64
	// WatchedAt is when the file was last watched to the end, or marked watched
	// (unix seconds); 0 when it was not.
	WatchedAt int64
	// PlayCount is how many times it was watched to the end or marked watched. It
	// is 0 exactly when WatchedAt is: the table checks it.
	PlayCount int
	UpdatedAt int64 // unix nanoseconds, so back-to-back updates still order
}

// Watched reports whether the file was watched, to the end or by being marked.
func (p Progress) Watched() bool { return p.WatchedAt != 0 }

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
// resulting state. A watch is counted (WatchedAt, PlayCount) when the position
// reaches the end from a stored one that had not: reports from the credits on
// do not count it again, and a second viewing, which starts from a position
// before the end, counts when it gets there. A file that was watched stays
// watched while it is played again, whatever position it stops at.
//
// A position before NotStartedSec that is not "watched" deletes the row and
// returns nil, unless the file was watched before. It returns ErrNotFound when
// it would have to store a row for an item that does not exist.
func (s *Store) SetProgress(ctx context.Context, itemID int64, positionSec, durationSec float64) (*Progress, error) {
	var out *Progress
	err := s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		now := time.Now()
		prev, err := getProgress(ctx, tx, itemID)
		if err != nil {
			return err
		}
		p := Progress{PositionSec: positionSec, DurationSec: durationSec, UpdatedAt: now.UnixNano()}
		if prev != nil {
			p.WatchedAt, p.PlayCount = prev.WatchedAt, prev.PlayCount
		}
		if IsWatched(positionSec, durationSec) && (prev == nil || !IsWatched(prev.PositionSec, prev.DurationSec)) {
			p.WatchedAt, p.PlayCount = now.Unix(), p.PlayCount+1
		}
		if positionSec < NotStartedSec && !p.Watched() {
			_, err := tx.ExecContext(ctx, `DELETE FROM progress WHERE item_id = ?`, itemID)
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO progress (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
			SELECT id, ?, ?, ?, ?, ? FROM items WHERE id = ?
			ON CONFLICT (item_id) DO UPDATE SET position_sec = excluded.position_sec,
				duration_sec = excluded.duration_sec, watched_at = excluded.watched_at,
				play_count = excluded.play_count, updated_at = excluded.updated_at`,
			p.PositionSec, p.DurationSec, p.WatchedAt, p.PlayCount, p.UpdatedAt, itemID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out = &p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Continue returns the started, unfinished items, most recently played first,
// with their streams and progress. It reads one snapshot: an item is never
// returned without the progress that put it on the list. A file that was watched
// and is being watched again is not on it: it is not unfinished.
func (s *Store) Continue(ctx context.Context, limit int) (entries []Entry, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `
			SELECT item_id FROM progress WHERE watched_at = 0
			ORDER BY updated_at DESC, item_id DESC LIMIT ?`, limit)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		entries = make([]Entry, 0, len(ids))
		for _, id := range ids {
			e, err := getEntry(ctx, q, id)
			if err != nil {
				return err // progress rows cascade with their item: not ErrNotFound
			}
			entries = append(entries, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

const progressCols = `position_sec, duration_sec, watched_at, play_count, updated_at`

// getProgress returns the progress of one item, or nil.
func getProgress(ctx context.Context, q queryer, itemID int64) (*Progress, error) {
	var p Progress
	err := q.QueryRowContext(ctx, `SELECT `+progressCols+` FROM progress WHERE item_id = ?`, itemID).
		Scan(&p.PositionSec, &p.DurationSec, &p.WatchedAt, &p.PlayCount, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// allProgress returns every progress row keyed by item id.
func allProgress(ctx context.Context, q queryer) (map[int64]*Progress, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, `+progressCols+` FROM progress`)
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
		if err := rows.Scan(&id, &p.PositionSec, &p.DurationSec, &p.WatchedAt, &p.PlayCount, &p.UpdatedAt); err != nil {
			return nil, err
		}
		m[id] = &p
	}
	return m, rows.Err()
}
