package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func setMtime(t *testing.T, p string, tm time.Time) {
	t.Helper()
	if err := os.Chtimes(p, tm, tm); err != nil {
		t.Fatal(err)
	}
}

// A file whose mtime is well in the future (FAT/exFAT local time, a skewed NAS
// clock) is indexed, and its directory is looked at once more (a copy may
// still be growing) but not again and again; one a few seconds ahead (written
// while the scan runs) still waits.
func TestFutureMtimeIsIndexed(t *testing.T) {
	root := t.TempDir()
	s, st, _ := newScanner(t, root)
	s.SetSettle(DefaultSettle)
	far := filepath.Join(root, "Far", "Skewed (2001).mkv")
	near := filepath.Join(root, "Near", "Writing (2002).mkv")
	write(t, far, "abc")
	write(t, near, "defg")
	setMtime(t, far, time.Now().Add(3*time.Hour))
	setMtime(t, near, time.Now().Add(2*time.Second))

	res := scanDirs(t, s, Dir{Path: root, Recursive: true})
	got := paths(t, st)
	if _, ok := got[far]; !ok {
		t.Error("a file with an mtime 3h in the future was not indexed")
	}
	if _, ok := got[near]; ok {
		t.Error("control: a file written just now (mtime 2s ahead) was indexed before settling")
	}
	if want := []Dir{{Path: filepath.Dir(far)}, {Path: filepath.Dir(near)}}; !slices.Equal(res.Deferred, want) {
		t.Errorf("Deferred = %v, want %v (the skewed file's directory once, after indexing it)", res.Deferred, want)
	}
	res = scanDirs(t, s, Dir{Path: filepath.Dir(far)})
	if len(res.Deferred) != 0 {
		t.Errorf("unchanged since: Deferred = %v, want none (no endless rescans)", res.Deferred)
	}
	setMtime(t, far, time.Now().Add(3*time.Hour+time.Minute)) // it grew (or was touched): look again
	if res = scanDirs(t, s, Dir{Path: filepath.Dir(far)}); len(res.Deferred) != 1 {
		t.Errorf("changed again: Deferred = %v, want its directory", res.Deferred)
	}

	s2, st2, _ := newScanner(t, root) // and a full scan
	s2.SetSettle(DefaultSettle)
	mustScan(t, s2)
	if _, ok := paths(t, st2)[far]; !ok {
		t.Error("full scan: a file with an mtime 3h in the future was not indexed")
	}
}

// A rename that changes only the case keeps the item on a case-insensitive
// file system, where the old spelling still opens the file.
func TestCaseOnlyRenameKeepsTheItem(t *testing.T) {
	root := t.TempDir()
	probe := filepath.Join(root, "probe")
	write(t, probe, "x")
	if _, err := os.Lstat(filepath.Join(root, "PROBE")); err != nil {
		t.Skip("case-sensitive file system")
	}
	mustRemove(t, probe)
	from := filepath.Join(root, "the matrix (1999).mkv")
	to := filepath.Join(root, "The Matrix (1999).mkv")
	write(t, from, "1")
	write(t, filepath.Join(root, "Other (2000).mkv"), "22")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, from)
	mustRename(t, from, to)

	scanDirs(t, s, Dir{Path: root})
	if got := idOf(t, st, to); got != id {
		t.Fatalf("after a case-only rename: id %d, want %d", got, id)
	}
	if n := len(paths(t, st)); n != 2 {
		t.Errorf("items = %d, want 2", n)
	}
}

// A move that did not keep the mtime and whose original was deleted before
// the copy settled: the gone row waits for the copy instead of being removed,
// then the two are paired by fingerprint.
func TestCopyThenDeleteWithinTheSettleTimeKeepsTheItem(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "A", "Movie (2001).mkv")
	to := filepath.Join(root, "B", "Movie (2001).mkv")
	unrelated := filepath.Join(root, "C", "Gone (1999).mkv")
	data := bytes.Repeat([]byte("frame"), 1000)
	writeBytes(t, from, data)
	write(t, unrelated, "zz")
	write(t, filepath.Join(root, "Keep (2000).mkv"), "k")
	setMtime(t, from, time.Now().Add(-time.Hour))
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, from)

	now := time.Now()
	s.SetSettle(DefaultSettle)
	s.now = func() time.Time { return now }
	writeBytes(t, to, data) // cp without -p: a new mtime
	mustRemove(t, from)
	mustRemove(t, unrelated)
	scanDirs(t, s, Dir{Path: root, Recursive: true})
	got := paths(t, st)
	if _, ok := got[from]; !ok {
		t.Fatal("the original's row was removed while a copy of its size was still settling")
	}
	if _, ok := got[unrelated]; ok {
		t.Error("control: a gone file of another size was kept")
	}

	now = now.Add(time.Minute)
	scanDirs(t, s, Dir{Path: root, Recursive: true})
	if got := idOf(t, st, to); got != id {
		t.Errorf("once settled the copy has id %d, want %d", got, id)
	}
	if idOf(t, st, from) != 0 {
		t.Error("the original's path is still indexed")
	}
}

// A volume mounted inside a root (not a root itself) that is unmounted keeps
// its rows; an ordinary directory emptied in the same scan loses them.
func TestUnmountedVolumeInsideARootKeepsItsItems(t *testing.T) {
	root := t.TempDir()
	vol := filepath.Join(root, "Anime")
	ep := filepath.Join(vol, "Show", "Ep (2001).mkv")
	plain := filepath.Join(root, "Plain", "Gone (2002).mkv")
	write(t, ep, "aaaa")
	write(t, plain, "bb")
	write(t, filepath.Join(root, "Keep (2000).mkv"), "c")
	s, st, _ := newScanner(t, root)
	mounted := true
	s.devOf = func(p string) (uint64, bool) {
		dev, ok := deviceOf(p)
		if mounted && filepath.Base(p) == "Anime" || mounted && strings.Contains(p, "/Anime/") {
			return dev + 1000, ok // another device while mounted
		}
		return dev, ok
	}
	mustScan(t, s) // learns the boundary

	mounted = false
	mustRemove(t, filepath.Join(vol, "Show")) // the mountpoint stays, empty
	mustRemove(t, plain)
	scanDirs(t, s, Dir{Path: root, Recursive: true})
	got := paths(t, st)
	if _, ok := got[ep]; !ok {
		t.Error("the rows of an unmounted volume inside the root were removed")
	}
	if _, ok := got[plain]; ok {
		t.Error("control: the row of a file deleted from an ordinary directory was kept")
	}
	mustScan(t, s) // a full scan keeps them too
	if _, ok := paths(t, st)[ep]; !ok {
		t.Error("a full scan removed the rows of the unmounted volume")
	}
}

// Mount boundaries survive a restart: a volume that is down when ply starts
// keeps its rows. A boundary whose directory was removed outright is forgotten,
// and its rows go.
func TestMountBoundariesAreRemembered(t *testing.T) {
	root := t.TempDir()
	vol := filepath.Join(root, "Anime")
	ep := filepath.Join(vol, "Show", "Ep (2001).mkv")
	write(t, ep, "aaaa")
	write(t, filepath.Join(root, "Keep (2000).mkv"), "c")
	s, st, fp := newScanner(t, root)
	mounted := true
	fakeDev := func(p string) (uint64, bool) {
		dev, ok := deviceOf(p)
		if mounted && (filepath.Base(p) == "Anime" || strings.Contains(p, "/Anime/")) {
			return dev + 1000, ok
		}
		return dev, ok
	}
	s.devOf = fakeDev
	mustScan(t, s) // learns and stores the boundary

	// ply restarts while the volume is down.
	mounted = false
	mustRemove(t, filepath.Join(vol, "Show"))
	s2 := NewScanner(context.Background(), st, fp, []string{root}, nil)
	s2.devOf = fakeDev
	mustScan(t, s2)
	if idOf(t, st, ep) == 0 {
		t.Fatal("after a restart, the rows of a volume that is down were removed")
	}

	// The mount point itself is deleted: no volume will come back there.
	mustRemove(t, vol)
	mustScan(t, s2)
	if idOf(t, st, ep) != 0 {
		t.Error("the rows under a removed mount point were kept")
	}
	if v, _, _ := st.GetSetting(context.Background(), mountsKey); strings.Contains(v, "Anime") {
		t.Errorf("the removed mount point is still remembered: %s", v)
	}
}

// A boundary met by a partial scan (the watcher's) is remembered too.
func TestMountBoundaryFromAPartialScanIsRemembered(t *testing.T) {
	root := t.TempDir()
	vol := filepath.Join(root, "Disk2")
	ep := filepath.Join(vol, "Film (2001).mkv")
	write(t, ep, "aaaa")
	write(t, filepath.Join(root, "Keep (2000).mkv"), "c")
	s, st, fp := newScanner(t, root)
	mounted := true
	fakeDev := func(p string) (uint64, bool) {
		dev, ok := deviceOf(p)
		if mounted && (filepath.Base(p) == "Disk2" || strings.Contains(p, "/Disk2/")) {
			return dev + 1000, ok
		}
		return dev, ok
	}
	s.devOf = fakeDev
	scanDirs(t, s, Dir{Path: root, Recursive: true})
	if v, _, _ := st.GetSetting(context.Background(), mountsKey); !strings.Contains(v, "Disk2") {
		t.Fatalf("a partial scan did not remember the boundary: %q", v)
	}
	mounted = false
	mustRemove(t, ep)
	s2 := NewScanner(context.Background(), st, fp, []string{root}, nil)
	s2.devOf = fakeDev
	mustScan(t, s2)
	if idOf(t, st, ep) == 0 {
		t.Error("after a restart, the rows of a volume that is down were removed")
	}
}

// A rename that changes only the case of a folder keeps its items too.
func TestCaseOnlyFolderRenameKeepsTheItems(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "probe"), "x")
	if _, err := os.Lstat(filepath.Join(root, "PROBE")); err != nil {
		t.Skip("case-sensitive file system")
	}
	mustRemove(t, filepath.Join(root, "probe"))
	from := filepath.Join(root, "breaking bad", "S01E01.mkv")
	write(t, from, "1")
	write(t, filepath.Join(root, "Other (2000).mkv"), "22")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, from)
	if err := os.Rename(filepath.Join(root, "breaking bad"), filepath.Join(root, "Breaking Bad")); err != nil {
		t.Fatal(err)
	}
	mustScan(t, s)
	if got := idOf(t, st, filepath.Join(root, "Breaking Bad", "S01E01.mkv")); got != id {
		t.Errorf("after a case-only folder rename: id %d, want %d", got, id)
	}
}

// A row held because a file of its size is settling is looked at again when
// the copy is: its directory (or what is left of it) is in Deferred.
func TestHeldRowsAreLookedAtAgain(t *testing.T) {
	root := t.TempDir()
	goneFile := filepath.Join(root, "A", "Sub", "Gone (2001).mkv")
	other := filepath.Join(root, "B", "Other (2002).mkv")
	write(t, goneFile, "0123456789")
	write(t, filepath.Join(root, "A", "keep.txt"), "x")
	write(t, filepath.Join(root, "Keep (2000).mkv"), "k")
	setMtime(t, goneFile, time.Now().Add(-time.Hour))
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	now := time.Now()
	s.SetSettle(DefaultSettle)
	s.now = func() time.Time { return now }
	mustRemove(t, filepath.Join(root, "A", "Sub")) // the file and its folder
	write(t, other, "abcdefghij")                  // same size, other content, still settling
	res := scanDirs(t, s, Dir{Path: root, Recursive: true})
	if idOf(t, st, goneFile) == 0 {
		t.Fatal("setup: the row was not held")
	}
	if !slices.Contains(res.Deferred, Dir{Path: filepath.Join(root, "A"), Recursive: true}) {
		t.Fatalf("Deferred = %v, want A (what is left of the held row's folder), recursive", res.Deferred)
	}

	now = now.Add(time.Minute) // the copy has settled; only the deferred dirs are scanned
	scanDirs(t, s, res.Deferred...)
	if idOf(t, st, goneFile) != 0 {
		t.Error("the held row was still there after its folder was looked at again")
	}
	if idOf(t, st, other) == 0 {
		t.Error("control: the settled copy was not indexed")
	}
}

// A copy with the same size and mtime in a scanned directory does not take
// over the row of an original that still exists outside the scan.
func TestCopyDoesNotTakeOverAnOriginalThatExists(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A", "Movie (2001).mkv")
	b := filepath.Join(root, "B", "Movie (2001).mkv")
	write(t, a, "0123456789")
	write(t, filepath.Join(root, "Z", "Other (1999).mkv"), "zz")
	tm := time.Now().Add(-time.Hour)
	setMtime(t, a, tm)
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	id := idOf(t, st, a)
	write(t, b, "0123456789")
	setMtime(t, b, tm)
	scanDirs(t, s, Dir{Path: filepath.Join(root, "B")})
	if got := idOf(t, st, a); got != id {
		t.Errorf("original row id = %d, want %d (taken over by the copy)", got, id)
	}
	if got := idOf(t, st, b); got == 0 || got == id {
		t.Errorf("copy row id = %d (original %d), want a new item", got, id)
	}
}

// Two gone rows that both match one new file: ambiguous, by size and mtime as
// by fingerprint, so neither is paired.
func TestTwoGoneRowsForOneNewFileAreNotPaired(t *testing.T) {
	for _, byFingerprint := range []bool{false, true} {
		t.Run(map[bool]string{false: "size and mtime", true: "fingerprint"}[byFingerprint], func(t *testing.T) {
			root := t.TempDir()
			a1 := filepath.Join(root, "A", "One (2001).mkv")
			a2 := filepath.Join(root, "A", "Two (2002).mkv")
			n := filepath.Join(root, "B", "New (2003).mkv")
			write(t, a1, "0123456789")
			write(t, a2, "0123456789")
			write(t, filepath.Join(root, "Keep (2000).mkv"), "k")
			t1 := time.Now().Add(-time.Hour)
			t2 := t1 // the same mtime: ambiguous by size and mtime
			if byFingerprint {
				t2 = t2.Add(-time.Hour)
			}
			setMtime(t, a1, t1)
			setMtime(t, a2, t2)
			s, st, _ := newScanner(t, root)
			mustScan(t, s)
			id1, id2 := idOf(t, st, a1), idOf(t, st, a2)
			mustRemove(t, a1)
			mustRemove(t, a2)
			write(t, n, "0123456789")
			if byFingerprint {
				setMtime(t, n, time.Now().Add(-3*time.Hour))
			} else {
				setMtime(t, n, t1)
			}
			scanDirs(t, s, Dir{Path: root, Recursive: true})
			if got := idOf(t, st, n); got == id1 || got == id2 || got == 0 {
				t.Errorf("new file has id %d (rows were %d, %d): want a new item", got, id1, id2)
			}
		})
	}
}

// A partial scan that cannot read a subdirectory removes nothing under it.
func TestUnreadableSubdirectoryInAPartialScanKeepsItsRows(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	f := filepath.Join(root, "Show", "Season 1", "Ep (2001).mkv")
	write(t, f, "abc")
	write(t, filepath.Join(root, "Other (2000).mkv"), "o")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	dir := filepath.Dir(f)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := s.ScanDirs(context.Background(), []Dir{{Path: root, Recursive: true}}); err == nil {
		t.Error("ScanDirs reported no error for an unreadable directory")
	}
	if _, ok := paths(t, st)[f]; !ok {
		t.Error("the row under an unreadable directory was removed")
	}
}

// A partial scan that cannot read the library says so with ErrNotScanned, so
// the watcher can try again; one that ran says nothing of the kind.
func TestScanDirsThatDidNotRun(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mkv"), "1")
	s, st, _ := newScanner(t, root)
	if _, err := s.ScanDirs(context.Background(), []Dir{{Path: root}}); errors.Is(err, ErrNotScanned) {
		t.Fatalf("control: a scan that ran returned %v", err)
	}
	st.Close()
	if _, err := s.ScanDirs(context.Background(), []Dir{{Path: root}}); !errors.Is(err, ErrNotScanned) {
		t.Errorf("with the store closed: %v, want ErrNotScanned", err)
	}
}

// A file indexed within the last minute leaves its directory in Deferred, so a
// copy that stalled past the settle time is looked at again; an old one does
// not.
func TestRecentlyWrittenFilesAreLookedAtAgain(t *testing.T) {
	root := t.TempDir()
	recent := filepath.Join(root, "Recent", "r.mkv")
	old := filepath.Join(root, "Old", "o.mkv")
	write(t, recent, "1")
	write(t, old, "22")
	now := time.Now()
	setMtime(t, recent, now.Add(-10*time.Second))
	setMtime(t, old, now.Add(-time.Hour))
	s, st, _ := newScanner(t, root)
	s.SetSettle(DefaultSettle)
	s.now = func() time.Time { return now }
	res := scanDirs(t, s, Dir{Path: root, Recursive: true})
	if n := len(paths(t, st)); n != 2 {
		t.Fatalf("indexed %d, want both (both are settled)", n)
	}
	if want := []Dir{{Path: filepath.Dir(recent)}}; !slices.Equal(res.Deferred, want) {
		t.Errorf("Deferred = %v, want %v", res.Deferred, want)
	}
	// Still recent and unchanged: still looked at again.
	if res = scanDirs(t, s, Dir{Path: root, Recursive: true}); len(res.Deferred) != 1 {
		t.Errorf("second pass: Deferred = %v, want the recent directory again", res.Deferred)
	}
	now = now.Add(2 * time.Minute)
	if res = scanDirs(t, s, Dir{Path: root, Recursive: true}); len(res.Deferred) != 0 {
		t.Errorf("a minute later: Deferred = %v, want none", res.Deferred)
	}
}
