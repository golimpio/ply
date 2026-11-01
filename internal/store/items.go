package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"ply/internal/probe"
)

const (
	KindMovie   = "movie"
	KindEpisode = "episode"
)

// Item is one library file. For episodes Title is the episode title (possibly
// empty); for movies Show, Season and Episode are zero.
type Item struct {
	ID          int64
	Kind        string
	Title       string
	Year        int // 0 = unknown
	Show        string
	Season      int
	Episode     int
	Path        string // raw bytes as reported by the OS
	Root        string
	Size        int64
	MTime       int64 // unix nanoseconds
	Container   string
	DurationSec float64
	Bitrate     int64
	AddedAt     int64 // unix seconds
	UpdatedAt   int64
}

// Entry is an item with its streams.
type Entry struct {
	Item
	Streams []probe.Stream
}

// FileState is what the scanner needs to decide whether a file changed.
type FileState struct {
	ID    int64
	Root  string
	Size  int64
	MTime int64
}

// ListFilter narrows ListItems. Empty fields match everything; Show matches
// case-insensitively.
type ListFilter struct {
	Kind string
	Show string
}

// Show summarises the episodes of one show.
type Show struct {
	Name         string
	Seasons      []int
	EpisodeCount int
}

const itemCols = `id, kind, title, year, show, season, episode, path, root, size, mtime,
	container, duration_sec, bitrate, added_at, updated_at`

const streamCols = `idx, type, codec, codec_tag, profile, width, height, pix_fmt, color_transfer,
	channels, language, title, is_default, forced, dovi`

type scanner interface{ Scan(dest ...any) error }

func scanItem(sc scanner) (Item, error) {
	var it Item
	err := sc.Scan(&it.ID, &it.Kind, &it.Title, &it.Year, &it.Show, &it.Season, &it.Episode,
		&it.Path, &it.Root, &it.Size, &it.MTime, &it.Container, &it.DurationSec, &it.Bitrate,
		&it.AddedAt, &it.UpdatedAt)
	return it, err
}

func scanStream(sc scanner) (probe.Stream, error) {
	var s probe.Stream
	err := sc.Scan(&s.Index, &s.Type, &s.Codec, &s.CodecTag, &s.Profile, &s.Width, &s.Height,
		&s.PixFmt, &s.ColorTransfer, &s.Channels, &s.Language, &s.Title, &s.Default, &s.Forced,
		&s.DolbyVision)
	return s, err
}

// UpsertItem inserts the item, or updates the existing row with the same Path
// (keeping its id and added_at), and replaces its streams. It returns the id.
func (s *Store) UpsertItem(ctx context.Context, it *Item, streams []probe.Stream) (int64, error) {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO items (kind, title, year, show, season, episode, path, root, size, mtime,
			container, duration_sec, bitrate, added_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET
			kind = excluded.kind, title = excluded.title, year = excluded.year,
			show = excluded.show, season = excluded.season, episode = excluded.episode,
			root = excluded.root, size = excluded.size, mtime = excluded.mtime,
			container = excluded.container, duration_sec = excluded.duration_sec,
			bitrate = excluded.bitrate, updated_at = excluded.updated_at
		RETURNING id`,
		it.Kind, it.Title, it.Year, it.Show, it.Season, it.Episode, it.Path, it.Root, it.Size, it.MTime,
		it.Container, it.DurationSec, it.Bitrate, now, now).Scan(&id)
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM streams WHERE item_id = ?`, id); err != nil {
		return 0, err
	}
	// The file changed (the scanner only upserts changed files), so the
	// cached keyframe index may no longer describe it.
	if _, err := tx.ExecContext(ctx, `DELETE FROM keyframes WHERE item_id = ?`, id); err != nil {
		return 0, err
	}
	for _, st := range streams {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO streams (item_id, `+streamCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, st.Index, st.Type, st.Codec, st.CodecTag, st.Profile, st.Width, st.Height,
			st.PixFmt, st.ColorTransfer, st.Channels, st.Language, st.Title, st.Default, st.Forced,
			st.DolbyVision); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	it.ID = id
	return id, nil
}

// GetEntry returns the item and its streams, or ErrNotFound.
func (s *Store) GetEntry(ctx context.Context, id int64) (Entry, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+streamCols+` FROM streams WHERE item_id = ? ORDER BY idx`, id)
	if err != nil {
		return Entry{}, err
	}
	defer rows.Close()
	e := Entry{Item: it}
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return Entry{}, err
		}
		e.Streams = append(e.Streams, st)
	}
	return e, rows.Err()
}

// ListItems returns matching items with their streams. Movies come first,
// ordered by title; then episodes ordered by show, season, episode.
func (s *Store) ListItems(ctx context.Context, f ListFilter) ([]Entry, error) {
	var (
		where []string
		args  []any
	)
	if f.Kind != "" {
		where = append(where, "i.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Show != "" {
		where = append(where, "i.show = ? COLLATE NOCASE")
		args = append(args, f.Show)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	// The pool has one connection: finish reading each result set before
	// starting the next query.
	items, err := s.queryItems(ctx, `SELECT i.id, i.kind, i.title, i.year, i.show, i.season, i.episode,
			i.path, i.root, i.size, i.mtime, i.container, i.duration_sec, i.bitrate, i.added_at, i.updated_at
		FROM items i`+clause+`
		ORDER BY i.kind DESC,
			LOWER(CASE i.kind WHEN 'movie' THEN i.title ELSE i.show END),
			i.season, i.episode, LOWER(i.title), i.id`, args)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]*Entry, len(items))
	entries := make([]Entry, len(items))
	for i, it := range items {
		entries[i].Item = it
		byID[it.ID] = &entries[i]
	}

	rows, err := s.db.QueryContext(ctx, `SELECT s.item_id, `+prefixed("s.", streamCols)+`
		FROM streams s JOIN items i ON i.id = s.item_id`+clause+`
		ORDER BY s.item_id, s.idx`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			itemID int64
			st     probe.Stream
		)
		if err := rows.Scan(&itemID, &st.Index, &st.Type, &st.Codec, &st.CodecTag, &st.Profile,
			&st.Width, &st.Height, &st.PixFmt, &st.ColorTransfer, &st.Channels, &st.Language,
			&st.Title, &st.Default, &st.Forced, &st.DolbyVision); err != nil {
			return nil, err
		}
		if e := byID[itemID]; e != nil {
			e.Streams = append(e.Streams, st)
		}
	}
	return entries, rows.Err()
}

func (s *Store) queryItems(ctx context.Context, query string, args []any) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// prefixed prepends prefix to every comma-separated column in cols.
func prefixed(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// Shows lists shows (episodes grouped by show name, ordered by name) with
// their seasons.
func (s *Store) Shows(ctx context.Context) ([]Show, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT show, season, COUNT(*) FROM items WHERE kind = 'episode'
		GROUP BY show, season ORDER BY LOWER(show), show, season`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shows []Show
	for rows.Next() {
		var (
			name   string
			season int
			n      int
		)
		if err := rows.Scan(&name, &season, &n); err != nil {
			return nil, err
		}
		if len(shows) == 0 || shows[len(shows)-1].Name != name {
			shows = append(shows, Show{Name: name})
		}
		last := &shows[len(shows)-1]
		last.Seasons = append(last.Seasons, season)
		last.EpisodeCount += n
	}
	return shows, rows.Err()
}

// FileStates returns every item's change-detection state keyed by raw path.
func (s *Store) FileStates(ctx context.Context) (map[string]FileState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, id, root, size, mtime FROM items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]FileState{}
	for rows.Next() {
		var (
			path string
			st   FileState
		)
		if err := rows.Scan(&path, &st.ID, &st.Root, &st.Size, &st.MTime); err != nil {
			return nil, err
		}
		states[path] = st
	}
	return states, rows.Err()
}

// DeleteItems removes the items (and, by cascade, their streams and
// dependent rows).
func (s *Store) DeleteItems(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CountItems returns the number of items in the library.
func (s *Store) CountItems(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n)
	return n, err
}
