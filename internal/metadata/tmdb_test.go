package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSendsTheKeyAsParameterOrBearer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		bearer bool
		want   string // the key as it should arrive
	}{
		{"v3 key", testKey, false, testKey},
		{"v3 key with whitespace around it", "  " + testKey + "\n", false, testKey},
		{"v4 token", testToken, true, testToken},
	} {
		f := newFakeTMDB(t)
		f.on("/configuration", "", "configuration.json")
		c := testClient(t, f, func(o *Options) { o.Key = tc.key })
		if _, err := c.Configuration(t.Context()); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		req := f.requests()[0]
		if tc.bearer {
			if got := req.Header.Get("Authorization"); got != "Bearer "+tc.want {
				t.Errorf("%s: Authorization = %q", tc.name, got)
			}
			if got := req.Query.Get("api_key"); got != "" {
				t.Errorf("%s: a v4 token went out as api_key=%q", tc.name, got)
			}
		} else {
			if got := req.Query.Get("api_key"); got != tc.want {
				t.Errorf("%s: api_key = %q, want %q", tc.name, got, tc.want)
			}
			if got := req.Header.Get("Authorization"); got != "" {
				t.Errorf("%s: a v3 key went out as Authorization %q", tc.name, got)
			}
		}
		if got := req.Query.Get("language"); got != "en-US" {
			t.Errorf("%s: language = %q", tc.name, got)
		}
		if req.Header.Get("Accept") != "application/json" {
			t.Errorf("%s: Accept = %q", tc.name, req.Header.Get("Accept"))
		}
	}
}

func TestClientLanguageAndRegion(t *testing.T) {
	for lang, want := range map[string][2]string{
		"":      {"en-US", "US"},
		"pt-BR": {"pt-BR", "BR"},
		"de-de": {"de-de", "DE"},
		"fr":    {"fr", "US"},
	} {
		c := NewClient(Options{Language: lang})
		if c.Language() != want[0] || c.Region() != want[1] {
			t.Errorf("language %q: Language %q Region %q, want %q %q", lang, c.Language(), c.Region(), want[0], want[1])
		}
	}
	c := NewClient(Options{ImageBaseURL: "http://images.test/t/p/"})
	if got := c.ImageURL("w342", "/abc.jpg"); got != "http://images.test/t/p/w342/abc.jpg" {
		t.Errorf("ImageURL = %q", got)
	}
	if got := c.ImageURL("original", "abc.png"); got != "http://images.test/t/p/original/abc.png" {
		t.Errorf("ImageURL without a slash = %q", got)
	}
	if got := NewClient(Options{}).ImageURL("w92", "/x.jpg"); got != DefaultImageBaseURL+"/w92/x.jpg" {
		t.Errorf("default image base: %q", got)
	}
}

func TestClientTypedResponses(t *testing.T) {
	ctx := t.Context()
	f := newFakeTMDB(t)
	f.on("/configuration", "", "configuration.json")
	f.on("/search/movie", params("query", "The Matrix", "year", "1999"), "search_movie_matrix.json")
	f.on("/search/tv", params("query", "Breaking Bad", "first_air_date_year", "2008"), "search_tv_breaking_bad.json")
	f.on("/search/tv", params("query", "Breaking Bad"), "search_tv_breaking_bad.json")
	f.on("/movie/603", "", "movie_603.json")
	f.on("/collection/2344", "", "collection_2344.json")
	f.on("/tv/1396", "", "tv_1396.json")
	f.on("/tv/100088", "", "tv_airing.json")
	f.on("/tv/1396/season/1", "", "tv_1396_season_1.json")
	f.on("/person/6384", "", "person_6384.json")
	f.on("/find/tt0133093", params("external_source", "imdb_id"), "find_imdb_tt0133093.json")
	f.on("/find/81189", params("external_source", "tvdb_id"), "find_tvdb_81189.json")
	c := testClient(t, f)

	cfg, err := c.Configuration(ctx)
	if err != nil || cfg.Images.SecureBaseURL != "https://image.tmdb.org/t/p/" || !slices.Contains(cfg.Images.PosterSizes, "w342") {
		t.Errorf("Configuration = %+v, %v", cfg, err)
	}

	movies, err := c.SearchMovies(ctx, "The Matrix", 1999)
	if err != nil || len(movies) != 5 {
		t.Fatalf("SearchMovies = %d results, %v", len(movies), err)
	}
	if m := movies[0]; m.ID != 603 || m.Title != "The Matrix" || m.ReleaseDate != "1999-03-30" || m.Popularity != 82.534 ||
		m.PosterPath != "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg" || m.OriginalTitle != "The Matrix" {
		t.Errorf("first movie = %+v", m)
	}
	if movies[4].BackdropPath != "" {
		t.Errorf("a null backdrop_path decoded to %q", movies[4].BackdropPath)
	}
	if got := f.requests()[1]; got.Query.Get("include_adult") != "false" || got.Query.Get("query") != "The Matrix" {
		t.Errorf("search request: %v", got.Query)
	}

	shows, err := c.SearchTV(ctx, "Breaking Bad", 2008)
	if err != nil || len(shows) != 2 || shows[0].ID != 1396 || shows[0].Name != "Breaking Bad" || shows[0].FirstAirDate != "2008-01-20" {
		t.Errorf("SearchTV = %+v, %v", shows, err)
	}
	if _, err := c.SearchTV(ctx, "Breaking Bad", 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests() {
		if r.Path == "/search/tv" && r.Query.Get("year") != "" {
			t.Errorf("a tv search sent year=%s (it is first_air_date_year)", r.Query.Get("year"))
		}
	}
	last := f.requests()[len(f.requests())-1]
	if last.Query.Has("first_air_date_year") {
		t.Errorf("year 0 still sent first_air_date_year: %v", last.Query)
	}

	m, err := c.MovieDetails(ctx, 603)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != 603 || m.IMDbID != "tt0133093" || m.Runtime != 136 || m.Status != "Released" || m.Tagline == "" ||
		len(m.Genres) != 2 || m.Genres[1].Name != "Science Fiction" || m.BelongsToCollection == nil || m.BelongsToCollection.ID != 2344 ||
		len(m.Credits.Cast) != 6 || len(m.Credits.Crew) != 10 || m.Credits.Cast[0].Name != "Keanu Reeves" ||
		m.ExternalIDs.IMDbID != "tt0133093" || len(m.Images.Logos) != 4 || len(m.ReleaseDates.Results) != 3 {
		t.Errorf("MovieDetails = %+v", m)
	}
	var movieReq fakeReq
	for _, r := range f.requests() {
		if r.Path == "/movie/603" {
			movieReq = r
		}
	}
	if got := movieReq.Query.Get("append_to_response"); got != "credits,release_dates,external_ids,images" {
		t.Errorf("movie append_to_response = %q", got)
	}
	if got := movieReq.Query.Get("include_image_language"); got != "en,null" {
		t.Errorf("include_image_language = %q", got)
	}

	tv, err := c.TVDetails(ctx, 1396)
	if err != nil {
		t.Fatal(err)
	}
	if tv.Name != "Breaking Bad" || tv.Status != "Ended" || tv.LastAirDate != "2013-09-29" || tv.NumberOfSeasons != 5 ||
		len(tv.Seasons) != 3 || tv.ExternalIDs.TVDBID != 81189 || tv.ExternalIDs.IMDbID != "tt0903747" ||
		len(tv.CreatedBy) != 1 || len(tv.Networks) != 1 || len(tv.AggregateCredits.Cast) != 3 || len(tv.ContentRatings.Results) != 3 ||
		tv.NextEpisodeToAir != nil || tv.LastEpisodeToAir == nil || tv.LastEpisodeToAir.Name != "Felina" || tv.LastEpisodeToAir.SeasonNumber != 5 {
		t.Errorf("TVDetails = %+v", tv)
	}
	airing, err := c.TVDetails(ctx, 100088)
	if err != nil || airing.NextEpisodeToAir == nil || airing.NextEpisodeToAir.AirDate != "2026-11-08" || airing.LastEpisodeToAir.AirDate != "2026-11-01" {
		t.Errorf("airing show: %+v, %v", airing, err)
	}
	for _, r := range f.requests() {
		if r.Path == "/tv/1396" && r.Query.Get("append_to_response") != "aggregate_credits,content_ratings,external_ids,images" {
			t.Errorf("tv append_to_response = %q", r.Query.Get("append_to_response"))
		}
	}

	season, err := c.SeasonDetails(ctx, 1396, 1)
	if err != nil || season.Name != "Season 1" || season.SeasonNumber != 1 || len(season.Episodes) != 7 ||
		season.Episodes[0].Name != "Pilot" || season.Episodes[0].Runtime != 58 || season.Episodes[2].StillPath != "" {
		t.Errorf("SeasonDetails = %+v, %v", season, err)
	}

	person, err := c.PersonDetails(ctx, 6384)
	if err != nil || person.Name != "Keanu Reeves" || person.Birthday != "1964-09-02" || person.Deathday != "" ||
		person.PlaceOfBirth != "Beirut, Lebanon" || person.IMDbID != "nm0000206" || person.KnownForDepartment != "Acting" {
		t.Errorf("PersonDetails = %+v, %v", person, err)
	}

	coll, err := c.CollectionDetails(ctx, 2344)
	if err != nil || coll.Name != "The Matrix Collection" || len(coll.Parts) != 2 || coll.Overview == "" {
		t.Errorf("CollectionDetails = %+v, %v", coll, err)
	}

	found, err := c.Find(ctx, ExternalIMDb, "tt0133093")
	if err != nil || len(found.MovieResults) != 1 || found.MovieResults[0].ID != 603 || len(found.TVResults) != 0 {
		t.Errorf("Find(imdb) = %+v, %v", found, err)
	}
	found, err = c.Find(ctx, ExternalTVDB, "81189")
	if err != nil || len(found.TVResults) != 1 || found.TVResults[0].ID != 1396 {
		t.Errorf("Find(tvdb) = %+v, %v", found, err)
	}
}

func TestClientChangesFollowPages(t *testing.T) {
	ctx := t.Context()
	f := newFakeTMDB(t)
	start := time.Date(2026, 11, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600)) // 2026-11-02 04:30 UTC
	end := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	dates := []string{"end_date", "2026-11-02", "start_date", "2026-11-02"}
	f.on("/movie/changes", params(append([]string{"page", "1"}, dates...)...), "changes_movie_page1.json")
	f.on("/movie/changes", params(append([]string{"page", "2"}, dates...)...), "changes_movie_page2.json")
	f.on("/tv/changes", params(append([]string{"page", "1"}, dates...)...), "changes_tv_page1.json")
	c := testClient(t, f)

	ids, err := c.MovieChanges(ctx, start, end)
	if err != nil || !slices.Equal(ids, []int64{603, 604, 999001, 27205, 949}) {
		t.Errorf("MovieChanges = %v, %v", ids, err)
	}
	ids, err = c.TVChanges(ctx, start, end)
	if err != nil || !slices.Equal(ids, []int64{1396, 100088}) {
		t.Errorf("TVChanges = %v, %v", ids, err)
	}
	if got, want := f.asked(), []string{
		"/movie/changes?end_date=2026-11-02&page=1&start_date=2026-11-02",
		"/movie/changes?end_date=2026-11-02&page=2&start_date=2026-11-02",
		"/tv/changes?end_date=2026-11-02&page=1&start_date=2026-11-02",
	}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}

	// A feed that never ends is cut off, and says so: a list that stops short
	// would pass for all that changed.
	var pages atomic.Int32
	feed := func(totalPages int) *fakeTMDB {
		f := newFakeTMDB(t)
		pages.Store(0)
		for p := 1; p <= maxChangePages+5; p++ {
			f.handle("/movie/changes", params("end_date", "2026-11-02", "page", fmt.Sprint(p), "start_date", "2026-11-02"),
				func(w http.ResponseWriter, r *http.Request) {
					pages.Add(1)
					fmt.Fprintf(w, `{"results":[{"id":1}],"total_pages":%d}`, totalPages)
				})
		}
		return f
	}
	if ids, err := testClient(t, feed(99999)).MovieChanges(ctx, start, end); !errors.Is(err, ErrChangesTruncated) || ids != nil || int(pages.Load()) != maxChangePages {
		t.Errorf("endless feed: %d ids, %d pages, %v; want ErrChangesTruncated after %d pages", len(ids), pages.Load(), err, maxChangePages)
	}
	if ids, err := testClient(t, feed(maxChangePages+1)).MovieChanges(ctx, start, end); !errors.Is(err, ErrChangesTruncated) || ids != nil {
		t.Errorf("a feed one page too long: %d ids, %v", len(ids), err)
	}
	if IsTransient(ErrChangesTruncated) {
		t.Error("a feed that is too long is not a transient error: retrying cannot make it shorter")
	}
	// Exactly as many pages as are followed is all of it.
	if ids, err := testClient(t, feed(maxChangePages)).MovieChanges(ctx, start, end); err != nil || len(ids) != maxChangePages || int(pages.Load()) != maxChangePages {
		t.Errorf("a feed of exactly %d pages: %d ids, %d pages, %v", maxChangePages, len(ids), pages.Load(), err)
	}

	// An error on a later page is an error, not a short list.
	broken := newFakeTMDB(t)
	broken.on("/movie/changes", params("end_date", "2026-11-02", "page", "1", "start_date", "2026-11-02"), "changes_movie_page1.json")
	broken.fail("/movie/changes", params("end_date", "2026-11-02", "page", "2", "start_date", "2026-11-02"), 503, "", nil)
	if ids, err := testClient(t, broken).MovieChanges(ctx, start, end); err == nil || ids != nil || !IsTransient(err) {
		t.Errorf("changes with a failing page 2 = %v, %v", ids, err)
	}
}

func TestClientErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		file      string
		headers   map[string]string
		transient bool
		notFound  bool
		unauth    bool
		message   string
	}{
		{"404", 404, "error_404.json", nil, false, true, false, "could not be found"},
		{"401", 401, "error_401.json", nil, false, false, true, "Invalid API key"},
		{"403", 403, "", nil, false, false, true, ""},
		{"400", 400, "", nil, false, false, false, ""},
		{"422", 422, "", nil, false, false, false, ""},
		{"500", 500, "", nil, true, false, false, ""},
		{"502 with an html body", 502, "", nil, true, false, false, ""},
		{"503", 503, "", nil, true, false, false, ""},
		{"504", 504, "", nil, true, false, false, ""},
		{"408", 408, "", nil, true, false, false, ""},
		{"429 to wait out later", 429, "error_429.json", map[string]string{"Retry-After": "3600"}, true, false, false, "over the allowed limit"},
	} {
		f := newFakeTMDB(t)
		f.fail("/movie/603", "", tc.status, tc.file, tc.headers)
		_, err := testClient(t, f).MovieDetails(t.Context(), 603)
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%s: error %v is not a *Error", tc.name, err)
			continue
		}
		if IsTransient(err) != tc.transient || IsNotFound(err) != tc.notFound || IsUnauthorized(err) != tc.unauth {
			t.Errorf("%s: transient %v notFound %v unauthorized %v; want %v %v %v",
				tc.name, IsTransient(err), IsNotFound(err), IsUnauthorized(err), tc.transient, tc.notFound, tc.unauth)
		}
		if e.Status != tc.status || !strings.Contains(e.Message, tc.message) || !strings.Contains(err.Error(), fmt.Sprint(tc.status)) {
			t.Errorf("%s: %+v / %q", tc.name, e, err)
		}
		if !strings.Contains(err.Error(), "GET /movie/603") {
			t.Errorf("%s: %q does not say what failed", tc.name, err)
		}
	}
}

func TestClientBrokenResponsesAreTransient(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"truncated json", ""}, // file below
		{"empty body", ""},
		{"html", "<html>Bad gateway</html>"},
		{"json of the wrong shape", `[1,2,3]`},
	} {
		f := newFakeTMDB(t)
		if tc.name == "truncated json" {
			f.on("/movie/603", "", "malformed.json")
		} else {
			f.set("/movie/603", "", fakeRoute{status: 200, body: tc.body})
		}
		_, err := testClient(t, f).MovieDetails(t.Context(), 603)
		if err == nil || !IsTransient(err) || IsNotFound(err) {
			t.Errorf("%s: err = %v, want a transient error", tc.name, err)
		}
	}
}

func TestClientNetworkFailureIsTransient(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := NewClient(Options{Key: testKey, BaseURL: dead.URL + "/3", Logger: discardLogger()})
	_, err := c.Configuration(t.Context())
	var e *Error
	if !errors.As(err, &e) || !e.Transient || e.Status != 0 {
		t.Fatalf("err = %#v, want a transient error without a status", err)
	}
	if IsNotFound(err) || IsUnauthorized(err) {
		t.Error("a network error was classified as a response")
	}
}

func TestClientTimeout(t *testing.T) {
	f := newFakeTMDB(t)
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
			w.Write(f.fixture("configuration.json"))
		case <-r.Context().Done():
		}
	}
	f.handle("/configuration", "", slow)

	start := time.Now()
	_, err := testClient(t, f, func(o *Options) { o.Timeout = 50 * time.Millisecond }).Configuration(t.Context())
	if err == nil || !IsTransient(err) || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a transient timeout", err)
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("the timeout took %v", took)
	}
	// Positive control: with enough time the same server answers.
	if _, err := testClient(t, f, func(o *Options) { o.Timeout = 5 * time.Second }).Configuration(t.Context()); err != nil {
		t.Errorf("slow server with a long timeout: %v", err)
	}
}

func TestClientCancelledContextIsNotATMDBFailure(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/configuration", "", "configuration.json")
	c := testClient(t, f)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.Configuration(ctx)
	if !errors.Is(err, context.Canceled) || IsTransient(err) {
		t.Errorf("err = %v; want context.Canceled and not transient", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("a cancelled call reached the server (%d requests)", n)
	}

	// Cancelled while the request is in flight.
	f.handle("/person/1", "", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := c.PersonDetails(ctx, 1); !errors.Is(err, context.Canceled) || IsTransient(err) {
		t.Errorf("in flight: err = %v", err)
	}
}

func TestClientHonoursRetryAfter(t *testing.T) {
	ctx := t.Context()
	respond429 := func(retryAfter string, until int32, hits *atomic.Int32, f *fakeTMDB) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) <= until {
				if retryAfter != "" {
					w.Header().Set("Retry-After", retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write(f.fixture("error_429.json"))
				return
			}
			w.Write(f.fixture("configuration.json"))
		}
	}

	t.Run("waits the seconds asked and retries", func(t *testing.T) {
		f, clk, hits := newFakeTMDB(t), newFakeClock(), new(atomic.Int32)
		f.handle("/configuration", "", respond429("3", 1, hits, f))
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		if _, err := c.Configuration(ctx); err != nil {
			t.Fatalf("err = %v, want success after the retry", err)
		}
		if hits.Load() != 2 || !slices.Equal(clk.slept(), []time.Duration{3 * time.Second}) {
			t.Errorf("hits %d, slept %v; want 2 requests and one 3s wait", hits.Load(), clk.slept())
		}
	})

	t.Run("an http date", func(t *testing.T) {
		f, clk, hits := newFakeTMDB(t), newFakeClock(), new(atomic.Int32)
		f.handle("/configuration", "", respond429(clk.now().Add(7*time.Second).UTC().Format(http.TimeFormat), 1, hits, f))
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		if _, err := c.Configuration(ctx); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(clk.slept(), []time.Duration{7 * time.Second}) {
			t.Errorf("slept %v, want 7s", clk.slept())
		}
	})

	t.Run("no header means two seconds", func(t *testing.T) {
		f, clk, hits := newFakeTMDB(t), newFakeClock(), new(atomic.Int32)
		f.handle("/configuration", "", respond429("", 1, hits, f))
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		if _, err := c.Configuration(ctx); err != nil || !slices.Equal(clk.slept(), []time.Duration{2 * time.Second}) {
			t.Errorf("err %v, slept %v; want 2s", err, clk.slept())
		}
	})

	t.Run("gives up after a few tries with the wait for the caller", func(t *testing.T) {
		f, clk, hits := newFakeTMDB(t), newFakeClock(), new(atomic.Int32)
		f.handle("/configuration", "", respond429("1", 100, hits, f))
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		_, err := c.Configuration(ctx)
		var e *Error
		if !errors.As(err, &e) || e.Status != 429 || !e.Transient || e.RetryAfter != time.Second {
			t.Fatalf("err = %#v", err)
		}
		if hits.Load() != 1+maxRateRetries || len(clk.slept()) != maxRateRetries {
			t.Errorf("hits %d, sleeps %v; want %d tries and %d waits", hits.Load(), clk.slept(), 1+maxRateRetries, maxRateRetries)
		}
	})

	t.Run("a long wait is not sat out; calls during it fail fast, then go through", func(t *testing.T) {
		f, clk, hits := newFakeTMDB(t), newFakeClock(), new(atomic.Int32)
		f.handle("/configuration", "", respond429("120", 1, hits, f))
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		_, err := c.Configuration(ctx)
		var e *Error
		if !errors.As(err, &e) || e.RetryAfter != 120*time.Second || hits.Load() != 1 || len(clk.slept()) != 0 {
			t.Fatalf("err %#v, hits %d, slept %v; want one request, no waiting, RetryAfter 120s", err, hits.Load(), clk.slept())
		}
		// Still inside the pause: nothing is sent.
		clk.advance(10 * time.Second)
		_, err = c.Configuration(ctx)
		if !errors.As(err, &e) || !e.Transient || e.RetryAfter != 110*time.Second || hits.Load() != 1 {
			t.Errorf("during the pause: err %#v, hits %d; want a fast transient failure with 110s left", err, hits.Load())
		}
		// Positive control: once it is over, requests flow again.
		clk.advance(111 * time.Second)
		if _, err := c.Configuration(ctx); err != nil || hits.Load() != 2 {
			t.Errorf("after the pause: err %v, hits %d", err, hits.Load())
		}
	})

	t.Run("a pause holds back every path", func(t *testing.T) {
		f, clk := newFakeTMDB(t), newFakeClock()
		var hits atomic.Int32
		f.handle("/configuration", "", respond429("5", 1, &hits, f))
		f.on("/person/6384", "", "person_6384.json")
		c := testClient(t, f)
		useClock(c, clk, 1000, 1000)
		if _, err := c.Configuration(ctx); err != nil { // 429, waits 5s, retries
			t.Fatal(err)
		}
		start := clk.now()
		c.pause(4 * time.Second) // as a 429 on some other call would
		if _, err := c.PersonDetails(ctx, 6384); err != nil {
			t.Fatal(err)
		}
		if got := clk.now().Sub(start); got != 4*time.Second {
			t.Errorf("a call during a pause waited %v, want 4s", got)
		}
	})
}

func TestBucketIsATokenBucket(t *testing.T) {
	clk := newFakeClock()
	b := newBucket(20, 20, clk.now)
	for i := 0; i < 20; i++ {
		if w := b.take(); w != 0 {
			t.Fatalf("take %d waits %v; the first 20 are free", i+1, w)
		}
	}
	for i, want := range []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond} {
		if w := b.take(); w != want {
			t.Errorf("take %d waits %v, want %v", 21+i, w, want)
		}
	}
	clk.advance(time.Second) // 20 tokens come back, 3 are owed
	if w := b.take(); w != 0 {
		t.Errorf("after a second: wait %v", w)
	}
	clk.advance(time.Hour) // refills only up to the burst
	for i := 0; i < 20; i++ {
		if w := b.take(); w != 0 {
			t.Fatalf("after an hour, take %d waits %v", i+1, w)
		}
	}
	if w := b.take(); w == 0 {
		t.Error("an hour of idleness saved up more than the burst")
	}
}

func TestClientPacesRequestsAt20PerSecond(t *testing.T) {
	f, clk := newFakeTMDB(t), newFakeClock()
	f.on("/configuration", "", "configuration.json")
	c := testClient(t, f, func(o *Options) { o.Rate = 20 })
	useClock(c, clk, 20, 20)
	start := clk.now()
	for i := 0; i < 45; i++ {
		if _, err := c.Configuration(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// 20 in the burst, then 25 more at 20 a second.
	if got, want := clk.now().Sub(start), 25*50*time.Millisecond; got != want {
		t.Errorf("45 requests took %v of waiting, want %v", got, want)
	}
	if len(clk.slept()) != 25 {
		t.Errorf("waited %d times, want 25", len(clk.slept()))
	}
}

func TestClientLimitsRequestsInFlight(t *testing.T) {
	for _, tc := range []struct {
		name          string
		setting, want int
	}{
		{"default is 4", 0, 4},
		{"explicit 2", 2, 2},
		{"explicit 7", 7, 7},
	} {
		f := newFakeTMDB(t)
		var cur, high atomic.Int32
		gate := make(chan struct{})
		f.handle("/configuration", "", func(w http.ResponseWriter, r *http.Request) {
			n := cur.Add(1)
			for {
				h := high.Load()
				if n <= h || high.CompareAndSwap(h, n) {
					break
				}
			}
			select {
			case <-gate:
			case <-r.Context().Done():
			}
			cur.Add(-1)
			w.Write(f.fixture("configuration.json"))
		})
		c := testClient(t, f, func(o *Options) { o.MaxInFlight = tc.setting })

		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.Configuration(t.Context()); err != nil {
					t.Error(err)
				}
			}()
		}
		deadline := time.Now().Add(5 * time.Second)
		for int(cur.Load()) < tc.want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(60 * time.Millisecond) // time for a fifth to sneak in, if it could
		if got := int(cur.Load()); got != tc.want {
			t.Errorf("%s: %d requests in flight at the peak, want %d", tc.name, got, tc.want)
		}
		close(gate)
		wg.Wait()
		if int(high.Load()) != tc.want {
			t.Errorf("%s: high-water mark %d, want %d", tc.name, high.Load(), tc.want)
		}
		if n := len(f.requests()); n != 12 {
			t.Errorf("%s: %d requests served, want 12", tc.name, n)
		}
	}
}

func TestBackoffSchedule(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		-3: time.Minute, 0: time.Minute, 1: time.Minute, 2: 5 * time.Minute, 3: 30 * time.Minute,
		4: 2 * time.Hour, 5: 6 * time.Hour, 6: 6 * time.Hour, 50: 6 * time.Hour,
	} {
		if got := Backoff(failures); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", failures, got, want)
		}
	}
	retry := &Error{Transient: true, Status: 429, RetryAfter: 3 * time.Hour}
	for _, tc := range []struct {
		name     string
		err      error
		failures int
		want     time.Duration
	}{
		{"plain error", errors.New("x"), 2, 5 * time.Minute},
		{"retry-after longer than the backoff", retry, 1, 3 * time.Hour},
		{"retry-after shorter than the backoff", retry, 5, 6 * time.Hour},
		{"wrapped", fmt.Errorf("fetching: %w", retry), 3, 3 * time.Hour},
		{"transient without a retry-after", &Error{Transient: true, Status: 503}, 4, 2 * time.Hour},
	} {
		if got := RetryDelay(tc.err, tc.failures); got != tc.want {
			t.Errorf("%s: RetryDelay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"0": 0, "3": 3 * time.Second, " 12 ": 12 * time.Second, "": 2 * time.Second, "soon": 2 * time.Second,
		"-5": 2 * time.Second,
		now.Add(9 * time.Second).Format(http.TimeFormat): 9 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):    0,
	} {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

// capture is a slog handler's output at every level.
func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestKeyNeverAppearsInErrorsOrLogs(t *testing.T) {
	for _, key := range []string{testKey, testToken} {
		logger, logs := capture()
		f := newFakeTMDB(t)
		// A server that echoes the credentials in its error messages, the worst case.
		echo := func(status int) fakeRoute {
			return fakeRoute{status: status, body: fmt.Sprintf(`{"status_code":7,"status_message":"Invalid key %s for api_key=%s"}`, key, key)}
		}
		f.on("/movie/603", "", "movie_603.json")
		f.set("/movie/401", "", echo(401))
		f.set("/movie/500", "", echo(500))
		f.set("/movie/404", "", echo(404))
		f.set("/movie/429", "", fakeRoute{status: 429, body: echo(429).body, headers: map[string]string{"Retry-After": "99999"}})
		f.on("/movie/bad", "", "malformed.json")
		f.handle("/movie/slow", "", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		// A 429 with a long Retry-After pauses its client, so every scenario gets its own.
		newClient := func() *Client {
			return testClient(t, f, func(o *Options) { o.Key = key; o.Logger = logger; o.Timeout = 100 * time.Millisecond })
		}

		var texts []string
		note := func(err error) {
			if err == nil {
				t.Errorf("a call that should fail succeeded")
				return
			}
			texts = append(texts, err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%s", err))
			for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
				texts = append(texts, u.Error(), fmt.Sprintf("%#v", u))
			}
		}
		if _, err := newClient().MovieDetails(t.Context(), 603); err != nil {
			t.Fatalf("a good call failed: %v", err)
		}
		for _, id := range []int64{401, 500, 404, 429} {
			_, err := newClient().MovieDetails(t.Context(), id)
			note(err)
		}
		for _, path := range []string{"bad", "slow"} {
			var out MovieDetails
			note(newClient().get(t.Context(), "/movie/"+path, nil, &out))
		}
		// The transport failure, whose net/http error carries the request URL.
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		down := NewClient(Options{Key: key, BaseURL: dead.URL + "/3", Logger: logger})
		_, err := down.Configuration(t.Context())
		note(err)
		// An unreachable host, by name.
		nohost := NewClient(Options{Key: key, BaseURL: "http://nonexistent.invalid/3", Logger: logger, Timeout: time.Second})
		_, err = nohost.PersonDetails(t.Context(), 1)
		note(err)
		// A bad base URL fails before any connection.
		bad := NewClient(Options{Key: key, BaseURL: "http://[::1/3", Logger: logger})
		_, err = bad.Configuration(t.Context())
		note(err)

		everything := strings.Join(texts, "\n") + "\n" + logs.String()
		for _, secret := range secretsOf(key) {
			if strings.Contains(everything, secret) {
				t.Errorf("key %q: %q appears in errors or logs:\n%s", key[:6], secret, everything)
			}
		}

		// Positive controls: the capture works, the redaction ran, and the thing
		// we guard against is real.
		if !strings.Contains(logs.String(), "tmdb request") || !strings.Contains(logs.String(), "/movie/603") {
			t.Errorf("the log capture saw no request log:\n%s", logs.String())
		}
		if !strings.Contains(logs.String(), "tmdb rate limited") {
			t.Errorf("the log capture saw no 429 warning:\n%s", logs.String())
		}
		if !strings.Contains(everything, "***") {
			t.Errorf("no redaction marker anywhere: the echoed key was never seen\n%s", everything)
		}
		if len(texts) < 20 {
			t.Errorf("only %d error texts were checked", len(texts))
		}
		if key == testKey {
			_, raw := http.Get(dead.URL + "/3/configuration?api_key=" + key)
			if raw == nil || !strings.Contains(raw.Error(), key) {
				t.Errorf("control: net/http's own error should show the key in the URL, got %v", raw)
			}
		}
	}
}

// secretsOf lists what must never be seen: the key, its escaped forms and,
// for a token, each of its parts.
func secretsOf(key string) []string {
	secrets := []string{key}
	if strings.Contains(key, ".") {
		secrets = append(secrets, strings.Split(key, ".")...)
	} else {
		secrets = append(secrets, key[:12], key[12:])
	}
	return secrets
}

func TestRedact(t *testing.T) {
	const key = "a b/c+d&e"
	c := NewClient(Options{Key: key})
	for _, in := range []string{key, url.QueryEscape(key), url.PathEscape(key)} {
		if got := c.redact(in); got != "***" {
			t.Errorf("redact(%q) = %q, want ***", in, got)
		}
	}
	if got := c.redact("not the key"); got != "not the key" {
		t.Errorf("redact changed text without the key: %q", got)
	}
	if got := NewClient(Options{}).redact("anything"); got != "anything" {
		t.Errorf("redact without a key changed the text: %q", got)
	}
}

// A call that is queued for a slot must not sleep through a pause that another
// call's 429 set while it waited. The pause must be recorded before the slot is
// given back; that is only a few instructions apart, so once the 429 is on its
// way the clock is made slow, which stretches recording the pause (it reads the
// time) while the queued call, woken by the slot, looks for one.
func TestQueuedCallDoesNotSitOutALongPauseSetMeanwhile(t *testing.T) {
	f, clk := newFakeTMDB(t), newFakeClock()
	gate := make(chan struct{})
	var hits atomic.Int32
	f.handle("/configuration", "", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write(f.fixture("error_429.json"))
	})
	c := testClient(t, f, func(o *Options) { o.MaxInFlight = 1 })
	useClock(c, clk, 1000, 1000)
	var slow atomic.Bool
	c.now = func() time.Time {
		if slow.Load() {
			time.Sleep(30 * time.Millisecond)
		}
		return clk.now()
	}

	first := make(chan error, 1)
	go func() { _, err := c.Configuration(t.Context()); first <- err }()
	for hits.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	second := make(chan error, 1)
	go func() { _, err := c.Configuration(t.Context()); second <- err }() // queued: the only slot is taken
	time.Sleep(100 * time.Millisecond)
	slow.Store(true)
	close(gate) // the first gets its 429 and sets the pause

	for i, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			var e *Error
			if !errors.As(err, &e) || e.Status != 429 || !e.Transient || e.RetryAfter < 3500*time.Second || !strings.Contains(e.Op, "GET /configuration") {
				t.Errorf("call %d: err = %#v", i+1, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("call %d sat out the pause", i+1)
		}
	}
	if s := clk.slept(); len(s) != 0 {
		t.Errorf("slept %v; a pause longer than MaxWait is never sat out", s)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests reached TMDB, want 1", hits.Load())
	}
}

func TestClientDefaults(t *testing.T) {
	c := NewClient(Options{})
	if c.bucket.rate != 20 || c.bucket.burst != 20 || DefaultRate != 20 {
		t.Errorf("rate %v burst %v, want 20 and 20", c.bucket.rate, c.bucket.burst)
	}
	if cap(c.slots) != 4 || c.timeout != DefaultTimeout || c.maxWait != DefaultMaxWait || c.lang != "en-US" {
		t.Errorf("slots %d timeout %v maxWait %v lang %q", cap(c.slots), c.timeout, c.maxWait, c.lang)
	}
	if DefaultTimeout != 15*time.Second || DefaultMaxWait != 30*time.Second || DefaultMaxInFlight != 4 {
		t.Error("the documented defaults changed")
	}
	// The bucket starts full: 20 requests at once, the 21st waits.
	for i := 0; i < 20; i++ {
		if w := c.bucket.take(); w != 0 {
			t.Fatalf("request %d waits %v", i+1, w)
		}
	}
	if w := c.bucket.take(); w < 40*time.Millisecond || w > 60*time.Millisecond {
		t.Errorf("the 21st request waits %v, want about 50ms (20 a second)", w)
	}
	// What is asked for is what is used.
	c = NewClient(Options{Rate: 5, MaxInFlight: 2, Timeout: time.Second, MaxWait: time.Minute, Language: "fr-FR"})
	if c.bucket.rate != 5 || c.bucket.burst != 5 || cap(c.slots) != 2 || c.timeout != time.Second || c.maxWait != time.Minute || c.lang != "fr-FR" {
		t.Errorf("explicit options: rate %v slots %d timeout %v maxWait %v lang %q", c.bucket.rate, cap(c.slots), c.timeout, c.maxWait, c.lang)
	}
}

// Calls cancelled while they wait out a pause give their slot back: after as
// many cancellations as there are slots the client still works.
func TestCancelledCallsDuringAPauseReleaseTheirSlot(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/configuration", "", "configuration.json")
	c := testClient(t, f, func(o *Options) { o.MaxInFlight = 1; o.MaxWait = 20 * time.Second })
	c.pause(10 * time.Second) // within MaxWait: calls sleep it out
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		_, err := c.Configuration(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: %v, want its own deadline", i+1, err)
		}
	}
	c.mu.Lock()
	c.pauseUntil = time.Time{}
	c.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := c.Configuration(t.Context()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("after the cancellations: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the slots leaked: the client is stuck after cancelled calls")
	}
}

// roundTripper fails as it is told.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorTextIsRedacted(t *testing.T) {
	// A proxy, a TLS library or a custom transport may put the credentials it
	// was handed in its error text.
	for _, key := range []string{testKey, testToken} {
		tr := roundTripper(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("proxyconnect tcp: refused for api_key=%s (token %s)", key, key)
		})
		c := NewClient(Options{Key: key, BaseURL: "http://tmdb.invalid/3", HTTPClient: &http.Client{Transport: tr}, Logger: discardLogger()})
		_, err := c.Configuration(t.Context())
		if err == nil || !IsTransient(err) {
			t.Fatalf("err = %v", err)
		}
		all := err.Error() + fmt.Sprintf("%v %+v %#v", err, err, err)
		for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
			all += u.Error() + fmt.Sprintf("%#v", u)
		}
		if strings.Contains(all, key) {
			t.Errorf("the key is in %q", all)
		}
		if !strings.Contains(err.Error(), "***") || !strings.Contains(err.Error(), "proxyconnect") {
			t.Errorf("err = %q; the cause should be there with the key cut out", err)
		}
	}
}

func TestResponsesAreCappedAtEightMiB(t *testing.T) {
	big := func(n int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Valid JSON of about n bytes.
			fmt.Fprintf(w, `{"id": 1, "overview": "%s"}`, strings.Repeat("x", n))
		}
	}
	f := newFakeTMDB(t)
	f.handle("/movie/1", "", big(7<<20))
	f.handle("/movie/2", "", big(9<<20))
	c := testClient(t, f)
	if m, err := c.MovieDetails(t.Context(), 1); err != nil || len(m.Overview) != 7<<20 {
		t.Errorf("7 MiB response: %v", err)
	}
	if _, err := c.MovieDetails(t.Context(), 2); err == nil || !IsTransient(err) {
		t.Errorf("9 MiB response: err = %v; it is cut off at 8 MiB and so cannot be read", err)
	}
}
