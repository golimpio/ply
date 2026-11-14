package store

import (
	"context"
	"database/sql"
	"errors"
)

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
		rows, err := q.QueryContext(ctx, `
			SELECT f.kind, f.tmdb_id, f.tvdb_id, f.imdb_id, f.title, f.original_title,
			       f.year, f.release_date, f.character, f.job, f.department, f.credit_kind,
			       f.poster, f.backdrop, f.rating, f.vote_count, f.popularity, f.episode_count,
			       CASE WHEN (
			           EXISTS (
			               SELECT 1 FROM titles t
			               JOIN items i ON i.title_id = t.id
			               WHERE t.kind = f.kind AND (
			                   (f.tmdb_id > 0 AND t.tmdb_id = f.tmdb_id) OR
			                   (f.imdb_id <> '' AND t.imdb_id = f.imdb_id) OR
			                   (f.tvdb_id > 0 AND t.tvdb_id = f.tvdb_id)
			               )
			           ) OR EXISTS (
			               SELECT 1 FROM titles t
			               JOIN series_matches sm ON sm.title_id = t.id
			               JOIN items e ON e.kind = 'episode' AND e.show = sm.show
			               WHERE t.kind = f.kind AND (
			                   (f.tmdb_id > 0 AND t.tmdb_id = f.tmdb_id) OR
			                   (f.imdb_id <> '' AND t.imdb_id = f.imdb_id) OR
			                   (f.tvdb_id > 0 AND t.tvdb_id = f.tvdb_id)
			               )
			           )
			       ) THEN 1 ELSE 0 END AS in_library
			FROM person_filmography f
			WHERE f.person_id = ?
			ORDER BY f.ord`, personID)
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
