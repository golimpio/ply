package metadata

import (
	"context"
	"errors"
	"time"

	"ply/internal/store"
)

// ErrNoSuchTitle means the provider has nothing under the id (or IMDb id, or
// address) given.
var ErrNoSuchTitle = errors.New("the provider has no such title")

// Ref names a title for a manual match: by TMDB id, by TVDB id (a show), by the slug
// of its address at thetvdb.com (a show), or by IMDb id. Give one.
type Ref struct {
	TMDBID   int64
	TVDBID   int64
	TVDBSlug string
	IMDbID   string
}

// resolveMovie returns the TMDB id a Ref names, asking TMDB's find for an IMDb id.
func resolveMovie(ctx context.Context, tm Provider, ref Ref) (int64, error) {
	if ref.TMDBID > 0 {
		return ref.TMDBID, nil
	}
	if ref.IMDbID == "" {
		return 0, ErrNoSuchTitle // a TVDB ref: TVDB has no movies
	}
	movies, _, err := tm.Find(ctx, ExternalIMDb, ref.IMDbID)
	if err != nil {
		return 0, err
	}
	if len(movies) == 0 {
		return 0, ErrNoSuchTitle
	}
	return movies[0].ID, nil
}

func (w *Worker) provider(ctx context.Context) (*Providers, error) {
	p, _ := w.o.Source.Provider(ctx)
	if p == nil {
		return nil, ErrLookupsOff
	}
	return p, nil
}

// MatchMovie links a movie file to the TMDB title ref names, as a person's
// decision (state "manual": it is never matched again on its own). The errors
// are ErrLookupsOff, ErrNoTMDB, ErrNoSuchTitle, store.ErrNotFound for an unknown
// item, or what TMDB said (see IsTransient).
func (w *Worker) MatchMovie(ctx context.Context, itemID int64, ref Ref) error {
	p, err := w.provider(ctx)
	if err != nil {
		return err
	}
	tm := p.Movies()
	if tm == nil {
		return ErrNoTMDB
	}
	e, err := w.o.Store.GetEntry(ctx, itemID)
	if err != nil {
		return err
	}
	if e.Kind != store.KindMovie {
		return errors.New("metadata: not a movie")
	}
	id, err := resolveMovie(ctx, tm, ref)
	if err != nil {
		return err
	}
	in, err := tm.Movie(ctx, id)
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

// MatchShow is MatchMovie for a whole show. The title is TVDB's when ref names one at
// TVDB (a TVDB id, a slug) or an IMDb id that TVDB knows (it is looked up there first
// when TVDB is on); TMDB's when it names one at TMDB. A person's pick of a TMDB title
// while TVDB is on stays TMDB's. ErrNoTVDB or ErrNoTMDB when the ref needs a provider
// that is not set up.
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
	sr, err := p.ResolveShow(ctx, ref)
	if err != nil {
		return err
	}
	seasons, err := w.o.Store.ShowSeasons(ctx, show)
	if err != nil {
		return err
	}
	in, err := w.fetchShow(ctx, p, sr.Source, sr.ID, seasons)
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

// Search lists the titles a query could mean, best first, for the "fix match"
// dialog: movies at TMDB; shows at TVDB, then TMDB, when TVDB is on (each result says
// which, in Source). A query that is "tvdb:81189" or the address of a show at
// thetvdb.com names that show: the one result, with score 1. ErrLookupsOff when nothing
// can be asked.
func (w *Worker) Search(ctx context.Context, q Query) ([]Scored, error) {
	p, err := w.provider(ctx)
	if err != nil {
		return nil, err
	}
	if q.Kind == store.KindSeries {
		if c, named, err := p.Lookup(ctx, q.Title); named {
			if err != nil {
				return nil, err
			}
			return []Scored{c}, nil
		}
		return p.SearchShows(ctx, q)
	}
	tm := p.Movies()
	if tm == nil {
		return nil, ErrNoTMDB
	}
	return Search(ctx, tm, q)
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
	_, err := w.EnsurePersonID(ctx, id)
	return err
}

// EnsurePersonID is EnsurePerson that returns the id the person has afterwards. A person
// of TVDB whose record gives a TMDB id that another person has is that person: the two
// are merged, and the id is the one that is left (the same as id otherwise).
//
// A person TMDB knows is asked of TMDB, whose details are fuller, when it is on; one
// only TVDB knows is asked of TVDB.
func (w *Worker) EnsurePersonID(ctx context.Context, id int64) (int64, error) {
	d, err := w.o.Store.GetPerson(ctx, id)
	if err != nil {
		return id, err
	}
	if d.FetchedAt != 0 || (d.TMDBID == 0 && d.TVDBID == 0) {
		return id, nil
	}
	now := w.now()
	w.mu.Lock()
	f, failed := w.personFailed[id]
	w.mu.Unlock()
	if failed && now.Before(f.until) {
		return id, f.err
	}
	p, err := w.provider(ctx)
	if err != nil {
		return id, err
	}
	var (
		info *store.Person
		from string
	)
	switch {
	case d.TMDBID != 0 && p.TMDB != nil:
		from = SourceTMDB
		info, err = p.TMDB.Person(ctx, d.TMDBID)
	case d.TVDBID != 0 && p.TVDB != nil:
		from = SourceTVDB
		info, err = p.TVDB.Person(ctx, d.TVDBID)
	default:
		return id, ErrLookupsOff
	}
	if err != nil {
		w.notePersonFailure(ctx, id, f.attempts, err, now)
		return id, err
	}
	w.mu.Lock()
	delete(w.personFailed, id)
	w.mu.Unlock()
	if from == SourceTVDB {
		// The store finds the survivor if another request merged this person meanwhile. On any
		// other failure the id stays what it was: 0 would be a page for no one.
		survivor, err := w.o.Store.SetPersonDetailsMerged(ctx, id, *info, w.now().Unix())
		if err != nil {
			return id, err
		}
		return survivor, nil
	}
	return id, w.o.Store.SetPersonDetails(ctx, id, *info, w.now().Unix())
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
