package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The two ways a title is in the library: a film file links to it, or an episode
// of a show linked to it does.
const (
	filmJoin   = `JOIN items i ON i.title_id = t.id`
	seriesJoin = `JOIN series_matches sm ON sm.title_id = t.id
		JOIN items e ON e.kind = 'episode' AND e.show = sm.show`
)

// inLibraryExists is one term of the filmography's in_library check: the title
// found by one provider id. One EXISTS per id, not an OR of the three inside one
// EXISTS: SQLite only indexes an OR when every term has an index and its
// statistics favour that plan; a separate EXISTS seeks its index whatever the
// statistics say. tmdb and tvdb have theirs in UNIQUE (kind, id); titles_imdb
// (migration 021) is imdb's.
func inLibraryExists(join, idCol, guard string) string {
	return `EXISTS (SELECT 1 FROM titles t ` + join + `
		WHERE t.kind = f.kind AND t.` + idCol + ` = f.` + idCol + ` AND f.` + idCol + ` ` + guard + `)`
}

// filmographySQL reads a person's stored external credits, best first, each with
// whether the library has that title.
var filmographySQL = `
	SELECT f.kind, f.tmdb_id, f.tvdb_id, f.imdb_id, f.title, f.original_title,
	       f.year, f.release_date, f.character, f.job, f.department, f.credit_kind,
	       f.poster, f.backdrop, f.rating, f.vote_count, f.popularity, f.episode_count,
	       CASE WHEN (` + strings.Join([]string{
	inLibraryExists(filmJoin, "tmdb_id", "> 0"),
	inLibraryExists(filmJoin, "imdb_id", "<> ''"),
	inLibraryExists(filmJoin, "tvdb_id", "> 0"),
	inLibraryExists(seriesJoin, "tmdb_id", "> 0"),
	inLibraryExists(seriesJoin, "imdb_id", "<> ''"),
	inLibraryExists(seriesJoin, "tvdb_id", "> 0"),
}, " OR ") + `) THEN 1 ELSE 0 END AS in_library
	FROM person_filmography f
	WHERE f.person_id = ?
	ORDER BY f.ord`

// GetPersonFilmography returns what is stored of a person's external filmography.
// ErrNotFound for an unknown person.
func (s *Store) GetPersonFilmography(ctx context.Context, personID int64) (PersonFilmography, error) {
	var out PersonFilmography
	err := s.snapshot(ctx, func(q queryer) error {
		err := q.QueryRowContext(ctx, `SELECT filmography_ref, filmography_checked FROM people WHERE id = ?`, personID).
			Scan(&out.Ref, &out.CheckedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, filmographySQL, personID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e         PersonFilmographyEntry
				inLibrary int
			)
			if err := rows.Scan(
				&e.Kind, &e.TMDBID, &e.TVDBID, &e.IMDbID, &e.Title, &e.OriginalTitle,
				&e.Year, &e.ReleaseDate, &e.Character, &e.Job, &e.Department, &e.CreditKind,
				&e.Poster, &e.Backdrop, &e.Rating, &e.VoteCount, &e.Popularity, &e.EpisodeCount,
				&inLibrary,
			); err != nil {
				return err
			}
			e.InLibrary = inLibrary == 1
			out.Entries = append(out.Entries, e)
		}
		return rows.Err()
	})
	return out, err
}

// SetPersonFilmography replaces the external filmography of a person with entries
// fetched for ref at checkedAt. ErrNotFound for a person that is gone.
func (s *Store) SetPersonFilmography(ctx context.Context, personID int64, ref string, entries []PersonFilmographyEntry, checkedAt int64) error {
	return s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE people SET filmography_ref = ?, filmography_checked = ? WHERE id = ?`, ref, checkedAt, personID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM person_filmography WHERE person_id = ?`, personID); err != nil {
			return err
		}
		for i, e := range entries {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO person_filmography (
					person_id, ord, kind, tmdb_id, tvdb_id, imdb_id, title, original_title,
					year, release_date, character, job, department, credit_kind, poster, backdrop,
					rating, vote_count, popularity, episode_count
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				personID, i, e.Kind, e.TMDBID, e.TVDBID, e.IMDbID, e.Title, e.OriginalTitle,
				e.Year, e.ReleaseDate, e.Character, e.Job, e.Department, e.CreditKind, e.Poster, e.Backdrop,
				e.Rating, e.VoteCount, e.Popularity, e.EpisodeCount,
			); err != nil {
				return err
			}
		}
		return nil
	})
}
