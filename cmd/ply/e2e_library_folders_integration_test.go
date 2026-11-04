//go:build integration && unix

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"ply/internal/store"
)

// folderStatus is a folder as GET /api/folders reports it, with the counts.
type folderStatus struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Guessed    bool   `json:"guessed"`
	Items      int    `json:"items"`
	LastScanAt int64  `json:"lastScanAt"`
}

// reviewFlag reads the needs_review flag of the item of a show straight from the
// server's database (the API shows an episode's show flag, not its own).
func reviewFlag(t *testing.T, dbPath, show string) bool {
	t.Helper()
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var flag bool
	if err := db.QueryRow(`SELECT needs_review FROM items WHERE show = ?`, show).Scan(&flag); err != nil {
		t.Fatalf("needs_review of %q: %v", show, err)
	}
	return flag
}

// TestEndToEndLibraryFolderLifecycle runs the real binary through what a person
// does with a library folder in Settings: add it (its files are scanned on their
// own), change its type (they are read again), remove it (they are gone, and a
// scan does not bring them back), add it back (the progress comes back, and so
// does the title the person fixed by hand).
func TestEndToEndLibraryFolderLifecycle(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	data := filepath.Join(base, "data dir", "ply data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	lost := filepath.Join(lib, "Lost", "Season 1", "Lost S01E02 Pilot.mp4")
	matrix := filepath.Join(lib, "The Matrix (1999)", "The Matrix (1999).mp4")
	makeMovie(t, matrix)
	makeMovie(t, lost)
	// Long enough ago for the scanner's settle time, and not the same moment: two
	// files with one size and one time would look like a file and its move.
	for i, p := range []string{lost, matrix} {
		old := time.Now().Add(-10*time.Minute - time.Duration(i)*time.Minute)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	tmdb := newFakeTMDB(t)
	bin := buildPly(t, base)
	p := startPly(t, base, bin, tmp, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0",
		"-hwaccel", "none", "-tmdb-key", e2eTMDBKey, "-tmdb-url", tmdb.apiURL(), "-tmdb-image-url", tmdb.imageURL())
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}

	items := func() []metaItem {
		t.Helper()
		_, body := c.do("GET", "/api/items", "", bearer)
		var list []metaItem
		mustJSON(t, body, &list)
		return list
	}
	// find returns the item whose title or show is name.
	find := func(name string) (metaItem, bool) {
		for _, it := range items() {
			if it.Title == name || it.Show == name {
				return it, true
			}
		}
		return metaItem{}, false
	}
	folder := func() (folderStatus, bool) {
		t.Helper()
		_, body := c.do("GET", "/api/folders", "", bearer)
		var list []folderStatus
		mustJSON(t, body, &list)
		for _, f := range list {
			if f.Path == lib {
				return f, true
			}
		}
		return folderStatus{}, false
	}
	// waitAll waits until the library holds exactly two items and each satisfies ok.
	waitAll := func(what string, ok func(metaItem) bool) {
		t.Helper()
		waitFor(t, what, 60*time.Second, func() bool {
			list := items()
			for _, it := range list {
				if !ok(it) {
					return false
				}
			}
			return len(list) == 2
		})
	}
	kindIs := func(kind string) func(metaItem) bool { return func(it metaItem) bool { return it.Kind == kind } }

	// 1. The folder is added; its files are scanned without asking for a scan.
	if f, ok := folder(); ok {
		t.Fatalf("a fresh server has the folder: %+v", f)
	}
	resp, body := c.do("POST", "/api/folders", `{"path":"`+lib+`","kind":"movies"}`, bearer)
	if resp.StatusCode != 201 {
		t.Fatalf("POST /api/folders = %d %s", resp.StatusCode, body)
	}
	waitAll("both files are scanned as movies", kindIs("movie"))
	waitFor(t, "the folder says it was scanned and holds 2 items", 30*time.Second, func() bool {
		f, ok := folder()
		return ok && f.LastScanAt > 0 && f.Items == 2
	})
	f, _ := folder()
	if time.Since(time.Unix(f.LastScanAt, 0)) > time.Minute {
		t.Errorf("lastScanAt = %v, want about now", time.Unix(f.LastScanAt, 0))
	}
	if m, ok := find("The Matrix"); !ok || m.Year == nil || *m.Year != 1999 {
		t.Errorf("the movie = %+v, %v", m, ok)
	}

	// 2. The type changes to shows: the same items are read again, in place.
	ids := map[int64]bool{}
	for _, it := range items() {
		ids[it.ID] = true
	}
	resp, body = c.do("PATCH", fmt.Sprintf("/api/folders/%d", f.ID), `{"kind":"shows"}`, bearer)
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH to shows = %d %s", resp.StatusCode, body)
	}
	waitAll("the files are episodes", kindIs("episode"))
	for _, it := range items() {
		if !ids[it.ID] {
			t.Errorf("item %d is new: the re-read must keep the items", it.ID)
		}
		switch it.Show {
		case "Lost":
			if it.Season == nil || *it.Season != 1 || it.Episode == nil || *it.Episode != 2 || reviewFlag(t, filepath.Join(data, "ply.db"), "Lost") {
				t.Errorf("Lost = %+v", it)
			}
		case "The Matrix (1999)":
			flag := reviewFlag(t, filepath.Join(data, "ply.db"), "The Matrix (1999)")
			if it.Season == nil || *it.Season != 1 || it.Episode == nil || *it.Episode != 0 || !flag {
				t.Errorf("The Matrix as an episode with no number = %+v (season %v, episode %v, flag %v), want episode 0, for review",
					it, it.Season != nil && *it.Season == 1, it.Episode != nil && *it.Episode == 0, flag)
			}
		default:
			t.Errorf("unexpected show %q in %+v", it.Show, it)
		}
	}

	// 3. Back to movies; then the person works with them: a title fixed by hand
	// and a place in the film.
	resp, body = c.do("PATCH", fmt.Sprintf("/api/folders/%d", f.ID), `{"kind":"movies"}`, bearer)
	if resp.StatusCode != 200 {
		t.Fatalf("PATCH to movies = %d %s", resp.StatusCode, body)
	}
	waitAll("the files are movies again", kindIs("movie"))
	matrixItem, _ := find("The Matrix")
	resp, body = c.do("PUT", fmt.Sprintf("/api/items/%d/match", matrixItem.ID), `{"tmdbId": 27205}`, bearer)
	var fixed metaItem
	mustJSON(t, body, &fixed)
	if resp.StatusCode != 200 || fixed.MatchState != "manual" || fixed.Meta == nil || fixed.Meta.Title != "Inception" {
		t.Fatalf("PUT match = %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("PUT", fmt.Sprintf("/api/items/%d/progress", matrixItem.ID), `{"positionSec": 600, "durationSec": 7200}`, bearer)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT progress = %d %s", resp.StatusCode, body)
	}

	// 4. The folder is removed: its items are gone at once, and stay gone when the
	// scanner runs again (and when the watcher looks at the folder).
	if resp, body := c.do("DELETE", fmt.Sprintf("/api/folders/%d", f.ID), "", bearer); resp.StatusCode != 204 {
		t.Fatalf("DELETE = %d %s", resp.StatusCode, body)
	}
	if got := items(); len(got) != 0 {
		t.Fatalf("the folder was removed, but the library still has %d items", len(got))
	}
	if _, ok := folder(); ok {
		t.Error("the folder is still listed")
	}
	for _, path := range []string{lost, matrix} {
		if _, err := os.Stat(path); err != nil { // the files are the person's: removing the folder leaves them alone
			t.Errorf("removing the folder touched %s: %v", path, err)
		}
	}
	scanStatus := func() (running bool, finished string) {
		_, body := c.do("GET", "/api/scan", "", bearer)
		var st struct {
			Running      bool    `json:"running"`
			LastFinished *string `json:"lastFinished"`
		}
		mustJSON(t, body, &st)
		if st.LastFinished != nil {
			finished = *st.LastFinished
		}
		return st.Running, finished
	}
	_, before := scanStatus()
	if resp, body := c.do("POST", "/api/scan", "", bearer); resp.StatusCode >= 300 {
		t.Fatalf("POST /api/scan = %d %s", resp.StatusCode, body)
	}
	waitFor(t, "a full scan finishes", 30*time.Second, func() bool {
		running, finished := scanStatus()
		return !running && finished != before
	})
	time.Sleep(2 * time.Second) // the watcher and the rest have had their say
	if got := items(); len(got) != 0 {
		t.Fatalf("a scan brought the removed folder's %d items back", len(got))
	}

	// 5. The folder is added back: the files return, with the progress and with the
	// title the person fixed.
	resp, body = c.do("POST", "/api/folders", `{"path":"`+lib+`","kind":"movies"}`, bearer)
	if resp.StatusCode != 201 {
		t.Fatalf("POST /api/folders again = %d %s", resp.StatusCode, body)
	}
	waitAll("both files are back", kindIs("movie"))
	waitFor(t, "the film has its progress and its title back", 30*time.Second, func() bool {
		for _, it := range items() {
			if it.Progress != nil {
				return it.Progress.PositionSec == 600 && it.MatchState == "manual"
			}
		}
		return false
	})
	for _, it := range items() {
		resp, body := c.do("GET", fmt.Sprintf("/api/items/%d", it.ID), "", bearer)
		var det metaItem
		mustJSON(t, body, &det)
		switch it.Progress {
		case nil: // the other film
			if det.MatchState == "manual" || det.Meta != nil && det.Meta.Title == "Inception" {
				t.Errorf("a film the person did not work with came back with a fixed match: %d %s", resp.StatusCode, body)
			}
		default:
			if resp.StatusCode != 200 || det.MatchState != "manual" || det.Meta == nil || det.Meta.Title != "Inception" {
				t.Errorf("the film that came back = %d %s", resp.StatusCode, body)
			}
		}
	}
	p.stop()
}

// TestEndToEndMediaFolderIsGuessed: a -media folder has no type until its first
// scan finds files. The scan guesses it, the guess is stored (Settings shows it as
// guessed), the files are read as that type, and the next start keeps all of it.
func TestEndToEndMediaFolderIsGuessed(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media")
	data := filepath.Join(base, "data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(media, "Lost", "Lost S01E01.mp4")
	makeMovie(t, first)
	video, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{first, filepath.Join(media, "Lost", "Lost S01E02.mp4"), filepath.Join(media, "Dune (2021).mp4")}
	for i, p := range files {
		if i > 0 {
			if err := os.WriteFile(p, video, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		old := time.Now().Add(-10*time.Minute - time.Duration(i)*time.Minute)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	bin := buildPly(t, base)
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	start := func() (*ply, *client) {
		p := startPly(t, base, bin, tmp, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0",
			"-hwaccel", "none", "-metadata=false", "-media", media)
		return p, &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	}
	folder := func(c *client) folderStatus {
		t.Helper()
		_, body := c.do("GET", "/api/folders", "", bearer)
		var list []folderStatus
		mustJSON(t, body, &list)
		if len(list) != 1 || list[0].Path != media {
			t.Fatalf("folders = %s, want the -media folder only", body)
		}
		return list[0]
	}
	items := func(c *client) []item {
		t.Helper()
		_, body := c.do("GET", "/api/items", "", bearer)
		var list []item
		mustJSON(t, body, &list)
		return list
	}

	p, c := start()
	waitFor(t, "the -media folder is guessed after its first scan", 60*time.Second, func() bool {
		f := folder(c)
		return f.Kind != "" && f.Items == 3
	})
	if f := folder(c); f.Kind != "shows" || !f.Guessed || f.ID == 0 {
		t.Errorf("folder = %+v, want shows, guessed", f)
	}
	waitFor(t, "every file is an episode", 30*time.Second, func() bool {
		list := items(c)
		for _, it := range list {
			if it.Kind != "episode" {
				return false
			}
		}
		return len(list) == 3
	})
	ids := map[int64]bool{}
	for _, it := range items(c) {
		ids[it.ID] = true
	}
	p.stop()

	p, c = start() // the type is the stored one, and nothing is read again
	waitFor(t, "the second start has scanned", 60*time.Second, func() bool { return folder(c).LastScanAt > 0 })
	if f := folder(c); f.Kind != "shows" || !f.Guessed {
		t.Errorf("after a restart: %+v, want shows, guessed", f)
	}
	list := items(c)
	if len(list) != 3 {
		t.Errorf("%d items after a restart, want 3", len(list))
	}
	for _, it := range list {
		if !ids[it.ID] || it.Kind != "episode" {
			t.Errorf("after a restart: %+v", it)
		}
	}
	p.stop()
}

// TestEndToEndFlagChangesTheType: a folder given with -movies at one start and -shows
// at the next keeps its items and has them read again as episodes.
func TestEndToEndFlagChangesTheType(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	data := filepath.Join(base, "data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(lib, "Lost", "Lost S01E02 Pilot.mp4")
	makeMovie(t, file)
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	bin := buildPly(t, base)
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	start := func(flag string) (*ply, *client) {
		p := startPly(t, base, bin, tmp, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0",
			"-hwaccel", "none", "-metadata=false", flag, lib)
		return p, &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	}
	only := func(c *client, kind string) item {
		t.Helper()
		var got item
		waitFor(t, "the file is a "+kind, 60*time.Second, func() bool {
			_, body := c.do("GET", "/api/items", "", bearer)
			var list []item
			mustJSON(t, body, &list)
			if len(list) != 1 || list[0].Kind != kind {
				return false
			}
			got = list[0]
			return true
		})
		return got
	}

	p, c := start("-movies")
	movie := only(c, "movie")
	p.stop()

	p, c = start("-shows")
	ep := only(c, "episode")
	if ep.ID != movie.ID || ep.Show != "Lost" || ep.Season == nil || *ep.Season != 1 || ep.Episode == nil || *ep.Episode != 2 {
		t.Errorf("after -shows: %+v, want episode 2 of Lost, season 1, and the item %d it was", ep, movie.ID)
	}
	p.stop()

	p, c = start("-movies") // and back
	only(c, "movie")
	p.stop()
}

// TestEndToEndMixedMediaFolderUpgrade: a -media folder that holds movies and
// episodes, indexed before folders had types, is not restructured when ply is
// upgraded. The guess is stored and shown as guessed; the episodes stay episodes of
// their show, with their progress and the show's match that a person fixed.
func TestEndToEndMixedMediaFolderUpgrade(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media")
	data := filepath.Join(base, "data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	dune := filepath.Join(media, "Movies", "Dune (2021).mp4")
	makeMovie(t, dune)
	video, err := os.ReadFile(dune)
	if err != nil {
		t.Fatal(err)
	}
	episodes := []string{filepath.Join(media, "TV", "Lost", "Season 1", "Lost S01E01.mp4"), filepath.Join(media, "TV", "Lost", "Season 1", "Lost S01E02.mp4")}
	for _, p := range episodes {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, video, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	heat := filepath.Join(media, "Movies", "Heat (1995).mp4")
	if err := os.WriteFile(heat, video, 0o644); err != nil {
		t.Fatal(err)
	}
	for i, p := range append([]string{dune, heat}, episodes...) {
		old := time.Now().Add(-10*time.Minute - time.Duration(i)*time.Minute)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	// The library as the version before folders had types left it: read as ParseName
	// reads, with a title the person fixed and a place in an episode.
	ctx := context.Background()
	st, err := store.Open(filepath.Join(data, "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	seed := func(path, kind, title string, year int, show string, season, episode int) int64 {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := st.UpsertItem(ctx, &store.Item{Kind: kind, Title: title, Year: year, Show: show, Season: season, Episode: episode, Path: path,
			Root: media, Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Container: "mp4", DurationSec: 2}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed(dune, store.KindMovie, "Dune", 2021, "", 0, 0)
	seed(heat, store.KindMovie, "Heat", 1995, "", 0, 0)
	first := seed(episodes[0], store.KindEpisode, "", 0, "Lost", 1, 1)
	seed(episodes[1], store.KindEpisode, "", 0, "Lost", 1, 2)
	lost, err := st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 4607, Title: "Lost"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: lost, State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetProgress(ctx, first, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// What an earlier version left is older than the first start of this one, which
	// stamps its time on the database: the stamp is made here by Open, so take it away
	// and age the items.
	dbExec(t, filepath.Join(data, "ply.db"),
		`DELETE FROM settings WHERE key = 'm7.since'`, `UPDATE items SET added_at = added_at - 3600`)

	bin := buildPly(t, base)
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	p := startPly(t, base, bin, tmp, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0",
		"-hwaccel", "none", "-metadata=false", "-media", media)
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}

	waitFor(t, "the -media folder is guessed after its first scan", 60*time.Second, func() bool {
		_, body := c.do("GET", "/api/folders", "", bearer)
		var list []folderStatus
		mustJSON(t, body, &list)
		return len(list) == 1 && list[0].Kind != ""
	})
	_, body := c.do("GET", "/api/folders", "", bearer)
	var list []folderStatus
	mustJSON(t, body, &list)
	if list[0].Kind != "movies" || !list[0].Guessed {
		t.Errorf("folder = %+v, want movies, guessed", list[0])
	}
	_, body = c.do("GET", "/api/items", "", bearer)
	var items []item
	mustJSON(t, body, &items)
	if len(items) != 4 {
		t.Fatalf("%d items, want 4: %s", len(items), body)
	}
	for _, it := range items {
		switch {
		case it.Show == "Lost":
			if it.Kind != "episode" {
				t.Errorf("an episode of Lost became %+v", it)
			}
		case it.Title == "Dune" || it.Title == "Heat":
			if it.Kind != "movie" {
				t.Errorf("a movie became %+v", it)
			}
		default:
			t.Errorf("unexpected item %+v", it)
		}
		if it.ID == first && (it.Progress == nil || it.Progress.PositionSec != 600) {
			t.Errorf("the first episode lost its progress: %+v", it)
		}
	}
	if state := dbString(t, filepath.Join(data, "ply.db"), `SELECT match_state FROM series_matches WHERE show = 'Lost'`); state != "manual" {
		t.Errorf("the match of Lost is %q, want the manual one kept", state)
	}
	if log := p.log.String(); !strings.Contains(log, "guessed movies; 2 existing episodes kept as they are") {
		t.Errorf("the log does not warn about the existing episodes:\n%s", log)
	}
	p.stop()
}

// dbExec runs statements on the server's database while it is not running.
func dbExec(t *testing.T, dbPath string, statements ...string) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// dbString reads one string from the server's database.
func dbString(t *testing.T, dbPath, query string) string {
	t.Helper()
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var s string
	if err := db.QueryRow(query).Scan(&s); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return s
}
