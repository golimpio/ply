package metadata_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/probe"
	"ply/internal/store"
)

var jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("image-bytes")...)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type wenv struct {
	t       *testing.T
	st      *store.Store
	fake    *metadatatest.Fake
	src     *metadata.StaticSource
	w       *metadata.Worker
	clock   *testClock
	images  *metadata.ImageCache
	imgDir  string
	imgHits atomic.Int32
	imgSrv  *httptest.Server
	// imgHang makes the image server hold requests until imgRelease is closed
	// (by close), as a CDN that hangs does.
	imgHang    atomic.Bool
	imgRelease chan struct{}
	imgOnce    sync.Once
	media      string
	ctx        context.Context
}

// close releases everything the environment holds; tests that count goroutines
// call it themselves, the others leave it to t.Cleanup.
func (e *wenv) close() {
	e.imgOnce.Do(func() { close(e.imgRelease) })
	e.w.Close()
	e.images.Close()
	e.imgSrv.Close()
	e.st.Close()
}

// newWEnv builds a worker over a real store, a fake provider and an image cache
// that downloads from a local server.
func newWEnv(t *testing.T, mod ...func(*metadata.WorkerOptions)) *wenv {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "My Data") // a space on purpose
	st, err := store.Open(filepath.Join(dir, "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &wenv{t: t, st: st, fake: &metadatatest.Fake{}, clock: &testClock{t: time.Now().Truncate(time.Second)},
		media: filepath.Join(dir, "media"), imgDir: filepath.Join(dir, "images"), ctx: t.Context(), imgRelease: make(chan struct{})}

	e.imgSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.imgHits.Add(1)
		if e.imgHang.Load() {
			select {
			case <-e.imgRelease:
			case <-r.Context().Done():
			}
			return
		}
		w.Write(jpeg)
	}))
	e.fake.ImageBase = e.imgSrv.URL
	e.src = metadata.NewStaticSource(e.fake)
	e.images, err = metadata.NewImageCache(metadata.ImageOptions{Dir: e.imgDir, URL: e.src.ImageURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.close)

	o := metadata.WorkerOptions{Store: st, Source: e.src, Images: e.images, Refresh: 24 * time.Hour, Now: e.clock.Now}
	for _, m := range mod {
		m(&o)
	}
	e.w = metadata.NewWorker(o)
	return e
}

func (e *wenv) once() { e.t.Helper(); e.w.RunOnce(e.ctx) }

func (e *wenv) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.media, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *wenv) addMovie(rel, title string, year int) int64 {
	e.t.Helper()
	p := e.write(rel, "video")
	id, err := e.st.UpsertItem(e.ctx, &store.Item{Kind: store.KindMovie, Title: title, Year: year, Path: p, Root: e.media,
		Size: 5, MTime: 1, Container: "mkv"}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *wenv) addEpisode(show string, season, ep int) int64 {
	e.t.Helper()
	p := e.write(fmt.Sprintf("TV/%s/Season %d/%s S%02dE%02d.mkv", show, season, show, season, ep), "video")
	id, err := e.st.UpsertItem(e.ctx, &store.Item{Kind: store.KindEpisode, Show: show, Season: season, Episode: ep, Path: p,
		Root: e.media, Size: 5, MTime: 1, Container: "mkv"}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *wenv) entry(id int64) store.Entry {
	e.t.Helper()
	en, err := e.st.GetEntry(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return en
}

func (e *wenv) title(id int64) store.Title {
	e.t.Helper()
	t, err := e.st.GetTitle(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *wenv) series(show string) store.SeriesMatch {
	e.t.Helper()
	sm, err := e.st.GetSeriesMatch(e.ctx, show)
	if err != nil {
		e.t.Fatal(err)
	}
	return sm
}

func (e *wenv) count(table string) int {
	e.t.Helper()
	n, err := e.countErr(table)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *wenv) countErr(table string) (int, error) {
	switch table {
	case "titles":
		cards, err := e.st.Titles(e.ctx, store.TitleFilter{})
		return len(cards), err
	}
	return 0, fmt.Errorf("unknown table %s", table)
}

func matrix() *store.TitleInput {
	return &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 603, IMDbID: "tt0133093", Title: "The Matrix",
		OriginalTitle: "The Matrix", SortTitle: "Matrix, The", Year: 1999, Overview: "A hacker learns.", Tagline: "Wake up.",
		ReleaseDate: "1999-03-30", RuntimeMin: 136, Rating: 8.2, VoteCount: 25000, Certification: "R",
		Poster: "tmdb:/p603.jpg", Backdrop: "tmdb:/b603.jpg", Source: store.SourceTMDB},
		Genres: []store.Genre{{ID: 28, Name: "Action"}},
		Credits: []store.Credit{
			{Person: store.Person{TMDBID: 6384, Name: "Keanu Reeves", Profile: "tmdb:/keanu.jpg"}, Kind: store.CreditCast, Character: "Neo"},
		}}
}

func (e *wenv) withMatrix() {
	e.fake.MovieSearch = map[string][]metadata.Candidate{
		"the matrix": {{TMDBID: 603, Title: "The Matrix", OriginalTitle: "The Matrix", Year: 1999, Popularity: 80}},
	}
	e.fake.Movies = map[int64]*store.TitleInput{603: matrix()}
}

func breakingBad() *store.TitleInput {
	ep := func(n int) store.Episode { return store.Episode{Number: n, Name: fmt.Sprintf("Episode %d", n)} }
	return &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 1396, Title: "Breaking Bad", Year: 2008,
		Status: "Ended", Poster: "tmdb:/bb.jpg", Backdrop: "tmdb:/bbb.jpg", Source: store.SourceTMDB},
		Seasons: []store.Season{
			{Number: 1, Name: "Season 1", Episodes: []store.Episode{ep(1), ep(2)}},
			{Number: 2, Name: "Season 2", Episodes: []store.Episode{ep(1)}},
			{Number: 3, Name: "Season 3", Episodes: []store.Episode{ep(1)}},
		}}
}

func (e *wenv) withBreakingBad() {
	e.fake.SeriesSearch = map[string][]metadata.Candidate{
		"breaking bad": {{TMDBID: 1396, Title: "Breaking Bad", OriginalTitle: "Breaking Bad", Year: 2008, Popularity: 150}},
	}
	e.fake.Shows = map[int64]*store.TitleInput{1396: breakingBad()}
}

func TestWorkerMatchesANewMovieAndPrefetchesItsImages(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	if got := e.entry(id); got.MatchState != store.MatchPending || got.TitleID != 0 {
		t.Fatalf("new item: %+v", got.Item)
	}

	e.once()
	got := e.entry(id)
	if got.MatchState != store.MatchMatched || got.TitleID == 0 || got.NeedsReview {
		t.Fatalf("after a pass: %+v", got.Item)
	}
	title := e.title(got.TitleID)
	if title.TMDBID != 603 || title.Title != "The Matrix" || title.Source != store.SourceTMDB {
		t.Errorf("title = %+v", title)
	}
	d, err := e.st.GetTitleDetail(e.ctx, got.TitleID)
	if err != nil || len(d.Cast) != 1 || d.Cast[0].Name != "Keanu Reeves" || d.MatchState != store.MatchMatched {
		t.Errorf("detail = %+v, %v", d, err)
	}
	if want := []string{`SearchMovies("The Matrix", 1999)`, "Movie(603)"}; !slices.Equal(e.fake.Calls(), want) {
		t.Errorf("calls = %q, want %q", e.fake.Calls(), want)
	}
	// The poster (w342) and the backdrop (w1280) are on disk, nothing else.
	for _, f := range []string{"tmdb/w342/p603.jpg", "tmdb/w1280/b603.jpg"} {
		if _, err := os.Stat(filepath.Join(e.imgDir, f)); err != nil {
			t.Errorf("not prefetched: %s", f)
		}
	}
	if e.imgHits.Load() != 2 {
		t.Errorf("%d image downloads, want 2", e.imgHits.Load())
	}

	// Nothing is left to do: the next pass asks TMDB nothing.
	before := len(e.fake.Calls())
	e.once()
	if len(e.fake.Calls()) != before {
		t.Errorf("a second pass made calls: %q", e.fake.Calls()[before:])
	}
}

func TestWorkerFlagsDoubtfulMatchesAndUnmatchesTheRest(t *testing.T) {
	e := newWEnv(t)
	e.fake.MovieSearch = map[string][]metadata.Candidate{
		// Right title, wrong year: 0.6 + 0 + 0.1 = 0.7.
		"dune": {{TMDBID: 438631, Title: "Dune", Year: 2021, Popularity: 100}},
		// Nothing like it.
		"foo bar baz": {{TMDBID: 11111, Title: "Foo", Year: 2005, Popularity: 3}},
	}
	e.fake.Movies = map[int64]*store.TitleInput{
		438631: {Title: store.Title{Kind: store.KindMovie, TMDBID: 438631, Title: "Dune", Year: 2021}},
	}
	doubt := e.addMovie("Movies/Dune (1990).mkv", "Dune", 1990)
	lost := e.addMovie("Movies/Foo Bar Baz (2010).mkv", "Foo Bar Baz", 2010)
	none := e.addMovie("Movies/Zzzz (2001).mkv", "Zzzz", 2001)

	e.once()
	if got := e.entry(doubt); got.MatchState != store.MatchMatched || !got.NeedsReview || got.TitleID == 0 {
		t.Errorf("doubtful: %+v", got.Item)
	}
	for name, id := range map[string]int64{"poor match": lost, "no results": none} {
		got := e.entry(id)
		if got.MatchState != store.MatchUnmatched || got.TitleID != 0 || got.NeedsReview {
			t.Errorf("%s: %+v", name, got.Item)
		}
	}
	if e.fake.Count("Movie(11111)") != 0 {
		t.Error("a poor match was fetched")
	}
	list, _ := e.st.ReviewList(e.ctx)
	if len(list) != 3 {
		t.Errorf("review list = %d entries, want 3", len(list))
	}
}

func TestWorkerOfflineBacksOffAndNeverUnmatches(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.fake.Offline()

	// Each failure: still pending, attempts up, due again after the next delay.
	for i, wantDelay := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 6 * time.Hour} {
		start := e.clock.Now()
		before := len(e.fake.Calls())
		e.once()
		got := e.entry(id)
		if got.MatchState != store.MatchPending {
			t.Fatalf("failure %d: state %s; a file must never be unmatched because TMDB was out of reach", i+1, got.MatchState)
		}
		attempts, next := attemptsOf(t, e, id)
		if attempts != i+1 || next != start.Add(wantDelay).Unix() {
			t.Errorf("failure %d: attempts %d next +%v; want %d and +%v", i+1, attempts, time.Duration(next-start.Unix())*time.Second, i+1, wantDelay)
		}
		if len(e.fake.Calls()) == before {
			t.Errorf("failure %d: TMDB was not asked", i+1)
		}
		// Before the delay is over nothing is asked again.
		e.clock.Advance(wantDelay - time.Second)
		mark := len(e.fake.Calls())
		e.once()
		if len(e.fake.Calls()) != mark {
			t.Errorf("failure %d: asked again %v early: %q", i+1, time.Second, e.fake.Calls()[mark:])
		}
		e.clock.Advance(time.Second)
	}

	// Back online: matched, and the bookkeeping is reset.
	e.fake.Online()
	e.once()
	got := e.entry(id)
	attempts, _ := attemptsOf(t, e, id)
	if got.MatchState != store.MatchMatched || attempts != 0 {
		t.Errorf("after coming back online: %+v, attempts %d", got.Item, attempts)
	}
}

func attemptsOf(t *testing.T, e *wenv, id int64) (int, int64) {
	t.Helper()
	m, err := e.st.GetItemMatch(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return m.Attempts, m.NextAttemptAt
}

func TestWorkerHonoursRetryAfterOverTheBackoff(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.fake.SetError(&metadata.Error{Op: "GET /search/movie", Status: 429, Transient: true, RetryAfter: 3 * time.Hour})
	start := e.clock.Now()
	e.once()
	if _, next := attemptsOf(t, e, id); next != start.Add(3*time.Hour).Unix() {
		t.Errorf("next attempt in %v, want 3h (TMDB's Retry-After beats the 1m backoff)", time.Duration(next-start.Unix())*time.Second)
	}
}

func TestWorkerStopsWhenTheKeyIsRefused(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	ids := []int64{
		e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999),
		e.addMovie("Movies/Other (2000).mkv", "Other", 2000),
		e.addMovie("Movies/Third (2001).mkv", "Third", 2001),
	}
	e.fake.SetError(&metadata.Error{Op: "GET /search/movie", Status: 401, Message: "Invalid API key"})
	e.once()
	if n := e.fake.Count("SearchMovies"); n != 1 {
		t.Errorf("%d searches after a refused key, want 1 (asking again for each file only repeats it)", n)
	}
	for _, id := range ids {
		got := e.entry(id)
		attempts, _ := attemptsOf(t, e, id)
		if got.MatchState != store.MatchPending || attempts != 0 {
			t.Errorf("item %d: %+v attempts %d; a refused key leaves files untouched", id, got.Item, attempts)
		}
	}
	if st := e.w.Status(e.ctx); !strings.Contains(st.LastError, "Invalid API key") {
		t.Errorf("status last error = %q", st.LastError)
	}
	// Positive control: with a working key they are matched.
	e.fake.Online()
	e.once()
	if got := e.entry(ids[0]); got.MatchState != store.MatchMatched {
		t.Errorf("with a good key: %+v", got.Item)
	}
	if st := e.w.Status(e.ctx); st.LastError != "" {
		t.Errorf("last error not cleared: %q", st.LastError)
	}
}

func TestWorkerWithoutAKeyKeepsFilesPendingAndReadsTheNFO(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.src.Set(nil, true) // no key
	withNFO := e.addMovie("Movies/Home Video (2011)/Home Video (2011).mkv", "Home Video", 2011)
	e.write("Movies/Home Video (2011)/Home Video (2011).nfo",
		`<movie><title>Home Video</title><year>2011</year><plot>Our holiday.</plot><genre>Family</genre>
		<actor><name>Uncle Bob</name><role>Himself</role></actor></movie>`)
	bare := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)

	e.once()
	got := e.entry(withNFO)
	if got.MatchState != store.MatchPending || got.TitleID == 0 {
		t.Fatalf("file with an NFO: %+v; want pending with its local title", got.Item)
	}
	title := e.title(got.TitleID)
	if title.TMDBID != 0 || title.Source != store.SourceNFO || title.Overview != "Our holiday." || title.Year != 2011 {
		t.Errorf("local title = %+v", title)
	}
	if got := e.entry(bare); got.MatchState != store.MatchPending || got.TitleID != 0 {
		t.Errorf("file without an NFO: %+v", got.Item)
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Errorf("TMDB was asked without a key: %q", e.fake.Calls())
	}
	st := e.w.Status(e.ctx)
	if !st.Enabled || st.KeySet || st.Pending != 2 {
		t.Errorf("status = %+v", st)
	}

	// Files are put off, not asked about every minute...
	e.once()
	if len(e.fake.Calls()) != 0 {
		t.Error("calls without a key")
	}
	// ...and once there is a key and the worker is told, they are matched.
	e.src.Set(e.fake, true)
	e.once()
	if got := e.entry(bare); got.MatchState != store.MatchPending {
		t.Errorf("matched before being told of the new key (they wait out the pause): %+v", got.Item)
	}
	e.w.Reload()
	e.once()
	if got := e.entry(bare); got.MatchState != store.MatchMatched {
		t.Errorf("after Reload: %+v", got.Item)
	}
	if got := e.entry(withNFO); got.MatchState != store.MatchUnmatched || e.title(got.TitleID).Source != store.SourceNFO {
		t.Errorf("file with a local NFO that TMDB cannot match: %+v; want unmatched, keeping its NFO title", got.Item)
	}
}

func TestWorkerPrivacySwitchOffAsksNothingAndFetchesNoImages(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.src.Set(e.fake, false) // -metadata=false
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	if len(e.fake.Calls()) != 0 || e.imgHits.Load() != 0 {
		t.Errorf("calls %q, image downloads %d with the switch off", e.fake.Calls(), e.imgHits.Load())
	}
	if got := e.entry(id); got.MatchState != store.MatchPending {
		t.Errorf("item = %+v", got.Item)
	}
	if st := e.w.Status(e.ctx); st.Enabled {
		t.Errorf("status = %+v; the switch is off", st)
	}
	if _, err := e.images.TMDB(e.ctx, "w342", "p603.jpg"); !errors.Is(err, metadata.ErrImageUnavailable) || e.imgHits.Load() != 0 {
		t.Errorf("an image was fetched with the switch off: %v", err)
	}
}

func TestWorkerNFOIdsAndOverlay(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()

	// An NFO with an id: no search, TMDB's data with the NFO's text over it.
	id := e.addMovie("Movies/neo/neo.mkv", "neo", 0)
	e.write("Movies/neo/neo.nfo", `<movie><title>Neo's Story</title><plot>Our plot.</plot><uniqueid type="tmdb">603</uniqueid></movie>`)
	e.once()
	got := e.entry(id)
	title := e.title(got.TitleID)
	if got.MatchState != store.MatchMatched || title.TMDBID != 603 || title.Source != store.SourceMixed ||
		title.Title != "Neo's Story" || title.Overview != "Our plot." || title.Tagline != "Wake up." || title.Year != 1999 {
		t.Errorf("item %+v title %+v", got.Item, title)
	}
	if n := e.fake.Count("SearchMovies"); n != 0 {
		t.Errorf("%d searches for a file whose NFO has the id", n)
	}

	// A wrong id in the NFO: fall back to the name.
	e.fake.MovieSearch["wrong"] = []metadata.Candidate{{TMDBID: 603, Title: "Wrong", Year: 2000}}
	bad := e.addMovie("Movies/wrong/wrong.mkv", "wrong", 2000)
	e.write("Movies/wrong/wrong.nfo", `<movie><uniqueid type="tmdb">999999</uniqueid></movie>`)
	calls := len(e.fake.Calls())
	e.once()
	if got := e.entry(bad); got.MatchState == store.MatchPending {
		t.Fatalf("bad id: %+v", got.Item)
	}
	if rest := e.fake.Calls()[calls:]; len(rest) < 2 || rest[0] != "Movie(999999)" || !strings.HasPrefix(rest[1], "SearchMovies(") {
		t.Errorf("calls after a bad id = %q; want Movie(999999) then a search", rest)
	}

	// An id in the file name works too: [tmdbid-603].
	named := e.addMovie("Movies/Whatever [tmdbid-603].mkv", "Whatever [tmdbid-603]", 0)
	calls = len(e.fake.Calls())
	e.once()
	if got := e.entry(named); got.MatchState != store.MatchMatched || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("id in the name: %+v", got.Item)
	}
	for _, c := range e.fake.Calls()[calls:] {
		if strings.HasPrefix(c, "Search") {
			t.Errorf("searched although the name has the id: %q", c)
		}
	}
}

func TestWorkerLocalArtworkIsRecorded(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999)/The Matrix (1999).mkv", "The Matrix", 1999)
	poster := e.write("Movies/The Matrix (1999)/poster.jpg", "p")
	back := e.write("Movies/The Matrix (1999)/Fanart.PNG", "b")
	e.once()
	got := e.entry(id)
	if got.LocalPoster != poster || got.LocalBackdrop != back {
		t.Errorf("art = %q %q, want %q %q", got.LocalPoster, got.LocalBackdrop, poster, back)
	}
	// The card prefers it to TMDB's.
	cards, _ := e.st.TitleCards(e.ctx, []int64{got.TitleID})
	if c := cards[got.TitleID]; c.Poster != store.LocalRef(store.ArtItem, id, "poster") {
		t.Errorf("card poster = %q", c.Poster)
	}
}

func TestWorkerMatchesAShowItsSeasonsAndNewSeasonsAtOnce(t *testing.T) {
	e := newWEnv(t)
	e.withBreakingBad()
	e.addEpisode("Breaking Bad", 1, 1)
	e.addEpisode("Breaking Bad", 1, 2)
	e.addEpisode("Breaking Bad", 2, 1)

	e.once()
	sm := e.series("Breaking Bad")
	if sm.State != store.MatchMatched || sm.TitleID == 0 || sm.NeedsReview {
		t.Fatalf("series = %+v", sm)
	}
	if want := []string{`SearchSeries("Breaking Bad", 0)`, "Series(1396, [1 2])"}; !slices.Equal(e.fake.Calls(), want) {
		t.Errorf("calls = %q, want %q", e.fake.Calls(), want)
	}
	if eps, _ := e.st.Episodes(e.ctx, sm.TitleID); len(eps) != 3 {
		t.Errorf("episodes = %+v, want seasons 1 and 2 only", eps)
	}
	// Posters were prefetched for the show, too.
	if _, err := os.Stat(filepath.Join(e.imgDir, "tmdb", "w342", "bb.jpg")); err != nil {
		t.Errorf("show poster not prefetched: %v", err)
	}

	// A file of season 3 appears: its episodes are fetched at once, without a rematch.
	e.addEpisode("Breaking Bad", 3, 1)
	need, _ := e.st.SeasonsNeedingFetch(e.ctx, sm.TitleID)
	if !slices.Equal(need, []int{3}) {
		t.Fatalf("seasons needing a fetch = %v", need)
	}
	calls := len(e.fake.Calls())
	e.w.Notify()
	e.once()
	if rest := e.fake.Calls()[calls:]; !slices.Equal(rest, []string{"Series(1396, [1 2 3])"}) {
		t.Errorf("calls for the new season = %q", rest)
	}
	if need, _ := e.st.SeasonsNeedingFetch(e.ctx, sm.TitleID); len(need) != 0 {
		t.Errorf("season still missing: %v", need)
	}
	if eps, _ := e.st.Episodes(e.ctx, sm.TitleID); len(eps) != 4 {
		t.Errorf("episodes = %d, want 4", len(eps))
	}

	// A season TMDB does not have is tried once, not on every pass.
	e.addEpisode("Breaking Bad", 9, 1)
	e.once()
	n := e.fake.Count("Series(")
	e.once()
	e.once()
	if e.fake.Count("Series(") != n {
		t.Errorf("a season TMDB has nothing for was asked for again at once (%d calls)", e.fake.Count("Series(")-n)
	}
	// Positive control: after the wait it is tried again.
	e.clock.Advance(7 * time.Hour)
	e.once()
	if e.fake.Count("Series(") != n+1 {
		t.Errorf("not retried after the wait (%d calls)", e.fake.Count("Series(")-n)
	}
}

func TestWorkerRefreshKeepsLockedTextButRefreshesTheRest(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	titleID := e.entry(id).TitleID

	mine := "My Matrix"
	if err := e.st.PatchTitle(e.ctx, titleID, store.TitlePatch{Title: &mine}); err != nil {
		t.Fatal(err)
	}
	updated := matrix()
	updated.Overview, updated.Rating, updated.Title.Title = "A new overview.", 9.5, "The Matrix (TMDB)"
	updated.Credits = append(updated.Credits, store.Credit{Person: store.Person{TMDBID: 2975, Name: "Laurence Fishburne"}, Kind: store.CreditCast, Character: "Morpheus"})
	e.fake.Movies[603] = updated

	e.w.RefreshItem(id)
	e.once()
	got := e.title(titleID)
	if got.Title != "My Matrix" || got.Overview != "A hacker learns." {
		t.Errorf("locked text was overwritten: %+v", got)
	}
	if got.Rating != 9.5 {
		t.Errorf("rating = %v; a locked title still refreshes its data", got.Rating)
	}
	if d, _ := e.st.GetTitleDetail(e.ctx, titleID); len(d.Cast) != 2 {
		t.Errorf("cast = %+v; credits refresh for locked titles too", d.Cast)
	}

	// Positive control: an unlocked title takes the new text.
	other := e.addMovie("Movies/Other (2000).mkv", "Other", 2000)
	e.fake.MovieSearch["other"] = []metadata.Candidate{{TMDBID: 7, Title: "Other", Year: 2000, Popularity: 1}}
	e.fake.Movies[7] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 7, Title: "Other", Year: 2000, Overview: "old"}}
	e.once()
	e.fake.Movies[7] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 7, Title: "Other", Year: 2000, Overview: "new"}}
	e.w.RefreshItem(other)
	e.once()
	if got := e.title(e.entry(other).TitleID); got.Overview != "new" {
		t.Errorf("unlocked overview = %q", got.Overview)
	}
}

// refreshFixture: three library titles, with known ages, and a fake that
// serves each of them.
func refreshFixture(t *testing.T, e *wenv, refresh time.Duration) (ids map[string]int64) {
	t.Helper()
	ids = map[string]int64{}
	e.fake.Movies = map[int64]*store.TitleInput{}
	e.fake.MovieSearch = map[string][]metadata.Candidate{}
	now := e.clock.Now()
	for _, m := range []struct {
		name    string
		tmdb    int64
		fetched time.Time
	}{
		{"fresh", 1, now}, {"stale", 2, now.Add(-45 * 24 * time.Hour)}, {"changed", 3, now.Add(-time.Hour)},
	} {
		in := &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: m.tmdb, Title: m.name, Year: 2000,
			ReleaseDate: "2000-01-01", Overview: "v1", FetchedAt: m.fetched.Unix()}}
		file := e.write("Movies/"+m.name+".mkv", "v")
		itemID, err := e.st.UpsertItem(e.ctx, &store.Item{Kind: store.KindMovie, Title: m.name, Year: 2000, Path: file, Root: e.media, Size: 1, MTime: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		titleID, err := e.st.UpsertTitle(e.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetItemMatch(e.ctx, itemID, store.Match{TitleID: titleID, State: store.MatchMatched, Score: 1}); err != nil {
			t.Fatal(err)
		}
		ids[m.name] = titleID
		e.fake.Movies[m.tmdb] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: m.tmdb, Title: m.name, Year: 2000, ReleaseDate: "2000-01-01", Overview: "v2"}}
	}
	return ids
}

func overview(t *testing.T, e *wenv, id int64) string { t.Helper(); return e.title(id).Overview }

func TestWorkerChangesFeedAndTheAgeFallback(t *testing.T) {
	e := newWEnv(t)
	ids := refreshFixture(t, e, 24*time.Hour)

	// First refresh: no poll on record, so by age. Only the stale title (45 days
	// old) is due; the poll time is recorded.
	e.once()
	if overview(t, e, ids["stale"]) != "v2" || overview(t, e, ids["fresh"]) != "v1" || overview(t, e, ids["changed"]) != "v1" {
		t.Errorf("by age: fresh %q stale %q changed %q", overview(t, e, ids["fresh"]), overview(t, e, ids["stale"]), overview(t, e, ids["changed"]))
	}
	if n := e.fake.Count("Changes"); n != 0 {
		t.Errorf("the changes feed was used without a previous poll (%d calls)", n)
	}
	if st := e.w.Status(e.ctx); st.LastRefresh == nil || !st.LastRefresh.Equal(e.clock.Now()) {
		t.Errorf("status last refresh = %v", st.LastRefresh)
	}
	v, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingChangesPoll)
	if !ok || v != fmt.Sprint(e.clock.Now().Unix()) {
		t.Errorf("poll time = %q, %v", v, ok)
	}

	// Within the period nothing is refreshed.
	e.fake.ChangedMovies = []int64{3}
	e.clock.Advance(23 * time.Hour)
	e.once()
	if e.fake.Count("Changes") != 0 || overview(t, e, ids["changed"]) != "v1" {
		t.Errorf("refreshed within the period: %q", e.fake.Calls())
	}

	// A day later: the feed names title 3 (and one that is not in the library).
	e.clock.Advance(2 * time.Hour)
	e.fake.ChangedMovies = []int64{3, 999}
	calls := len(e.fake.Calls())
	e.once()
	rest := e.fake.Calls()[calls:]
	if !slices.Equal(rest, []string{"Changes(movie)", "Changes(series)", "Movie(3)"}) {
		t.Errorf("calls = %q; want the feed, then only the title it names", rest)
	}
	if overview(t, e, ids["changed"]) != "v2" || overview(t, e, ids["fresh"]) != "v1" {
		t.Errorf("changed %q fresh %q", overview(t, e, ids["changed"]), overview(t, e, ids["fresh"]))
	}

	// A poll older than the feed's 14 days: by age again, no feed call. (The
	// titles are older than 30 days by then, so by age they are all due.)
	e.clock.Advance(31 * 24 * time.Hour)
	calls = len(e.fake.Calls())
	e.once()
	rest = e.fake.Calls()[calls:]
	if slices.ContainsFunc(rest, func(c string) bool { return strings.HasPrefix(c, "Changes") }) {
		t.Errorf("the feed was used after more than 14 days: %q", rest)
	}
	if len(rest) == 0 {
		t.Error("nothing was refreshed by age after 31 days")
	}
}

// A feed too long to follow is not a list of what changed: the refresh goes by
// age, the poll time stays, and the next refresh asks the feed for everything
// since then.
func TestWorkerChangesFeedTooLongFallsBackToAgeAndKeepsThePollTime(t *testing.T) {
	e := newWEnv(t)
	ids := refreshFixture(t, e, 24*time.Hour)
	e.once() // by age: the poll time is recorded
	poll, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingChangesPoll)
	if !ok {
		t.Fatal("no poll time recorded")
	}

	e.clock.Advance(25 * time.Hour)
	e.fake.ChangedMovies = []int64{3}
	e.fake.Hook = func(ctx context.Context, call string) error {
		if strings.HasPrefix(call, "Changes(") {
			return metadata.ErrChangesTruncated
		}
		return nil
	}
	e.once()
	if e.fake.Count("Changes(") == 0 {
		t.Fatal("the feed was not tried")
	}
	if got, _, _ := e.st.GetSetting(e.ctx, metadata.SettingChangesPoll); got != poll {
		t.Errorf("poll time = %q after a feed that could not be followed, want it kept at %q", got, poll)
	}
	if got, _, _ := e.st.GetSetting(e.ctx, metadata.SettingLastRefresh); got != fmt.Sprint(e.clock.Now().Unix()) {
		t.Errorf("last refresh = %q, want now (the refresh by age is done)", got)
	}
	if overview(t, e, ids["changed"]) != "v1" {
		t.Errorf("a title the feed never named was refreshed by it: %q", overview(t, e, ids["changed"]))
	}

	// The feed works again: it is asked for everything since the old poll time.
	e.fake.Hook = nil
	e.clock.Advance(25 * time.Hour)
	e.once()
	if overview(t, e, ids["changed"]) != "v2" {
		t.Errorf("the change was lost: %q", overview(t, e, ids["changed"]))
	}
	if got, _, _ := e.st.GetSetting(e.ctx, metadata.SettingChangesPoll); got != fmt.Sprint(e.clock.Now().Unix()) {
		t.Errorf("poll time = %q after a feed that worked, want now", got)
	}
}

func TestWorkerChangesFeedBoundaryIsFourteenDays(t *testing.T) {
	if metadata.ChangesWindow != 14*24*time.Hour {
		t.Errorf("ChangesWindow = %v; TMDB's changes feed reaches back 14 days", metadata.ChangesWindow)
	}
	for name, tc := range map[string]struct {
		since    time.Duration
		wantFeed bool
	}{
		"13 days":              {13 * 24 * time.Hour, true},
		"14 days":              {14 * 24 * time.Hour, true},
		"14 days and a second": {14*24*time.Hour + time.Second, false},
		"14 days and a minute": {14*24*time.Hour + time.Minute, false},
		"15 days and a second": {15*24*time.Hour + time.Second, false},
	} {
		e := newWEnv(t)
		refreshFixture(t, e, 24*time.Hour)
		e.w = metadata.NewWorker(metadata.WorkerOptions{Store: e.st, Source: e.src, Refresh: 24 * time.Hour, Now: e.clock.Now})
		e.st.SetSetting(e.ctx, metadata.SettingChangesPoll, fmt.Sprint(e.clock.Now().Add(-tc.since).Unix()))
		e.st.SetSetting(e.ctx, metadata.SettingLastRefresh, fmt.Sprint(e.clock.Now().Add(-tc.since).Unix()))
		e.once()
		if used := e.fake.Count("Changes") > 0; used != tc.wantFeed {
			t.Errorf("%s since the last poll: feed used = %v, want %v", name, used, tc.wantFeed)
		}
	}
}

func TestWorkerRefreshOffSkipsRefreshButNotMatching(t *testing.T) {
	e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Refresh = 0 })
	refreshFixture(t, e, 0)
	e.withMatrix()
	e.fake.Movies[3] = matrix() // the fixture's movies are in the fake already
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	if got := e.entry(id); got.MatchState != store.MatchMatched {
		t.Errorf("new file with refresh off: %+v", got.Item)
	}
	if e.fake.Count("Changes") != 0 || e.fake.Count("Movie(2)") != 0 {
		t.Errorf("refreshed with -metadata-refresh=0: %q", e.fake.Calls())
	}
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingLastRefresh); ok {
		t.Error("a refresh was recorded")
	}
}

func TestWorkerFailedRefreshIsRetriedLater(t *testing.T) {
	e := newWEnv(t)
	refreshFixture(t, e, 24*time.Hour)
	e.fake.SetError(&metadata.Error{Transient: true, Message: "offline"})
	e.once()
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingLastRefresh); ok {
		t.Error("a failed refresh was recorded as done")
	}
	attempts := e.fake.Count("Movie(")
	e.once() // inside the 15 minute pause
	if e.fake.Count("Movie(") != attempts {
		t.Error("retried at once")
	}
	e.fake.Online()
	e.clock.Advance(16 * time.Minute)
	e.once()
	if _, ok, _ := e.st.GetSetting(e.ctx, metadata.SettingLastRefresh); !ok {
		t.Error("the retry did not complete the refresh")
	}
}

func TestWorkerRefreshesAShowWhenItsEpisodeHasAired(t *testing.T) {
	e := newWEnv(t)
	e.withBreakingBad()
	e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	titleID := e.series("Breaking Bad").TitleID

	// The next episode aired yesterday and the title was fetched before that.
	yesterday := e.clock.Now().Add(-30 * time.Hour).Format("2006-01-02")
	in := breakingBad()
	in.Status, in.NextAirDate, in.FetchedAt = "Returning Series", yesterday, e.clock.Now().Add(-48*time.Hour).Unix()
	if _, err := e.st.UpsertTitle(e.ctx, in); err != nil {
		t.Fatal(err)
	}
	fresh := breakingBad()
	fresh.Overview, fresh.Status = "new episode", "Returning Series"
	next := e.clock.Now().Add(6 * 24 * time.Hour).Format("2006-01-02")
	fresh.NextAirDate, fresh.NextEpisodeSeason, fresh.NextEpisodeNumber, fresh.NextEpisodeName = next, 5, 7, "Ozymandias"
	e.fake.Shows[1396] = fresh
	e.fake.Hook = nil
	e.once()
	if e.title(titleID).Overview != "new episode" {
		t.Errorf("a show whose episode aired was not refreshed: %+v", e.title(titleID))
	}
	// The refresh stores the episode to wait for next, with its date.
	if got := e.title(titleID); got.NextAirDate != next || got.NextEpisodeSeason != 5 || got.NextEpisodeNumber != 7 || got.NextEpisodeName != "Ozymandias" {
		t.Errorf("next episode after the refresh: %q S%dE%d %q", got.NextAirDate, got.NextEpisodeSeason, got.NextEpisodeNumber, got.NextEpisodeName)
	}
}

func TestWorkerSidecarNotificationsReadLocalFilesWithoutRematching(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999)/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	if got := e.entry(id); got.LocalPoster != "" {
		t.Fatalf("poster before it exists: %q", got.LocalPoster)
	}

	poster := e.write("Movies/The Matrix (1999)/poster.jpg", "p")
	e.write("Movies/The Matrix (1999)/The Matrix (1999).nfo", `<movie><title>The Matrix Reloaded Cut</title></movie>`)
	calls := len(e.fake.Calls())
	e.w.NotifySidecars([]int64{id})
	e.once()
	got := e.entry(id)
	if got.LocalPoster != poster {
		t.Errorf("poster = %q, want %q", got.LocalPoster, poster)
	}
	for _, c := range e.fake.Calls()[calls:] {
		if strings.HasPrefix(c, "Search") {
			t.Errorf("a sidecar change searched again: %q", c)
		}
	}
	title := e.title(got.TitleID)
	if title.Title != "The Matrix Reloaded Cut" || title.Source != store.SourceMixed || title.TMDBID != 603 {
		t.Errorf("title after the NFO appeared: %+v", title)
	}

	// The poster goes away.
	if err := os.Remove(poster); err != nil {
		t.Fatal(err)
	}
	e.w.NotifySidecars([]int64{id, 424242}) // an id that is gone is ignored
	e.once()
	if got := e.entry(id); got.LocalPoster != "" {
		t.Errorf("poster after removal = %q", got.LocalPoster)
	}

	// An NFO-only title is rebuilt without any network.
	e.src.Set(nil, true)
	local := e.addMovie("Movies/Home/Home.mkv", "Home", 2011)
	e.write("Movies/Home/Home.nfo", `<movie><title>Home</title><plot>v1</plot></movie>`)
	e.once()
	localTitle := e.entry(local).TitleID
	e.write("Movies/Home/Home.nfo", `<movie><title>Home</title><plot>v2</plot></movie>`)
	calls = len(e.fake.Calls())
	e.w.NotifySidecars([]int64{local})
	e.once()
	if e.title(localTitle).Overview != "v2" || len(e.fake.Calls()) != calls {
		t.Errorf("local title %q, %d calls", e.title(localTitle).Overview, len(e.fake.Calls())-calls)
	}
}

func TestWorkerSidecarsOfEpisodesAndShows(t *testing.T) {
	e := newWEnv(t)
	e.withBreakingBad()
	ep := e.addEpisode("Breaking Bad", 1, 1)
	e.once()
	showPoster := e.write("TV/Breaking Bad/poster.jpg", "p")
	thumb := e.write("TV/Breaking Bad/Season 1/Breaking Bad S01E01-thumb.jpg", "t")
	e.w.NotifySidecars([]int64{ep})
	e.once()
	if got := e.entry(ep); got.LocalThumb != thumb {
		t.Errorf("thumb = %q, want %q", got.LocalThumb, thumb)
	}
	if sm := e.series("Breaking Bad"); sm.LocalPoster != showPoster {
		t.Errorf("show poster = %q, want %q", sm.LocalPoster, showPoster)
	}
}

// 40 episodes of one show move: the scanner reports all of them. That is one
// show to read again, and TMDB is asked nothing while its NFO is the same.
func TestWorkerSidecarBatchOfAShowReadsItOnceAndAsksTMDBOnlyWhenTheNFOChanged(t *testing.T) {
	e := newWEnv(t)
	e.withBreakingBad()
	nfo := func(plot string) {
		e.write("TV/Breaking Bad/tvshow.nfo", `<tvshow><title>Breaking Bad</title><plot>`+plot+`</plot></tvshow>`)
	}
	nfo("v1")
	var ids []int64
	for n := 1; n <= 40; n++ {
		ids = append(ids, e.addEpisode("Breaking Bad", 1, n))
	}
	e.once()
	sm := e.series("Breaking Bad")
	if sm.State != store.MatchMatched || e.title(sm.TitleID).Overview != "v1" {
		t.Fatalf("show after the first pass: %+v, overview %q", sm, e.title(sm.TitleID).Overview)
	}
	if e.fake.Count("Series(") == 0 {
		t.Fatal("control: the show was not fetched")
	}
	thumb := e.write("TV/Breaking Bad/Season 1/Breaking Bad S01E40-thumb.jpg", "t")

	// Nothing about the NFO changed: the artwork is read for every episode, the
	// show not at all.
	calls := len(e.fake.Calls())
	e.w.NotifySidecars(ids)
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("an unchanged NFO cost %d calls to TMDB: %q", len(rest), rest)
	}
	if got := e.entry(ids[39]); got.LocalThumb != thumb {
		t.Errorf("thumb of the last episode = %q, want %q", got.LocalThumb, thumb)
	}

	// The NFO changed, TMDB is out of reach: the show is tried once, not once
	// per episode, and the change is not marked as taken in.
	nfo("version two")
	e.fake.Offline()
	calls = len(e.fake.Calls())
	e.w.NotifySidecars(ids)
	e.once()
	if n := e.fake.Count("Series(") - countPrefix(e.fake.Calls()[:calls], "Series("); n != 1 {
		t.Errorf("a changed NFO read the show %d times for 40 episodes, want 1", n)
	}
	if e.title(sm.TitleID).Overview != "v1" {
		t.Errorf("overview = %q while TMDB was out of reach", e.title(sm.TitleID).Overview)
	}

	// Back online the next notification takes the change in, once.
	e.fake.Online()
	calls = len(e.fake.Calls())
	e.w.NotifySidecars(ids)
	e.once()
	if n := e.fake.Count("Series(") - countPrefix(e.fake.Calls()[:calls], "Series("); n != 1 {
		t.Errorf("the changed NFO was taken in with %d fetches, want 1", n)
	}
	if got := e.title(sm.TitleID); got.Overview != "version two" || got.Source != store.SourceMixed {
		t.Errorf("title after the NFO changed: %+v", got)
	}
	calls = len(e.fake.Calls())
	e.w.NotifySidecars(ids)
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("a notification after the change was taken in cost calls: %q", rest)
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Two files of one movie are one title to read again, and only the NFO of the
// first one is laid over it.
func TestWorkerSidecarBatchOfAMovieWithTwoFilesReadsTheTitleOnce(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	a := e.addMovie("Movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", "The Matrix", 1999)
	b := e.addMovie("Movies/The Matrix (1999)/The Matrix (1999) 4K.mkv", "The Matrix", 1999)
	e.once()
	if e.entry(a).TitleID == 0 || e.entry(a).TitleID != e.entry(b).TitleID {
		t.Fatalf("the files are not one title: %d %d", e.entry(a).TitleID, e.entry(b).TitleID)
	}
	e.write("Movies/The Matrix (1999)/The Matrix (1999) 4K.nfo", `<movie><title>The Matrix</title><plot>of the second file</plot></movie>`)
	e.fake.Offline() // nothing is stored, so every attempt is counted
	calls := len(e.fake.Calls())
	e.w.NotifySidecars([]int64{b})
	e.once()
	if rest := e.fake.Calls()[calls:]; len(rest) != 0 {
		t.Errorf("the NFO of a second version cost calls: %q", rest)
	}
	e.write("Movies/The Matrix (1999)/The Matrix (1999) 1080p.nfo", `<movie><title>The Matrix</title><plot>of the first file</plot></movie>`)
	e.w.NotifySidecars([]int64{b, a})
	e.once()
	if n := countPrefix(e.fake.Calls()[calls:], "Movie("); n != 1 {
		t.Errorf("a movie with two files was fetched %d times, want 1", n)
	}
	e.fake.Online()
	e.w.NotifySidecars([]int64{a, b})
	e.once()
	if got := e.title(e.entry(a).TitleID); got.Overview != "of the first file" {
		t.Errorf("overview = %q, want the first file's", got.Overview)
	}
}

func TestWorkerPrunesOrphansAfterABatch(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	titleID := e.entry(id).TitleID
	if e.count("titles") != 1 {
		t.Fatal("no title to prune")
	}
	if err := e.st.DeleteItems(e.ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetTitle(e.ctx, titleID); err != nil {
		t.Fatal("positive control: the title is still stored before the pass")
	}
	e.w.Notify()
	e.once() // nothing to match: an idle pass does no pruning
	if _, err := e.st.GetTitle(e.ctx, titleID); err != nil {
		t.Errorf("an idle pass pruned: %v", err)
	}
	e.addMovie("Movies/Other (2000).mkv", "Other", 2000)
	e.once() // a pass that did work does
	if _, err := e.st.GetTitle(e.ctx, titleID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("orphan title after a batch: %v", err)
	}
}

func TestWorkerStatus(t *testing.T) {
	e := newWEnv(t)
	e.fake.MovieSearch = map[string][]metadata.Candidate{"dune": {{TMDBID: 1, Title: "Dune", Year: 2021, Popularity: 1}}}
	e.fake.Movies = map[int64]*store.TitleInput{1: {Title: store.Title{Kind: store.KindMovie, TMDBID: 1, Title: "Dune"}}}
	e.addMovie("Movies/Dune (1990).mkv", "Dune", 1990)       // review
	e.addMovie("Movies/Nothing (2000).mkv", "Nothing", 2000) // unmatched
	e.addMovie("Movies/Later (2001).mkv", "Later", 2001)

	if st := e.w.Status(e.ctx); !st.Enabled || !st.KeySet || st.Pending != 3 || st.Running || st.LastRefresh != nil || st.LastError != "" {
		t.Errorf("before: %+v", st)
	}
	e.once()
	st := e.w.Status(e.ctx)
	if st.Pending != 0 || st.Unmatched != 2 || st.NeedsReview != 1 || st.LastRefresh == nil || st.Running {
		t.Errorf("after: %+v", st)
	}
}

func TestWorkerManualMatchUnmatchAndSearch(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.fake.FindMovies = map[string][]metadata.Candidate{"imdb_id:tt0133093": {{TMDBID: 603, Title: "The Matrix"}}}
	id := e.addMovie("Movies/Something Else (1990).mkv", "Something Else", 1990)
	show := e.addEpisode("Breaking Bad", 1, 1)
	e.withBreakingBad()

	// By TMDB id.
	if err := e.w.MatchMovie(e.ctx, id, metadata.Ref{TMDBID: 603}); err != nil {
		t.Fatal(err)
	}
	got := e.entry(id)
	if got.MatchState != store.MatchManual || got.NeedsReview || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("after a manual match: %+v", got.Item)
	}
	if e.fake.Count("Search") != 0 {
		t.Error("a manual match searched")
	}
	// The worker leaves it alone.
	e.once()
	if again := e.entry(id); again.MatchState != store.MatchManual || again.TitleID != got.TitleID {
		t.Errorf("the worker changed a manual match: %+v", again.Item)
	}

	// By IMDb id.
	other := e.addMovie("Movies/Other (1991).mkv", "Other", 1991)
	if err := e.w.MatchMovie(e.ctx, other, metadata.Ref{IMDbID: "tt0133093"}); err != nil || e.entry(other).TitleID != got.TitleID {
		t.Errorf("by imdb id: %v, title %d want %d", err, e.entry(other).TitleID, got.TitleID)
	}
	if err := e.w.MatchMovie(e.ctx, other, metadata.Ref{IMDbID: "tt9999999"}); !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("unknown imdb id: %v", err)
	}
	if err := e.w.MatchMovie(e.ctx, other, metadata.Ref{TMDBID: 424242}); !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("unknown tmdb id: %v", err)
	}
	if err := e.w.MatchMovie(e.ctx, 99999, metadata.Ref{TMDBID: 603}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
	if err := e.w.MatchMovie(e.ctx, show, metadata.Ref{TMDBID: 603}); err == nil {
		t.Error("an episode was matched as a movie")
	}

	// Shows.
	if err := e.w.MatchShow(e.ctx, "Breaking Bad", metadata.Ref{TMDBID: 1396}); err != nil {
		t.Fatal(err)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchManual || sm.TitleID == 0 {
		t.Errorf("series = %+v", sm)
	}
	if err := e.w.MatchShow(e.ctx, "No Such Show", metadata.Ref{TMDBID: 1396}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown show: %v", err)
	}

	// Unmatch: back to the file name, and the title no one uses is gone.
	if err := e.w.UnmatchItem(e.ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.w.UnmatchItem(e.ctx, other); err != nil {
		t.Fatal(err)
	}
	if got := e.entry(id); got.MatchState != store.MatchManual || got.TitleID != 0 {
		t.Errorf("after unmatch: %+v", got.Item)
	}
	if _, err := e.st.GetTitle(e.ctx, got.TitleID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the unused title was not pruned: %v", err)
	}
	if err := e.w.UnmatchShow(e.ctx, "Breaking Bad"); err != nil {
		t.Fatal(err)
	}

	// Search.
	e.fake.MovieSearch["something"] = []metadata.Candidate{{TMDBID: 1, Title: "Something", Year: 1990, Popularity: 1}}
	res, err := e.w.Search(e.ctx, metadata.Query{Kind: store.KindMovie, Title: "Something", Year: 1990})
	if err != nil || len(res) != 1 || res[0].TMDBID != 1 || res[0].Score < 0.9 {
		t.Errorf("Search = %+v, %v", res, err)
	}

	// Without TMDB.
	e.src.Set(nil, true)
	if err := e.w.MatchMovie(e.ctx, id, metadata.Ref{TMDBID: 603}); !errors.Is(err, metadata.ErrLookupsOff) {
		t.Errorf("no key: %v", err)
	}
	if _, err := e.w.Search(e.ctx, metadata.Query{Kind: store.KindMovie, Title: "x"}); !errors.Is(err, metadata.ErrLookupsOff) {
		t.Errorf("search without a key: %v", err)
	}
	if err := e.w.UnmatchItem(e.ctx, id); err != nil {
		t.Errorf("unmatching needs no TMDB: %v", err)
	}
}

// The match is stored when the call returns: a CDN that hangs holds up neither
// the answer nor the pruning, whatever the deadline of the request.
func TestWorkerManualMatchDoesNotWaitForImagesOrFailAfterTheyHang(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.withBreakingBad()
	id := e.addMovie("Movies/Something Else (1990).mkv", "Something Else", 1990)
	old := e.addMovie("Movies/Old (1991).mkv", "Old", 1991)
	e.addEpisode("Breaking Bad", 1, 1)
	// An orphan the pruning after the match must take: a title nobody uses.
	orphan, err := e.st.UpsertTitle(e.ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 9, Title: "Orphan"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = old
	e.imgHang.Store(true)

	// The request's own deadline is short: the match must be done long before it.
	for name, match := range map[string]func(context.Context) error{
		"movie": func(ctx context.Context) error { return e.w.MatchMovie(ctx, id, metadata.Ref{TMDBID: 603}) },
		"show":  func(ctx context.Context) error { return e.w.MatchShow(ctx, "Breaking Bad", metadata.Ref{TMDBID: 1396}) },
	} {
		ctx, cancel := context.WithTimeout(e.ctx, 500*time.Millisecond)
		start := time.Now()
		err := match(ctx)
		took := time.Since(start)
		cancel()
		if err != nil || took > 300*time.Millisecond {
			t.Errorf("%s: MatchX = %v after %v with the image server hanging; want nil at once", name, err, took)
		}
	}
	if got := e.entry(id); got.MatchState != store.MatchManual || got.TitleID == 0 || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("movie after the match: %+v", got.Item)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchManual || sm.TitleID == 0 {
		t.Errorf("show after the match: %+v", sm)
	}
	if _, err := e.st.GetTitle(e.ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the orphan was not pruned after the match: %v", err)
	}
	// The images are being fetched in the background (positive control: the
	// server was asked), and nothing is on disk yet.
	deadline := time.Now().Add(5 * time.Second)
	for e.imgHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.imgHits.Load() == 0 {
		t.Error("the images were never asked for")
	}
	if _, err := os.Stat(filepath.Join(e.imgDir, "tmdb/w342/p603.jpg")); err == nil {
		t.Error("an image arrived from a server that hangs")
	}
	// Close (what Run does on the way out) ends the background work.
	done := make(chan struct{})
	go func() { e.w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the background prefetch")
	}
}

// Released images are fetched, in the background, after the match.
func TestWorkerManualMatchPrefetchesInTheBackground(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	id := e.addMovie("Movies/Something Else (1990).mkv", "Something Else", 1990)
	if err := e.w.MatchMovie(e.ctx, id, metadata.Ref{TMDBID: 603}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"tmdb/w342/p603.jpg", "tmdb/w1280/b603.jpg"} {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(e.imgDir, f)); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := os.Stat(filepath.Join(e.imgDir, f)); err != nil {
			t.Errorf("not prefetched: %s", f)
		}
	}
}

// What a person decided is never matched again on its own: not by a refresh
// someone asks for, and not by a sidecar that appears. Each case has a control
// (the same thing done to an unmatched file does requeue it), so that a test
// that cannot fail is noticed.
func TestWorkerManualIsNeverRequeued(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.withBreakingBad()
	e.fake.MovieSearch["something"] = []metadata.Candidate{{TMDBID: 603, Title: "The Matrix", OriginalTitle: "The Matrix", Year: 1999, Popularity: 80}}

	manual := e.addMovie("Movies/The Matrix (1999)/The Matrix (1999).mkv", "The Matrix", 1999)
	lost := e.addMovie("Movies/Lost (2000)/Lost (2000).mkv", "Lost", 2000)
	manualShow := e.addEpisode("Breaking Bad", 1, 1)
	lostShow := e.addEpisode("Lost Show", 1, 1)
	e.once() // the matrix and breaking bad match; the others are unmatched
	if got := e.entry(lost); got.MatchState != store.MatchUnmatched {
		t.Fatalf("control file: %+v", got.Item)
	}
	if sm := e.series("Lost Show"); sm.State != store.MatchUnmatched {
		t.Fatalf("control show: %+v", sm)
	}
	if err := e.w.UnmatchItem(e.ctx, manual); err != nil {
		t.Fatal(err)
	}
	if err := e.w.UnmatchShow(e.ctx, "Breaking Bad"); err != nil {
		t.Fatal(err)
	}
	if got := e.entry(manual); got.MatchState != store.MatchManual || got.TitleID != 0 {
		t.Fatalf("manual file: %+v", got.Item)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchManual || sm.TitleID != 0 {
		t.Fatalf("manual show: %+v", sm)
	}
	// An NFO that names the title would match a pending file.
	e.write("Movies/The Matrix (1999)/The Matrix (1999).nfo", `<movie><title>The Matrix</title><uniqueid type="tmdb">603</uniqueid></movie>`)
	e.write("Movies/Lost (2000)/Lost (2000).nfo", `<movie><title>The Matrix</title><uniqueid type="tmdb">603</uniqueid></movie>`)
	calls := len(e.fake.Calls())

	// RefreshItem (refreshLinked), RefreshShow (refreshShow) and a sidecar
	// notification (rereadLocal), for the manual ones and the controls.
	e.w.RefreshItem(manual)
	e.w.RefreshItem(manualShow)
	e.w.RefreshShow("Breaking Bad")
	e.w.NotifySidecars([]int64{manual})
	e.w.RefreshItem(lost)
	e.w.RefreshShow("Lost Show")
	e.once()
	if got := e.entry(manual); got.MatchState != store.MatchManual || got.TitleID != 0 {
		t.Errorf("a manual file was matched again: %+v", got.Item)
	}
	if sm := e.series("Breaking Bad"); sm.State != store.MatchManual || sm.TitleID != 0 {
		t.Errorf("a manual show was matched again: %+v", sm)
	}
	for _, c := range e.fake.Calls()[calls:] {
		if strings.Contains(c, "Breaking") || strings.Contains(c, "1396") {
			t.Errorf("TMDB was asked about the manual show: %q", c)
		}
	}
	// Controls: an unmatched file and show go back to the queue (and are tried).
	if got := e.entry(lost); got.MatchState == store.MatchUnmatched && e.fake.Count("Search") == 0 {
		t.Errorf("control: RefreshItem did not requeue an unmatched file: %+v", got.Item)
	}
	if sm := e.series("Lost Show"); sm.State == store.MatchUnmatched && e.fake.Count("SearchSeries") < 2 {
		t.Errorf("control: RefreshShow did not requeue an unmatched show: %+v", sm)
	}
	_ = lostShow
}

// A file nothing was found for is matched through an NFO that appears next to
// it, unless a person decided about it.
func TestWorkerSidecarRequeuesUnmatchedButNotManual(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	unmatched := e.addMovie("Movies/Unknown A (2001)/Unknown A (2001).mkv", "Unknown A", 2001)
	manual := e.addMovie("Movies/Unknown B (2002)/Unknown B (2002).mkv", "Unknown B", 2002)
	e.once()
	if err := e.w.UnmatchItem(e.ctx, manual); err != nil {
		t.Fatal(err)
	}
	if got := e.entry(unmatched); got.MatchState != store.MatchUnmatched {
		t.Fatalf("control file: %+v", got.Item)
	}
	for _, d := range []string{"Unknown A (2001)", "Unknown B (2002)"} {
		e.write("Movies/"+d+"/"+d+".nfo", `<movie><uniqueid type="tmdb">603</uniqueid></movie>`)
	}
	e.w.NotifySidecars([]int64{unmatched, manual})
	e.once()
	if got := e.entry(unmatched); got.MatchState != store.MatchMatched || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("the NFO did not match an unmatched file: %+v", got.Item)
	}
	if got := e.entry(manual); got.MatchState != store.MatchManual || got.TitleID != 0 {
		t.Errorf("the NFO matched a file a person had decided about: %+v", got.Item)
	}
}

// A file replaced under the same item is probed again: the tags and the cover
// index of the old one are not those of the new one.
func TestWorkerProbesAReplacedFileAgain(t *testing.T) {
	probes := &atomic.Int32{}
	e := newWEnv(t, func(o *metadata.WorkerOptions) {
		o.Probe = func(ctx context.Context, path string) (probe.Result, error) {
			if probes.Add(1) == 1 {
				return probe.Result{Tags: probe.Tags{Title: "Zzz Nothing Like It"}}, nil
			}
			return probe.Result{Tags: probe.Tags{Title: "The Matrix", Year: 1999}}, nil
		}
	})
	e.withMatrix()
	id := e.addMovie("Movies/rip_0042.mkv", "rip 0042", 0)
	e.once()
	if got := e.entry(id); got.MatchState != store.MatchUnmatched || probes.Load() != 1 {
		t.Fatalf("before the file is replaced: %+v, %d probes", got.Item, probes.Load())
	}
	// Nothing changed: asking again does not probe again.
	e.w.RefreshItem(id)
	e.once()
	if probes.Load() != 1 {
		t.Errorf("the same file was probed %d times", probes.Load())
	}

	// A new file under the same path: same item, other size and time.
	it := e.entry(id).Item
	it.Size, it.MTime = it.Size+10, it.MTime+1
	if again, err := e.st.UpsertItem(e.ctx, &it, nil); err != nil || again != id {
		t.Fatalf("re-indexing: %d, %v", again, err)
	}
	e.w.RefreshItem(id)
	e.once()
	if got := e.entry(id); got.MatchState != store.MatchMatched || e.title(got.TitleID).TMDBID != 603 || probes.Load() != 2 {
		t.Errorf("after the file was replaced: %+v, %d probes (want matched through the new tags, 2 probes)", got.Item, probes.Load())
	}
}

// TMDB not having a person, or not answering, is not asked again at once: on
// every visit to the page would be a request each.
func TestWorkerEnsurePersonRemembersFailures(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	m := matrix()
	m.Credits = append(m.Credits,
		store.Credit{Person: store.Person{TMDBID: 7777, Name: "Carrie-Anne Moss"}, Kind: store.CreditCast, Character: "Trinity", Order: 1},
		store.Credit{Person: store.Person{TMDBID: 8888, Name: "Laurence Fishburne"}, Kind: store.CreditCast, Character: "Morpheus", Order: 2})
	e.fake.Movies[603] = m
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	personID := func(name string) int64 {
		t.Helper()
		people, err := e.st.SearchPeople(e.ctx, name, 5)
		if err != nil || len(people) != 1 {
			t.Fatalf("SearchPeople(%q) = %+v, %v", name, people, err)
		}
		return people[0].ID
	}
	keanu, carrie := personID("Keanu"), personID("Carrie")
	asked := func() int { return e.fake.Count("Person(") }

	// Not found: once a day.
	for i := 0; i < 3; i++ {
		if err := e.w.EnsurePerson(e.ctx, keanu); !metadata.IsNotFound(err) {
			t.Fatalf("EnsurePerson %d = %v, want TMDB's 404", i+1, err)
		}
	}
	if asked() != 1 {
		t.Errorf("a person TMDB does not have was asked for %d times in a row, want 1", asked())
	}
	e.clock.Advance(23 * time.Hour)
	e.w.EnsurePerson(e.ctx, keanu)
	if asked() != 1 {
		t.Errorf("asked again after 23 hours (%d)", asked())
	}
	e.clock.Advance(2 * time.Hour)
	e.w.EnsurePerson(e.ctx, keanu)
	if asked() != 2 {
		t.Errorf("not asked again after a day (%d)", asked())
	}

	// Out of reach: five minutes, then longer each time.
	e.fake.Offline()
	base := asked()
	step := func(after time.Duration, wantAsked int, what string) {
		t.Helper()
		e.clock.Advance(after)
		err := e.w.EnsurePerson(e.ctx, carrie)
		if err == nil || !metadata.IsTransient(err) {
			t.Fatalf("%s: err = %v, want the transient error", what, err)
		}
		if got := asked() - base; got != wantAsked {
			t.Fatalf("%s: %d requests so far, want %d", what, got, wantAsked)
		}
	}
	step(0, 1, "first failure")
	step(0, 1, "at once again")
	step(4*time.Minute, 1, "4 minutes later")
	step(2*time.Minute, 2, "6 minutes later")
	step(6*time.Minute, 2, "6 minutes after the second failure (backoff 10 minutes)")
	step(5*time.Minute, 3, "11 minutes after the second failure")

	// A key change forgets the failures; success is kept for good.
	e.fake.Online()
	e.fake.People = map[int64]*store.Person{7777: {TMDBID: 7777, Name: "Carrie-Anne Moss", Biography: "Trinity."}}
	e.w.Reload()
	if err := e.w.EnsurePerson(e.ctx, carrie); err != nil {
		t.Fatalf("after Reload: %v", err)
	}
	if d, err := e.st.GetPerson(e.ctx, carrie); err != nil || d.Biography != "Trinity." || d.FetchedAt == 0 {
		t.Errorf("person after the fetch: %+v, %v", d.Person, err)
	}
	n := asked()
	e.w.EnsurePerson(e.ctx, carrie)
	if asked() != n {
		t.Error("a person already fetched was asked for again")
	}

	// A caller that went away says nothing about the person.
	morpheus := personID("Laurence")
	ctx, cancel := context.WithCancel(e.ctx)
	e.fake.Hook = func(ctx context.Context, call string) error { cancel(); return ctx.Err() } // the client leaves mid-request
	n = asked()
	if err := e.w.EnsurePerson(ctx, morpheus); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	e.fake.Hook = nil
	e.w.EnsurePerson(e.ctx, morpheus)
	if asked() != n+2 {
		t.Errorf("a cancelled request was remembered as a failure of the person (%d requests, want %d)", asked()-n, 2)
	}
}

func TestWorkerRefreshAllRequeuesUnmatchedAndRefreshesTitles(t *testing.T) {
	e := newWEnv(t)
	ids := refreshFixture(t, e, 24*time.Hour)
	lost := e.addMovie("Movies/Lost (2000).mkv", "Lost", 2000)
	e.once()
	if got := e.entry(lost); got.MatchState != store.MatchUnmatched {
		t.Fatalf("lost: %+v", got.Item)
	}
	e.fake.MovieSearch["lost"] = []metadata.Candidate{{TMDBID: 55, Title: "Lost", Year: 2000, Popularity: 1}}
	e.fake.Movies[55] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 55, Title: "Lost", Year: 2000}}
	e.fake.Movies[1] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 1, Title: "fresh", Year: 2000, ReleaseDate: "2000-01-01", Overview: "v3"}}

	e.w.RefreshAll()
	e.once()
	if got := e.entry(lost); got.MatchState != store.MatchMatched {
		t.Errorf("unmatched file after RefreshAll: %+v", got.Item)
	}
	if overview(t, e, ids["fresh"]) != "v3" {
		t.Errorf("a fresh title was not refreshed by RefreshAll: %q", overview(t, e, ids["fresh"]))
	}
}

func TestWorkerUsesEmbeddedTagsAndCovers(t *testing.T) {
	probes := &atomic.Int32{}
	e := newWEnv(t, func(o *metadata.WorkerOptions) {
		o.Probe = func(ctx context.Context, path string) (probe.Result, error) {
			probes.Add(1)
			return probe.Result{Tags: probe.Tags{Title: "The Matrix", Year: 1999, Description: "Embedded plot."}, Cover: 3, HasCover: true}, nil
		}
	})
	e.withMatrix()

	// The file name is no help; the tag names the movie.
	id := e.addMovie("Movies/rip_0042.mkv", "rip 0042", 0)
	e.once()
	got := e.entry(id)
	if got.MatchState != store.MatchMatched || e.title(got.TitleID).TMDBID != 603 {
		t.Errorf("tag-assisted match: %+v", got.Item)
	}
	if probes.Load() != 1 {
		t.Errorf("%d probes, want 1", probes.Load())
	}

	// A movie TMDB has no poster for takes the cover inside the file.
	e.fake.MovieSearch["no poster"] = []metadata.Candidate{{TMDBID: 8, Title: "No Poster", Year: 2000, Popularity: 1}}
	e.fake.Movies[8] = &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 8, Title: "No Poster", Year: 2000}}
	np := e.addMovie("Movies/No Poster (2000).mkv", "No Poster", 2000)
	e.once()
	if p := e.entry(np).LocalPoster; p != "embedded:3" {
		t.Errorf("poster = %q, want the embedded cover", p)
	}
	// One with a poster at TMDB is not probed for one.
	if e.entry(id).LocalPoster != "" {
		t.Errorf("a title with a TMDB poster got an embedded cover: %q", e.entry(id).LocalPoster)
	}

	// Offline source: no key, no NFO, but tags.
	e.src.Set(nil, true)
	off := e.addMovie("Movies/offline_file.mkv", "offline file", 0)
	e.once()
	got = e.entry(off)
	if got.MatchState != store.MatchPending || got.TitleID == 0 {
		t.Fatalf("offline: %+v", got.Item)
	}
	if title := e.title(got.TitleID); title.Title != "The Matrix" || title.Overview != "Embedded plot." || title.Source != store.SourceNFO || title.TMDBID != 0 {
		t.Errorf("tag title = %+v", title)
	}
}

func TestWorkerRunStopsOnCancelAndLeavesNoGoroutines(t *testing.T) {
	base := settle(t, 0)
	func() {
		e := newWEnv(t, func(o *metadata.WorkerOptions) { o.Tick = 5 * time.Millisecond })
		e.withMatrix()
		id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { e.w.Run(ctx); close(done) }()

		// It matches, and keeps ticking.
		deadline := time.Now().Add(5 * time.Second)
		for e.entry(id).MatchState != store.MatchMatched && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if e.entry(id).MatchState != store.MatchMatched {
			t.Fatal("Run did not match the file")
		}
		e.w.Notify()
		e.w.NotifySidecars([]int64{id})
		e.w.Reload()
		e.w.RefreshAll()
		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after the context was cancelled")
		}
		e.close()
	}()
	settle(t, base)
}

// settle waits for the goroutines of finished work to exit and returns the
// count; with base > 0 it fails when the count does not come back to base.
func settle(t *testing.T, base int) int {
	t.Helper()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	var n int
	for i := 0; i < 100; i++ {
		runtime.GC()
		n = runtime.NumGoroutine()
		if base > 0 && n <= base {
			return n
		}
		if base == 0 && i > 2 {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	if base > 0 {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Errorf("%d goroutines after shutdown, started with %d:\n%s", n, base, buf)
	}
	return n
}

func TestWorkerRunReturnsPromptlyWhileTMDBIsHanging(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	entered := make(chan struct{}, 1)
	e.fake.Hook = func(ctx context.Context, call string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done() // a TMDB that never answers
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop while a request was hanging")
	}
	// A cancelled request is not a failure of the file.
	if got := e.entry(1); got.MatchState != store.MatchPending {
		t.Errorf("item = %+v", got.Item)
	}
	if attempts, _ := attemptsOf(t, e, 1); attempts != 0 {
		t.Errorf("attempts = %d after a cancel; shutting down must not count as a failure", attempts)
	}
}

// A person fixes a match while the worker is waiting for TMDB: the worker's
// late result must not overwrite the fix (and leaves no title behind).
func TestWorkerLateResultNeverOverwritesAPersonsFix(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	mine, err := e.st.UpsertTitle(e.ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 7, Title: "Chosen by hand"}})
	if err != nil {
		t.Fatal(err)
	}
	id := e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	fixed := false
	e.fake.Hook = func(ctx context.Context, call string) error {
		if call == "Movie(603)" && !fixed {
			fixed = true // the user acts while TMDB is being asked
			return e.st.SetItemMatch(ctx, id, store.Match{TitleID: mine, State: store.MatchManual, Score: 1})
		}
		return nil
	}
	e.once()
	if !fixed {
		t.Fatal("the hook never ran: the test did not race anything")
	}
	got := e.entry(id)
	if got.MatchState != store.MatchManual || got.TitleID != mine {
		t.Errorf("the worker overwrote the fix: %+v", got.Item)
	}
	if n := e.count("titles"); n != 1 {
		t.Errorf("titles = %d; the dropped result must leave none behind", n)
	}
	if e.imgHits.Load() != 0 {
		t.Errorf("images were fetched for a dropped result")
	}

	// The same for a show.
	e.withBreakingBad()
	e.addEpisode("Breaking Bad", 1, 1)
	fixedShow := false
	other, _ := e.st.UpsertTitle(e.ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 9, Title: "Chosen show"}})
	e.fake.Hook = func(ctx context.Context, call string) error {
		if strings.HasPrefix(call, "Series(1396") && !fixedShow {
			fixedShow = true
			return e.st.SetSeriesMatch(ctx, "Breaking Bad", store.Match{TitleID: other, State: store.MatchManual, Score: 1})
		}
		return nil
	}
	e.once()
	if sm := e.series("Breaking Bad"); !fixedShow || sm.State != store.MatchManual || sm.TitleID != other {
		t.Errorf("series = %+v (raced: %v)", sm, fixedShow)
	}
}

// An NFO's actor thumbnail (a URL or a path) is never fetched or opened.
func TestWorkerNeverFetchesAnNFOActorThumb(t *testing.T) {
	var hits atomic.Int32
	thumbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(jpeg) }))
	t.Cleanup(thumbSrv.Close)
	e := newWEnv(t)
	e.src.Set(nil, true) // an NFO-only title: the actors come from the NFO alone
	id := e.addMovie("Movies/Home/Home.mkv", "Home", 2011)
	e.write("Movies/Home/Home.nfo", `<movie><title>Home</title>
		<actor><name>Uncle Bob</name><thumb>`+thumbSrv.URL+`/bob.jpg</thumb></actor>
		<actor><name>Aunt May</name><thumb>/etc/hosts</thumb></actor></movie>`)
	e.once()
	got := e.entry(id)
	d, err := e.st.GetTitleDetail(e.ctx, got.TitleID)
	if err != nil || len(d.Cast) != 2 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	for _, c := range d.Cast {
		if c.Profile != "" {
			t.Errorf("%s has profile %q; an NFO thumb must not become a photo", c.Name, c.Profile)
		}
	}
	// Even with TMDB on and a match, nothing is fetched from the NFO.
	e.src.Set(e.fake, true)
	e.fake.MovieSearch = map[string][]metadata.Candidate{"home": {{TMDBID: 5, Title: "Home", Year: 2011, Popularity: 1}}}
	e.fake.Movies = map[int64]*store.TitleInput{5: {Title: store.Title{Kind: store.KindMovie, TMDBID: 5, Title: "Home", Year: 2011}}}
	e.w.Reload()
	e.once()
	if hits.Load() != 0 {
		t.Errorf("the NFO's thumb URL was fetched %d times", hits.Load())
	}
}
