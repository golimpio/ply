package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Library folder types, as in the folders table (the same strings as
// library.KindMovies and library.KindShows, which this package cannot import).
const (
	FolderMovies = "movies"
	FolderShows  = "shows"
)

// ErrFolderExists is returned by AddFolder for a path that is already a folder.
var ErrFolderExists = errors.New("store: folder already exists")

// Folder is a library folder and its type.
type Folder struct {
	ID      int64
	Path    string // absolute, cleaned
	Kind    string // FolderMovies | FolderShows
	Guessed bool   // the kind was guessed, not chosen
	AddedAt int64  // unix seconds
}

func validFolderKind(k string) error {
	if k == FolderMovies || k == FolderShows {
		return nil
	}
	return fmt.Errorf("store: folder kind %q", k)
}

const folderCols = `id, path, kind, guessed, added_at`

func scanFolder(sc scanner) (Folder, error) {
	var f Folder
	err := sc.Scan(&f.ID, &f.Path, &f.Kind, &f.Guessed, &f.AddedAt)
	return f, err
}

// Folders returns every folder, oldest first.
func (s *Store) Folders(ctx context.Context) ([]Folder, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT `+folderCols+` FROM folders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFolder returns the folder with this id, or ErrNotFound.
func (s *Store) GetFolder(ctx context.Context, id int64) (Folder, error) {
	f, err := scanFolder(s.ro.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	return f, err
}

// AddFolder stores a folder, or returns ErrFolderExists when the path is
// already one. guessed says the kind was guessed rather than chosen.
func (s *Store) AddFolder(ctx context.Context, path, kind string, guessed bool) (Folder, error) {
	if err := validFolderKind(kind); err != nil {
		return Folder{}, err
	}
	var f Folder
	err := s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		var err error
		f, err = scanFolder(tx.QueryRowContext(ctx, `
			INSERT INTO folders (path, kind, guessed, added_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (path) DO NOTHING
			RETURNING `+folderCols, path, kind, guessed, time.Now().Unix()))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrFolderExists
	}
	return f, err
}

// SetFolderKind makes kind the folder's type, as a person's choice (it is no
// longer a guess), or returns ErrNotFound. It does not touch the items: the
// scanner re-parses them (SetParsed).
func (s *Store) SetFolderKind(ctx context.Context, id int64, kind string) error {
	if err := validFolderKind(kind); err != nil {
		return err
	}
	res, err := s.exec(ctx, prioHigh, `UPDATE folders SET kind = ?, guessed = 0 WHERE id = ?`, kind, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFolder removes the folder's row, or returns ErrNotFound. Its items stay:
// see ArchiveAndDeleteRoot.
func (s *Store) DeleteFolder(ctx context.Context, id int64) error {
	res, err := s.exec(ctx, prioHigh, `DELETE FROM folders WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertFlagFolder records a command-line folder, which is done at every start.
//
// -movies and -shows pass their type as kind: it replaces the stored one and
// the folder is no guess. -media passes kind "" and a guess (which may be "" too):
// a stored folder is kept as it is; otherwise it is inserted with the guess and
// guessed set, or not stored at all when there is no guess yet (the scanner makes
// one after its first look at the files, and calls this again).
//
// changed says that a stored kind was replaced by a different one, so the items
// parsed under the old kind need a re-parse. The returned folder has ID 0 when
// nothing is stored for the path.
func (s *Store) UpsertFlagFolder(ctx context.Context, path, kind, guess string) (f Folder, changed bool, err error) {
	if kind != "" {
		err = validFolderKind(kind)
	} else if guess != "" {
		err = validFolderKind(guess)
	}
	if err != nil {
		return Folder{}, false, err
	}
	err = s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		cur, err := scanFolder(tx.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE path = ?`, path))
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		switch {
		case exists && (kind == "" || (cur.Kind == kind && !cur.Guessed)):
			f = cur
			return nil
		case exists:
			changed = cur.Kind != kind
			cur.Kind, cur.Guessed = kind, false
			if _, err := tx.ExecContext(ctx, `UPDATE folders SET kind = ?, guessed = 0 WHERE id = ?`, kind, cur.ID); err != nil {
				return err
			}
			f = cur
			return nil
		}
		chosen, guessed := kind, false
		if chosen == "" {
			chosen, guessed = guess, true
		}
		if chosen == "" {
			return nil
		}
		f, err = scanFolder(tx.QueryRowContext(ctx, `
			INSERT INTO folders (path, kind, guessed, added_at) VALUES (?, ?, ?, ?)
			RETURNING `+folderCols, path, chosen, guessed, time.Now().Unix()))
		return err
	})
	if err != nil {
		return Folder{}, false, err
	}
	return f, changed, nil
}

// CountItemsByRoot returns the number of items under each root that has any.
func (s *Store) CountItemsByRoot(ctx context.Context) (map[string]int, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT root, COUNT(*) FROM items GROUP BY root`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var (
			root string
			n    int
		)
		if err := rows.Scan(&root, &n); err != nil {
			return nil, err
		}
		m[root] = n
	}
	return m, rows.Err()
}

// RootItem is an item of a library folder, as far as its name goes: what the
// scanner compares with a new reading of the name (see SetParsed).
type RootItem struct {
	Path    string
	Root    string // the root the item is indexed under
	AddedAt int64  // unix seconds; compare with TypedSince
	ParsedRow
}

// sinceKey is the setting that says when this version first ran on the database.
const sinceKey = "m7.since"

// stampSince records, once, the time this version first opened the database. A
// database an earlier version left has items older than that, and no later start
// changes it. Open calls it after the migrations.
func (s *Store) stampSince(ctx context.Context) error {
	_, err := s.rw.ExecContext(ctx, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`,
		sinceKey, strconv.FormatInt(time.Now().Unix(), 10))
	return err
}

// TypedSince is the time (unix seconds) this version first opened the database. An
// item added before it was read by an earlier version, one that knew no folder types:
// the library as it was, which the scanner does not restructure on a guess. An item
// added from then on was read by this version, whatever the id it got or how many
// times ply was restarted since.
func (s *Store) TypedSince(ctx context.Context) (int64, error) {
	v, ok, err := s.GetSetting(ctx, sinceKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("store: no " + sinceKey + " stamp")
	}
	return strconv.ParseInt(v, 10, 64)
}

// RootItems returns every item whose root is root, lowest id first. NeedsReview is
// the item's own flag.
func (s *Store) RootItems(ctx context.Context, root string) ([]RootItem, error) {
	return s.rootItems(ctx, `WHERE root = ?`, root)
}

// ItemsBelow returns every item whose file is inside dir, whatever root it is indexed
// under, lowest id first: the folder's own items, and the rows an earlier configuration
// left under another root, which the scan has yet to move to it.
func (s *Store) ItemsBelow(ctx context.Context, dir string) ([]RootItem, error) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	return s.rootItems(ctx, `WHERE substr(path, 1, ?) = ?`, utf8.RuneCountInString(prefix), prefix) // substr counts characters
}

func (s *Store) rootItems(ctx context.Context, where string, args ...any) ([]RootItem, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT id, path, root, added_at, kind, title, year, show, season, episode, needs_review
		FROM items `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RootItem
	for rows.Next() {
		var it RootItem
		if err := rows.Scan(&it.ID, &it.Path, &it.Root, &it.AddedAt, &it.Kind, &it.Title, &it.Year, &it.Show, &it.Season,
			&it.Episode, &it.NeedsReview); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ArchiveAndDeleteRoot removes the library folder root from the library: every
// item whose root is root is deleted (streams, keyframes, progress and the other
// rows hanging off it go with it), and returns how many. Unless forget is set,
// what a person made of an item is first kept in item_archive, by path, so that
// adding the folder back restores it (RestoreArchived): the progress, and the
// TMDB title of a manual match (a movie's own, or its show's; tmdb_kind is the
// title's kind, KindMovie or KindSeries). Items with neither are not archived.
//
// It works through the items writeChunk at a time, by id, and each batch is one
// transaction that archives its items and deletes them: an item is never deleted
// without its archive row, and the writer is free between batches. After an error
// (or a crash) the batches before it stay archived and deleted, the failing one
// is rolled back whole, and calling again continues with what is left; the count
// returned with an error is what was deleted before it. The folder's own row is
// not touched here.
func (s *Store) ArchiveAndDeleteRoot(ctx context.Context, root string, forget bool) (int, error) {
	now := time.Now().Unix()
	total := 0
	for {
		var n int
		err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			ids, err := rootBatch(ctx, tx, root)
			if err != nil {
				return err
			}
			if n = len(ids); n == 0 {
				return nil
			}
			in, args := placeholders(n), idArgs(ids)
			if forget {
				// Nothing of these files is kept, a match still waiting in the archive
				// included.
				if _, err := tx.ExecContext(ctx, `
						DELETE FROM item_archive WHERE path IN (SELECT path FROM items WHERE id IN (`+in+`))`, args...); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `
						INSERT INTO item_archive (path, size, tmdb_id, tmdb_kind, series_id, series_tvdb_id, series_show, position_sec,
							duration_sec, watched, progress_at, archived_at)
						SELECT i.path, i.size, COALESCE(tm.tmdb_id, 0), CASE WHEN tm.tmdb_id IS NULL THEN '' ELSE tm.kind END,
							-- a show matched through TVDB is kept by its TVDB id, one matched through TMDB by its TMDB id
							CASE WHEN ts.source = 'tvdb' THEN 0 ELSE COALESCE(ts.tmdb_id, 0) END,
							CASE WHEN ts.source = 'tvdb' THEN COALESCE(ts.tvdb_id, 0) ELSE 0 END,
							CASE WHEN COALESCE(CASE WHEN ts.source = 'tvdb' THEN ts.tvdb_id ELSE ts.tmdb_id END, 0) = 0 THEN '' ELSE i.show END,
							COALESCE(p.position_sec, 0), COALESCE(p.duration_sec, 0), COALESCE(p.watched, 0),
							COALESCE(p.updated_at, 0), ?
						FROM items i
						LEFT JOIN progress p ON p.item_id = i.id
						LEFT JOIN titles tm ON tm.id = CASE WHEN i.kind <> 'episode' AND i.match_state = 'manual' THEN i.title_id END
						LEFT JOIN series_matches sm ON i.kind = 'episode' AND sm.show = i.show AND sm.match_state = 'manual'
						LEFT JOIN titles ts ON ts.id = sm.title_id
						WHERE i.id IN (`+in+`) AND (p.item_id IS NOT NULL OR tm.tmdb_id IS NOT NULL OR ts.tmdb_id IS NOT NULL
							OR (ts.source = 'tvdb' AND ts.tvdb_id IS NOT NULL))
						ON CONFLICT (path) DO UPDATE SET size = excluded.size,
							-- a match still waiting to be applied (see RestoreArchived) is not replaced by none
							tmdb_id = CASE WHEN excluded.tmdb_id <> 0 THEN excluded.tmdb_id ELSE item_archive.tmdb_id END,
							tmdb_kind = CASE WHEN excluded.tmdb_id <> 0 THEN excluded.tmdb_kind ELSE item_archive.tmdb_kind END,
							series_id = CASE WHEN excluded.series_id <> 0 OR excluded.series_tvdb_id <> 0 THEN excluded.series_id ELSE item_archive.series_id END,
							series_tvdb_id = CASE WHEN excluded.series_id <> 0 OR excluded.series_tvdb_id <> 0 THEN excluded.series_tvdb_id ELSE item_archive.series_tvdb_id END,
							series_show = CASE WHEN excluded.series_id <> 0 OR excluded.series_tvdb_id <> 0 THEN excluded.series_show ELSE item_archive.series_show END,
							position_sec = excluded.position_sec,
							duration_sec = excluded.duration_sec, watched = excluded.watched,
							progress_at = excluded.progress_at, archived_at = excluded.archived_at`,
					append([]any{now}, args...)...); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM items WHERE id IN (`+in+`)`, args...)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < writeChunk {
			return total, nil
		}
	}
}

// rootBatch returns the ids of up to writeChunk items under root, lowest first.
func rootBatch(ctx context.Context, tx *sql.Tx, root string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM items WHERE root = ? ORDER BY id LIMIT ?`, root, writeChunk)
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

// RestoreArchived gives a file that came back what ArchiveAndDeleteRoot kept of
// it. The progress is restored only when size is the archived size (a different
// file is not where the person stopped), and is used up: the archive row loses it.
// The manual match is returned either way, for the caller to apply by TMDB id (the
// kind is KindMovie or KindSeries; a movie's own if the row has both), and stays in
// the archive until the caller has applied it and says so (ForgetArchivedMatch): a
// match that could not be applied yet (no TMDB key, no network) must not be lost,
// and PendingMatches finds it again. ok is false when the path was not archived.
// Calling it again is harmless.
func (s *Store) RestoreArchived(ctx context.Context, itemID int64, path string, size int64) (tmdbID int64, tmdbKind string, ok bool, err error) {
	err = s.write(ctx, prioLow, func(tx *sql.Tx) error {
		var (
			archivedSize, progressAt, movieID, seriesID, seriesTVDB int64
			movieKind                                               string
			position, duration                                      float64
			watched                                                 bool
		)
		err := tx.QueryRowContext(ctx, `
			SELECT size, tmdb_id, tmdb_kind, series_id, series_tvdb_id, position_sec, duration_sec, watched, progress_at
			FROM item_archive WHERE path = ?`, path).
			Scan(&archivedSize, &movieID, &movieKind, &seriesID, &seriesTVDB, &position, &duration, &watched, &progressAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case movieID != 0:
			tmdbID, tmdbKind = movieID, movieKind
		case seriesTVDB != 0:
			tmdbID, tmdbKind = seriesTVDB, KindSeriesTVDB
		case seriesID != 0:
			tmdbID, tmdbKind = seriesID, KindSeries
		}
		if archivedSize == size && progressAt != 0 {
			// The item may already have progress (it was played since it came back):
			// that is newer.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at)
				SELECT id, ?, ?, ?, ? FROM items WHERE id = ?
				ON CONFLICT (item_id) DO NOTHING`, position, duration, watched, progressAt, itemID); err != nil {
				return err
			}
		}
		query := `DELETE FROM item_archive WHERE path = ?`
		if tmdbID != 0 {
			query = `UPDATE item_archive SET position_sec = 0, duration_sec = 0, watched = 0, progress_at = 0 WHERE path = ?`
		}
		if _, err := tx.ExecContext(ctx, query, path); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return 0, "", false, err
	}
	return tmdbID, tmdbKind, true, nil
}

// ForgetArchivedMatch drops the manual match of a kind (KindMovie, or KindSeries
// for an episode's show) that an archive row keeps for path, once it has been
// applied; the row goes with it unless it still holds progress or the other match.
func (s *Store) ForgetArchivedMatch(ctx context.Context, path, kind string) error {
	var set string
	switch kind {
	case KindMovie:
		set = `tmdb_id = 0, tmdb_kind = ''`
	case KindSeries, KindSeriesTVDB:
		// The two are alternatives: a show's match is made at one of the providers.
		set = `series_id = 0, series_tvdb_id = 0, series_show = ''`
	default:
		return fmt.Errorf("store: title kind %q", kind)
	}
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE item_archive SET `+set+` WHERE path = ?`, path); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM item_archive WHERE path = ? AND progress_at = 0 AND tmdb_id = 0 AND series_id = 0 AND series_tvdb_id = 0`, path)
		return err
	})
}

// PendingMatch is a manual match kept in the archive for an item that is in the
// library, to be applied by id: a movie's own, or its show's (Kind is KindMovie or
// KindSeries, as the item's kind says, or KindSeriesTVDB for a show matched through
// TVDB). TMDBID is the id at the provider Kind names: TMDB's, or TVDB's for KindSeriesTVDB.
type PendingMatch struct {
	ItemID int64
	Path   string
	TMDBID int64
	Kind   string
}

// PendingMatches lists the archived manual matches whose item is in the library, is
// of a kind the match fits (a movie for a movie's title, an episode of the same
// show for a show's) and has no manual match of its own yet. Whoever applies them
// calls ForgetArchivedMatch for each; one that is not forgotten is listed again.
func (s *Store) PendingMatches(ctx context.Context) ([]PendingMatch, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT i.id, a.path, a.tmdb_id, 'movie'
		FROM item_archive a JOIN items i ON i.path = a.path
		WHERE a.tmdb_id <> 0 AND a.tmdb_kind = 'movie' AND i.kind = 'movie' AND i.match_state <> 'manual'
		UNION ALL
		SELECT i.id, a.path, a.series_id, 'series'
		FROM item_archive a JOIN items i ON i.path = a.path
		WHERE a.series_id <> 0 AND i.kind = 'episode' AND (a.series_show = '' OR a.series_show = i.show)
			AND NOT EXISTS (SELECT 1 FROM series_matches sm WHERE sm.show = i.show AND sm.match_state = 'manual')
		UNION ALL
		SELECT i.id, a.path, a.series_tvdb_id, 'series-tvdb'
		FROM item_archive a JOIN items i ON i.path = a.path
		WHERE a.series_tvdb_id <> 0 AND i.kind = 'episode' AND (a.series_show = '' OR a.series_show = i.show)
			AND NOT EXISTS (SELECT 1 FROM series_matches sm WHERE sm.show = i.show AND sm.match_state = 'manual')
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingMatch
	for rows.Next() {
		var m PendingMatch
		if err := rows.Scan(&m.ItemID, &m.Path, &m.TMDBID, &m.Kind); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ArchivedProgress returns the paths whose archive row still holds progress to
// give back (RestoreArchived).
func (s *Store) ArchivedProgress(ctx context.Context) (map[string]bool, error) {
	rows, err := s.ro.QueryContext(ctx, `SELECT path FROM item_archive WHERE progress_at <> 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ParsedRow is what a file's name and folder say about it: the fields of an item
// that come from its path.
type ParsedRow struct {
	ID          int64
	Kind        string // KindMovie | KindEpisode
	Title       string
	Year        int
	Show        string
	Season      int
	Episode     int
	NeedsReview bool // an episode with no number in its name
}

// SetParsed stores a new reading of items' names, for when their folder's type
// changed. Per item it replaces kind, title, year, show, season and episode and
// applies the rules for matches: a match that is not manual is cleared (the item
// goes back to pending, due at once, with NeedsReview as given), and a manual one
// is dropped when its TMDB title no longer fits the item's kind (a movie's title
// on an episode, or the other way round). A manual match that fits is kept as it
// is.
//
// An episode's match is its show's: for every show the items had before and
// have now, the series row is cleared when it is not manual, and removed when no
// episode of the show is left. Unknown ids are skipped.
//
// The rows are written writeChunk at a time, one transaction each (so that a
// library of ten thousand files does not hold the writer for the whole re-parse),
// and all of them are checked before the first is written. The matches of a
// batch's shows are cleared in its own transaction, so that a committed batch
// never leaves its shows with a stale match; a series row is removed only in the
// closing pass, which also clears the matches of all the shows once more, because
// a show with no episode in the middle may have some at the end (and a manual
// match must survive that). The closing pass is what makes the end state that of
// one big transaction whatever the matcher did in between. Every step can be
// repeated: after an error, calling again with the same rows finishes the job.
// (The row of a show that lost its last episode in a batch stays until then, or
// until PruneMetadata, if the call does not get to its end.)
func (s *Store) SetParsed(ctx context.Context, rows []ParsedRow) error {
	for _, r := range rows {
		if r.Kind != KindMovie && r.Kind != KindEpisode {
			return fmt.Errorf("store: item kind %q", r.Kind)
		}
	}
	if err := s.renameMatchedShows(ctx, rows); err != nil {
		return err
	}
	all := map[string]bool{}
	for chunk := range slices.Chunk(rows, writeChunk) {
		shows := map[string]bool{}
		err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			now := time.Now().Unix()
			for _, r := range chunk {
				if err := setParsedRow(ctx, tx, r, now, shows); err != nil {
					return err
				}
			}
			return cleanSeries(ctx, tx, slices.Collect(maps.Keys(shows)), false)
		})
		if err != nil {
			return err
		}
		maps.Copy(all, shows)
	}
	for chunk := range slices.Chunk(slices.Sorted(maps.Keys(all)), writeChunk) {
		if err := s.write(ctx, prioLow, func(tx *sql.Tx) error { return cleanSeries(ctx, tx, chunk, true) }); err != nil {
			return err
		}
	}
	return nil
}

// renameMatchedShows moves the manual match of a show to its new name when SetParsed
// reads ALL of the show's episodes as the episodes of ONE other show, and that show
// has no episodes yet and gets no others: the show is being renamed, which is what a
// folder's first type or a type change does to "Lost" in "Lost (2004)/Season 1/
// Lost.S01E01.mkv" (the file name says "Lost", the folder says "Lost (2004)"). The
// series row keeps its title and its manual state, and its art; a row the new name
// had that is not manual (the matcher's guess) is replaced by it. Anything else
// (a type change that makes the show's episodes movies, several shows read as one,
// a new name with a manual match of its own) leaves the match to setParsedRow, which
// archives it for the day the old show is back. It is a transaction of its own: if a
// later batch of SetParsed fails, a second call finds the match at the new name and
// the episodes that are left at the old one without a row to archive.
func (s *Store) renameMatchedShows(ctx context.Context, rows []ParsedRow) error {
	// Most libraries have no show with a manual match: the writer is not asked for.
	var one int
	err := s.ro.QueryRowContext(ctx, `SELECT 1 FROM series_matches WHERE match_state = 'manual' LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		manual, err := manualShows(ctx, tx)
		if err != nil || len(manual) == 0 {
			return err
		}
		byID := make(map[int64]ParsedRow, len(rows))
		arriving := map[string]int{} // how many items are read as an episode of the show
		for _, r := range rows {
			byID[r.ID] = r
			if r.Kind == KindEpisode {
				arriving[r.Show]++
			}
		}
		for _, old := range manual {
			ids, err := episodeIDs(ctx, tx, old)
			if err != nil {
				return err
			}
			newName, ok := "", len(ids) > 0
			for _, id := range ids {
				r, in := byID[id]
				if !in || r.Kind != KindEpisode || r.Show == "" || (newName != "" && r.Show != newName) {
					ok = false
					break
				}
				newName = r.Show
			}
			if !ok || newName == old || arriving[newName] != len(ids) {
				continue
			}
			if there, err := episodeIDs(ctx, tx, newName); err != nil {
				return err
			} else if len(there) > 0 {
				continue // the new show has episodes of its own
			}
			var state string
			err = tx.QueryRowContext(ctx, `SELECT match_state FROM series_matches WHERE show = ?`, newName).Scan(&state)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return err
			case state == MatchManual:
				continue // a decision of its own stands
			default:
				if _, err := tx.ExecContext(ctx, `DELETE FROM series_matches WHERE show = ?`, newName); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE series_matches SET show = ? WHERE show = ?`, newName, old); err != nil {
				return err
			}
		}
		return nil
	})
}

// manualShows are the shows with a manual match, sorted.
func manualShows(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT show FROM series_matches WHERE match_state = 'manual' ORDER BY show`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var show string
		if err := rows.Scan(&show); err != nil {
			return nil, err
		}
		out = append(out, show)
	}
	return out, rows.Err()
}

// episodeIDs are the ids of the episodes of a show.
func episodeIDs(ctx context.Context, tx *sql.Tx, show string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM items WHERE kind = 'episode' AND show = ?`, show)
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

// setParsedRow updates one item, and adds the shows it has and had, as episodes,
// to shows. A manual match it drops (a movie's title on an item that becomes an
// episode, or the manual match of the show an episode leaves) is kept in the
// archive by path, so that giving the folder its old type back restores it
// (PendingMatches).
func setParsedRow(ctx context.Context, tx *sql.Tx, r ParsedRow, now int64, shows map[string]bool) error {
	var (
		oldKind, oldShow, state, path string
		size                          int64
		titleKind, seriesState        sql.NullString
		titleTMDB, seriesTMDB         sql.NullInt64
		seriesSource                  sql.NullString
		seriesTVDB                    sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT i.kind, i.show, i.match_state, i.path, i.size, t.kind, t.tmdb_id, sm.match_state, st.tmdb_id, st.source, st.tvdb_id
		FROM items i LEFT JOIN titles t ON t.id = i.title_id
		LEFT JOIN series_matches sm ON i.kind = 'episode' AND sm.show = i.show
		LEFT JOIN titles st ON st.id = sm.title_id
		WHERE i.id = ?`, r.ID).
		Scan(&oldKind, &oldShow, &state, &path, &size, &titleKind, &titleTMDB, &seriesState, &seriesTMDB, &seriesSource, &seriesTVDB)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if oldKind == KindEpisode {
		shows[oldShow] = true
	}
	if r.Kind == KindEpisode {
		shows[r.Show] = true
	}

	// An item's own manual match is a movie's. A manual "no match" (no title)
	// keeps standing while the item is the kind it was decided for.
	keep := state == MatchManual
	if keep && titleKind.Valid {
		keep = r.Kind == KindMovie && titleKind.String == KindMovie
	} else if keep {
		keep = r.Kind == oldKind
	}
	if !keep && state == MatchManual && titleKind.Valid && titleTMDB.Int64 != 0 {
		if err := archiveMatch(ctx, tx, path, size, titleTMDB.Int64, KindMovie, "", now); err != nil {
			return err
		}
	}
	// The show's match stays with the show: an episode that leaves it (it is a movie
	// now, or another show's episode) takes it to the archive, for the day it is back.
	if oldKind == KindEpisode && (r.Kind != KindEpisode || r.Show != oldShow) && seriesState.String == MatchManual {
		// A match made through TVDB is kept by its TVDB id.
		switch {
		case seriesSource.String == SourceTVDB && seriesTVDB.Int64 != 0:
			err = archiveMatch(ctx, tx, path, size, seriesTVDB.Int64, KindSeriesTVDB, oldShow, now)
		case seriesSource.String != SourceTVDB && seriesTMDB.Int64 != 0:
			err = archiveMatch(ctx, tx, path, size, seriesTMDB.Int64, KindSeries, oldShow, now)
		}
		if err != nil {
			return err
		}
	}
	reset := ``
	if !keep {
		reset = `, title_id = NULL, match_state = 'pending', needs_review = ?, match_score = 0,
			match_attempts = 0, next_attempt_at = 0`
	}
	args := []any{r.Kind, r.Title, r.Year, r.Show, r.Season, r.Episode, now}
	if !keep {
		args = append(args, r.NeedsReview)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE items SET kind = ?, title = ?, year = ?, show = ?, season = ?, episode = ?,
			updated_at = ?`+reset+` WHERE id = ?`, append(args, r.ID)...)
	return err
}

// archiveMatch keeps a manual match by the path of the item that had it, in the
// archive of a removed folder: whatever progress the row already holds stays. A
// movie's match and a show's have a slot each (tmdb_id, and series_id with the show
// it was for), so that an item that is a movie, then an episode, then a movie again
// does not lose the one while the other is archived. Each slot holds the latest.
func archiveMatch(ctx context.Context, tx *sql.Tx, path string, size, tmdbID int64, kind, show string, now int64) error {
	switch kind {
	case KindSeries:
		_, err := tx.ExecContext(ctx, `
			INSERT INTO item_archive (path, size, series_id, series_show, archived_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET size = excluded.size, series_id = excluded.series_id, series_tvdb_id = 0,
				series_show = excluded.series_show, archived_at = excluded.archived_at`, path, size, tmdbID, show, now)
		return err
	case KindSeriesTVDB: // tmdbID is the TVDB id; the show's slot holds one of the two
		_, err := tx.ExecContext(ctx, `
			INSERT INTO item_archive (path, size, series_tvdb_id, series_show, archived_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (path) DO UPDATE SET size = excluded.size, series_tvdb_id = excluded.series_tvdb_id, series_id = 0,
				series_show = excluded.series_show, archived_at = excluded.archived_at`, path, size, tmdbID, show, now)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO item_archive (path, size, tmdb_id, tmdb_kind, archived_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET size = excluded.size, tmdb_id = excluded.tmdb_id,
			tmdb_kind = excluded.tmdb_kind, archived_at = excluded.archived_at`, path, size, tmdbID, KindMovie, now)
	return err
}

// cleanSeries clears the match of each show unless it is manual and, when remove
// is set, removes the series row of each that has no episode left (manual or not).
func cleanSeries(ctx context.Context, tx *sql.Tx, shows []string, remove bool) error {
	for _, show := range shows {
		if remove {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM series_matches WHERE show = ?
					AND NOT EXISTS (SELECT 1 FROM items WHERE kind = 'episode' AND show = series_matches.show)`, show); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE series_matches SET title_id = NULL, match_state = 'pending', needs_review = 0, match_score = 0,
				match_attempts = 0, next_attempt_at = 0
			WHERE show = ? AND match_state <> 'manual'`, show); err != nil {
			return err
		}
	}
	return nil
}
