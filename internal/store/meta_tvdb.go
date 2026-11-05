package store

import (
	"context"
	"database/sql"
	"errors"
)

// SetEpisodeOrder sets how a show's files map to its episodes (OrderAired,
// OrderDVD, OrderAbsolute). The episodes themselves are replaced by the next
// UpsertTitle with ResetSeasons. It returns ErrNotFound for an unknown title or one
// that is not a show.
func (s *Store) SetEpisodeOrder(ctx context.Context, titleID int64, order string) error {
	if !ValidEpisodeOrder(order) {
		return errors.New("store: episode order " + order)
	}
	res, err := s.exec(ctx, prioHigh, `UPDATE titles SET episode_order = ? WHERE id = ? AND kind = 'series'`, order, titleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTitleByTVDB returns the show with the TVDB id, or ErrNotFound: what a fetch of
// it must know to keep the episode order the person chose.
func (s *Store) GetTitleByTVDB(ctx context.Context, tvdbID int64) (Title, error) {
	t, err := scanTitle(s.ro.QueryRowContext(ctx, `SELECT `+titleCols+` FROM titles WHERE kind = 'series' AND tvdb_id = ?`, tvdbID))
	if errors.Is(err, sql.ErrNoRows) {
		return Title{}, ErrNotFound
	}
	return t, err
}

// KindSeriesTVDB is, for the archive of a removed folder (PendingMatch, ForgetArchivedMatch,
// RestoreArchived), the kind of a show's manual match made through TVDB: the id that goes
// with it is a TVDB id. It is not a kind of title.
const KindSeriesTVDB = "series-tvdb"

// UnmappedEpisodeFiles counts the episode files of the shows linked to the title that have
// no stored episode in the title's order (by season and number): what a change of order
// leaves without a name, an overview or a still.
func (s *Store) UnmappedEpisodeFiles(ctx context.Context, titleID int64) (int, error) {
	var n int
	err := s.ro.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM series_matches sm JOIN items i ON i.kind = 'episode' AND i.show = sm.show
		WHERE sm.title_id = ?1 AND NOT EXISTS (
			SELECT 1 FROM episodes e WHERE e.title_id = ?1 AND e.season = i.season AND e.episode = i.episode)`, titleID).Scan(&n)
	return n, err
}

// MaxTitleID is the highest id a title has, or 0 when there is none: every title
// made after this call has a higher one.
func (s *Store) MaxTitleID(ctx context.Context) (int64, error) {
	var id int64
	err := s.ro.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM titles`).Scan(&id)
	return id, err
}

// TMDBSeriesWithTVDBID returns the shows of the library that came from TMDB (also
// those with NFO text on top), that TMDB gave a TVDB id, and whose id is at most upTo:
// the shows that can move to TVDB by id, with no search. Shows are listed once, lowest
// id first.
func (s *Store) TMDBSeriesWithTVDBID(ctx context.Context, upTo int64) ([]Title, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT `+prefixed("t.", titleCols)+` FROM titles t
		WHERE t.kind = 'series' AND t.source IN ('tmdb', 'mixed') AND t.tvdb_id IS NOT NULL AND t.id <= ? AND `+libraryTitle+`
		ORDER BY t.id`, upTo)
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

// SetPersonDetailsMerged is SetPersonDetails for a person fetched from TVDB, whose
// record may say the person's TMDB id (p.TMDBID).
//
//   - If another person already has that TMDB id, the two are one: the credits of
//     person id move to that one, which takes the TVDB id too, and person id is
//     removed. That person's own details stay if they were fetched; otherwise they
//     are p's. The id returned is the one that is left.
//   - Otherwise the person takes the TMDB id, so that a later credit from TMDB finds it.
//
// With no TMDB id in p it is SetPersonDetails. A person that is not there any more
// because another request merged it a moment ago is that other person (found by the TMDB
// id), whose details are left as that request wrote them. ErrNotFound for an unknown person.
func (s *Store) SetPersonDetailsMerged(ctx context.Context, id int64, p Person, fetchedAt int64) (int64, error) {
	survivor := id
	err := s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		var tmdb, tvdb sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT tmdb_id, tvdb_id FROM people WHERE id = ?`, id).Scan(&tmdb, &tvdb)
		if errors.Is(err, sql.ErrNoRows) {
			if p.TMDBID != 0 {
				if other, _, oerr := personByTMDB(ctx, tx, p.TMDBID); oerr != nil || other != 0 {
					survivor = other
					return oerr
				}
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		keepDetails := false
		if p.TMDBID != 0 && !tmdb.Valid {
			other, otherTVDB, err := personByTMDB(ctx, tx, p.TMDBID)
			if err != nil {
				return err
			}
			switch {
			case other != 0 && otherTVDB.Valid && tvdb.Valid && otherTVDB.Int64 != tvdb.Int64:
				// TVDB lists two of its people under one TMDB person: they are not merged (the one
				// row cannot hold two TVDB ids), and this one keeps its own id and takes no TMDB id.
			case other == 0:
				if _, err := tx.ExecContext(ctx, `UPDATE people SET tmdb_id = ? WHERE id = ?`, p.TMDBID, id); err != nil {
					return err
				}
			default:
				if survivor, keepDetails, err = mergePeople(ctx, tx, id, other, tvdb); err != nil {
					return err
				}
			}
		}
		if keepDetails {
			// The TMDB person was fetched already: TVDB adds a photo only if there is none.
			_, err = tx.ExecContext(ctx, `UPDATE people SET profile = CASE WHEN profile = '' THEN ? ELSE profile END WHERE id = ?`,
				p.Profile, survivor)
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE people SET biography = ?, birthday = ?, deathday = ?, place_of_birth = ?, known_for = ?,
				imdb_id = ?, profile = CASE WHEN ? <> '' THEN ? ELSE profile END, fetched_at = ?
			WHERE id = ?`,
			p.Biography, p.Birthday, p.Deathday, p.PlaceOfBirth, p.KnownFor, p.IMDbID, p.Profile, p.Profile,
			fetchedAt, survivor)
		return err
	})
	if err != nil {
		return 0, err
	}
	return survivor, nil
}

// personByTMDB is the id of the person with a TMDB id, and their TVDB id; 0 for none.
func personByTMDB(ctx context.Context, tx *sql.Tx, tmdbID int64) (int64, sql.NullInt64, error) {
	var (
		id   int64
		tvdb sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT id, tvdb_id FROM people WHERE tmdb_id = ?`, tmdbID).Scan(&id, &tvdb)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sql.NullInt64{}, nil
	}
	return id, tvdb, err
}

// mergePeople folds person from into person into: from's credits and TVDB id go to
// into, and from is removed. It reports whether into had its details fetched.
func mergePeople(ctx context.Context, tx *sql.Tx, from, into int64, tvdb sql.NullInt64) (survivor int64, fetched bool, err error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO credits (title_id, person_id, kind, job, character, ord)
		SELECT title_id, ?, kind, job, character, ord FROM credits WHERE person_id = ?`, into, from); err != nil {
		return 0, false, err
	}
	// from goes first: its TVDB id is unique, and its credits go with it.
	if _, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id = ?`, from); err != nil {
		return 0, false, err
	}
	if tvdb.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE people SET tvdb_id = ? WHERE id = ? AND tvdb_id IS NULL`, tvdb.Int64, into); err != nil {
			return 0, false, err
		}
	}
	var at int64
	if err := tx.QueryRowContext(ctx, `SELECT fetched_at FROM people WHERE id = ?`, into).Scan(&at); err != nil {
		return 0, false, err
	}
	return into, at != 0, nil
}
