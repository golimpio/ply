package store

import (
	"context"
	"time"
)

// MoveItem points an existing item at the file it was moved or renamed to,
// keeping its id and so everything that hangs off it (streams, progress,
// matches, edits). It updates what comes from the path (path, root, kind,
// title, year, show, season, episode) and the mtime, which a move does not
// always keep. ErrNotFound if there is no such item.
func (s *Store) MoveItem(ctx context.Context, id int64, it *Item) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE items SET path = ?, root = ?, kind = ?, title = ?, year = ?, show = ?, season = ?, episode = ?,
			mtime = ?, updated_at = ?
		WHERE id = ?`,
		it.Path, it.Root, it.Kind, it.Title, it.Year, it.Show, it.Season, it.Episode, it.MTime,
		time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PathsWithFingerprint returns the paths of the items with this size and
// content fingerprint.
func (s *Store) PathsWithFingerprint(ctx context.Context, size int64, fingerprint string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM items WHERE size = ? AND fingerprint = ?`, size, fingerprint)
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
	rows, err := s.db.QueryContext(ctx, `
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
// still the file it was computed from (same size and mtime).
func (s *Store) SetFingerprint(ctx context.Context, id, size, mtime int64, fingerprint string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE items SET fingerprint = ? WHERE id = ? AND size = ? AND mtime = ?`,
		fingerprint, id, size, mtime)
	return err
}
