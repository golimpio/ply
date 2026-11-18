package store

import (
	"context"
	"database/sql"
)

// ArtSeason is the owner kind of a season's local artwork, as in
// "local:season:<titleID>:<season>".
const ArtSeason = "season"

// LiveImageRefs returns every provider image ref the library still uses (titles,
// seasons, episodes, people, collections and the cached external filmography), so
// the image cache can drop the files of refs that are gone. Sidecar artwork
// (items.local_*, series_matches.local_*, seasons.local_poster) is an absolute
// path served in place, not a cached file, so it is not needed here.
func (s *Store) LiveImageRefs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT poster FROM titles WHERE poster <> ''
		UNION SELECT backdrop FROM titles WHERE backdrop <> ''
		UNION SELECT logo FROM titles WHERE logo <> ''
		UNION SELECT poster FROM seasons WHERE poster <> ''
		UNION SELECT still FROM episodes WHERE still <> ''
		UNION SELECT profile FROM people WHERE profile <> ''
		UNION SELECT poster FROM collections WHERE poster <> ''
		UNION SELECT backdrop FROM collections WHERE backdrop <> ''
		UNION SELECT poster FROM person_filmography WHERE poster <> ''
		UNION SELECT backdrop FROM person_filmography WHERE backdrop <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]bool{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs[ref] = true
	}
	return refs, rows.Err()
}

// SetSeasonArt records the sidecar poster found next to a show's season ("" when
// there is none). A season that is not stored yet is left alone: the art is read
// again once the title's seasons are written.
func (s *Store) SetSeasonArt(ctx context.Context, titleID int64, season int, poster string) error {
	return s.write(ctx, prioLow, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE seasons SET local_poster = ? WHERE title_id = ? AND season = ?`,
			poster, titleID, season)
		return err
	})
}