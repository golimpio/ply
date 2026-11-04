package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/store"
)

// fakeMatcher records what the restorer asks the metadata worker to apply.
type fakeMatcher struct {
	mu     sync.Mutex
	movies []int64  // item ids
	shows  []string // show names
	refs   []int64  // the TMDB ids asked for, in order
	errs   []error  // answers for the next calls, in order; then nil
}

func (m *fakeMatcher) next() error {
	if len(m.errs) == 0 {
		return nil
	}
	err := m.errs[0]
	m.errs = m.errs[1:]
	return err
}

func (m *fakeMatcher) MatchMovie(_ context.Context, itemID int64, ref metadata.Ref) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.movies = append(m.movies, itemID)
	m.refs = append(m.refs, ref.TMDBID)
	return m.next()
}

func (m *fakeMatcher) MatchShow(_ context.Context, show string, ref metadata.Ref) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shows = append(m.shows, show)
	m.refs = append(m.refs, ref.TMDBID)
	return m.next()
}

func (m *fakeMatcher) snapshot() (movies []int64, shows []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.movies), slices.Clone(m.shows)
}

func newRestorer(t *testing.T, m matcher) (*matchRestorer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return newMatchRestorer(st, m, slog.New(slog.DiscardHandler)), st
}

func addItem(t *testing.T, st *store.Store, it *store.Item) int64 {
	t.Helper()
	it.Root, it.Size = "/lib", 100
	id, err := st.UpsertItem(context.Background(), it, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// archivedMatches puts the library through a removal of /lib and a return, with a
// manual match on a movie and on a show: the matches wait in the archive for whoever
// applies them. It returns the movie and the episodes.
func archivedMatches(t *testing.T, st *store.Store) (movie int64, lost []int64) {
	t.Helper()
	ctx := context.Background()
	items := func() (int64, []int64) {
		m := addItem(t, st, &store.Item{Kind: store.KindMovie, Title: "M", Path: "/lib/m.mkv"})
		e1 := addItem(t, st, &store.Item{Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 1, Path: "/lib/Lost/1.mkv"})
		e2 := addItem(t, st, &store.Item{Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 2, Path: "/lib/Lost/2.mkv"})
		return m, []int64{e1, e2}
	}
	movie, lost = items()
	mt, _ := st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix"}})
	lt, _ := st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 4607, Title: "Lost"}})
	if err := st.SetItemMatch(ctx, movie, store.Match{TitleID: mt, State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: lt, State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	// What pruning does with a show nobody has; the restore is by path.
	if _, err := st.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	movie, lost = items()
	for _, p := range []string{"/lib/m.mkv", "/lib/Lost/1.mkv", "/lib/Lost/2.mkv"} {
		id := map[string]int64{"/lib/m.mkv": movie, "/lib/Lost/1.mkv": lost[0], "/lib/Lost/2.mkv": lost[1]}[p]
		if _, _, _, err := st.RestoreArchived(ctx, id, p, 100); err != nil {
			t.Fatal(err)
		}
	}
	return movie, lost
}

// pending reports every match the archive holds, as the scanner does after a scan.
func pending(t *testing.T, st *store.Store, r *matchRestorer) []store.PendingMatch {
	t.Helper()
	list, err := st.PendingMatches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		r.add(m.ItemID, m.TMDBID, m.Kind)
	}
	return list
}

func waits(list []store.PendingMatch, itemID int64) bool {
	return slices.ContainsFunc(list, func(p store.PendingMatch) bool { return p.ItemID == itemID })
}

// A movie's match goes to MatchMovie; a show's, however many of its episodes are
// reported, to MatchShow once; each is forgotten once it is applied.
func TestMatchRestorerAppliesMatchesAndForgetsThem(t *testing.T) {
	m := &fakeMatcher{}
	r, st := newRestorer(t, m)
	movie, _ := archivedMatches(t, st)
	if list := pending(t, st, r); len(list) != 3 {
		t.Fatalf("control: %d matches waiting, want the movie's and the two episodes'", len(list))
	}
	r.drain(context.Background())

	movies, shows := m.snapshot()
	if !slices.Equal(movies, []int64{movie}) {
		t.Errorf("MatchMovie was asked for items %v, want [%d]", movies, movie)
	}
	if !slices.Equal(shows, []string{"Lost"}) {
		t.Errorf("MatchShow was asked for %v, want Lost once", shows)
	}
	if !slices.Equal(m.refs, []int64{603, 4607}) {
		t.Errorf("TMDB ids = %v", m.refs)
	}
	if list, _ := st.PendingMatches(context.Background()); len(list) != 0 {
		t.Errorf("%v still wait in the archive after they were applied", list)
	}
}

// A match is queued once however many times it is reported before it is applied.
func TestMatchRestorerQueuesAMatchOnce(t *testing.T) {
	m := &fakeMatcher{}
	r, st := newRestorer(t, m)
	movie, _ := archivedMatches(t, st)
	for range 3 {
		r.add(movie, 603, store.KindMovie)
	}
	r.drain(context.Background())
	if movies, _ := m.snapshot(); len(movies) != 1 {
		t.Errorf("MatchMovie was called %d times", len(movies))
	}
	r.add(movie, 603, store.KindMovie) // a later scan reports it again: it is on the books no more
	r.drain(context.Background())
	if movies, _ := m.snapshot(); len(movies) != 2 {
		t.Errorf("MatchMovie was called %d times after a second report, want 2", len(movies))
	}
}

// With online lookups off the match is not applied and not lost: it waits in the
// archive, and the report after the next scan finds lookups on.
func TestMatchRestorerKeepsAMatchWhileLookupsAreOff(t *testing.T) {
	m := &fakeMatcher{errs: []error{metadata.ErrLookupsOff}}
	r, st := newRestorer(t, m)
	movie, _ := archivedMatches(t, st)
	r.add(movie, 603, store.KindMovie)
	r.drain(context.Background())
	if list, _ := st.PendingMatches(context.Background()); !waits(list, movie) {
		t.Fatalf("the movie's match is not in the archive any more: %v", list)
	}

	r.add(movie, 603, store.KindMovie) // the scan after the key was set
	r.drain(context.Background())
	if movies, _ := m.snapshot(); len(movies) != 2 {
		t.Errorf("MatchMovie was called %d times, want 2", len(movies))
	}
	if list, _ := st.PendingMatches(context.Background()); waits(list, movie) {
		t.Errorf("the match was applied, but waits still: %v", list)
	}
}

// A file that is not an episode any more gets no show match and keeps the archive
// row; a title TMDB no longer knows is dropped; any other error keeps the match.
func TestMatchRestorerSkipsWhatDoesNotFit(t *testing.T) {
	m := &fakeMatcher{errs: []error{metadata.ErrNoSuchTitle, errors.New("boom")}}
	r, st := newRestorer(t, m)
	movie, lost := archivedMatches(t, st)
	r.add(movie, 603, store.KindSeries)    // asked as a show: it is a movie
	r.add(movie, 603, store.KindMovie)     // TMDB does not know it: dropped
	r.add(lost[0], 4607, store.KindSeries) // fails some other way: kept
	r.add(movie, 1, "podcast")             // nonsense
	r.add(movie+100, 1, store.KindSeries)  // gone from the library
	r.drain(context.Background())

	movies, shows := m.snapshot()
	if !slices.Equal(movies, []int64{movie}) || !slices.Equal(shows, []string{"Lost"}) {
		t.Errorf("MatchMovie calls %v and MatchShow calls %v, want one each", movies, shows)
	}
	list, _ := st.PendingMatches(context.Background())
	if waits(list, movie) {
		t.Errorf("the match of a title TMDB does not know was kept: %v", list)
	}
	if !waits(list, lost[0]) {
		t.Errorf("the match that failed was dropped: %v", list)
	}
}

// TMDB being out of reach is tried again later, a few times; the match is still
// there for the scan after that.
func TestMatchRestorerRetriesWhatMayPass(t *testing.T) {
	oldRetry, oldRetries := restoreRetry, restoreRetries
	restoreRetry, restoreRetries = 5*time.Millisecond, 2
	t.Cleanup(func() { restoreRetry, restoreRetries = oldRetry, oldRetries })
	down := &metadata.Error{Op: "GET /movie/603", Transient: true, Err: errors.New("connection refused")}
	m := &fakeMatcher{errs: []error{down, down, down, down}}
	r, st := newRestorer(t, m)
	movie, _ := archivedMatches(t, st)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()
	r.add(movie, 603, store.KindMovie)

	deadline := time.Now().Add(5 * time.Second)
	for {
		movies, _ := m.snapshot()
		if len(movies) >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // and no more than the first try plus two
	cancel()
	<-done
	if movies, _ := m.snapshot(); len(movies) != 3 {
		t.Errorf("MatchMovie was called %d times, want 3 (the try and 2 retries)", len(movies))
	}
	if list, _ := st.PendingMatches(context.Background()); !waits(list, movie) {
		t.Errorf("the match was lost when TMDB stayed out of reach: %v", list)
	}
}

// firstTyped names the folders a flag types that were not saved before: they may have
// items from before types existed, which are read again as the type says.
func TestFirstTyped(t *testing.T) {
	flags := []library.Root{
		{Path: "/films", Kind: library.KindMovies}, // saved before
		{Path: "/tv", Kind: library.KindShows},     // not saved: typed for the first time
		{Path: "/media"},                           // -media: no type given
		{Path: "/more", Kind: library.KindMovies},  // not saved
	}
	before := map[string]string{"/films": library.KindMovies}
	if got, want := firstTyped(flags, before), []string{"/tv", "/more"}; !slices.Equal(got, want) {
		t.Errorf("firstTyped = %v, want %v", got, want)
	}
	if got := firstTyped(nil, before); len(got) != 0 {
		t.Errorf("firstTyped with no flags = %v", got)
	}
}

// previousRoots gives the scanner the types the folders had before this start's
// flags: a type a flag changed differs, a folder that was not saved is read as it is.
func TestPreviousRoots(t *testing.T) {
	roots := []library.Root{
		{Path: "/films", Kind: library.KindShows}, // a flag made it shows; it was movies
		{Path: "/tv", Kind: library.KindShows},    // unchanged
		{Path: "/new", Kind: library.KindMovies},  // not saved before
		{Path: "/media"},                          // -media, not guessed yet
	}
	before := map[string]string{"/films": library.KindMovies, "/tv": library.KindShows}
	want := []library.Root{
		{Path: "/films", Kind: library.KindMovies}, {Path: "/tv", Kind: library.KindShows},
		{Path: "/new", Kind: library.KindMovies}, {Path: "/media"},
	}
	if got := previousRoots(roots, before); !slices.Equal(got, want) {
		t.Errorf("previousRoots = %v, want %v", got, want)
	}
	if roots[0].Kind != library.KindShows {
		t.Error("previousRoots changed its argument")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddFolder(context.Background(), "/films", library.KindMovies, false); err != nil {
		t.Fatal(err)
	}
	got, err := storedKinds(context.Background(), st)
	if err != nil || len(got) != 1 || got["/films"] != library.KindMovies {
		t.Errorf("storedKinds = %v, %v", got, err)
	}
}
