// Package metadatatest has a fake metadata.Provider for the tests of the
// worker and the API: it answers from the data it is given and never touches
// the network.
package metadatatest

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// ShowOrder names a show in one episode order.
type ShowOrder struct {
	ID    int64
	Order string
}

// Fake is a metadata.Provider. Fill the fields before use; they are read
// without locking, so do not change them while it is in use (Err and Calls
// aside, which are safe through the methods).
//
// It stands for TMDB, or, with Source set to metadata.SourceTVDB, for TVDB: shows
// only, every season in each fetch, and the episode order of the fetch in the result.
type Fake struct {
	// Source is whose ids the fake gives: metadata.SourceTMDB (the default) or SourceTVDB.
	Source string
	// Search results by lower-case query; the year is ignored. A query that is
	// not here finds nothing.
	MovieSearch  map[string][]metadata.Candidate
	SeriesSearch map[string][]metadata.Candidate
	// FindMovies / FindSeries by "imdb_id:tt0133093" or "tvdb_id:81189".
	FindMovies map[string][]metadata.Candidate
	FindSeries map[string][]metadata.Candidate
	// What Movie, Series and Person return by TMDB id; an id that is not here
	// is a 404. Series returns a copy holding only the seasons asked for.
	Movies map[int64]*store.TitleInput
	Shows  map[int64]*store.TitleInput
	People map[int64]*store.Person
	// For a TVDB fake: a show in an episode order other than aired (Series falls back to
	// Shows for an order not here), and the show each thetvdb.com slug names.
	ShowOrders map[ShowOrder]*store.TitleInput
	Slugs      map[string]int64
	// What Trailers returns by the title's id (either kind), as it is; an id that is not here
	// is a 404.
	TrailerLists map[int64][]store.Video
	// What Changes returns.
	ChangedMovies []int64
	ChangedShows  []int64
	// ImageBase is where ImageURL points (an httptest server serving images).
	ImageBase string
	// Hook, when set, is called at the start of every method with the call as a
	// string ("Movie(603)"); a non-nil error is returned by the method. A test
	// can make calls slow or block them until the context is done.
	Hook func(ctx context.Context, call string) error

	mu    sync.Mutex
	err   error
	calls []string
}

var _ metadata.Provider = (*Fake)(nil)

// Name implements metadata.Provider.
func (f *Fake) Name() string {
	if f.Source == "" {
		return metadata.SourceTMDB
	}
	return f.Source
}

func (f *Fake) tvdb() bool { return f.Name() == metadata.SourceTVDB }

// Offline makes every method fail with a transient error, as when there is no
// network; Online undoes it.
func (f *Fake) Offline() {
	f.SetError(&metadata.Error{Op: "GET /", Transient: true, Message: "offline"})
}
func (f *Fake) Online() { f.SetError(nil) }

// SetError makes every method return err (nil: stop doing so).
func (f *Fake) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Calls returns the calls made so far, as "Method(args)" strings, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Count returns how many calls started with prefix ("Movie(", "Search").
func (f *Fake) Count(prefix string) int {
	n := 0
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *Fake) enter(ctx context.Context, format string, args ...any) error {
	call := fmt.Sprintf(format, args...)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	err := f.err
	f.mu.Unlock()
	if err == nil && f.Hook != nil {
		err = f.Hook(ctx, call)
	}
	return err
}

func notFound(op string) error {
	return &metadata.Error{Op: op, Status: http.StatusNotFound, Message: "The resource you requested could not be found."}
}

func (f *Fake) SearchMovies(ctx context.Context, query string, year int) ([]metadata.Candidate, error) {
	if err := f.enter(ctx, "SearchMovies(%q, %d)", query, year); err != nil {
		return nil, err
	}
	return slices.Clone(f.MovieSearch[strings.ToLower(query)]), nil
}

// Brief describes a show without its episodes (see metadata.Providers.Lookup).
func (f *Fake) Brief(ctx context.Context, id int64) (metadata.Candidate, error) {
	if err := f.enter(ctx, "Brief(%d)", id); err != nil {
		return metadata.Candidate{}, err
	}
	in, ok := f.Shows[id]
	if !ok {
		return metadata.Candidate{}, notFound(fmt.Sprintf("GET /series/%d/extended", id))
	}
	return metadata.Candidate{ID: id, Source: f.Name(), Title: in.Title.Title, OriginalTitle: in.OriginalTitle, Year: in.Year,
		Overview: in.Overview, Poster: in.Poster}, nil
}

// SeriesBySlug finds the show of a thetvdb.com address by Slugs.
func (f *Fake) SeriesBySlug(ctx context.Context, slug string) (int64, error) {
	if err := f.enter(ctx, "SeriesBySlug(%q)", slug); err != nil {
		return 0, err
	}
	if id, ok := f.Slugs[slug]; ok {
		return id, nil
	}
	return 0, metadata.ErrNoSuchTitle
}

func (f *Fake) SearchSeries(ctx context.Context, query string, year int) ([]metadata.Candidate, error) {
	if err := f.enter(ctx, "SearchSeries(%q, %d)", query, year); err != nil {
		return nil, err
	}
	return slices.Clone(f.SeriesSearch[strings.ToLower(query)]), nil
}

func (f *Fake) Find(ctx context.Context, source, id string) (movies, series []metadata.Candidate, err error) {
	if err := f.enter(ctx, "Find(%s, %s)", source, id); err != nil {
		return nil, nil, err
	}
	key := source + ":" + id
	return slices.Clone(f.FindMovies[key]), slices.Clone(f.FindSeries[key]), nil
}

func (f *Fake) Movie(ctx context.Context, id int64) (*store.TitleInput, error) {
	if err := f.enter(ctx, "Movie(%d)", id); err != nil {
		return nil, err
	}
	in, ok := f.Movies[id]
	if !ok || f.tvdb() {
		return nil, notFound(fmt.Sprintf("GET /movie/%d", id))
	}
	cp := *in
	return &cp, nil
}

// Series gives the show. A TMDB fake gives the seasons asked for; a TVDB fake gives all
// of them, from ShowOrders for an order that has an entry there, else from Shows, and
// the result has the order of the call.
func (f *Fake) Series(ctx context.Context, id int64, order string, seasons []int) (*store.TitleInput, error) {
	if f.tvdb() {
		if order == "" {
			order = store.OrderAired
		}
		if err := f.enter(ctx, "Series(%d, %s)", id, order); err != nil {
			return nil, err
		}
	} else if err := f.enter(ctx, "Series(%d, %v)", id, seasons); err != nil {
		return nil, err
	}
	in, ok := f.Shows[id]
	if v, has := f.ShowOrders[ShowOrder{id, order}]; has && f.tvdb() {
		in, ok = v, true
	}
	if !ok {
		return nil, notFound(fmt.Sprintf("GET /tv/%d", id))
	}
	cp := *in
	if f.tvdb() {
		cp.EpisodeOrder, cp.ResetSeasons = order, len(cp.Seasons) > 0
		return &cp, nil
	}
	cp.Seasons = nil
	for _, se := range in.Seasons {
		if slices.Contains(seasons, se.Number) {
			cp.Seasons = append(cp.Seasons, se)
		}
	}
	return &cp, nil
}

// Trailers is the provider's list of a title's trailers.
func (f *Fake) Trailers(ctx context.Context, kind string, id int64) ([]store.Video, error) {
	if err := f.enter(ctx, "Trailers(%s,%d)", kind, id); err != nil {
		return nil, err
	}
	vs, ok := f.TrailerLists[id]
	if !ok {
		return nil, notFound(fmt.Sprintf("GET /%s/%d/videos", kind, id))
	}
	return slices.Clone(vs), nil
}

func (f *Fake) Person(ctx context.Context, id int64) (*store.Person, error) {
	if err := f.enter(ctx, "Person(%d)", id); err != nil {
		return nil, err
	}
	p, ok := f.People[id]
	if !ok {
		return nil, notFound(fmt.Sprintf("GET /person/%d", id))
	}
	cp := *p
	return &cp, nil
}

func (f *Fake) Changes(ctx context.Context, kind string, _, _ time.Time) ([]int64, error) {
	if err := f.enter(ctx, "Changes(%s)", kind); err != nil {
		return nil, err
	}
	if kind == store.KindSeries {
		return slices.Clone(f.ChangedShows), nil
	}
	return slices.Clone(f.ChangedMovies), nil
}

func (f *Fake) Check(ctx context.Context) error { return f.enter(ctx, "Check()") }

func (f *Fake) ImageURL(size, file string) string {
	if f.tvdb() {
		if !metadata.ValidTVDBImage(size, file) {
			return ""
		}
		return strings.TrimRight(f.ImageBase, "/") + "/tvdb/" + size + "/" + strings.TrimPrefix(file, "/")
	}
	if !metadata.ValidSize(size) || !metadata.ValidImagePath(file) {
		return ""
	}
	return strings.TrimRight(f.ImageBase, "/") + "/" + size + "/" + strings.TrimPrefix(file, "/")
}
