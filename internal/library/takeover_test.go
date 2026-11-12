package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/store"
)

// An old -media /m indexed "/m/TV/...", and the new start gives only -shows /m/TV: the
// folder takes over the rows the old root left, and what it takes over is read as a
// shows folder reads it, like the files that come after it.
func TestAFolderThatTakesOverAnOlderRootsItemsReadsThemAsItsType(t *testing.T) {
	ctx := context.Background()
	type env3 struct {
		e           *env
		s           *Scanner
		tv, ep, doc string
	}
	setup := func(t *testing.T) *env3 {
		e := newEnv(t)
		base := t.TempDir()
		m := filepath.Join(base, "m")
		x := &env3{e: e, tv: filepath.Join(m, "TV")}
		x.ep = filepath.Join(x.tv, "Lost (2004)", "Season 1", "Lost.S01E01.mkv")
		x.doc = filepath.Join(x.tv, "Docs", "Planet Earth (2006).mkv")
		write(t, x.ep, "1")
		write(t, x.doc, "22")
		mustScan(t, NewScanner(ctx, e.st, e.fp, []string{m}, nil)) // the old version: no types
		e.makeLegacy()
		if it := e.item(x.ep); it.Show != "Lost" || it.Root != m {
			t.Fatalf("control: the old reader's episode = %+v", it.Item)
		}
		if it := e.item(x.doc); it.Kind != store.KindMovie {
			t.Fatalf("control: the old reader's documentary = %+v", it.Item)
		}
		x.s = NewScanner(ctx, e.st, e.fp, []string{x.tv}, nil) // the new start, as main does it
		x.s.SetRoots([]Root{shows(x.tv)})
		x.s.Wait()
		return x
	}
	check := func(t *testing.T, x *env3) {
		t.Helper()
		if doc := x.e.item(x.doc); doc.Kind != store.KindEpisode {
			t.Errorf("a documentary in a shows folder = %+v; want an episode", doc.Item)
		}
		// Files that come later are read the shows folder's way: it is the same show.
		next := filepath.Join(x.tv, "Lost (2004)", "Season 1", "Lost.S01E02.mkv")
		write(t, next, "333")
		mustScan(t, x.s)
		if ep, nx := x.e.item(x.ep), x.e.item(next); ep.Show != "Lost (2004)" || ep.Show != nx.Show {
			t.Errorf("the taken-over episode is of %q, the new file of %q; want the same show, Lost (2004)", ep.Show, nx.Show)
		}
	}

	t.Run("the scan", func(t *testing.T) {
		x := setup(t)
		mustScan(t, x.s)
		if it := x.e.item(x.ep); it.Root != x.tv {
			t.Fatalf("control: the scan did not take the row over: %+v", it.Item)
		}
		check(t, x)
	})

	// The note that the folder was to be read is spent by a Reread that finds no rows
	// under it yet: the rows it is about to take over are read then.
	t.Run("a Reread before the scan", func(t *testing.T) {
		x := setup(t)
		var calls int
		var doneErr error
		x.s.Reread([]string{x.tv}, func(root string, err error) { calls, doneErr = calls+1, err })
		x.s.Wait()
		if calls != 1 || doneErr != nil {
			t.Fatalf("done called %d times with %v", calls, doneErr)
		}
		if it := x.e.item(x.doc); it.Kind != store.KindEpisode || it.Root == x.tv {
			t.Fatalf("after Reread alone: %+v; want the row read again, still waiting for the scan to move it", it.Item)
		}
		mustScan(t, x.s)
		if it := x.e.item(x.ep); it.Root != x.tv {
			t.Fatalf("control: the scan did not take the row over: %+v", it.Item)
		}
		check(t, x)
	})

	// The first scan of a folder that was added (a partial one) takes the rows over as well.
	t.Run("a partial scan", func(t *testing.T) {
		x := setup(t)
		if _, err := x.s.ScanDirs(ctx, []Dir{{Path: x.tv, Recursive: true}}); err != nil {
			t.Fatal(err)
		}
		if it := x.e.item(x.ep); it.Root != x.tv {
			t.Fatalf("control: the scan did not take the row over: %+v", it.Item)
		}
		check(t, x)
	})

	// A folder with no type yet leaves what it takes over as it is (the guess does).
	t.Run("an untyped folder", func(t *testing.T) {
		x := setup(t)
		x.e.setRootsQuiet(Root{Path: x.tv})
		if err := x.e.st.SetParsed(ctx, []store.ParsedRow{{ID: x.e.item(x.doc).ID, Kind: store.KindEpisode, Show: "A person's", Season: 1, Episode: 1}}); err != nil {
			t.Fatal(err) // a reading that no name would give: a re-reading would show
		}
		mustScan(t, x.e.s)
		if it := x.e.item(x.doc); it.Root != x.tv || it.Show != "A person's" {
			t.Errorf("an untyped folder read what it took over again: %+v", it.Item)
		}
	})

	// A file that changed in between is indexed again, which moves its row too.
	t.Run("a file that changed", func(t *testing.T) {
		x := setup(t)
		write(t, x.doc, "a new encode of the documentary")
		future := time.Now().Add(time.Minute)
		if err := os.Chtimes(x.doc, future, future); err != nil {
			t.Fatal(err)
		}
		mustScan(t, x.s)
		if it := x.e.item(x.doc); it.Root != x.tv || it.Size != int64(len("a new encode of the documentary")) {
			t.Fatalf("control: the documentary was not indexed again under the folder: %+v", it.Item)
		}
		check(t, x)
	})

	// The part of the old root that a nested folder of its own indexes is that folder's.
	t.Run("a nested folder keeps its items", func(t *testing.T) {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m")
		tv := filepath.Join(m, "TV")
		docs := filepath.Join(tv, "Docs")
		ep := filepath.Join(tv, "Lost (2004)", "Season 1", "Lost.S01E01.mkv")
		doc := filepath.Join(docs, "Planet Earth (2006).mkv")
		write(t, ep, "1")
		write(t, doc, "22")
		mustScan(t, NewScanner(ctx, e.st, e.fp, []string{docs, tv}, nil)) // nested, the inner one first
		e.makeLegacy()
		if it := e.item(doc); it.Root != docs {
			t.Fatalf("control: %+v", it.Item)
		}
		e.setRootsQuiet(shows(tv), movies(docs))
		e.s.Reread([]string{tv}, nil)
		e.s.Wait()
		if it := e.item(doc); it.Kind != store.KindMovie {
			t.Errorf("Reread of the outer folder read an item of the nested movies folder as a show: %+v", it.Item)
		}
		if it := e.item(ep); it.Show != "Lost (2004)" {
			t.Errorf("control: the outer folder's own item: %+v", it.Item)
		}

		// The nested folder is taken out (its items are for DropRoot to archive and drop):
		// the outer one does not read them either.
		e.setRootsQuiet(shows(tv), movies(docs))
		e.execRaw(`UPDATE items SET kind = 'episode', show = 'A person''s', season = 1, episode = 1 WHERE path = ?`, doc)
		e.s.SetRoots([]Root{shows(tv)})
		e.s.Wait()
		e.s.Reread([]string{tv}, nil)
		e.s.Wait()
		if it := e.item(doc); it.Show != "A person's" || it.Root != docs {
			t.Errorf("Reread of the outer folder read an item of a folder that was taken out: %+v", it.Item)
		}
	})
}

// restartRereads does what the server does at a start: it reads again the folders that are
// noted in the settings (cmd/ply: rereadPending), and forgets each one it has read.
func restartRereads(t *testing.T, e *env, roots ...Root) {
	t.Helper()
	ctx := context.Background()
	paths := make([]string, len(roots))
	for i, r := range roots {
		paths[i] = r.Path
	}
	s := NewScanner(ctx, e.st, e.fp, paths, nil)
	s.SetRoots(roots)
	notes, err := e.st.SettingsWithPrefix(ctx, RereadNotePrefix)
	if err != nil {
		t.Fatal(err)
	}
	var due []string
	for key := range notes {
		due = append(due, strings.TrimPrefix(key, RereadNotePrefix))
	}
	s.Reread(due, func(root string, err error) {
		if err == nil {
			e.st.DeleteSetting(ctx, RereadNotePrefix+root)
		}
	})
	s.Wait()
}

// A folder takes rows over and the pass ends before they are read again (the reading fails,
// or the process is shut down): the rows have their new root, so no later pass sees them
// arrive. A note in the settings, written before the first row moves, is what keeps the
// re-reading from being lost: the scanner tries again at its next pass, and a start reads
// the folder again from the note, whether the folder came from the command line or was
// added in the UI.
func TestRowsTakenOverAreReadAgainWhateverEndsThePass(t *testing.T) {
	ctx := context.Background()
	defer func(pad time.Duration, n int) { freshRetryPad, freshScanRetries = pad, n }(freshRetryPad, freshScanRetries)
	freshRetryPad, freshScanRetries = time.Millisecond, 1

	type world struct {
		e           *env
		tv, ep, doc string
	}
	setup := func(t *testing.T) *world {
		e := newEnv(t)
		m := filepath.Join(t.TempDir(), "m")
		w := &world{e: e, tv: filepath.Join(m, "TV")}
		w.ep = filepath.Join(w.tv, "Lost (2004)", "Season 1", "Lost.S01E01.mkv")
		w.doc = filepath.Join(w.tv, "Docs", "Planet Earth (2006).mkv")
		write(t, w.ep, "1")
		write(t, w.doc, "22")
		mustScan(t, NewScanner(ctx, e.st, e.fp, []string{m}, nil)) // the old version: no types
		e.makeLegacy()
		return w
	}
	note := func(w *world) string { // the value of the note, or ""
		v, _, err := w.e.st.GetSetting(ctx, RereadNotePrefix+w.tv)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	fail := func(w *world) { // the re-reading cannot be written
		w.e.execRaw(`CREATE TRIGGER no_reread BEFORE UPDATE OF kind ON items BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	}
	allow := func(w *world) { w.e.execRaw(`DROP TRIGGER no_reread`) }
	read := func(t *testing.T, w *world) {
		t.Helper()
		if ep, doc := w.e.item(w.ep), w.e.item(w.doc); ep.Show != "Lost (2004)" || doc.Kind != store.KindEpisode {
			t.Errorf("the rows were not read again as a shows folder reads them: %+v, %+v", ep.Item, doc.Item)
		}
	}
	movedNotRead := func(t *testing.T, w *world) {
		t.Helper()
		if ep, doc := w.e.item(w.ep), w.e.item(w.doc); ep.Root != w.tv || doc.Root != w.tv || ep.Show != "Lost" || doc.Kind != store.KindMovie {
			t.Fatalf("control: want the rows under the new root with the old reading: %+v, %+v", ep.Item, doc.Item)
		}
	}

	// A folder added in the UI, the reading fails: the next pass of the same process reads
	// the rows, though they have their new root now.
	t.Run("the reading fails, the next pass reads", func(t *testing.T) {
		w := setup(t)
		if note(w) != "" {
			t.Fatal("control: a note before anything happened")
		}
		fail(w)
		w.e.setRoots(shows(w.tv)) // SetRoots adds it: its first scan moves the rows
		movedNotRead(t, w)
		if got := note(w); got != "arrived" {
			t.Errorf("note after a pass that could not read the rows = %q, want one", got)
		}
		allow(w)
		mustScan(t, w.e.s)
		read(t, w)
		if got := note(w); got != "" {
			t.Errorf("note after the rows were read = %q, want none", got)
		}
	})

	// The process ends instead: the next start finds the note.
	t.Run("the reading fails, a start reads", func(t *testing.T) {
		w := setup(t)
		fail(w)
		w.e.setRoots(shows(w.tv))
		movedNotRead(t, w)
		allow(w)
		restartRereads(t, w.e, shows(w.tv))
		read(t, w)
		if got := note(w); got != "" {
			t.Errorf("note after the start read the folder = %q, want none", got)
		}
	})

	// A changed file arrives through indexing, not through the walk's move of a row.
	t.Run("a changed file", func(t *testing.T) {
		w := setup(t)
		mustRemove(t, w.ep)
		w.e.execRaw(`DELETE FROM items WHERE path = ?`, w.ep) // only the documentary is left, and it changed
		write(t, w.doc, "a new encode of the documentary")
		future := time.Now().Add(time.Minute)
		if err := os.Chtimes(w.doc, future, future); err != nil {
			t.Fatal(err)
		}
		fail(w)
		w.e.setRoots(shows(w.tv))
		if doc := w.e.item(w.doc); doc.Root != w.tv || doc.Kind != store.KindMovie {
			t.Fatalf("control: want the changed file under the new root with the old reading: %+v", doc.Item)
		}
		if got := note(w); got != "arrived" {
			t.Errorf("note after a changed file arrived = %q, want one", got)
		}
		allow(w)
		restartRereads(t, w.e, shows(w.tv))
		if doc := w.e.item(w.doc); doc.Kind != store.KindEpisode {
			t.Errorf("the changed file was not read again: %+v", doc.Item)
		}
	})

	// The shutdown comes while the pass indexes the new files: the context ends, the re-reading
	// is never reached.
	t.Run("a shutdown in the window", func(t *testing.T) {
		w := setup(t)
		write(t, filepath.Join(w.tv, "New (2005).mkv"), "333")
		g := &gateProber{inner: w.e.fp, entered: make(chan string, 4)}
		open := g.hold(filepath.Join(w.tv, "New (2005).mkv"))
		t.Cleanup(open)
		cctx, cancel := context.WithCancel(ctx)
		s := NewScanner(cctx, w.e.st, g, nil, nil)
		s.SetRoots([]Root{shows(w.tv)}) // the walk moves the rows, then the new file is probed
		<-g.entered
		cancel()
		open()
		s.Wait()
		movedNotRead(t, w)
		if got := note(w); got != "arrived" {
			t.Errorf("note after a shutdown in the window = %q, want one", got)
		}
		restartRereads(t, w.e, shows(w.tv))
		read(t, w)
		if got := note(w); got != "" {
			t.Errorf("note after the start read the folder = %q, want none", got)
		}
	})

	// A note someone else made (the command line's first typed folder) is theirs to clear.
	t.Run("another note stands", func(t *testing.T) {
		w := setup(t)
		w.e.setRootsQuiet(shows(w.tv))
		if err := w.e.st.SetSetting(ctx, RereadNotePrefix+w.tv, "1"); err != nil {
			t.Fatal(err)
		}
		mustScan(t, w.e.s)
		read(t, w)
		if got := note(w); got != "1" {
			t.Errorf("note = %q; the scanner cleared one it did not make", got)
		}
	})

	// An untyped folder takes rows over as they are, and notes nothing.
	t.Run("an untyped folder", func(t *testing.T) {
		w := setup(t)
		w.e.setRootsQuiet(Root{Path: w.tv})
		mustScan(t, w.e.s)
		if got := note(w); got != "" {
			t.Errorf("note for an untyped folder = %q", got)
		}
	})

	// The folder is removed before its rows were read: nothing is left to read, or to note.
	t.Run("the folder is removed", func(t *testing.T) {
		w := setup(t)
		fail(w)
		w.e.setRoots(shows(w.tv))
		if note(w) == "" {
			t.Fatal("control: no note")
		}
		allow(w)
		if _, err := w.e.s.DropRoot(ctx, w.tv, true); err != nil {
			t.Fatal(err)
		}
		if got := note(w); got != "" {
			t.Errorf("note after the folder was removed = %q", got)
		}
		mustScan(t, w.e.s) // and nothing is read again for a folder that is gone
	})
}

// A share that is not mounted at this start (it is the old root of the rows, and the new
// configuration has a narrower folder inside it) is not a disk the files are gone from:
// the rows stay, with their progress. When the share is there and a file is not, it goes.
func TestRowsOfAnUnmountedShareSurviveANarrowerConfiguration(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	vol := filepath.Join(t.TempDir(), "NAS")
	tv := filepath.Join(vol, "TV")
	show := filepath.Join(tv, "Show", "Show.S01E01.mkv")
	film := filepath.Join(vol, "Other", "Film (2000).mkv")
	gone := filepath.Join(vol, "Other", "Gone (2001).mkv")
	for i, p := range []string{show, film, gone} {
		write(t, p, string(make([]byte, i+1)))
	}
	mustScan(t, NewScanner(ctx, e.st, e.fp, []string{vol}, nil)) // the old version: the whole share
	e.progress(e.item(film).ID, 600)
	share := t.TempDir() + "-away"
	if err := os.Rename(vol, share); err != nil { // the NAS is not mounted at this start
		t.Fatal(err)
	}

	s := NewScanner(ctx, e.st, e.fp, []string{tv}, nil)
	s.SetRoots([]Root{shows(tv)})
	s.Wait()
	if err := s.Scan(ctx); err == nil {
		t.Fatal("control: scanning the folder of an unmounted share succeeded")
	}
	for _, p := range []string{show, film, gone} {
		if _, ok := e.items()[p]; !ok {
			t.Errorf("%s was removed while its share was not mounted; left: %v", filepath.Base(p), keys(e.items()))
		}
	}
	if it := e.item(film); it.Progress == nil || it.Progress.PositionSec != 600 {
		t.Errorf("progress of a file on the unmounted share: %+v", it.Progress)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM item_archive`); n != 0 {
		t.Errorf("%d archive rows; nothing was dropped", n)
	}

	// The share is back, and one file is not: that one goes, the others stay.
	if err := os.Rename(share, vol); err != nil {
		t.Fatal(err)
	}
	mustRemove(t, gone)
	mustScan(t, s)
	if _, ok := e.items()[gone]; ok {
		t.Error("control: a file that is really gone, on a share that is mounted, was kept")
	}
	if _, ok := e.items()[film]; !ok {
		t.Error("a file that is there was removed")
	}
}

// The same for a volume mounted inside the old root: its mount point is back on the
// device of its parent, and its rows are kept.
func TestRowsUnderAnUnmountedVolumeInsideAnOldRootSurvive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	vol := filepath.Join(t.TempDir(), "media")
	tv := filepath.Join(vol, "TV")
	show := filepath.Join(tv, "Show", "Show.S01E01.mkv")
	other := filepath.Join(vol, "Disk2", "Film (2000).mkv")
	write(t, show, "1")
	write(t, other, "22")
	earlier := NewScanner(ctx, e.st, e.fp, []string{vol}, nil)
	earlier.devOf = func(path string) (uint64, bool) {
		if filepath.Base(path) == "Disk2" {
			return 2, true // another device: a volume mounted here
		}
		return 1, true
	}
	mustScan(t, earlier)                     // learns the mount point, and keeps it in the database
	if err := os.Remove(other); err != nil { // the volume goes: its mount point is an empty directory
		t.Fatal(err)
	}

	s := NewScanner(ctx, e.st, e.fp, []string{tv}, nil)
	s.devOf = func(string) (uint64, bool) { return 1, true } // all on one device now
	s.SetRoots([]Root{shows(tv)})
	s.Wait()
	mustScan(t, s)
	if _, ok := e.items()[other]; !ok {
		t.Errorf("a row under a volume that is not mounted now was removed; left: %v", keys(e.items()))
	}
}

// A scan of a folder that fails to remove what is gone knows nothing of it whole: no scan
// time, and no guess for a -media folder, as in a full scan.
func TestAPartialScanThatCannotRemoveNotesNoScanAndGuessesNothing(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "media")
	for i, n := range []string{"Dune (2021)", "Heat (1995)", "Alien (1979)"} {
		write(t, filepath.Join(root, n+".mkv"), string(make([]byte, i+1)))
	}
	// A row whose file is not there: the scan has something to remove.
	stale := filepath.Join(root, "Stale (1999).mkv")
	if _, err := e.st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Stale", Path: stale, Root: root, Size: 9, MTime: 9}, nil); err != nil {
		t.Fatal(err)
	}
	e.setRootsQuiet(Root{Path: root})
	e.execRaw(`CREATE TRIGGER no_delete BEFORE DELETE ON items BEGIN SELECT RAISE(ABORT, 'refused'); END`)

	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err == nil {
		t.Fatal("control: the removal was meant to fail")
	}
	if !e.s.LastScan(root).IsZero() {
		t.Errorf("a scan that could not remove what is gone noted a scan of the folder: %v", e.s.LastScan(root))
	}
	if kind, _ := e.s.rootKind(root); kind != "" {
		t.Errorf("type = %q; a folder not known whole is not guessed", kind)
	}

	// Control: with the removal possible, the same scan notes the time and guesses.
	e.execRaw(`DROP TRIGGER no_delete`)
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if e.s.LastScan(root).IsZero() {
		t.Error("control: no scan time once the removal worked")
	}
	if kind, _ := e.s.rootKind(root); kind != KindMovies {
		t.Errorf("control: type = %q, want movies", kind)
	}
	if _, ok := e.items()[stale]; ok {
		t.Error("control: the stale row is still there")
	}
}

// The scan of a folder that was just added looks again when it could not run (the library
// could not be read, say), a bounded number of times, after the usual wait.
func TestAFreshFolderIsScannedAgainWhenTheScanCouldNotRun(t *testing.T) {
	defer func(pad time.Duration, n int) { freshRetryPad, freshScanRetries = pad, n }(freshRetryPad, freshScanRetries)
	freshRetryPad, freshScanRetries = 5*time.Millisecond, 3

	newRoot := func(t *testing.T) (*env, string) {
		e := newEnv(t)
		root := filepath.Join(t.TempDir(), "films")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		return e, root
	}
	failing := func(e *env, failures int, calls *atomic.Int32) {
		e.s.fileStates = func(ctx context.Context) (map[string]store.FileState, error) {
			if int(calls.Add(1)) <= failures {
				return nil, errors.New("database is locked")
			}
			return e.st.FileStates(ctx)
		}
	}

	t.Run("recovers", func(t *testing.T) {
		e, root := newRoot(t)
		var calls atomic.Int32
		failing(e, 2, &calls)
		e.setRoots(movies(root))
		if e.count() != 1 {
			t.Errorf("%d items; the folder was given up on after the library could not be read", e.count())
		}
		if got := calls.Load(); got != 3 {
			t.Errorf("the library was read %d times, want 3 (two failures and the scan)", got)
		}
		if e.s.LastScan(root).IsZero() {
			t.Error("no scan time once the scan ran")
		}
	})

	t.Run("bounded", func(t *testing.T) {
		e, root := newRoot(t)
		var calls atomic.Int32
		failing(e, 1000, &calls)
		e.setRoots(movies(root))
		if got := calls.Load(); got != int32(freshScanRetries)+1 {
			t.Errorf("the scan was tried %d times, want %d", got, freshScanRetries+1)
		}
		if e.count() != 0 {
			t.Errorf("%d items from a library that could not be read", e.count())
		}
	})

	// Any error: a file that could not be probed this time is probed the next.
	t.Run("a scan that failed on a file", func(t *testing.T) {
		e, root := newRoot(t)
		file := filepath.Join(root, "Dune (2021).mkv")
		e.fp.fail.Store(file, struct{}{})
		var calls atomic.Int32
		e.s.fileStates = func(ctx context.Context) (map[string]store.FileState, error) {
			if calls.Add(1) == 2 {
				e.fp.fail.Delete(file) // the disk is fine by the second try
			}
			return e.st.FileStates(ctx)
		}
		e.setRoots(movies(root))
		if e.count() != 1 {
			t.Errorf("%d items; a scan that ended with errors was not tried again", e.count())
		}
	})
}

// A pass that waits for its turn while DropRoot has taken a folder out does not walk
// the folder when its turn comes: the roots are looked at then, not when it queued.
func TestPassesQueuedBehindARunningOneSkipTheFolderDropRootTookOut(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	base, err := filepath.EvalSymlinks(t.TempDir()) // the paths a walk sees: /private/var, not /var, on macOS
	if err != nil {
		t.Fatal(err)
	}
	root, other := filepath.Join(base, "films"), filepath.Join(base, "other")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	write(t, filepath.Join(root, "Sub", "Old (2000).mkv"), "11")
	write(t, filepath.Join(other, "Heat (1995).mkv"), "22")
	e.setRoots(movies(root), movies(other))
	fresh := filepath.Join(root, "Sub", "New (2002).mkv")
	write(t, fresh, "333") // a walk of the folder would index it
	newOther := filepath.Join(other, "Sub", "New (2003).mkv")
	write(t, newOther, "4444") // and a walk of the other one must
	calls := e.fp.calls.Load()
	var walkedRoot, walkedOther atomic.Int32 // directories that were looked at
	e.s.devOf = func(path string) (uint64, bool) {
		switch {
		case path == root || within(root, path):
			walkedRoot.Add(1)
		case path == other || within(other, path):
			walkedOther.Add(1)
		}
		return deviceOf(path)
	}

	queued := make(chan struct{}, 8)
	e.s.queued = func() { queued <- struct{}{} }
	release := e.hold()
	var wg sync.WaitGroup
	if !e.s.Trigger() { // a full scan, queued
		t.Fatal("Trigger refused")
	}
	<-queued
	wg.Add(2)
	var partialErr error
	go func() { // a partial one, of both folders
		defer wg.Done()
		_, partialErr = e.s.ScanDirs(ctx, []Dir{{Path: root, Recursive: true}, {Path: other, Recursive: true}})
	}()
	<-queued
	var n int
	go func() { defer wg.Done(); n, _ = e.s.DropRoot(ctx, root, false) }()
	<-queued
	waitUntil(t, "the folder is taken out", func() bool { return e.s.rootRetired(root) })
	release()
	wg.Wait()
	e.s.Wait()

	// The passes did run: they walked the other folder, and indexed its new file.
	if partialErr != nil {
		t.Errorf("the partial scan = %v", partialErr)
	}
	if st := e.s.Status(ctx); st.LastFinished == nil || st.LastError != "" {
		t.Errorf("the full scan did not run: %+v", st)
	}
	if _, ok := e.items()[newOther]; !ok {
		t.Error("control: the new file of the other folder was not indexed by the queued passes")
	}
	if walkedOther.Load() == 0 {
		t.Error("control: the queued passes looked at no directory of the other folder")
	}
	// And they left the folder that was taken out alone.
	if n != 2 {
		t.Errorf("DropRoot removed %d items, want its 2", n)
	}
	if got := e.fp.calls.Load(); got != calls+1 {
		t.Errorf("%d files probed by the passes that were queued, want only the other folder's new one", got-calls)
	}
	if got := walkedRoot.Load(); got != 0 {
		t.Errorf("the passes that were queued looked at %d directories of a folder that was taken out", got)
	}
	if _, ok := e.items()[fresh]; ok {
		t.Error("a file of the folder that was taken out was indexed")
	}
}

// DropRoot waits a limited time for the pass that is running, and passes queued ahead of it
// are no reason to wait longer: it gives up, the folder is back and nothing is lost, and
// the queued passes run when the pass ends.
func TestDropRootGivesUpWithPassesQueuedAheadOfIt(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	defer func(d time.Duration) { dropWait = d }(dropWait)
	dropWait = 60 * time.Millisecond
	base := t.TempDir()
	root := filepath.Join(base, "films")
	file := filepath.Join(root, "Dune (2021).mkv")
	write(t, file, "1")
	e.setRoots(movies(root))
	e.progress(e.item(file).ID, 600)
	fresh := filepath.Join(root, "New (2002).mkv")
	write(t, fresh, "22")

	queued := make(chan struct{}, 8)
	e.s.queued = func() { queued <- struct{}{} }
	release := e.hold()
	if !e.s.Trigger() { // a full scan, queued
		t.Fatal("Trigger refused")
	}
	<-queued
	var wg sync.WaitGroup
	wg.Go(func() { e.s.ScanDirs(ctx, []Dir{{Path: root, Recursive: true}}) }) // a partial one
	<-queued

	start := time.Now()
	n, err := e.s.DropRoot(ctx, root, false)
	if !errors.Is(err, ErrScanBusy) || n != 0 {
		t.Fatalf("DropRoot = %d, %v; want ErrScanBusy", n, err)
	}
	if d := time.Since(start); d < dropWait || d > 5*time.Second {
		t.Errorf("DropRoot waited %v with dropWait at %v", d, dropWait)
	}
	if !rootsContain(e.s, root) || e.s.rootRetired(root) {
		t.Error("the folder is not back where it was")
	}
	if it := e.item(file); it.Progress == nil || it.Progress.PositionSec != 600 {
		t.Errorf("progress after the DropRoot that gave up: %+v", it.Progress)
	}
	if _, ok := e.items()[fresh]; ok {
		t.Fatal("control: the queued passes ran before the pass in front of them ended")
	}

	release()
	wg.Wait()
	e.s.Wait()
	if _, ok := e.items()[fresh]; !ok {
		t.Error("the queued passes (and the folder put back) did not scan the folder")
	}
	if st := e.s.Status(ctx); st.Running || st.LastFinished == nil {
		t.Errorf("after everything ended: %+v", st)
	}
}

// Once the scanner is closed nothing starts in the background: a request goroutine that
// puts a folder back or adds one cannot race the Wait of a shutdown.
func TestNothingStartsInTheBackgroundOnceTheScannerIsClosed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	file := filepath.Join(root, "Dune (2021).mkv")
	write(t, file, "1")
	e.s.Close()

	e.s.SetRoots([]Root{movies(root)})
	if e.s.Trigger() {
		t.Error("Trigger started a scan of a closed scanner")
	}
	if e.s.full.Load() {
		t.Error("a refused Trigger left the scanner looking busy")
	}
	var done atomic.Int32
	e.s.Reread([]string{root}, func(string, error) { done.Add(1) })
	e.s.putBack(root, &takenRoot{Root: movies(root)}) // what DropRoot does when it fails
	e.s.Wait()
	if e.count() != 0 {
		t.Errorf("%d items; a closed scanner scanned a folder", e.count())
	}
	if done.Load() != 0 {
		t.Error("a closed scanner read a folder again")
	}
	if !rootsContain(e.s, root) {
		t.Error("control: SetRoots did not take effect (only the background work is refused)")
	}
	// Its own passes still run, for whoever calls them.
	if err := e.s.Scan(ctx); err != nil || e.count() != 1 {
		t.Errorf("Scan after Close = %v, %d items", err, e.count())
	}

	// The Wait of a shutdown closes the scanner whose context has ended.
	ended, cancel := context.WithCancel(ctx)
	s := NewScanner(ended, e.st, e.fp, nil, nil)
	cancel()
	s.Wait()
	if s.Trigger() {
		t.Error("a scanner whose context ended started a scan after Wait")
	}
	s.Wait()
}

// The status says running while any pass is under way or waiting for its turn, not only
// during a full scan: a page that follows a folder it just added sees its scan through.
func TestStatusIsRunningWhileTheBackgroundPassesRun(t *testing.T) {
	ctx := context.Background()
	running := func(e *env) bool { return e.s.Status(ctx).Running }
	base := t.TempDir()

	t.Run("a folder that was added", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(base, "added")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		if running(e) {
			t.Fatal("control: a scanner that does nothing says it is running")
		}
		release := e.hold() // a pass is under way: the scan of the new folder waits for its turn
		e.s.SetRoots([]Root{movies(root)})
		if !running(e) {
			t.Error("not running right after a folder was added")
		}
		release()
		e.s.Wait()
		if running(e) {
			t.Error("still running after the scan of the folder ended")
		}
		if e.count() != 1 {
			t.Errorf("control: %d items", e.count())
		}
	})

	t.Run("a type that changed", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(base, "retyped")
		write(t, filepath.Join(root, "Lost.S01E01.mkv"), "1")
		e.setRoots(movies(root))
		if running(e) {
			t.Fatal("control: running after the first scan ended")
		}
		release := e.hold()
		e.s.SetRoots([]Root{shows(root)})
		if !running(e) {
			t.Error("not running right after a folder's type changed")
		}
		release()
		e.s.Wait()
		if running(e) {
			t.Error("still running after the items were read again")
		}
		if it := e.item(filepath.Join(root, "Lost.S01E01.mkv")); it.Kind != store.KindEpisode {
			t.Errorf("control: the items were not read again: %+v", it.Item)
		}
	})

	t.Run("a first typed folder and a full scan", func(t *testing.T) {
		e := newEnv(t)
		root := filepath.Join(base, "typed")
		write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
		e.setRootsQuiet(movies(root))
		release := e.hold()
		e.s.Reread([]string{root}, nil)
		if !running(e) {
			t.Error("not running while Reread waits for its turn")
		}
		release()
		e.s.Wait()
		if running(e) {
			t.Error("still running after Reread")
		}
		release = e.hold()
		if !e.s.Trigger() {
			t.Fatal("Trigger refused")
		}
		if !running(e) {
			t.Error("not running while a full scan waits for its turn")
		}
		release()
		e.s.Wait()
		st := e.s.Status(ctx)
		if st.Running || st.LastFinished == nil {
			t.Errorf("after the full scan: %+v; want idle, with the scan finished", st)
		}
	})

	// A partial scan a caller runs itself (the watcher does), and the full scan of the
	// periodic loop, are passes too.
	t.Run("passes the caller runs", func(t *testing.T) {
		e := newEnv(t)
		g := &gateProber{inner: e.fp, entered: make(chan string, 4)}
		e.s.prober = g
		root := filepath.Join(base, "direct")
		file := filepath.Join(root, "Dune (2021).mkv")
		write(t, file, "1")
		e.setRootsQuiet(movies(root))
		for name, pass := range map[string]func() error{
			"partial": func() error { _, err := e.s.ScanDirs(ctx, []Dir{{Path: root, Recursive: true}}); return err },
			"full":    func() error { return e.s.Scan(ctx) },
		} {
			open := g.hold(file)
			done := make(chan error, 1)
			go func() { done <- pass() }()
			<-g.entered // the scan is at the file
			if !running(e) {
				t.Errorf("%s scan: not running while it works", name)
			}
			open()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if running(e) {
				t.Errorf("%s scan: still running after it ended", name)
			}
			e.execRaw(`DELETE FROM items`) // so that the next one has the file to probe again
		}
	})

	// Two pieces of background work at once: it is running until both are over, however
	// they end one after the other.
	t.Run("overlapping passes", func(t *testing.T) {
		e := newEnv(t)
		g := &gateProber{inner: e.fp, entered: make(chan string, 4)}
		e.s.prober = g
		a := filepath.Join(base, "a")
		fa := filepath.Join(a, "A (2000).mkv")
		write(t, fa, "1")
		openA := g.hold(fa)
		inDone, letGo := make(chan struct{}), make(chan struct{})
		finish := sync.OnceFunc(func() { close(letGo) })
		t.Cleanup(func() { openA(); finish() })

		e.s.SetRoots([]Root{movies(a)}) // its scan stops at A's file
		if p := <-g.entered; p != fa {
			t.Fatalf("probing %s first", p)
		}
		// Reading the folder again waits behind it, and stops when it is its turn.
		e.s.Reread([]string{a}, func(string, error) { close(inDone); <-letGo })
		openA()
		<-inDone // the first one is over (or about to leave), the second is at work
		waitUntil(t, "the first one has left", func() bool {
			e.s.mu.Lock()
			defer e.s.mu.Unlock()
			return e.s.busy <= 1
		})
		if !running(e) {
			t.Error("not running while the second works, after the first ended")
		}
		finish()
		e.s.Wait()
		if running(e) {
			t.Error("still running after both ended")
		}
	})
}

// A pass that panics does not leave the scanner running for ever.
func TestAFullScanThatPanicsLeavesTheScannerIdle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.s.fileStates = func(context.Context) (map[string]store.FileState, error) { panic("boom") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("control: the scan did not panic")
			}
		}()
		e.s.Scan(ctx)
	}()
	if st := e.s.Status(ctx); st.Running || st.LastFinished == nil {
		t.Errorf("status after a scan that panicked = %+v; want idle, finished", st)
	}
}

// A context that has ended gets no turn, however free the semaphore is.
func TestAnEndedContextGetsNoTurn(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.s.acquire(ctx); err != nil { // control: a free semaphore is taken
		t.Fatal(err)
	}
	e.s.release()
	cancel()
	if err := e.s.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire with an ended context = %v", err)
	}
	if len(e.s.sem) != 0 {
		t.Error("the semaphore is held by a pass that was refused")
	}
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	e.setRootsQuiet(movies(root))
	if _, err := e.s.ScanDirs(ctx, []Dir{{Path: root, Recursive: true}}); !errors.Is(err, context.Canceled) {
		t.Errorf("ScanDirs with an ended context = %v", err)
	}
	if e.count() != 0 {
		t.Error("a scan with an ended context indexed a file")
	}
}

// The status is not running while the scan of a new folder sleeps before it looks again at
// files still being written, only while a pass runs or waits for its turn.
func TestStatusIsIdleWhileAFreshScanSleeps(t *testing.T) {
	defer func(pad time.Duration, n int) { freshRetryPad, freshScanRetries = pad, n }(freshRetryPad, freshScanRetries)
	freshRetryPad, freshScanRetries = 600*time.Millisecond, 1
	ctx := context.Background()
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	var calls atomic.Int32
	during := make(chan bool, 1) // whether the status said running, in the second look
	e.s.fileStates = func(ctx context.Context) (map[string]store.FileState, error) {
		if calls.Add(1) == 2 {
			during <- e.s.Status(ctx).Running
		}
		if calls.Load() == 1 {
			return nil, errors.New("database is locked") // the first look fails: the scan sleeps
		}
		return e.st.FileStates(ctx)
	}
	e.s.SetRoots([]Root{movies(root)})

	waitUntil(t, "the first look", func() bool { return calls.Load() >= 1 })
	deadline := time.Now().Add(300 * time.Millisecond) // well inside the sleep
	for e.s.Status(ctx).Running {
		if time.Now().After(deadline) {
			t.Fatal("still running while the scan of the new folder sleeps")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatal("control: the second look began before the check")
	}
	if !<-during {
		t.Error("not running in the second look")
	}
	e.s.Wait()
	if e.s.Status(ctx).Running || e.count() != 1 {
		t.Errorf("after the scan: running %v, %d items", e.s.Status(ctx).Running, e.count())
	}
}

// Folders put back and scans triggered from other goroutines while the scanner shuts down:
// under -race, Add never meets Wait.
func TestBackgroundWorkRacingAShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "films")
	write(t, filepath.Join(root, "Dune (2021).mkv"), "1")
	s := NewScanner(ctx, e.st, e.fp, []string{root}, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				s.putBack(root, &takenRoot{Root: movies(root)})
				s.Trigger()
				s.SetRoots([]Root{movies(filepath.Join(root, "x"))})
			}
		})
	}
	time.Sleep(2 * time.Millisecond)
	cancel()
	s.Wait() // the shutdown
	wg.Wait()
	s.Wait()
}
