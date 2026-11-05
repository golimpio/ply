package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/store"
)

// ratingsEnv is the ratings service over a real store, fake IMDb dataset servers (ratings, and the
// episodes of shows) and a fake MDBList.
type ratingsEnv struct {
	t        *testing.T
	st       *store.Store
	dbPath   string
	settings *Settings
	rt       *Ratings
	dir      string
	imdb     *datasetServer // title.ratings.tsv.gz
	episodes *datasetServer // title.episode.tsv.gz
	mdb      *mdbServer
	mdbReply atomic.Pointer[func(w http.ResponseWriter, r *http.Request, n int)]
	paces    atomic.Int32
	onPace   atomic.Pointer[func()]
	movies   atomic.Int32 // movies added, for their file names

	mu  sync.Mutex
	now time.Time
}

func (e *ratingsEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *ratingsEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// datasetBody is a dataset with the given rows ("tt1" -> "8.7 2000").
func datasetBody(t testing.TB, rows map[string]string) []byte {
	var sb strings.Builder
	sb.WriteString(imdbHead)
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		v, n, _ := strings.Cut(rows[id], " ")
		fmt.Fprintf(&sb, "%s\t%s\t%s\n", id, v, n)
	}
	return gzipOf(t, sb.String())
}

// episodesBody is an episode dataset with the given rows ("tt3 tt2 1 4": episode tt3 of show tt2 is S1E4).
func episodesBody(t testing.TB, rows ...string) []byte {
	var sb strings.Builder
	sb.WriteString(episodesHead)
	for _, r := range rows {
		sb.WriteString(strings.ReplaceAll(r, " ", "\t") + "\n")
	}
	return gzipOf(t, sb.String())
}

func newRatingsEnv(t *testing.T, mod ...func(*SettingsOptions, *RatingsOptions)) *ratingsEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "My Data", "ply.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &ratingsEnv{t: t, st: st, dbPath: dbPath, dir: filepath.Join(t.TempDir(), "cache", "imdb"), now: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}
	e.imdb = newDatasetServer(t, datasetBody(t, map[string]string{}))
	e.episodes = newDatasetServer(t, episodesBody(t, "tt0000090 tt0000091 1 1")) // a show nobody has
	e.mdb = newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if f := e.mdbReply.Load(); f != nil {
			(*f)(w, r, n)
			return
		}
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})
	so := SettingsOptions{Store: st, MDBListKeyFlag: mdbTestKey, MDBListBaseURL: e.mdb.URL, Logger: slog.New(slog.DiscardHandler)}
	ro := RatingsOptions{Store: st, Dir: e.dir, IMDbURL: e.imdb.URL + "/title.ratings.tsv.gz", IMDbEpisodesURL: e.episodes.URL + "/title.episode.tsv.gz",
		HTTPClient: e.imdb.Client(), Logger: slog.New(slog.DiscardHandler), Now: e.clock,
		Pace: func(ctx context.Context) error {
			e.paces.Add(1)
			if f := e.onPace.Load(); f != nil {
				(*f)()
			}
			return ctx.Err()
		}}
	for _, m := range mod {
		m(&so, &ro)
	}
	e.settings = NewSettings(so)
	ro.Settings = e.settings
	e.rt = NewRatings(ro)
	return e
}

func (e *ratingsEnv) reply(f func(w http.ResponseWriter, r *http.Request, n int)) {
	e.mdbReply.Store(&f)
}

func (e *ratingsEnv) setDataset(rows map[string]string) {
	b := datasetBody(e.t, rows)
	e.imdb.body.Store(&b)
}

// setEpisodes sets the rows of the episode dataset (see episodesBody).
func (e *ratingsEnv) setEpisodes(rows ...string) {
	b := episodesBody(e.t, rows...)
	e.episodes.body.Store(&b)
}

// newVersion makes a server answer as if its file had changed.
func newVersion(d *datasetServer, tag string) {
	d.etag, d.modTime = `"`+tag+`"`, "Thu, 05 Nov 2026 03:00:00 GMT"
}

// file reads one of the kept datasets ("" when it is not there).
func (e *ratingsEnv) file(name string) []byte {
	b, _ := os.ReadFile(filepath.Join(e.dir, name))
	return b
}

// movie adds a movie with a file, matched to a title with these ids.
func (e *ratingsEnv) movie(tmdb int64, imdb string) int64 {
	e.t.Helper()
	ctx := context.Background()
	n := e.movies.Add(1)
	title := fmt.Sprintf("Movie %d", n)
	itemID, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: title, Path: fmt.Sprintf("/media/%d.mkv", n), Root: "/media",
		Size: 1, MTime: 1, Container: "mkv"}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: tmdb, IMDbID: imdb, Title: title,
		ReleaseDate: "1999-03-30", Rating: 8, VoteCount: 10, Source: store.SourceTMDB}})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.SetItemMatch(ctx, itemID, store.Match{TitleID: id, State: store.MatchMatched, Score: 1}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// show adds a show whose episodes all have files, and returns its title id.
func (e *ratingsEnv) show(name string, in store.Title, eps ...store.Episode) int64 {
	e.t.Helper()
	ctx := context.Background()
	for _, ep := range eps {
		if _, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Title: ep.Name, Show: name, Season: ep.Season, Episode: ep.Number,
			Path: fmt.Sprintf("/media/%s/S%02dE%02d.mkv", name, ep.Season, ep.Number), Root: "/media", Size: 1, MTime: 1, Container: "mkv"}, nil); err != nil {
			e.t.Fatal(err)
		}
	}
	in.Kind, in.Title = store.KindSeries, name
	id, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: in, Seasons: []store.Season{{Number: 1, Episodes: eps}}, ResetSeasons: in.Source == store.SourceTVDB})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.SetSeriesMatch(ctx, name, store.Match{TitleID: id, State: store.MatchMatched, Score: 1}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// episodesOf lists a show's episodes as number=id@source (a blank id as "-").
func (e *ratingsEnv) episodesOf(titleID int64) []string {
	e.t.Helper()
	eps, err := e.st.Episodes(context.Background(), titleID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, ep := range eps {
		id := ep.IMDbID
		if id == "" {
			id = "-"
		}
		out = append(out, fmt.Sprintf("%d=%s@%s", ep.Number, id, ep.IMDbSource))
	}
	return out
}

func (e *ratingsEnv) ratings(titleID int64) []store.Rating {
	e.t.Helper()
	cards, err := e.st.TitleCards(context.Background(), []int64{titleID})
	if err != nil {
		e.t.Fatal(err)
	}
	return cards[titleID].Ratings
}

func (e *ratingsEnv) rating(titleID int64, source string) (store.Rating, bool) {
	for _, r := range e.ratings(titleID) {
		if r.Source == source {
			return r, true
		}
	}
	return store.Rating{}, false
}

// countRows counts a table's rows through a reader of its own on the same file.
func (e *ratingsEnv) countRows(table string) int {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *ratingsEnv) setting(key string) string {
	v, _, _ := e.st.GetSetting(context.Background(), key)
	return v
}

// ---- the IMDb job ----

// The job keeps the ratings of the library's titles and episodes, and only those. The episodes' ids
// come from the episode dataset, in the same run, so that the episodes are rated at once.
func TestRunIMDbKeepsOnlyTheLibrary(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	s := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "One"}, store.Episode{Season: 1, Number: 2, Name: "Two"})
	ghost, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 3, IMDbID: "tt4000004", Title: "Ghost", Source: store.SourceTMDB}})
	if err != nil {
		t.Fatal(err)
	}
	e.setEpisodes("tt3000003 tt2000002 1 1", "tt3000004 tt2000002 1 2", "tt3000005 tt6000006 1 1") // the last is a show nobody has
	e.setDataset(map[string]string{
		"tt1000001": "8.7 2000000", "tt2000002": "9.5 1900000", "tt3000003": "9.0 40000",
		"tt4000004": "6.1 100", // the ghost's: in the dataset, not in the library
		"tt5000005": "7.7 300", // nobody's
	})

	if !e.rt.IMDbDue(ctx) {
		t.Fatal("IMDbDue = false with a library that has IMDb ids and no dataset")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got, eps := e.imdb.hits.Load(), e.episodes.hits.Load(); got != 1 || eps != 1 {
		t.Errorf("%d requests for the ratings and %d for the episodes, want 1 and 1", got, eps)
	}
	if r, ok := e.rating(a, store.RatingIMDb); !ok || r.Value != 8.7 || r.Votes != 2000000 || r.Scale != 10 || r.URL != "https://www.imdb.com/title/tt1000001/" {
		t.Errorf("the movie's rating = %+v, %v", r, ok)
	}
	if r, ok := e.rating(s, store.RatingIMDb); !ok || r.Value != 9.5 || r.URL != "https://www.imdb.com/title/tt2000002/" {
		t.Errorf("the show's rating = %+v, %v", r, ok)
	}
	if got, want := e.episodesOf(s), []string{"1=tt3000003@dataset", "2=tt3000004@dataset"}; !slices.Equal(got, want) {
		t.Errorf("episodes = %v, want %v", got, want)
	}
	eps, _ := e.st.Episodes(ctx, s)
	if r := eps[0].IMDbRating; r == nil || r.Value != 9.0 || r.Votes != 40000 {
		t.Errorf("episode 1 = %+v", r)
	}
	if eps[1].IMDbRating != nil {
		t.Errorf("episode 2 has a rating: the ratings dataset has none for its id")
	}
	// The ghost has an id the dataset knows (positive control), and gets nothing.
	if got, err := e.st.GetTitle(ctx, ghost); err != nil || got.IMDbID != "tt4000004" {
		t.Fatalf("control: the ghost = %+v, %v", got, err)
	}
	if _, ok := e.rating(ghost, store.RatingIMDb); ok {
		t.Error("a title with no file got a rating")
	}
	if n := e.countRows("title_ratings"); n != 2 {
		t.Errorf("title_ratings has %d rows, want the movie's and the show's", n)
	}
	if n := e.countRows("episode_ratings"); n != 1 {
		t.Errorf("episode_ratings has %d rows, want episode 1's; the library has no other id", n)
	}

	// The copies are kept for the next time, and the date of the ratings' is the dataset's.
	for _, name := range []string{IMDbRatingsFile, IMDbEpisodesFile} {
		if _, err := os.Stat(filepath.Join(e.dir, name)); err != nil {
			t.Errorf("%s is not kept: %v", name, err)
		}
	}
	at, ok := e.rt.IMDbDatasetUpdatedAt(ctx)
	if !ok || !at.Equal(time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("dataset date = %v, %v; want the Last-Modified of the file", at, ok)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("IMDbDue is still true after the run")
	}
}

// The ratings are written in chunks of 500, and the job yields between them.
func TestRunIMDbYieldsBetweenChunks(t *testing.T) {
	e := newRatingsEnv(t)
	rows := map[string]string{}
	for i := range 520 {
		id := fmt.Sprintf("tt%07d", 100+i)
		e.movie(int64(1000+i), id)
		rows[id] = "7.5 100"
	}
	e.setDataset(rows)

	var atFirstYield atomic.Int32
	atFirstYield.Store(-1)
	check := func() {
		if atFirstYield.Load() < 0 {
			atFirstYield.Store(int32(e.countRows("title_ratings")))
		}
	}
	e.onPace.Store(&check)
	if err := e.rt.RunIMDb(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.countRows("title_ratings"); got != 520 {
		t.Fatalf("%d ratings stored, want 520", got)
	}
	if got := e.paces.Load(); got < 2 {
		t.Errorf("the job yielded %d times for two chunks", got)
	}
	if got := atFirstYield.Load(); got != 500 {
		t.Errorf("at the first yield %d ratings were stored, want the 500 of the first chunk (a job that writes all and then yields yields to no one)", got)
	}
}

// A 304 means the file is as it was: nothing is downloaded, nothing is rewritten, in the file or
// in the database. A change of library, or a new dataset, is written. A library of movies never asks
// for the episodes.
func TestRunIMDbConditionalRequestAndNoRewrite(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.dir, IMDbRatingsFile)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := e.rating(a, store.RatingIMDb)
	stamp := e.setting(setRatings.key("stamp"))
	if stamp == "" {
		t.Fatal("control: no stamp after a write")
	}

	// A week later: asked again, with what it said, and told it has not changed.
	e.advance(8 * 24 * time.Hour)
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("IMDbDue = false a week on")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.imdb.hits.Load(); got != 2 {
		t.Fatalf("%d requests, want 2", got)
	}
	if h := e.imdb.seen.Load(); h.Get("If-None-Match") != e.imdb.etag || h.Get("If-Modified-Since") != e.imdb.modTime {
		t.Errorf("the second request carried %v", *h)
	}
	if fi, _ := os.Stat(path); fi.ModTime().Sub(old).Abs() > time.Second {
		t.Errorf("a 304 rewrote the file (mtime %v)", fi.ModTime())
	}
	if after, _ := e.rating(a, store.RatingIMDb); after != before {
		t.Errorf("a 304 rewrote the rating: %+v, was %+v", after, before)
	}
	if got := e.setting(setRatings.key("stamp")); got != stamp {
		t.Errorf("a 304 wrote again (stamp %s, was %s)", got, stamp)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("a 304 leaves the dataset due: the week starts again")
	}

	// A title that came since is given its rating from the copy we have, with no request.
	b := e.movie(2, "tt1000002")
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("a new title with an IMDb id does not make the job due")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.imdb.hits.Load(); got != 2 {
		t.Errorf("%d requests; a new title needs the copy, not the network", got)
	}
	if r, ok := e.rating(b, store.RatingIMDb); !ok || r.Value != 6.0 {
		t.Errorf("the new title's rating = %+v, %v", r, ok)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("still due")
	}

	// A new dataset: downloaded and written.
	e.setDataset(map[string]string{"tt1000001": "9.1 3000", "tt1000002": "6.0 20"})
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.rating(a, store.RatingIMDb); r.Value != 9.1 || r.Votes != 3000 {
		t.Errorf("the new dataset was not applied: %+v", r)
	}
	if got := e.imdb.hits.Load(); got != 3 {
		t.Errorf("%d requests, want 3", got)
	}
	at, _ := e.rt.IMDbDatasetUpdatedAt(ctx)
	if !at.Equal(time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("dataset date = %v", at)
	}
	if got := e.episodes.hits.Load(); got != 0 {
		t.Errorf("%d requests for the episodes of a library of movies", got)
	}
}

// Every write has a stamp later than the last, though the clock has not moved (two in a second, or a
// clock set back): what the stamp does not reach is pruned, so a title that lost its id loses its rating.
func TestRunIMDbPrunesWithAClockThatDoesNotMove(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	b := e.movie(2, "tt1000002")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(b, store.RatingIMDb); !ok {
		t.Fatal("control: b has no rating")
	}
	first := e.setting(setRatings.key("stamp"))

	// b loses its id; the clock stays where it is.
	if _, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{ID: b, Kind: store.KindMovie, TMDBID: 2, Title: "Movie 2", Source: store.SourceTMDB}}); err != nil {
		t.Fatal(err)
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Error("a lost its rating")
	}
	if r, ok := e.rating(b, store.RatingIMDb); ok {
		t.Errorf("b has no IMDb id and still has %+v", r)
	}
	if second := e.setting(setRatings.key("stamp")); second <= first {
		t.Errorf("stamp %s after %s: it must grow with each write", second, first)
	}
}

// A rating of an id the title no longer has, or the dataset no longer lists, goes.
func TestRunIMDbPrunesWhatNoLongerHolds(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	b := e.movie(2, "tt1000002")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20", "tt1000003": "5.0 5"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Fatal("control: a has no rating")
	}

	// a is matched to another title (another id); b's id is not in the dataset any more.
	if _, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{ID: a, Kind: store.KindMovie, TMDBID: 1, IMDbID: "tt1000003", Title: "Movie 1", Source: store.SourceTMDB}}); err != nil {
		t.Fatal(err)
	}
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000003": "5.0 5"})
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if r, ok := e.rating(a, store.RatingIMDb); !ok || r.Value != 5.0 {
		t.Errorf("a = %+v, %v; it has another id now, and its rating", r, ok)
	}
	if r, ok := e.rating(b, store.RatingIMDb); ok {
		t.Errorf("b still has %+v; its id is not in the dataset", r)
	}
	if n := e.countRows("title_ratings"); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
}

// With the privacy switch off nothing is fetched, whichever way it was turned off; and a library
// without IMDb ids needs nothing from IMDb.
func TestRunIMDbPrivacyAndNothingToDo(t *testing.T) {
	forced := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.Disabled = true })
	stored := newRatingsEnv(t)
	if err := stored.st.SetSetting(t.Context(), SettingEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	on := newRatingsEnv(t)
	for name, e := range map[string]*ratingsEnv{"-metadata=false": forced, "the setting": stored, "control: on": on} {
		e.movie(1, "tt1000001")
		e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB}, store.Episode{Season: 1, Number: 1, Name: "One"})
		e.setDataset(map[string]string{"tt1000001": "8.7 2000"})
		due := e.rt.IMDbDue(t.Context())
		err := e.rt.RunIMDb(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		off := e != on
		requests := e.imdb.hits.Load() + e.episodes.hits.Load()
		if off && (due || requests != 0 || e.countRows("title_ratings") != 0) {
			t.Errorf("%s: due %v, %d requests, %d ratings; nothing may be fetched", name, due, requests, e.countRows("title_ratings"))
		}
		if !off && (!due || requests != 2 || e.countRows("title_ratings") != 1) {
			t.Errorf("%s: due %v, %d requests, %d ratings; the control must fetch both datasets", name, due, requests, e.countRows("title_ratings"))
		}
		for _, file := range []string{IMDbRatingsFile, IMDbEpisodesFile} {
			if _, err := os.Stat(filepath.Join(e.dir, file)); (err == nil) == off {
				t.Errorf("%s: %s present = %v", name, file, err == nil)
			}
		}
	}

	bare := newRatingsEnv(t)
	bare.movie(1, "") // no IMDb id
	if bare.rt.IMDbDue(t.Context()) {
		t.Error("due with no IMDb id in the library")
	}
	if err := bare.rt.RunIMDb(t.Context()); err != nil || bare.imdb.hits.Load() != 0 {
		t.Errorf("a library with no IMDb ids: %v, %d requests", err, bare.imdb.hits.Load())
	}
}

// When IMDb cannot be reached the copy we have is used; with none, the job fails and tries later.
func TestRunIMDbOutOfReach(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})

	e.imdb.status.Store(503)
	if err := e.rt.RunIMDb(ctx); err == nil {
		t.Fatal("no error with no copy and no network")
	}
	if e.countRows("title_ratings") != 0 {
		t.Error("ratings were stored from nothing")
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("due again at once after a failure: the server is left alone for a while")
	}
	e.advance(31 * time.Minute)
	if !e.rt.IMDbDue(ctx) {
		t.Error("not due after the wait")
	}
	e.imdb.status.Store(0)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Fatal("control: no rating")
	}

	b := e.movie(2, "tt1000002")
	e.advance(8 * 24 * time.Hour)
	e.imdb.status.Store(503)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Errorf("out of reach with a copy: %v", err)
	}
	if r, ok := e.rating(b, store.RatingIMDb); !ok || r.Value != 6.0 {
		t.Errorf("the new title's rating from the copy = %+v, %v", r, ok)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("due again at once after a failure")
	}
	e.advance(31 * time.Minute)
	if !e.rt.IMDbDue(ctx) {
		t.Error("the week was started again by a failure: the server must be asked after the wait")
	}
}

// After each failure to reach IMDb the wait doubles, from 30 minutes up to a day; a success starts it
// again. A server that is down is not asked every look.
func TestIMDbOutageBackoff(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000"})
	e.imdb.status.Store(503)

	waits := []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, wait := range waits {
		if !e.rt.IMDbDue(ctx) {
			t.Fatalf("failure %d: not due", i+1)
		}
		hits := e.imdb.hits.Load()
		if err := e.rt.RunIMDb(ctx); err == nil || e.imdb.hits.Load() != hits+1 {
			t.Fatalf("failure %d: err %v, %d requests", i+1, err, e.imdb.hits.Load()-hits)
		}
		e.advance(wait - time.Minute)
		if e.rt.IMDbDue(ctx) {
			t.Errorf("failure %d: due %v after the failure, want to wait %v", i+1, wait-time.Minute, wait)
		}
		e.advance(2 * time.Minute)
	}

	// It comes back: the ratings are written, and the next outage starts at 30 minutes again.
	e.imdb.status.Store(0)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Fatal("control: no rating after the outage")
	}
	if e.setting(setRatings.key("fails")) != "" || e.setting(setRatings.key("retry")) != "" {
		t.Errorf("the backoff survived a success: fails %q, retry %q", e.setting(setRatings.key("fails")), e.setting(setRatings.key("retry")))
	}
	e.advance(8 * 24 * time.Hour)
	e.imdb.status.Store(503)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	e.advance(31 * time.Minute)
	if !e.rt.IMDbDue(ctx) {
		t.Error("after a success the wait starts from 30 minutes again")
	}
}

// A server that gives something that is not the dataset is not believed, and not asked again for a
// week; the copy we have stays.
func TestRunIMDbNotTheDataset(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")

	// With no copy: an error the job does not retry, nothing stored, nothing kept.
	bad := gzipOf(t, "id\trating\n1\t5\n")
	e.imdb.body.Store(&bad)
	err := e.rt.RunIMDb(ctx)
	if !errors.Is(err, ErrIMDbFormat) {
		t.Fatalf("err = %v, want ErrIMDbFormat (the job does not retry it)", err)
	}
	if e.countRows("title_ratings") != 0 || e.file(IMDbRatingsFile) != nil {
		t.Error("something was kept from it")
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("due again at once: the file is as it was, for a week")
	}
	e.advance(8 * 24 * time.Hour)
	if !e.rt.IMDbDue(ctx) {
		t.Error("not due after a week")
	}
	good := datasetBody(t, map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})
	e.imdb.body.Store(&good)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Fatal("control: no rating from the repaired file")
	}

	// A good copy that went bad on disk: removed, and fetched again at the next run.
	path := filepath.Join(e.dir, IMDbRatingsFile)
	if err := os.WriteFile(path, gzipOf(t, "garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.movie(2, "tt1000002") // the library changed, so the copy is read
	e.advance(2 * time.Hour)
	if err := e.rt.RunIMDb(ctx); !errors.Is(err, ErrIMDbFormat) {
		t.Fatalf("a bad copy: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the bad copy is still there")
	}
	hits := e.imdb.hits.Load()
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if e.imdb.hits.Load() != hits+1 {
		t.Error("the dataset was not fetched again")
	}
}

// IMDb adds a column to the header: the file is not the dataset any more as far as we can tell, and
// the good copy is not lost for it, nor is the file downloaded at every look (670 MB a day with the
// real one).
func TestRunIMDbAChangedHeaderIsNotFetchedAgainForAWeek(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	good := e.file(IMDbRatingsFile)
	if len(good) == 0 {
		t.Fatal("control: no copy")
	}

	// The columns changed: the first three are now others.
	bad := gzipOf(t, "tconst\tvotes\trating\ntt1000001\t2000\t8.7\n")
	e.imdb.body.Store(&bad)
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	start := e.imdb.hits.Load()
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatalf("a refused file with a good copy to use: %v", err)
	}
	if e.imdb.hits.Load() != start+1 {
		t.Fatalf("control: the changed file was not asked for")
	}
	if !bytes.Equal(e.file(IMDbRatingsFile), good) {
		t.Error("the good copy was lost")
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Error("the rating was lost")
	}

	// Two days of looks every 15 minutes: not one more download.
	for range 8 * 24 {
		e.advance(15 * time.Minute)
		if e.rt.IMDbDue(ctx) {
			if err := e.rt.RunIMDb(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := e.imdb.hits.Load() - start; got != 1 {
		t.Errorf("%d downloads in the 2 days from the refusal, want just the one that was refused", got)
	}
	// A week on the file is asked for again, and the repaired one is taken.
	fixed := datasetBody(t, map[string]string{"tt1000001": "9.9 5000", "tt1000002": "6.0 20"})
	e.imdb.body.Store(&fixed)
	newVersion(e.imdb, "v3")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.rating(a, store.RatingIMDb); r.Value != 9.9 {
		t.Errorf("the repaired file was not taken: %+v", r)
	}
}

// A dataset with the right header and (nearly) no rows is a mirror that is cut off, not IMDb's: it does
// not replace the copy we have, and does not take the ratings away. Half of the rows of the last one is
// the least.
func TestRunIMDbRefusesADatasetWithTooFewRows(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	b := e.movie(2, "tt1000002")
	four := map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20", "tt1000003": "5.0 5", "tt1000004": "4.0 4"}
	e.setDataset(four)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if e.setting(setRatings.key("rows")) != "4" {
		t.Fatalf("rows = %q, want the 4 of the dataset", e.setting(setRatings.key("rows")))
	}
	good := e.file(IMDbRatingsFile)
	for n, rows := range []map[string]string{{}, {"tt1000001": "8.7 2000"}} {
		e.setDataset(rows)
		newVersion(e.imdb, fmt.Sprint("short", n))
		e.advance(8 * 24 * time.Hour)
		hits := e.imdb.hits.Load()
		if err := e.rt.RunIMDb(ctx); err != nil {
			t.Fatalf("%d rows: %v", len(rows), err)
		}
		if e.imdb.hits.Load() != hits+1 {
			t.Fatalf("%d rows: control: the file was not asked for", len(rows))
		}
		if !bytes.Equal(e.file(IMDbRatingsFile), good) {
			t.Errorf("%d rows: the copy was replaced", len(rows))
		}
		for _, id := range []int64{a, b} {
			if _, ok := e.rating(id, store.RatingIMDb); !ok {
				t.Errorf("%d rows: title %d lost its rating", len(rows), id)
			}
		}
		if got := e.setting(setRatings.key("rows")); got != "4" {
			t.Errorf("%d rows: the row count moved to %s", len(rows), got)
		}
	}

	// Exactly half is a dataset: the control, and it is applied (b's id is not in it any more).
	e.setDataset(map[string]string{"tt1000001": "9.0 3000", "tt1000009": "3.0 3"})
	newVersion(e.imdb, "half")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.rating(a, store.RatingIMDb); r.Value != 9.0 {
		t.Errorf("a half-size dataset was not applied: %+v", r)
	}
	if _, ok := e.rating(b, store.RatingIMDb); ok {
		t.Error("b keeps a rating its id has no row for")
	}

	// The first copy there is: a header and no rows is nothing to keep.
	first := newRatingsEnv(t)
	first.movie(1, "tt1000001")
	first.setDataset(map[string]string{})
	if err := first.rt.RunIMDb(ctx); !errors.Is(err, ErrIMDbShort) {
		t.Errorf("a first copy with no rows: %v, want ErrIMDbShort", err)
	}
	if first.file(IMDbRatingsFile) != nil {
		t.Error("a copy with no rows was kept")
	}
}

// A dataset that has none of the library's ids is not a reason to take the ratings away: they stay,
// and the job is not due again for it at every look.
func TestRunIMDbDoesNotPruneWhenNoIDOfTheLibraryIsInIt(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Fatal("control: no rating")
	}

	e.setDataset(map[string]string{"tt2000001": "8.7 2000", "tt2000002": "6.0 20"}) // as many rows, none of ours
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if e.imdb.hits.Load() != 2 {
		t.Fatalf("control: the new file was not fetched (%d requests)", e.imdb.hits.Load())
	}
	if r, ok := e.rating(a, store.RatingIMDb); !ok || r.Value != 8.7 {
		t.Errorf("the rating went: %+v, %v", r, ok)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("due again: it would read the file at every look")
	}
}

// An apply that failed after the download (the job was stopped) is done again from the copy at the
// next run, with no request: what is recorded as written is what was written.
func TestRunIMDbRetriesAWriteThatFailed(t *testing.T) {
	var fail atomic.Bool
	e := newRatingsEnv(t, func(_ *SettingsOptions, ro *RatingsOptions) {
		ro.Pace = func(ctx context.Context) error {
			if fail.CompareAndSwap(true, false) {
				return errors.New("job stopped")
			}
			return ctx.Err()
		}
	})
	ctx := t.Context()
	rows := map[string]string{}
	var ids []int64
	for i := range 520 {
		id := fmt.Sprintf("tt%07d", 100+i)
		ids = append(ids, e.movie(int64(1000+i), id))
		rows[id] = "7.5 100"
	}
	e.setDataset(rows)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	for id := range rows {
		rows[id] = "9.0 100"
	}
	e.setDataset(rows)
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	fail.Store(true)
	if err := e.rt.RunIMDb(ctx); err == nil {
		t.Fatal("the stopped write was not an error")
	}
	hits := e.imdb.hits.Load()
	if hits != 2 {
		t.Fatalf("control: %d requests; the new dataset was downloaded", hits)
	}
	old := func() int {
		n := 0
		for _, id := range ids {
			if r, _ := e.rating(id, store.RatingIMDb); r.Value == 7.5 {
				n++
			}
		}
		return n
	}
	if old() == 0 {
		t.Fatal("control: the stopped write finished")
	}
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("not due: a copy nothing was written from")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if e.imdb.hits.Load() != hits {
		t.Errorf("%d requests to write what was downloaded", e.imdb.hits.Load()-hits)
	}
	if n := old(); n != 0 {
		t.Errorf("%d titles are on last week's ratings", n)
	}
	if e.rt.IMDbDue(ctx) {
		t.Error("still due once it was written")
	}
}

// What a dataset was written for is the library's ids and the copy: a library that changed is written
// again, but not before an hour has passed since the last write, so that a library being scanned is not
// read at every look; a new copy at once.
func TestIMDbDueWhenTheLibraryChanges(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	e.movie(1, "tt1000001")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt1000002": "6.0 20", "tt1000003": "5.0 3", "tt1000004": "4.0 4"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}

	// Nothing changed: not due, even long after the hour.
	e.advance(3 * time.Hour)
	if e.rt.IMDbDue(ctx) {
		t.Fatal("due with nothing changed")
	}

	// A title came with an IMDb id: due, three hours after the last write.
	b := e.movie(2, "tt1000002")
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("a new title with an IMDb id does not make the job due")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(b, store.RatingIMDb); !ok {
		t.Fatal("the new title has no rating")
	}

	// Another one, straight after: not before the refit time (ten minutes) has passed since that write.
	c := e.movie(3, "tt1000003")
	if e.rt.IMDbDue(ctx) {
		t.Error("due at once for a new title: it waits for the refit time since the last write")
	}
	e.advance(IMDbRatingsRefit - time.Minute)
	if e.rt.IMDbDue(ctx) {
		t.Error("due before the refit time since the last write")
	}
	e.advance(2 * time.Minute)
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("not due after the refit time since the last write, with a new title")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.rating(c, store.RatingIMDb); !ok {
		t.Fatal("the third title has no rating")
	}

	// The same titles, one with another IMDb id: that is a change too.
	e.advance(IMDbRatingsRefit + time.Minute)
	if e.rt.IMDbDue(ctx) {
		t.Fatal("control: due again with nothing changed")
	}
	if _, err := e.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{ID: b, Kind: store.KindMovie, TMDBID: 2, IMDbID: "tt1000004", Title: "Movie 2", Source: store.SourceTMDB}}); err != nil {
		t.Fatal(err)
	}
	if !e.rt.IMDbDue(ctx) {
		t.Error("a title with another IMDb id (and the same number of titles) does not make the job due")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.rating(b, store.RatingIMDb); r.Value != 4.0 {
		t.Errorf("the title with the other id has %+v", r)
	}

	// A new copy of the dataset is due at once, library or not.
	e.setDataset(map[string]string{"tt1000001": "9.9 2000", "tt1000002": "6.0 20", "tt1000003": "5.0 3", "tt1000004": "4.0 4"})
	newVersion(e.imdb, "v2")
	e.advance(8 * 24 * time.Hour)
	if !e.rt.IMDbDue(ctx) {
		t.Error("not due a week on")
	}
}

// The fingerprints are the same for the same ids in any order and differ when any of the ids, or the
// titles that have them, do.
func TestFingerprints(t *testing.T) {
	lib := func(titles map[string][]int64, episodes ...string) store.IMDbLibrary {
		l := store.IMDbLibrary{Titles: titles, Episodes: map[string]struct{}{}}
		for _, e := range episodes {
			l.Episodes[e] = struct{}{}
		}
		return l
	}
	base := lib(map[string][]int64{"tt1": {1}, "tt2": {2, 3}}, "tt10", "tt11")
	same := lib(map[string][]int64{"tt2": {2, 3}, "tt1": {1}}, "tt11", "tt10")
	if libraryFingerprint(base) != libraryFingerprint(same) {
		t.Error("the fingerprint depends on the order")
	}
	for name, other := range map[string]store.IMDbLibrary{
		"another title id":      lib(map[string][]int64{"tt1": {9}, "tt2": {2, 3}}, "tt10", "tt11"),
		"another title tconst":  lib(map[string][]int64{"tt7": {1}, "tt2": {2, 3}}, "tt10", "tt11"),
		"another episode":       lib(map[string][]int64{"tt1": {1}, "tt2": {2, 3}}, "tt10", "tt12"),
		"one id more":           lib(map[string][]int64{"tt1": {1}, "tt2": {2, 3, 4}}, "tt10", "tt11"),
		"an episode less":       lib(map[string][]int64{"tt1": {1}, "tt2": {2, 3}}, "tt10"),
		"an episode as a title": lib(map[string][]int64{"tt1": {1}, "tt2": {2, 3}, "tt10": {0}}, "tt11"),
	} {
		if libraryFingerprint(base) == libraryFingerprint(other) {
			t.Errorf("%s: the fingerprint did not change", name)
		}
	}

	shows := func(s ...store.IMDbShow) []store.IMDbShow { return s }
	sb := shows(store.IMDbShow{TitleID: 1, IMDbID: "tt1", Episodes: 5}, store.IMDbShow{TitleID: 2, IMDbID: "tt2", Episodes: 8})
	if showsFingerprint(sb) != showsFingerprint(shows(sb[1], sb[0])) {
		t.Error("the shows' fingerprint depends on the order")
	}
	for name, other := range map[string][]store.IMDbShow{
		"a show less":          sb[:1],
		"another title id":     shows(store.IMDbShow{TitleID: 3, IMDbID: "tt1", Episodes: 5}, sb[1]),
		"another IMDb id":      shows(store.IMDbShow{TitleID: 1, IMDbID: "tt9", Episodes: 5}, sb[1]),
		"more episodes stored": shows(store.IMDbShow{TitleID: 1, IMDbID: "tt1", Episodes: 6}, sb[1]),
	} {
		if showsFingerprint(sb) == showsFingerprint(other) {
			t.Errorf("%s: the shows' fingerprint did not change", name)
		}
	}
}

// ---- the episodes' IMDb ids ----

// The episode dataset maps the ids of a show's episodes by season and number, for a show in the aired
// order; a provider's id is kept; a season whose count is far from the dataset's is left alone.
func TestRunIMDbMapsTheEpisodes(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	var eps []store.Episode
	for n := 1; n <= 4; n++ {
		eps = append(eps, store.Episode{Season: 1, Number: n, Name: fmt.Sprint("E", n)})
	}
	eps[2].IMDbID = "tt9000003" // a provider gave this one
	show := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB}, eps...)
	// A show whose season has a very different count: eight in the dataset against two stored.
	far := e.show("Far", store.Title{TMDBID: 11, IMDbID: "tt2000003", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "A"}, store.Episode{Season: 1, Number: 2, Name: "B"})
	rows := []string{"tt3000001 tt2000002 1 1", "tt3000002 tt2000002 1 2", "tt3000003 tt2000002 1 3", "tt3000004 tt2000002 1 4",
		"tt3000000 tt2000002 0 1"} // a special
	for n := 1; n <= 8; n++ {
		rows = append(rows, fmt.Sprintf("tt400000%d tt2000003 1 %d", n, n))
	}
	e.setEpisodes(rows...)
	e.setDataset(map[string]string{"tt3000001": "9.1 100", "tt3000003": "7.3 100", "tt9000003": "6.2 100", "tt4000001": "5.0 5"})

	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := e.episodesOf(show), []string{"1=tt3000001@dataset", "2=tt3000002@dataset", "3=tt9000003@provider", "4=tt3000004@dataset"}; !slices.Equal(got, want) {
		t.Errorf("episodes = %v, want %v (the provider's id is kept)", got, want)
	}
	if got, want := e.episodesOf(far), []string{"1=-@", "2=-@"}; !slices.Equal(got, want) {
		t.Errorf("the show whose counts differ: %v, want no ids", got)
	}
	got, _ := e.st.Episodes(ctx, show)
	if got[2].IMDbRating == nil || got[2].IMDbRating.Value != 6.2 || got[0].IMDbRating == nil || got[0].IMDbRating.Value != 9.1 {
		t.Errorf("ratings of the episodes: %+v / %+v", got[0].IMDbRating, got[2].IMDbRating)
	}

	// The shows are not mapped again while nothing changes; a stored refresh keeps what was mapped.
	e.advance(3 * time.Hour)
	if e.rt.IMDbDue(ctx) {
		t.Error("due with nothing changed")
	}
	again := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 10, IMDbID: "tt2000002", Title: "Show", Source: store.SourceTMDB}}
	again.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{{Number: 1, Name: "E1 (new)"}, {Number: 2, Name: "E2"}, {Number: 3, Name: "E3"}, {Number: 4, Name: "E4"}}}}
	if _, err := e.st.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got, want := e.episodesOf(show), []string{"1=tt3000001@dataset", "2=tt3000002@dataset", "3=tt9000003@provider", "4=tt3000004@dataset"}; !slices.Equal(got, want) {
		t.Errorf("after a refresh of the show: %v", got)
	}
}

// The episode dataset is asked for only when a show needs it: a show in an order other than the aired
// one is not mapped, and a library with no such show does not download the file.
func TestRunIMDbAsksForTheEpisodesOnlyForAiredShows(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	anime := e.show("Anime", store.Title{TVDBID: 555, IMDbID: "tt2000009", Source: store.SourceTVDB, EpisodeOrder: store.OrderDVD},
		store.Episode{Season: 1, Number: 1, Name: "A"}, store.Episode{Season: 1, Number: 2, Name: "B"})
	e.movie(1, "tt1000001")
	e.setEpisodes("tt3000001 tt2000009 1 1", "tt3000002 tt2000009 1 2")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt3000001": "9.0 5"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.episodes.hits.Load(); got != 0 {
		t.Errorf("%d requests for the episodes of a library whose only show is not in the aired order", got)
	}
	if got := e.episodesOf(anime); !slices.Equal(got, []string{"1=-@", "2=-@"}) {
		t.Errorf("the show in the DVD order got ids: %v", got)
	}
	if e.imdb.hits.Load() != 1 {
		t.Errorf("control: %d requests for the ratings", e.imdb.hits.Load())
	}

	// Control: an aired show makes the file wanted, and it maps the aired one only.
	aired := e.show("Aired", store.Title{TMDBID: 10, IMDbID: "tt2000009", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "A"}, store.Episode{Season: 1, Number: 2, Name: "B"})
	e.advance(2 * time.Hour)
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("an aired show with an IMDb id does not make the job due")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if e.episodes.hits.Load() != 1 {
		t.Errorf("control: %d requests for the episodes", e.episodes.hits.Load())
	}
	if got := e.episodesOf(aired); !slices.Equal(got, []string{"1=tt3000001@dataset", "2=tt3000002@dataset"}) {
		t.Errorf("the aired show: %v", got)
	}
	if got := e.episodesOf(anime); !slices.Equal(got, []string{"1=-@", "2=-@"}) {
		t.Errorf("the show in the DVD order got ids once an aired show was mapped: %v", got)
	}
}

// The episode file is held to the same checks as the ratings': a cut-off mirror does not take the ids
// away, and not having it does not stop the ratings.
func TestRunIMDbEpisodeFileFailures(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	a := e.movie(1, "tt1000001")
	show := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "A"}, store.Episode{Season: 1, Number: 2, Name: "B"})
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt3000001": "9.0 5"})

	// No episode file to be had: the ratings are still written, and the error says why.
	e.episodes.status.Store(503)
	err := e.rt.RunIMDb(ctx)
	if err == nil {
		t.Error("no error though the episode dataset could not be fetched")
	}
	if _, ok := e.rating(a, store.RatingIMDb); !ok {
		t.Error("the ratings were not written because the episodes could not be fetched")
	}

	// It comes: the ids are mapped. The 30 minutes of the failure have passed.
	e.episodes.status.Store(0)
	e.setEpisodes("tt3000001 tt2000002 1 1", "tt3000002 tt2000002 1 2")
	e.advance(31 * time.Minute)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"1=tt3000001@dataset", "2=tt3000002@dataset"}
	if got := e.episodesOf(show); !slices.Equal(got, want) {
		t.Fatalf("episodes = %v, want %v", got, want)
	}
	good := e.file(IMDbEpisodesFile)

	// A new file with a header and no rows, and one with the columns changed: refused, the ids stay.
	for n, body := range [][]byte{episodesBody(t), gzipOf(t, "tconst\tshow\tseason\tepisode\ntt3000001\ttt2000002\t1\t1\n")} {
		e.episodes.body.Store(&body)
		newVersion(e.episodes, fmt.Sprint("bad", n))
		e.advance(8 * 24 * time.Hour)
		hits := e.episodes.hits.Load()
		if err := e.rt.RunIMDb(ctx); err != nil {
			t.Fatalf("file %d: %v", n, err)
		}
		if e.episodes.hits.Load() != hits+1 {
			t.Fatalf("file %d: control: not asked for", n)
		}
		if !bytes.Equal(e.file(IMDbEpisodesFile), good) {
			t.Errorf("file %d: the copy was replaced", n)
		}
		if got := e.episodesOf(show); !slices.Equal(got, want) {
			t.Errorf("file %d: episodes = %v, want %v", n, got, want)
		}
	}

	// A good file that lacks this show (as many rows, other shows): its ids are not taken away.
	e.setEpisodes("tt5000001 tt6000006 1 1", "tt5000002 tt6000006 1 2")
	newVersion(e.episodes, "others")
	e.advance(8 * 24 * time.Hour)
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.episodesOf(show); !slices.Equal(got, want) {
		t.Errorf("a file without the show took its ids away: %v", got)
	}
}

// A mapping that failed half way is done again from the copy, with no request.
func TestRunIMDbRetriesAMappingThatFailed(t *testing.T) {
	var fail atomic.Bool
	e := newRatingsEnv(t, func(_ *SettingsOptions, ro *RatingsOptions) {
		ro.Pace = func(ctx context.Context) error {
			if fail.CompareAndSwap(true, false) {
				return errors.New("job stopped")
			}
			return ctx.Err()
		}
	})
	ctx := t.Context()
	var eps []store.Episode
	var rows []string
	for n := 1; n <= 600; n++ {
		eps = append(eps, store.Episode{Season: 1, Number: n, Name: fmt.Sprint("E", n)})
		rows = append(rows, fmt.Sprintf("tt30%05d tt2000002 1 %d", n, n))
	}
	show := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB}, eps...)
	e.setEpisodes(rows...)
	e.setDataset(map[string]string{"tt2000002": "9.0 5"})
	fail.Store(true)
	if err := e.rt.RunIMDb(ctx); err == nil {
		t.Fatal("the stopped mapping was not an error")
	}
	mapped := func() int {
		n := 0
		for _, s := range e.episodesOf(show) {
			if strings.Contains(s, "@dataset") {
				n++
			}
		}
		return n
	}
	if n := mapped(); n != 500 {
		t.Fatalf("control: %d episodes mapped by the stopped run, want the first chunk's 500", n)
	}
	hits := e.episodes.hits.Load()
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("not due: a copy nothing was fully written from")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if n := mapped(); n != 600 {
		t.Errorf("%d episodes mapped after the retry, want 600", n)
	}
	if e.episodes.hits.Load() != hits {
		t.Errorf("%d requests to map what was downloaded", e.episodes.hits.Load()-hits)
	}
}

// ---- MDBList ----

func TestRunMDBListStoresTheAnswer(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	movie := e.movie(603, "tt0133093")
	show := e.show("Show", store.Title{TMDBID: 1396, IMDbID: "tt0903747", Rating: 8.9, VoteCount: 50, Source: store.SourceTMDB}, store.Episode{Season: 1, Number: 1, Name: "One"})

	due, err := e.rt.DueMDBList(ctx, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v; want both titles", due, err)
	}
	for _, d := range due {
		if err := e.rt.RunMDBList(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	var paths []string
	for _, r := range e.mdb.requests() {
		paths = append(paths, r.Path)
	}
	if !slices.Equal(paths, []string{"/user", "/tmdb/movie/603", "/tmdb/show/1396"}) {
		t.Errorf("MDBList was asked %v; the day's allowance once, then the TMDB id of each, and nothing else", paths)
	}
	for _, r := range e.mdb.requests() {
		if r.Key != mdbTestKey || strings.Contains(strings.TrimPrefix(r.Query, "apikey="+mdbTestKey), "Movie") {
			t.Errorf("request %+v sends more than the key", r)
		}
	}
	for _, id := range []int64{movie, show} {
		got := e.ratings(id)
		var names []string
		for _, r := range got {
			names = append(names, r.Source)
		}
		if !slices.Equal(names, []string{store.RatingRTCritics, store.RatingRTAudience, store.RatingMetacritic, store.RatingLetterboxd, store.RatingTrakt, store.RatingTMDB}) {
			t.Errorf("title %d: ratings %v", id, names)
		}
	}
	if r, _ := e.rating(movie, store.RatingRTCritics); r.State != "fresh" || r.Value != 83 || r.FetchedAt != e.clock().Unix() {
		t.Errorf("rt_critics = %+v", r)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("due after the answers: %+v", due)
	}
	// 14 days on they are due again; 13 and a bit, not yet.
	e.advance(13 * 24 * time.Hour)
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("due after 13 days: %+v", due)
	}
	e.advance(2 * 24 * time.Hour)
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 2 {
		t.Errorf("due after 15 days: %+v", due)
	}
}

// By TMDB id first, by IMDb id when TMDB's is not known to MDBList or the title has none; an
// answer of none is an answer.
func TestRunMDBListIdsAndNotFound(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	both := e.movie(1, "tt0000001")
	onlyIMDb := e.movie(0, "tt0000002")
	onlyTMDB := e.movie(3, "")
	nothing := e.movie(0, "")
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{}`)
		case "/tmdb/movie/1": // TMDB's id is not known to MDBList; IMDb's is
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error": "Not found"}`)
		case "/imdb/movie/tt0000001", "/imdb/movie/tt0000002":
			fmt.Fprint(w, `{"ratings": [{"source": "metacritic", "value": 70}]}`)
		default: // tmdb 3 is not known at all
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error": "Not found"}`)
		}
	})

	due, _ := e.rt.DueMDBList(ctx, 10)
	var ids []int64
	for _, d := range due {
		ids = append(ids, d.ID)
	}
	slices.Sort(ids)
	if want := []int64{both, onlyIMDb, onlyTMDB}; !slices.Equal(ids, want) {
		t.Fatalf("due = %v, want %v (and not %d, which has no id)", ids, want, nothing)
	}
	for _, id := range ids {
		if err := e.rt.RunMDBList(ctx, id); err != nil {
			t.Fatalf("title %d: %v", id, err)
		}
	}
	var paths []string
	for _, r := range e.mdb.requests() {
		paths = append(paths, r.Path)
	}
	if want := []string{"/user", "/tmdb/movie/1", "/imdb/movie/tt0000001", "/imdb/movie/tt0000002", "/tmdb/movie/3"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	for _, id := range []int64{both, onlyIMDb} {
		if r, ok := e.rating(id, store.RatingMetacritic); !ok || r.Value != 70 {
			t.Errorf("title %d: metacritic = %+v, %v", id, r, ok)
		}
	}
	if _, ok := e.rating(onlyTMDB, store.RatingMetacritic); ok {
		t.Error("a title MDBList does not know got a rating")
	}
	// Asked, and not asked again for a while, though it has no ratings.
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("due after the answers: %+v (an empty answer is an answer)", due)
	}
	if err := e.rt.RunMDBList(ctx, 424242); err != nil {
		t.Errorf("a title that is gone: %v", err)
	}
}

// What MDBList gives is paced to the day's budget: MDBList's own allowance less a tenth, or 900.
func TestMDBListBudget(t *testing.T) {
	ctx := t.Context()

	t.Run("900 when MDBList does not say", func(t *testing.T) {
		e := newRatingsEnv(t)
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprint(w, `{}`) })
		client, _ := e.settings.MDBList(ctx)
		granted := 0
		for range 1000 {
			ok, err := e.rt.reserve(ctx, client)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				granted++
			}
		}
		if granted != 900 {
			t.Errorf("%d requests granted, want 900", granted)
		}
		if got := e.rt.RemainingMDBList(ctx); got != 0 {
			t.Errorf("remaining = %d", got)
		}
	})

	t.Run("the allowance and use MDBList reports", func(t *testing.T) {
		e := newRatingsEnv(t)
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 120}`)
		})
		client, _ := e.settings.MDBList(ctx)
		if ok, err := e.rt.reserve(ctx, client); err != nil || !ok {
			t.Fatalf("reserve = %v, %v", ok, err)
		}
		if got := e.rt.RemainingMDBList(ctx); got != 900-120-1 {
			t.Errorf("remaining = %d, want %d (900 of 1,000, less 120 used and the one just made)", got, 900-121)
		}
		if n := e.mdb.count(); n != 1 {
			t.Errorf("%d requests to read the allowance, want 1", n)
		}
		// Not read again the same day.
		e.rt.reserve(ctx, client)
		if n := e.mdb.count(); n != 1 {
			t.Errorf("%d requests; the allowance is read once a day", n)
		}
		// The next UTC day it is, and the count starts from what MDBList says.
		e.advance(24 * time.Hour)
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 10}`)
		})
		if got := e.rt.RemainingMDBList(ctx); got != 900 {
			t.Errorf("remaining on a new day before any request = %d, want a full 900", got)
		}
		e.rt.reserve(ctx, client)
		if n := e.mdb.count(); n != 2 {
			t.Errorf("%d requests; the allowance is read again on a new day", n)
		}
		if got := e.rt.RemainingMDBList(ctx); got != 900-10-1 {
			t.Errorf("remaining = %d, want %d", got, 900-11)
		}
	})

	t.Run("a spent allowance asks nothing and leaves the title due", func(t *testing.T) {
		e := newRatingsEnv(t)
		id := e.movie(603, "tt0133093")
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			if r.URL.Path != "/user" {
				t.Errorf("a title was asked for (%s) with the allowance spent", r.URL.Path)
			}
			fmt.Fprint(w, `{"api_requests": 100, "api_requests_count": 95}`)
		})
		if err := e.rt.RunMDBList(ctx, id); err != nil {
			t.Fatal(err)
		}
		if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
			t.Errorf("DueMDBList = %+v with no budget left", due)
		}
		e.advance(24 * time.Hour) // a new day: the title is still due, and now there is budget
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			if r.URL.Path == "/user" {
				fmt.Fprint(w, `{"api_requests": 100, "api_requests_count": 0}`)
				return
			}
			fmt.Fprint(w, mdbTitle)
		})
		due, _ := e.rt.DueMDBList(ctx, 10)
		if len(due) != 1 || due[0].ID != id {
			t.Fatalf("due on the next day = %+v", due)
		}
		if err := e.rt.RunMDBList(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.rating(id, store.RatingMetacritic); !ok {
			t.Error("the title was not rated on the next day")
		}
	})

	t.Run("the count survives a restart", func(t *testing.T) {
		e := newRatingsEnv(t)
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprint(w, `{}`) }) // MDBList says nothing, so the count is ours
		client, _ := e.settings.MDBList(ctx)
		for range 300 {
			e.rt.reserve(ctx, client)
		}
		again := NewRatings(RatingsOptions{Store: e.st, Settings: e.settings, Dir: e.dir, Now: e.clock})
		if got := again.RemainingMDBList(ctx); got != 600 {
			t.Errorf("remaining after a restart = %d, want 600", got)
		}
		// The new process reads MDBList's allowance for the day, which says nothing here: what we counted
		// is not forgotten for it.
		if ok, err := again.reserve(ctx, client); err != nil || !ok {
			t.Fatalf("reserve after a restart = %v, %v", ok, err)
		}
		if got := again.RemainingMDBList(ctx); got != 599 {
			t.Errorf("remaining after the first request of a new process = %d, want 599", got)
		}
	})

	t.Run("the day's allowance gone, said by MDBList", func(t *testing.T) {
		e := newRatingsEnv(t)
		a, b := e.movie(1, "tt0000001"), e.movie(2, "tt0000002")
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			if r.URL.Path == "/user" {
				fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
				return
			}
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error": "You have reached your daily API limit"}`)
		})
		if err := e.rt.RunMDBList(ctx, a); err != nil {
			t.Fatalf("a daily limit is not a failure of the job: %v", err)
		}
		if got := e.rt.RemainingMDBList(ctx); got != 0 {
			t.Errorf("remaining = %d after MDBList said the day is over", got)
		}
		n := e.mdb.count()
		if err := e.rt.RunMDBList(ctx, b); err != nil || e.mdb.count() != n {
			t.Errorf("the next title: %v, %d new requests; none while the allowance is gone", err, e.mdb.count()-n)
		}
		if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
			t.Errorf("due = %+v", due)
		}
	})

	t.Run("a batch is no more than what is left", func(t *testing.T) {
		e := newRatingsEnv(t)
		for i := range 12 {
			e.movie(int64(100+i), fmt.Sprintf("tt%07d", 100+i))
		}
		e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
			fmt.Fprint(w, `{"api_requests": 100, "api_requests_count": 85}`) // 90 allowed, 85 used: 5 left
		})
		client, _ := e.settings.MDBList(ctx)
		e.rt.reserve(ctx, client) // 4 left now
		due, err := e.rt.DueMDBList(ctx, 10)
		if err != nil || len(due) != 4 {
			t.Errorf("due = %d, %v; want 4 (the budget), not 10 (the batch) or 12 (the titles)", len(due), err)
		}
		if due, _ := e.rt.DueMDBList(ctx, 3); len(due) != 3 {
			t.Errorf("a batch of 3 gave %d", len(due))
		}
	})
}

// The key and the switches decide whether MDBList is asked at all.
func TestMDBListOnlyWithAKeyAndLookupsOn(t *testing.T) {
	for name, mod := range map[string]func(*SettingsOptions, *RatingsOptions){
		"no key":          func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag = "" },
		"-metadata=false": func(so *SettingsOptions, _ *RatingsOptions) { so.Disabled = true },
	} {
		e := newRatingsEnv(t, mod)
		id := e.movie(603, "tt0133093")
		due, err := e.rt.DueMDBList(t.Context(), 10)
		if err != nil || len(due) != 0 {
			t.Errorf("%s: due = %+v, %v", name, due, err)
		}
		if err := e.rt.RunMDBList(t.Context(), id); err != nil || e.mdb.count() != 0 {
			t.Errorf("%s: %v, %d requests", name, err, e.mdb.count())
		}
	}
	// The setting that turns lookups off.
	e := newRatingsEnv(t)
	id := e.movie(603, "tt0133093")
	if err := e.st.SetSetting(t.Context(), SettingEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if due, _ := e.rt.DueMDBList(t.Context(), 10); len(due) != 0 {
		t.Errorf("lookups off: due %+v", due)
	}
	if err := e.rt.RunMDBList(t.Context(), id); err != nil || e.mdb.count() != 0 {
		t.Errorf("lookups off: %v, %d requests", err, e.mdb.count())
	}
	// Control: on again, it asks.
	if err := e.st.SetSetting(t.Context(), SettingEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if due, _ := e.rt.DueMDBList(t.Context(), 10); len(due) != 1 {
		t.Fatalf("control: due %+v", due)
	}
	if err := e.rt.RunMDBList(t.Context(), id); err != nil || e.mdb.count() == 0 {
		t.Errorf("control: %v, %d requests", err, e.mdb.count())
	}
}

// A refused key is a final failure that stops the asking until the key is changed; a failure of
// the network is a failure to try again, and leaves the title due.
func TestMDBListFailures(t *testing.T) {
	ctx := t.Context()
	e := newRatingsEnv(t)
	id := e.movie(603, "tt0133093")
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error": "Invalid API key"}`)
	})
	err := e.rt.RunMDBList(ctx, id)
	if !IsUnauthorized(err) {
		t.Fatalf("err = %v, want a refusal of the key", err)
	}
	if !e.settings.MDBListRefused() {
		t.Error("the refusal was not remembered")
	}
	n := e.mdb.count()
	if err := e.rt.RunMDBList(ctx, id); err != nil || e.mdb.count() != n {
		t.Errorf("after a refusal: %v, %d new requests", err, e.mdb.count()-n)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("a refused key still has titles due: %+v", due)
	}

	// A new key (one that works) ends the refusal.
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})
	newKey := "NEWKEY0123456789"
	if err := e.settings.Update(ctx, Update{MDBListKey: &newKey}); err != nil {
		t.Fatal(err)
	}
	// The flag still beats the stored key, so what is asked is the flag's; the refusal is over all the same.
	if e.settings.MDBListRefused() {
		t.Error("a new key did not end the refusal")
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 1 {
		t.Errorf("due = %+v", due)
	}

	// The network: an error to retry, and the title is not marked asked.
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(502)
	})
	err = e.rt.RunMDBList(ctx, id)
	if err == nil || !IsTransient(err) || IsUnauthorized(err) {
		t.Fatalf("a 502: %v", err)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 1 {
		t.Errorf("a failed ask marked the title as asked: due %+v", due)
	}
}

// ---- one title MDBList will not rate, and a pause ----

// An answer for one title that asking again will not change (a refusal, or a 200 that says it failed in
// words we do not know) is not a failure of the run: the title is marked as asked, its ratings stay as
// they were, and the titles behind it are rated.
func TestMDBListATitleItWillNotRateDoesNotHoldUpTheRest(t *testing.T) {
	ctx := t.Context()
	e := newRatingsEnv(t)
	ids := []int64{e.movie(1, "tt0000001"), e.movie(2, "tt0000002"), e.movie(3, "tt0000003"), e.movie(4, "tt0000004")}
	// Poison: 2 gets a 422, 3 a 200 that says it failed.
	poison := map[string]func(w http.ResponseWriter){
		"/tmdb/movie/2": func(w http.ResponseWriter) { w.WriteHeader(422); fmt.Fprint(w, `{"error": "cannot process"}`) },
		"/tmdb/movie/3": func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"response": false, "error": "something we have not seen"}`)
		},
	}
	var armed atomic.Bool
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
			return
		}
		if f, ok := poison[r.URL.Path]; ok && armed.Load() {
			f(w)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})

	// First all four are rated; the poison titles have ratings to keep.
	for _, id := range ids {
		if err := e.rt.RunMDBList(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		if _, ok := e.rating(id, store.RatingMetacritic); !ok {
			t.Fatalf("control: title %d has no rating", id)
		}
	}

	e.advance(15 * 24 * time.Hour) // all due again
	armed.Store(true)
	asked := e.clock().Unix()
	for _, id := range ids {
		if err := e.rt.RunMDBList(ctx, id); err != nil {
			t.Errorf("title %d: %v; a refused title is not a failure of the run", id, err)
		}
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("due = %+v; every title was asked about, the poison ones too", due)
	}
	for _, id := range ids {
		r, ok := e.rating(id, store.RatingMetacritic)
		if !ok {
			t.Errorf("title %d lost its ratings", id)
		}
		// The healthy ones have the new answer; the poison ones are as they were.
		if fresh := r.FetchedAt == asked; fresh == (id == ids[1] || id == ids[2]) {
			t.Errorf("title %d: fetched at %d (asked now: %d)", id, r.FetchedAt, asked)
		}
	}

	// A failure that may pass is still an error to retry, and leaves the title due.
	e.advance(15 * 24 * time.Hour)
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(503)
	})
	if err := e.rt.RunMDBList(ctx, ids[0]); !IsTransient(err) {
		t.Errorf("a 503: %v, want a transient error", err)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 4 {
		t.Errorf("due = %d after a 503, want all four", len(due))
	}
}

// While MDBList has the client paused for longer than it waits, a title is not sent and not counted:
// the day's budget is for requests that were made. A 200 that says the rate limit was reached is such a
// pause too, a minute long.
func TestMDBListAPauseSpendsNoBudget(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name  string
		limit func(w http.ResponseWriter)
		after time.Duration // the pause the error carries
	}{
		{"a 429 with Retry-After", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error": "Too many requests"}`)
		}, 120 * time.Second},
		{"a 200 with API Rate Limit Reached", func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"response": false, "error": "API Rate Limit Reached!"}`)
		}, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRatingsEnv(t)
			var ids []int64
			for i := range 8 {
				ids = append(ids, e.movie(int64(100+i), fmt.Sprintf("tt%07d", 100+i)))
			}
			var limited atomic.Bool
			e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
				if r.URL.Path == "/user" {
					fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
					return
				}
				if limited.Load() {
					tc.limit(w)
					return
				}
				fmt.Fprint(w, mdbTitle)
			})
			// Control: a title that goes through costs one request, in the count and on the wire.
			if err := e.rt.RunMDBList(ctx, ids[0]); err != nil {
				t.Fatal(err)
			}
			if got := e.rt.RemainingMDBList(ctx); got != 899 {
				t.Fatalf("control: remaining = %d after one title, want 899", got)
			}

			limited.Store(true)
			err := e.rt.RunMDBList(ctx, ids[1]) // the one that finds out
			var me *MDBListError
			if !errors.As(err, &me) || !IsTransient(err) || me.RetryAfter != tc.after || me.Daily {
				t.Fatalf("err = %v (%+v), want a transient limit of %v", err, me, tc.after)
			}
			wire, remaining := e.mdb.count(), e.rt.RemainingMDBList(ctx)
			if remaining != 898 {
				t.Fatalf("remaining = %d after the title that was sent and limited, want 898", remaining)
			}
			for _, id := range ids[2:] {
				if err := e.rt.RunMDBList(ctx, id); !IsTransient(err) {
					t.Errorf("title %d during the pause: %v, want a transient error", id, err)
				}
			}
			if got := e.mdb.count(); got != wire {
				t.Errorf("%d requests were sent during the pause", got-wire)
			}
			if got := e.rt.RemainingMDBList(ctx); got != remaining {
				t.Errorf("remaining = %d after %d titles during the pause, want %d: the budget was spent on requests not made", got, len(ids)-2, remaining)
			}
			if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 7 {
				t.Errorf("due = %d, want the seven that were not rated", len(due))
			}
		})
	}
}

// ---- the run over every title that is due ----

// moviesWith adds n movies (TMDB ids 100, 101, ...) and says how MDBList answers for each path.
func moviesWith(t *testing.T, e *ratingsEnv, n int, answer func(w http.ResponseWriter, path string)) []int64 {
	t.Helper()
	var ids []int64
	for i := range n {
		ids = append(ids, e.movie(int64(100+i), fmt.Sprintf("tt%07d", 100+i)))
	}
	e.reply(func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
			return
		}
		answer(w, r.URL.Path)
	})
	return ids
}

func (e *ratingsEnv) rated(ids []int64) (n int) {
	for _, id := range ids {
		if _, ok := e.rating(id, store.RatingMetacritic); ok {
			n++
		}
	}
	return n
}

// A title MDBList always answers with a server error does not hold up the ones behind it: the run goes
// on past it, and the second time in a row it is given up on (marked as asked), so that it does not
// come back at every run.
func TestRunMDBListDueGoesOnPastATitleThatAlwaysFails(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	ids := moviesWith(t, e, 5, func(w http.ResponseWriter, path string) {
		if path == "/tmdb/movie/100" {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})

	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil {
		t.Fatalf("first run: %v; one title failing is not the run failing", err)
	}
	if got := e.rated(ids); got != 4 {
		t.Errorf("first run: %d titles rated, want the 4 behind the failing one", got)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 1 || due[0].ID != ids[0] {
		t.Errorf("after the first run %d titles are due (%+v), want the failing one only", len(due), due)
	}

	// The second run: it fails again, and is given up on.
	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if due, _ := e.rt.DueMDBList(ctx, 10); len(due) != 0 {
		t.Errorf("after the second run %d titles are due, want none: the title was marked as asked", len(due))
	}
	if got := e.rated(ids); got != 4 {
		t.Errorf("%d titles rated", got)
	}
	asked := 0
	for _, r := range e.mdb.requests() {
		if r.Path == "/tmdb/movie/100" {
			asked++
		}
	}
	if asked != 2 {
		t.Errorf("the failing title was asked %d times, want 2", asked)
	}
	// And it is not asked again by a third.
	before := e.mdb.count()
	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil || e.mdb.count() != before {
		t.Errorf("third run: %v, %d requests; nothing is due", err, e.mdb.count()-before)
	}
}

// A title that fails once and then works is rated, and its failure is forgotten: it takes two in a row to
// be given up on.
func TestRunMDBListDueForgetsAFailureThatPassed(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	var fail atomic.Int32 // the failures left for the first title
	fail.Store(1)
	ids := moviesWith(t, e, 2, func(w http.ResponseWriter, path string) {
		if path == "/tmdb/movie/100" && fail.Add(-1) >= 0 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})
	for run := 1; run <= 2; run++ {
		if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if got := e.rated(ids); got != 2 {
		t.Errorf("%d titles rated, want both", got)
	}

	// Fails, works, fails: never twice in a row, so it is never given up on, and is still due after the
	// second failure.
	e.advance(15 * 24 * time.Hour)
	fail.Store(1)
	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil { // fails: once
		t.Fatal(err)
	}
	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil { // works: forgotten
		t.Fatal(err)
	}
	if r, ok := e.rating(ids[0], store.RatingMetacritic); !ok || r.FetchedAt != e.clock().Unix() {
		t.Fatalf("control: the first title was not rated again: %+v, %v", r, ok)
	}
	e.advance(15 * 24 * time.Hour)
	fail.Store(1)
	if err := e.rt.RunMDBListDue(ctx, 20, nil); err != nil { // fails: once again, not twice
		t.Fatal(err)
	}
	due, _ := e.rt.DueMDBList(ctx, 10)
	if len(due) != 1 || due[0].ID != ids[0] {
		t.Errorf("due = %+v; a title that failed once since it last worked is still due, not given up on", due)
	}
}

// Three titles in a row answered with a server error end the run, with the error: MDBList is down. A
// title that works in between starts the count again.
func TestRunMDBListDueStopsAfterThreeInARow(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	moviesWith(t, e, 6, func(w http.ResponseWriter, path string) { w.WriteHeader(502) })
	err := e.rt.RunMDBListDue(ctx, 20, nil)
	if err == nil || !IsTransient(err) || !serverFault(err) {
		t.Fatalf("err = %v, want the server error that ended the run", err)
	}
	asked := 0
	for _, r := range e.mdb.requests() {
		if r.Path != "/user" {
			asked++
		}
	}
	if asked != 3 {
		t.Errorf("%d titles asked before the run stopped, want 3", asked)
	}

	// Control: failures with a success between never add up to three in a row.
	c := newRatingsEnv(t)
	cids := moviesWith(t, c, 5, func(w http.ResponseWriter, path string) {
		if path == "/tmdb/movie/102" {
			fmt.Fprint(w, mdbTitle)
			return
		}
		w.WriteHeader(502)
	})
	if err := c.rt.RunMDBListDue(ctx, 20, nil); err != nil {
		t.Fatalf("failures with a success between: %v", err)
	}
	if got := c.rated(cids); got != 1 {
		t.Errorf("%d rated, want the one that works", got)
	}
	if n := len(c.mdb.requests()) - 1; n != 5 {
		t.Errorf("%d titles asked, want all 5", n)
	}
}

// A failure that is not the title's ends the run at once: a limit, the network, the key.
func TestRunMDBListDueStopsForWhatIsNotTheTitles(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func(w http.ResponseWriter)
		check  func(error) bool
	}{
		"a limit": {func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		}, func(err error) bool { return IsTransient(err) && !serverFault(err) }},
		"the key": {func(w http.ResponseWriter) {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error": "Invalid API key"}`)
		}, IsUnauthorized},
		"the network": {func(w http.ResponseWriter) { panic(http.ErrAbortHandler) }, func(err error) bool { return IsTransient(err) && !serverFault(err) }},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRatingsEnv(t)
			moviesWith(t, e, 5, func(w http.ResponseWriter, path string) { tc.answer(w) })
			err := e.rt.RunMDBListDue(t.Context(), 20, nil)
			if err == nil || !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			asked := map[string]bool{}
			for _, r := range e.mdb.requests() {
				if r.Path != "/user" {
					asked[r.Path] = true
				}
			}
			if len(asked) != 1 {
				t.Errorf("%d different titles were asked (%v), want only the first", len(asked), asked)
			}
		})
	}
}

// The run asks for the titles batch at a time, and a title that is still due after it was run is not
// run again within the run: it would never end.
func TestRunMDBListDueBatchesAndAsksATitleOnce(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	ids := moviesWith(t, e, 5, func(w http.ResponseWriter, path string) { fmt.Fprint(w, mdbTitle) })
	var sizes []int
	real := e.rt.due
	e.rt.due = func(ctx context.Context, n int) ([]store.RatingsDue, error) {
		sizes = append(sizes, n)
		return real(ctx, n)
	}
	if err := e.rt.RunMDBListDue(ctx, 2, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sizes, []int{2, 2, 2, 2}) {
		t.Errorf("the store was asked for %v titles, want 2 each time: three batches, then the empty one", sizes)
	}
	if got := e.rated(ids); got != 5 {
		t.Errorf("%d titles rated, want all 5", got)
	}

	// Titles that stay due whatever is done to them (here the run does nothing): each is run once, and
	// the run ends.
	f := newRatingsEnv(t)
	fids := moviesWith(t, f, 3, func(w http.ResponseWriter, path string) { fmt.Fprint(w, mdbTitle) })
	calls := map[int64]int{}
	f.rt.run = func(ctx context.Context, id int64) error { calls[id]++; return nil }
	done, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := f.rt.RunMDBListDue(done, 20, nil); err != nil {
		t.Fatalf("titles that stay due: %v", err)
	}
	for _, id := range fids {
		if calls[id] != 1 {
			t.Errorf("title %d was run %d times, want once", id, calls[id])
		}
	}
}

// before is called ahead of each title with the number done so far, and its error ends the run.
func TestRunMDBListDueCallsBefore(t *testing.T) {
	e := newRatingsEnv(t)
	moviesWith(t, e, 4, func(w http.ResponseWriter, path string) { fmt.Fprint(w, mdbTitle) })
	var seen []int
	stop := errors.New("stop")
	err := e.rt.RunMDBListDue(t.Context(), 20, func(_ context.Context, done int, tl store.RatingsDue) error {
		seen = append(seen, done)
		if tl.Title == "" {
			t.Error("the title has no name for the progress line")
		}
		if done == 2 {
			return stop
		}
		return nil
	})
	if err != stop || !slices.Equal(seen, []int{0, 1, 2}) {
		t.Errorf("err = %v, before saw %v; want it stopped at the third title", err, seen)
	}
	if got := e.rated(nil); got != 0 || len(e.mdb.requests()) != 3 { // /user and two titles
		t.Errorf("%d requests; the third title must not be asked once before stops the run", len(e.mdb.requests()))
	}
}

// ---- nothing changed: nothing is read again ----

// A dataset is read again only when the copy or what it was written for changed: with the same copy and
// the same library it is not opened (here the copy is replaced on disk by garbage, which opening would
// find out), and a change in what the mapping depends on does open it.
func TestRunIMDbDoesNotReadACopyAgainWhenNothingChanged(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	e.movie(1, "tt1000001")
	show := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "One"}, store.Episode{Season: 1, Number: 2, Name: "Two"})
	e.setEpisodes("tt3000001 tt2000002 1 1", "tt3000002 tt2000002 1 2")
	e.setDataset(map[string]string{"tt1000001": "8.7 2000", "tt3000001": "9.0 5"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.episodesOf(show); !slices.Equal(got, []string{"1=tt3000001@dataset", "2=tt3000002@dataset"}) {
		t.Fatalf("control: episodes = %v", got)
	}

	// Both copies are now garbage on disk. Nothing changed, so neither is opened.
	for _, name := range []string{IMDbEpisodesFile, IMDbRatingsFile} {
		if err := os.WriteFile(filepath.Join(e.dir, name), gzipOf(t, "garbage\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(3 * time.Hour)
	if e.rt.IMDbDue(ctx) {
		t.Error("due with nothing changed")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatalf("a run with nothing changed: %v; a copy was read", err)
	}
	if string(e.file(IMDbEpisodesFile)) != string(gzipOf(t, "garbage\n")) || string(e.file(IMDbRatingsFile)) != string(gzipOf(t, "garbage\n")) {
		t.Error("a copy was opened and discarded")
	}

	// A show with another number of episodes stored changes what the mapping was made for: the episode
	// copy is read (and found to be garbage).
	again := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 10, IMDbID: "tt2000002", Title: "Show", Source: store.SourceTMDB}}
	again.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{{Number: 1, Name: "One"}, {Number: 2, Name: "Two"}, {Number: 3, Name: "Three"}}}}
	if _, err := e.st.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Hour)
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("a show with another number of episodes does not make the job due")
	}
	if err := e.rt.RunIMDb(ctx); !errors.Is(err, ErrIMDbFormat) {
		t.Errorf("err = %v; the episode copy should have been read, and found not to be the dataset", err)
	}
}

// The episodes are written after an hour, the ratings after ten minutes: a change that only the mapping
// depends on (a show that has more episodes now) waits for the longer.
func TestIMDbDueEpisodesWaitLongerThanRatings(t *testing.T) {
	e := newRatingsEnv(t)
	ctx := t.Context()
	show := e.show("Show", store.Title{TMDBID: 10, IMDbID: "tt2000002", Source: store.SourceTMDB},
		store.Episode{Season: 1, Number: 1, Name: "One"}, store.Episode{Season: 1, Number: 2, Name: "Two"})
	e.setEpisodes("tt3000001 tt2000002 1 1", "tt3000002 tt2000002 1 2", "tt3000003 tt2000002 1 3")
	e.setDataset(map[string]string{"tt2000002": "9.0 5", "tt3000001": "9.1 5"})
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}

	// The show gets a third episode: the ratings do not depend on it (no episode id changed), the mapping does.
	again := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 10, IMDbID: "tt2000002", Title: "Show", Source: store.SourceTMDB}}
	again.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{{Number: 1, Name: "One"}, {Number: 2, Name: "Two"}, {Number: 3, Name: "Three"}}}}
	if _, err := e.st.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	e.advance(IMDbRatingsRefit + time.Minute)
	if e.rt.IMDbDue(ctx) {
		t.Error("due after the ratings' refit time for a change only the mapping depends on")
	}
	e.advance(IMDbEpisodesRefit - IMDbRatingsRefit)
	if !e.rt.IMDbDue(ctx) {
		t.Fatal("not due after the episodes' refit time")
	}
	if err := e.rt.RunIMDb(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.episodesOf(show); !slices.Equal(got, []string{"1=tt3000001@dataset", "2=tt3000002@dataset", "3=tt3000003@dataset"}) {
		t.Errorf("episodes = %v", got)
	}
}

// ---- settings ----

func TestMDBListKeyPrecedenceAndSecrecy(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name              string
		flag, env, stored string
		want              string
		source            KeySource
	}{
		{"flag wins", "FLAG", "ENV", "STORED", "FLAG", KeyFromFlag},
		{"then the environment", "", "ENV", "STORED", "ENV", KeyFromEnv},
		{"then the settings", "", "", "STORED", "STORED", KeyFromSettings},
		{"none", "", "", "", "", KeyNone},
	} {
		e := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag, so.MDBListKeyEnv = tc.flag, tc.env })
		if tc.stored != "" {
			if err := e.st.SetSetting(ctx, SettingMDBList, tc.stored); err != nil {
				t.Fatal(err)
			}
		}
		r := e.settings.Resolve(ctx)
		if r.MDBListKey() != tc.want || r.MDBListKeySource != tc.source || r.MDBListKeySet() != (tc.want != "") {
			t.Errorf("%s: key %q from %q", tc.name, r.MDBListKey(), r.MDBListKeySource)
		}
		for _, s := range []string{fmt.Sprint(r), fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r), fmt.Sprint(e.settings), fmt.Sprintf("%#v", e.settings)} {
			if tc.want != "" && strings.Contains(s, tc.want) {
				t.Errorf("%s: the key is in %q", tc.name, s)
			}
		}
	}
}

func TestSettingsUpdateMDBListKey(t *testing.T) {
	ctx := t.Context()
	good := "GOODKEY0123456789"
	e := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag = "" })
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		switch r.URL.Query().Get("apikey") {
		case good:
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
		case "DOWN":
			w.WriteHeader(502)
		default:
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error": "Invalid API key"}`)
		}
	})
	stored := func() string { return e.setting(SettingMDBList) }
	set := func(k string) error { return e.settings.Update(ctx, Update{MDBListKey: &k}) }

	if err := set("BADKEY0123456789"); !errors.Is(err, ErrMDBListRejected) || stored() != "" {
		t.Errorf("a key MDBList refuses: %v, stored %q", err, stored())
	}
	if err := set("DOWN"); !errors.Is(err, ErrMDBListUnreachable) || stored() != "" {
		t.Errorf("MDBList out of reach: %v, stored %q", err, stored())
	}
	for _, bad := range []string{"has space", "semi;colon", strings.Repeat("a", 3000), "quote\"s"} {
		if err := set(bad); !errors.Is(err, ErrInvalidSetting) {
			t.Errorf("%q: %v, want ErrInvalidSetting", bad, err)
		}
	}
	n := e.mdb.count()
	if err := set(good); err != nil || stored() != good {
		t.Fatalf("a good key: %v, stored %q", err, stored())
	}
	if e.mdb.count() != n+1 {
		t.Errorf("the key was checked with %d requests, want 1", e.mdb.count()-n)
	}
	if r := e.settings.Resolve(ctx); !r.MDBListKeySet() || r.MDBListKeySource != KeyFromSettings {
		t.Errorf("resolved = %v", r)
	}
	if err := set(""); err != nil || stored() != "" {
		t.Errorf("removal: %v, stored %q", err, stored())
	}
	if r := e.settings.Resolve(ctx); r.MDBListKeySet() {
		t.Error("the key is still set after its removal")
	}

	// With the privacy switch off nothing is sent: the key is stored as given.
	off := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag, so.Disabled = "", true })
	k := "UNCHECKED0123456"
	if err := off.settings.Update(ctx, Update{MDBListKey: &k}); err != nil || off.setting(SettingMDBList) != k || off.mdb.count() != 0 {
		t.Errorf("lookups off: %v, stored %q, %d requests", err, off.setting(SettingMDBList), off.mdb.count())
	}
}

func TestSettingsTestMDBList(t *testing.T) {
	ctx := t.Context()
	e := newRatingsEnv(t)
	if err := e.settings.Test(ctx, SourceMDBList); err != nil {
		t.Fatalf("a key that works: %v", err)
	}
	if r := e.mdb.last(); r.Path != "/user" || r.Key != mdbTestKey {
		t.Errorf("request = %+v; the key in use is tested", r)
	}
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error": "Invalid API key `+r.URL.Query().Get("apikey")+`"}`)
	})
	err := e.settings.Test(ctx, SourceMDBList)
	if !errors.Is(err, ErrMDBListRejected) {
		t.Fatalf("a key that is refused: %v", err)
	}
	if strings.Contains(err.Error(), mdbTestKey) {
		t.Errorf("the error shows the key: %v", err)
	}
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(503) })
	if err := e.settings.Test(ctx, SourceMDBList); !errors.Is(err, ErrMDBListUnreachable) {
		t.Errorf("MDBList out of reach: %v", err)
	}
	// A refusal that was remembered is over when the key works.
	e.settings.NoteMDBList(&Error{Status: 401})
	if !e.settings.MDBListRefused() {
		t.Fatal("control: not refused")
	}
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprint(w, `{}`) })
	if err := e.settings.Test(ctx, SourceMDBList); err != nil || e.settings.MDBListRefused() {
		t.Errorf("a key that works: %v, refused %v", err, e.settings.MDBListRefused())
	}

	none := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag = "" })
	if err := none.settings.Test(ctx, SourceMDBList); !errors.Is(err, ErrNoMDBList) || none.mdb.count() != 0 {
		t.Errorf("no key: %v", err)
	}
	off := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.Disabled = true })
	if err := off.settings.Test(ctx, SourceMDBList); !errors.Is(err, ErrLookupsOff) || off.mdb.count() != 0 {
		t.Errorf("lookups off: %v, %d requests", err, off.mdb.count())
	}
	if err := e.settings.Test(ctx, "omdb"); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("another provider: %v", err)
	}
}

func TestRatingsPreferences(t *testing.T) {
	ctx := t.Context()
	e := newRatingsEnv(t)
	primary, shown := e.settings.RatingsPrefs(ctx)
	if primary != "imdb" || !slices.Equal(shown, store.RatingSources) {
		t.Fatalf("defaults = %q, %v", primary, shown)
	}
	p := "rt_critics"
	list := []string{"metacritic", "imdb", "tmdb"}
	if err := e.settings.Update(ctx, Update{RatingsPrimary: &p, RatingsShown: &list}); err != nil {
		t.Fatal(err)
	}
	primary, shown = e.settings.RatingsPrefs(ctx)
	if primary != "rt_critics" || !slices.Equal(shown, list) {
		t.Errorf("after the update = %q, %v", primary, shown)
	}
	// An empty list is a list: nothing is shown (also when it is a nil slice: never null).
	none := []string{}
	if err := e.settings.Update(ctx, Update{RatingsShown: &none}); err != nil {
		t.Fatal(err)
	}
	if _, shown := e.settings.RatingsPrefs(ctx); shown == nil || len(shown) != 0 {
		t.Errorf("an empty list = %#v", shown)
	}
	if err := e.settings.Update(ctx, Update{RatingsShown: &list}); err != nil {
		t.Fatal(err)
	}
	var nilList []string
	if err := e.settings.Update(ctx, Update{RatingsShown: &nilList}); err != nil {
		t.Fatal(err)
	}
	if _, shown := e.settings.RatingsPrefs(ctx); shown == nil || len(shown) != 0 {
		t.Errorf("a nil list = %#v, want an empty one", shown)
	}

	for name, u := range map[string]Update{
		"an unknown primary":        {RatingsPrimary: ptrTo("omdb")},
		"the audience score":        {RatingsPrimary: ptrTo("rt_audience")}, // it cannot be the primary
		"an empty primary":          {RatingsPrimary: ptrTo("")},
		"an unknown source":         {RatingsShown: &[]string{"imdb", "omdb"}},
		"a source twice":            {RatingsShown: &[]string{"imdb", "imdb"}},
		"a bad list beside a good":  {RatingsPrimary: ptrTo("imdb"), RatingsShown: &[]string{"x"}},
		"capitals are not a source": {RatingsShown: &[]string{"IMDb"}},
	} {
		if err := e.settings.Update(ctx, u); !errors.Is(err, ErrInvalidSetting) {
			t.Errorf("%s: %v, want ErrInvalidSetting", name, err)
		}
	}
	// Nothing of a refused update was stored (the good primary beside the bad list included).
	if primary, shown := e.settings.RatingsPrefs(ctx); primary != "rt_critics" || len(shown) != 0 {
		t.Errorf("after the refusals = %q, %v; they changed something", primary, shown)
	}
	for _, src := range RatingPrimaries { // every primary is accepted
		if err := e.settings.Update(ctx, Update{RatingsPrimary: &src}); err != nil {
			t.Errorf("primary %s: %v", src, err)
		}
	}

	// A stored value that is not valid reads as the default.
	e.st.SetSetting(ctx, SettingRatingsPrimary, "rt_audience")
	e.st.SetSetting(ctx, SettingRatingsShown, `["imdb","imdb"]`)
	if primary, shown := e.settings.RatingsPrefs(ctx); primary != "imdb" || !slices.Equal(shown, store.RatingSources) {
		t.Errorf("bad stored values = %q, %v", primary, shown)
	}
	e.st.SetSetting(ctx, SettingRatingsShown, `not json`)
	if _, shown := e.settings.RatingsPrefs(ctx); !slices.Equal(shown, store.RatingSources) {
		t.Errorf("unreadable stored list = %v", shown)
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestRatingsAvailable(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		mod  func(*SettingsOptions)
		want []string
	}{
		{"nothing but IMDb's dataset", func(so *SettingsOptions) { so.MDBListKeyFlag = "" }, []string{"imdb"}},
		{"a TMDB key", func(so *SettingsOptions) { so.MDBListKeyFlag, so.KeyFlag = "", "k" }, []string{"imdb", "tmdb"}},
		{"an MDBList key", func(so *SettingsOptions) {}, []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt"}},
		{"both", func(so *SettingsOptions) { so.KeyFlag = "k" }, []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt", "tmdb"}},
		{"the privacy switch off", func(so *SettingsOptions) { so.KeyFlag, so.Disabled = "k", true }, []string{}},
	} {
		e := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { tc.mod(so) })
		got := e.settings.RatingsAvailable(ctx)
		if got == nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %#v, want %v", tc.name, got, tc.want)
		}
	}
}
