//go:build integration && unix

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// e2eTVDBKey is made up. The fake TVDB logs in with it, and the test looks for it everywhere.
const e2eTVDBKey = "e2e-tvdb-0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"

const tvdbFixtures = "../../internal/metadata/testdata/tvdb/"

// fakeTVDB is a TVDB that logs in with e2eTVDBKey (with no PIN), hands out a token, wants it on
// every other request and answers from the fixtures in internal/metadata/testdata/tvdb: the
// made-up show 900001 "Test Anime", aired and absolute. A search for anything else finds nothing.
type fakeTVDB struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	hits     map[string]int
	logins   []string // the bodies
	leaked   int      // requests with the key in the URL
	badToken int
	token    string
}

func newFakeTVDB(t *testing.T) *fakeTVDB {
	t.Helper()
	f := &fakeTVDB{t: t, hits: map[string]int{}, token: "e2e.tvdb.token.0123456789"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTVDB) apiURL() string   { return f.srv.URL + "/v4" }
func (f *fakeTVDB) imageURL() string { return f.srv.URL + "/img" }

func (f *fakeTVDB) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, v := range f.hits {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

func (f *fakeTVDB) fixture(name string) []byte {
	b, err := os.ReadFile(tvdbFixtures + name)
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
	}
	return b
}

func (f *fakeTVDB) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if strings.Contains(r.URL.String(), e2eTVDBKey) {
		f.leaked++
	}
	f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/img/") {
		f.hit("image " + r.URL.Path)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(e2eJPEG)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v4")
	w.Header().Set("Content-Type", "application/json")
	if path == "/login" {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.logins = append(f.logins, string(body))
		f.mu.Unlock()
		f.hit("/login")
		if !strings.Contains(string(body), `"apikey":"`+e2eTVDBKey+`"`) {
			w.WriteHeader(401)
			w.Write(f.fixture("login_401.json"))
			return
		}
		w.Write([]byte(strings.ReplaceAll(string(f.fixture("login.json")), "{{token}}", f.token)))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.mu.Lock()
		f.badToken++
		f.mu.Unlock()
		w.WriteHeader(401)
		w.Write(f.fixture("error_401.json"))
		return
	}
	f.hit(path + "?" + r.URL.RawQuery)
	q := r.URL.Query()
	switch {
	case path == "/search":
		switch strings.ToLower(q.Get("query")) {
		case "test anime", "test-anime":
			w.Write([]byte(`{"status":"success","data":[{"tvdb_id":"900001","name":"Test Anime","slug":"test-anime","year":"2020",
				"overview":"A fictional series for ply's tests.","image_url":"https://artworks.thetvdb.com/banners/posters/900001-1.jpg",
				"primary_type":"series","type":"series","translations":{"eng":"Test Anime"},"overviews":{},"remote_ids":[]}]}`))
		default:
			w.Write(f.fixture("search_empty.json"))
		}
	case path == "/series/900001/extended":
		w.Write(f.fixture("series_900001_extended.json"))
	case path == "/series/900001/episodes/official/eng":
		w.Write(f.fixture("episodes_900001_official_eng.json"))
	case path == "/series/900001/episodes/absolute/eng":
		w.Write(f.fixture("episodes_900001_absolute_eng.json"))
	case path == "/people/296140/extended":
		w.Write(f.fixture("person_296140.json"))
	case path == "/updates":
		w.Write([]byte(`{"status":"success","data":[],"links":{"next":null,"total_items":0}}`))
	default:
		w.WriteHeader(404)
		w.Write(f.fixture("error_404.json"))
	}
}

func (f *fakeTVDB) hit(key string) {
	f.mu.Lock()
	f.hits[key]++
	f.mu.Unlock()
}

// tmdbWithShows is the fake TMDB of the other e2e (three movies) that also knows one show, "Fallback
// Show" (4242), which TVDB does not.
func tmdbWithShows(t *testing.T, tmdb *fakeTMDB) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if r.URL.Query().Get("api_key") == e2eTMDBKey {
			switch {
			case p == "/3/search/tv":
				tmdb.hit("search tv " + strings.ToLower(r.URL.Query().Get("query")))
				var res []map[string]any
				if strings.EqualFold(r.URL.Query().Get("query"), "Fallback Show") {
					res = append(res, map[string]any{"id": 4242, "name": "Fallback Show", "original_name": "Fallback Show",
						"first_air_date": "2015-05-05", "overview": "Only TMDB knows it.", "popularity": 50.0, "poster_path": "/fb.jpg"})
				}
				writeJSONTo(w, 200, map[string]any{"page": 1, "results": res})
				return
			case p == "/3/tv/4242":
				tmdb.hit("tv 4242")
				writeJSONTo(w, 200, map[string]any{"id": 4242, "name": "Fallback Show", "original_name": "Fallback Show",
					"first_air_date": "2015-05-05", "overview": "Only TMDB knows it.", "status": "Ended", "poster_path": "/fb.jpg",
					"external_ids": map[string]any{"tvdb_id": 0}})
				return
			case p == "/3/tv/4242/season/1":
				writeJSONTo(w, 200, map[string]any{"season_number": 1, "name": "Season 1",
					"episodes": []map[string]any{{"episode_number": 1, "name": "TMDB pilot"}}})
				return
			}
		}
		tmdb.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type showView struct {
	Show       string `json:"show"`
	MatchState string `json:"matchState"`
	Poster     string `json:"poster"`
	Meta       *struct {
		ID           int64  `json:"id"`
		Title        string `json:"title"`
		Source       string `json:"source"`
		EpisodeOrder string `json:"episodeOrder"`
		TVDBID       int64  `json:"tvdbId"`
		TMDBID       int64  `json:"tmdbId"`
	} `json:"meta"`
}

// TestEndToEndTVDB runs the real binary against a fake TMDB and a fake TVDB: shows are matched at
// TVDB (a show it does not know, at TMDB), movies at TMDB; the key is in use and never shown; the
// show's artwork is served from the TVDB tree; a show is fixed with an address at thetvdb.com; the
// episode order is changed and the files find other episodes; the TVDB key and the images stay
// where they belong; and the shutdown is clean.
func TestEndToEndTVDB(t *testing.T) {
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
	tmdbSrv := tmdbWithShows(t, tmdb)
	tvdb := newFakeTVDB(t)
	bin := buildPly(t, base)

	matrix := filepath.Join(movies, "The Matrix (1999).mp4")
	makeMovie(t, matrix)
	video, err := os.ReadFile(matrix)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	files := []string{matrix}
	for _, rel := range []string{"Test Anime/Season 1/Test Anime S01E01.mp4", "Test Anime/Season 1/Test Anime S01E03.mp4", "Fallback Show/Season 1/Fallback Show S01E01.mp4"} {
		p := filepath.Join(shows, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, video, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	for _, f := range files {
		os.Chtimes(f, old, old)
	}

	p := startPly(t, base, bin, tmp, "-movies", movies, "-shows", shows, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken,
		"-name", e2eName, "-scan-interval", "0", "-hwaccel", "none", "-watch=true",
		"-tmdb-key", e2eTMDBKey, "-tmdb-url", tmdbSrv.URL+"/3", "-tmdb-image-url", tmdb.imageURL(),
		"-tvdb-key", e2eTVDBKey, "-tvdb-url", tvdb.apiURL(), "-tvdb-image-url", tvdb.imageURL())
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	var allBodies strings.Builder
	get := func(target string) (*http.Response, []byte) {
		t.Helper()
		resp, body := c.do("GET", target, "", bearer)
		allBodies.Write(body)
		return resp, body
	}
	show := func(name string) showView {
		t.Helper()
		_, body := get("/api/shows/" + url.PathEscape(name))
		var s showView
		mustJSON(t, body, &s)
		return s
	}

	// 1. Scanned, then matched on their own: each show where it belongs.
	c.waitForScan(4)
	waitFor(t, "both shows are matched", 40*time.Second, func() bool {
		return show("Test Anime").MatchState == "matched" && show("Fallback Show").MatchState == "matched"
	})
	anime, fallback := show("Test Anime"), show("Fallback Show")
	if anime.Meta == nil || anime.Meta.Source != "tvdb" || anime.Meta.EpisodeOrder != "aired" || anime.Meta.TVDBID != 900001 || anime.Meta.Title != "Test Anime" {
		t.Fatalf("Test Anime = %+v / %+v", anime, anime.Meta)
	}
	if fallback.Meta == nil || fallback.Meta.Source != "tmdb" || fallback.Meta.TMDBID != 4242 || fallback.Meta.EpisodeOrder != "" { // a show from TMDB has no order to show
		t.Fatalf("Fallback Show = %+v / %+v", fallback, fallback.Meta)
	}
	if tvdb.count("/search?query=Fallback") == 0 {
		t.Error("TVDB was not asked first about the show it does not know")
	}
	if n := tmdb.count("search tv test anime") + tmdb.count("tv 900001"); n != 0 {
		t.Errorf("TMDB was asked %d times about a show TVDB matched", n)
	}
	waitFor(t, "the movie is matched at TMDB", 30*time.Second, func() bool {
		_, body := get("/api/items?kind=movie")
		return strings.Contains(string(body), `"matchState":"matched"`)
	})
	if tvdb.count("/search?query=The") != 0 {
		t.Error("TVDB was asked about a movie")
	}

	// 2. The artwork of the TVDB show is served from the TVDB tree, from the cache.
	if !strings.HasPrefix(anime.Poster, "/api/images/tvdb/w342/banners/posters/900001-1.jpg") {
		t.Fatalf("poster = %q", anime.Poster)
	}
	waitFor(t, "the poster is prefetched", 30*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(data, "images", "tvdb", "w342", "banners", "posters", "900001-1.jpg"))
		return err == nil
	})
	resp, body := c.do("GET", anime.Poster, "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || string(body) != string(e2eJPEG) ||
		resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("poster = %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if n := tvdb.count("image /img/banners/posters/900001-1_t.jpg"); n != 1 {
		t.Errorf("the small poster was fetched %d times, want once (the thumbnail, prefetched, then cached)", n)
	}
	images := tvdb.count("image ")
	for _, bad := range []string{"/api/images/tvdb/w342/banners/..%2f..%2fx.jpg", "/api/images/tvdb/w100/banners/posters/900001-1.jpg",
		"/api/images/tvdb/w342/banners/posters/900001-1.svg", "/api/images/tvdb/w342/%2e%2e/%2e%2e/etc/passwd"} {
		if resp, _ := c.do("GET", bad, "", bearer); resp.StatusCode != 400 && resp.StatusCode != 404 && resp.StatusCode != 301 && resp.StatusCode != 307 {
			t.Errorf("GET %s = %d", bad, resp.StatusCode)
		}
	}
	if tvdb.count("image ") != images {
		t.Error("a path that is not allowed reached the image server")
	}
	if resp, _ := c.do("GET", strings.Split(anime.Poster, "?")[0], "", nil); resp.StatusCode != 401 {
		t.Errorf("poster without a token = %d, want 401", resp.StatusCode)
	}

	// 3. Settings and status: the key is in use, from the flag, and never shown; no PIN.
	resp, body = get("/api/settings")
	var settings struct {
		KeySet      bool   `json:"tvdbKeySet"`
		PINSet      bool   `json:"tvdbPinSet"`
		Source      string `json:"tvdbKeySource"`
		Attribution string `json:"tvdbAttribution"`
	}
	mustJSON(t, body, &settings)
	if resp.StatusCode != 200 || !settings.KeySet || settings.PINSet || settings.Source != "flag" || !strings.Contains(settings.Attribution, "TheTVDB") {
		t.Errorf("settings = %s", body)
	}
	_, body = get("/api/metadata/status")
	if !strings.Contains(string(body), `"showsLine":"Shows: TVDB"`) || !strings.Contains(string(body), `"tvdbKeySet":true`) {
		t.Errorf("status = %s", body)
	}
	if len(tvdb.logins) == 0 || strings.Contains(strings.Join(tvdb.logins, "|"), `"pin"`) {
		t.Errorf("logins = %q: with no PIN the login has no pin", tvdb.logins)
	}

	// 4. The episode order: S01E03 is no episode in the aired order, and is one in the absolute.
	var epID int64
	_, body = get("/api/items?kind=episode")
	var eps []struct {
		ID      int64  `json:"id"`
		Show    string `json:"show"`
		Episode int    `json:"episode"`
	}
	mustJSON(t, body, &eps)
	for _, e := range eps {
		if e.Show == "Test Anime" && e.Episode == 3 {
			epID = e.ID
		}
	}
	if epID == 0 {
		t.Fatalf("no S01E03 of Test Anime in %s", body)
	}
	epInfo := func() string {
		_, body := get(fmt.Sprintf("/api/items/%d", epID))
		var d struct {
			EpisodeInfo *struct {
				Name string `json:"name"`
			} `json:"episodeInfo"`
		}
		mustJSON(t, body, &d)
		if d.EpisodeInfo == nil {
			return ""
		}
		return d.EpisodeInfo.Name
	}
	if got := epInfo(); got != "" {
		t.Errorf("S01E03 in the aired order = %q, want no episode", got)
	}
	resp, body = c.do("PATCH", fmt.Sprintf("/api/titles/%d", anime.Meta.ID), `{"episodeOrder": "absolute"}`, bearer)
	allBodies.Write(body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"episodeOrder":"absolute"`) || !strings.Contains(string(body), `"source":"tvdb"`) {
		t.Fatalf("PATCH episodeOrder = %d %s", resp.StatusCode, body)
	}
	if got := epInfo(); got != "Return" {
		t.Errorf("S01E03 in the absolute order = %q, want Return", got)
	}
	if tvdb.count("/series/900001/episodes/absolute/eng") == 0 {
		t.Error("TVDB was not asked for the absolute order")
	}
	if resp, _ := c.do("PATCH", fmt.Sprintf("/api/titles/%d", anime.Meta.ID), `{"episodeOrder": "sideways"}`, bearer); resp.StatusCode != 400 {
		t.Errorf("a bad order = %d, want 400", resp.StatusCode)
	}
	if resp, _ := c.do("PATCH", fmt.Sprintf("/api/titles/%d", fallback.Meta.ID), `{"episodeOrder": "dvd"}`, bearer); resp.StatusCode != 400 {
		t.Errorf("an order for a show from TMDB = %d, want 400", resp.StatusCode)
	}

	// 5. Fix match with an address at thetvdb.com: searched, then applied by hand to the other show.
	resp, body = get("/api/metadata/search?kind=series&q=" + url.QueryEscape("https://thetvdb.com/series/test-anime"))
	var found []struct {
		Source string `json:"source"`
		TVDBID int64  `json:"tvdbId"`
		Title  string `json:"title"`
	}
	mustJSON(t, body, &found)
	if resp.StatusCode != 200 || len(found) != 1 || found[0].Source != "tvdb" || found[0].TVDBID != 900001 || found[0].Title != "Test Anime" {
		t.Fatalf("search by address = %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("PUT", "/api/shows/Fallback%20Show/match", `{"tvdb": "https://thetvdb.com/series/test-anime"}`, bearer)
	allBodies.Write(body)
	var fixed showView
	mustJSON(t, body, &fixed)
	if resp.StatusCode != 200 || fixed.MatchState != "manual" || fixed.Meta == nil || fixed.Meta.Source != "tvdb" || fixed.Meta.TVDBID != 900001 {
		t.Fatalf("PUT match by address = %d %s", resp.StatusCode, body)
	}
	if resp, _ := c.do("PUT", "/api/shows/Fallback%20Show/match", `{"tvdb": "https://evil.example/series/test-anime"}`, bearer); resp.StatusCode != 400 {
		t.Errorf("an address elsewhere = %d, want 400", resp.StatusCode)
	}
	// A match by hand is not undone by the worker.
	time.Sleep(1500 * time.Millisecond)
	if s := show("Fallback Show"); s.MatchState != "manual" || s.Meta == nil || s.Meta.Source != "tvdb" {
		t.Errorf("after a few worker passes: %+v", s)
	}

	// 6. The keys are nowhere: not in a response, not in the log, not in a URL the fake TVDB saw.
	if strings.Contains(allBodies.String(), e2eTVDBKey) || strings.Contains(allBodies.String(), e2eTMDBKey) {
		t.Error("a key appears in an API response")
	}
	if strings.Contains(p.log.String(), e2eTVDBKey) || strings.Contains(p.log.String(), e2eTMDBKey) {
		t.Errorf("a key appears in the server log:\n%s", p.log.String())
	}
	if tvdb.leaked != 0 || tvdb.badToken != 0 {
		t.Errorf("TVDB saw the key in %d URLs and a wrong token %d times", tvdb.leaked, tvdb.badToken)
	}
	if tmdb.bad != 0 {
		t.Errorf("TMDB was sent a wrong key %d times", tmdb.bad)
	}
	if len(mentioning(t, base)) == 0 {
		t.Fatal("positive control: the process scan finds nothing that mentions the test directory")
	}

	// 7. SIGTERM: a clean exit, and nothing left behind.
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
	if log := p.log.String(); !strings.Contains(log, "shutting down") || strings.Contains(log, "level=ERROR") || strings.Contains(log, "panic") {
		t.Errorf("server log is not that of a clean run:\n%s", log)
	}
	if left := mentioning(t, base); len(left) != 0 {
		t.Errorf("processes still reference %s: %v", base, left)
	}
}
