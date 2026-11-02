package library

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"ply/internal/store"
)

func scanDirs(t *testing.T, s *Scanner, dirs ...Dir) Result {
	t.Helper()
	res, err := s.ScanDirs(context.Background(), dirs)
	if err != nil {
		t.Fatalf("ScanDirs: %v", err)
	}
	return res
}

func idOf(t *testing.T, st *store.Store, path string) int64 {
	t.Helper()
	states, err := st.FileStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, ok := states[path]
	if !ok {
		return 0
	}
	return s.ID
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

// A partial scan indexes and removes only inside the directories it is given.
func TestScanDirsStaysInScope(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	oldA, oldB, deep := filepath.Join(a, "old a.mkv"), filepath.Join(b, "old b.mkv"), filepath.Join(a, "sub", "deep.mkv")
	write(t, oldA, "1")
	write(t, oldB, "22")
	write(t, deep, "333")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	newA, newB := filepath.Join(a, "new a.mkv"), filepath.Join(b, "new b.mkv")
	write(t, newA, "4444")
	write(t, newB, "55555")
	mustRemove(t, oldA)
	mustRemove(t, oldB)
	mustRemove(t, deep)

	res := scanDirs(t, s, Dir{Path: a})
	got := paths(t, st)
	for path, want := range map[string]bool{newA: true, oldA: false, newB: false, oldB: true, deep: true} {
		if _, ok := got[path]; ok != want {
			t.Errorf("after ScanDirs(A): %s indexed = %v, want %v", filepath.Base(path), ok, want)
		}
	}
	if id := idOf(t, st, newA); !slices.Equal(res.Added, []int64{id}) {
		t.Errorf("Added = %v, want [%d] (new a only)", res.Added, id)
	}

	// Recursive reaches the subdirectory.
	scanDirs(t, s, Dir{Path: a, Recursive: true})
	if _, ok := paths(t, st)[deep]; ok {
		t.Error("a recursive scan of A kept the row of a file deleted from A/sub")
	}
}

// A directory that is gone is not a reason to remove anything: the scan of its
// parent is.
func TestScanDirsOfAMissingDirectoryRemovesNothing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Movie (2020)")
	file := filepath.Join(dir, "Movie (2020).mkv")
	write(t, file, "1")
	write(t, filepath.Join(root, "Other (2021).mkv"), "22") // the root stays non-empty
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	mustRemove(t, dir)
	scanDirs(t, s, Dir{Path: dir})
	if _, ok := paths(t, st)[file]; !ok {
		t.Fatal("a scan of a vanished directory removed its rows")
	}
	scanDirs(t, s, Dir{Path: root, Recursive: true}) // positive control
	if _, ok := paths(t, st)[file]; ok {
		t.Error("the scan of the parent did not remove the row of the vanished directory")
	}
}

// A root that turns up empty in a partial scan keeps its rows, like in a full
// scan, while a subdirectory that is emptied loses them.
func TestScanDirsOfAnEmptyRootRemovesNothing(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "Show", "Season 1")
	ep := filepath.Join(sub, "S01E01.mkv")
	top := filepath.Join(root, "Top (2021).mkv")
	write(t, ep, "1")
	write(t, top, "22")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	mustRemove(t, ep)
	scanDirs(t, s, Dir{Path: sub}) // positive control: an emptied subdirectory
	if _, ok := paths(t, st)[ep]; ok {
		t.Fatal("the row of a file deleted from a subdirectory was kept")
	}

	mustRemove(t, top)
	if _, err := s.ScanDirs(context.Background(), []Dir{{Path: root}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := paths(t, st)[top]; !ok {
		t.Error("a partial scan of a root with no video files left removed its rows (unmounted volume?)")
	}
}

// Renaming a file keeps its item: id, progress and all; the new name is parsed.
func TestRenameKeepsTheItem(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "movie.mkv")
	to := filepath.Join(root, "Blade Runner (1982).mkv")
	write(t, from, "1")
	s, st, fp := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, from)
	if _, err := st.SetProgress(context.Background(), id, 30, 600); err != nil {
		t.Fatal(err)
	}
	var added []int64
	s.OnAdded(func(ids []int64) { added = append(added, ids...) })
	probes := fp.calls.Load()

	mustRename(t, from, to)
	mustScan(t, s)

	if got := idOf(t, st, to); got != id {
		t.Fatalf("renamed file has id %d, want %d (the old item)", got, id)
	}
	if idOf(t, st, from) != 0 {
		t.Error("the old path is still indexed")
	}
	e, err := st.GetEntry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "Blade Runner" || e.Year != 1982 {
		t.Errorf("title/year = %q/%d, want them parsed from the new name", e.Title, e.Year)
	}
	if e.Progress == nil || e.Progress.PositionSec != 30 {
		t.Errorf("progress = %+v, want it kept", e.Progress)
	}
	if len(e.Streams) == 0 {
		t.Error("streams were lost")
	}
	if n := fp.calls.Load() - probes; n != 0 {
		t.Errorf("a moved file was probed %d times, want 0", n)
	}
	if !slices.Equal(added, []int64{id}) {
		t.Errorf("OnAdded got %v, want [%d]", added, id)
	}
}

// A move from a root scanned first to one scanned later keeps the item: rows
// are removed only after every root has been walked.
func TestMoveBetweenRootsKeepsTheItem(t *testing.T) {
	base := t.TempDir()
	r1, r2 := filepath.Join(base, "r1"), filepath.Join(base, "r2")
	from := filepath.Join(r1, "Film (2001).mkv")
	to := filepath.Join(r2, "Film (2001).mkv")
	write(t, from, "1")
	write(t, filepath.Join(r1, "Other (2002).mkv"), "22") // keeps r1 non-empty
	write(t, filepath.Join(r2, "Third (2003).mkv"), "333")
	s, st, _ := newScanner(t, r1, r2)
	mustScan(t, s)
	id := idOf(t, st, from)

	mustRename(t, from, to)
	mustScan(t, s)
	if got := idOf(t, st, to); got != id {
		t.Fatalf("moved file has id %d, want %d", got, id)
	}
	if e := paths(t, st)[to]; e.Root != r2 {
		t.Errorf("moved file belongs to %q, want %q", e.Root, r2)
	}
}

// Partial scans pair a move when both directories are in the call, and also
// when only the destination is (the old file is known to be gone).
func TestScanDirsPairsMoves(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	one, two := filepath.Join(a, "one.mkv"), filepath.Join(a, "two.mkv")
	write(t, one, "1")
	write(t, two, "22")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id1, id2 := idOf(t, st, one), idOf(t, st, two)

	mustRename(t, one, filepath.Join(b, "one.mkv"))
	scanDirs(t, s, Dir{Path: a}, Dir{Path: b})
	if got := idOf(t, st, filepath.Join(b, "one.mkv")); got != id1 {
		t.Errorf("both dirs in one call: id %d, want %d", got, id1)
	}

	mustRename(t, two, filepath.Join(b, "two.mkv"))
	res := scanDirs(t, s, Dir{Path: b})
	if got := idOf(t, st, filepath.Join(b, "two.mkv")); got != id2 {
		t.Errorf("only the destination scanned: id %d, want %d", got, id2)
	}
	if !slices.Equal(res.Added, []int64{id2}) {
		t.Errorf("Added = %v, want [%d]", res.Added, id2)
	}
	if idOf(t, st, two) != 0 {
		t.Error("the old path is still indexed")
	}
}

// A move that did not keep the mtime (cp without -p, then rm) is paired by the
// content fingerprint: same id, no probe, and the row takes the new mtime.
func TestMoveWithANewMtimeIsPairedByFingerprint(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "A", "Film (2001).mkv")
	data := bytes.Repeat([]byte("frame"), 50000) // 250 KB: head and tail are separate reads
	writeBytes(t, from, data)
	write(t, filepath.Join(root, "Other (2002).mkv"), "x")
	s, st, fp := newScanner(t, root)
	scanDirs(t, s, Dir{Path: root}) // a partial scan: no backfill to cover for indexing
	id := idOf(t, st, from)
	if e, _ := st.GetEntry(context.Background(), id); e.Fingerprint == "" {
		t.Fatal("indexing did not record a fingerprint")
	}

	to := filepath.Join(root, "B", "Film (2001).mkv")
	writeBytes(t, to, data)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(to, later, later); err != nil {
		t.Fatal(err)
	}
	mustRemove(t, from)
	// A look-alike of the same size but other content must not be the one.
	decoy := filepath.Join(root, "B", "Decoy (2003).mkv")
	other := bytes.Clone(data)
	other[len(other)-1] ^= 1
	writeBytes(t, decoy, other)
	probes := fp.calls.Load()

	mustScan(t, s)
	if got := idOf(t, st, to); got != id {
		t.Fatalf("copied-then-deleted file has id %d, want %d", got, id)
	}
	if got := idOf(t, st, decoy); got == id || got == 0 {
		t.Errorf("decoy id = %d, want a new item", got)
	}
	if n := fp.calls.Load() - probes; n != 1 {
		t.Errorf("probed %d files, want 1 (the decoy)", n)
	}
	mustScan(t, s)
	if n := fp.calls.Load() - probes; n != 1 {
		t.Errorf("the next scan probed again (%d): the moved row kept its old mtime", n-1)
	}
}

// Items indexed before fingerprints existed get one from full scans.
func TestFingerprintBackfill(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Film (2001).mkv")
	write(t, file, "content")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, file)
	e, _ := st.GetEntry(context.Background(), id)
	want := e.Fingerprint
	if err := st.SetFingerprint(context.Background(), id, e.Size, e.MTime, ""); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetEntry(context.Background(), id); e.Fingerprint != "" {
		t.Fatal("setup: fingerprint not cleared")
	}
	mustScan(t, s)
	if e, _ := st.GetEntry(context.Background(), id); e.Fingerprint != want {
		t.Errorf("fingerprint after a full scan = %q, want %q", e.Fingerprint, want)
	}
}

// Two new files with the moved one's size and mtime: the fingerprint tells them
// apart when their content differs, and when it does not (true duplicates)
// neither takes the item over.
func TestAmbiguousMoveIsNotPaired(t *testing.T) {
	for _, dup := range []bool{false, true} {
		t.Run(map[bool]string{false: "content differs", true: "duplicates"}[dup], func(t *testing.T) {
			root := t.TempDir()
			from := filepath.Join(root, "orig.mkv")
			write(t, from, "1")
			stamp := time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC)
			if err := os.Chtimes(from, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			s, st, _ := newScanner(t, root)
			mustScan(t, s)
			id := idOf(t, st, from)

			c1, c2 := filepath.Join(root, "copy1.mkv"), filepath.Join(root, "copy2.mkv")
			mustRename(t, from, c1)
			write(t, c2, map[bool]string{false: "2", true: "1"}[dup])
			if err := os.Chtimes(c2, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			mustScan(t, s)
			got := paths(t, st)
			if len(got) != 2 {
				t.Fatalf("items = %v, want copy1 and copy2", keys(got))
			}
			if dup {
				for _, p := range []string{c1, c2} {
					if got[p].ID == id {
						t.Errorf("%s took over item %d although two files matched it", filepath.Base(p), id)
					}
				}
			} else if got[c1].ID != id {
				t.Errorf("copy1 (the renamed original) has id %d, want %d", got[c1].ID, id)
			}
		})
	}
}

// A row whose root looks unmounted is not handed to a look-alike file
// elsewhere: the original may come back.
func TestMoveIsNotPairedFromAnUnavailableRoot(t *testing.T) {
	for _, mounted := range []bool{true, false} {
		t.Run(map[bool]string{true: "mounted (control)", false: "unmounted"}[mounted], func(t *testing.T) {
			base := t.TempDir()
			disk, backup := filepath.Join(base, "disk"), filepath.Join(base, "backup")
			orig := filepath.Join(disk, "Film (2001).mkv")
			write(t, orig, "1")
			write(t, filepath.Join(disk, "Other (2002).mkv"), "22")
			if err := os.MkdirAll(backup, 0o755); err != nil {
				t.Fatal(err)
			}
			s, st, _ := newScanner(t, disk, backup)
			mustScan(t, s)
			id := idOf(t, st, orig)

			// The file shows up on the backup root with the same size and mtime.
			copyTo := filepath.Join(backup, "Film (2001).mkv")
			mustRename(t, orig, copyTo)
			if !mounted {
				mustRemove(t, filepath.Join(disk, "Other (2002).mkv")) // disk is now an empty mountpoint
			}
			_ = s.Scan(context.Background()) // the empty root is reported as an error
			paired := idOf(t, st, copyTo) == id
			if paired != mounted {
				t.Errorf("paired = %v, want %v", paired, mounted)
			}
		})
	}
}

// Files modified within the settle time are left for later: a new one is not
// indexed, a changed one keeps its old row and is not re-probed.
func TestSettle(t *testing.T) {
	root := t.TempDir()
	changed := filepath.Join(root, "Changed (2001).mkv")
	write(t, changed, "1")
	s, st, fp := newScanner(t, root)
	mustScan(t, s)
	before := paths(t, st)[changed]

	now := time.Now()
	s.SetSettle(time.Minute)
	s.now = func() time.Time { return now }
	fresh := filepath.Join(root, "Sub", "Fresh (2002).mkv")
	stable := filepath.Join(root, "Sub", "Stable (2003).mkv")
	write(t, fresh, "22")
	write(t, stable, "333")
	write(t, changed, "4444")
	old := now.Add(-2 * time.Minute)
	if err := os.Chtimes(stable, old, old); err != nil {
		t.Fatal(err)
	}
	probes := fp.calls.Load()

	res := scanDirs(t, s, Dir{Path: root, Recursive: true})
	got := paths(t, st)
	if _, ok := got[stable]; !ok {
		t.Fatal("control: a file older than the settle time was not indexed")
	}
	if _, ok := got[fresh]; ok {
		t.Error("a file modified within the settle time was indexed")
	}
	if e, ok := got[changed]; !ok || e.Size != before.Size {
		t.Errorf("a file being rewritten: row %+v, want the old row kept", e.Item)
	}
	if n := fp.calls.Load() - probes; n != 1 {
		t.Errorf("probed %d files, want 1 (the stable one)", n)
	}
	want := []Dir{{Path: root}, {Path: filepath.Join(root, "Sub")}}
	if !slices.Equal(res.Deferred, want) {
		t.Errorf("Deferred = %v, want %v", res.Deferred, want)
	}

	now = now.Add(2 * time.Minute)
	res = scanDirs(t, s, res.Deferred...)
	got = paths(t, st)
	if _, ok := got[fresh]; !ok {
		t.Error("the settled file was not indexed on the next scan")
	}
	if got[changed].Size == before.Size {
		t.Error("the settled rewrite was not re-indexed")
	}
	if len(res.Deferred) != 0 {
		t.Errorf("Deferred = %v, want none", res.Deferred)
	}
}

// OnSidecarsChanged gets the items under the directories that carried
// Sidecars, without those just added.
func TestSidecarsHook(t *testing.T) {
	root := t.TempDir()
	show := filepath.Join(root, "Show")
	e1 := filepath.Join(show, "Season 1", "S01E01.mkv")
	e2 := filepath.Join(show, "Season 2", "S02E01.mkv")
	movie := filepath.Join(root, "Movie (2020).mkv")
	write(t, e1, "1")
	write(t, e2, "22")
	write(t, movie, "333")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	e3 := filepath.Join(show, "Season 2", "S02E02.mkv")
	write(t, e3, "4444")
	renamed := filepath.Join(show, "Season 2", "S02E01 Renamed.mkv")
	mustRename(t, e2, renamed)
	var sidecars, added []int64
	s.OnSidecarsChanged(func(ids []int64) { sidecars = ids })
	s.OnAdded(func(ids []int64) { added = ids })
	scanDirs(t, s, Dir{Path: show, Sidecars: true}, Dir{Path: filepath.Join(show, "Season 2")})

	if want := []int64{idOf(t, st, e1)}; !slices.Equal(sidecars, want) {
		t.Errorf("sidecars = %v, want %v (the show's episodes, not the movie, not the new or moved one)", sidecars, want)
	}
	want := []int64{idOf(t, st, e3), idOf(t, st, renamed)}
	slices.Sort(want)
	slices.Sort(added)
	if !slices.Equal(added, want) {
		t.Errorf("added = %v, want %v (the new and the moved episode)", added, want)
	}
}

// Directories outside the roots or inside hidden ones are ignored; overlapping
// ones are walked once; a root is scanned recursively.
func TestScanDirsScopes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	write(t, filepath.Join(outside, "x.mkv"), "1")
	write(t, filepath.Join(root, ".hidden", "h.mkv"), "22")
	nested := filepath.Join(root, "a", "b", "n.mkv")
	write(t, nested, "333")
	s, st, fp := newScanner(t, root)

	scanDirs(t, s, Dir{Path: outside}, Dir{Path: filepath.Join(root, ".hidden")}, Dir{Path: "relative"})
	if n := len(paths(t, st)); n != 0 || fp.calls.Load() != 0 {
		t.Fatalf("out-of-scope dirs indexed %d items (%d probes)", n, fp.calls.Load())
	}
	scanDirs(t, s, Dir{Path: filepath.Join(root, "a"), Recursive: true}, Dir{Path: filepath.Join(root, "a", "b")},
		Dir{Path: filepath.Join(root, "a", "b"), Recursive: true})
	if n := fp.calls.Load(); n != 1 {
		t.Errorf("overlapping dirs probed %d times, want 1", n)
	}
	mustRemove(t, nested)
	write(t, filepath.Join(root, "top.mkv"), "4444") // keeps the root non-empty
	scanDirs(t, s, Dir{Path: root})                  // not marked recursive
	if _, ok := paths(t, st)[nested]; ok {
		t.Error("a scan of the root was not recursive")
	}
}

// A partial scan waits for a full one instead of failing, and a full scan
// queued behind a partial one still runs.
func TestPartialAndFullScansTakeTurns(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mkv"), "1")
	s, st, fp := newScanner(t, root)
	fp.block = make(chan struct{})
	fp.entered = make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := s.ScanDirs(context.Background(), []Dir{{Path: root}}); err != nil {
			t.Errorf("ScanDirs: %v", err)
		}
	}()
	<-fp.entered // the partial scan is probing
	if !s.Trigger() {
		t.Fatal("Trigger during a partial scan = false, want it queued")
	}
	if err := s.Scan(context.Background()); err != ErrRunning {
		t.Errorf("Scan with a full scan queued = %v, want ErrRunning", err)
	}
	close(fp.block)
	wg.Wait()
	s.Wait()
	if n := len(paths(t, st)); n != 1 {
		t.Errorf("items = %d, want 1", n)
	}
	if st := s.Status(context.Background()); st.LastFinished == nil {
		t.Error("the queued full scan never ran")
	}
}

// Rows of a root that is no longer configured are kept while the configured
// root that contains their files fails to scan: it may be the one about to
// take them over (the files only look gone).
func TestOrphansUnderAFailedRootAreKept(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "m")
	inner := filepath.Join(outer, "movies")
	file := filepath.Join(inner, "Film (2001).mkv")
	write(t, file, "1")
	s, st, fp := newScanner(t, inner)
	mustScan(t, s)

	// The configuration now has the outer directory, which is not mounted:
	// it is missing, and so is the file as far as Lstat can tell.
	moved := filepath.Join(base, "elsewhere")
	mustRename(t, outer, moved)
	s = NewScanner(context.Background(), st, fp, []string{outer}, nil)
	if err := s.Scan(context.Background()); err == nil {
		t.Fatal("scanning a missing root succeeded")
	}
	if _, ok := paths(t, st)[file]; !ok {
		t.Error("an orphan row under a root that failed to scan was pruned")
	}

	// Control: once the root is there and clean, a file that is really gone goes.
	mustRename(t, moved, outer)
	mustRemove(t, file)
	write(t, filepath.Join(outer, "keep.mkv"), "22")
	mustScan(t, s)
	if _, ok := paths(t, st)[file]; ok {
		t.Error("control: the orphan row of a vanished file was not pruned")
	}
}
