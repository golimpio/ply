//go:build integration && unix

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const e2eTMDBKey = "e2e0123456789abcdef0123456789abcd"

var e2eJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("e2e-poster-bytes")...)

type tmdbMovie struct {
	id       int
	title    string
	year     int
	overview string
	cast     []tmdbPerson
	director tmdbPerson
}

type tmdbPerson struct {
	id   int
	name string
}

var (
	keanu     = tmdbPerson{6384, "Keanu Reeves"}
	dicaprio  = tmdbPerson{6193, "Leonardo DiCaprio"}
	chalamet  = tmdbPerson{1190668, "Timothée Chalamet"}
	wachowski = tmdbPerson{9339, "Lana Wachowski"}
	nolan     = tmdbPerson{525, "Christopher Nolan"}
	villeneuv = tmdbPerson{137427, "Denis Villeneuve"}

	tmdbMovies = []tmdbMovie{
		{603, "The Matrix", 1999, "A hacker learns the truth.", []tmdbPerson{keanu}, wachowski},
		{27205, "Inception", 2010, "A thief enters dreams.", []tmdbPerson{dicaprio}, nolan},
		{438631, "Dune", 2021, "Paul Atreides travels to Arrakis.", []tmdbPerson{chalamet}, villeneuv},
	}
)

// fakeTMDB is a TMDB that answers like the real one for three movies, wants the
// key, and, when it is not given, echoes whatever key it was sent in the error
// (the worst case for a server that must never repeat the key).
type fakeTMDB struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	bad  int
}

func newFakeTMDB(t *testing.T) *fakeTMDB {
	t.Helper()
	f := &fakeTMDB{hits: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTMDB) apiURL() string   { return f.srv.URL + "/3" }
func (f *fakeTMDB) imageURL() string { return f.srv.URL + "/img" }

func (f *fakeTMDB) hit(key string) {
	f.mu.Lock()
	f.hits[key]++
	f.mu.Unlock()
}

func (f *fakeTMDB) count(prefix string) int {
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

func writeJSONTo(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeTMDB) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasPrefix(path, "/img/") {
		f.hit("image " + path)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(e2eJPEG)
		return
	}
	if r.URL.Query().Get("api_key") != e2eTMDBKey {
		f.mu.Lock()
		f.bad++
		f.mu.Unlock()
		writeJSONTo(w, 401, map[string]any{"status_code": 7,
			"status_message": "Invalid API key: " + r.URL.Query().Get("api_key") + " is not valid."})
		return
	}
	f.hit(strings.TrimPrefix(path, "/3"))
	switch {
	case path == "/3/configuration":
		writeJSONTo(w, 200, map[string]any{"images": map[string]any{"secure_base_url": "https://image.tmdb.org/t/p/"}})
	case path == "/3/search/movie":
		q := strings.ToLower(r.URL.Query().Get("query"))
		var results []map[string]any
		for _, m := range tmdbMovies {
			if strings.ToLower(m.title) == q {
				results = append(results, map[string]any{"id": m.id, "title": m.title, "original_title": m.title,
					"release_date": fmt.Sprintf("%d-01-01", m.year), "overview": m.overview, "popularity": 50.0,
					"poster_path": fmt.Sprintf("/p%d.jpg", m.id)})
			}
		}
		writeJSONTo(w, 200, map[string]any{"page": 1, "results": results})
	case strings.HasPrefix(path, "/3/movie/"):
		for _, m := range tmdbMovies {
			if path == fmt.Sprintf("/3/movie/%d", m.id) {
				var cast []map[string]any
				for i, p := range m.cast {
					cast = append(cast, map[string]any{"id": p.id, "name": p.name, "character": "Lead", "order": i,
						"profile_path": fmt.Sprintf("/pr%d.jpg", p.id)})
				}
				writeJSONTo(w, 200, map[string]any{
					"id": m.id, "title": m.title, "original_title": m.title, "overview": m.overview, "tagline": "Tagline.",
					"release_date": fmt.Sprintf("%d-01-01", m.year), "runtime": 120, "status": "Released", "vote_average": 8.1,
					"vote_count": 100, "poster_path": fmt.Sprintf("/p%d.jpg", m.id), "backdrop_path": fmt.Sprintf("/b%d.jpg", m.id),
					"genres":  []map[string]any{{"id": 878, "name": "Science Fiction"}},
					"imdb_id": fmt.Sprintf("tt%07d", m.id),
					"release_dates": map[string]any{"results": []map[string]any{{"iso_3166_1": "US",
						"release_dates": []map[string]any{{"certification": "PG-13", "type": 3}}}}},
					"credits": map[string]any{"cast": cast, "crew": []map[string]any{{"id": m.director.id, "name": m.director.name,
						"job": "Director"}}},
					"images": map[string]any{"logos": []any{}},
				})
				return
			}
		}
		writeJSONTo(w, 404, map[string]any{"status_code": 34, "status_message": "The resource you requested could not be found."})
	case strings.HasPrefix(path, "/3/person/"):
		for _, m := range tmdbMovies {
			for _, p := range append(m.cast, m.director) {
				if path == fmt.Sprintf("/3/person/%d", p.id) {
					writeJSONTo(w, 200, map[string]any{"id": p.id, "name": p.name, "biography": "Biography of " + p.name,
						"birthday": "1970-01-01", "place_of_birth": "Somewhere", "known_for_department": "Acting",
						"profile_path": fmt.Sprintf("/pr%d.jpg", p.id), "imdb_id": "nm0000001"})
					return
				}
			}
		}
		writeJSONTo(w, 404, map[string]any{"status_code": 34, "status_message": "The resource you requested could not be found."})
	default:
		writeJSONTo(w, 404, map[string]any{"status_code": 34, "status_message": "The resource you requested could not be found."})
	}
}

// makeMovie writes a two second mp4 with video and audio.
func makeMovie(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
}

type metaItem struct {
	item
	TitleID     *int64 `json:"titleId"`
	Poster      string `json:"poster"`
	Backdrop    string `json:"backdrop"`
	MatchState  string `json:"matchState"`
	NeedsReview bool   `json:"needsReview"`
	Meta        *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Cast  []struct {
			PersonID int64  `json:"personId"`
			Name     string `json:"name"`
			Profile  string `json:"profile"`
		} `json:"cast"`
		Crew []struct {
			Name string `json:"name"`
			Job  string `json:"job"`
		} `json:"crew"`
		Source string `json:"source"`
	} `json:"meta"`
}

// waitFor polls until fn returns true.
func waitFor(t *testing.T, what string, timeout time.Duration, fn func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !fn() {
		if time.Since(start) > timeout {
			t.Fatalf("%s: not after %v", what, timeout)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return time.Since(start)
}

// TestEndToEndMetadata runs the real binary against a fake TMDB: a scanned file
// is matched with poster and cast, the match is fixed through the API, the
// person endpoint lists the file, images are served, a file dropped in the
// media folder appears matched by itself, and the shutdown is clean.
func TestEndToEndMetadata(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media root")
	data := filepath.Join(base, "data dir", "ply data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	tmdb := newFakeTMDB(t)
	bin := buildPly(t, base)

	matrixPath := filepath.Join(media, "Movies", "The Matrix (1999).mp4")
	makeMovie(t, matrixPath)
	// Old enough that the scanner's settle time does not hold it back.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(matrixPath, old, old); err != nil {
		t.Fatal(err)
	}

	p := startPly(t, base, bin, tmp, "-media", media, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken,
		"-name", e2eName, "-scan-interval", "0", "-hwaccel", "none", "-watch=true",
		"-tmdb-key", e2eTMDBKey, "-tmdb-url", tmdb.apiURL(), "-tmdb-image-url", tmdb.imageURL())
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	var allBodies strings.Builder // everything the API said, to look for the key in
	get := func(target string) (*http.Response, []byte) {
		t.Helper()
		resp, body := c.do("GET", target, "", bearer)
		allBodies.Write(body)
		return resp, body
	}
	list := func() []metaItem {
		t.Helper()
		_, body := get("/api/items?kind=movie")
		var items []metaItem
		mustJSON(t, body, &items)
		return items
	}
	find := func(title string) (metaItem, bool) {
		for _, it := range list() {
			if it.Title == title {
				return it, true
			}
		}
		return metaItem{}, false
	}

	// 1. Scanned, then matched on its own, with a poster and the cast.
	c.waitForScan(1)
	waitFor(t, "the movie is matched", 30*time.Second, func() bool {
		it, ok := find("The Matrix")
		return ok && it.MatchState == "matched"
	})
	matrix, _ := find("The Matrix")
	if matrix.TitleID == nil || matrix.Poster == "" || matrix.Backdrop == "" || matrix.NeedsReview {
		t.Fatalf("matched movie = %+v", matrix)
	}
	resp, body := get(fmt.Sprintf("/api/items/%d", matrix.ID))
	var det metaItem
	mustJSON(t, body, &det)
	if resp.StatusCode != 200 || det.Meta == nil || det.Meta.Title != "The Matrix" || det.Meta.Source != "tmdb" ||
		len(det.Meta.Cast) != 1 || det.Meta.Cast[0].Name != "Keanu Reeves" || det.Meta.Cast[0].Profile == "" ||
		len(det.Meta.Crew) != 1 || det.Meta.Crew[0].Job != "Director" {
		t.Fatalf("detail meta = %s", body)
	}
	keanuID := det.Meta.Cast[0].PersonID

	// 2. The image endpoint serves the poster, from the cache after the worker fetched it.
	resp, body = c.do("GET", matrix.Poster, "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || string(body) != string(e2eJPEG) ||
		resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("poster %s = %d %s %q", matrix.Poster, resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := c.do("GET", strings.Split(matrix.Poster, "?")[0], "", nil); resp.StatusCode != 401 {
		t.Errorf("poster without a token = %d, want 401", resp.StatusCode)
	}
	if n := tmdb.count("image /img/w342/p603.jpg"); n != 1 {
		t.Errorf("the poster was downloaded %d times, want once (prefetched, then cached)", n)
	}
	if _, err := os.Stat(filepath.Join(data, "images", "tmdb", "w1280", "b603.jpg")); err != nil {
		t.Errorf("the backdrop was not prefetched: %v", err)
	}
	if resp, _ := c.do("GET", "/api/images/tmdb/w100/p603.jpg", "", bearer); resp.StatusCode != 400 {
		t.Errorf("a size off the allowlist = %d, want 400", resp.StatusCode)
	}
	if resp, _ := c.do("GET", "/api/images/tmdb/w342/..%2F..%2Fx.jpg", "", bearer); resp.StatusCode != 400 && resp.StatusCode != 404 {
		t.Errorf("a traversal attempt = %d", resp.StatusCode)
	}

	// 3. The person endpoint lists the file (and fetches the person's details on first use).
	resp, body = get(fmt.Sprintf("/api/people/%d", keanuID))
	var person struct {
		Name      string `json:"name"`
		Biography string `json:"biography"`
		Credits   []struct {
			TitleID int64 `json:"titleId"`
			Title   struct {
				ItemID *int64 `json:"itemId"`
			} `json:"title"`
		} `json:"credits"`
	}
	mustJSON(t, body, &person)
	if resp.StatusCode != 200 || person.Name != "Keanu Reeves" || person.Biography != "Biography of Keanu Reeves" ||
		len(person.Credits) != 1 || person.Credits[0].Title.ItemID == nil || *person.Credits[0].Title.ItemID != matrix.ID {
		t.Fatalf("person = %d %s", resp.StatusCode, body)
	}
	resp, body = get("/api/people?q=keanu")
	if !strings.Contains(string(body), `"Keanu Reeves"`) {
		t.Errorf("people search = %s", body)
	}

	// 4. Fix the match through the API: the file becomes Inception, by hand.
	resp, body = c.do("PUT", fmt.Sprintf("/api/items/%d/match", matrix.ID), `{"tmdbId": 27205}`, bearer)
	allBodies.Write(body)
	var fixed metaItem
	mustJSON(t, body, &fixed)
	if resp.StatusCode != 200 || fixed.Meta == nil || fixed.Meta.Title != "Inception" || fixed.MatchState != "manual" ||
		len(fixed.Meta.Cast) != 1 || fixed.Meta.Cast[0].Name != "Leonardo DiCaprio" {
		t.Fatalf("PUT match = %d %s", resp.StatusCode, body)
	}
	dicaprioID := fixed.Meta.Cast[0].PersonID
	resp, body = get(fmt.Sprintf("/api/people/%d", dicaprioID))
	person.Credits = nil
	mustJSON(t, body, &person)
	if resp.StatusCode != 200 || len(person.Credits) != 1 || person.Credits[0].Title.ItemID == nil || *person.Credits[0].Title.ItemID != matrix.ID {
		t.Errorf("the new cast member's credits = %d %s", resp.StatusCode, body)
	}
	// The old title and its people are gone (pruned): absence, after the positive control in step 3.
	if resp, _ := c.do("GET", fmt.Sprintf("/api/people/%d", keanuID), "", bearer); resp.StatusCode != 404 {
		t.Errorf("a person only the old title had = %d, want 404", resp.StatusCode)
	}
	// A match made by hand is not undone by the worker.
	time.Sleep(1500 * time.Millisecond)
	if it, _ := find("The Matrix"); it.MatchState != "manual" {
		t.Errorf("after a few worker passes: %+v", it)
	}
	// Bad input is a 400, an unknown id a 404.
	if resp, _ := c.do("PUT", fmt.Sprintf("/api/items/%d/match", matrix.ID), `{"tmdbId": 424242}`, bearer); resp.StatusCode != 400 {
		t.Errorf("TMDB has nothing at that id: %d, want 400", resp.StatusCode)
	}
	if resp, _ := c.do("PUT", "/api/items/99999/match", `{"tmdbId": 603}`, bearer); resp.StatusCode != 404 {
		t.Errorf("unknown item: %d, want 404", resp.StatusCode)
	}

	// 5. Settings and status: the key is in use and never shown.
	resp, body = get("/api/settings")
	var settings struct {
		KeySet bool   `json:"tmdbKeySet"`
		Source string `json:"tmdbKeySource"`
	}
	mustJSON(t, body, &settings)
	if resp.StatusCode != 200 || !settings.KeySet || settings.Source != "flag" {
		t.Errorf("settings = %s", body)
	}
	resp, body = get("/api/metadata/status")
	if !strings.Contains(string(body), `"keySet":true`) || !strings.Contains(string(body), `"enabled":true`) {
		t.Errorf("status = %s", body)
	}

	// 6. Live pickup: drop a file into the media folder, without asking for a scan.
	dropped := filepath.Join(media, "Movies", "Dune (2021).mp4")
	scansBefore := tmdb.count("/search/movie")
	data1, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropped, data1, 0o644); err != nil {
		t.Fatal(err)
	}
	took := waitFor(t, "the dropped file is listed, matched and has a poster", 40*time.Second, func() bool {
		it, ok := find("Dune")
		return ok && it.MatchState == "matched" && it.Poster != ""
	})
	t.Logf("a file dropped into the media folder was matched after %v", took.Round(100*time.Millisecond))
	if took > 30*time.Second {
		t.Errorf("live pickup took %v, want about 15 s", took)
	}
	if tmdb.count("/search/movie") <= scansBefore {
		t.Error("the fake TMDB was not asked about the new file")
	}
	dune, _ := find("Dune")
	if resp, _ := c.do("GET", dune.Poster, "", nil); resp.StatusCode != 200 {
		t.Errorf("poster of the dropped file = %d", resp.StatusCode)
	}
	// GET /api/scan reports the watcher.
	_, body = get("/api/scan")
	var scan struct {
		Watching []struct {
			Root     string `json:"root"`
			Watching bool   `json:"watching"`
			Dirs     int    `json:"dirs"`
		} `json:"watching"`
	}
	mustJSON(t, body, &scan)
	if len(scan.Watching) != 1 || scan.Watching[0].Root != media || !scan.Watching[0].Watching || scan.Watching[0].Dirs < 2 {
		t.Errorf("scan status watching = %s", body)
	}

	// 7. And a deleted file disappears from the library, and from the title lists.
	if err := os.Remove(dropped); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the deleted file is gone", 40*time.Second, func() bool {
		_, ok := find("Dune")
		return !ok
	})
	_, body = get("/api/titles")
	if strings.Contains(string(body), `"Dune"`) {
		t.Errorf("a title with no file is still listed: %s", body)
	}
	if !strings.Contains(string(body), `"Inception"`) {
		t.Errorf("positive control: the remaining title is not listed: %s", body)
	}

	// 8. The key is nowhere: not in any response, not in the server's log, and
	// TMDB was never asked with a wrong one.
	if strings.Contains(allBodies.String(), e2eTMDBKey) {
		t.Error("the TMDB key appears in an API response")
	}
	if strings.Contains(p.log.String(), e2eTMDBKey) {
		t.Errorf("the TMDB key appears in the server log:\n%s", p.log.String())
	}
	if tmdb.bad != 0 {
		t.Errorf("TMDB was sent a wrong key %d times", tmdb.bad)
	}
	ffmpegsRunning := len(mentioning(t, base))
	if ffmpegsRunning == 0 {
		t.Fatal("positive control: the process scan finds nothing that mentions the test directory")
	}

	// 9. SIGTERM: a clean exit, and nothing left behind.
	start := time.Now()
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
	t.Logf("exited %s after SIGTERM", time.Since(start).Round(time.Millisecond))
	if log := p.log.String(); !strings.Contains(log, "shutting down") || strings.Contains(log, "level=ERROR") || strings.Contains(log, "panic") {
		t.Errorf("server log is not that of a clean run:\n%s", log)
	}
	if left := mentioning(t, base); len(left) != 0 {
		t.Errorf("processes still reference %s: %v", base, left)
	}
	var leftovers []string
	filepath.WalkDir(filepath.Join(data, "images"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasPrefix(d.Name(), ".dl-") || strings.HasSuffix(d.Name(), ".tmp.jpg")) {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if len(leftovers) != 0 {
		t.Errorf("partial downloads left behind: %v", leftovers)
	}
}
