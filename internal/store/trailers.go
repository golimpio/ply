package store

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
)

// filmsBelowSQL is the probe for the film files inside a trailer's owner folder:
// a byte range on path (belowCond), which seeks the UNIQUE(path) index where the
// substr form scanned every movie, once per trailer candidate.
var filmsBelowSQL = `SELECT ` + itemCols + ` FROM items WHERE kind = ? AND ` + belowCond("path", "?")

// TitleTrailers returns the trailers of a title (docs/M13-TRAILERS.md, "Whose trailer it
// is"), ordered by path. A trailer has no title of its own: it belongs to the film whose
// files are in its owner folder, or to the show of its Show.
//
// A film's: a trailer whose owner folder holds a file of the film below it. When every film
// file below that folder is one film (several versions of it), the trailer is that film's;
// when they are several films (loose films in a shared folder), it is the film's only if its
// name says so: a file of the film directly in the folder has the trailer's title and year.
func (s *Store) TitleTrailers(ctx context.Context, titleID int64) ([]Item, error) {
	shows, err := queryItems(ctx, s.ro, `SELECT `+itemCols+` FROM items
		WHERE kind = ? AND show <> '' AND show IN (SELECT show FROM series_matches WHERE title_id = ?)
		ORDER BY path`, []any{KindTrailer, titleID})
	if err != nil || len(shows) > 0 {
		return shows, err
	}
	candidates, err := queryItems(ctx, s.ro, `SELECT `+prefixed("t.", itemCols)+` FROM items t
		WHERE t.kind = ? AND t.show = '' AND t.owner_dir <> '' AND EXISTS (
			SELECT 1 FROM items m WHERE m.kind = ? AND m.title_id = ?
				AND `+belowCond("m.path", "t.owner_dir")+`)
		ORDER BY t.path`, []any{KindTrailer, KindMovie, titleID})
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, t := range candidates {
		films, err := queryItems(ctx, s.ro, filmsBelowSQL, []any{KindMovie, t.OwnerDir, t.OwnerDir})
		if err != nil {
			return nil, err
		}
		if ownsTrailer(films, t, titleID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// ownsTrailer reports whether the title is the owner of trailer t, given the film files
// below t's owner folder (see TitleTrailers).
func ownsTrailer(films []Item, t Item, titleID int64) bool {
	owners := map[string]bool{} // one key per film: its title, else what its name says
	for _, f := range films {
		owners[filmKey(f)] = true
	}
	if len(owners) == 1 {
		return true // the query found a file of this title below the folder
	}
	for _, f := range films {
		if f.TitleID == titleID && filepath.Dir(f.Path) == t.OwnerDir &&
			strings.EqualFold(f.Title, t.Title) && f.Year == t.Year {
			return true
		}
	}
	return false
}

func filmKey(f Item) string {
	if f.TitleID != 0 {
		return "t" + strconv.FormatInt(f.TitleID, 10)
	}
	return "n" + strings.ToLower(f.Title) + "\x00" + strconv.Itoa(f.Year)
}

// TitleFiles returns the files of a title, lowest id first: a film's versions, or a
// show's episodes. Trailers are not among them.
func (s *Store) TitleFiles(ctx context.Context, titleID int64) ([]Item, error) {
	films, err := queryItems(ctx, s.ro, `SELECT `+itemCols+` FROM items WHERE kind = ? AND title_id = ? ORDER BY id`,
		[]any{KindMovie, titleID})
	if err != nil || len(films) > 0 {
		return films, err
	}
	return queryItems(ctx, s.ro, `SELECT `+prefixed("i.", itemCols)+` FROM items i
		JOIN series_matches sm ON sm.show = i.show AND sm.title_id = ?
		WHERE i.kind = ? ORDER BY i.id`, []any{titleID, KindEpisode})
}
