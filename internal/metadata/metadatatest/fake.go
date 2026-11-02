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

// Fake is a metadata.Provider. Fill the fields before use; they are read
// without locking, so do not change them while it is in use (Err and Calls
// aside, which are safe through the methods).
type Fake struct {
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
	if !ok {
		return nil, notFound(fmt.Sprintf("GET /movie/%d", id))
	}
	cp := *in
	return &cp, nil
}

func (f *Fake) Series(ctx context.Context, id int64, seasons []int) (*store.TitleInput, error) {
	if err := f.enter(ctx, "Series(%d, %v)", id, seasons); err != nil {
		return nil, err
	}
	in, ok := f.Shows[id]
	if !ok {
		return nil, notFound(fmt.Sprintf("GET /tv/%d", id))
	}
	cp := *in
	cp.Seasons = nil
	for _, se := range in.Seasons {
		if slices.Contains(seasons, se.Number) {
			cp.Seasons = append(cp.Seasons, se)
		}
	}
	return &cp, nil
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
	if !metadata.ValidSize(size) || !metadata.ValidImagePath(file) {
		return ""
	}
	return strings.TrimRight(f.ImageBase, "/") + "/" + size + "/" + strings.TrimPrefix(file, "/")
}
