package metadata

import (
	"context"
	"errors"
	"time"

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
	local := w.localMovie(ctx, e.Item, dirNames)
	overlayNFO(in, local.nfo)
	// A person's decision always wins, and the title and the link are stored in
	// one step, so that no prune can take the title in between.
	titleID, _, err := w.o.Store.LinkItemTitle(ctx, itemID, in, store.Match{State: store.MatchManual, Score: 1}, "")
	if err != nil {
		return err
	}
	w.noteNFO(titleID, local.loc.NFO)
	w.committed(ctx, in)
	return nil
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
	nfoPath := ShowLocal(dir, dirNames(dir)).NFO
	overlayNFO(in, w.readNFO(nfoPath))
	titleID, _, err := w.o.Store.LinkSeriesTitle(ctx, show, in, store.Match{State: store.MatchManual, Score: 1}, "")
	if err != nil {
		return err
	}
	w.noteNFO(titleID, nfoPath)
	w.committed(ctx, in)
	return nil
}

// committed is what follows a manual match that is stored: the match stands
// whatever happens next, so the person is not kept waiting for it. Orphans are
// pruned at once, under a context that outlives the request; the images are
// fetched in the background.
func (w *Worker) committed(ctx context.Context, in *store.TitleInput) {
	if _, err := w.o.Store.PruneMetadata(context.WithoutCancel(ctx)); err != nil {
		w.noteError(err)
	}
	w.prefetchLater(in)
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

// How long a person TMDB failed to give details for is left alone: one TMDB
// does not know for a day; one it could not be asked about for a while that
// doubles with each failure.
const (
	personMissingTTL = 24 * time.Hour
	personRetryBase  = 5 * time.Minute
	personRetryMax   = time.Hour
	maxPersonFailed  = 1024 // people remembered; the oldest entries give way
)

// personFailure is why a person's details are not asked for again until.
type personFailure struct {
	until    time.Time
	attempts int
	err      error
}

// EnsurePerson fetches a person's details from TMDB the first time they are
// wanted and stores them. A person already fetched, or one an NFO named, is left
// alone. A failure is remembered (see personFailure) and returned again until it
// is time to try again, so that opening the page of a person TMDB does not
// have, or while TMDB is out of reach, asks it nothing each time.
func (w *Worker) EnsurePerson(ctx context.Context, id int64) error {
	d, err := w.o.Store.GetPerson(ctx, id)
	if err != nil {
		return err
	}
	if d.FetchedAt != 0 || d.TMDBID == 0 {
		return nil
	}
	now := w.now()
	w.mu.Lock()
	f, failed := w.personFailed[id]
	w.mu.Unlock()
	if failed && now.Before(f.until) {
		return f.err
	}
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	info, err := p.Person(ctx, d.TMDBID)
	if err != nil {
		w.notePersonFailure(ctx, id, f.attempts, err, now)
		return err
	}
	w.mu.Lock()
	delete(w.personFailed, id)
	w.mu.Unlock()
	return w.o.Store.SetPersonDetails(ctx, id, *info, w.now().Unix())
}

// notePersonFailure remembers a failed fetch. A refused key and a caller that
// went away say nothing about the person and are not remembered; a timeout does
// (TMDB did not answer).
func (w *Worker) notePersonFailure(ctx context.Context, id int64, attempts int, err error, now time.Time) {
	if IsUnauthorized(err) || errors.Is(err, context.Canceled) || (ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded)) {
		return
	}
	f := personFailure{attempts: attempts + 1, err: err}
	if IsNotFound(err) {
		f.until = now.Add(personMissingTTL)
	} else {
		f.until = now.Add(min(personRetryBase<<min(attempts, 10), personRetryMax))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.personFailed) >= maxPersonFailed {
		for k, v := range w.personFailed {
			if !now.Before(v.until) {
				delete(w.personFailed, k)
			}
		}
		if len(w.personFailed) >= maxPersonFailed {
			return // all of them still count: this one is asked again next time
		}
	}
	w.personFailed[id] = f
}
