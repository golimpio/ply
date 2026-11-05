package metadata_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// pacer is a WorkerOptions.Pace a test can steer: it counts its calls, can refuse from the
// (n+1)th on, and can hold every call until released.
type pacer struct {
	mu      sync.Mutex
	calls   int
	allowed int           // calls that succeed before it starts to refuse; -1 never refuses
	gate    chan struct{} // when not nil, a call waits for it to be closed (or for its context)
	onCall  func()        // when not nil, run at the start of a call: something that happens during the pass
}

var errPaceStop = errors.New("the queue wants the machine")

func newPacer() *pacer { return &pacer{allowed: -1} }

func (p *pacer) pace(ctx context.Context) error {
	p.mu.Lock()
	p.calls++
	n, allowed, gate, onCall := p.calls, p.allowed, p.gate, p.onCall
	p.mu.Unlock()
	if onCall != nil {
		onCall()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if allowed >= 0 && n > allowed {
		return errPaceStop
	}
	return nil
}

func (p *pacer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// refuseAfter makes the calls from now on refuse once n more have succeeded.
func (p *pacer) refuseAfter(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowed = p.calls + n
}

func (p *pacer) allowAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowed = -1
}

func dune() *store.TitleInput {
	return &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 438631, Title: "Dune", Year: 2021, Overview: "v1", Source: store.SourceTMDB}}
}

// twoMatchedMovies is an environment with the pacer, whose movies The Matrix and Dune are matched.
func twoMatchedMovies(t *testing.T, p *pacer) (e *wenv, matrixItem, duneItem int64) {
	t.Helper()
	e = newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withMatrix()
	e.fake.MovieSearch["dune"] = []metadata.Candidate{{ID: 438631, Title: "Dune", OriginalTitle: "Dune", Year: 2021, Popularity: 90}}
	e.fake.Movies[438631] = dune()
	matrixItem = e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	duneItem = e.addMovie("Movies/Dune (2021).mkv", "Dune", 2021)
	e.once()
	for _, id := range []int64{matrixItem, duneItem} {
		if got := e.entry(id); got.MatchState != store.MatchMatched {
			t.Fatalf("setup: %+v", got.Item)
		}
	}
	return e, matrixItem, duneItem
}

func TestWorkerPaceIsCalledOnceForEachItem(t *testing.T) {
	p := newPacer()
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withMatrix()
	e.withBreakingBad()
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.addMovie("Movies/Other (2000).mkv", "Other", 2000) // nothing found: still an item
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	if p.count() != 3 {
		t.Errorf("%d calls for 2 movies and a show, want 3", p.count())
	}
	// Nothing is due now: no item, no call.
	before := p.count()
	e.once()
	if p.count() != before {
		t.Errorf("a pass with nothing to do called Pace %d times", p.count()-before)
	}

	// Two sidecar notifications and a refresh somebody asked for: one call each.
	p3 := newPacer()
	e3 := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p3.pace })
	e3.withMatrix()
	id := e3.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e3.once()
	base := p3.count()
	e3.w.NotifySidecars([]int64{id, id + 1000}) // the second is no item: it is still an entry of the queue
	e3.w.RefreshItem(id)
	e3.once()
	if got := p3.count() - base; got != 3 {
		t.Errorf("%d calls for two notifications and a refresh, want 3", got)
	}
}

func TestWorkerPaceThatBlocksHoldsThePass(t *testing.T) {
	p := newPacer()
	p.gate = make(chan struct{})
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)

	done := make(chan struct{})
	go func() { e.once(); close(done) }()
	select {
	case <-done:
		t.Fatal("the pass finished while Pace held it")
	case <-time.After(300 * time.Millisecond):
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("the provider was asked %d times while Pace held the pass: %q", n, e.fake.Calls())
	}
	if got := e.entry(id); got.MatchState != store.MatchPending {
		t.Errorf("item = %+v while held", got.Item)
	}
	if !e.w.Status(e.ctx).Running {
		t.Error("the status does not say the pass is running")
	}
	close(p.gate)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass did not go on after Pace let it")
	}
	if got := e.entry(id); got.MatchState != store.MatchMatched {
		t.Errorf("item = %+v after Pace let the pass go on", got.Item)
	}
}

// A Pace that holds the pass is let go by the pass's context, and the pass ends.
func TestWorkerPaceThatBlocksIsReleasedByTheContext(t *testing.T) {
	p := newPacer()
	p.gate = make(chan struct{})
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.w.RunOnce(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass did not end when its context did")
	}
	if got := e.entry(id); got.MatchState != store.MatchPending {
		t.Errorf("item = %+v", got.Item)
	}
	if attempts, _ := attemptsOf(t, e, id); attempts != 0 {
		t.Errorf("a cancelled pass counted %d attempts", attempts)
	}
}

// An error from Pace ends the pass with nothing noted, nothing marked and nothing asked of the
// provider for the item it was called for; the next pass takes up where this one stopped.
func TestWorkerPaceThatRefusesStopsThePassWithoutSideEffects(t *testing.T) {
	p := newPacer()
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Pace = p.pace })
	e.withMatrix()
	e.fake.MovieSearch["dune"] = []metadata.Candidate{{ID: 438631, Title: "Dune", OriginalTitle: "Dune", Year: 2021, Popularity: 90}}
	e.fake.Movies[438631] = dune()
	e.withBreakingBad()
	first := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	second := e.addMovie("Movies/Dune (2021).mkv", "Dune", 2021)
	e.addEpisode("Breaking Bad", 1, 1)

	p.refuseAfter(1)
	e.once()
	if got := e.entry(first); got.MatchState != store.MatchMatched {
		t.Errorf("the item Pace let through: %+v", got.Item)
	}
	if got := e.entry(second); got.MatchState != store.MatchPending || got.TitleID != 0 {
		t.Errorf("the item Pace refused: %+v", got.Item)
	}
	if attempts, next := attemptsOf(t, e, second); attempts != 0 || next != 0 {
		t.Errorf("the refused item was marked: %d attempts, next %d; it must be as it was", attempts, next)
	}
	if n := e.fake.Count(`SearchMovies("Dune"`); n != 0 {
		t.Errorf("the provider was asked about the refused item")
	}
	if sm, err := e.st.GetSeriesMatch(e.ctx, "Breaking Bad"); err == nil && sm.State != store.MatchPending {
		t.Errorf("the show after the pass stopped = %+v", sm)
	}
	if n := e.fake.Count("SearchSeries"); n != 0 {
		t.Error("the pass went on to the shows after Pace refused")
	}
	if st := e.w.Status(e.ctx); st.LastError != "" || st.Running {
		t.Errorf("status = %+v: a refusal to go on is not an error", st)
	}

	// Let it go on: the rest is done by the next pass.
	p.allowAll()
	e.once()
	if got := e.entry(second); got.MatchState != store.MatchMatched {
		t.Errorf("the refused item on the next pass: %+v", got.Item)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchMatched {
		t.Errorf("the show on the next pass: %+v", sm)
	}
}

// What the pass has taken out of its queues (sidecar notifications, refreshes people asked
// for) and has not done when Pace stops it is put back, in order, ahead of what came since.
func TestWorkerPaceThatRefusesLeavesTheInboxForTheNextPass(t *testing.T) {
	p := newPacer()
	e, matrixItem, duneItem := twoMatchedMovies(t, p)

	// An NFO that appears next to each file: only the notification makes the worker read it.
	nfo := func(rel, plot string) { e.write(rel, `<movie><title>x</title><plot>`+plot+`</plot></movie>`) }
	nfo("Movies/The Matrix (1999).nfo", "Matrix plot from the NFO.")
	nfo("Movies/Dune (2021).nfo", "Dune plot from the NFO.")
	titleOf := func(item int64) store.Title { return e.title(e.entry(item).TitleID) }

	p.refuseAfter(1)
	calls := len(e.fake.Calls())
	e.w.NotifySidecars([]int64{matrixItem, duneItem})
	e.w.RefreshItem(matrixItem)
	e.w.RefreshItem(duneItem)
	e.once()
	// One item was taken in hand (the first of the sidecars, in id order), the rest refused.
	reread := 0
	for _, item := range []int64{matrixItem, duneItem} {
		if titleOf(item).Source == store.SourceMixed {
			reread++
		}
	}
	if reread != 1 {
		t.Errorf("%d sidecar notifications were handled in the pass that Pace stopped, want 1", reread)
	}
	if rest := e.fake.Calls()[calls:]; len(rest) != 1 {
		t.Errorf("provider calls in the stopped pass = %q; one reread of an NFO over a TMDB title fetches it once", rest)
	}

	// The next pass does the rest: the other notification, then both refreshes, in the order they were asked.
	p.allowAll()
	calls = len(e.fake.Calls())
	e.once()
	if titleOf(matrixItem).Source != store.SourceMixed || titleOf(duneItem).Source != store.SourceMixed {
		t.Errorf("after the next pass: %q, %q", titleOf(matrixItem).Source, titleOf(duneItem).Source)
	}
	var movies []string
	for _, c := range e.fake.Calls()[calls:] {
		if len(c) > 6 && c[:6] == "Movie(" {
			movies = append(movies, c)
		}
	}
	if want := []string{"Movie(603)", "Movie(438631)"}; len(movies) < 2 || !slices.Equal(movies[len(movies)-2:], want) {
		t.Errorf("the refreshes ran as %q, want %q last, in the order asked", movies, want)
	}
	// Nothing left: another pass makes no calls.
	calls = len(e.fake.Calls())
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("a refresh ran twice: %q", rest)
	}
}

// A refresh asked for while Pace is refusing is not lost, and one that came after the refused ones
// runs after them.
func TestWorkerPaceThatRefusesKeepsTheOrderOfJobs(t *testing.T) {
	p := newPacer()
	e, matrixItem, duneItem := twoMatchedMovies(t, p)
	p.refuseAfter(0) // refuse at the first item
	calls := len(e.fake.Calls())
	e.w.RefreshItem(matrixItem)
	// Dune's refresh is asked for while the pass is under way: after the Matrix's, which the
	// pass has already taken out of the queue.
	var once sync.Once
	p.mu.Lock()
	p.onCall = func() { once.Do(func() { e.w.RefreshItem(duneItem) }) }
	p.mu.Unlock()
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Fatalf("calls while Pace refused: %q", rest)
	}
	p.allowAll()
	e.once()
	var movies []string
	for _, c := range e.fake.Calls()[calls:] {
		if len(c) > 6 && c[:6] == "Movie(" {
			movies = append(movies, c)
		}
	}
	if want := []string{"Movie(603)", "Movie(438631)"}; !slices.Equal(movies, want) {
		t.Errorf("refreshes ran as %q, want %q", movies, want)
	}
}
