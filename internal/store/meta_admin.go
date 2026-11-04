package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"
)

// GetSetting returns the stored value of key; ok is false when there is none.
func (s *Store) GetSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.ro.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting stores value under key, replacing any earlier value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.exec(ctx, prioHigh, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes key; a key that is not there is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.exec(ctx, prioHigh, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// SettingsWithPrefix returns the stored settings whose key starts with prefix, by key.
func (s *Store) SettingsWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT key, value FROM settings WHERE substr(key, 1, ?) = ?`,
		utf8.RuneCountInString(prefix), prefix) // substr counts characters
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PruneResult counts what PruneMetadata removed.
type PruneResult struct {
	Series      int // series_matches rows of shows with no episodes left
	Titles      int
	People      int
	Collections int
	Genres      int
}

// PruneMetadata removes metadata nothing refers to any more: shows without
// episodes, titles no movie file or show links to, then the people without
// credits, collections without titles and genres without titles that this
// leaves behind.
//
// One round is one transaction that takes up to writeChunk rows from each step
// (titleChunk titles: each one takes its credits, seasons and episodes with it).
// When a step has more, another round follows, until a round finds fewer than
// that everywhere: a prune after a big removal is done in chunks, not in one
// transaction. Each round leaves the database consistent (the steps run in order
// inside it), so an error after some rounds leaves a partial prune, and the
// result returned with the error counts what those rounds removed; calling again
// finishes it.
func (s *Store) PruneMetadata(ctx context.Context) (PruneResult, error) {
	var r PruneResult
	for {
		var round PruneResult // counted into r only once the round has committed
		more := false
		err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			// Order matters: each step can orphan the rows of the next. The
			// conditions are on x, the row looked at.
			for _, step := range []struct {
				n     *int
				table string
				where string
				limit int
			}{
				{&round.Series, "series_matches", `NOT EXISTS (
					SELECT 1 FROM items WHERE kind = 'episode' AND show = x.show)`, writeChunk},
				{&round.Titles, "titles", `NOT EXISTS (SELECT 1 FROM items WHERE title_id = x.id)
					AND NOT EXISTS (SELECT 1 FROM series_matches WHERE title_id = x.id)`, titleChunk},
				{&round.People, "people", `NOT EXISTS (SELECT 1 FROM credits WHERE person_id = x.id)`, writeChunk},
				{&round.Collections, "collections", `NOT EXISTS (SELECT 1 FROM titles WHERE collection_id = x.id)`, writeChunk},
				{&round.Genres, "genres", `NOT EXISTS (SELECT 1 FROM title_genres WHERE genre_id = x.id)`, writeChunk},
			} {
				n, err := deleteSome(ctx, tx, step.table, step.where, step.limit)
				if err != nil {
					return err
				}
				*step.n = n
				more = more || n == step.limit
			}
			return nil
		})
		if err != nil {
			return r, err
		}
		r.Series += round.Series
		r.Titles += round.Titles
		r.People += round.People
		r.Collections += round.Collections
		r.Genres += round.Genres
		if !more {
			return r, nil
		}
	}
}

// titleChunk is the most titles a prune round removes: a title cascades to its
// credits, seasons, episodes and genre links, a hundred rows or several thousand.
const titleChunk = 50

// deleteSome deletes up to limit rows of table that satisfy where (written in
// terms of x, the table), lowest rowid first, and returns how many.
func deleteSome(ctx context.Context, tx *sql.Tx, table, where string, limit int) (int, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid IN (
		SELECT x.rowid FROM `+table+` x WHERE `+where+` ORDER BY x.rowid LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
	rows, err := s.ro.QueryContext(ctx, `
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
	rows, err := s.ro.QueryContext(ctx, `
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
