package store

import (
	"context"
	"database/sql"
	"slices"
)

// ExternalSubtitle is a sidecar subtitle file next to an item's video.
type ExternalSubtitle struct {
	Path     string // raw path, as reported by the OS
	Language string // "en", "pt-br"; empty when the file name has none
	Format   string // srt | vtt | ass
	Forced   bool
}

// SetExternalSubtitles replaces the item's sidecar subtitles. Unchanged lists
// are not written, so a rescan of an untouched library does not write at all (and
// does not wait for the writer: the first look is a read).
func (s *Store) SetExternalSubtitles(ctx context.Context, itemID int64, subs []ExternalSubtitle) error {
	subs = slices.Clone(subs)
	sortSubtitles(subs)
	current, err := externalSubtitles(ctx, s.ro, itemID)
	if err != nil {
		return err
	}
	if slices.Equal(current, subs) {
		return nil
	}
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		// Looked at again where nothing can write between the look and the change.
		current, err := externalSubtitles(ctx, tx, itemID)
		if err != nil {
			return err
		}
		if slices.Equal(current, subs) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM subtitles_external WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		for _, sub := range subs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO subtitles_external (item_id, path, language, format, forced)
				SELECT id, ?, ?, ?, ? FROM items WHERE id = ?`,
				sub.Path, sub.Language, sub.Format, sub.Forced, itemID); err != nil {
				return err
			}
		}
		return nil
	})
}

func sortSubtitles(subs []ExternalSubtitle) {
	slices.SortFunc(subs, func(a, b ExternalSubtitle) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		}
		return 0
	})
}

// allExternalSubtitles returns every item's sidecars, each list ordered by path.
func allExternalSubtitles(ctx context.Context, q queryer) (map[int64][]ExternalSubtitle, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT item_id, path, language, format, forced FROM subtitles_external ORDER BY item_id, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64][]ExternalSubtitle{}
	for rows.Next() {
		var (
			id  int64
			sub ExternalSubtitle
		)
		if err := rows.Scan(&id, &sub.Path, &sub.Language, &sub.Format, &sub.Forced); err != nil {
			return nil, err
		}
		m[id] = append(m[id], sub)
	}
	return m, rows.Err()
}

// externalSubtitles returns the item's sidecars ordered by path, which is the
// order of their ext<N> keys.
func externalSubtitles(ctx context.Context, q queryer, itemID int64) ([]ExternalSubtitle, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT path, language, format, forced FROM subtitles_external
		WHERE item_id = ? ORDER BY path`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subs []ExternalSubtitle
	for rows.Next() {
		var sub ExternalSubtitle
		if err := rows.Scan(&sub.Path, &sub.Language, &sub.Format, &sub.Forced); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
