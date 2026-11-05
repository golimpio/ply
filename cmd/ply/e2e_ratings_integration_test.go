//go:build integration && unix

package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// e2eMDBListKey is made up. The fake MDBList takes it, and the test looks for it everywhere.
const e2eMDBListKey = "e2e-mdblist-5a4b3c2d1e0f9a8b7c6d5e4f"

const e2eIMDbModified = "Wed, 04 Nov 2026 03:00:00 GMT"

// fakeIMDbDataset serves one of IMDb's datasets (title.ratings.tsv.gz or title.episode.tsv.gz), with
// an ETag and a Last-Modified, and counts how it was asked.
type fakeIMDbDataset struct {
	srv *httptest.Server

	mu          sync.Mutex
	hits        int
	conditional int // requests that carried If-None-Match or If-Modified-Since
	body        []byte
}

func newFakeIMDbDataset(t *testing.T, rows map[string]string) *fakeIMDbDataset {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("tconst\taverageRating\tnumVotes\n")
	for i := range 3000 { // the rest of IMDb, which nobody has in the library
		fmt.Fprintf(&sb, "tt%07d\t%.1f\t%d\n", 5_000_000+i, float64(10+i%90)/10, 10+i)
	}
	for id, v := range rows {
		rating, votes, _ := strings.Cut(v, " ")
		fmt.Fprintf(&sb, "%s\t%s\t%s\n", id, rating, votes)
	}
	return serveIMDbDataset(t, sb.String())
}

// newFakeIMDbEpisodes serves the episode dataset: the rows given ("episode show season number") among
// those of shows nobody has.
func newFakeIMDbEpisodes(t *testing.T, rows ...string) *fakeIMDbDataset {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("tconst\tparentTconst\tseasonNumber\tepisodeNumber\n")
	for i := range 3000 {
		fmt.Fprintf(&sb, "tt%07d\ttt%07d\t1\t%d\n", 8_000_000+i, 4_000_000+i/20, i%20+1)
	}
	for _, r := range rows {
		sb.WriteString(strings.ReplaceAll(r, " ", "\t") + "\n")
	}
	return serveIMDbDataset(t, sb.String())
}

func serveIMDbDataset(t *testing.T, text string) *fakeIMDbDataset {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(text))
	zw.Close()
	f := &fakeIMDbDataset{body: buf.Bytes()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			f.conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"e2e-1"`)
		w.Header().Set("Last-Modified", e2eIMDbModified)
		w.Header().Set("Content-Type", "application/x-gzip")
		w.Write(f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIMDbDataset) url() string { return f.srv.URL + "/title.ratings.tsv.gz" }

func (f *fakeIMDbDataset) counts() (hits, conditional int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, f.conditional
}

// fakeMDBListAPI is an MDBList that takes e2eMDBListKey, answers /user and the title paths below,
// and records every path it was asked for and every key it was sent.
type fakeMDBListAPI struct {
	srv *httptest.Server

	mu    sync.Mutex
	paths []string
	keys  map[string]int
	leak  int // requests whose address has something but the id and the key in it
}

const mdbE2ETitle = `{"title": %q, "year": 2000, "type": "movie", "ratings": [
  {"source": "imdb", "value": 8.7, "score": 87, "votes": 2000000, "url": "https://www.imdb.com/title/tt0000603"},
  {"source": "metacritic", "value": 73, "score": 73, "votes": 35, "url": "https://www.metacritic.com/movie/x"},
  {"source": "trakt", "value": 86, "score": 86, "votes": 70000, "url": "https://trakt.tv/movies/x"},
  {"source": "tomatoes", "value": %v, "score": %v, "votes": 150, "url": "https://www.rottentomatoes.com/m/x"},
  {"source": "tomatoesaudience", "value": 85, "score": 85, "votes": 250000, "url": "https://www.rottentomatoes.com/m/x"},
  {"source": "letterboxd", "value": 4.1, "score": 82, "votes": 1500000, "url": "https://letterboxd.com/film/x/"}]}`

func newFakeMDBListAPI(t *testing.T) *fakeMDBListAPI {
	t.Helper()
	f := &fakeMDBListAPI{keys: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.keys[key]++
		if len(r.URL.Query()) != 1 {
			f.leak++
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if key != e2eMDBListKey {
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error": "Invalid API key %s"}`, key)
			return
		}
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 7, "is_supporter": false}`)
		case "/tmdb/movie/603", "/imdb/movie/tt0000603":
			fmt.Fprintf(w, mdbE2ETitle, "The Matrix", 83.0, 83.0)
		case "/tmdb/show/5151":
			fmt.Fprintf(w, mdbE2ETitle, "Rated Show", 95.0, 95.0)
		case "/imdb/movie/tt9000001":
			fmt.Fprintf(w, mdbE2ETitle, "Some Film", 61.0, 61.0)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error": "Not found"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMDBListAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == path {
			n++
		}
	}
	return n
}

// seen is what the server was asked, under its lock: the paths, the number of distinct keys sent, and
// the requests that carried more than the key.
func (f *fakeMDBListAPI) seen() (paths []string, keys int, leak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), len(f.keys), f.leak
}

func (f *fakeMDBListAPI) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

// tmdbWithRatedShow is the fake TMDB of the other e2e (three movies) that also knows one show, "Rated
// Show" (5151, IMDb tt6000005), with one episode. TMDB gives no id for the episode: IMDb's episode
// dataset does.
func tmdbWithRatedShow(t *testing.T, tmdb *fakeTMDB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if r.URL.Query().Get("api_key") == e2eTMDBKey {
			switch {
			case p == "/3/search/tv":
				var res []map[string]any
				if strings.EqualFold(r.URL.Query().Get("query"), "Rated Show") {
					res = append(res, map[string]any{"id": 5151, "name": "Rated Show", "original_name": "Rated Show",
						"first_air_date": "2015-05-05", "overview": "A show.", "popularity": 50.0})
				}
				writeJSONTo(w, 200, map[string]any{"page": 1, "results": res})
				return
			case p == "/3/tv/5151":
				writeJSONTo(w, 200, map[string]any{"id": 5151, "name": "Rated Show", "original_name": "Rated Show",
					"first_air_date": "2015-05-05", "overview": "A show.", "status": "Ended", "external_ids": map[string]any{"imdb_id": "tt6000005"}})
				return
			case p == "/3/tv/5151/season/1":
				writeJSONTo(w, 200, map[string]any{"season_number": 1, "name": "Season 1",
					"episodes": []map[string]any{{"episode_number": 1, "name": "Pilot"}}})
				return
			case strings.HasSuffix(p, "/external_ids") && strings.Contains(p, "/episode/"):
				tmdb.hit("episode ids") // nobody should ask: the ids come from IMDb's episode dataset
				writeJSONTo(w, 200, map[string]any{"imdb_id": "tt7000001"})
				return
			}
		}
		tmdb.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ratingEntry is the shape of a rating in the API.
type ratingEntry struct {
	Source    string  `json:"source"`
	Value     float64 `json:"value"`
	Scale     float64 `json:"scale"`
	Score100  int     `json:"score100"`
	Votes     int     `json:"votes"`
	State     string  `json:"state"`
	URL       string  `json:"url"`
	FetchedAt string  `json:"fetchedAt"`
}

type ratedRow struct {
	ID         int64        `json:"id"`
	Kind       string       `json:"kind"`
	Show       string       `json:"show"`
	MatchState string       `json:"matchState"`
	Rating     *ratingEntry `json:"rating"`
	Ratings    []ratingEntry
	Meta       *struct {
		Ratings []ratingEntry `json:"ratings"`
	} `json:"meta"`
}

func sourcesOf(rs []ratingEntry) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Source
	}
	return out
}

// TestEndToEndRatings runs the real binary against a fake TMDB, a fake IMDb dataset and a fake
// MDBList: a movie and a show are matched, then their ratings arrive by themselves (IMDb's from
// the dataset, which is asked for once, Rotten Tomatoes and the rest from MDBList, within its
// budget), the show's episode gets its IMDb id from IMDb's episode dataset and so its IMDb rating,
// cards show the primary source or the first there is, the lists sort and filter by rating, the key
// and the datasets stay where they belong, and the shutdown is clean.
func TestEndToEndRatings(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	movies := filepath.Join(base, "media", "Movies")
	shows := filepath.Join(base, "media", "Shows")
	data := filepath.Join(base, "data dir", "ply data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	tmdb := newFakeTMDB(t)
	tmdbSrv := tmdbWithRatedShow(t, tmdb)
	imdb := newFakeIMDbDataset(t, map[string]string{"tt0000603": "8.7 2000000", "tt7000001": "9.1 31000"})
	episodes := newFakeIMDbEpisodes(t, "tt7000001 tt6000005 1 1")
	mdb := newFakeMDBListAPI(t)
	bin := buildPly(t, base)

	matrix := filepath.Join(movies, "The Matrix (1999).mp4")
	episode := filepath.Join(shows, "Rated Show", "Season 1", "Rated Show S01E01.mp4")
	makeMovie(t, matrix)
	video, err := os.ReadFile(matrix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(episode), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(episode, video, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	for _, f := range []string{matrix, episode} {
		os.Chtimes(f, old, old)
	}

	p := startPly(t, base, bin, tmp, "-movies", movies, "-shows", shows, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken,
		"-name", e2eName, "-scan-interval", "0", "-hwaccel", "none", "-watch=true",
		"-tmdb-key", e2eTMDBKey, "-tmdb-url", tmdbSrv.URL+"/3", "-tmdb-image-url", tmdb.imageURL(),
		"-mdblist-key", e2eMDBListKey, "-mdblist-url", mdb.srv.URL, "-imdb-ratings-url", imdb.url(), "-imdb-episodes-url", episodes.url())
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	var allBodies strings.Builder
	get := func(target string) []byte {
		t.Helper()
		resp, body := c.do("GET", target, "", bearer)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s = %d %s", target, resp.StatusCode, body)
		}
		allBodies.Write(body)
		return body
	}
	rows := func(q string) []ratedRow {
		t.Helper()
		var out []ratedRow
		mustJSON(t, get("/api/items"+q), &out)
		return out
	}
	rowOf := func(kind string) (ratedRow, bool) {
		for _, r := range rows("?kind=" + kind) {
			return r, true
		}
		return ratedRow{}, false
	}

	// 1. Scanned, matched at TMDB, and then the ratings come on their own.
	c.waitForScan(2)
	took := waitFor(t, "the movie has its IMDb rating, and Rotten Tomatoes'", 90*time.Second, func() bool {
		r, ok := rowOf("movie")
		if !ok {
			return false
		}
		var d ratedRow
		mustJSON(t, get(fmt.Sprintf("/api/items/%d", r.ID)), &d)
		return d.Meta != nil && slices.Contains(sourcesOf(d.Meta.Ratings), "imdb") && slices.Contains(sourcesOf(d.Meta.Ratings), "rt_critics")
	})
	t.Logf("the ratings of a matched movie arrived after %v", took.Round(100*time.Millisecond))
	movie, _ := rowOf("movie")
	var detail ratedRow
	mustJSON(t, get(fmt.Sprintf("/api/items/%d", movie.ID)), &detail)

	// The fixed order, with TMDB's score (it is the title's own) at the end.
	if got, want := sourcesOf(detail.Meta.Ratings), []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt", "tmdb"}; !slices.Equal(got, want) {
		t.Fatalf("ratings = %v, want %v", got, want)
	}
	byName := map[string]ratingEntry{}
	for _, r := range detail.Meta.Ratings {
		byName[r.Source] = r
		if r.FetchedAt == "" || r.URL == "" || r.Scale == 0 {
			t.Errorf("incomplete entry: %+v", r)
		}
	}
	if r := byName["imdb"]; r.Value != 8.7 || r.Votes != 2000000 || r.Score100 != 87 || r.URL != "https://www.imdb.com/title/tt0000603/" {
		t.Errorf("imdb = %+v; it comes from the dataset, with the link to the title", r)
	}
	if r := byName["rt_critics"]; r.Value != 83 || r.State != "fresh" || r.URL != "https://www.rottentomatoes.com/m/x" {
		t.Errorf("rt_critics = %+v", r)
	}
	if r := byName["rt_audience"]; r.Value != 85 || r.State != "upright" {
		t.Errorf("rt_audience = %+v", r)
	}
	if r := byName["letterboxd"]; r.Value != 4.1 || r.Scale != 5 || r.Score100 != 82 {
		t.Errorf("letterboxd = %+v", r)
	}
	if r := byName["tmdb"]; r.Value != 8.1 || r.Votes != 100 || r.URL != "https://www.themoviedb.org/movie/603" {
		t.Errorf("tmdb = %+v", r)
	}
	if movie.Rating == nil || movie.Rating.Source != "imdb" {
		t.Errorf("the card rating = %+v, want IMDb's (the primary source)", movie.Rating)
	}

	// 2. The show has no IMDb rating (not in the ratings dataset): its card shows the first it has in the
	// fixed order. Its episode gets its IMDb id from the episode dataset, and its IMDb rating through
	// the ratings dataset.
	waitFor(t, "the episode has its IMDb rating", 90*time.Second, func() bool {
		for _, r := range rows("?kind=episode") {
			if len(r.Ratings) == 1 {
				return true
			}
		}
		return false
	})
	ep := rows("?kind=episode")[0]
	if len(ep.Ratings) != 1 || ep.Ratings[0].Source != "imdb" || ep.Ratings[0].Value != 9.1 || ep.Ratings[0].Votes != 31000 ||
		ep.Ratings[0].URL != "https://www.imdb.com/title/tt7000001/" {
		t.Errorf("episode ratings = %+v", ep.Ratings)
	}
	if ep.Rating == nil || ep.Rating.Source != "rt_critics" || ep.Rating.Value != 95 {
		t.Errorf("the show's card rating = %+v; with no IMDb rating it is the first it has in the fixed order", ep.Rating)
	}
	if mrow := rows("?kind=movie")[0]; mrow.Ratings != nil {
		t.Errorf("a movie row has a ratings list: %+v", mrow.Ratings)
	}

	// 3. Lists sort and filter by rating (The Matrix is 87, the show 95).
	titleNames := func(q string) []string {
		var cards []struct {
			Title string `json:"title"`
		}
		mustJSON(t, get("/api/titles"+q), &cards)
		var out []string
		for _, c := range cards {
			out = append(out, c.Title)
		}
		return out
	}
	if got := titleNames("?sort=rating"); !slices.Equal(got, []string{"Rated Show", "The Matrix"}) {
		t.Errorf("sort=rating = %v", got)
	}
	if got := titleNames("?minRating=90"); !slices.Equal(got, []string{"Rated Show"}) {
		t.Errorf("minRating=90 = %v", got)
	}
	if got := titleNames("?minRating=96"); len(got) != 0 {
		t.Errorf("minRating=96 = %v", got)
	}
	if resp, _ := c.do("GET", "/api/titles?sort=popularity", "", bearer); resp.StatusCode != 400 {
		t.Errorf("sort=popularity = %d", resp.StatusCode)
	}

	// 4. The primary source is a choice.
	if resp, body := c.do("PATCH", "/api/settings", `{"ratingsPrimary": "trakt"}`, bearer); resp.StatusCode != 200 {
		t.Fatalf("PATCH ratingsPrimary = %d %s", resp.StatusCode, body)
	}
	if m, _ := rowOf("movie"); m.Rating == nil || m.Rating.Source != "trakt" || m.Rating.Value != 86 {
		t.Errorf("with trakt as primary the movie's card shows %+v", m.Rating)
	}

	// 5. Settings: the sources there are, where the dataset's date and the key come from, and the notices.
	var settings struct {
		MDBListKeySet    bool     `json:"mdblistKeySet"`
		MDBListKeySource string   `json:"mdblistKeySource"`
		Primary          string   `json:"ratingsPrimary"`
		Shown            []string `json:"ratingsShown"`
		Available        []string `json:"ratingsAvailable"`
		IMDbAttribution  string   `json:"imdbAttribution"`
		IMDbUpdated      string   `json:"imdbDatasetUpdatedAt"`
		MDBListAttr      string   `json:"mdblistAttribution"`
	}
	mustJSON(t, get("/api/settings"), &settings)
	if !settings.MDBListKeySet || settings.MDBListKeySource != "flag" || settings.Primary != "trakt" ||
		!slices.Equal(settings.Available, []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt", "tmdb"}) ||
		settings.IMDbAttribution != "Information courtesy of IMDb (https://www.imdb.com). Used with permission." ||
		!strings.Contains(settings.MDBListAttr, "MDBList") || len(settings.Shown) != 7 {
		t.Errorf("settings = %+v", settings)
	}
	if at, err := time.Parse(time.RFC3339, settings.IMDbUpdated); err != nil || !at.Equal(time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("imdbDatasetUpdatedAt = %q (%v); want the Last-Modified of the dataset", settings.IMDbUpdated, err)
	}
	// Test checks the key in use.
	resp, body := c.do("POST", "/api/settings/test", `{"provider": "mdblist"}`, bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("POST /api/settings/test mdblist = %d %s", resp.StatusCode, body)
	}
	allBodies.Write(body)

	// 6. What was asked of whom: the dataset once, with no validators; MDBList once about the key's
	// allowance and once for each title, by its TMDB id and nothing else; TMDB never for an episode's id.
	hits, conditional := imdb.counts()
	if hits != 1 || conditional != 0 {
		t.Errorf("the IMDb dataset was asked %d times (%d conditional); once, plainly, however many times the ratings were written", hits, conditional)
	}
	if hits, conditional := episodes.counts(); hits != 1 || conditional != 0 {
		t.Errorf("the IMDb episode dataset was asked %d times (%d conditional); once, plainly", hits, conditional)
	}
	for path, want := range map[string]int{"/tmdb/movie/603": 1, "/tmdb/show/5151": 1, "/user": 2} { // /user: once by the job, once by Test
		if got := mdb.count(path); got != want {
			t.Errorf("MDBList was asked %d times for %s, want %d", got, path, want)
		}
	}
	paths, keys, leak := mdb.seen()
	if len(paths) != 4 {
		t.Errorf("MDBList was asked %d times in all (%v), want 4", len(paths), paths)
	}
	if leak != 0 {
		t.Errorf("%d requests to MDBList carried more than the key", leak)
	}
	if keys != 1 {
		t.Errorf("MDBList was sent %d different keys", keys)
	}
	if n := tmdb.count("episode ids"); n != 0 {
		t.Errorf("TMDB was asked for the episode's ids %d times, want none", n)
	}

	// 7. The datasets are kept for the next time, privately; nothing partial is left.
	cache := filepath.Join(data, "cache", "imdb")
	for _, name := range []string{"title.ratings.tsv.gz", "title.episode.tsv.gz"} {
		fi, err := os.Stat(filepath.Join(cache, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s in the cache: %v, %v", name, fi, err)
		}
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 2 {
		t.Errorf("the cache folder holds %d entries, want only the two datasets", len(entries))
	}

	// 8. The key is nowhere: not in a response, not in the log.
	if strings.Contains(allBodies.String(), e2eMDBListKey) {
		t.Error("the MDBList key appears in an API response")
	}
	if strings.Contains(p.log.String(), e2eMDBListKey) {
		t.Errorf("the MDBList key appears in the server log:\n%s", p.log.String())
	}

	// 9. SIGTERM: a clean exit.
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("ply still running 10 s after SIGTERM\n%s", p.log.String())
	}
	if p.waitErr != nil || p.cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("ply exited with %v (code %d)\n%s", p.waitErr, p.cmd.ProcessState.ExitCode(), p.log.String())
	}
	if log := p.log.String(); strings.Contains(log, "level=ERROR") || strings.Contains(log, "panic") {
		t.Errorf("server log is not that of a clean run:\n%s", log)
	}
}

// TestEndToEndRatingsPrivacy: with lookups off nothing is downloaded, whatever the library holds. The
// movie's IMDb id comes from its .nfo, so it is in the library with lookups off; a first run with
// lookups on is the control (the dataset and MDBList are asked), and the second, on the same data,
// with -metadata=false, asks nothing of anyone, while the ratings it already has are still shown.
func TestEndToEndRatingsPrivacy(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	movies := filepath.Join(base, "media", "Movies")
	data := filepath.Join(base, "data dir", "ply data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildPly(t, base)
	film := filepath.Join(movies, "Some Film (2001).mp4")
	makeMovie(t, film)
	nfo := `<movie><title>Some Film</title><year>2001</year><uniqueid type="imdb" default="true">tt9000001</uniqueid></movie>`
	if err := os.WriteFile(filepath.Join(movies, "Some Film (2001).nfo"), []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(film, old, old)
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}

	run := func(extra ...string) (*ply, *client) {
		t.Helper()
		args := append([]string{"-movies", movies, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-name", e2eName,
			"-scan-interval", "0", "-hwaccel", "none", "-watch=false"}, extra...)
		p := startPly(t, base, bin, tmp, args...)
		return p, &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	}
	stop := func(p *ply) {
		t.Helper()
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			t.Fatalf("ply still running 10 s after SIGTERM\n%s", p.log.String())
		}
	}

	// The control: lookups on (and no TMDB key: the ratings need none). The dataset and MDBList are asked
	// about the NFO's id.
	imdb1 := newFakeIMDbDataset(t, map[string]string{"tt9000001": "7.4 1234"})
	ep1 := newFakeIMDbEpisodes(t)
	mdb1 := newFakeMDBListAPI(t)
	p1, c1 := run("-mdblist-key", e2eMDBListKey, "-mdblist-url", mdb1.srv.URL, "-imdb-ratings-url", imdb1.url(), "-imdb-episodes-url", ep1.url())
	c1.waitForScan(1)
	waitFor(t, "the control run rates the film", 90*time.Second, func() bool {
		_, body := c1.do("GET", "/api/items?kind=movie", "", bearer)
		var list []ratedRow
		mustJSON(t, body, &list)
		return len(list) == 1 && list[0].Rating != nil && list[0].Rating.Source == "imdb" && mdb1.count("/imdb/movie/tt9000001") == 1
	})
	if hits, _ := imdb1.counts(); hits != 1 {
		t.Errorf("control: the dataset was asked %d times", hits)
	}
	if hits, _ := ep1.counts(); hits != 0 {
		t.Errorf("the episode dataset was asked %d times by a library of one film", hits)
	}
	stop(p1)

	// Lookups off, on the same data: nobody is asked, though the film is in the library with an IMDb id.
	imdb2 := newFakeIMDbDataset(t, map[string]string{"tt9000001": "1.0 1"})
	ep2 := newFakeIMDbEpisodes(t)
	mdb2 := newFakeMDBListAPI(t)
	p2, c2 := run("-metadata=false", "-mdblist-key", e2eMDBListKey, "-mdblist-url", mdb2.srv.URL, "-imdb-ratings-url", imdb2.url(), "-imdb-episodes-url", ep2.url())
	c2.waitForScan(1)
	// Long enough for the schedule's first look and the first tick of the metadata pass.
	time.Sleep(6 * time.Second)
	resp, body := c2.do("GET", "/api/items?kind=movie", "", bearer)
	var list []ratedRow
	mustJSON(t, body, &list)
	if resp.StatusCode != 200 || len(list) != 1 || list[0].Rating == nil || list[0].Rating.Source != "imdb" || list[0].Rating.Value != 7.4 {
		t.Errorf("what was stored is still shown with lookups off: %s", body)
	}
	if hits, _ := imdb2.counts(); hits != 0 {
		t.Errorf("-metadata=false: the dataset was asked %d times", hits)
	}
	if hits, _ := ep2.counts(); hits != 0 {
		t.Errorf("-metadata=false: the episode dataset was asked %d times", hits)
	}
	if paths, _, _ := mdb2.seen(); len(paths) != 0 {
		t.Errorf("-metadata=false: MDBList was asked %d times (%v)", len(paths), paths)
	}
	_, body = c2.do("GET", "/api/settings", "", bearer)
	if !strings.Contains(string(body), `"ratingsAvailable":[]`) {
		t.Errorf("settings with lookups off: %s", body)
	}
	stop(p2)
	if strings.Contains(p1.log.String()+p2.log.String(), e2eMDBListKey) {
		t.Error("the MDBList key appears in a server log")
	}
}
