package library

import (
	"context"
	"errors"
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

// DropRoot gives up on a pass that is stuck (ErrScanBusy), the API puts the folder
// back, and the pass, which left the rest of the folder's files unseen, ends: the
// rows of the files it skipped are not removed, with their progress and matches. The
// same for a full scan and for a partial one.
func TestABusyDropThatIsPutBackKeepsTheRowsOfSkippedFiles(t *testing.T) {
	t.Run("full scan", func(t *testing.T) { busyDropPutBack(t, false) })
	t.Run("partial scan", func(t *testing.T) { busyDropPutBack(t, true) })
}

func busyDropPutBack(t *testing.T, partial bool) {
	ctx := context.Background()
	e := newEnv(t)
	defer func(d time.Duration) { dropWait = d }(dropWait)
	dropWait = 50 * time.Millisecond
	base := t.TempDir()
	r, k := filepath.Join(base, "r"), filepath.Join(base, "k")
	first := filepath.Join(r, "0 first (1999).mkv") // visited before the stuck directory
	xfilm := filepath.Join(r, "x Film (2000).mkv")  // after the directory A in the walk
	other := filepath.Join(r, "A", "Other (2001).mkv")
	write(t, first, "1")
	write(t, xfilm, "22")
	write(t, other, "333")
	write(t, filepath.Join(k, "k1 (2002).mkv"), "4444")
	e.setRoots(movies(r), movies(k))
	e.progress(e.item(xfilm).ID, 900)
	e.matchMovie(e.item(other).ID, 77, store.MatchManual)
	ids := map[string]int64{first: e.item(first).ID, xfilm: e.item(xfilm).ID, other: e.item(other).ID}

	write(t, filepath.Join(k, "new (2003).mkv"), "55555") // makes the pass probe, and block there
	e.fp.block = make(chan struct{})
	e.fp.entered = make(chan struct{}, 10)
	unblock := sync.OnceFunc(func() { close(e.fp.block) })
	t.Cleanup(unblock)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseWalk := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseWalk)
	var once sync.Once
	e.s.devOf = func(path string) (uint64, bool) {
		if filepath.Base(path) == "A" { // the walk of r is stuck at the directory A
			once.Do(func() {
				close(entered)
				<-release
			})
		}
		return deviceOf(path)
	}
	scanned := make(chan error, 1)
	go func() {
		if partial {
			_, err := e.s.ScanDirs(ctx, []Dir{{Path: r, Recursive: true}, {Path: k, Recursive: true}})
			scanned <- err
			return
		}
		scanned <- e.s.Scan(ctx)
	}()
	<-entered
	e.s.SetRoots([]Root{movies(k)}) // the API: the folder is taken out while the walk is stuck
	releaseWalk()
	<-e.fp.entered // the pass is probing the new file of k; it has skipped the rest of r's files
	if _, err := e.s.DropRoot(ctx, r, false); !errors.Is(err, ErrScanBusy) {
		t.Fatalf("DropRoot = %v; want ErrScanBusy", err)
	}
	e.s.SetRoots([]Root{movies(r), movies(k)}) // the API's restore() after ErrScanBusy
	unblock()
	if err := <-scanned; err != nil {
		t.Fatal(err)
	}
	e.s.Wait()
	for p, id := range ids {
		it, ok := e.items()[p]
		if !ok || it.ID != id {
			t.Errorf("%s after the pass: in the library %v, id %d -> %d; want the row it had", filepath.Base(p), ok, id, it.ID)
		}
	}
	if it := e.item(xfilm); it.Progress == nil || it.Progress.PositionSec != 900 {
		t.Errorf("progress of the skipped file: %+v", it.Progress)
	}
	if it := e.item(other); it.MatchState != store.MatchManual {
		t.Errorf("match of the skipped file: %+v", it.Item)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 0 {
		t.Errorf("%d archive rows; nothing was dropped", n)
	}
}

// gateProber holds the probe of chosen files until the test lets them through.
type gateProber struct {
	inner   *fakeProber
	mu      sync.Mutex
	gates   map[string]chan struct{}
	entered chan string
}

func (g *gateProber) hold(path string) (open func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = map[string]chan struct{}{}
	}
	ch := make(chan struct{})
	g.gates[path] = ch
	return sync.OnceFunc(func() { close(ch) })
}

func (g *gateProber) Probe(ctx context.Context, path string) (probe.Result, error) {
	g.mu.Lock()
	ch := g.gates[path]
	g.mu.Unlock()
	if ch != nil {
		g.entered <- path
		select {
		case <-ch:
		case <-ctx.Done():
			return probe.Result{}, ctx.Err()
		}
	}
	return g.inner.Probe(ctx, path)
}

// A pass that skipped the new file of a folder taken out (and put back before the pass
// ended) did not see the folder whole: it notes no scan of it.
func TestAPassThatSkippedAFileOfATakenOutFolderNotesNoScanOfIt(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.s.workers = 1
	g := &gateProber{inner: e.fp, entered: make(chan string, 10)}
	e.s.prober = g
	base := t.TempDir()
	x, r, y := filepath.Join(base, "x"), filepath.Join(base, "r"), filepath.Join(base, "y")
	write(t, filepath.Join(x, "X0 (2000).mkv"), "1")
	write(t, filepath.Join(r, "R0 (2001).mkv"), "22")
	write(t, filepath.Join(y, "Y0 (2002).mkv"), "333")
	e.setRoots(movies(x), movies(r), movies(y))
	before := e.s.LastScan(r)
	if before.IsZero() {
		t.Fatal("control: no scan time for r")
	}

	x1, rnew, y1 := filepath.Join(x, "X1 (2003).mkv"), filepath.Join(r, "R1 (2004).mkv"), filepath.Join(y, "Y1 (2005).mkv")
	for i, p := range []string{x1, rnew, y1} {
		write(t, p, string(make([]byte, 10+i)))
	}
	openX, openY, openR := g.hold(x1), g.hold(y1), g.hold(rnew)
	t.Cleanup(func() { openX(); openY(); openR() })
	done := make(chan error, 1)
	go func() { done <- e.s.Scan(ctx) }() // the jobs go in the order of the roots: x1, rnew, y1
	if p := <-g.entered; p != x1 {
		t.Fatalf("probing %s first, want %s", p, x1)
	}
	e.s.SetRoots([]Root{movies(x), movies(y)}) // r is taken out while the first file is probed
	openX()
	if p := <-g.entered; p != y1 { // r's new file was skipped; y's is next
		t.Fatalf("probing %s second, want %s (r's file skipped)", p, y1)
	}
	e.s.SetRoots([]Root{movies(x), movies(r), movies(y)}) // and put back before the pass ends
	openY()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// r's own scan (SetRoots) is in its turn, held at the file the first pass skipped.
	if p := <-g.entered; p != rnew {
		t.Fatalf("probing %s, want %s in the scan of r that follows", p, rnew)
	}
	if after := e.s.LastScan(r); !after.Equal(before) {
		t.Errorf("the pass noted a scan of r (%v -> %v) though it skipped a file of it", before, after)
	}
	openR()
	e.s.Wait()
	if _, ok := e.items()[rnew]; !ok {
		t.Errorf("the file r's scan was to index is not in the library")
	}
	if after := e.s.LastScan(r); !after.After(before) {
		t.Errorf("r's own scan noted nothing (%v)", after)
	}
}

// A partial pass whose walk is stuck while its folder is taken out removes nothing of
// it when it ends, and notes no scan of it (this is what keeps the retired root's rows
// until DropRoot, or the put-back, decides).
func TestAPartialPassRemovesNothingOfAFolderTakenOutMeanwhile(t *testing.T) {
	ctx := context.Background()
	for _, putBack := range []bool{false, true} {
		name := "left out"
		if putBack {
			name = "put back"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			root := filepath.Join(t.TempDir(), "films")
			film := filepath.Join(root, "x Film (2000).mkv") // after the directory A in the walk
			write(t, film, "1")
			write(t, filepath.Join(root, "0 first (1999).mkv"), "333") // before it: seen, so the folder does not look unmounted
			write(t, filepath.Join(root, "A", "Other (2001).mkv"), "22")
			e.setRoots(movies(root))
			e.progress(e.item(film).ID, 100)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			e.s.devOf = func(path string) (uint64, bool) {
				if filepath.Base(path) == "A" {
					once.Do(func() {
						close(entered)
						<-release
					})
				}
				return deviceOf(path)
			}
			releaseWalk := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseWalk)
			before := e.s.LastScan(root)
			done := make(chan error, 1)
			go func() {
				_, err := e.s.ScanDirs(ctx, []Dir{{Path: root}})
				done <- err
			}()
			<-entered
			e.s.SetRoots(nil)
			releaseWalk()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if e.count() != 3 {
				t.Errorf("%d items after the pass; want all 3 of the folder that was taken out", e.count())
			}
			if after := e.s.LastScan(root); !after.Equal(before) {
				t.Errorf("the pass noted a scan of a folder it did not finish (%v -> %v)", before, after)
			}
			if putBack {
				e.setRoots(movies(root))
				if e.count() != 3 {
					t.Errorf("%d items once the folder is back", e.count())
				}
			}
			if it := e.item(film); it.Progress == nil || it.Progress.PositionSec != 100 {
				t.Errorf("progress after the pass: %+v", it.Progress)
			}
		})
	}
}

// The full scan gives progress back to a row that was in the library and unchanged,
// as a partial one does (the process ended between the indexing and the restore).
func TestAFullScanRestoresARowWithoutItsRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	seen := filepath.Join(root, "Seen (2020).mkv")
	write(t, seen, "abc")
	e.setRoots(movies(root))
	e.progress(e.item(seen).ID, 600)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(seen)
	if _, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Seen", Year: 2020, Path: seen, Root: root,
		Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Container: "mkv"}, nil); err != nil { // and then it died
		t.Fatal(err)
	}
	e.setRootsQuiet(movies(root))
	mustScan(t, e.s)
	if it := e.item(seen); it.Progress == nil || it.Progress.PositionSec != 600 {
		t.Errorf("progress after a full scan: %+v", it.Progress)
	}
}

// A file of a legacy folder (indexed before types) that changes after the guess keeps
// the reading it had, and so does what hangs off it: its show, and the show's match.
func TestAChangedFileOfALegacyFolderKeepsItsReading(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
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
	if err := e.st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: e.title(store.KindSeries, 4607, "Lost"), State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	e.setRootsQuiet(Root{Path: root})
	mustScan(t, e.s)
	if kind, _ := e.s.rootKind(root); kind != KindMovies {
		t.Fatalf("control: guessed %q, want movies", kind)
	}

	// Two files of the library are replaced (re-encoded): same path, new size and time.
	future := time.Now().Add(time.Minute)
	write(t, e1, "re-encoded, longer")
	write(t, dune, "a new Dune encode")
	for _, p := range []string{e1, dune} {
		if err := os.Chtimes(p, future, future); err != nil {
			t.Fatal(err)
		}
	}
	mustScan(t, e.s)
	if it := e.item(e1); it.Kind != store.KindEpisode || it.Show != "Lost" || it.Size != int64(len("re-encoded, longer")) {
		t.Errorf("the re-encoded episode = %+v; want the same episode of Lost, the new file", it.Item)
	}
	if m, err := e.st.GetSeriesMatch(ctx, "Lost"); err != nil || m.State != store.MatchManual {
		t.Errorf("the match of Lost after the re-encode: %+v, %v", m, err)
	}
	if it := e.item(dune); it.Kind != store.KindMovie || it.MatchState != store.MatchManual {
		t.Errorf("the re-encoded movie = %+v", it.Item)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 0 {
		t.Errorf("%d archive rows after a re-encode", n)
	}
}

// A file moved out of a movies folder into a shows folder is an episode there, with
// the rules of a re-reading: the manual movie match goes to the archive under the new
// path, the progress is kept.
func TestAFileMovedAcrossFoldersOfOtherTypesIsReadAgain(t *testing.T) {
	e := newEnv(t)
	base := t.TempDir()
	films, tv := filepath.Join(base, "films"), filepath.Join(base, "tv")
	f := filepath.Join(films, "Dune (2021).mkv")
	write(t, f, "1")
	write(t, filepath.Join(tv, "Lost", "Lost S01E01.mkv"), "22")
	write(t, filepath.Join(films, "Heat (1995).mkv"), "333")
	e.setRoots(movies(films), shows(tv))
	id := e.item(f).ID
	e.matchMovie(id, 438631, store.MatchManual)
	e.progress(id, 300)

	dst := filepath.Join(tv, "Dune", "Dune S01E01.mkv")
	mustRename(t, f, dst)
	mustScan(t, e.s)
	it := e.item(dst)
	if it.ID != id || it.Kind != store.KindEpisode || it.Show != "Dune" || it.MatchState != store.MatchPending || it.TitleID != 0 {
		t.Errorf("the moved file = %+v; want the same item, an episode of Dune, no movie match", it.Item)
	}
	if it.Progress == nil || it.Progress.PositionSec != 300 {
		t.Errorf("progress of the moved file: %+v", it.Progress)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE path = ? AND tmdb_id = 438631 AND tmdb_kind = 'movie'`, dst); n != 1 {
		t.Errorf("the movie match is not in the archive under the new path")
	}
}

// A new -media folder whose first look leaves files for later (they are still being
// written) is guessed at a later pass: everything it has was made by this run, and is
// read as the guess, with no warning about files "from before".
func TestGuessAfterAFirstPassThatDeferredReadsEverything(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	lost := filepath.Join(root, "Lost S01E01.mkv")
	write(t, lost, "1")
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(lost, past, past); err != nil {
		t.Fatal(err)
	}
	for i, n := range []string{"Dune (2021)", "Heat (1995)", "Alien (1979)"} {
		write(t, filepath.Join(root, n+".mkv"), string(make([]byte, i+2)))
	}
	e.s.SetSettle(time.Hour)
	e.setRootsQuiet(Root{Path: root})
	res, err := e.s.ScanDirs(ctx, []Dir{{Path: root}})
	if err != nil || len(res.Deferred) == 0 {
		t.Fatalf("control: ScanDirs = %+v, %v; want files left for later", res, err)
	}
	if e.count() != 1 {
		t.Fatalf("control: %d items after the first look, want the old file", e.count())
	}
	e.s.SetSettle(0) // the files have settled
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if kind, _ := e.s.rootKind(root); kind != KindMovies {
		t.Fatalf("type = %q, want movies", kind)
	}
	if it := e.item(lost); it.Kind != store.KindMovie || it.Show != "" {
		t.Errorf("the file the first look made = %+v; want a movie, as the folder is", it.Item)
	}
	if strings.Contains(logs.String(), "kept as they are") {
		t.Errorf("a warning about existing files for a folder that had none:\n%s", logs.String())
	}
}

// The person changes the type of a legacy mixed folder and two shows are read as one
// (both are "TV" to a shows folder): that is not a rename, so the manual match of the
// show goes to the archive for its episodes, not into the void.
func TestAShowMergedByATypeChangeKeepsItsManualMatchInTheArchive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "media")
	e1 := filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E01.mkv")
	for i, p := range []string{e1, filepath.Join(root, "TV", "Lost", "Season 1", "Lost S01E02.mkv"),
		filepath.Join(root, "TV", "Heroes", "Season 1", "Heroes S01E01.mkv"),
		filepath.Join(root, "Movies", "Dune (2021).mkv"), filepath.Join(root, "Movies", "Heat (1995).mkv"),
		filepath.Join(root, "Movies", "Alien (1979).mkv")} {
		write(t, p, string(make([]byte, i+1)))
	}
	old := NewScanner(ctx, e.st, e.fp, []string{root}, nil)
	mustScan(t, old)
	e.makeLegacy()
	if err := e.st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: e.title(store.KindSeries, 4607, "Lost"), State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	e.setRootsQuiet(Root{Path: root})
	mustScan(t, e.s) // guessed movies; the episodes stay as they are

	e.setRoots(shows(root)) // the person changes the type in Settings
	got := e.item(e1)
	if got.Kind != store.KindEpisode || got.Show == "Lost" {
		t.Fatalf("control: the episode after the change = %+v; want an episode of another show", got.Item)
	}
	if _, err := e.st.GetSeriesMatch(ctx, "Lost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the series row of a show with no episodes: %v", err)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive WHERE series_id = 4607 AND series_show = 'Lost'`); n != 2 {
		t.Errorf("%d archive rows with the match of Lost, want one for each episode", n)
	}

	// The episodes are of another show; the match waits for the show it is for.
	var r restores
	e.s.OnRestoreMatch(r.hook)
	mustScan(t, e.s)
	if got := r.list(); len(got) != 0 {
		t.Errorf("the hook heard %v; the match is for another show", got)
	}
}

func writeOld(t *testing.T, path, content string) {
	t.Helper()
	write(t, path, content)
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
}

// The common upgrade: a -media TV folder with the year in the show folder's name, a
// manual match on the show, and a first -shows flag. The show is "Lost" to the old
// reader and "Lost (2004)" to a shows folder: that is the same show renamed, and its
// manual match follows it (it would be archived for "Lost", which is never again).
func TestAFirstShowsFlagRenamesAShowAndItsManualMatchFollows(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "tv")
	e1 := filepath.Join(root, "Lost (2004)", "Season 1", "Lost.S01E01.mkv")
	e2 := filepath.Join(root, "Lost (2004)", "Season 1", "Lost.S01E02.mkv")
	write(t, e1, "1")
	write(t, e2, "22")
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{root}, nil)) // before there were types
	e.makeLegacy()
	if got := e.item(e1).Show; got != "Lost" {
		t.Fatalf("control: the old reader's show is %q", got)
	}
	title := e.title(store.KindSeries, 4607, "Lost")
	if err := e.st.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: title, State: store.MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	var r restores
	e.s.OnRestoreMatch(r.hook)

	e.s.SetRoots([]Root{shows(root)}) // as main does: SetRoots, then Reread for a first -shows
	e.s.Wait()
	e.s.Reread([]string{root}, nil)
	e.s.Wait()

	if got := e.item(e1).Show; got != "Lost (2004)" {
		t.Fatalf("control: the show after the re-reading is %q", got)
	}
	m, err := e.st.GetSeriesMatch(ctx, "Lost (2004)")
	if err != nil || m.State != store.MatchManual || m.TitleID != title {
		t.Errorf("the match of the renamed show: %+v, %v; want the manual one it had", m, err)
	}
	if _, err := e.st.GetSeriesMatch(ctx, "Lost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old name still has a series row: %v", err)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 0 {
		t.Errorf("%d archive rows; the match was to follow the show, not wait for it", n)
	}
	if got := r.list(); len(got) != 0 {
		t.Errorf("the hook heard %v; nothing was archived", got)
	}
}

// Rowids are reused: the legacy item with the highest id goes away while the guess waits
// for files to settle, and the file that comes next gets its id. It is a new file, not a
// legacy item: it is read as the guess.
func TestARowidThatIsUsedAgainDoesNotMakeANewFileLegacy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	for i, n := range []string{"Alien (1979)", "Dune (2021)", "Heat (1995)", "Lost S01E01"} {
		writeOld(t, filepath.Join(root, n+".mkv"), string(make([]byte, i+1)))
	}
	earlier := NewScanner(ctx, e.st, e.fp, []string{root}, nil) // an earlier version: no types
	earlier.workers = 1                                         // ids in the order of the names
	mustScan(t, earlier)
	e.makeLegacy()
	var lost string // the file of the legacy item with the highest id
	var top int64
	for p, it := range e.items() {
		if it.ID > top {
			top, lost = it.ID, p
		}
	}

	e.s.workers = 1 // the order of the ids is the order of the names
	zz := filepath.Join(root, "Zz Last (2016).mkv")
	write(t, zz, "55555") // still being written: the guess waits
	e.s.SetSettle(time.Hour)
	e.setRootsQuiet(Root{Path: root})
	if res, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil || len(res.Deferred) == 0 {
		t.Fatalf("control: ScanDirs = %+v, %v", res, err)
	}
	if err := os.Remove(lost); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil { // removes the row
		t.Fatal(err)
	}
	newEp := filepath.Join(root, "Aa Show S01E01.mkv")
	writeOld(t, newEp, "666666")
	writeOld(t, zz, "55555")
	e.s.SetSettle(0)
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if kind, _ := e.s.rootKind(root); kind != KindMovies {
		t.Fatalf("type = %q, want movies", kind)
	}
	it := e.item(newEp)
	if it.ID != top {
		t.Fatalf("control: the new file got id %d, the highest legacy id was %d: the id was not used again", it.ID, top)
	}
	if it.Kind != store.KindMovie {
		t.Errorf("the new file with a legacy item's id = %+v; want it read as the guess, a movie", it.Item)
	}
	if strings.Contains(logs.String(), "kept as they are") {
		t.Errorf("a warning about existing files, though none differs:\n%s", logs.String())
	}
}

// The stamp (Store.TypedSince) is where old ends and new begins: an item added the
// second before it is an earlier version's, one added in the second it was made is this
// version's.
func TestTheStampIsWhereOldItemsEnd(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "media")
	before, at := filepath.Join(root, "Lost S01E01.mkv"), filepath.Join(root, "Lost S01E02.mkv")
	write(t, before, "1")
	write(t, at, "22")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "333")
	write(t, filepath.Join(root, "Heat (1995).mkv"), "4444")
	write(t, filepath.Join(root, "Alien (1979).mkv"), "55555")
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{root}, nil))
	since, err := e.st.TypedSince(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.execRaw(`UPDATE items SET added_at = ? - 1000`, since)
	e.execRaw(`UPDATE items SET added_at = ? - 1 WHERE path = ?`, since, before)
	e.execRaw(`UPDATE items SET added_at = ? WHERE path = ?`, since, at)

	e.setRootsQuiet(Root{Path: root})
	mustScan(t, e.s) // guessed movies
	if it := e.item(before); it.Kind != store.KindEpisode {
		t.Errorf("the item added the second before the stamp = %+v; want it left as it was", it.Item)
	}
	if it := e.item(at); it.Kind != store.KindMovie {
		t.Errorf("the item added in the second of the stamp = %+v; want it read as the guess", it.Item)
	}
}

// The first look at a new -media folder is cut short and the process ends before the
// guess; at the next start the files the first run indexed are not an earlier version's:
// they are read as the guess.
func TestARestartBeforeTheGuessDoesNotMakeTheFirstRunsFilesLegacy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	lost := filepath.Join(root, "Lost S01E01.mkv")
	writeOld(t, lost, "1")
	for i, n := range []string{"Dune (2021)", "Heat (1995)", "Alien (1979)"} {
		write(t, filepath.Join(root, n+".mkv"), string(make([]byte, i+2)))
	}
	e.s.SetSettle(time.Hour)
	e.setRootsQuiet(Root{Path: root})
	if res, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil || len(res.Deferred) == 0 || e.count() != 1 {
		t.Fatalf("control: ScanDirs = %+v, %v, %d items", res, err, e.count())
	}

	// The process restarts; the other files have settled.
	second := NewScanner(ctx, e.st, e.fp, []string{root}, e.s.log)
	second.SetSettle(0)
	second.rootMu.Lock()
	second.managed, second.roots = true, []Root{{Path: root}}
	second.rootMu.Unlock()
	if _, err := second.ScanDirs(ctx, []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if kind, _ := second.rootKind(root); kind != KindMovies {
		t.Fatalf("type = %q, want movies", kind)
	}
	if it := e.item(lost); it.Kind != store.KindMovie {
		t.Errorf("the file the first run indexed = %+v; want a movie, as the folder is", it.Item)
	}
	if strings.Contains(logs.String(), "kept as they are") {
		t.Errorf("a warning about existing files for a folder this version made:\n%s", logs.String())
	}
}

// A folder removed with forget does not bring back a match that was waiting in the
// archive when it is added again.
func TestForgettingAFolderDropsAMatchStillWaitingInTheArchive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	f := filepath.Join(root, "Seen (2020).mkv")
	write(t, f, "a")
	e.setRoots(movies(root))
	e.matchMovie(e.item(f).ID, 603, store.MatchManual)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	var first restores
	e.s.OnRestoreMatch(first.hook) // nobody applies it (no TMDB key): it waits in the archive
	e.setRoots(movies(root))
	if got := first.list(); len(got) != 1 {
		t.Fatalf("control: the hook heard %v", got)
	}
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, true); err != nil { // and now the person says forget
		t.Fatal(err)
	}
	var second restores
	e.s.OnRestoreMatch(second.hook)
	e.setRoots(movies(root))
	if got := second.list(); len(got) != 0 {
		t.Errorf("the hook heard %v for a folder that was removed with forget", got)
	}
	if pm, _ := e.st.PendingMatches(ctx); len(pm) != 0 {
		t.Errorf("PendingMatches = %+v", pm)
	}
}

// A type that a person gives on the command line to a folder that had items from
// before types existed reads those items as the type says, and the log says how many
// changed. Without it the files already there would be read one way and the new ones
// another.
func TestAFirstTypedFolderIsReadAgain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "tv")
	lost1 := filepath.Join(root, "Lost (2004)", "Season 1", "Lost.S01E01.mkv")
	lost2 := filepath.Join(root, "Lost (2004)", "Season 1", "Lost.S01E02.mkv")
	write(t, lost1, "1")
	write(t, lost2, "22")
	old := NewScanner(ctx, e.st, e.fp, []string{root}, nil) // before there were types
	mustScan(t, old)
	legacy := e.item(lost1)

	// What the type says, from a library that never knew better.
	control := newEnv(t)
	control.setRoots(shows(root))
	want := control.item(lost1)
	if legacy.Show == want.Show {
		t.Fatalf("control: both read the show as %q; the test needs a reading that differs", want.Show)
	}

	e.s.SetRoots([]Root{shows(root)}) // main: a typed root the scanner did not know adopts its type
	e.s.Wait()
	if got := e.item(lost1); got.Show != legacy.Show {
		t.Fatalf("control: SetRoots alone read the items again: %+v", got.Item)
	}
	e.s.Reread([]string{root, filepath.Join(t.TempDir(), "not a root")}, nil)
	e.s.Wait()
	for _, p := range []string{lost1, lost2} {
		if got, want := e.item(p), control.item(p); got.Kind != want.Kind || got.Show != want.Show || got.Season != want.Season || got.Episode != want.Episode {
			t.Errorf("%s after Reread = %+v; want %+v", filepath.Base(p), got.Item, want.Item)
		}
	}
	log := logs.String()
	if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "2 of its 2 items are read again") {
		t.Errorf("the log does not say how many items were read again:\n%s", log)
	}

	// A second call finds nothing to change, and says nothing more.
	logs.buf.Reset()
	e.s.Reread([]string{root}, nil)
	e.s.Wait()
	if strings.Contains(logs.String(), "are read again") {
		t.Errorf("Reread warned for items that were already read as the type says:\n%s", logs.String())
	}
	// A folder without a type is left alone.
	untyped := filepath.Join(t.TempDir(), "media")
	mixed := filepath.Join(untyped, "Lost", "Lost.S01E01.mkv")
	write(t, mixed, "333")
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{untyped}, nil)) // read without a type: an episode
	e.setRootsQuiet(shows(root), Root{Path: untyped})                // no scan: the folder has no type, and no guess yet
	if err := e.st.SetParsed(ctx, []store.ParsedRow{{ID: e.item(mixed).ID, Kind: store.KindEpisode, Show: "A person's", Season: 1, Episode: 1}}); err != nil {
		t.Fatal(err) // a reading that no name would give: a re-reading would show
	}
	before := e.item(mixed)
	e.s.Reread([]string{untyped}, nil)
	e.s.Wait()
	if after := e.item(mixed); after.Item != before.Item {
		t.Errorf("Reread read an untyped folder again: %+v -> %+v", before.Item, after.Item)
	}
}

// A folder taken out and added back (the same process) is a new folder to the guess:
// the ids of the rows it had are not remembered as "from before", and the database
// gives them out again.
func TestAFolderDroppedAndAddedBackIsNewToTheGuess(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := e.logTo()
	root := filepath.Join(t.TempDir(), "media")
	old := time.Now().Add(-2 * time.Hour)
	fill := func() {
		for i, p := range []string{"Lost S01E01.mkv", "Lost S01E02.mkv", "Lost S01E03.mkv", "Dune (2021).mkv"} {
			p = filepath.Join(root, p)
			write(t, p, string(make([]byte, i+1)))
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	fill()
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{root}, nil)) // read without a type
	dune := filepath.Join(root, "Dune (2021).mkv")
	if it := e.item(dune); it.Kind != store.KindMovie {
		t.Fatalf("control: %+v", it.Item)
	}

	// The first look at the folder is cut short (a file is still being written): no guess.
	fresh := filepath.Join(root, "Fresh (2022).mkv")
	write(t, fresh, "fresh")
	e.s.SetSettle(time.Hour)
	e.setRootsQuiet(Root{Path: root})
	if res, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil || len(res.Deferred) == 0 {
		t.Fatalf("control: ScanDirs = %+v, %v", res, err)
	}
	if kind, _ := e.s.rootKind(root); kind != "" {
		t.Fatalf("control: guessed %q with a file still settling", kind)
	}

	// It is removed, and the same folder is added back with other files.
	if _, err := e.s.DropRoot(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	fill()
	e.s.SetSettle(0)
	e.setRootsQuiet(Root{Path: root})
	mustScan(t, e.s)
	if kind, _ := e.s.rootKind(root); kind != KindShows {
		t.Fatalf("type = %q, want shows", kind)
	}
	if it := e.item(dune); it.Kind != store.KindEpisode {
		t.Errorf("Dune in the folder that was added back = %+v; want it read as the guess, as a new folder's files are", it.Item)
	}
	if strings.Contains(logs.String(), "kept as they are") {
		t.Errorf("a warning about existing files for a folder that had none:\n%s", logs.String())
	}
}

// Reread tells its caller about each folder once it has been looked at: nil when it
// reads as its type says (also when there was nothing to do), the error when it could
// not be read, and nothing when the scanner ended before it got to it.
func TestRereadSaysWhichFoldersItDidAndWhichFailed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	base := t.TempDir()
	tv, untyped, gone := filepath.Join(base, "tv"), filepath.Join(base, "media"), filepath.Join(base, "gone")
	write(t, filepath.Join(tv, "Lost (2004)", "Lost.S01E01.mkv"), "1")
	write(t, filepath.Join(untyped, "Dune (2021).mkv"), "22")
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{tv, untyped}, nil))
	e.setRootsQuiet(shows(tv), Root{Path: untyped})

	var mu sync.Mutex
	got := map[string]error{}
	done := func(root string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if _, twice := got[root]; twice {
			t.Errorf("done called twice for %s", root)
		}
		got[root] = err
	}
	e.s.Reread([]string{tv, untyped, gone}, done)
	e.s.Wait()
	for _, root := range []string{tv, untyped, gone} {
		if err, ok := got[root]; !ok || err != nil {
			t.Errorf("done for %s = %v, %v; want called with nil", filepath.Base(root), err, ok)
		}
	}
	if it := e.item(filepath.Join(tv, "Lost (2004)", "Lost.S01E01.mkv")); it.Show != "Lost (2004)" {
		t.Errorf("the typed folder was not read again: %+v", it.Item)
	}

	// A folder that cannot be read: the error comes back.
	bad := newEnv(t)
	bad.setRootsQuiet(shows(tv))
	bad.st.Close()
	got = map[string]error{}
	bad.s.Reread([]string{tv}, done)
	bad.s.Wait()
	if err, ok := got[tv]; !ok || err == nil {
		t.Errorf("done with the store closed = %v, %v; want the error", err, ok)
	}

	// The scanner ends in the middle of the list: the folders after that are not done.
	mid, stop := context.WithCancel(ctx)
	defer stop()
	m := newEnv(t)
	m.s = NewScanner(mid, m.st, m.fp, nil, nil)
	other := filepath.Join(base, "tv2")
	write(t, filepath.Join(other, "Heroes", "Heroes.S01E01.mkv"), "3")
	m.setRootsQuiet(shows(tv), shows(other))
	got = map[string]error{}
	m.s.Reread([]string{tv, other}, func(root string, err error) {
		done(root, err)
		stop() // the process is ending
	})
	m.s.Wait()
	if _, ok := got[other]; ok || len(got) != 1 {
		t.Errorf("done = %v; want the first folder only, the scanner ended before the second", got)
	}

	// A scanner that has ended does not get to it: done is not called.
	ended, cancel := context.WithCancel(ctx)
	s := NewScanner(ended, e.st, e.fp, nil, nil)
	s.SetRoots([]Root{shows(tv)})
	s.Wait()
	cancel()
	got = map[string]error{}
	s.Reread([]string{tv}, done)
	s.Wait()
	if len(got) != 0 {
		t.Errorf("done called after the scanner ended: %v", got)
	}
}

// While online lookups are off the scanner reports no archived matches (nobody could
// apply them); when the gate opens, ReportMatchesIfDue reports them.
func TestArchivedMatchesAreReportedWhenLookupsComeBack(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	f := filepath.Join(root, "Seen (2020).mkv")
	write(t, f, "a")
	e.setRoots(movies(root))
	e.matchMovie(e.item(f).ID, 603, store.MatchManual)
	e.s.SetRoots(nil)
	if _, err := e.s.DropRoot(ctx, root, false); err != nil {
		t.Fatal(err)
	}
	var (
		r    restores
		open atomic.Bool
	)
	e.s.OnRestoreMatch(r.hook)
	e.s.SetMatchGate(open.Load)
	e.setRoots(movies(root))
	mustScan(t, e.s)
	if got := r.list(); len(got) != 0 {
		t.Fatalf("the hook heard %v while lookups were off", got)
	}
	e.s.ReportMatchesIfDue(ctx) // still off
	if got := r.list(); len(got) != 0 {
		t.Fatalf("the hook heard %v while lookups were still off", got)
	}
	open.Store(true)
	e.s.ReportMatchesIfDue(ctx)
	if got, want := r.list(), []restoreMatch{{e.item(f).ID, 603, store.KindMovie}}; !slices.Equal(got, want) {
		t.Fatalf("the hook heard %v once lookups were back, want %v", got, want)
	}
	// Nothing was held back since: nothing more to report.
	e.s.ReportMatchesIfDue(ctx)
	if got := r.list(); len(got) != 1 {
		t.Errorf("the hook heard %v after the report", got)
	}
	// With lookups on, the end of a pass reports as before.
	mustScan(t, e.s)
	if got := r.list(); len(got) != 2 {
		t.Errorf("the hook heard %v after a pass with lookups on, want the unforgotten match again", got)
	}
}
