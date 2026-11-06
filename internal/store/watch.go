package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
)

// Watch state (docs/DESIGN.md, "Watch state"). The one place it is kept is the
// progress row of a file (see Progress). Whether a film, a season or a show is
// watched is read off its files, never stored a second time:
//
//   - an episode is watched when any of its files is (two copies of an episode are
//     one episode), and a film that no title is known for when its file is;
//   - a film with a title is watched when any file of the title is (a second copy of
//     a film that was seen is a film that was seen), so marking it, or unmarking it,
//     is done to all of its files;
//   - a season is watched when all of its episodes are, and a show when all its
//     episodes but the specials are; the counts say how far it is.
//
// The one thing kept besides the files is a mark (title_watch), for the time between a
// watched file going and its replacement arriving: see markWatchedFiles.

// EpisodeCounts says how far through its episodes a show, a season or a series is.
// Episodes are counted, not files: an episode with two files is one.
type EpisodeCounts struct {
	EpisodeCount int // episodes in the library
	Watched      int // of them, watched (in any of their files)
	InProgress   int // of them, started and not watched
}

// Done reports whether there are episodes and all of them are watched.
func (c EpisodeCounts) Done() bool { return c.EpisodeCount > 0 && c.Watched == c.EpisodeCount }

// totalOf is the counts of a show or a series from those of its seasons. The specials
// (season 0) are not counted in it, so that a show whose regular episodes are all
// watched is watched; a show that has nothing but specials is counted by them.
func totalOf(bySeason map[int]EpisodeCounts) EpisodeCounts {
	var t EpisodeCounts
	for season, c := range bySeason {
		if season != 0 {
			t.EpisodeCount += c.EpisodeCount
			t.Watched += c.Watched
			t.InProgress += c.InProgress
		}
	}
	if t.EpisodeCount == 0 {
		return bySeason[0]
	}
	return t
}

// episodeCountsSQL counts the episodes, per group (an expression of the from clause) and season: an
// episode is a (show or series, season, episode number), whatever number of files it is on, and is
// watched when any of its files is, and started when none is watched and one is started. Files
// that have no number in their name are episodes of their own. from names the items as i and
// their progress as p. The columns are group, season, episodes, watched, started.
func episodeCountsSQL(group, from, where string) string {
	const number = `CASE WHEN i.episode = 0 THEN -i.id ELSE i.episode END`
	return `
		SELECT g, season, COUNT(*), SUM(w), SUM(CASE WHEN w = 0 AND s = 1 THEN 1 ELSE 0 END) FROM (
			SELECT ` + group + ` AS g, i.season AS season,
				MAX(CASE WHEN p.watched_at <> 0 THEN 1 ELSE 0 END) AS w,
				MAX(CASE WHEN p.item_id IS NOT NULL THEN 1 ELSE 0 END) AS s
			` + from + ` WHERE ` + where + `
			GROUP BY ` + group + `, i.season, ` + number + `
		) GROUP BY g, season`
}

// FilmWatch is the watch state of a film: that of its files together.
type FilmWatch struct {
	WatchedAt int64 // the latest watch of any of its files (unix seconds)
	PlayCount int   // the highest count of any of its files
}

// WatchedItem is a file that a change of watch state covered, with its progress
// afterwards: nil when it has none (not started, not watched).
type WatchedItem struct {
	ID       int64
	Progress *Progress
}

// SetItemWatched marks an item watched or not watched. For a film that has a
// title it is the film that is marked: all the files of the title (see
// SetTitleWatched). It returns the files it covered, or ErrNotFound.
func (s *Store) SetItemWatched(ctx context.Context, itemID int64, watched bool) ([]WatchedItem, error) {
	return s.setWatched(ctx, watched, func(tx *sql.Tx) ([]int64, error) {
		var (
			kind    string
			titleID sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `SELECT kind, title_id FROM items WHERE id = ?`, itemID).Scan(&kind, &titleID)
		if err != nil {
			return nil, notFound(err)
		}
		if kind == KindMovie && titleID.Valid {
			return queryIDs(ctx, tx, `SELECT id FROM items WHERE kind = 'movie' AND title_id = ? ORDER BY id`, titleID.Int64)
		}
		return []int64{itemID}, nil
	}, unmarkFiles)
}

// SetTitleWatched marks every file of a title watched or not watched: a film's
// versions, or all the episodes of the shows a series is linked to. It returns the
// files it covered, or ErrNotFound when the title has none.
func (s *Store) SetTitleWatched(ctx context.Context, titleID int64, watched bool) ([]WatchedItem, error) {
	return s.setWatched(ctx, watched, func(tx *sql.Tx) ([]int64, error) {
		return queryIDs(ctx, tx, `
			SELECT id FROM items WHERE kind = 'movie' AND title_id = ?
			UNION
			SELECT i.id FROM series_matches sm JOIN items i ON i.kind = 'episode' AND i.show = sm.show
			WHERE sm.title_id = ?
			ORDER BY 1`, titleID, titleID)
	}, func(ctx context.Context, tx *sql.Tx, _ []int64) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM title_watch WHERE title_id = ?`, titleID)
		return err
	})
}

// SetShowWatched marks every episode of a show (by its name in the library)
// watched or not watched. It returns the episodes it covered, or ErrNotFound.
func (s *Store) SetShowWatched(ctx context.Context, show string, watched bool) ([]WatchedItem, error) {
	return s.setWatched(ctx, watched, func(tx *sql.Tx) ([]int64, error) {
		return queryIDs(ctx, tx, `SELECT id FROM items WHERE kind = 'episode' AND show = ? ORDER BY season, episode, id`, show)
	}, func(ctx context.Context, tx *sql.Tx, _ []int64) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM title_watch WHERE title_id IN (
			SELECT title_id FROM series_matches WHERE show = ? AND title_id IS NOT NULL)`, show)
		return err
	})
}

// SetSeasonWatched is SetShowWatched for one season of the show.
func (s *Store) SetSeasonWatched(ctx context.Context, show string, season int, watched bool) ([]WatchedItem, error) {
	return s.setWatched(ctx, watched, func(tx *sql.Tx) ([]int64, error) {
		return queryIDs(ctx, tx, `SELECT id FROM items WHERE kind = 'episode' AND show = ? AND season = ? ORDER BY episode, id`, show, season)
	}, func(ctx context.Context, tx *sql.Tx, _ []int64) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM title_watch WHERE season = ? AND title_id IN (
			SELECT title_id FROM series_matches WHERE show = ? AND title_id IS NOT NULL)`, season, show)
		return err
	})
}

// setWatched finds the files with pick and changes them in the same transaction, so
// that a file that arrives meanwhile is not left out of the title it belongs to.
//
// Watched: each file gets a row with position 0. A file that was watched keeps the
// time and the count it had (marking twice, or marking a season of which some
// episodes were seen, does not rewrite their history); the others are watched now,
// once. Not watched: the rows go, positions included, and so do the marks that a
// watched file that went left for its replacement (unmark), or it would come back
// watched.
func (s *Store) setWatched(ctx context.Context, watched bool, pick func(tx *sql.Tx) ([]int64, error),
	unmark func(ctx context.Context, tx *sql.Tx, ids []int64) error) (out []WatchedItem, err error) {
	err = s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		ids, err := pick(tx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNotFound
		}
		now := time.Now()
		for chunk := range slices.Chunk(ids, writeChunk) {
			in, args := placeholders(len(chunk)), idArgs(chunk)
			if !watched {
				if _, err := tx.ExecContext(ctx, `DELETE FROM progress WHERE item_id IN (`+in+`)`, args...); err != nil {
					return err
				}
				continue
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO progress (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
				SELECT id, 0, duration_sec, ?, 1, ? FROM items WHERE id IN (`+in+`)
				ON CONFLICT (item_id) DO UPDATE SET position_sec = 0, updated_at = excluded.updated_at,
					watched_at = CASE WHEN progress.watched_at = 0 THEN excluded.watched_at ELSE progress.watched_at END,
					play_count = MAX(progress.play_count, 1)`,
				append([]any{now.Unix(), now.UnixNano()}, args...)...)
			if err != nil {
				return err
			}
		}
		if !watched {
			if err := unmark(ctx, tx, ids); err != nil {
				return err
			}
		}
		out = make([]WatchedItem, len(ids))
		for i, id := range ids {
			out[i].ID = id
		}
		return fillProgress(ctx, tx, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fillProgress reads the progress of the items, which are in the order of their ids.
func fillProgress(ctx context.Context, q queryer, items []WatchedItem) error {
	at := make(map[int64]int, len(items))
	for i, it := range items {
		at[it.ID] = i
	}
	for chunk := range slices.Chunk(items, writeChunk) {
		ids := make([]int64, len(chunk))
		for i, it := range chunk {
			ids[i] = it.ID
		}
		rows, err := q.QueryContext(ctx, `SELECT item_id, `+progressCols+` FROM progress WHERE item_id IN (`+placeholders(len(ids))+`)`, idArgs(ids)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				id int64
				p  Progress
			)
			if err := rows.Scan(&id, &p.PositionSec, &p.DurationSec, &p.WatchedAt, &p.PlayCount, &p.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			items[at[id]].Progress = &p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// queryIDs reads a column of ids.
func queryIDs(ctx context.Context, q queryer, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// notFound turns the error of a QueryRow that found nothing into ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// FilmWatches returns the watch state of the films among the titles, keyed by title
// id; a film that is not watched has no entry.
func (s *Store) FilmWatches(ctx context.Context, titleIDs []int64) (m map[int64]FilmWatch, err error) {
	err = s.snapshot(ctx, func(q queryer) (err error) {
		m, err = filmWatches(ctx, q, titleIDs)
		return err
	})
	return m, err
}

func filmWatches(ctx context.Context, q queryer, titleIDs []int64) (map[int64]FilmWatch, error) {
	m := map[int64]FilmWatch{}
	for chunk := range slices.Chunk(titleIDs, briefChunk) {
		rows, err := q.QueryContext(ctx, `
			SELECT i.title_id, MAX(p.watched_at), MAX(p.play_count)
			FROM items i JOIN progress p ON p.item_id = i.id
			WHERE i.kind = 'movie' AND p.watched_at <> 0 AND i.title_id IN (`+placeholders(len(chunk))+`)
			GROUP BY i.title_id`, idArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				id int64
				w  FilmWatch
			)
			if err := rows.Scan(&id, &w.WatchedAt, &w.PlayCount); err != nil {
				rows.Close()
				return nil, err
			}
			m[id] = w
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

// seriesCounts returns the episode counts of the series among the titles, keyed by
// title id: every episode of every show linked to the title (an episode that two of
// them have is one), the specials left out (see totalOf). A series with no episode
// files has no entry.
func seriesCounts(ctx context.Context, q queryer, titleIDs []int64) (map[int64]EpisodeCounts, error) {
	bySeason := map[int64]map[int]EpisodeCounts{}
	for chunk := range slices.Chunk(titleIDs, briefChunk) {
		rows, err := q.QueryContext(ctx, episodeCountsSQL("sm.title_id", `FROM series_matches sm
				JOIN items i ON i.kind = 'episode' AND i.show = sm.show
				LEFT JOIN progress p ON p.item_id = i.id`, `sm.title_id IN (`+placeholders(len(chunk))+`)`), idArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				id     int64
				season int
				c      EpisodeCounts
			)
			if err := rows.Scan(&id, &season, &c.EpisodeCount, &c.Watched, &c.InProgress); err != nil {
				rows.Close()
				return nil, err
			}
			if bySeason[id] == nil {
				bySeason[id] = map[int]EpisodeCounts{}
			}
			bySeason[id][season] = c
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	m := make(map[int64]EpisodeCounts, len(bySeason))
	for id, seasons := range bySeason {
		m[id] = totalOf(seasons)
	}
	return m, nil
}

// attachWatch fills the watch state of the cards: a film is Watched when any of its
// files is; a series has its episode counts, and is Watched when all its episodes are.
func attachWatch(ctx context.Context, q queryer, cards []TitleCard) error {
	var films, series []int64
	seen := map[int64]bool{}
	for _, c := range cards {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if c.Kind == KindSeries {
			series = append(series, c.ID)
		} else {
			films = append(films, c.ID)
		}
	}
	fw, err := filmWatches(ctx, q, films)
	if err != nil {
		return err
	}
	sc, err := seriesCounts(ctx, q, series)
	if err != nil {
		return err
	}
	for i := range cards {
		if cards[i].Kind == KindSeries {
			c := sc[cards[i].ID]
			cards[i].Episodes, cards[i].Watched = &c, c.Done()
			continue
		}
		_, cards[i].Watched = fw[cards[i].ID]
	}
	return nil
}

// ---- marks: a watched file that went, for the file that replaces it ----

// A watched film or episode whose file is deleted (DeleteItems: it is gone from the
// disk; a replacement, such as an upgrade at another path, arrives under another name)
// leaves a mark in title_watch, by the title it was linked to (an episode's is its
// show's) and, for an episode, its season and number. A file linked to that title later
// (a film), or in that show with that number (an episode), is given the watch state
// of the mark, and the mark goes: this is done at the end of every write
// (applyWatchMarks), so that every way a file gets a title is covered, and a copy of the
// file that is still there gets it at once. Unmarking a title, a season or a show
// deletes its marks too, or what was unmarked would come back watched. Marks do not
// outlive markTTL, and keep their title from PruneMetadata while they last.
//
// Removing a library folder does not leave marks: the archive keeps what a person made
// of its files (by path), and a folder removed with "forget" is forgotten.
const markTTL = 90 * 24 * time.Hour

// markWatchedFiles leaves a mark for each of the watched files among ids, which are
// about to be deleted, in the same transaction.
func (s *Store) markWatchedFiles(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) error {
	if !s.marks {
		return nil
	}
	const upsert = ` ON CONFLICT (title_id, season, episode) DO UPDATE SET
		watched_at = MAX(title_watch.watched_at, excluded.watched_at),
		play_count = MAX(title_watch.play_count, excluded.play_count), removed_at = excluded.removed_at`
	for chunk := range slices.Chunk(ids, writeChunk) {
		in := placeholders(len(chunk))
		args := append([]any{now.Unix()}, idArgs(chunk)...)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO title_watch (title_id, season, episode, watched_at, play_count, removed_at)
			SELECT i.title_id, 0, 0, p.watched_at, p.play_count, ?
			FROM items i JOIN progress p ON p.item_id = i.id
			WHERE i.id IN (`+in+`) AND i.kind = 'movie' AND i.title_id IS NOT NULL AND p.watched_at <> 0`+upsert, args...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO title_watch (title_id, season, episode, watched_at, play_count, removed_at)
			SELECT sm.title_id, i.season, i.episode, p.watched_at, p.play_count, ?
			FROM items i JOIN progress p ON p.item_id = i.id
			JOIN series_matches sm ON sm.show = i.show AND sm.title_id IS NOT NULL
			WHERE i.id IN (`+in+`) AND i.kind = 'episode' AND i.episode > 0 AND p.watched_at <> 0`+upsert, args...); err != nil {
			return err
		}
	}
	return nil
}

// applyWatchMarks gives the files that have a mark waiting for them its watch state, and
// deletes the mark. A file that was watched, or is being watched again, keeps its own time
// and position; the count is the larger one. Cheap when there are no marks, which is
// nearly always.
func (s *Store) applyWatchMarks(ctx context.Context, tx *sql.Tx) error {
	if !s.marks {
		return nil
	}
	var waiting int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM title_watch)`).Scan(&waiting); err != nil || waiting == 0 {
		return err
	}
	const give = `
		ON CONFLICT (item_id) DO UPDATE SET
			watched_at = CASE WHEN progress.watched_at = 0 THEN excluded.watched_at ELSE progress.watched_at END,
			play_count = MAX(progress.play_count, excluded.play_count)`
	for _, q := range []string{
		// A film: the files linked to the title.
		`INSERT INTO progress (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
			SELECT i.id, 0, i.duration_sec, tw.watched_at, tw.play_count, tw.watched_at * 1000000000
			FROM title_watch tw JOIN items i ON i.kind = 'movie' AND i.title_id = tw.title_id
			WHERE tw.episode = 0` + give,
		`DELETE FROM title_watch WHERE episode = 0 AND EXISTS (
			SELECT 1 FROM items i WHERE i.kind = 'movie' AND i.title_id = title_watch.title_id)`,
		// An episode: the files of that number in the shows linked to the title.
		`INSERT INTO progress (item_id, position_sec, duration_sec, watched_at, play_count, updated_at)
			SELECT e.id, 0, e.duration_sec, tw.watched_at, tw.play_count, tw.watched_at * 1000000000
			FROM title_watch tw JOIN series_matches sm ON sm.title_id = tw.title_id
			JOIN items e ON e.kind = 'episode' AND e.show = sm.show AND e.season = tw.season AND e.episode = tw.episode
			WHERE tw.episode > 0` + give,
		`DELETE FROM title_watch WHERE episode > 0 AND EXISTS (
			SELECT 1 FROM series_matches sm JOIN items e ON e.kind = 'episode' AND e.show = sm.show
			WHERE sm.title_id = title_watch.title_id AND e.season = title_watch.season AND e.episode = title_watch.episode)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// unmarkFiles deletes the marks of the titles (and, for episodes, the numbers) that the
// files are of.
func unmarkFiles(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for chunk := range slices.Chunk(ids, writeChunk) {
		in, args := placeholders(len(chunk)), idArgs(chunk)
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM title_watch WHERE (title_id, season, episode) IN (
				SELECT i.title_id, 0, 0 FROM items i WHERE i.id IN (`+in+`) AND i.kind = 'movie' AND i.title_id IS NOT NULL
				UNION
				SELECT sm.title_id, i.season, i.episode FROM items i
				JOIN series_matches sm ON sm.show = i.show AND sm.title_id IS NOT NULL
				WHERE i.id IN (`+in+`) AND i.kind = 'episode')`, append(slices.Clone(args), args...)...); err != nil {
			return err
		}
	}
	return nil
}
