package library

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/probe"
	"ply/internal/store"
)

// syncBuffer is a log destination that is safe to write from the scanner's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logTo makes the scanner of e log to the returned buffer. Call it before the
// scanner does anything.
func (e *env) logTo() *syncBuffer {
	b := &syncBuffer{}
	e.s.log = slog.New(slog.NewTextHandler(b, nil))
	return b
}

// setRootsQuiet gives the scanner its roots as SetRoots would, without the scan
// SetRoots starts, so that a test runs the passes itself.
func (e *env) setRootsQuiet(roots ...Root) {
	e.s.rootMu.Lock()
	e.s.managed, e.s.roots = true, roots
	e.s.rootMu.Unlock()
}

// makeLegacy makes the items in the library now ones an earlier version left: added
// before this version first ran on the database (Store.TypedSince). The test's
// "earlier version" is a scanner run before the guess, in the same second.
func (e *env) makeLegacy() {
	e.t.Helper()
	e.execRaw(`UPDATE items SET added_at = (SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'm7.since') - 1000`)
}

// forgetting returns a hook that records what it hears and, as the server's does
// once it has applied a match, forgets the archived match of the item.
func (e *env) forgetting(r *restores) func(itemID, tmdbID int64, kind string) {
	return func(itemID, tmdbID int64, kind string) {
		r.hook(itemID, tmdbID, kind)
		entry, err := e.st.GetEntry(context.Background(), itemID)
		if err != nil {
			e.t.Errorf("GetEntry(%d): %v", itemID, err)
			return
		}
		if err := e.st.ForgetArchivedMatch(context.Background(), entry.Path, kind); err != nil {
			e.t.Error(err)
		}
	}
}

// A mixed -media folder, indexed before there were types, is not restructured when
// ply is upgraded: the guess is stored, the files already in the library are kept
// as they are, with their shows, matches and progress, and only the files that are
// new are read as the guessed type.
func TestGuessKeepsAnExistingLibraryAsItIs(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	dune := filepath.Join(root, "Movies", "Dune (2021)", "Dune (2021).mkv")
	heat := filepath.Join(root, "Movies", "Heat (1995).mkv")
	alien := filepath.Join(root, "Movies", "Alien (1979).mkv")
	e1 := filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E01.mkv")
	e2 := filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E02.mkv")
	for i, p := range []string{dune, heat, alien, e1, e2} {
		write(t, p, string(make([]byte, i+1)))
	}
	old := NewScanner(ctx, e.st, e.fp, []string{root}, nil) // before there were types
	mustScan(t, old)
	e.makeLegacy()
	e.matchMovie(e.item(dune).ID, 438631, store.MatchManual)
	e.matchMovie(e.item(heat).ID, 949, store.MatchMatched)
	e.progress(e.item(e1).ID, 1200)
	if err := e.st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: e.title(store.KindSeries, 4607, "Lost"), State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	// Two files arrive before the upgrade's first scan.
	newMovie := filepath.Join(root, "Movies", "Sicario (2015).mkv")
	newEpisode := filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E03.mkv")
	write(t, newMovie, "sixsix")
	write(t, newEpisode, "seven77")

	// The upgrade: the folder is -media, nothing stored about it.
	e.setRootsQuiet(Root{Path: root}) // as NewScanner(root) + SetRoots(root) leave them
	mustScan(t, e.s)

	rows, _ := e.st.Folders(ctx)
	if len(rows) != 1 || rows[0].Path != root || rows[0].Kind != KindMovies || !rows[0].Guessed {
		t.Fatalf("stored folders = %+v; want the root as movies, guessed", rows)
	}
	for _, p := range []string{e1, e2} {
		it := e.item(p)
		if it.Kind != store.KindEpisode || it.Show != "Lost" || it.Season != 1 {
			t.Errorf("%s was restructured by the guess: %+v", filepath.Base(p), it.Item)
		}
	}
	if it := e.item(e1); it.Progress == nil || it.Progress.PositionSec != 1200 {
		t.Errorf("progress of the first episode: %+v", it.Progress)
	}
	if m, err := e.st.GetSeriesMatch(ctx, "Lost"); err != nil || m.State != store.MatchManual {
		t.Errorf("the manual match of Lost after the guess: %+v, %v", m, err)
	}
	if it := e.item(dune); it.MatchState != store.MatchManual || it.Kind != store.KindMovie {
		t.Errorf("Dune after the guess: %+v", it.Item)
	}
	if it := e.item(heat); it.MatchState != store.MatchMatched {
		t.Errorf("Heat after the guess: %+v", it.Item)
	}
	// What this scan made is read as the guessed type, the existing files are not.
	if it := e.item(newEpisode); it.Kind != store.KindMovie || it.Show != "" {
		t.Errorf("a new file named like an episode, in a movies folder: %+v, want a movie", it.Item)
	}
	if it := e.item(newMovie); it.Kind != store.KindMovie {
		t.Errorf("a new movie: %+v", it.Item)
	}
	log := logs.String()
	if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "guessed movies; 2 existing episodes kept as they are") ||
		!strings.Contains(log, "change the type in Settings") {
		t.Errorf("the log does not warn that the existing episodes were kept:\n%s", log)
	}

	// Later files are read as the type from the start.
	later := filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E04.mkv")
	write(t, later, "eighteight")
	mustScan(t, e.s)
	if it := e.item(later); it.Kind != store.KindMovie {
		t.Errorf("a later file: %+v", it.Item)
	}
	if it := e.item(e1); it.Kind != store.KindEpisode {
		t.Errorf("a later scan restructured the first episode: %+v", it.Item)
	}
}

// A folder that had nothing before is read as its guessed type from the first
// scan: that is not a restructuring.
func TestGuessReadsAFolderThatHadNothing(t *testing.T) {
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	for i := 1; i <= 3; i++ {
		write(t, filepath.Join(root, "Lost", "Lost S01E0"+string(rune('0'+i))+".mkv"), string(make([]byte, i)))
	}
	write(t, filepath.Join(root, "Dune (2021).mkv"), "dune")
	e.setRoots(Root{Path: root})
	if it := e.item(filepath.Join(root, "Dune (2021).mkv")); it.Kind != store.KindEpisode {
		t.Errorf("Dune in a shows folder: %+v", it.Item)
	}
	if strings.Contains(logs.String(), "kept as they are") {
		t.Errorf("a warning about existing files for a folder that had none:\n%s", logs.String())
	}
}

// The type of a folder is a person's to change. Changing it reads the items again;
// the manual matches that cannot stay are kept in the archive, and come back when
// the type does. The progress is never touched.
func TestTypeChangeKeepsTheMatchesItDropsAndGivesThemBack(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "lib")
	dune := filepath.Join(root, "Dune (2021)", "Dune (2021).mkv")
	l1 := filepath.Join(root, "Lost", "Lost S01E01.mkv")
	l2 := filepath.Join(root, "Lost", "Lost S01E02.mkv")
	write(t, dune, "1")
	write(t, l1, "22")
	write(t, l2, "333")
	var r restores
	e.s.OnRestoreMatch(e.forgetting(&r))

	e.setRoots(movies(root))
	e.matchMovie(e.item(dune).ID, 438631, store.MatchManual)
	e.progress(e.item(dune).ID, 600)
	e.progress(e.item(l1).ID, 900)

	// To shows: Dune is an episode, the movie's title cannot stay.
	e.setRoots(shows(root))
	d := e.item(dune)
	if d.Kind != store.KindEpisode || d.MatchState != store.MatchPending || d.Progress == nil || d.Progress.PositionSec != 600 {
		t.Errorf("Dune as an episode: %+v, progress %+v", d.Item, d.Progress)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE path = ? AND tmdb_id = 438631 AND tmdb_kind = 'movie'`, dune); n != 1 {
		t.Fatalf("the movie match of Dune was not kept in the archive")
	}
	if got := r.list(); len(got) != 0 {
		t.Errorf("the hook heard %v while the kinds do not fit", got)
	}
	if err := e.st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: e.title(store.KindSeries, 4607, "Lost"), State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}

	// Back to movies: Dune's title returns, the show's match is kept for the episodes.
	e.setRoots(movies(root))
	d = e.item(dune)
	if d.Kind != store.KindMovie || d.Progress == nil || d.Progress.PositionSec != 600 {
		t.Errorf("Dune as a movie again: %+v, progress %+v", d.Item, d.Progress)
	}
	if got, want := r.list(), []restoreMatch{{d.ID, 438631, store.KindMovie}}; !slices.Equal(got, want) {
		t.Errorf("the hook heard %v after the type came back, want %v", got, want)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE series_id <> 0 AND series_show = 'Lost'`); n != 2 {
		t.Errorf("%d series matches in the archive, want one for each of the two episodes", n)
	}
	if _, err := e.st.GetSeriesMatch(ctx, "Lost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the series row of a show with no episode: %v", err)
	}
	if it := e.item(l1); it.Progress == nil || it.Progress.PositionSec != 900 {
		t.Errorf("Lost S01E01 progress: %+v", it.Progress)
	}

	// And to shows once more: now it is the show's match that comes back.
	e.setRoots(shows(root))
	i1, i2 := e.item(l1).ID, e.item(l2).ID
	got := r.list()
	slices.SortFunc(got[1:], func(a, b restoreMatch) int { return int(a.itemID - b.itemID) })
	want := []restoreMatch{{d.ID, 438631, store.KindMovie}, {min(i1, i2), 4607, store.KindSeries}, {max(i1, i2), 4607, store.KindSeries}}
	if !slices.Equal(got, want) {
		t.Errorf("the hook heard %v, want %v", got, want)
	}
}

// The process ended between the indexing of a file and the restore of what its
// folder kept: the next scan restores it, though the row is there and unchanged.
func TestARowWithoutItsRestoreIsRestoredByTheNextScan(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	seen := filepath.Join(root, "Seen (2020).mkv")
	write(t, seen, "abc")
	e.setRoots(movies(root))
	e.progress(e.item(seen).ID, 600)
	e.matchMovie(e.item(seen).ID, 603, store.MatchManual)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(seen)
	if _, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Seen", Year: 2020, Path: seen, Root: root,
		Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Container: "mkv"}, nil); err != nil { // and then it died
		t.Fatal(err)
	}
	var r restores
	e.s.OnRestoreMatch(r.hook)
	calls := e.fp.calls.Load()

	e.setRoots(movies(root))
	it := e.item(seen)
	if it.Progress == nil || it.Progress.PositionSec != 600 {
		t.Errorf("the progress after the next scan: %+v", it.Progress)
	}
	if got, want := r.list(), []restoreMatch{{it.ID, 603, store.KindMovie}}; !slices.Equal(got, want) {
		t.Errorf("the hook heard %v, want %v", got, want)
	}
	if n := e.fp.calls.Load(); n != calls {
		t.Errorf("the file was probed again (%d calls)", n-calls)
	}
}

// RestoreArchived is a write: it is asked for only where the archive has progress
// for the path, not for every file the scanner indexes.
func TestRestoreIsAskedForOnlyWhereTheArchiveHasProgress(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	a, b, c := filepath.Join(root, "A (2020).mkv"), filepath.Join(root, "B (2021).mkv"), filepath.Join(root, "C (2022).mkv")
	write(t, a, "a")
	write(t, b, "bb")
	var (
		mu    sync.Mutex
		paths []string
	)
	real := e.s.restoreArchived
	e.s.restoreArchived = func(ctx context.Context, id int64, path string, size int64) (int64, string, bool, error) {
		mu.Lock()
		paths = append(paths, filepath.Base(path))
		mu.Unlock()
		return real(ctx, id, path, size)
	}
	asked := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Sorted(slices.Values(paths))
	}
	e.setRoots(movies(root)) // two new files, nothing archived
	if got := asked(); len(got) != 0 {
		t.Fatalf("RestoreArchived asked for %v for files nothing was archived for", got)
	}
	// A file that changed in place, with progress archived for its path (it was
	// there when the folder was removed, and the row came back some other way).
	e.execRaw(`INSERT INTO item_archive (path, size, position_sec, duration_sec, progress_at, archived_at) VALUES (?, 3, 50, 100, 5, 1)`, a)
	write(t, a, "aaa") // changed: re-indexed, size 3
	write(t, c, "c")   // new, nothing archived
	e.s.Wait()
	mustScan(t, e.s)
	if got := asked(); !slices.Equal(got, []string{"A (2020).mkv"}) {
		t.Errorf("RestoreArchived asked for %v, want only the changed file with progress in the archive", got)
	}
	if it := e.item(a); it.Progress == nil || it.Progress.PositionSec != 50 {
		t.Errorf("the changed file's progress: %+v", it.Progress)
	}
}

// A match that is not applied stays in the archive: the next scan, or the next start
// of the server, reports it again.
func TestAPendingMatchIsReportedAgainAfterARestart(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	file := filepath.Join(root, "Seen (2020).mkv")
	write(t, file, "a")
	e.setRoots(movies(root))
	e.matchMovie(e.item(file).ID, 603, store.MatchManual)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	var first restores
	e.s.OnRestoreMatch(first.hook) // it hears, and cannot apply (no TMDB key)
	e.setRoots(movies(root))
	if got := first.list(); len(got) != 1 || got[0].tmdbID != 603 {
		t.Fatalf("the first hook heard %v", got)
	}

	// The server starts again: a new scanner, and its first scan.
	var second restores
	again := NewScanner(ctx, e.st, e.fp, []string{root}, nil)
	again.OnRestoreMatch(second.hook)
	again.SetRoots([]Root{movies(root)})
	if err := again.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if got := second.list(); len(got) != 1 || got[0].tmdbID != 603 || got[0].itemID != e.item(file).ID {
		t.Errorf("after a restart the hook heard %v, want the match again", got)
	}
}

// cancelProber cancels the context while probing its n-th file.
type cancelProber struct {
	inner  Prober
	cancel context.CancelFunc
	n      int64
	calls  atomic.Int64
}

func (c *cancelProber) Probe(ctx context.Context, path string) (probe.Result, error) {
	if c.calls.Add(1) == c.n {
		c.cancel()
	}
	return c.inner.Probe(ctx, path)
}

// The server shuts down during the scan of a folder that was added back: what was
// indexed is announced, and no archived match is lost; the next scan reports them.
func TestShutdownDuringTheScanOfAFolderAddedBack(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.s.workers = 1
	root := filepath.Join(t.TempDir(), "films")
	a, b := filepath.Join(root, "A (2020).mkv"), filepath.Join(root, "B (2021).mkv")
	write(t, a, "a")
	write(t, b, "bb")
	e.setRoots(movies(root))
	e.matchMovie(e.item(a).ID, 11, store.MatchManual)
	e.matchMovie(e.item(b).ID, 22, store.MatchManual)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	var (
		r     restores
		mu    sync.Mutex
		added []int64
	)
	e.s.OnRestoreMatch(r.hook)
	e.s.OnAdded(func(ids []int64) {
		mu.Lock()
		defer mu.Unlock()
		added = append(added, ids...)
	})

	cctx, cancel := context.WithCancel(ctx)
	e.s.prober = &cancelProber{inner: e.fp, cancel: cancel, n: 2}
	e.setRootsQuiet(movies(root))
	if _, err := e.s.ScanDirs(cctx, []Dir{{Path: root}}); err == nil {
		t.Fatal("control: the scan was meant to be cut short")
	}
	mu.Lock()
	told := slices.Clone(added)
	mu.Unlock()
	if e.count() == 0 || len(told) != e.count() {
		t.Errorf("%d items indexed before the end, but the worker was told about %v", e.count(), told)
	}
	e.s.prober = e.fp
	mustScan(t, e.s)
	if got := r.list(); len(got) != 2 {
		t.Errorf("the next scan reported %v, want both matches", got)
	}
}

// No guess from a scan that did not go cleanly: some of the files could not be read.
func TestNoGuessFromAScanWithFilesThatCouldNotBeProbed(t *testing.T) {
	ctx := context.Background()
	build := func(t *testing.T) (*env, string) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "media")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		for i := 1; i <= 3; i++ {
			p := filepath.Join(root, "Lost", "Lost S01E0"+string(rune('0'+i))+".mkv")
			write(t, p, string(make([]byte, i+1)))
			e.fp.fail.Store(p, struct{}{})
		}
		return e, root
	}
	check := func(t *testing.T, e *env, root string) {
		t.Helper()
		if rows, _ := e.st.Folders(ctx); len(rows) != 0 {
			t.Errorf("guessed %+v from a scan where 3 of 4 files could not be probed", rows)
		}
		if kind, _ := e.s.rootKind(root); kind != "" {
			t.Errorf("the root took the type %q", kind)
		}
	}
	t.Run("partial scan", func(t *testing.T) {
		e, root := build(t)
		e.setRoots(Root{Path: root})
		check(t, e, root)
	})
	t.Run("full scan", func(t *testing.T) {
		e, root := build(t)
		e.setRootsQuiet(Root{Path: root})
		_ = e.s.Scan(ctx)
		check(t, e, root)
	})
	// A positive control: once the files can be read, the next scan guesses.
	t.Run("the next scan", func(t *testing.T) {
		e, root := build(t)
		e.setRootsQuiet(Root{Path: root})
		_ = e.s.Scan(ctx)
		e.fp.fail.Range(func(k, _ any) bool { e.fp.fail.Delete(k); return true })
		mustScan(t, e.s)
		if kind, _ := e.s.rootKind(root); kind != KindShows {
			t.Errorf("type = %q after a clean scan, want shows", kind)
		}
	})
}

// No guess, and no scan time, while files of the folder are still settling: it is
// being copied into, and what is there says little about what will be.
func TestNoGuessWhileFilesSettle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.s.SetSettle(time.Hour) // every file counts as still being written, except the old one
	root := filepath.Join(t.TempDir(), "media")
	oldFile := filepath.Join(root, "Dune (2021).mkv")
	write(t, oldFile, "1")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		write(t, filepath.Join(root, "Lost", "Lost S01E0"+string(rune('0'+i))+".mkv"), string(make([]byte, i+1)))
	}
	e.setRootsQuiet(Root{Path: root})
	res, err := e.s.ScanDirs(ctx, []Dir{{Path: root}})
	if err != nil || len(res.Deferred) == 0 {
		t.Fatalf("control: ScanDirs = %+v, %v; want directories left for later", res, err)
	}
	if rows, _ := e.st.Folders(ctx); len(rows) != 0 {
		t.Errorf("guessed %+v with 5 of 6 files still settling", rows)
	}
	if !e.s.LastScan(root).IsZero() {
		t.Errorf("the root has a scan time while its files were settling: %v", e.s.LastScan(root))
	}

	e.s.SetSettle(0) // they have settled
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if kind, _ := e.s.rootKind(root); kind != KindShows {
		t.Errorf("type = %q once the files have settled, want shows", kind)
	}
	if e.s.LastScan(root).IsZero() {
		t.Error("no scan time once the files have settled")
	}
}

// A pass that is running leaves a folder alone once it is taken out: DropRoot, which
// waits for the pass, does not wait for the rest of its files.
func TestARunningPassStopsIndexingAFolderThatWasTakenOut(t *testing.T) {
	e := newEnv(t)
	e.s.workers = 1
	root := filepath.Join(t.TempDir(), "films")
	for i := range 6 {
		write(t, filepath.Join(root, "Film "+string(rune('A'+i))+" (2000).mkv"), string(make([]byte, i+1)))
	}
	e.fp.block = make(chan struct{})
	e.fp.entered = make(chan struct{}, 10)
	unblock := sync.OnceFunc(func() { close(e.fp.block) })
	t.Cleanup(unblock)
	e.setRootsQuiet(movies(root))
	scanned := make(chan struct{})
	go func() {
		e.s.ScanDirs(context.Background(), []Dir{{Path: root}})
		close(scanned)
	}()
	<-e.fp.entered // the first file is being probed, the other five wait
	dropped := make(chan int, 1)
	go func() {
		n, _ := e.s.DropRoot(context.Background(), root, false)
		dropped <- n
	}()
	waitUntil(t, "the root is taken out", func() bool { return e.s.rootRetired(root) })
	unblock()
	<-scanned
	if n := <-dropped; n != 1 {
		t.Errorf("DropRoot removed %d items, want the 1 that was being indexed", n)
	}
	if n := e.fp.calls.Load(); n != 1 {
		t.Errorf("%d files probed, want only the one in flight when the folder was taken out", n)
	}
}

func waitUntil(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 5 s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The walk of a folder that was taken out stops touching its files: a file that is
// already in the library does not have its subtitles updated.
func TestAWalkStopsLookingAtAFolderThatWasTakenOut(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	film := filepath.Join(root, "x Film (2000).mkv") // after the directory "A" in the walk
	write(t, film, "1")
	write(t, filepath.Join(root, "A", "Other (2001).mkv"), "22")
	e.setRoots(movies(root))
	external := func() int {
		entry, err := e.st.GetEntry(ctx, e.item(film).ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(entry.External)
	}
	if n := external(); n != 0 {
		t.Fatalf("control: %d subtitles", n)
	}
	write(t, filepath.Join(root, "x Film (2000).en.srt"), "1\n00:00:01,000 --> 00:00:02,000\nhi\n")

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.s.devOf = func(path string) (uint64, bool) {
		if filepath.Base(path) == "A" { // the walk is stuck at the directory A
			once.Do(func() { close(entered) })
			<-release
		}
		return deviceOf(path)
	}
	released := sync.OnceFunc(func() { close(release) })
	t.Cleanup(released)
	done := make(chan error, 1)
	go func() { done <- e.s.Scan(ctx) }()
	<-entered
	e.s.SetRoots(nil) // the folder is taken out while the walk is stuck
	released()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := external(); n != 0 {
		t.Errorf("the walk updated the subtitles of a folder that was taken out (%d)", n)
	}
	if e.count() != 2 {
		t.Errorf("%d items; a pass must not remove the items of a folder that was taken out", e.count())
	}
	// Positive control: the folder is back, and the same walk does update them.
	e.s.SetRoots([]Root{movies(root)})
	e.s.Wait()
	mustScan(t, e.s)
	if n := external(); n != 1 {
		t.Errorf("control: %d subtitles once the folder is back, want 1", n)
	}
}

// DropRoot does not wait for ever for a scan that is stuck: after dropWait it gives
// up with ErrScanBusy, nothing removed, the folder back and scanned again.
func TestDropRootGivesUpOnAStuckScan(t *testing.T) {
	e := newEnv(t)
	defer func(d time.Duration) { dropWait = d }(dropWait)
	dropWait = 50 * time.Millisecond
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	write(t, filepath.Join(a, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(b, "Heat (1995).mkv"), "22")
	e.setRoots(movies(a), movies(b))

	release := e.hold() // a scan that does not end
	start := time.Now()
	n, err := e.s.DropRoot(context.Background(), a, false)
	if !errors.Is(err, ErrScanBusy) || n != 0 {
		t.Fatalf("DropRoot = %d, %v; want ErrScanBusy", n, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("DropRoot waited %v with dropWait at %v", d, dropWait)
	}
	if roots := rootsOf(e.s); !slices.Equal(roots, []Root{movies(a), movies(b)}) {
		t.Errorf("roots = %v, want the folder back where it was", roots)
	}
	if e.s.rootRetired(a) {
		t.Error("the folder is still retired after DropRoot put it back")
	}
	// It is scanned again: a file that came meanwhile is indexed, with no scan asked for.
	write(t, filepath.Join(a, "New (2002).mkv"), "333")
	release()
	e.s.Wait()
	if _, ok := e.items()[filepath.Join(a, "New (2002).mkv")]; !ok {
		t.Errorf("the folder that DropRoot put back was not scanned: %v", keys(e.items()))
	}

	// The caller's context ending is not a busy scan.
	release = e.hold()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	dropWait = time.Minute
	if _, err := e.s.DropRoot(ctx, a, false); err == nil || errors.Is(err, ErrScanBusy) {
		t.Errorf("DropRoot with a context that ended = %v, want that error", err)
	}
	release()
	e.s.Wait()
}

// When the caller took the root out first and the wait times out, the root stays
// retired: it is for the caller to put it back, and no scan removes its items.
func TestDropRootTimeoutAfterSetRootsLeavesTheRootRetired(t *testing.T) {
	e := newEnv(t)
	defer func(d time.Duration) { dropWait = d }(dropWait)
	dropWait = 20 * time.Millisecond
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	e.setRoots(movies(root))
	e.s.SetRoots(nil)
	release := e.hold()
	if _, err := e.s.DropRoot(context.Background(), root, false); !errors.Is(err, ErrScanBusy) {
		t.Fatalf("DropRoot = %v", err)
	}
	if !e.s.rootRetired(root) || rootsContain(e.s, root) {
		t.Error("DropRoot put back a root it did not take out")
	}
	release()
	e.s.SetRoots([]Root{movies(root)}) // the caller restores it
	e.s.Wait()
	if e.s.rootRetired(root) {
		t.Error("still retired after SetRoots gave it back")
	}
}

// RestoreArchived-style mutants: a changed type cannot be mistaken for NeedsReview.
// A match the matcher flagged for review is not forgotten by a re-reading that finds
// the same name.
func TestReparseIgnoresNeedsReview(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "lib")
	f := filepath.Join(root, "Dune (2021).mkv")
	write(t, f, "1")
	e.setRoots(movies(root))
	e.matchMovie(e.item(f).ID, 2, store.MatchMatched)
	e.execRaw(`UPDATE items SET needs_review = 1 WHERE path = ?`, f) // the matcher was unsure
	release := e.hold()
	defer release()
	ids, err := e.s.reparse(ctx, root, nil)
	if err != nil || len(ids) != 0 {
		t.Fatalf("reparse = %v, %v; want nothing to change", ids, err)
	}
	if it := e.item(f); it.MatchState != store.MatchMatched || it.TitleID == 0 || !it.NeedsReview {
		t.Errorf("a re-reading that found nothing new reset the match: %+v", it.Item)
	}
}

// A folder taken out by DropRoot alone, while a scan waits for its turn and its
// files are gone: nothing but DropRoot removes the items, so the progress is archived.
func TestDropRootAloneKeepsTheItemsOfFilesThatAreGone(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	file := filepath.Join(root, "Seen (2020).mkv")
	write(t, file, "1")
	write(t, filepath.Join(root, "Other (2021).mkv"), "22") // keeps the folder from looking unmounted
	e.setRoots(movies(root))
	e.progress(e.item(file).ID, 600)

	release := e.hold()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.s.Scan(ctx) }()
	time.Sleep(100 * time.Millisecond) // the scan waits for its turn
	mustRemove(t, root)                // an unmounted disk, say
	var n int
	go func() { defer wg.Done(); n, _ = e.s.DropRoot(ctx, root, false) }()
	time.Sleep(100 * time.Millisecond)
	release()
	wg.Wait()
	if n != 2 {
		t.Errorf("DropRoot removed %d items, want both: the scan before it must not have taken them", n)
	}
	if got := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE path = ? AND position_sec = 600`, file); got != 1 {
		t.Errorf("the progress was lost: a scan removed the item before DropRoot archived it")
	}
}

// Once DropRoot has finished, nothing of the folder is protected any more: a row
// that turns up for it later, with its file gone, is cleaned up like any leftover.
func TestARootIsNotRetiredAfterItsItemsAreDropped(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	base := t.TempDir()
	gone, kept := filepath.Join(base, "gone"), filepath.Join(base, "kept")
	write(t, filepath.Join(gone, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(kept, "Heat (1995).mkv"), "22")
	e.setRoots(movies(gone), movies(kept))
	if _, err := e.s.DropRoot(ctx, gone, false); err != nil {
		t.Fatal(err)
	}
	if e.s.rootRetired(gone) {
		t.Fatal("still retired after DropRoot")
	}
	leftover := filepath.Join(gone, "Leftover (1999).mkv")
	if _, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Leftover", Path: leftover, Root: gone, Size: 5, MTime: 5}, nil); err != nil {
		t.Fatal(err)
	}
	mustScan(t, e.s)
	if _, ok := e.items()[leftover]; ok {
		t.Error("a leftover row of a folder that is no longer configured, with its file gone, was kept")
	}
}

// The scan of a folder that was just added looks again at what was still being
// written, after the settle time.
func TestScanFreshLooksAgainAtFilesStillSettling(t *testing.T) {
	e := newEnv(t)
	defer func(p time.Duration, n int) { freshRetryPad, freshScanRetries = p, n }(freshRetryPad, freshScanRetries)
	freshRetryPad, freshScanRetries = 10*time.Millisecond, 1
	e.s.SetSettle(300 * time.Millisecond)
	root := filepath.Join(t.TempDir(), "films")
	file := filepath.Join(root, "Copying (2020).mkv")
	write(t, file, "1") // written just now: the first look leaves it for later
	e.setRoots(movies(root))
	if _, ok := e.items()[file]; !ok {
		t.Errorf("the file that was settling was not indexed by the second look: %v", keys(e.items()))
	}
}

// A root DropRoot could not remove goes back where it was: the order of the roots
// decides which of two nested ones indexes a file.
func TestPutBackKeepsThePositionOfTheRoot(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	for _, d := range []string{a, b, c} {
		write(t, filepath.Join(d, "Film (2000).mkv"), "1")
	}
	e.setRoots(movies(a), movies(b), movies(c))
	release := e.hold()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.s.DropRoot(ctx, b, false); err == nil {
		t.Fatal("control: DropRoot was meant to give up")
	}
	release()
	e.s.Wait()
	if roots := rootsOf(e.s); !slices.Equal(roots, []Root{movies(a), movies(b), movies(c)}) {
		t.Errorf("roots = %v, want b back in the middle", roots)
	}
}

// A pass works on a snapshot of the retired roots, not on the scanner's own map.
func TestSnapshotDoesNotShareTheRetiredRoots(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	a := filepath.Join(base, "a")
	write(t, filepath.Join(a, "Film (2000).mkv"), "1")
	e.setRoots(movies(a))
	roots, retired := e.s.snapshot()
	e.s.SetRoots(nil) // retires a
	if len(retired) != 0 {
		t.Errorf("the snapshot's retired roots changed under it: %v", retired)
	}
	if !slices.Equal(roots, []Root{movies(a)}) {
		t.Errorf("the snapshot's roots changed under it: %v", roots)
	}
	_, now := e.s.snapshot()
	if !now[a] {
		t.Errorf("a is not retired: %v", now)
	}
}
