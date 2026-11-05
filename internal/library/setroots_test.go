package library

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"ply/internal/store"
)

// env is a scanner like the server's: it starts with no folders and is given
// them (SetRoots), on a real store with a fake prober.
type env struct {
	t      *testing.T
	s      *Scanner
	st     *store.Store
	fp     *fakeProber
	dbPath string
	rawDB  *sql.DB
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "data dir", "ply.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fp := &fakeProber{}
	e := &env{t: t, st: st, fp: fp, dbPath: dbPath}
	e.s = NewScanner(context.Background(), st, fp, nil, nil)
	t.Cleanup(e.s.Wait) // runs before the store is closed
	return e
}

// raw is a second connection to the database: for the setup the store has no call
// for, and for faults (a trigger that makes a delete fail).
func (e *env) raw() *sql.DB {
	e.t.Helper()
	if e.rawDB == nil {
		u := url.URL{Scheme: "file", Path: e.dbPath, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			e.t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		e.t.Cleanup(func() { db.Close() })
		e.rawDB = db
	}
	return e.rawDB
}

func (e *env) execRaw(query string, args ...any) {
	e.t.Helper()
	if _, err := e.raw().Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *env) countRaw(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.raw().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// hold takes the scan semaphore, as a pass that is running does. The returned func
// gives it up; it also runs at the end of the test, so that a failed test does not
// leave scans waiting for ever.
func (e *env) hold() func() {
	e.t.Helper()
	if err := e.s.acquire(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	release := sync.OnceFunc(e.s.release)
	e.t.Cleanup(release)
	return release
}

func movies(path string) Root { return Root{Path: path, Kind: KindMovies} }
func shows(path string) Root  { return Root{Path: path, Kind: KindShows} }

// setRoots gives the scanner its roots and waits for the scans and re-reads that
// follow.
func (e *env) setRoots(roots ...Root) {
	e.t.Helper()
	e.s.SetRoots(roots)
	e.s.Wait()
}

func (e *env) items() map[string]store.Entry { return paths(e.t, e.st) }

func (e *env) item(path string) store.Entry {
	e.t.Helper()
	it, ok := e.items()[path]
	if !ok {
		e.t.Fatalf("%s is not in the library: %v", path, keys(e.items()))
	}
	return it
}

func (e *env) count() int {
	e.t.Helper()
	n, err := e.st.CountItems(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// rootsOf are the scanner's roots, as a pass would see them.
func rootsOf(s *Scanner) []Root {
	roots, _ := s.snapshot()
	return roots
}

// title stores a TMDB title and returns its id.
func (e *env) title(kind string, tmdb int64, name string) int64 {
	e.t.Helper()
	id, err := e.st.UpsertTitle(context.Background(), &store.TitleInput{Title: store.Title{
		Kind: kind, TMDBID: tmdb, Title: name, SortTitle: name, Source: store.SourceTMDB}})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// matchMovie gives a movie item a match in the given state.
func (e *env) matchMovie(id int64, tmdb int64, state string) {
	e.t.Helper()
	title := e.title(store.KindMovie, tmdb, fmt.Sprintf("Title %d", tmdb))
	if err := e.st.SetItemMatch(context.Background(), id, store.Match{TitleID: title, State: state, Score: 0.9}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) progress(id int64, pos float64) {
	e.t.Helper()
	if _, err := e.st.SetProgress(context.Background(), id, pos, 7200); err != nil {
		e.t.Fatal(err)
	}
}

// restoreMatch is what OnRestoreMatch reports.
type restoreMatch struct {
	itemID, tmdbID int64
	kind           string
}

// restores collects what OnRestoreMatch reports.
type restores struct {
	mu  sync.Mutex
	got []restoreMatch
}

func (r *restores) hook(itemID, tmdbID int64, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, restoreMatch{itemID, tmdbID, kind})
}

func (r *restores) list() []restoreMatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// SetRoots scans the roots that are new, and only those, and never at the same
// time as another pass.
func TestSetRootsScansNewRoots(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	films, tv := filepath.Join(base, "films"), filepath.Join(base, "tv")
	write(t, filepath.Join(films, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(films, "Heat (1995).mkv"), "22")
	write(t, filepath.Join(tv, "Lost", "Season 1", "Lost S01E01.mkv"), "333")

	e.setRoots(movies(films))
	if got := e.items(); len(got) != 2 {
		t.Fatalf("after adding the films folder: %v, want its 2 files", keys(got))
	}
	if n := e.fp.calls.Load(); n != 2 {
		t.Errorf("%d probes, want 2", n)
	}

	write(t, filepath.Join(films, "Later (2000).mkv"), "4444")
	e.setRoots(movies(films), shows(tv))
	got := e.items()
	if len(got) != 3 {
		t.Fatalf("after adding the tv folder: %v, want 3 files (the films folder is not scanned again)", keys(got))
	}
	if n := e.fp.calls.Load(); n != 3 {
		t.Errorf("%d probes, want 3: only the new folder is scanned", n)
	}
	ep := got[filepath.Join(tv, "Lost", "Season 1", "Lost S01E01.mkv")]
	if ep.Kind != store.KindEpisode || ep.Show != "Lost" || ep.Season != 1 || ep.Episode != 1 {
		t.Errorf("episode of the shows folder = %+v", ep.Item)
	}
	if m := got[filepath.Join(films, "Dune (2021).mkv")]; m.Kind != store.KindMovie || m.Title != "Dune" || m.Year != 2021 {
		t.Errorf("movie of the movies folder = %+v", m.Item)
	}
}

// SetRoots is called with the API's lock held: it must not wait for the scan that
// is running. The scan of the new root waits for its turn.
func TestSetRootsDoesNotBlockAndWaitsItsTurn(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(root, "Heat (1995).mkv"), "22")

	release := e.hold() // a pass is running
	returned := make(chan struct{})
	go func() {
		e.s.SetRoots([]Root{movies(root)})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("SetRoots waited for the running pass")
	}
	time.Sleep(100 * time.Millisecond)
	if n := e.count(); n != 0 {
		t.Fatalf("%d items while another pass held the scan semaphore: the scan of the new root did not wait", n)
	}
	release()
	e.s.Wait()
	if n := e.count(); n != 2 {
		t.Errorf("%d items after the pass ended, want 2", n)
	}
}

// A new type re-reads the items in place: ids, probes and files stay; kind,
// show, season and episode change; matches that do not fit are cleared.
func TestKindChangeReadsItemsAgain(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "lib")
	cool := filepath.Join(root, "Cool Show", "Season 2", "Cool Show S02E05 Pilot.mkv")
	dune := filepath.Join(root, "Dune (2021)", "Dune (2021).mkv")
	write(t, cool, "1")
	write(t, dune, "22")
	var (
		mu       sync.Mutex
		notified []int64
	)
	e.s.OnAdded(func(ids []int64) {
		mu.Lock()
		defer mu.Unlock()
		notified = append(notified, ids...)
	})

	e.setRoots(movies(root))
	a, b := e.item(cool), e.item(dune)
	if a.Kind != store.KindMovie || b.Kind != store.KindMovie || b.Title != "Dune" || b.Year != 2021 {
		t.Fatalf("read as movies: %+v, %+v", a.Item, b.Item)
	}
	e.matchMovie(a.ID, 1, store.MatchMatched)
	e.matchMovie(b.ID, 2, store.MatchManual)
	probes := e.fp.calls.Load()
	mu.Lock()
	notified = nil
	mu.Unlock()

	e.setRoots(shows(root)) // the type changes: movies to shows
	a2, b2 := e.item(cool), e.item(dune)
	if a2.ID != a.ID || b2.ID != b.ID {
		t.Errorf("ids changed: %d, %d, want %d, %d", a2.ID, b2.ID, a.ID, b.ID)
	}
	if a2.Kind != store.KindEpisode || a2.Show != "Cool Show" || a2.Season != 2 || a2.Episode != 5 || a2.Title != "Pilot" || a2.NeedsReview {
		t.Errorf("Cool Show read as a show: %+v", a2.Item)
	}
	if b2.Kind != store.KindEpisode || b2.Show != "Dune (2021)" || b2.Season != 1 || b2.Episode != 0 || !b2.NeedsReview {
		t.Errorf("Dune read as a show (no episode number: episode 0, for review): %+v", b2.Item)
	}
	for _, it := range []store.Entry{a2, b2} {
		if it.MatchState != store.MatchPending || it.TitleID != 0 {
			t.Errorf("%s keeps a match that no longer fits: state %q, title %d", filepath.Base(it.Path), it.MatchState, it.TitleID)
		}
	}
	mu.Lock()
	got := slices.Clone(notified)
	mu.Unlock()
	slices.Sort(got)
	if want := []int64{min(a.ID, b.ID), max(a.ID, b.ID)}; !slices.Equal(got, want) {
		t.Errorf("OnAdded told %v, want %v (the worker must match them again)", got, want)
	}
	if n := e.fp.calls.Load(); n != probes {
		t.Errorf("%d probes for the re-read, want none", n-probes)
	}

	e.setRoots(movies(root)) // and back
	a3, b3 := e.item(cool), e.item(dune)
	if a3.ID != a.ID || a3.Kind != store.KindMovie || a3.Show != "" || a3.Season != 0 || a3.Episode != 0 || a3.NeedsReview {
		t.Errorf("Cool Show read as a movie again: %+v", a3.Item)
	}
	if b3.ID != b.ID || b3.Kind != store.KindMovie || b3.Title != "Dune" || b3.Year != 2021 || b3.NeedsReview {
		t.Errorf("Dune read as a movie again: %+v", b3.Item)
	}
}

// A root given again with the type it has, or with no type, reads nothing again:
// the matches stay.
func TestSameKindReadsNothingAgain(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "lib")
	file := filepath.Join(root, "Dune (2021).mkv")
	write(t, file, "1")
	e.setRoots(movies(root))
	e.matchMovie(e.item(file).ID, 1, store.MatchMatched)

	e.setRoots(movies(root))
	e.setRoots(Root{Path: root}) // no type: the scanner keeps the one it has
	e.setRoots(movies(root), movies(root))
	if it := e.item(file); it.MatchState != store.MatchMatched || it.Kind != store.KindMovie {
		t.Errorf("after giving the same type again: %+v", it.Item)
	}
	if kind, _ := e.s.rootKind(root); kind != KindMovies {
		t.Errorf("a root given with no type has the type %q, want movies", kind)
	}
}

// At start the server builds the scanner with the roots and gives it their saved
// types: what was read with those types stays as it is (no match is lost on every
// restart), and what was read with another type is read again.
func TestSetRootsAtStart(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "tv")
	file := filepath.Join(root, "Lost", "Lost S01E02.mkv")
	write(t, file, "1")
	e.setRoots(shows(root))
	if err := e.st.SetSeriesMatch(context.Background(), "Lost",
		store.Match{TitleID: e.title(store.KindSeries, 4, "Lost"), State: store.MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}

	same := NewScanner(context.Background(), e.st, e.fp, []string{root}, nil)
	same.SetRoots([]Root{shows(root)})
	same.Wait()
	if m, err := e.st.GetSeriesMatch(context.Background(), "Lost"); err != nil || m.State != store.MatchMatched {
		t.Errorf("the match of the show after a restart: %+v, %v; want it kept", m, err)
	}

	// A type the scanner is given for the first time reads nothing again: what the
	// items were read with is not known, and a library is not restructured on its own.
	first := NewScanner(context.Background(), e.st, e.fp, []string{root}, nil)
	first.SetRoots([]Root{movies(root)})
	first.Wait()
	if it := e.item(file); it.Kind != store.KindEpisode || it.Show != "Lost" {
		t.Errorf("a scanner that was given a type for the first time read the items again: %+v", it.Item)
	}

	// A flag changed the type: main gives the scanner the type the folder had, then
	// the one it has.
	other := NewScanner(context.Background(), e.st, e.fp, []string{root}, nil)
	other.SetRoots([]Root{shows(root)})
	other.SetRoots([]Root{movies(root)})
	other.Wait()
	if it := e.item(file); it.Kind != store.KindMovie || it.Show != "" {
		t.Errorf("after the type changed at start: %+v, want a movie", it.Item)
	}
}

// A -media folder with no type is read as before until its first scan, which
// guesses its type from the files, stores the guess and reads the files again
// as that type says.
func TestGuessAfterTheFirstScan(t *testing.T) {
	ctx := context.Background()
	t.Run("shows", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		eps := []string{
			filepath.Join(root, "Lost", "Season 1", "Lost S01E01.mkv"),
			filepath.Join(root, "Lost", "Season 1", "Lost S01E02.mkv"),
			filepath.Join(root, "Lost", "Season 1", "Lost S01E03.mkv"),
		}
		for i, p := range eps {
			write(t, p, string(make([]byte, i+1)))
		}
		write(t, filepath.Join(root, "Dune (2021).mkv"), "four")

		e.setRoots(Root{Path: root})
		rows, err := e.st.Folders(ctx)
		if err != nil || len(rows) != 1 || rows[0].Path != root || rows[0].Kind != KindShows || !rows[0].Guessed {
			t.Fatalf("stored folders = %+v, %v; want the root as shows, guessed", rows, err)
		}
		if kind, _ := e.s.rootKind(root); kind != KindShows {
			t.Errorf("the scanner has the type %q for the root, want shows", kind)
		}
		// The movie in a shows folder is an episode now, for review.
		if d := e.item(filepath.Join(root, "Dune (2021).mkv")); d.Kind != store.KindEpisode || !d.NeedsReview {
			t.Errorf("Dune after the guess = %+v, want an episode to review", d.Item)
		}
		if ep := e.item(eps[1]); ep.Kind != store.KindEpisode || ep.Show != "Lost" || ep.Episode != 2 {
			t.Errorf("episode after the guess = %+v", ep.Item)
		}
	})

	// The guess differs from how the files were read until then: the one episode
	// that was read as an episode is a movie now.
	t.Run("movies, and the re-read", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		write(t, filepath.Join(root, "Heat (1995).mkv"), "22")
		write(t, filepath.Join(root, "Alien (1979).mkv"), "333")
		odd := filepath.Join(root, "Some Show S01E01.mkv")
		write(t, odd, "4444")

		var told []int64
		var mu sync.Mutex
		e.s.OnAdded(func(ids []int64) { mu.Lock(); told = append(told, ids...); mu.Unlock() })
		e.setRoots(Root{Path: root})
		rows, _ := e.st.Folders(ctx)
		if len(rows) != 1 || rows[0].Kind != KindMovies || !rows[0].Guessed {
			t.Fatalf("stored folders = %+v, want the root as movies, guessed", rows)
		}
		it := e.item(odd)
		if it.Kind != store.KindMovie || it.Show != "" {
			t.Errorf("the episode-named file in a movies folder = %+v, want a movie", it.Item)
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Contains(told, it.ID) {
			t.Errorf("OnAdded was not told about the re-read file %d: %v", it.ID, told)
		}
	})

	// The guess is made once: later scans do not look again.
	t.Run("once", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		e.setRoots(Root{Path: root})
		for i := 1; i <= 3; i++ {
			write(t, filepath.Join(root, "Lost", fmt.Sprintf("Lost S01E0%d.mkv", i)), string(make([]byte, i+1)))
		}
		mustScan(t, e.s) // now most of the files are episodes
		if kind, _ := e.s.rootKind(root); kind != KindMovies {
			t.Errorf("the type changed to %q on a later scan, want it to stay movies", kind)
		}
	})

	// A type stored meanwhile (another run, the Settings page) wins over a new guess.
	t.Run("a stored type wins", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		write(t, filepath.Join(root, "Heat (1995).mkv"), "22")
		if _, _, err := e.st.UpsertFlagFolder(ctx, root, "", KindShows); err != nil {
			t.Fatal(err)
		}
		e.setRoots(Root{Path: root})
		if kind, _ := e.s.rootKind(root); kind != KindShows {
			t.Errorf("type = %q, want the stored shows", kind)
		}
	})

	// No files, nothing to go by: no guess, no stored row.
	t.Run("no files", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "notes.txt"), "x")
		e.setRoots(Root{Path: root})
		if rows, _ := e.st.Folders(ctx); len(rows) != 0 {
			t.Errorf("stored folders = %+v, want none", rows)
		}
		if kind, _ := e.s.rootKind(root); kind != "" {
			t.Errorf("type = %q, want none yet", kind)
		}
	})

	// A scanner whose roots all come from NewScanner never guesses: it reads files as it always did.
	t.Run("plain scanner", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		write(t, filepath.Join(root, "Lost S01E01.mkv"), "22")
		s, st, _ := newScanner(t, root)
		mustScan(t, s)
		if rows, _ := st.Folders(ctx); len(rows) != 0 {
			t.Errorf("stored folders = %+v, want none", rows)
		}
		if got := paths(t, st)[filepath.Join(root, "Lost S01E01.mkv")]; got.Kind != store.KindEpisode {
			t.Errorf("episode = %+v", got.Item)
		}
	})
}

// The scanner keeps what the parser says is for review.
func TestNeedsReviewIsStored(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "tv")
	numbered := filepath.Join(root, "Lost", "Lost S01E02.mkv")
	bare := filepath.Join(root, "Lost", "Making Of.mkv")
	write(t, numbered, "1")
	write(t, bare, "22")
	e.setRoots(shows(root))
	if it := e.item(numbered); it.NeedsReview || it.Episode != 2 {
		t.Errorf("numbered episode = %+v", it.Item)
	}
	if it := e.item(bare); !it.NeedsReview || it.Kind != store.KindEpisode || it.Episode != 0 || it.Show != "Lost" {
		t.Errorf("episode with no number = %+v, want episode 0 of Lost, for review", it.Item)
	}
}

// LastScan is per root, and moves when a scan covers all of the root without trouble.
func TestLastScanIsPerRoot(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	write(t, filepath.Join(a, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(a, "sub", "Heat (1995).mkv"), "22")
	write(t, filepath.Join(b, "Alien (1979).mkv"), "333")
	var (
		mu    sync.Mutex
		clock = time.Unix(1_000_000, 0)
	)
	tick := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Minute)
		return clock
	}
	e.s.now = tick
	if !e.s.LastScan(a).IsZero() {
		t.Fatal("a root nobody scanned has a scan time")
	}

	e.setRoots(movies(a), movies(b))
	ta, tb := e.s.LastScan(a), e.s.LastScan(b)
	if ta.IsZero() || tb.IsZero() {
		t.Fatalf("after the first scans: %v, %v", ta, tb)
	}

	scanDirs(t, e.s, Dir{Path: a}) // the whole root a (a configured root is always scanned whole)
	ta2 := e.s.LastScan(a)
	if !ta2.After(ta) {
		t.Errorf("a was scanned again: %v, was %v", ta2, ta)
	}
	if got := e.s.LastScan(b); !got.Equal(tb) {
		t.Errorf("scanning a moved the time of b: %v, was %v", got, tb)
	}

	scanDirs(t, e.s, Dir{Path: filepath.Join(a, "sub"), Recursive: true}) // a part of a
	if got := e.s.LastScan(a); !got.Equal(ta2) {
		t.Errorf("scanning a part of a moved its time: %v, was %v", got, ta2)
	}

	mustRename(t, b, filepath.Join(base, "away")) // b is unreachable now: the full scan fails for it
	if err := e.s.Scan(context.Background()); err == nil {
		t.Fatal("control: the scan of a missing root should report an error")
	}
	if got := e.s.LastScan(b); !got.Equal(tb) {
		t.Errorf("a failed scan of b set its time: %v, was %v", got, tb)
	}
	if got := e.s.LastScan(a); !got.After(ta2) {
		t.Errorf("the full scan did not move the time of a: %v, was %v", got, ta2)
	}
	if n, err := e.s.DropRoot(context.Background(), a, true); err != nil || n != 2 {
		t.Fatalf("DropRoot = %d, %v", n, err)
	}
	if got := e.s.LastScan(a); !got.IsZero() {
		t.Errorf("a removed root keeps its scan time: %v", got)
	}
}

// What a removed folder kept comes back with its files: the progress at once, a
// manual match through the hook, once a scan has put them back.
func TestFilesComeBackWithWhatTheFolderKept(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	seen := filepath.Join(root, "Seen (2020).mkv")
	fixed := filepath.Join(root, "Fixed (2021).mkv")
	both := filepath.Join(root, "Both (2022).mkv")
	plain := filepath.Join(root, "Plain (2023).mkv")
	write(t, seen, "a")
	write(t, fixed, "bb")
	write(t, both, "ccc")
	write(t, plain, "dddd")
	e.setRoots(movies(root))
	e.progress(e.item(seen).ID, 600)
	e.matchMovie(e.item(fixed).ID, 603, store.MatchManual)
	e.progress(e.item(both).ID, 900)
	e.matchMovie(e.item(both).ID, 604, store.MatchManual)
	e.matchMovie(e.item(plain).ID, 605, store.MatchMatched) // the worker's own match: not kept

	var r restores
	e.s.OnRestoreMatch(r.hook)
	e.s.SetRoots(nil) // what the API does first
	if n, err := e.s.DropRoot(context.Background(), root, false); err != nil || n != 4 {
		t.Fatalf("DropRoot = %d, %v; want 4", n, err)
	}
	if e.count() != 0 {
		t.Fatal("control: the items are still there")
	}
	write(t, both, "ccc, another file") // this one is not the file the person watched

	e.setRoots(movies(root))
	if it := e.item(seen); it.Progress == nil || it.Progress.PositionSec != 600 {
		t.Errorf("Seen after coming back: progress %+v, want 600 s", it.Progress)
	}
	if it := e.item(fixed); it.Progress != nil {
		t.Errorf("Fixed had no progress, has %+v", it.Progress)
	}
	if it := e.item(both); it.Progress != nil {
		t.Errorf("Both is another file now (size): its progress came back: %+v", it.Progress)
	}
	if it := e.item(plain); it.Progress != nil || it.MatchState == store.MatchMatched {
		t.Errorf("Plain = %+v, want nothing restored", it.Item)
	}
	want := []restoreMatch{{e.item(fixed).ID, 603, store.KindMovie}, {e.item(both).ID, 604, store.KindMovie}}
	got := r.list()
	slices.SortFunc(got, func(a, b restoreMatch) int { return int(a.tmdbID - b.tmdbID) })
	if !slices.Equal(got, want) {
		t.Errorf("OnRestoreMatch got %v, want %v (the match survives a change of size, the progress does not)", got, want)
	}
	// The matches wait in the archive until they are applied: a hook that does not
	// apply them hears of them again after the next scan.
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 2 {
		t.Errorf("%d archive rows after the files came back, want the 2 matches", n)
	}
	e.progress(e.item(seen).ID, 0)
	mustScan(t, e.s)
	if it := e.item(seen); it.Progress != nil {
		t.Errorf("progress restored a second time: %+v", it.Progress)
	}
	if got := r.list(); len(got) != 4 {
		t.Errorf("OnRestoreMatch heard %v, want the two matches twice (they were not applied)", got)
	}
	// Once applied and forgotten, they are not reported again.
	for _, p := range []string{fixed, both} {
		if err := e.st.ForgetArchivedMatch(context.Background(), p, store.KindMovie); err != nil {
			t.Fatal(err)
		}
	}
	mustScan(t, e.s)
	if got := r.list(); len(got) != 4 {
		t.Errorf("OnRestoreMatch heard %v after the matches were forgotten", got)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 0 {
		t.Errorf("%d archive rows left", n)
	}
}

// The episodes of a show whose match a person fixed report the show's title.
func TestRestoredEpisodesReportTheShowsMatch(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "tv")
	one := filepath.Join(root, "Lost", "Lost S01E01.mkv")
	two := filepath.Join(root, "Lost", "Lost S01E02.mkv")
	write(t, one, "a")
	write(t, two, "bb")
	e.setRoots(shows(root))
	e.progress(e.item(one).ID, 700)
	if err := e.st.SetSeriesMatch(context.Background(), "Lost",
		store.Match{TitleID: e.title(store.KindSeries, 4607, "Lost"), State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	var r restores
	e.s.OnRestoreMatch(r.hook)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(context.Background(), root, false); err != nil {
		t.Fatal(err)
	}
	e.execRaw(`DELETE FROM series_matches`) // what pruning does with a show nobody has
	e.setRoots(shows(root))

	got := r.list()
	slices.SortFunc(got, func(a, b restoreMatch) int { return int(a.itemID - b.itemID) })
	want := []restoreMatch{{e.item(one).ID, 4607, store.KindSeries}, {e.item(two).ID, 4607, store.KindSeries}}
	slices.SortFunc(want, func(a, b restoreMatch) int { return int(a.itemID - b.itemID) })
	if !slices.Equal(got, want) {
		t.Errorf("OnRestoreMatch got %v, want %v", got, want)
	}
	if it := e.item(one); it.Progress == nil || it.Progress.PositionSec != 700 {
		t.Errorf("progress = %+v, want 700 s", it.Progress)
	}
}

// forget leaves nothing to restore.
func TestForgottenFolderRestoresNothing(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	file := filepath.Join(root, "Seen (2020).mkv")
	write(t, file, "a")
	e.setRoots(movies(root))
	e.progress(e.item(file).ID, 600)
	e.matchMovie(e.item(file).ID, 603, store.MatchManual)
	var r restores
	e.s.OnRestoreMatch(r.hook)
	if _, err := e.s.DropRoot(context.Background(), root, true); err != nil {
		t.Fatal(err)
	}
	e.setRoots(movies(root))
	if it := e.item(file); it.Progress != nil || it.MatchState == store.MatchManual {
		t.Errorf("after forgetting: %+v, progress %+v", it.Item, it.Progress)
	}
	if got := r.list(); len(got) != 0 {
		t.Errorf("OnRestoreMatch got %v", got)
	}
}

// A file that moved is the item it was, with its own progress: the archive of
// another file that had the path it moved to is not used (the item is not new).
func TestAMoveDoesNotRestore(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	x, y := filepath.Join(root, "X (2020).mkv"), filepath.Join(root, "Y (2021).mkv")
	write(t, x, "1")
	write(t, y, "22")
	e.setRoots(movies(root))
	e.progress(e.item(y).ID, 700)
	e.matchMovie(e.item(y).ID, 700, store.MatchManual)

	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(context.Background(), root, false); err != nil { // archives Y
		t.Fatal(err)
	}
	mustRemove(t, y)
	e.setRoots(movies(root)) // X only
	var r restores
	e.s.OnRestoreMatch(r.hook)
	id := e.item(x).ID
	e.progress(id, 100)

	mustRename(t, x, y)
	mustScan(t, e.s)
	moved := e.item(y)
	if moved.ID != id {
		t.Fatalf("control: the file did not keep its item: %d, want %d", moved.ID, id)
	}
	if moved.Progress == nil || moved.Progress.PositionSec != 100 {
		t.Errorf("progress after the move = %+v, want its own 100 s", moved.Progress)
	}
	mustScan(t, e.s) // and once more: nothing waits to be restored onto the moved item
	if got := r.list(); len(got) != 0 {
		t.Errorf("OnRestoreMatch got %v after a move", got)
	}
	if it := e.item(y); it.Progress == nil || it.Progress.PositionSec != 100 || it.MatchState == store.MatchManual {
		t.Errorf("the moved item after another scan: %+v, progress %+v", it.Item, it.Progress)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE path = ?`, y); n != 0 {
		t.Errorf("the archive of the file that was at %s outlived the move onto its path (%d rows)", filepath.Base(y), n)
	}
}

// The race the review called blocking: a folder is deleted, a scan runs, and
// nothing of the folder comes back.
func TestDeletedFolderStaysGone(t *testing.T) {
	setup := func(t *testing.T) (*env, string, string) {
		e := newEnv(t)
		base := t.TempDir()
		gone, kept := filepath.Join(base, "gone"), filepath.Join(base, "kept")
		write(t, filepath.Join(gone, "Dune (2021).mkv"), "1")
		write(t, filepath.Join(gone, "Heat (1995).mkv"), "22")
		write(t, filepath.Join(kept, "Alien (1979).mkv"), "333")
		e.setRoots(movies(gone), movies(kept))
		if e.count() != 3 {
			t.Fatalf("control: %d items, want 3", e.count())
		}
		return e, gone, kept
	}
	check := func(t *testing.T, e *env, gone string) {
		t.Helper()
		for p := range e.items() {
			if within(gone, p) {
				t.Errorf("%s is back", p)
			}
		}
		if e.count() != 1 {
			t.Errorf("%d items, want only the kept folder's", e.count())
		}
	}

	t.Run("as the API does it", func(t *testing.T) {
		e, gone, kept := setup(t)
		e.s.SetRoots([]Root{movies(kept)})
		if n, err := e.s.DropRoot(context.Background(), gone, false); err != nil || n != 2 {
			t.Fatalf("DropRoot = %d, %v", n, err)
		}
		mustScan(t, e.s)
		scanDirs(t, e.s, Dir{Path: gone}, Dir{Path: filepath.Join(gone, "x"), Recursive: true})
		check(t, e, gone)
	})

	// DropRoot takes the root out itself first: with nothing else telling the scanner,
	// a scan after it must not bring the files back.
	t.Run("DropRoot alone", func(t *testing.T) {
		e, gone, _ := setup(t)
		if n, err := e.s.DropRoot(context.Background(), gone, false); err != nil || n != 2 {
			t.Fatalf("DropRoot = %d, %v", n, err)
		}
		mustScan(t, e.s)
		check(t, e, gone)
	})

	// A pass that was waiting for its turn when the folder was removed starts after
	// it: it works on the roots as they are then.
	t.Run("a scan waiting for its turn", func(t *testing.T) {
		e, gone, kept := setup(t)
		release := e.hold() // a pass is running
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); e.s.Scan(context.Background()) }()
		go func() { defer wg.Done(); e.s.ScanDirs(context.Background(), []Dir{{Path: gone}}) }()
		time.Sleep(100 * time.Millisecond) // both wait for the semaphore
		e.s.SetRoots([]Root{movies(kept)})
		dropped := make(chan error, 1)
		go func() { _, err := e.s.DropRoot(context.Background(), gone, false); dropped <- err }()
		time.Sleep(100 * time.Millisecond)
		release()
		wg.Wait()
		if err := <-dropped; err != nil {
			t.Fatal(err)
		}
		check(t, e, gone)
	})
}

// DropRoot waits for the pass that is running, and removes what that pass added.
func TestDropRootWaitsForARunningScan(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	for i, name := range []string{"Dune (2021)", "Heat (1995)", "Alien (1979)"} {
		write(t, filepath.Join(root, name+".mkv"), string(make([]byte, i+1)))
	}
	e.fp.block = make(chan struct{})
	e.fp.entered = make(chan struct{}, 10)
	unblock := sync.OnceFunc(func() { close(e.fp.block) })
	t.Cleanup(unblock)                 // a failure must not leave the probes stuck
	e.s.SetRoots([]Root{movies(root)}) // its scan starts and stops at the probes
	for range 3 {
		<-e.fp.entered // all three files are being probed
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := e.s.DropRoot(context.Background(), root, false)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("DropRoot returned (%d, %v) while a scan of the folder was running", r.n, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if rootsContain(e.s, root) {
		t.Error("the root is still configured while DropRoot waits")
	}
	unblock()
	r := <-done
	if r.err != nil || r.n != 3 {
		t.Fatalf("DropRoot = %d, %v; want the 3 items the scan added, removed after it", r.n, r.err)
	}
	e.s.Wait()
	mustScan(t, e.s)
	if n := e.count(); n != 0 {
		t.Errorf("%d items after the folder was dropped and scanned again", n)
	}
}

func rootsContain(s *Scanner, path string) bool {
	return slices.ContainsFunc(rootsOf(s), func(r Root) bool { return r.Path == path })
}

// DropRoot honours its context while it waits for the pass, and puts the root back.
func TestDropRootGivesUpWhenItsContextEnds(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	e.setRoots(movies(root))

	release := e.hold()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	n, err := e.s.DropRoot(ctx, root, false)
	release()
	if err == nil || n != 0 {
		t.Fatalf("DropRoot = %d, %v; want a context error", n, err)
	}
	if e.count() != 1 {
		t.Errorf("the item went although DropRoot gave up")
	}
	if roots := rootsOf(e.s); !slices.Equal(roots, []Root{movies(root)}) {
		t.Errorf("roots = %v, want the root back with its type", roots)
	}
	write(t, filepath.Join(root, "Heat (1995).mkv"), "22")
	mustScan(t, e.s)
	if e.count() != 2 {
		t.Errorf("a file added after the failed DropRoot was not scanned: %d items", e.count())
	}
}

// A removal that fails puts the root back; one that failed half way is finished
// by calling again, and archives every item once.
func TestDropRootPutsTheRootBackAndResumes(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir() // an empty folder: the items are put in the store directly
	const total = 1100
	e.execRaw(`
		WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO items (kind, title, path, root, size, mtime, added_at, updated_at)
		SELECT 'movie', 'Seed ' || i, ? || '/' || i || '.mkv', ?, 100 + i, 1, 1, 1 FROM seq`, total, root, root)
	e.execRaw(`INSERT INTO progress (item_id, position_sec, duration_sec, updated_at)
		SELECT id, 100, 7200, 5 FROM items WHERE root = ?`, root)
	var poisoned int64
	if err := e.raw().QueryRow(`SELECT id FROM items WHERE root = ? ORDER BY id LIMIT 1 OFFSET 700`, root).Scan(&poisoned); err != nil {
		t.Fatal(err)
	}
	e.setRoots(movies(root))
	e.execRaw(fmt.Sprintf(`CREATE TRIGGER fail_in_batch_two BEFORE DELETE ON items WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'refused'); END`, poisoned))

	n, err := e.s.DropRoot(context.Background(), root, false)
	if err == nil {
		t.Fatal("control: the removal was meant to fail")
	}
	if n != 500 {
		t.Errorf("returned %d with the error, want the 500 removed before it", n)
	}
	if roots := rootsOf(e.s); !slices.Equal(roots, []Root{movies(root)}) {
		t.Errorf("roots after the failure = %v, want the root back", roots)
	}
	if left := e.count(); left != total-500 {
		t.Errorf("%d items left, want %d", left, total-500)
	}

	e.execRaw(`DROP TRIGGER fail_in_batch_two`)
	n, err = e.s.DropRoot(context.Background(), root, false)
	if err != nil || n != total-500 {
		t.Fatalf("second DropRoot = %d, %v; want %d, nil", n, err, total-500)
	}
	if left := e.count(); left != 0 {
		t.Errorf("%d items left", left)
	}
	if got := e.countRaw(`SELECT COUNT(*) FROM item_archive`); got != total {
		t.Errorf("%d archive rows, want one for each of the %d items", got, total)
	}
	if rootsContain(e.s, root) {
		t.Error("the root is back although the removal finished")
	}
}

// When the caller took the root out with SetRoots first (the API does) and the
// removal fails, DropRoot cannot put the root back, so it stays retired, with its
// items, until the caller does.
func TestFailedDropAfterSetRootsKeepsTheItems(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	gone, kept := filepath.Join(base, "gone"), filepath.Join(base, "kept")
	write(t, filepath.Join(gone, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(kept, "Heat (1995).mkv"), "22")
	e.setRoots(movies(gone), movies(kept))

	e.s.SetRoots([]Root{movies(kept)})
	e.execRaw(`CREATE TRIGGER no_delete BEFORE DELETE ON items BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	if _, err := e.s.DropRoot(context.Background(), gone, false); err == nil {
		t.Fatal("control: the removal was meant to fail")
	}
	e.execRaw(`DROP TRIGGER no_delete`)
	mustRemove(t, gone) // and the disk is gone as well: nothing but DropRoot may remove the items
	mustScan(t, e.s)
	if e.count() != 2 {
		t.Fatalf("%d items, want both (a failed drop must not lose them)", e.count())
	}

	// The caller puts the root back; the disk is back too.
	write(t, filepath.Join(gone, "Dune (2021).mkv"), "1")
	e.setRoots(movies(gone), movies(kept))
	mustScan(t, e.s)
	if e.count() != 2 {
		t.Errorf("%d items after the root came back, want 2", e.count())
	}
}

// Only DropRoot removes the items of a root that is no longer configured and was
// just taken out, even when its files are gone: it archives what a person made of
// them first. Rows of a root that is no longer configured and was never taken out
// are still cleaned up, when their files are gone.
func TestOnlyDropRootRemovesADroppedRootsItems(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	gone, kept, never := filepath.Join(base, "gone"), filepath.Join(base, "kept"), filepath.Join(base, "never")
	dune := filepath.Join(gone, "Dune (2021).mkv")
	write(t, dune, "1")
	write(t, filepath.Join(gone, "Other (2020).mkv"), "4444") // keeps the folder there when Dune goes
	write(t, filepath.Join(kept, "Heat (1995).mkv"), "22")
	e.setRoots(movies(gone), movies(kept))
	e.progress(e.item(dune).ID, 600)
	// A row of a root that is not configured, and was never taken out: a leftover. Its
	// folder is there, with another file in it (no unmounted disk), and the file is not.
	write(t, filepath.Join(never, "Present.mkv"), "333")
	if _, err := e.st.UpsertItem(context.Background(), &store.Item{Kind: store.KindMovie, Title: "Old", Path: filepath.Join(never, "Old.mkv"),
		Root: never, Size: 5, MTime: 5}, nil); err != nil {
		t.Fatal(err)
	}

	e.s.SetRoots([]Root{movies(kept)})
	mustRemove(t, dune) // the file is gone, the folder is there: no unmounted disk
	mustScan(t, e.s)
	if _, ok := e.items()[dune]; !ok {
		t.Fatal("a scan removed the item of a root that was just taken out, with its progress unarchived")
	}
	if _, ok := e.items()[filepath.Join(never, "Old.mkv")]; ok {
		t.Error("control: the leftover of a root that was never taken out should be cleaned up")
	}

	if n, err := e.s.DropRoot(context.Background(), gone, false); err != nil || n != 2 {
		t.Fatalf("DropRoot = %d, %v", n, err)
	}
	if got := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE path = ? AND position_sec = 600`, dune); got != 1 {
		t.Errorf("the progress was not archived")
	}
	// After DropRoot nothing of it is protected any more: a leftover row would be cleaned up.
	if len(e.items()) != 1 {
		t.Errorf("items = %v, want the kept folder's only", keys(e.items()))
	}
}

// A partial scan that waited for the semaphore looks at the roots when its turn
// comes: a folder removed meanwhile is not scanned.
func TestScanDirsLooksAtTheRootsWhenItsTurnComes(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	gone, kept := filepath.Join(base, "gone"), filepath.Join(base, "kept")
	write(t, filepath.Join(gone, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(kept, "Heat (1995).mkv"), "22")
	e.setRoots(movies(gone), movies(kept))
	write(t, filepath.Join(gone, "New (2000).mkv"), "333")

	release := e.hold() // a pass is running
	scanned := make(chan error, 1)
	go func() {
		_, err := e.s.ScanDirs(context.Background(), []Dir{{Path: gone}})
		scanned <- err
	}()
	time.Sleep(100 * time.Millisecond) // it waits for the semaphore
	e.s.SetRoots([]Root{movies(kept)})
	release()
	if err := <-scanned; err != nil {
		t.Fatal(err)
	}
	if _, ok := e.items()[filepath.Join(gone, "New (2000).mkv")]; ok {
		t.Error("the partial scan indexed a file of a folder that was removed while it waited")
	}
	if e.count() != 2 {
		t.Errorf("%d items, want 2: the removed folder's item stays until DropRoot", e.count())
	}
}
