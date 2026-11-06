package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"time"
)

// MoveItem points an existing item at the file it was moved or renamed to,
// keeping its id and so everything that hangs off it (streams, progress, edits).
// It updates what comes from the path (path, root, kind, title, year, show, season,
// episode) and the mtime, which a move does not always keep.
//
// A move that leaves the item the kind it was, in the show it was, keeps its
// matches. One that changes its kind (a movie moved to a shows folder is an episode
// now), or the show of an episode, is a re-reading like SetParsed's, with its rules:
// a match that is not manual is cleared, a manual one that no longer fits goes to
// the archive under the new path (the folder's type given back restores it), and
// the series rows are cleaned.
//
// needs_review follows the reading of an episode's name (it.NeedsReview) and is
// cleared when an episode becomes a movie; a movie's flag is the matcher's and is
// left alone. What a removed folder archived for the new path belongs to the file
// that was there, not to this one: it is dropped. ErrNotFound if there is no such
// item.
func (s *Store) MoveItem(ctx context.Context, id int64, it *Item) error {
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		var oldKind, oldShow string
		err := tx.QueryRowContext(ctx, `SELECT kind, show FROM items WHERE id = ?`, id).Scan(&oldKind, &oldShow)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE items SET path = ?, root = ?, mtime = ?, updated_at = ? WHERE id = ?`,
			it.Path, it.Root, it.MTime, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_archive WHERE path = ?`, it.Path); err != nil {
			return err
		}
		if oldKind != it.Kind || (it.Kind == KindEpisode && oldShow != it.Show) {
			shows := map[string]bool{}
			if err := setParsedRow(ctx, tx, ParsedRow{ID: id, Kind: it.Kind, Title: it.Title, Year: it.Year, Show: it.Show,
				Season: it.Season, Episode: it.Episode, NeedsReview: it.NeedsReview}, now, shows); err != nil {
				return err
			}
			names := slices.Collect(maps.Keys(shows))
			if err := cleanSeries(ctx, tx, names, false); err != nil {
				return err
			}
			return cleanSeries(ctx, tx, names, true)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE items SET title = ?, year = ?, season = ?, episode = ?,
				needs_review = CASE WHEN ? = 'episode' THEN ? ELSE needs_review END
			WHERE id = ?`,
			it.Title, it.Year, it.Season, it.Episode, it.Kind, it.NeedsReview, id)
		return err
	})
}

// PathsWithFingerprint returns the paths of the items with this size and
// content fingerprint.
func (s *Store) PathsWithFingerprint(ctx context.Context, size int64, fingerprint string) ([]string, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT path FROM items WHERE size = ? AND fingerprint = ?`, size, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Unfingerprinted is an item indexed before fingerprints existed.
type Unfingerprinted struct {
	ID    int64
	Path  string
	Size  int64
	MTime int64
}

// WithoutFingerprint returns up to limit items with an id above afterID that
// have no fingerprint yet, in id order.
func (s *Store) WithoutFingerprint(ctx context.Context, afterID int64, limit int) ([]Unfingerprinted, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT id, path, size, mtime FROM items WHERE fingerprint = '' AND id > ? ORDER BY id LIMIT ?`,
		afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unfingerprinted
	for rows.Next() {
		var u Unfingerprinted
		if err := rows.Scan(&u.ID, &u.Path, &u.Size, &u.MTime); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetFingerprint records the item's fingerprint, but only while the item is
// still the file it was computed from (same size and mtime). No list shows it:
// it leaves Generation alone.
func (s *Store) SetFingerprint(ctx context.Context, id, size, mtime int64, fingerprint string) error {
	_, err := s.execQuiet(ctx, prioLow, `UPDATE items SET fingerprint = ? WHERE id = ? AND size = ? AND mtime = ?`,
		fingerprint, id, size, mtime)
	return err
}
