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

// Entry is an item with its streams. GetEntry also fills External (sidecar
// subtitles, ordered by path); GetEntry, ListItems and Continue fill Progress
// (nil when the item has none).
type Entry struct {
	Item
	Streams  []probe.Stream
	External []ExternalSubtitle
	Progress *Progress
}

// FileState is what the scanner needs to decide whether a file changed, plus
// the sidecar subtitles it recorded last time.
type FileState struct {
	ID    int64
	Root  string
	Size  int64
	MTime int64
	Subs  []ExternalSubtitle
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
	e := Entry{Item: it}
	if e.Streams, err = s.itemStreams(ctx, id); err != nil {
		return Entry{}, err
	}
	if e.External, err = s.externalSubtitles(ctx, id); err != nil {
		return Entry{}, err
	}
	if e.Progress, err = s.getProgress(ctx, id); err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *Store) itemStreams(ctx context.Context, id int64) ([]probe.Stream, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+streamCols+` FROM streams WHERE item_id = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var streams []probe.Stream
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		streams = append(streams, st)
	}
	return streams, rows.Err()
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
	defer rows.Close() // idempotent: also closed explicitly below
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
	err = rows.Err()
	rows.Close() // before the next query: the pool has one connection
	if err != nil {
		return nil, err
	}

	progress, err := s.allProgress(ctx)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Progress = progress[entries[i].ID]
	}
	return entries, nil
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
	defer rows.Close() // idempotent: also closed explicitly below
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	subs, err := s.allExternalSubtitles(ctx)
	if err != nil {
		return nil, err
	}
	for path, st := range states {
		if list, ok := subs[st.ID]; ok {
			st.Subs = list
			states[path] = st
		}
	}
	return states, nil
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

// SetItemRoot records which configured media root the item belongs to,
// without touching anything else (the scanner uses it when a file is found
// under a different root than the one it was indexed with).
func (s *Store) SetItemRoot(ctx context.Context, id int64, root string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE items SET root = ? WHERE id = ?`, root, id)
	return err
}

// CountItems returns the number of items in the library.
func (s *Store) CountItems(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n)
	return n, err
}
