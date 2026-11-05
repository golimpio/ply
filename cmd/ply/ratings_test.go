package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/metadata"
	"ply/internal/store"
	"ply/internal/tier"
)

// ratingsRig is the ratings schedule and its kinds over a real store, with the dataset and MDBList
// on servers of the test's.
type ratingsRig struct {
	t      *testing.T
	st     *store.Store
	dbPath string
	rt     *metadata.Ratings
	q      *jobs.Queue
	imdb   *httptest.Server
	hits   atomic.Int32 // requests to the dataset
	mdb    atomic.Int32 // requests to MDBList
	mdbBad atomic.Int32 // when set, the status MDBList answers everything with
	movies int

	imdbBody atomic.Pointer[[]byte]                                        // what the dataset server gives
	mdbOver  atomic.Pointer[func(http.ResponseWriter, *http.Request) bool] // when it returns true, it answered
}

func newRatingsRig(t *testing.T, so func(*metadata.SettingsOptions), pace func(*testing.T) func(context.Context) error) *ratingsRig {
	t.Helper()
	r := &ratingsRig{t: t, dbPath: filepath.Join(t.TempDir(), "My Data", "ply.db")}
	var err error
	if r.st, err = store.Open(r.dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.st.Close() })

	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	io.WriteString(zw, "tconst\taverageRating\tnumVotes\n")
	for i := range 600 {
		fmt.Fprintf(zw, "tt%07d\t7.5\t100\n", 1000+i)
	}
	zw.Close()
	dataset := body.Bytes()
	r.imdbBody.Store(&dataset)
	r.imdb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		w.Header().Set("Last-Modified", "Wed, 04 Nov 2026 03:00:00 GMT")
		w.Write(*r.imdbBody.Load())
	}))
	t.Cleanup(r.imdb.Close)
	mdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mdb.Add(1)
		if f := r.mdbOver.Load(); f != nil && (*f)(w, req) {
			return
		}
		if st := int(r.mdbBad.Load()); st != 0 {
			w.WriteHeader(st)
			fmt.Fprint(w, `{"error": "Invalid API key"}`)
			return
		}
		if req.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 0}`)
			return
		}
		fmt.Fprint(w, `{"ratings": [{"source": "metacritic", "value": 70}]}`)
	}))
	t.Cleanup(mdb.Close)

	opts := metadata.SettingsOptions{Store: r.st, MDBListKeyFlag: "rig-mdblist-key", MDBListBaseURL: mdb.URL, Logger: slog.New(slog.DiscardHandler)}
	if so != nil {
		so(&opts)
	}
	settings := metadata.NewSettings(opts)
	ropts := metadata.RatingsOptions{Store: r.st, Settings: settings, Dir: filepath.Join(t.TempDir(), "imdb"), IMDbURL: r.imdb.URL,
		Logger: slog.New(slog.DiscardHandler)}
	if pace != nil {
		ropts.Pace = pace(t)
	}
	r.rt = metadata.NewRatings(ropts)
	return r
}

// addMovies puts n movies with files, each with an IMDb id the dataset knows, in the library.
func (r *ratingsRig) addMovies(n int) {
	r.t.Helper()
	ctx := context.Background()
	for range n {
		r.movies++
		name := fmt.Sprintf("Movie %d", r.movies)
		itemID, err := r.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: name, Path: fmt.Sprintf("/media/%d.mkv", r.movies), Root: "/media",
			Size: 1, MTime: 1, Container: "mkv"}, nil)
		if err != nil {
			r.t.Fatal(err)
		}
		id, err := r.st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: int64(r.movies), IMDbID: fmt.Sprintf("tt%07d", 1000+r.movies-1),
			Title: name, Source: store.SourceTMDB}})
		if err != nil {
			r.t.Fatal(err)
		}
		if err := r.st.SetItemMatch(ctx, itemID, store.Match{TitleID: id, State: store.MatchMatched, Score: 1}); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *ratingsRig) count(table string) int {
	r.t.Helper()
	db, err := sql.Open("sqlite", "file:"+r.dbPath+"?mode=ro")
	if err != nil {
		r.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// queue builds the queue with the ratings kinds (and nothing else), not running.
func (r *ratingsRig) queue(gate jobs.Gate) *jobs.Queue {
	r.q = jobs.New(jobs.Options{Gate: gate, Logger: slog.New(slog.DiscardHandler)})
	registerRatings(r.q, queueDeps{ratings: r.rt})
	return r.q
}

// queued lists what is waiting, by kind and key.
func (r *ratingsRig) queued() map[string]jobs.Job {
	out := map[string]jobs.Job{}
	for _, j := range r.q.Snapshot().Queued {
		out[j.Kind+"/"+j.Key] = j
	}
	return out
}

func startSchedule(t *testing.T, r *ratingsRig) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scheduleRatings(ctx, r.q, r.rt, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
	return cancel
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The ratings kinds are declared as the queue's refresh work, with the keys that merge repeats, and
// the schedule queues them when they fall due: one job for the IMDb datasets, and one for every title
// MDBList is to be asked about, not one each.
func TestRatingsKindsAndSchedule(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(3)
	r.queue(nil)
	startSchedule(t, r)

	waitUntil(t, "the IMDb job and the titles' job are queued", 5*time.Second, func() bool { return len(r.queued()) == 2 })
	got := r.queued()
	if j, ok := got["ratings.imdb/imdb"]; !ok || j.Priority != jobs.PriorityRefresh || j.Title != "Fetch IMDb ratings" || j.Held != "" {
		t.Errorf("the IMDb job = %+v, %v", j, ok)
	}
	if j, ok := got["ratings.mdblist/mdblist"]; !ok || j.Priority != jobs.PriorityRefresh || j.Title != "Fetch ratings from MDBList" || j.Held != "" {
		t.Errorf("the MDBList job = %+v, %v (one job for the three titles)", j, ok)
	}
	// Nothing in the schedule asks anyone: it only queues.
	if r.hits.Load() != 0 || r.mdb.Load() != 0 {
		t.Errorf("the schedule made %d + %d requests", r.hits.Load(), r.mdb.Load())
	}

	// Waking again merges into what is queued, not twice.
	for range 3 {
		r.rt.Wake()
		time.Sleep(20 * time.Millisecond)
	}
	if len(r.queued()) != 2 {
		t.Errorf("after waking again: %d queued, want 2", len(r.queued()))
	}
}

// While a job for the titles is queued or running (here a job of its own, with another key), the
// schedule does not queue a second one.
func TestRatingsScheduleDoesNotQueueTheTitlesJobWhileOneIsBusy(t *testing.T) {
	count := func(r *ratingsRig) (n int) {
		for _, j := range r.queued() {
			if j.Kind == kindRatingsMDBList {
				n++
			}
		}
		return n
	}

	// Control: with none busy, the schedule queues its own.
	free := newRatingsRig(t, nil, nil)
	free.addMovies(2)
	free.queue(nil)
	startSchedule(t, free)
	waitUntil(t, "the titles' job to be queued", 5*time.Second, func() bool { return count(free) == 1 })

	r := newRatingsRig(t, nil, nil)
	r.addMovies(2)
	r.queue(nil)
	if _, err := r.q.Enqueue(t.Context(), kindRatingsMDBList, jobs.Opt{Key: "by hand"}); err != nil {
		t.Fatal(err)
	}
	startSchedule(t, r)
	waitUntil(t, "the IMDb job to be queued", 5*time.Second, func() bool { _, ok := r.queued()["ratings.imdb/imdb"]; return ok })
	for range 3 {
		r.rt.Wake()
		time.Sleep(20 * time.Millisecond)
	}
	if n := count(r); n != 1 {
		t.Errorf("%d jobs for the titles queued, want only the one that was busy", n)
	}
}

// With the privacy switch off, or no key, the schedule queues nothing that would ask: no IMDb job, no
// MDBList jobs. The controls are the same rig with them on.
func TestRatingsScheduleQueuesNothingWithLookupsOff(t *testing.T) {
	for name, tc := range map[string]struct {
		so           func(*metadata.SettingsOptions)
		imdb, titles bool
	}{
		"control":     {nil, true, true},
		"lookups off": {func(o *metadata.SettingsOptions) { o.Disabled = true }, false, false},
		"no MDBList":  {func(o *metadata.SettingsOptions) { o.MDBListKeyFlag = "" }, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRatingsRig(t, tc.so, nil)
			r.addMovies(2)
			r.queue(nil)
			startSchedule(t, r)
			r.rt.Wake()
			if tc.imdb || tc.titles {
				waitUntil(t, "something to be queued", 5*time.Second, func() bool { return len(r.queued()) > 0 })
			}
			time.Sleep(150 * time.Millisecond) // long enough for the first look and a wake
			_, hasIMDb := r.queued()["ratings.imdb/imdb"]
			titles := 0
			for _, j := range r.queued() {
				if j.Kind == kindRatingsMDBList {
					titles++
				}
			}
			if hasIMDb != tc.imdb || (titles > 0) != tc.titles {
				t.Errorf("queued: IMDb job %v, %d title jobs; want %v and %v", hasIMDb, titles, tc.imdb, tc.titles)
			}
		})
	}
}

// A nudge (the metadata pass ended) makes the schedule look once nudgeSettle after the first of a run of
// them, however many come.
func TestRatingsScheduleLooksAfterANudge(t *testing.T) {
	old := nudgeSettle
	nudgeSettle = 80 * time.Millisecond
	t.Cleanup(func() { nudgeSettle = old })

	r := newRatingsRig(t, nil, nil)
	r.queue(nil)
	startSchedule(t, r)
	time.Sleep(100 * time.Millisecond) // the first look: an empty library
	if len(r.queued()) != 0 {
		t.Fatalf("control: %d queued with an empty library", len(r.queued()))
	}
	r.addMovies(2) // matched since the first look
	start := time.Now()
	// Nudges keep coming (the metadata pass runs on and on through a big first scan): the look is still
	// nudgeSettle after the first, not after the last.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			r.rt.Nudge()
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if len(r.queued()) != 0 {
		t.Errorf("%d queued straight after a nudge; the look is nudgeSettle later", len(r.queued()))
	}
	waitUntil(t, "the look while the nudges go on", 3*time.Second, func() bool { return len(r.queued()) == 2 })
	took := time.Since(start)
	close(stop)
	<-done
	if took < 60*time.Millisecond || took > 400*time.Millisecond {
		t.Errorf("the look came after %v, want about %v, with the nudges still coming", took, nudgeSettle)
	}
}

// A stalled viewer holds the IMDb job between chunks: the first chunk is written, and the rest waits
// until the viewer is served. (The queue starts the job; the pace is the one main gives the service.)
func TestRatingsIMDbJobYieldsToAStalledViewer(t *testing.T) {
	pb := &stalled{}
	gov := tier.New(tier.Options{Playback: pb, Poll: 5 * time.Millisecond})
	var once sync.Once
	var r *ratingsRig
	r = newRatingsRig(t, nil, func(*testing.T) func(context.Context) error {
		pace := paceFor(gov, tier.Need{CPU: true})
		return func(ctx context.Context) error {
			once.Do(func() { pb.set(1) }) // a viewer stalls as the first chunk is written
			return pace(ctx)
		}
	})
	r.addMovies(520)
	q := r.queue(gov)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	if _, err := q.Enqueue(ctx, kindRatingsIMDb, jobs.Opt{Key: "imdb"}); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, "the first chunk to be written", 20*time.Second, func() bool { return r.count("title_ratings") >= 500 })
	time.Sleep(250 * time.Millisecond)
	if n := r.count("title_ratings"); n != 500 {
		t.Fatalf("%d ratings written while a viewer is stalled, want only the first chunk (500)", n)
	}
	var held jobs.Job
	for _, j := range q.Snapshot().Running {
		if j.Kind == kindRatingsIMDb {
			held = j
		}
	}
	if held.State != jobs.Running || held.Held == "" {
		t.Errorf("the job = %+v; it should be running and say why it waits", held)
	}

	pb.set(0)
	waitUntil(t, "the rest to be written once the viewer is served", 20*time.Second, func() bool { return r.count("title_ratings") == 520 })
	if r.hits.Load() != 1 {
		t.Errorf("%d requests to the dataset", r.hits.Load())
	}
}

// A viewer that stalls while the titles' job is running holds it before the next title: the one it was
// on is finished, and nothing more is asked of MDBList until the viewer is served.
func TestRatingsMDBListJobYieldsToAStalledViewer(t *testing.T) {
	pb := &stalled{}
	gov := tier.New(tier.Options{Playback: pb, Poll: 5 * time.Millisecond})
	r := newRatingsRig(t, nil, nil)
	r.addMovies(3)
	var once sync.Once
	r.mdbOver.Store(ptr(func(w http.ResponseWriter, req *http.Request) bool {
		if strings.HasPrefix(req.URL.Path, "/tmdb/movie/") {
			once.Do(func() { pb.set(1) }) // a viewer stalls as the first title is asked for
		}
		return false
	}))
	q, ctx := runQueue(t, r, gov)
	if _, err := q.Enqueue(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first title to be rated", 10*time.Second, func() bool { return r.count("title_ratings") == 1 })
	waitUntil(t, "the job to be held", 10*time.Second, func() bool {
		for _, j := range q.Snapshot().Running {
			if j.Kind == kindRatingsMDBList && j.Held != "" {
				return true
			}
		}
		return false
	})
	time.Sleep(200 * time.Millisecond)
	if n, titles := r.mdb.Load(), r.count("title_ratings"); n != 2 || titles != 1 {
		t.Fatalf("%d requests to MDBList and %d titles rated while a viewer is stalled; want the allowance and the first title", n, titles)
	}
	pb.set(0)
	waitUntil(t, "the titles to be rated once the viewer is served", 20*time.Second, func() bool { return r.count("title_ratings") == 3 })
}

// A key MDBList refuses ends the job for good: it is not queued again to try with the same key.
func TestRatingsRefusedKeyIsFinal(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(1)
	r.mdbBad.Store(401)
	q := r.queue(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	if _, err := q.Enqueue(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the job to fail", 10*time.Second, func() bool {
		for _, j := range q.Snapshot().History {
			if j.Kind == kindRatingsMDBList && j.State == jobs.Failed {
				return true
			}
		}
		return false
	})
	for _, j := range q.Snapshot().History {
		if j.Kind == kindRatingsMDBList && (j.Attempts != 1 || j.Error == "") {
			t.Errorf("the job = %+v; one attempt, and an error that says why", j)
		}
	}
	if len(r.queued()) != 0 {
		t.Errorf("still queued: %v", r.queued())
	}
	if n := r.mdb.Load(); n != 1 {
		t.Errorf("MDBList was asked %d times, want 1", n)
	}
}

// runQueue starts the queue and returns it ready, and its context.
func runQueue(t *testing.T, r *ratingsRig, gate jobs.Gate) (*jobs.Queue, context.Context) {
	t.Helper()
	q := r.queue(gate)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	return q, ctx
}

// The job for the titles goes through every title that is due, a batch at a time (more than one
// batch here).
func TestRatingsMDBListJobRatesEveryDueTitle(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	const n = 3*mdblistBatch + 5
	r.addMovies(n)
	q, ctx := runQueue(t, r, nil)

	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if got := r.count("title_ratings"); got != n {
		t.Errorf("%d titles rated, want all %d", got, n)
	}
	if got := r.mdb.Load(); got != n+1 {
		t.Errorf("%d requests, want %d: the allowance once, and one for each title", got, n+1)
	}
	// Nothing is due now: another run asks nothing.
	before := r.mdb.Load()
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil || r.mdb.Load() != before {
		t.Errorf("a run with nothing due: %v, %d requests", err, r.mdb.Load()-before)
	}
}

// The job ends when the day's budget is spent, with the rest due; it does not go round again for them.
func TestRatingsMDBListJobStopsWhenTheBudgetIsSpent(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(40)
	r.mdbOver.Store(ptr(func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == "/user" {
			fmt.Fprint(w, `{"api_requests": 30, "api_requests_count": 0}`) // 27 for ply
			return true
		}
		return false
	}))
	q, ctx := runQueue(t, r, nil)
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if got := r.count("title_ratings"); got != 27 {
		t.Errorf("%d titles rated, want the 27 of the budget", got)
	}
	if got := r.mdb.Load(); got != 28 {
		t.Errorf("%d requests, want 28 (the allowance and 27 titles)", got)
	}
}

// A title MDBList will not rate does not hold up the rest of the run, which is not an error.
func TestRatingsMDBListJobGoesOnPastARefusedTitle(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(5)
	r.mdbOver.Store(ptr(func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == "/tmdb/movie/2" {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"error": "cannot process"}`)
			return true
		}
		return false
	}))
	q, ctx := runQueue(t, r, nil)
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil {
		t.Fatalf("the run: %v", err)
	}
	if got := r.count("title_ratings"); got != 4 {
		t.Errorf("%d titles rated, want the four that MDBList would rate", got)
	}
	before := r.mdb.Load()
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil || r.mdb.Load() != before {
		t.Errorf("the refused title is asked again at once: %v, %d requests", err, r.mdb.Load()-before)
	}
}

// A title MDBList answers with a server error does not fail the job or hold up the rest: the job ends
// well, and the title is given up on when it fails again in the next one.
func TestRatingsMDBListJobGoesOnPastATitleThatFails(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(5)
	r.mdbOver.Store(ptr(func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == "/tmdb/movie/2" {
			w.WriteHeader(500)
			return true
		}
		return false
	}))
	q, ctx := runQueue(t, r, nil)
	for run := 1; run <= 2; run++ {
		if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if got := r.count("title_ratings"); got != 4 {
			t.Errorf("run %d: %d titles rated, want the four behind the failing one", run, got)
		}
	}
	before := r.mdb.Load()
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil || r.mdb.Load() != before {
		t.Errorf("a third run: %v, %d requests; the failing title was given up on in the second", err, r.mdb.Load()-before)
	}
}

func ptr[T any](v T) *T { return &v }

// A file that is not IMDb's dataset, or is far less of it, is a final failure of the job (asking again
// at once gets the same file), and nothing is stored from it.
func TestRatingsIMDbJobFailsForGoodOnADatasetThatIsNotOne(t *testing.T) {
	for name, body := range map[string]string{
		"another header": "id\trating\n1\t5\n",
		"no rows":        "tconst\taverageRating\tnumVotes\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRatingsRig(t, nil, nil)
			r.addMovies(2)
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			io.WriteString(zw, body)
			zw.Close()
			bad := buf.Bytes()
			r.imdbBody.Store(&bad)
			q, ctx := runQueue(t, r, nil)
			if _, err := q.Do(ctx, kindRatingsIMDb, jobs.Opt{Key: keyRatingsIMDb, Attempts: 3}); err == nil {
				t.Fatal("the job succeeded")
			}
			for _, j := range q.Snapshot().History {
				if j.Kind == kindRatingsIMDb && j.Attempts != 1 {
					t.Errorf("%d attempts, want 1: the failure is final", j.Attempts)
				}
			}
			if r.count("title_ratings") != 0 || r.hits.Load() != 1 {
				t.Errorf("%d ratings stored, %d requests", r.count("title_ratings"), r.hits.Load())
			}
		})
	}
}

// The jobs run as the queue runs them: the IMDb job rates the library, the titles' job asks MDBList,
// and a refused key is final (it is not retried).
func TestRatingsJobsRun(t *testing.T) {
	r := newRatingsRig(t, nil, nil)
	r.addMovies(2)
	q := r.queue(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()

	if _, err := q.Do(ctx, kindRatingsIMDb, jobs.Opt{Key: "imdb", Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if n := r.count("title_ratings"); n != 2 {
		t.Errorf("%d ratings after the IMDb job", n)
	}
	if _, err := q.Do(ctx, kindRatingsMDBList, jobs.Opt{Key: keyRatingsMDBList, Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	if n := r.count("title_ratings"); n != 4 {
		t.Errorf("%d ratings after the MDBList job; each of the two titles gains Metacritic's", n)
	}
	var titles int
	db, err := sql.Open("sqlite", "file:"+r.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT COUNT(DISTINCT title_id) FROM title_ratings WHERE source = 'metacritic'`).Scan(&titles); err != nil || titles != 2 {
		t.Errorf("Metacritic's rating went to %d titles (%v), want both", titles, err)
	}
}
