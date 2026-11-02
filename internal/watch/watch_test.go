package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"ply/internal/library"
)

// newUnit returns a watcher on a fake backend and a fake scanner, with the
// timings short and the limits off. Run is not started.
func newUnit(t *testing.T, roots ...string) (*Watcher, *fakeBackend, *fakeScanner, *logBuf) {
	t.Helper()
	log, lb := testLogger(t)
	fb, sc := newFakeBackend(), &fakeScanner{}
	w := New(sc, roots, log)
	w.mkBackend = func() (backend, error) { return fb, nil }
	w.maxDirs = -1
	w.quiet, w.maxWait, w.retry, w.recheck = 100*time.Millisecond, 2*time.Second, 100*time.Millisecond, time.Hour
	return w, fb, sc, lb
}

// setup adds the watches without starting the loops, so a test can feed
// events to handle and look at the pending set.
func setup(t *testing.T, w *Watcher, fb *fakeBackend) {
	t.Helper()
	w.use(fb)
	for i := range w.roots {
		w.setupRoot(context.Background(), i)
	}
}

// drain empties the pending set and returns it, sorted.
func drain(w *Watcher) []library.Dir {
	batch, _ := w.take(time.Now().Add(24 * time.Hour))
	return batch
}

func TestEventMapping(t *testing.T) {
	root := t.TempDir()
	d, e := filepath.Join(root, "D"), filepath.Join(root, "E")
	mkdirs(t, root, "D", "E")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)

	plain := func(dir string) []library.Dir { return []library.Dir{{Path: dir}} }
	side := func(dir string) []library.Dir { return []library.Dir{{Path: dir, Sidecars: true}} }
	cases := []struct {
		name string
		ev   event
		want []library.Dir // nil: nothing is queued
	}{
		{"video created", event{d, "a.mkv", opCreate}, plain(d)},
		{"video written", event{d, "a.mkv", opWrite}, plain(d)},
		{"video removed", event{d, "a.mkv", opRemove}, plain(d)},
		{"video renamed", event{d, "a.mkv", opRename}, plain(d)},
		{"video created and written", event{d, "a.mkv", opCreate | opWrite}, plain(d)},
		{"mp4", event{d, "a.mp4", opCreate}, plain(d)},
		{"m2ts", event{d, "a.m2ts", opCreate}, plain(d)},
		{"upper case extension", event{d, "A.MKV", opCreate}, plain(d)},
		{"srt", event{d, "a.en.srt", opCreate}, plain(d)},
		{"vtt", event{d, "a.vtt", opWrite}, plain(d)},
		{"ass", event{d, "a.ass", opRemove}, plain(d)},
		{"nfo", event{d, "movie.nfo", opWrite}, side(d)},
		{"jpg", event{d, "poster.jpg", opCreate}, side(d)},
		{"jpeg", event{d, "poster.jpeg", opCreate}, side(d)},
		{"png upper case", event{d, "poster.PNG", opCreate}, side(d)},
		{"webp", event{d, "fanart.webp", opCreate}, side(d)},
		{"tbn", event{d, "folder.tbn", opCreate}, side(d)},
		{"nfo removed", event{d, "movie.nfo", opRemove}, side(d)},
		{"chmod with write", event{d, "a.mkv", opChmod | opWrite}, plain(d)},

		{"rsync temp file", event{d, ".a.mkv.Xk2dQ", opCreate}, nil},
		{"hidden video", event{d, ".a.mkv", opCreate}, nil},
		{"appledouble", event{d, "._a.mkv", opCreate}, nil},
		{"hidden nfo", event{d, ".movie.nfo", opWrite}, nil},
		{"chmod only", event{d, "a.mkv", opChmod}, nil},
		{"unrelated file", event{d, "notes.txt", opCreate}, nil},
		{"partial download", event{d, "a.mkv.part", opCreate}, nil},
		{"no extension", event{d, "mkv", opCreate}, nil},
		{"unwatched directory", event{filepath.Join(root, "elsewhere"), "a.mkv", opCreate}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			drain(w)
			w.handle(context.Background(), c.ev)
			got := drain(w)
			if c.want != nil {
				if !slices.Equal(got, c.want) {
					t.Fatalf("queued %v, want %v", got, c.want)
				}
				return
			}
			// Positive control: the same watcher queues a video in E right
			// after, and nothing but E is pending, so the event queued nothing.
			w.handle(context.Background(), event{e, "control.mkv", opCreate})
			if got, want := append(got, drain(w)...), plain(e); !slices.Equal(got, want) {
				t.Fatalf("queued %v, want only %v", got, want)
			}
		})
	}
}

func TestEventsMergeIntoOneDirPerPath(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "D")
	mkdirs(t, root, "D")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)

	w.handle(context.Background(), event{d, "a.mkv", opWrite})
	w.handle(context.Background(), event{d, "a.nfo", opWrite})
	w.handle(context.Background(), event{d, "b.mkv", opWrite})
	if got, want := drain(w), []library.Dir{{Path: d, Sidecars: true}}; !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
}

func TestNewDirIsWatchedAndQueuedRecursively(t *testing.T) {
	root := t.TempDir()
	shows := filepath.Join(root, "Shows")
	mkdirs(t, root, "Shows")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)
	before := fb.watched()
	if want := []string{root, shows}; !slices.Equal(before, want) {
		t.Fatalf("watched %v, want %v", before, want)
	}

	// A new tree with a hidden directory and a symlink to a directory that
	// would loop back into it.
	mkdirs(t, shows, "New/S1/Extras", "New/.hidden/x")
	if err := os.Symlink(shows, filepath.Join(shows, "New", "link")); err != nil {
		t.Fatal(err)
	}
	// A file event is not a directory: no watch is added for it.
	touch(t, filepath.Join(shows, "plain.mkv"), 1)
	w.handle(context.Background(), event{shows, "plain.mkv", opCreate})
	if got := fb.watched(); !slices.Equal(got, before) {
		t.Fatalf("a file got a watch: %v", got)
	}
	drain(w)

	w.handle(context.Background(), event{shows, "New", opCreate})
	n := filepath.Join(shows, "New")
	want := []string{root, shows, n, filepath.Join(n, "S1"), filepath.Join(n, "S1", "Extras")}
	if got := fb.watched(); !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v (no hidden directory, no symlink)", got, want)
	}
	if got, want := drain(w), []library.Dir{{Path: n, Recursive: true}}; !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
	if got := w.Status()[0].Dirs; got != len(want) {
		t.Fatalf("Status().Dirs = %d, want %d", got, len(want))
	}

	// The same event again (mkdir -p reports each level) adds nothing.
	w.handle(context.Background(), event{shows, "New", opCreate})
	if c := fb.addCalls(n); c != 1 {
		t.Fatalf("directory added %d times", c)
	}
	// A hidden new directory is ignored.
	w.handle(context.Background(), event{shows, ".hidden", opCreate})
	if got := drain(w); len(got) != 0 {
		t.Fatalf("hidden directory queued %v", got)
	}
}

func TestGoneDirDropsItsTreeAndQueuesTheParent(t *testing.T) {
	for _, o := range []op{opRemove, opRename} {
		t.Run(map[op]string{opRemove: "removed", opRename: "renamed away"}[o], func(t *testing.T) {
			root := t.TempDir()
			mkdirs(t, root, "A/sub/deep", "Keep")
			a := filepath.Join(root, "A")
			w, fb, _, _ := newUnit(t, root)
			setup(t, w, fb)
			if got := w.Status()[0].Dirs; got != 5 {
				t.Fatalf("Dirs = %d before, want 5", got)
			}
			if err := os.RemoveAll(a); err != nil { // renamed away or removed: A is not there
				t.Fatal(err)
			}

			w.handle(context.Background(), event{root, "A", o})
			if got, want := fb.watched(), []string{root, filepath.Join(root, "Keep")}; !slices.Equal(got, want) {
				t.Fatalf("watched %v, want %v", got, want)
			}
			if got, want := drain(w), []library.Dir{{Path: root, Recursive: true}}; !slices.Equal(got, want) {
				t.Fatalf("queued %v, want %v", got, want)
			}
			if got := w.Status()[0].Dirs; got != 2 {
				t.Fatalf("Dirs = %d after, want 2", got)
			}
			// The directory's own event arrives too (inotify): nothing more.
			w.handle(context.Background(), event{dir: a, op: opDirGone})
			if got := drain(w); len(got) != 0 {
				t.Fatalf("second notice queued %v", got)
			}
		})
	}
}

func TestGoneDirThatCameBackIsWatchedAgain(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A/sub")
	a := filepath.Join(root, "A")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)

	// Replaced by a new directory (or the notice is late): A exists.
	w.handle(context.Background(), event{dir: a, op: opDirGone})
	if got, want := fb.watched(), []string{root, a, filepath.Join(a, "sub")}; !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
	got := drain(w)
	want := []library.Dir{{Path: root, Recursive: true}, {Path: a, Recursive: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
}

func TestRelistForBackendsWithoutNames(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Old/sub", "Same")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)

	// Something changed in root: Old is gone, New (with a child) is there.
	if err := os.RemoveAll(filepath.Join(root, "Old")); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, root, "New/child", ".hidden")
	w.handle(context.Background(), event{dir: root, op: opWrite})

	want := []string{root, filepath.Join(root, "New"), filepath.Join(root, "New", "child"), filepath.Join(root, "Same")}
	if got := fb.watched(); !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
	wantQ := []library.Dir{
		{Path: root, Recursive: true, Sidecars: true}, // its items; and Old's parent
		{Path: filepath.Join(root, "New"), Recursive: true},
	}
	if got := drain(w); !slices.Equal(got, wantQ) {
		t.Fatalf("queued %v, want %v", got, wantQ)
	}
}

// With a backend that cannot name what changed, the snapshot can: only the
// files that differ are queued, as named events would have been, so a new
// movie in a root does not report every item of the root as having new
// sidecars.
func TestRelistDiffsTheSnapshot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "Film (2001)")
	keep, gone, grow := filepath.Join(root, "Keep.mkv"), filepath.Join(root, "Gone.mkv"), filepath.Join(sub, "Film.mkv")
	touch(t, keep, 1)
	touch(t, gone, 1)
	touch(t, grow, 1)
	w, fb, _, _ := newUnit(t, root)
	fb.unnamed = true
	setup(t, w, fb)

	w.handle(context.Background(), event{dir: root, op: opWrite}) // nothing changed
	if got := drain(w); len(got) != 0 {
		t.Fatalf("an event with no change on disk queued %v", got)
	}

	touch(t, filepath.Join(root, "New.mkv"), 1)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	w.handle(context.Background(), event{dir: root, op: opWrite})
	if got, want := drain(w), []library.Dir{{Path: root}}; !slices.Equal(got, want) {
		t.Fatalf("a video added and one removed queued %v, want %v (no sidecars)", got, want)
	}

	touch(t, filepath.Join(sub, "poster.jpg"), 1)
	w.handle(context.Background(), event{dir: sub, op: opWrite})
	if got, want := drain(w), []library.Dir{{Path: sub, Sidecars: true}}; !slices.Equal(got, want) {
		t.Fatalf("new artwork queued %v, want %v", got, want)
	}

	touch(t, grow, 2) // the same name, changed
	w.handle(context.Background(), event{dir: sub, op: opWrite})
	if got, want := drain(w), []library.Dir{{Path: sub}}; !slices.Equal(got, want) {
		t.Fatalf("a rewritten video queued %v, want %v", got, want)
	}

	notes := filepath.Join(root, "notes.txt")
	touch(t, notes, 1)
	w.handle(context.Background(), event{dir: root, op: opWrite})
	if got := drain(w); len(got) != 0 {
		t.Fatalf("an unrelated file queued %v", got)
	}
}

func TestStatusOfAMissingRoot(t *testing.T) {
	good := t.TempDir()
	mkdirs(t, good, "a")
	missing := filepath.Join(t.TempDir(), "gone")
	w, fb, sc, _ := newUnit(t, good, missing)
	if got := w.Status(); got[0].Watching || got[1].Watching {
		t.Fatalf("watching before Run: %+v", got)
	}
	start(t, w)
	waitRoot(t, w, 0, "good root watched", func(s RootStatus) bool { return s.Watching })
	waitRoot(t, w, 1, "missing root looked at", func(s RootStatus) bool { return s.Error != "" })

	want := []RootStatus{
		{Root: good, Watching: true, Dirs: 2},
		{Root: missing, Watching: false, Dirs: 0, Error: "root not found"},
	}
	if got := w.Status(); !slices.Equal(got, want) {
		t.Fatalf("Status() = %+v, want %+v", got, want)
	}
	for _, d := range fb.watched() {
		if strings.HasPrefix(d, missing) {
			t.Fatalf("watch added under the missing root: %s", d)
		}
	}
	// Positive control: an event in the good root is scanned; the missing
	// root never is.
	fb.ev <- event{filepath.Join(good, "a"), "x.mkv", opCreate}
	waitFor(t, 5*time.Second, "a scan", func() bool { return sc.count() > 0 })
	if sc.sawPath(missing) {
		t.Fatal("the missing root was scanned")
	}
}

func TestDebounceBatchesIntoOneScan(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A", "B")
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	w, fb, sc, _ := newUnit(t, root)
	w.quiet = 300 * time.Millisecond
	start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	fb.ev <- event{a, "a.mkv", opCreate}
	time.Sleep(40 * time.Millisecond)
	sentLast := time.Now()
	fb.ev <- event{b, "b.mkv", opCreate}

	time.Sleep(100 * time.Millisecond)
	if n := sc.count(); n != 0 {
		t.Fatalf("scanned %d times before the quiet period passed", n)
	}
	waitFor(t, 5*time.Second, "the scan", func() bool { return sc.count() > 0 })
	time.Sleep(2 * w.quiet) // nothing else comes
	calls := sc.snapshot()
	if len(calls) != 1 {
		t.Fatalf("%d scans, want 1: %v", len(calls), calls)
	}
	if got, want := calls[0].dirs, []library.Dir{{Path: a}, {Path: b}}; !slices.Equal(got, want) {
		t.Fatalf("scanned %v, want %v", got, want)
	}
	if wait := calls[0].at.Sub(sentLast); wait < w.quiet-10*time.Millisecond {
		t.Fatalf("scanned %v after the last event, quiet is %v", wait, w.quiet)
	}
}

func TestQuietPeriodRestartsWithEachEvent(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A", "B", "C", "D")
	w, fb, sc, _ := newUnit(t, root)
	w.quiet = 300 * time.Millisecond
	start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	// Four events 120ms apart: each gap is shorter than the quiet period,
	// the whole run is longer.
	var last time.Time
	for i, d := range []string{"A", "B", "C", "D"} {
		if i > 0 {
			time.Sleep(120 * time.Millisecond)
		}
		last = time.Now()
		fb.ev <- event{filepath.Join(root, d), "x.mkv", opCreate}
	}
	waitFor(t, 5*time.Second, "the scan", func() bool { return sc.count() > 0 })
	time.Sleep(2 * w.quiet)
	calls := sc.snapshot()
	if len(calls) != 1 || len(calls[0].dirs) != 4 {
		t.Fatalf("scans = %v, want one with all four directories", calls)
	}
	if wait := calls[0].at.Sub(last); wait < w.quiet-10*time.Millisecond {
		t.Fatalf("scanned %v after the last event, quiet is %v", wait, w.quiet)
	}
}

func TestMaxWaitCapsSteadyActivity(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A")
	a := filepath.Join(root, "A")
	w, fb, sc, _ := newUnit(t, root)
	w.quiet, w.maxWait = 200*time.Millisecond, 600*time.Millisecond
	start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	// An event every 50ms for 2s: the quiet period never passes.
	started := time.Now()
	stopSending := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for end := started.Add(2 * time.Second); time.Now().Before(end); {
			select {
			case fb.ev <- event{a, "growing.mkv", opWrite}:
			case <-stopSending:
				return
			}
			<-tick.C
		}
	}()
	defer func() { close(stopSending); <-sent }()

	waitFor(t, 5*time.Second, "a scan during the activity", func() bool { return sc.count() > 0 })
	first := sc.snapshot()[0].at.Sub(started)
	select {
	case <-sent:
		t.Fatalf("events had stopped before the first scan (%v): the test shows nothing", first)
	default:
	}
	if first < w.maxWait-50*time.Millisecond || first > 1400*time.Millisecond {
		t.Fatalf("first scan %v after the first event, want about maxWait %v", first, w.maxWait)
	}
}

func TestDeferredDirsAreQueuedAgainAfterRetry(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A")
	a := filepath.Join(root, "A")
	w, fb, sc, _ := newUnit(t, root)
	w.retry = 200 * time.Millisecond
	sc.fn = func(_ context.Context, n int, _ []library.Dir) (library.Result, error) {
		if n == 1 {
			return library.Result{Deferred: []library.Dir{{Path: a}}}, nil
		}
		return library.Result{}, nil
	}
	start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	fb.ev <- event{a, "x.mkv", opCreate}
	waitFor(t, 5*time.Second, "the second scan", func() bool { return sc.count() >= 2 })
	time.Sleep(3 * w.retry) // and no third
	calls := sc.snapshot()
	if len(calls) != 2 {
		t.Fatalf("%d scans, want 2: %v", len(calls), calls)
	}
	if got, want := calls[1].dirs, []library.Dir{{Path: a}}; !slices.Equal(got, want) {
		t.Fatalf("second scan %v, want %v", got, want)
	}
	if gap := calls[1].at.Sub(calls[0].at); gap < w.retry-10*time.Millisecond {
		t.Fatalf("second scan %v after the first, retry is %v", gap, w.retry)
	}
}

func TestOverflowQueuesEveryRoot(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	mkdirs(t, r1, "a")
	mkdirs(t, r2, "b")
	w, fb, sc, lb := newUnit(t, r1, r2)
	w.quiet = 80 * time.Millisecond
	start(t, w)
	waitRoot(t, w, 1, "watched", func(s RootStatus) bool { return s.Watching })

	// Positive control for "an error is not an overflow": another error is
	// logged and queues nothing.
	fb.errc <- errors.New("something else")
	waitFor(t, 5*time.Second, "the error logged", func() bool { return lb.count("file watcher error") > 0 })
	time.Sleep(3 * w.quiet)
	if n := sc.count(); n != 0 {
		t.Fatalf("another error caused %d scans", n)
	}

	// A directory made while events were being lost has no watch yet.
	mkdirs(t, r1, "a/missed")
	fb.errc <- errOverflow
	waitFor(t, 5*time.Second, "the scan", func() bool { return sc.count() > 0 })
	want := []library.Dir{{Path: r1, Recursive: true}, {Path: r2, Recursive: true}}
	if got := sc.snapshot()[0].dirs; !slices.Equal(got, want) {
		t.Fatalf("scanned %v, want %v", got, want)
	}
	if !slices.Contains(fb.watched(), filepath.Join(r1, "a", "missed")) {
		t.Fatalf("the directory missed in the overflow is not watched: %v", fb.watched())
	}
}

func TestScanErrorsAreNotFatal(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A")
	a := filepath.Join(root, "A")
	w, fb, sc, lb := newUnit(t, root)
	sc.fn = func(_ context.Context, n int, _ []library.Dir) (library.Result, error) {
		if n == 1 {
			return library.Result{}, errors.New("probe failed")
		}
		return library.Result{}, nil
	}
	start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	fb.ev <- event{a, "x.mkv", opCreate}
	waitFor(t, 5*time.Second, "the failing scan", func() bool { return sc.count() == 1 })
	waitFor(t, 5*time.Second, "its log line", func() bool { return lb.count("probe failed") == 1 })
	fb.ev <- event{a, "y.mkv", opCreate}
	waitFor(t, 5*time.Second, "the next scan", func() bool { return sc.count() == 2 })
}

// Queuing a directory twice keeps every flag either asked for; so does
// scheduling it twice for later.
func TestQueueMergesFlags(t *testing.T) {
	root := t.TempDir()
	w, _, _, _ := newUnit(t, root)
	w.queue(library.Dir{Path: root, Recursive: true})
	w.queue(library.Dir{Path: root, Sidecars: true})
	w.queue(library.Dir{Path: root})
	if got, want := drain(w), []library.Dir{{Path: root, Recursive: true, Sidecars: true}}; !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
	w.deferFor([]library.Dir{{Path: root, Sidecars: true}}, 0)
	w.deferFor([]library.Dir{{Path: root, Recursive: true}}, 0)
	if got, want := drain(w), []library.Dir{{Path: root, Recursive: true, Sidecars: true}}; !slices.Equal(got, want) {
		t.Fatalf("deferred %v, want %v", got, want)
	}
}

// A root inside another root that disappears gets the outer root scanned, so
// its files, if they moved, keep their items.
func TestNestedRootGoneQueuesTheOuterRoot(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	mkdirs(t, outer, "inner/a", "b")
	w, fb, _, _ := newUnit(t, outer, inner)
	setup(t, w, fb)
	drain(w)
	if err := os.RemoveAll(inner); err != nil {
		t.Fatal(err)
	}
	w.handle(context.Background(), event{dir: inner, op: opDirGone})
	if got := drain(w); !slices.Contains(got, library.Dir{Path: outer, Recursive: true}) {
		t.Fatalf("queued %v, want the outer root, recursive", got)
	}
	if st := w.Status()[1]; st.Watching || st.Error != rootNotFound {
		t.Fatalf("inner root status %+v, want not found", st)
	}
}

// A root whose watches failed on a limit is not walked again at every
// recheck: the wait doubles, and the first try that works resets it.
func TestFailedRootBacksOff(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a")
	w, fb, _, _ := newUnit(t, root)
	w.recheck = time.Minute
	now := time.Now()
	w.now = func() time.Time { return now }
	failing := true
	fb.addErr = func(dir string) error {
		if failing {
			return &os.PathError{Op: "add", Path: dir, Err: syscall.ENOSPC}
		}
		return nil
	}
	setup(t, w, fb)
	tries := func() int { return fb.addCalls(root) }
	if tries() != 1 || w.Status()[0].Watching {
		t.Fatalf("setup: %d tries, status %+v", tries(), w.Status()[0])
	}
	at := func(d time.Duration) int {
		now = now.Add(d)
		w.recheckRoots(context.Background())
		return tries()
	}
	if n := at(30 * time.Second); n != 1 {
		t.Fatalf("tried again after 30s (%d tries), want to wait a minute", n)
	}
	if n := at(31 * time.Second); n != 2 {
		t.Fatalf("after a minute: %d tries, want 2", n)
	}
	if n := at(61 * time.Second); n != 2 {
		t.Fatalf("a minute after the second failure: %d tries, want the wait doubled to 2m", n)
	}
	if n := at(60 * time.Second); n != 3 {
		t.Fatalf("two minutes after it: %d tries, want 3", n)
	}
	failing = false
	if n := at(4*time.Minute + time.Second); n != 4 || !w.Status()[0].Watching {
		t.Fatalf("limit gone: %d tries, status %+v; want watching", n, w.Status()[0])
	}
	if r := w.roots[0]; r.backoff != 0 {
		t.Fatalf("backoff %v after a success, want reset", r.backoff)
	}
}

// A partial scan that could not run (ErrNotScanned) is tried again with the
// same directories; one that ran with errors is not.
func TestScanThatDidNotRunIsRetried(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a", "b")
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	w, fb, sc, _ := newUnit(t, root)
	sc.fn = func(_ context.Context, n int, dirs []library.Dir) (library.Result, error) {
		switch {
		case n == 1:
			return library.Result{}, fmt.Errorf("%w: database is locked", library.ErrNotScanned)
		case slices.ContainsFunc(dirs, func(d library.Dir) bool { return d.Path == b }):
			return library.Result{}, errors.New("1 errors while walking")
		}
		return library.Result{}, nil
	}
	start(t, w)
	fb.ev <- event{a, "x.mkv", opCreate}
	waitFor(t, 5*time.Second, "the retry", func() bool { return sc.count() >= 2 })
	if c := sc.snapshot()[1]; !slices.Equal(dirsOf(c), []string{a}) {
		t.Fatalf("retry scanned %v, want %s", dirsOf(c), a)
	}

	fb.ev <- event{b, "y.mkv", opCreate}
	waitFor(t, 5*time.Second, "b's scan", func() bool { return sc.sawPath(b) })
	n := sc.count()
	time.Sleep(10 * w.retry)
	if sc.count() != n {
		t.Fatalf("a scan that ran (with walk errors) was retried: %v", sc.snapshot())
	}
}

func TestWatchLimits(t *testing.T) {
	for _, c := range []struct {
		name  string
		errno error
		text  string
	}{
		{"inotify watches", syscall.ENOSPC, "max_user_watches"},
		{"descriptors", syscall.EMFILE, "too many open files"},
		{"system descriptors", syscall.ENFILE, "too many open files"},
		{"anything else", errors.New("boom"), "boom"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r1, r2 := t.TempDir(), t.TempDir()
			mkdirs(t, r1, "a", "deep/x", "z")
			mkdirs(t, r2, "b")
			w, fb, sc, lb := newUnit(t, r1, r2)
			fb.addErr = func(dir string) error {
				if strings.HasSuffix(dir, "deep") {
					return &os.PathError{Op: "add", Path: dir, Err: c.errno}
				}
				return nil
			}
			w.recheck = 30 * time.Millisecond
			start(t, w)
			waitRoot(t, w, 1, "second root watched", func(s RootStatus) bool { return s.Watching })

			got := w.Status()
			if got[0].Watching || got[0].Dirs != 0 || !strings.Contains(got[0].Error, c.text) {
				t.Fatalf("first root: %+v, want not watching, 0 dirs, error with %q", got[0], c.text)
			}
			if !got[1].Watching || got[1].Dirs != 2 || got[1].Error != "" {
				t.Fatalf("second root: %+v, want watching 2 dirs", got[1])
			}
			// Its watches are gone, the other root's stay.
			for _, d := range fb.watched() {
				if strings.HasPrefix(d, r1) {
					t.Fatalf("watch left under the failed root: %s", d)
				}
			}
			if len(fb.watched()) != 2 {
				t.Fatalf("watched %v, want the second root's two", fb.watched())
			}
			// The root is tried again at every recheck (every 30ms here), and
			// the failure is logged once.
			waitFor(t, 5*time.Second, "another try", func() bool { return fb.addCalls(filepath.Join(r1, "deep")) >= 3 })
			if n := lb.count("cannot watch media root", r1); n != 1 {
				t.Fatalf("logged %d times, want once:\n%s", n, lb.String())
			}
			if sc.count() != 0 {
				t.Fatalf("a failed watch queued a scan: %v", sc.snapshot())
			}

			// The limit goes away: the next try works, and the root is scanned
			// from the top, for whatever happened meanwhile.
			fb.mu.Lock()
			fb.addErr = nil
			fb.mu.Unlock()
			waitRoot(t, w, 0, "the root watched", func(s RootStatus) bool { return s.Watching && s.Dirs == 5 && s.Error == "" })
			waitFor(t, 5*time.Second, "its scan", func() bool {
				return sc.saw(func(d library.Dir) bool { return d.Path == r1 && d.Recursive })
			})
		})
	}
}

func TestPrivateAndVanishedDirsDoNotFailTheRoot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "ok", "lost+found", "gone")
	w, fb, _, _ := newUnit(t, root)
	fb.addErr = func(dir string) error {
		switch filepath.Base(dir) {
		case "lost+found":
			return &os.PathError{Op: "add", Path: dir, Err: syscall.EACCES}
		case "gone":
			return &os.PathError{Op: "add", Path: dir, Err: syscall.ENOENT}
		}
		return nil
	}
	setup(t, w, fb)
	if got := w.Status()[0]; !got.Watching || got.Dirs != 2 {
		t.Fatalf("Status() = %+v, want watching root and ok", got)
	}
}

func TestTooManyDirsIsNotTried(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	mkdirs(t, r1, "a", "b")
	mkdirs(t, r2, "c")
	w, fb, _, _ := newUnit(t, r1, r2)
	w.maxDirs = 2
	w.recheck = 30 * time.Millisecond
	start(t, w)
	waitRoot(t, w, 1, "second root", func(s RootStatus) bool { return s.Watching })
	time.Sleep(200 * time.Millisecond) // many rechecks

	got := w.Status()
	if got[0].Watching || !strings.Contains(got[0].Error, "3 directories") {
		t.Fatalf("first root: %+v", got[0])
	}
	if !got[1].Watching || got[1].Dirs != 2 {
		t.Fatalf("second root (within the limit): %+v", got[1])
	}
	for _, d := range []string{r1, filepath.Join(r1, "a"), filepath.Join(r1, "b")} {
		if fb.addCalls(d) != 0 {
			t.Fatalf("a watch was tried for %s", d)
		}
	}
}

func TestRecheck(t *testing.T) {
	r1, r2 := resolvedTemp(t), resolvedTemp(t)
	root := filepath.Join(r1, "lib")
	mkdirs(t, r2, "b")
	w, fb, sc, _ := newUnit(t, root, r2)
	w.recheck = 40 * time.Millisecond
	w.quiet = 60 * time.Millisecond
	start(t, w)
	waitRoot(t, w, 1, "second root", func(s RootStatus) bool { return s.Watching })
	other := func() bool { return w.Status()[1].Watching && w.Status()[1].Dirs == 2 }

	// Missing at the start: not watched, and not scanned.
	if got := w.Status()[0]; got.Watching || got.Error != "root not found" {
		t.Fatalf("missing root: %+v", got)
	}
	if sc.count() != 0 {
		t.Fatalf("scanned before anything appeared: %v", sc.snapshot())
	}

	// It appears: watched and scanned from the top.
	mkdirs(t, root, "sub")
	waitRoot(t, w, 0, "the root found", func(s RootStatus) bool { return s.Watching && s.Dirs == 2 })
	waitFor(t, 5*time.Second, "its scan", func() bool {
		return sc.saw(func(d library.Dir) bool { return d.Path == root && d.Recursive })
	})

	// Another directory is put in its place (a volume mounted over it): the
	// old one is kept at another name so that it has another inode.
	sc.reset()
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the replacement watched", func() bool {
		return fb.addCalls(root) == 2 && w.Status()[0].Watching && w.Status()[0].Dirs == 1
	})
	waitFor(t, 5*time.Second, "its scan", func() bool {
		return sc.saw(func(d library.Dir) bool { return d.Path == root && d.Recursive })
	})
	if slices.Contains(fb.watched(), filepath.Join(root, "sub")) {
		t.Fatal("the old subdirectory is still watched")
	}

	// And it goes away.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	waitRoot(t, w, 0, "the root lost", func(s RootStatus) bool { return !s.Watching })
	if got := w.Status()[0]; got.Dirs != 0 || got.Error != "root not found" {
		t.Fatalf("lost root: %+v", got)
	}
	if slices.Contains(fb.watched(), root) {
		t.Fatal("the lost root is still watched")
	}
	if !other() {
		t.Fatalf("the other root was disturbed: %+v", w.Status()[1])
	}
}

func TestRootDirGoneEvent(t *testing.T) {
	base := resolvedTemp(t)
	root := filepath.Join(base, "lib")
	mkdirs(t, root, "sub")
	w, fb, _, _ := newUnit(t, root)
	setup(t, w, fb)

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	w.handle(context.Background(), event{dir: root, op: opDirGone})
	if got := w.Status()[0]; got.Watching || got.Dirs != 0 || got.Error != "root not found" {
		t.Fatalf("after removal: %+v", got)
	}
	if len(fb.watched()) != 0 {
		t.Fatalf("watches left: %v", fb.watched())
	}
	// Its parent is outside the roots: nothing is queued for it.
	if got := drain(w); len(got) != 0 {
		t.Fatalf("queued %v", got)
	}

	// Removed and made again at once: watched again, and scanned.
	mkdirs(t, root, "sub")
	setup(t, w, fb)
	w.handle(context.Background(), event{dir: root, op: opDirGone})
	if got := w.Status()[0]; !got.Watching || got.Dirs != 2 {
		t.Fatalf("after replacement: %+v", got)
	}
	if got, want := drain(w), []library.Dir{{Path: root, Recursive: true}}; !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
}

func TestBackendFailureLeavesRootsToThePeriodicScan(t *testing.T) {
	root := t.TempDir()
	log, _ := testLogger(t)
	w := New(&fakeScanner{}, []string{root}, log)
	w.mkBackend = func() (backend, error) { return nil, syscall.EMFILE }
	stop := start(t, w)
	waitRoot(t, w, 0, "status", func(s RootStatus) bool { return s.Error != "" })
	if got := w.Status()[0]; got.Watching || !strings.Contains(got.Error, "cannot watch") {
		t.Fatalf("Status() = %+v", got)
	}
	stop() // Run still blocks until ctx is done
}

func TestBackendThatBreaksLeavesRootsToThePeriodicScan(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "a")
	w, fb, _, lb := newUnit(t, root)
	w.recheck = 20 * time.Millisecond
	stop := start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })

	close(fb.ev) // as a backend does when it cannot go on
	waitRoot(t, w, 0, "watching to stop", func(s RootStatus) bool { return !s.Watching })
	if got := w.Status()[0]; got.Dirs != 0 || got.Error != "file watcher stopped" {
		t.Fatalf("Status() = %+v", got)
	}
	time.Sleep(100 * time.Millisecond) // rechecks must not bring it back
	if got := w.Status()[0]; got.Watching {
		t.Fatalf("Status() = %+v after rechecks", got)
	}
	if lb.count("file watching stopped") != 1 {
		t.Fatalf("log:\n%s", lb.String())
	}
	stop()
}

// settles waits for the goroutine count to come back to base.
func settles(t *testing.T, base int) {
	t.Helper()
	waitFor(t, 3*time.Second, "goroutines to finish", func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= base
	})
}

func TestRunReturnsAndLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A")
	a := filepath.Join(root, "A")
	base := runtime.NumGoroutine()

	w, fb, sc, _ := newUnit(t, root)
	sc.fn = func(_ context.Context, n int, _ []library.Dir) (library.Result, error) {
		return library.Result{Deferred: []library.Dir{{Path: a}}}, nil // a retry is always pending
	}
	w.retry = time.Hour
	stop := start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })
	fb.ev <- event{a, "x.mkv", opCreate}
	waitFor(t, 5*time.Second, "a scan", func() bool { return sc.count() > 0 })

	began := time.Now()
	stop()
	if d := time.Since(began); d > time.Second {
		t.Fatalf("Run took %v to return", d)
	}
	if !fb.isClosed() {
		t.Fatal("the backend was not closed")
	}
	settles(t, base)
}

func TestCancelDuringAScan(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "A")
	a := filepath.Join(root, "A")
	base := runtime.NumGoroutine()

	w, fb, sc, _ := newUnit(t, root)
	entered := make(chan struct{})
	sc.fn = func(ctx context.Context, _ int, _ []library.Dir) (library.Result, error) {
		close(entered)
		<-ctx.Done()
		return library.Result{}, ctx.Err()
	}
	stop := start(t, w)
	waitRoot(t, w, 0, "watched", func(s RootStatus) bool { return s.Watching })
	fb.ev <- event{a, "x.mkv", opCreate}
	select {
	case <-entered: // positive control: the scan really is in flight
	case <-time.After(5 * time.Second):
		t.Fatal("no scan started")
	}
	began := time.Now()
	stop()
	if d := time.Since(began); d > time.Second {
		t.Fatalf("Run took %v to return with a scan in flight", d)
	}
	settles(t, base)
}
