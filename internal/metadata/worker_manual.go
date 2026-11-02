package metadata

import (
	"context"
	"errors"

	"ply/internal/store"
)

// ErrNoSuchTitle means TMDB has nothing under the id (or IMDb id) given.
var ErrNoSuchTitle = errors.New("TMDB has no such title")

// Ref names a TMDB title for a manual match: by TMDB id, or by IMDb id.
type Ref struct {
	TMDBID int64
	IMDbID string
}

// resolve returns the TMDB id a Ref names, asking TMDB's find for an IMDb id.
func resolve(ctx context.Context, p Provider, kind string, ref Ref) (int64, error) {
	if ref.TMDBID > 0 {
		return ref.TMDBID, nil
	}
	movies, series, err := p.Find(ctx, ExternalIMDb, ref.IMDbID)
	if err != nil {
		return 0, err
	}
	found := movies
	if kind == store.KindSeries {
		found = series
	}
	if len(found) == 0 {
		return 0, ErrNoSuchTitle
	}
	return found[0].TMDBID, nil
}

func (w *Worker) provider(ctx context.Context) (Provider, error) {
	p, _ := w.o.Source.Provider(ctx)
	if p == nil {
		return nil, ErrLookupsOff
	}
	return p, nil
}

// MatchMovie links a movie file to the TMDB title ref names, as a person's
// decision (state "manual": it is never matched again on its own). The errors
// are ErrLookupsOff, ErrNoSuchTitle, store.ErrNotFound for an unknown item, or
// what TMDB said (see IsTransient).
func (w *Worker) MatchMovie(ctx context.Context, itemID int64, ref Ref) error {
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	e, err := w.o.Store.GetEntry(ctx, itemID)
	if err != nil {
		return err
	}
	if e.Kind != store.KindMovie {
		return errors.New("metadata: not a movie")
	}
	id, err := resolve(ctx, p, store.KindMovie, ref)
	if err != nil {
		return err
	}
	in, err := p.Movie(ctx, id)
	if IsNotFound(err) {
		return ErrNoSuchTitle
	}
	if err != nil {
		return err
	}
	overlayNFO(in, w.localMovie(ctx, e.Item, dirNames).nfo)
	// A person's decision always wins, and the title and the link are stored in
	// one step, so that no prune can take the title in between.
	if _, _, err := w.o.Store.LinkItemTitle(ctx, itemID, in, store.Match{State: store.MatchManual, Score: 1}, ""); err != nil {
		return err
	}
	w.prefetch(ctx, in)
	_, err = w.o.Store.PruneMetadata(ctx)
	return err
}

// MatchShow is MatchMovie for a whole show.
func (w *Worker) MatchShow(ctx context.Context, show string, ref Ref) error {
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	items, err := w.o.Store.ShowItems(ctx, show)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return store.ErrNotFound
	}
	id, err := resolve(ctx, p, store.KindSeries, ref)
	if err != nil {
		return err
	}
	seasons, err := w.o.Store.ShowSeasons(ctx, show)
	if err != nil {
		return err
	}
	in, err := p.Series(ctx, id, seasons)
	if IsNotFound(err) {
		return ErrNoSuchTitle
	}
	if err != nil {
		return err
	}
	dir := showDir(items[0].Path)
	overlayNFO(in, w.readNFO(ShowLocal(dir, dirNames(dir)).NFO))
	if _, _, err := w.o.Store.LinkSeriesTitle(ctx, show, in, store.Match{State: store.MatchManual, Score: 1}, ""); err != nil {
		return err
	}
	w.prefetch(ctx, in)
	_, err = w.o.Store.PruneMetadata(ctx)
	return err
}

// UnmatchItem takes the metadata off a movie: it goes back to what its file
// name says, and is not matched again on its own.
func (w *Worker) UnmatchItem(ctx context.Context, itemID int64) error {
	if err := w.o.Store.SetItemMatch(ctx, itemID, store.Match{State: store.MatchManual}); err != nil {
		return err
	}
	_, err := w.o.Store.PruneMetadata(ctx)
	return err
}

// UnmatchShow is UnmatchItem for a show.
func (w *Worker) UnmatchShow(ctx context.Context, show string) error {
	if err := w.o.Store.SetSeriesMatch(ctx, show, store.Match{State: store.MatchManual}); err != nil {
		return err
	}
	_, err := w.o.Store.PruneMetadata(ctx)
	return err
}

// Search lists the TMDB titles a query could mean, best first, for the "fix
// match" dialog. ErrLookupsOff when TMDB is not available.
func (w *Worker) Search(ctx context.Context, q Query) ([]Scored, error) {
	p, err := w.provider(ctx)
	if err != nil {
		return nil, err
	}
	return Search(ctx, p, q)
}

// EnsurePerson fetches a person's details from TMDB the first time they are
// wanted and stores them. A person already fetched, or one an NFO named, is left
// alone.
func (w *Worker) EnsurePerson(ctx context.Context, id int64) error {
	d, err := w.o.Store.GetPerson(ctx, id)
	if err != nil {
		return err
	}
	if d.FetchedAt != 0 || d.TMDBID == 0 {
		return nil
	}
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	info, err := p.Person(ctx, d.TMDBID)
	if err != nil {
		return err
	}
	return w.o.Store.SetPersonDetails(ctx, id, *info, w.now().Unix())
}
