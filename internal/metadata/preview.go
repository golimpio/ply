package metadata

import (
	"context"
	"time"

	"ply/internal/store"
)

// previewFetch bounds a preview: the dialog that asks shows what it has meanwhile.
const previewFetch = 10 * time.Second

// Preview describes a title the library does not have, as TMDB has it (kind store.KindMovie or
// store.KindSeries, tmdbID TMDB's id): what a person's page shows of one of their credits
// outside the library. Nothing is stored, and the fetch stops when ctx is done (the dialog
// was closed). ErrLookupsOff when nothing may be asked, ErrNoTMDB when TMDB is not set up.
func (w *Worker) Preview(ctx context.Context, kind string, tmdbID int64) (*store.TitleInput, error) {
	p, _ := w.o.Source.Provider(ctx)
	if p == nil {
		return nil, ErrLookupsOff
	}
	if p.TMDB == nil {
		return nil, ErrNoTMDB
	}
	ctx, cancel := context.WithTimeout(ctx, previewFetch)
	defer cancel()
	if kind == store.KindSeries {
		return p.TMDB.Series(ctx, tmdbID, "", nil)
	}
	return p.TMDB.Movie(ctx, tmdbID)
}
