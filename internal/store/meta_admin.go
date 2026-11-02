package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetSetting returns the stored value of key; ok is false when there is none.
func (s *Store) GetSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting stores value under key, replacing any earlier value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes key; a key that is not there is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// PruneResult counts what PruneMetadata removed.
type PruneResult struct {
	Series      int // series_matches rows of shows with no episodes left
	Titles      int
	People      int
	Collections int
	Genres      int
}

// PruneMetadata removes metadata nothing refers to any more, in one
// transaction: shows without episodes, titles no movie file or show links to,
// then the people without credits, collections without titles and genres
// without titles that this leaves behind.
func (s *Store) PruneMetadata(ctx context.Context) (PruneResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, err
	}
	defer tx.Rollback()

	var r PruneResult
	// Order matters: each step can orphan the rows of the next.
	for _, step := range []struct {
		n     *int
		query string
	}{
		{&r.Series, `DELETE FROM series_matches WHERE NOT EXISTS (
			SELECT 1 FROM items WHERE kind = 'episode' AND show = series_matches.show)`},
		{&r.Titles, `DELETE FROM titles WHERE NOT EXISTS (SELECT 1 FROM items WHERE title_id = titles.id)
			AND NOT EXISTS (SELECT 1 FROM series_matches WHERE title_id = titles.id)`},
		{&r.People, `DELETE FROM people WHERE NOT EXISTS (SELECT 1 FROM credits WHERE person_id = people.id)`},
		{&r.Collections, `DELETE FROM collections WHERE NOT EXISTS (
			SELECT 1 FROM titles WHERE collection_id = collections.id)`},
		{&r.Genres, `DELETE FROM genres WHERE NOT EXISTS (SELECT 1 FROM title_genres WHERE genre_id = genres.id)`},
	} {
		res, err := tx.ExecContext(ctx, step.query)
		if err != nil {
			return PruneResult{}, err
		}
		n, _ := res.RowsAffected()
		*step.n = int(n)
	}
	if err := tx.Commit(); err != nil {
		return PruneResult{}, err
	}
	return r, nil
}

// Refresh ages: how old a title's data may get before it is fetched again.
const (
	// RefreshActiveAge applies to shows that have not ended and to movies
	// released in the last RecentMovieWindow (or not yet released).
	RefreshActiveAge = 7 * 24 * time.Hour
	// RefreshIdleAge applies to everything else.
	RefreshIdleAge    = 30 * 24 * time.Hour
	RecentMovieWindow = 90 * 24 * time.Hour
	// AiringDelay is how long after a show's next_air_date it is refreshed, so
	// the new episode's name and still are there when its file lands.
	AiringDelay = 6 * time.Hour
)

// TitlesDueForRefresh returns up to limit titles of the library that have a
// TMDB id and whose data is older than their refresh age (RefreshActiveAge,
// RefreshIdleAge), or whose next episode aired AiringDelay ago or longer and
// has not been fetched since. The least recently fetched come first.
func (s *Store) TitlesDueForRefresh(ctx context.Context, now time.Time, limit int) ([]Title, error) {
	return s.dueTitles(ctx, now, limit, true)
}

// AiringTitlesDue is TitlesDueForRefresh for the airing rule alone: shows whose
// next episode aired AiringDelay ago or longer and that have not been fetched
// since. It is what still applies when a changes feed does the rest.
func (s *Store) AiringTitlesDue(ctx context.Context, now time.Time, limit int) ([]Title, error) {
	return s.dueTitles(ctx, now, limit, false)
}

func (s *Store) dueTitles(ctx context.Context, now time.Time, limit int, byAge bool) ([]Title, error) {
	delay := int64(AiringDelay / time.Second)
	cond := `(t.kind = 'series' AND COALESCE(t.next_air_date, '') <> ''
			AND strftime('%s', t.next_air_date) + ? <= ?
			AND t.fetched_at < strftime('%s', t.next_air_date) + ?)`
	args := []any{delay, now.Unix(), delay}
	if byAge {
		cond = `t.fetched_at < CASE
					WHEN (t.kind = 'series' AND t.status NOT IN ('Ended', 'Canceled'))
						OR (t.kind = 'movie' AND t.release_date >= ?) THEN ?
					ELSE ? END
				OR ` + cond
		args = append([]any{now.Add(-RecentMovieWindow).Format("2006-01-02"),
			now.Add(-RefreshActiveAge).Unix(), now.Add(-RefreshIdleAge).Unix()}, args...)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prefixed("t.", titleCols)+` FROM titles t
		WHERE t.tmdb_id IS NOT NULL AND `+libraryTitle+` AND (`+cond+`)
		ORDER BY t.fetched_at, t.id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Title
	for rows.Next() {
		t, err := scanTitle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TitleRef identifies a title of the library at TMDB.
type TitleRef struct {
	ID     int64
	Kind   string
	TMDBID int64
	Locked bool
}

// LibraryTitleRefs returns every title of the library that has a TMDB id, for
// intersecting with a changes feed.
func (s *Store) LibraryTitleRefs(ctx context.Context) ([]TitleRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.kind, t.tmdb_id, t.locked FROM titles t
		WHERE t.tmdb_id IS NOT NULL AND `+libraryTitle+` ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TitleRef
	for rows.Next() {
		var r TitleRef
		if err := rows.Scan(&r.ID, &r.Kind, &r.TMDBID, &r.Locked); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
