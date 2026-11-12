package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/store"
)

// The key every test here uses. It is made up; the tests look for it in what comes out.
const mdbTestKey = "MDB-test-key-0123456789abcdef"

// mdbTitle is the answer for The Matrix in the shape MDBList's clients read it (the values are
// invented): every source MDBList has, the ones ply does not take among them.
const mdbTitle = `{
  "title": "The Matrix", "year": 1999, "type": "movie",
  "ids": {"imdb": "tt0133093", "tmdb": 603, "trakt": 481},
  "ratings": [
    {"source": "imdb", "value": 8.7, "score": 87, "votes": 2100000, "url": "https://www.imdb.com/title/tt0133093"},
    {"source": "tmdb", "value": 82, "score": 82, "votes": 25000},
    {"source": "metacritic", "value": 73, "score": 73, "votes": 35, "url": "https://www.metacritic.com/movie/the-matrix"},
    {"source": "metacriticuser", "value": 8.1, "score": 81, "votes": 3000},
    {"source": "trakt", "value": 86, "score": 86, "votes": 70000, "url": "https://trakt.tv/movies/the-matrix-1999"},
    {"source": "tomatoes", "value": 83, "score": 83, "votes": 150, "url": "https://www.rottentomatoes.com/m/matrix"},
    {"source": "tomatoesaudience", "value": 85, "score": 85, "votes": 250000, "url": "https://www.rottentomatoes.com/m/matrix"},
    {"source": "letterboxd", "value": 4.1, "score": 82, "votes": 1500000, "url": "https://letterboxd.com/film/the-matrix/"},
    {"source": "myanimelist", "value": null, "score": null, "votes": null}
  ]
}`

type mdbRequest struct {
	Path, Query, Key, Agent string
}

// mdbServer is a fake MDBList that records requests and answers as handler says.
type mdbServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []mdbRequest
}

func newMDBServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *mdbServer {
	t.Helper()
	m := &mdbServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.reqs = append(m.reqs, mdbRequest{r.URL.Path, r.URL.RawQuery, r.URL.Query().Get("apikey"), r.UserAgent()})
		n := len(m.reqs)
		m.mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(m.Close)
	return m
}

// requests is a copy of what the server has been asked, in order.
func (m *mdbServer) requests() []mdbRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

func (m *mdbServer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

func (m *mdbServer) last() mdbRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[len(m.reqs)-1]
}

func jsonReply(status int, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

// quickMDB is a client for srv that does not wait: its clock is fixed and its sleeps are recorded.
func quickMDB(t *testing.T, srv *mdbServer, mod ...func(*MDBListOptions)) (*MDBListClient, *[]time.Duration) {
	t.Helper()
	return quickMDBAt(t, srv, time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC), mod...)
}

// quickMDBAt is quickMDB with its clock stood at now.
func quickMDBAt(t *testing.T, srv *mdbServer, now time.Time, mod ...func(*MDBListOptions)) (*MDBListClient, *[]time.Duration) {
	t.Helper()
	o := MDBListOptions{Key: mdbTestKey, BaseURL: srv.URL, Rate: 1000, Logger: slog.New(slog.DiscardHandler)}
	for _, f := range mod {
		f(&o)
	}
	c := NewMDBListClient(o)
	// The clock stands still, and a sleep moves it on by what it was asked: a pause that is waited out ends.
	var mu sync.Mutex
	var slept []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		now = now.Add(d)
		mu.Unlock()
		return ctx.Err()
	}
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return c, &slept
}

func TestMDBListAskedByTMDBByIMDbAndByTVDB(t *testing.T) {
	srv := newMDBServer(t, jsonReply(200, mdbTitle))
	c, _ := quickMDB(t, srv)

	got, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if err != nil || got.Title != "The Matrix" || len(got.Ratings) != 9 {
		t.Fatalf("ByTMDB = %+v, %v", got, err)
	}
	if r := srv.last(); r.Path != "/tmdb/movie/603" || r.Key != mdbTestKey || r.Agent != "ply" {
		t.Errorf("request = %+v", r)
	}
	if _, err := c.ByTMDB(t.Context(), MDBListShow, 1396); err != nil {
		t.Fatal(err)
	}
	if r := srv.last(); r.Path != "/tmdb/show/1396" {
		t.Errorf("request = %+v", r)
	}
	if _, err := c.ByIMDb(t.Context(), MDBListMovie, "tt0133093"); err != nil {
		t.Fatal(err)
	}
	if r := srv.last(); r.Path != "/imdb/movie/tt0133093" {
		t.Errorf("request = %+v", r)
	}
	if _, err := c.ByTVDB(t.Context(), MDBListShow, 81189); err != nil {
		t.Fatal(err)
	}
	if r := srv.last(); r.Path != "/tvdb/show/81189" {
		t.Errorf("request = %+v", r)
	}

	// What is not an id is not asked: the id goes in the path.
	n := srv.count()
	for _, bad := range []string{"", "0133093", "tt", "tt1/../../user", "tt1?x=1", "TT1", "tt1 ", "tt1234567890123"} {
		if _, err := c.ByIMDb(t.Context(), MDBListMovie, bad); err == nil {
			t.Errorf("%q was asked", bad)
		}
	}
	if _, err := c.ByTMDB(t.Context(), "episode", 1); err == nil {
		t.Error("a media type of episode was asked")
	}
	if _, err := c.ByTMDB(t.Context(), MDBListMovie, 0); err == nil {
		t.Error("id 0 was asked")
	}
	if _, err := c.ByTVDB(t.Context(), "movie/../user", 1); err == nil {
		t.Error("a media type with a path in it was asked")
	}
	if _, err := c.ByTVDB(t.Context(), MDBListShow, 0); err == nil {
		t.Error("TVDB id 0 was asked")
	}
	if _, err := c.ByIMDb(t.Context(), "movie/../user", "tt1"); err == nil {
		t.Error("a media type with a path in it was asked")
	}
	if srv.count() != n {
		t.Errorf("%d requests were made for things that are not ids", srv.count()-n)
	}
}

// The mapping: every source MDBList gives and ply takes, with its scale and its state; the ones ply
// does not take (IMDb and TMDB have sources of their own, the rest are not shown) are left out.
func TestMDBListMapsEverySource(t *testing.T) {
	var title MDBListTitle
	if err := json.Unmarshal([]byte(mdbTitle), &title); err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Rating{}
	for _, r := range title.StoreRatings(1234) {
		if _, twice := got[r.Source]; twice {
			t.Errorf("%s twice", r.Source)
		}
		got[r.Source] = r
	}
	want := map[string]store.Rating{
		store.RatingRTCritics:  {Source: "rt_critics", Value: 83, Scale: 100, Votes: 150, State: "fresh", URL: "https://www.rottentomatoes.com/m/matrix", FetchedAt: 1234},
		store.RatingRTAudience: {Source: "rt_audience", Value: 85, Scale: 100, Votes: 250000, State: "upright", URL: "https://www.rottentomatoes.com/m/matrix", FetchedAt: 1234},
		store.RatingMetacritic: {Source: "metacritic", Value: 73, Scale: 100, Votes: 35, URL: "https://www.metacritic.com/movie/the-matrix", FetchedAt: 1234},
		store.RatingLetterboxd: {Source: "letterboxd", Value: 4.1, Scale: 5, Votes: 1500000, URL: "https://letterboxd.com/film/the-matrix/", FetchedAt: 1234},
		store.RatingTrakt:      {Source: "trakt", Value: 86, Scale: 100, Votes: 70000, URL: "https://trakt.tv/movies/the-matrix-1999", FetchedAt: 1234},
	}
	if len(got) != len(want) {
		t.Errorf("sources = %d (%v), want %d: IMDb, TMDB, Metacritic's users and MyAnimeList are not taken", len(got), got, len(want))
	}
	for src, w := range want {
		if got[src] != w {
			t.Errorf("%s = %+v, want %+v", src, got[src], w)
		}
		if !store.ValidRatingSource(src) {
			t.Errorf("%s is not a source", src)
		}
	}
}

func TestMDBListRatingStates(t *testing.T) {
	rt := func(source string, value float64, state string) string {
		return fmt.Sprintf(`{"source": %q, "value": %v, "state": %q}`, source, value, state)
	}
	for _, tc := range []struct {
		name, entry, source, state string
	}{
		{"fresh at 60", rt("tomatoes", 60, ""), "rt_critics", "fresh"},
		{"rotten at 59", rt("tomatoes", 59, ""), "rt_critics", "rotten"},
		{"fresh at 100", rt("tomatoes", 100, ""), "rt_critics", "fresh"},
		{"certified, as given", rt("tomatoes", 93, "Certified Fresh"), "rt_critics", "certified"},
		{"certified, one word", rt("tomatoes", 93, "certified"), "rt_critics", "certified"},
		{"rotten, as given", rt("tomatoes", 80, "rotten"), "rt_critics", "rotten"},
		{"a word we do not know is the score's", rt("tomatoes", 40, "splat"), "rt_critics", "rotten"},
		{"upright at 60", rt("tomatoesaudience", 60, ""), "rt_audience", "upright"},
		{"spilled at 59", rt("tomatoesaudience", 59, ""), "rt_audience", "spilled"},
		{"verified hot, with a space", rt("tomatoesaudience", 93, "Verified Hot"), "rt_audience", "verified_hot"},
		{"verified hot, snake", rt("tomatoesaudience", 93, "verified_hot"), "rt_audience", "verified_hot"},
		{"popcorn is the audience", rt("popcorn", 30, ""), "rt_audience", "spilled"},
		{"the critics' words do not fit the audience", rt("tomatoesaudience", 30, "fresh"), "rt_audience", "spilled"},
	} {
		var title MDBListTitle
		if err := json.Unmarshal([]byte(`{"ratings": [`+tc.entry+`]}`), &title); err != nil {
			t.Fatal(err)
		}
		got := title.StoreRatings(1)
		if len(got) != 1 || got[0].Source != tc.source || got[0].State != tc.state {
			t.Errorf("%s: %+v, want %s %s", tc.name, got, tc.source, tc.state)
		}
	}
	// Only Rotten Tomatoes has a state.
	var title MDBListTitle
	json.Unmarshal([]byte(`{"ratings": [{"source": "metacritic", "value": 70, "state": "fresh"}, {"source": "trakt", "value": 70, "status": "certified"}]}`), &title)
	for _, r := range title.StoreRatings(1) {
		if r.State != "" {
			t.Errorf("%s has state %q", r.Source, r.State)
		}
	}
}

func TestMDBListRatingValues(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		want        float64 // 0: left out
	}{
		{"a string number", `{"source": "metacritic", "value": "73"}`, 73},
		{"null", `{"source": "metacritic", "value": null}`, 0},
		{"not available", `{"source": "metacritic", "value": "N/A"}`, 0},
		{"missing", `{"source": "metacritic"}`, 0},
		{"zero", `{"source": "metacritic", "value": 0}`, 0},
		{"negative", `{"source": "metacritic", "value": -5}`, 0},
		{"above its scale", `{"source": "metacritic", "value": 101}`, 0},
		{"at its scale", `{"source": "metacritic", "value": 100}`, 100},
		{"letterboxd /5", `{"source": "letterboxd", "value": 4.1}`, 4.1},
		{"letterboxd given /10 is halved", `{"source": "letterboxd", "value": 8.2}`, 4.1},
		{"letterboxd above 10", `{"source": "letterboxd", "value": 11}`, 0},
		// Out of the scale we know: MDBList's own 0-100 reading is used when there is one.
		{"above its scale, with a score", `{"source": "metacritic", "value": 101, "score": 80}`, 80},
		{"letterboxd above 10, with a score", `{"source": "letterboxd", "value": 11, "score": 84}`, 4.2},
		{"above its scale, a score that is a string", `{"source": "trakt", "value": 250, "score": "60"}`, 60},
		{"above its scale, a score that is none", `{"source": "metacritic", "value": 101, "score": 0}`, 0},
		{"above its scale, a score above 100", `{"source": "metacritic", "value": 101, "score": 130}`, 0},
		{"above its scale, no score", `{"source": "metacritic", "value": 101}`, 0},
		{"within its scale the score is not used", `{"source": "metacritic", "value": 73, "score": 20}`, 73},
		{"zero with a score is still none", `{"source": "metacritic", "value": 0, "score": 70}`, 0},
		{"letterboxd 5 is 5", `{"source": "letterboxd", "value": 5}`, 5},
		{"a source in capitals", `{"source": "Trakt", "value": 80}`, 80},
	} {
		var title MDBListTitle
		if err := json.Unmarshal([]byte(`{"ratings": [`+tc.entry+`]}`), &title); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := title.StoreRatings(1)
		switch {
		case tc.want == 0 && len(got) != 0:
			t.Errorf("%s: %+v, want nothing", tc.name, got)
		case tc.want != 0 && (len(got) != 1 || got[0].Value != tc.want):
			t.Errorf("%s: %+v, want %v", tc.name, got, tc.want)
		}
	}
	// A source seen twice: the first stands.
	var twice MDBListTitle
	json.Unmarshal([]byte(`{"ratings": [{"source": "tomatoesaudience", "value": 80}, {"source": "popcorn", "value": 20}]}`), &twice)
	if got := twice.StoreRatings(1); len(got) != 1 || got[0].Value != 80 {
		t.Errorf("twice = %+v", got)
	}
	// Votes: a count, or nothing.
	var votes MDBListTitle
	json.Unmarshal([]byte(`{"ratings": [{"source": "trakt", "value": 80, "votes": -5}, {"source": "metacritic", "value": 80, "votes": 9999999999999}]}`), &votes)
	for _, r := range votes.StoreRatings(1) {
		if r.Votes < 0 {
			t.Errorf("%s has %d votes", r.Source, r.Votes)
		}
	}
}

// The link of a rating comes from outside and is shown as a link: it is kept only when it is an
// https address on the source's own site.
func TestMDBListRatingLinks(t *testing.T) {
	for _, tc := range []struct{ source, in, want string }{
		{"rt_critics", "https://www.rottentomatoes.com/m/matrix", "https://www.rottentomatoes.com/m/matrix"},
		{"rt_critics", "https://rottentomatoes.com/m/matrix", "https://rottentomatoes.com/m/matrix"},
		{"rt_critics", "https://www.rottentomatoes.com/tv/x#reviews", "https://www.rottentomatoes.com/tv/x"},
		{"rt_audience", "https://www.rottentomatoes.com/m/matrix?a=b", "https://www.rottentomatoes.com/m/matrix?a=b"},
		{"metacritic", "https://www.metacritic.com/movie/the-matrix/", "https://www.metacritic.com/movie/the-matrix/"},
		{"letterboxd", "https://letterboxd.com/film/the-matrix/", "https://letterboxd.com/film/the-matrix/"},
		{"trakt", "https://trakt.tv/movies/the-matrix-1999", "https://trakt.tv/movies/the-matrix-1999"},
		{"trakt", "http://trakt.tv/movies/x", ""},
		{"trakt", "javascript:alert(1)", ""},
		{"trakt", "//trakt.tv/movies/x", ""},
		{"trakt", "https://evil.example/trakt.tv", ""},
		{"trakt", "https://trakt.tv.evil.example/x", ""},
		{"trakt", "https://eviltrakt.tv/x", ""},
		{"trakt", "https://user:pw@trakt.tv/x", ""},
		{"trakt", "https://trakt.tv:8443/x", ""},
		{"trakt", "https://www.rottentomatoes.com/m/matrix", ""}, // the wrong site for the source
		{"trakt", "", ""},
		{"trakt", "not a url", ""},
		{"trakt", "https://trakt.tv/" + strings.Repeat("a", 600), ""},
		{"imdb", "https://www.imdb.com/title/tt1/", ""}, // not a source MDBList is taken for
	} {
		if got := cleanRatingURL(tc.source, tc.in); got != tc.want {
			t.Errorf("%s %q = %q, want %q", tc.source, tc.in, got, tc.want)
		}
	}
}

func TestMDBListLimits(t *testing.T) {
	srv := newMDBServer(t, jsonReply(200, `{"user_id": 1, "api_requests": 1000, "api_requests_count": 123, "is_supporter": false}`))
	c, _ := quickMDB(t, srv)
	lim, err := c.Limits(t.Context())
	if err != nil || lim != (MDBListLimits{Limit: 1000, Used: 123}) {
		t.Errorf("limits = %+v, %v", lim, err)
	}
	if r := srv.last(); r.Path != "/user" || r.Key != mdbTestKey {
		t.Errorf("request = %+v", r)
	}
	if err := c.Check(t.Context()); err != nil {
		t.Errorf("Check = %v", err)
	}

	// An answer without the numbers says no limit; numbers as strings are numbers.
	srv2 := newMDBServer(t, jsonReply(200, `{"api_requests": "250000", "api_requests_count": "7"}`))
	c2, _ := quickMDB(t, srv2)
	if lim, err := c2.Limits(t.Context()); err != nil || lim != (MDBListLimits{Limit: 250000, Used: 7}) {
		t.Errorf("limits (strings) = %+v, %v", lim, err)
	}
	srv3 := newMDBServer(t, jsonReply(200, `{}`))
	c3, _ := quickMDB(t, srv3)
	if lim, err := c3.Limits(t.Context()); err != nil || lim != (MDBListLimits{}) {
		t.Errorf("limits (none) = %+v, %v", lim, err)
	}
}

func TestMDBListErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		unauth    bool
		notFound  bool
		transient bool
	}{
		{"401", 401, `{"error": "Invalid API key"}`, true, false, false},
		{"403", 403, `{"error": "Forbidden"}`, true, false, false},
		{"404", 404, `{"error": "Not found"}`, false, true, false},
		{"500", 500, `oops`, false, false, true},
		{"502", 502, ``, false, false, true},
		{"408", 408, ``, false, false, true},
		{"400", 400, `{"error": "bad id"}`, false, false, false},
		{"200 that says it failed: bad key", 200, `{"response": false, "error": "Invalid API key"}`, true, false, false},
		{"200 that says it failed: not found", 200, `{"response": false, "error": "Title not found"}`, false, true, false},
		{"200 that is not JSON", 200, `<html>`, false, false, true},
		{"200 of another shape", 200, `[1, 2]`, false, false, true},
	} {
		srv := newMDBServer(t, jsonReply(tc.status, tc.body))
		c, _ := quickMDB(t, srv)
		_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
		if err == nil {
			t.Errorf("%s: no error", tc.name)
			continue
		}
		if IsUnauthorized(err) != tc.unauth || IsNotFound(err) != tc.notFound || IsTransient(err) != tc.transient {
			t.Errorf("%s: unauthorized %v, not found %v, transient %v; want %v, %v, %v (%v)", tc.name,
				IsUnauthorized(err), IsNotFound(err), IsTransient(err), tc.unauth, tc.notFound, tc.transient, err)
		}
		var me *MDBListError
		if !errors.As(err, &me) {
			t.Errorf("%s: the error is not an *MDBListError: %T", tc.name, err)
		}
		if srv.count() != 1 {
			t.Errorf("%s: %d requests; a final answer is not asked again", tc.name, srv.count())
		}
	}
	// Positive control: the classification is not just "everything is an error".
	srv := newMDBServer(t, jsonReply(200, mdbTitle))
	c, _ := quickMDB(t, srv)
	if _, err := c.ByTMDB(t.Context(), MDBListMovie, 603); err != nil {
		t.Errorf("control: %v", err)
	}
}

func TestMDBListNoKeyIsRefusedWithoutARequest(t *testing.T) {
	srv := newMDBServer(t, jsonReply(200, mdbTitle))
	c, _ := quickMDB(t, srv, func(o *MDBListOptions) { o.Key = "  " })
	_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if !IsUnauthorized(err) || srv.count() != 0 {
		t.Errorf("err = %v, requests %d; want unauthorized without a request", err, srv.count())
	}
}

// A 429 is waited out as long as MDBList says (up to the client's limit) and the call tried again,
// a few times; one it cannot wait out is an error at once, and the next call fails without a request.
func TestMDBList429(t *testing.T) {
	// Retry-After: 3, then success.
	srv := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error": "Rate limit exceeded"}`)
			return
		}
		fmt.Fprint(w, mdbTitle)
	})
	c, slept := quickMDB(t, srv)
	if _, err := c.ByTMDB(t.Context(), MDBListMovie, 603); err != nil {
		t.Fatalf("after one 429: %v", err)
	}
	if srv.count() != 2 {
		t.Errorf("%d requests, want 2", srv.count())
	}
	if len(*slept) == 0 || (*slept)[0] != 3*time.Second {
		t.Errorf("slept %v, want to wait the 3 s MDBList asked for", *slept)
	}

	// Always 429: given up after a few tries, as transient with the wait it asked for.
	always := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
	})
	c2, _ := quickMDB(t, always)
	_, err := c2.ByTMDB(t.Context(), MDBListMovie, 603)
	var me *MDBListError
	if !errors.As(err, &me) || me.Status != 429 || !me.Transient || me.RetryAfter != 2*time.Second || me.Daily || !IsTransient(err) {
		t.Fatalf("err = %v (%+v)", err, me)
	}
	if got := always.count(); got != maxRateRetries+1 {
		t.Errorf("%d requests, want %d (the first and %d retries)", got, maxRateRetries+1, maxRateRetries)
	}
	if d := RetryDelay(err, 1); d < 2*time.Second {
		t.Errorf("RetryDelay = %v; it must not be shorter than what MDBList asked", d)
	}

	// A wait longer than the client sits out: an error at once, and the service is left alone.
	long := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
	})
	c3, _ := quickMDB(t, long)
	if _, err := c3.ByTMDB(t.Context(), MDBListMovie, 603); !IsTransient(err) {
		t.Fatalf("a long 429: %v", err)
	}
	if long.count() != 1 {
		t.Errorf("%d requests for a 429 that cannot be waited out, want 1", long.count())
	}
	if _, err := c3.ByTMDB(t.Context(), MDBListMovie, 604); !IsTransient(err) || long.count() != 1 {
		t.Errorf("the next call: %v, requests %d; MDBList asked us to stay away, so no request is made", err, long.count())
	}
}

// The day's allowance used up: said in a 429 or in a 200 that says it failed. Nothing is asked
// again until the day (UTC) is over, and the calls in between say it is the day's allowance, also
// in the last hour of the day, when the pause is shorter than some 429s ask for.
func TestMDBListDailyLimit(t *testing.T) {
	for _, at := range []struct {
		name  string
		now   time.Time
		until time.Duration // to 00:01 UTC
	}{
		{"20:00", time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC), 4*time.Hour + time.Minute},
		{"23:49", time.Date(2026, 11, 11, 23, 49, 0, 0, time.UTC), 12 * time.Minute},
	} {
		for kind, handler := range map[string]func(http.ResponseWriter, *http.Request, int){
			"429":      jsonReply(429, `{"error": "You have reached your daily API limit"}`),
			"200 says": jsonReply(200, `{"response": false, "error": "API Limit Reached!"}`),
		} {
			name := at.name + " " + kind
			srv := newMDBServer(t, handler)
			c, _ := quickMDBAt(t, srv, at.now)
			_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
			until, daily := IsMDBListDaily(err)
			if !daily || !IsTransient(err) {
				t.Fatalf("%s: err = %v, daily %v", name, err, daily)
			}
			if until != at.until {
				t.Errorf("%s: until = %v, want %v", name, until, at.until)
			}
			if srv.count() != 1 {
				t.Errorf("%s: %d requests; a daily limit is not retried", name, srv.count())
			}
			_, next := c.ByIMDb(t.Context(), MDBListMovie, "tt0133093")
			if !IsTransient(next) {
				t.Errorf("%s: the next call: %v", name, next)
			}
			if _, daily := IsMDBListDaily(next); !daily {
				t.Errorf("%s: the next call does not say it is the day's allowance: %v", name, next)
			}
			if _, daily := IsMDBListDaily(c.Gate()); !daily {
				t.Errorf("%s: Gate does not say it is the day's allowance", name)
			}
			if srv.count() != 1 {
				t.Errorf("%s: %d requests; none is made while the allowance is gone", name, srv.count())
			}
		}
	}
	// A rate limit is not the day's allowance.
	rate := newMDBServer(t, jsonReply(200, `{"response": false, "error": "API Rate Limit Reached!"}`))
	c, _ := quickMDB(t, rate)
	_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if _, daily := IsMDBListDaily(err); daily || !IsTransient(err) {
		t.Errorf("a rate limit: %v", err)
	}
}

// Whether a pause is for the day's allowance is kept while it lasts, whatever else pauses the client
// on top of it, and is forgotten once it is over.
func TestMDBListDailyPause(t *testing.T) {
	srv := newMDBServer(t, jsonReply(200, mdbTitle))
	dailyGate := func(c *MDBListClient) bool {
		err := c.Gate()
		if !IsTransient(err) {
			t.Fatalf("Gate = %v during a pause", err)
		}
		_, daily := IsMDBListDaily(err)
		return daily
	}

	c, _ := quickMDB(t, srv)
	c.pause(2*time.Minute, true)
	c.pause(time.Second, false) // a rate limit while the day's allowance is gone
	if !dailyGate(c) {
		t.Error("a rate pause inside the day's pause makes it a rate pause")
	}

	c, _ = quickMDB(t, srv)
	c.pause(time.Minute, false)
	c.pause(2*time.Minute, true) // the day's allowance gone during a rate pause
	if !dailyGate(c) {
		t.Error("the day's pause on top of a rate pause is not seen")
	}
	// A request queued when the day's pause began says so too.
	if _, err := c.acquire(t.Context()); !IsTransient(err) {
		t.Errorf("acquire = %v during the day's pause", err)
	} else if _, daily := IsMDBListDaily(err); !daily {
		t.Errorf("acquire does not say it is the day's allowance: %v", err)
	}

	c.sleep(t.Context(), 3*time.Minute) // the clock moves on past it
	c.pause(2*time.Minute, false)
	if dailyGate(c) {
		t.Error("a rate pause after the day's pause ended is taken for the day's")
	}
}

// A 200 that says "API Rate Limit Reached!" is MDBList's limit for a period, not the day's: every
// request is held off for a minute, as for a 429, and Gate says so without sending anything.
func TestMDBListRateLimitAnswerPausesTheClient(t *testing.T) {
	ctx := t.Context()
	srv := newMDBServer(t, jsonReply(200, `{"response": false, "error": "API Rate Limit Reached!"}`))
	c, _ := quickMDB(t, srv)
	if err := c.Gate(); err != nil {
		t.Fatalf("control: Gate before any pause = %v", err)
	}
	_, err := c.ByTMDB(ctx, MDBListMovie, 603)
	var me *MDBListError
	if !errors.As(err, &me) || me.Status != 429 || !me.Transient || me.Daily || me.RetryAfter != time.Minute {
		t.Fatalf("err = %v (%+v), want a transient limit of a minute", err, me)
	}
	if srv.count() != 1 {
		t.Fatalf("%d requests, want 1", srv.count())
	}
	for i := range 5 {
		if _, err := c.ByTMDB(ctx, MDBListMovie, int64(700+i)); !IsTransient(err) {
			t.Errorf("call %d during the pause: %v", i, err)
		}
	}
	if srv.count() != 1 {
		t.Errorf("%d requests; none is made during the minute", srv.count())
	}
	if err := c.Gate(); err == nil || !IsTransient(err) {
		t.Errorf("Gate during the pause = %v, want a transient error", err)
	}

	// The minute passes (the test's sleep moves its clock): requests go again.
	c.sleep(ctx, time.Minute+time.Second)
	if err := c.Gate(); err != nil {
		t.Errorf("Gate after the minute = %v", err)
	}
	c.ByTMDB(ctx, MDBListMovie, 603)
	if srv.count() != 2 {
		t.Errorf("%d requests after the minute, want 2", srv.count())
	}
}

// The key is in the query of every request, so it is cut out of everything that is said of one:
// errors, the bodies they quote, the transport's messages, logs and the client's own printing.
func TestMDBListNeverShowsTheKey(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	check := func(what string, v any) {
		t.Helper()
		for _, s := range []string{fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v)} {
			if strings.Contains(s, mdbTestKey) {
				t.Errorf("%s shows the key: %s", what, s)
			}
		}
	}

	// A server that echoes the request, key and all, in its error.
	echo := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.WriteHeader(500)
		fmt.Fprintf(w, `{"error": "failed: %s"}`, r.URL.String())
	})
	c, _ := quickMDB(t, echo, func(o *MDBListOptions) { o.Logger = log })
	_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if err == nil {
		t.Fatal("no error")
	}
	check("an error that echoes", err)
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("the key was cut out but left no mark: %v", err)
	}

	// A server that is not there: net/http's error has the whole URL in it.
	gone := newMDBServer(t, jsonReply(200, ""))
	base := gone.URL
	gone.Close()
	c2 := NewMDBListClient(MDBListOptions{Key: mdbTestKey, BaseURL: base, Logger: log, Timeout: time.Second})
	_, err = c2.ByTMDB(t.Context(), MDBListMovie, 603)
	if err == nil || !IsTransient(err) {
		t.Fatalf("a server that is not there: %v", err)
	}
	check("a transport error", err)
	var me *MDBListError
	if errors.As(err, &me) {
		check("its cause", me.Err)
		for e := error(me); e != nil; e = errors.Unwrap(e) {
			check("a link of its chain", e.Error())
		}
	}

	// A key in a 200 that is not JSON.
	bad := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprintf(w, "not json %s", r.URL.RawQuery) })
	c3, _ := quickMDB(t, bad, func(o *MDBListOptions) { o.Logger = log })
	_, err = c3.ByTMDB(t.Context(), MDBListMovie, 603)
	check("a malformed answer", err)

	// The options, the client and the logs.
	opts := MDBListOptions{Key: mdbTestKey, BaseURL: "https://api.mdblist.com"}
	check("the options", opts)
	check("the options (pointer)", &opts)
	check("the client", c)
	var ob bytes.Buffer
	slog.New(slog.NewTextHandler(&ob, nil)).Info("x", "opts", opts, "client", c)
	check("a log line with them", ob.String())
	check("the logs of the client", logs.String())
	if logs.Len() == 0 {
		t.Error("control: the client logged nothing at debug level, so the check above looked at nothing")
	}
	if !strings.Contains(logs.String(), "mdblist request") {
		t.Errorf("control: the logs hold no request line: %s", logs.String())
	}
}

// A redirect is not followed: the key is in the address, and goes where the base URL says, no further.
func TestMDBListDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		fmt.Fprint(w, mdbTitle)
	}))
	defer other.Close()
	srv := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		http.Redirect(w, r, other.URL+"/steal?"+r.URL.RawQuery, http.StatusFound)
	})
	c, _ := quickMDB(t, srv)
	_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if err == nil || !strings.Contains(err.Error(), "redirect not followed") || IsTransient(err) {
		t.Errorf("err = %v, want a final \"redirect not followed\"", err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the redirect was followed: %d requests to the other host (with the key)", elsewhere.Load())
	}

	// A client given a client of its own gets the same, unless that has its own policy.
	c2 := NewMDBListClient(MDBListOptions{Key: mdbTestKey, BaseURL: srv.URL, HTTPClient: srv.Client(), Rate: 1000})
	if _, err := c2.ByTMDB(t.Context(), MDBListMovie, 603); err == nil || elsewhere.Load() != 0 {
		t.Errorf("with a client of its own: %v, %d", err, elsewhere.Load())
	}
	if srv.Client().CheckRedirect != nil {
		t.Error("the caller's client was changed")
	}
}

// The key goes only over https (a server on this machine is let off, for tests and a local mirror).
func TestMDBListBaseURL(t *testing.T) {
	for _, tc := range []struct {
		base string
		ok   bool
	}{
		{"https://api.mdblist.com", true}, {"", true}, {"http://127.0.0.1:9", true}, {"http://localhost:8080", true}, {"http://[::1]:80", true},
		{"http://api.mdblist.com", false}, {"http://mdblist.example.test", false}, {"ftp://x", false}, {"api.mdblist.com", false}, {"https://", false},
	} {
		c := NewMDBListClient(MDBListOptions{Key: mdbTestKey, BaseURL: tc.base, Timeout: time.Second})
		if (c.baseErr == "") != tc.ok {
			t.Errorf("%q: baseErr = %q, ok should be %v", tc.base, c.baseErr, tc.ok)
		}
	}
	c := NewMDBListClient(MDBListOptions{Key: mdbTestKey, BaseURL: "http://mdblist.example.test"})
	_, err := c.ByTMDB(t.Context(), MDBListMovie, 603)
	if err == nil || !strings.Contains(err.Error(), "https") || IsTransient(err) {
		t.Errorf("err = %v; want a final refusal that says https", err)
	}
	if c.base != "http://mdblist.example.test" || strings.Contains(c.String(), mdbTestKey) {
		t.Errorf("client = %s", c)
	}
}

// Requests are spaced by the bucket (the rate), and no more than MaxInFlight are out at once.
func TestMDBListLimiter(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := newMDBServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(w, mdbTitle)
	})
	c, _ := quickMDB(t, srv, func(o *MDBListOptions) { o.MaxInFlight = 2 })
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ByTMDB(t.Context(), MDBListMovie, int64(i+1)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Errorf("%d requests were out at once, want at most 2", peak.Load())
	}
	if peak.Load() < 2 {
		t.Errorf("control: the peak was %d; the test never had two out at once", peak.Load())
	}

	// With a rate of 2 a second and a burst of 2, the third request in a row waits half a second.
	srv2 := newMDBServer(t, jsonReply(200, mdbTitle))
	paced, slept := quickMDB(t, srv2, func(o *MDBListOptions) { o.Rate = 2 })
	for i := range 4 {
		if _, err := paced.ByTMDB(t.Context(), MDBListMovie, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) < 2 {
		t.Errorf("slept %v for 4 requests at 2 a second with a burst of 2", *slept)
	}
}
