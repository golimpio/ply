package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Match is the metadata-matching state of a movie item or of a show.
type Match struct {
	TitleID       int64 // 0 = not linked
	State         string
	NeedsReview   bool
	Score         float64
	Attempts      int
	NextAttemptAt int64 // unix seconds; the item is due when this is in the past
}

func (m Match) validate() error {
	switch m.State {
	case MatchPending, MatchMatched, MatchUnmatched, MatchManual, MatchDisabled:
		return nil
	}
	return fmt.Errorf("store: match state %q", m.State)
}

// SeriesMatch is a show's row in series_matches.
type SeriesMatch struct {
	ID   int64 // the row id, used in "local:series:<id>:<slot>" image refs
	Show string
	Match
	LocalPoster   string
	LocalBackdrop string
}

// DueItem is a movie waiting to be matched.
type DueItem struct {
	Item
	Attempts int
}

// DueSeries is a show waiting to be matched. Path is one of its episode files
// (the first indexed), for hints in the folder names.
type DueSeries struct {
	Show     string
	Path     string
	Attempts int
}

// SetItemMatch replaces the item's match state, link, score and retry
// bookkeeping in one statement. It returns ErrNotFound for an unknown item.
func (s *Store) SetItemMatch(ctx context.Context, itemID int64, m Match) error {
	if err := m.validate(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE items SET title_id = ?, match_state = ?, needs_review = ?, match_score = ?,
			match_attempts = ?, next_attempt_at = ?
		WHERE id = ?`,
		nullInt(m.TitleID), m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSeriesMatch is SetItemMatch for a show, creating its series_matches row
// when it has none. It returns ErrNotFound for a show without episodes.
func (s *Store) SetSeriesMatch(ctx context.Context, show string, m Match) error {
	if err := m.validate(); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO series_matches (show, title_id, match_state, needs_review, match_score,
			match_attempts, next_attempt_at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE `+hasEpisodes+`
		ON CONFLICT (show) DO UPDATE SET title_id = excluded.title_id, match_state = excluded.match_state,
			needs_review = excluded.needs_review, match_score = excluded.match_score,
			match_attempts = excluded.match_attempts, next_attempt_at = excluded.next_attempt_at`,
		show, nullInt(m.TitleID), m.State, m.NeedsReview, m.Score, m.Attempts, m.NextAttemptAt, show)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// hasEpisodes is a condition on a bind parameter: the show has an episode.
const hasEpisodes = `EXISTS (SELECT 1 FROM items WHERE kind = 'episode' AND show = ?)`

// DeferItemMatch records a failed attempt that should be retried: the item
// stays as it is, its attempts go up by one and it is due again at next.
func (s *Store) DeferItemMatch(ctx context.Context, itemID int64, next int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE items SET match_attempts = match_attempts + 1, next_attempt_at = ? WHERE id = ?`, next, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeferSeriesMatch is DeferItemMatch for a show.
func (s *Store) DeferSeriesMatch(ctx context.Context, show string, next int64) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO series_matches (show, match_attempts, next_attempt_at) SELECT ?, 1, ? WHERE `+hasEpisodes+`
		ON CONFLICT (show) DO UPDATE SET match_attempts = match_attempts + 1,
			next_attempt_at = excluded.next_attempt_at`, show, next, show)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DueItems returns up to limit movies that are pending with next_attempt_at at
// or before now, oldest first.
func (s *Store) DueItems(ctx context.Context, now int64, limit int) ([]DueItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+itemCols+`, match_attempts FROM items
		WHERE kind = 'movie' AND match_state = 'pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueItem
	for rows.Next() {
		var d DueItem
		var titleID sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Kind, &d.Title, &d.Year, &d.Show, &d.Season, &d.Episode,
			&d.Path, &d.Root, &d.Size, &d.MTime, &d.Container, &d.DurationSec, &d.Bitrate,
			&d.AddedAt, &d.UpdatedAt, &titleID, &d.MatchState, &d.NeedsReview,
			&d.LocalPoster, &d.LocalBackdrop, &d.LocalThumb, &d.Fingerprint, &d.Attempts); err != nil {
			return nil, err
		}
		d.TitleID = titleID.Int64
		out = append(out, d)
	}
	return out, rows.Err()
}

// DueSeries returns up to limit shows that are due for matching: pending with
// next_attempt_at at or before now, or with no series_matches row yet.
func (s *Store) DueSeries(ctx context.Context, now int64, limit int) ([]DueSeries, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.show, COALESCE(sm.match_attempts, 0),
			(SELECT path FROM items WHERE kind = 'episode' AND show = e.show ORDER BY id LIMIT 1)
		FROM (SELECT DISTINCT show FROM items WHERE kind = 'episode' AND show <> '') e
		LEFT JOIN series_matches sm ON sm.show = e.show
		WHERE sm.show IS NULL OR (sm.match_state = 'pending' AND sm.next_attempt_at <= ?)
		ORDER BY COALESCE(sm.next_attempt_at, 0), e.show LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueSeries
	for rows.Next() {
		var d DueSeries
		if err := rows.Scan(&d.Show, &d.Attempts, &d.Path); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetSeriesMatch returns the show's row, or ErrNotFound when it has none (a
// show nobody has looked at yet).
func (s *Store) GetSeriesMatch(ctx context.Context, show string) (SeriesMatch, error) {
	sm, err := scanSeriesMatch(s.db.QueryRowContext(ctx, `SELECT `+seriesCols+` FROM series_matches WHERE show = ?`, show))
	if errors.Is(err, sql.ErrNoRows) {
		return SeriesMatch{}, ErrNotFound
	}
	return sm, err
}

// SeriesMatches returns every show's row keyed by show name.
func (s *Store) SeriesMatches(ctx context.Context) (map[string]SeriesMatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+seriesCols+` FROM series_matches`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]SeriesMatch{}
	for rows.Next() {
		sm, err := scanSeriesMatch(rows)
		if err != nil {
			return nil, err
		}
		m[sm.Show] = sm
	}
	return m, rows.Err()
}

const seriesCols = `rowid, show, title_id, match_state, needs_review, match_score, match_attempts,
	next_attempt_at, local_poster, local_backdrop`

func scanSeriesMatch(sc scanner) (SeriesMatch, error) {
	var (
		sm      SeriesMatch
		titleID sql.NullInt64
	)
	err := sc.Scan(&sm.ID, &sm.Show, &titleID, &sm.State, &sm.NeedsReview, &sm.Score, &sm.Attempts,
		&sm.NextAttemptAt, &sm.LocalPoster, &sm.LocalBackdrop)
	sm.TitleID = titleID.Int64
	return sm, err
}

// MatchCounts is what the metadata status shows.
type MatchCounts struct {
	Pending     int // movies and shows waiting, due or not
	Unmatched   int
	NeedsReview int
}

// CountMatches counts movies and shows (shows with episodes only) by state.
func (s *Store) CountMatches(ctx context.Context) (c MatchCounts, err error) {
	err = s.snapshot(ctx, func(q queryer) error {
		for _, query := range []string{
			`SELECT
				COALESCE(SUM(match_state = 'pending'), 0),
				COALESCE(SUM(match_state = 'unmatched'), 0),
				COALESCE(SUM(needs_review), 0)
			FROM items WHERE kind = 'movie'`,
			// A show with no row yet is pending.
			`SELECT
				COALESCE(SUM(sm.show IS NULL OR sm.match_state = 'pending'), 0),
				COALESCE(SUM(sm.match_state = 'unmatched'), 0),
				COALESCE(SUM(sm.needs_review), 0)
			FROM (SELECT DISTINCT show FROM items WHERE kind = 'episode' AND show <> '') e
			LEFT JOIN series_matches sm ON sm.show = e.show`,
		} {
			var p, u, r int
			if err := q.QueryRowContext(ctx, query).Scan(&p, &u, &r); err != nil {
				return err
			}
			c.Pending += p
			c.Unmatched += u
			c.NeedsReview += r
		}
		return nil
	})
	return c, err
}

// Art is the sidecar artwork found next to a file; "" = none.
type Art struct {
	Poster   string
	Backdrop string
	Thumb    string
}

// SetItemArt records the item's sidecar artwork (absolute paths). An unchanged
// record is not written. It returns ErrNotFound for an unknown item.
func (s *Store) SetItemArt(ctx context.Context, itemID int64, a Art) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur Art
	err = tx.QueryRowContext(ctx, `SELECT local_poster, local_backdrop, local_thumb FROM items WHERE id = ?`,
		itemID).Scan(&cur.Poster, &cur.Backdrop, &cur.Thumb)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur == a {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE items SET local_poster = ?, local_backdrop = ?, local_thumb = ? WHERE id = ?`,
		a.Poster, a.Backdrop, a.Thumb, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSeriesArt records a show's folder artwork, creating its series_matches row
// when it has none. It returns ErrNotFound for a show without episodes.
func (s *Store) SetSeriesArt(ctx context.Context, show string, a Art) error {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO series_matches (show, local_poster, local_backdrop) SELECT ?, ?, ? WHERE `+hasEpisodes+`
		ON CONFLICT (show) DO UPDATE SET local_poster = excluded.local_poster,
			local_backdrop = excluded.local_backdrop
		WHERE local_poster <> excluded.local_poster OR local_backdrop <> excluded.local_backdrop`,
		show, a.Poster, a.Backdrop, show)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either unchanged or no such show: tell them apart.
		var one int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM series_matches WHERE show = ?`, show).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// Kinds of owner of local artwork, as in "local:<kind>:<id>:<slot>".
const (
	ArtItem   = "item"
	ArtSeries = "series"
)

// LocalArt returns the sidecar artwork path of an item (id = item id) or a
// show (id = SeriesMatch.ID) for slot "poster", "backdrop" or, for items,
// "thumb". It returns ErrNotFound when there is none.
func (s *Store) LocalArt(ctx context.Context, kind string, id int64, slot string) (string, error) {
	var query string
	switch {
	case kind == ArtItem && slot == "poster":
		query = `SELECT local_poster FROM items WHERE id = ?`
	case kind == ArtItem && slot == "backdrop":
		query = `SELECT local_backdrop FROM items WHERE id = ?`
	case kind == ArtItem && slot == "thumb":
		query = `SELECT local_thumb FROM items WHERE id = ?`
	case kind == ArtSeries && slot == "poster":
		query = `SELECT local_poster FROM series_matches WHERE rowid = ?`
	case kind == ArtSeries && slot == "backdrop":
		query = `SELECT local_backdrop FROM series_matches WHERE rowid = ?`
	default:
		return "", ErrNotFound
	}
	var path string
	err := s.db.QueryRowContext(ctx, query, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && path == "") {
		return "", ErrNotFound
	}
	return path, err
}
