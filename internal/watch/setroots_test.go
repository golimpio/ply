package watch

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"ply/internal/library"
)

// statusRoots is the roots Status reports, in order.
func statusRoots(w *Watcher) []string {
	var out []string
	for _, s := range w.Status() {
		out = append(out, s.Root)
	}
	return out
}

func TestSetRootsWhileRunning(t *testing.T) {
	base := resolvedTemp(t)
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	mkdirs(t, base, "a/x", "b/y", "c/z")
	w, fb, sc, _ := newUnit(t, a, b)
	start(t, w)
	waitRoot(t, w, 1, "b watched", func(s RootStatus) bool { return s.Watching })

	w.SetRoots([]string{b, c})
	waitFor(t, 5e9, "c watched", func() bool {
		st := w.Status()
		return len(st) == 2 && st[1].Root == c && st[1].Watching
	})
	if got := statusRoots(w); !slices.Equal(got, []string{b, c}) {
		t.Fatalf("Status roots %v", got)
	}
	want := []string{b, filepath.Join(b, "y"), c, filepath.Join(c, "z")}
	if got := fb.watched(); !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v (a dropped, c added)", got, want)
	}
	if fb.addCalls(b) != 1 {
		t.Fatalf("b was watched again (%d adds): a kept root keeps its watches", fb.addCalls(b))
	}
	if st := w.Status(); st[0].Dirs != 2 || st[1].Dirs != 2 {
		t.Fatalf("Status %+v, want 2 directories each", st)
	}

	// An event from the dropped root is not about any root now; one from the
	// new root is scanned (positive control).
	fb.ev <- event{dir: filepath.Join(a, "x"), name: "m.mkv", op: opCreate}
	fb.ev <- event{dir: filepath.Join(c, "z"), name: "m.mkv", op: opCreate}
	waitFor(t, 5e9, "a scan of c/z", func() bool { return sc.sawPath(filepath.Join(c, "z")) })
	if sc.saw(func(d library.Dir) bool { return within(a, d.Path) }) {
		t.Fatalf("scanned the dropped root: %v", sc.snapshot())
	}

	// The directory counts follow the roots to their new places: a directory
	// made in b (now the first root) is counted for b, not for c.
	mkdirs(t, b, "y/new")
	fb.ev <- event{dir: filepath.Join(b, "y"), name: "new", op: opCreate}
	waitRoot(t, w, 0, "b's new directory counted", func(s RootStatus) bool { return s.Dirs == 3 })
	if got := w.Status()[1].Dirs; got != 2 {
		t.Fatalf("c has %d directories, want 2", got)
	}
}

func TestSetRootsForgetsWhatWasQueuedUnderADroppedRoot(t *testing.T) {
	base := resolvedTemp(t)
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	mkdirs(t, base, "a/x", "b/y")
	w, fb, _, _ := newUnit(t, a, b)
	setup(t, w, fb)
	w.queue(library.Dir{Path: filepath.Join(a, "x")})
	w.queue(library.Dir{Path: filepath.Join(b, "y")})
	w.deferDirs([]library.Dir{{Path: a, Recursive: true}, {Path: b, Recursive: true}})

	w.reroot(context.Background(), []string{b}, "")
	w.mu.Lock()
	_, aDeferred := w.deferred[a]
	_, bDeferred := w.deferred[b]
	w.mu.Unlock()
	if aDeferred || !bDeferred {
		t.Fatalf("deferred a=%v b=%v, want only b", aDeferred, bDeferred)
	}
	// drain takes the deferred directories that are due too.
	want := []library.Dir{{Path: b, Recursive: true}, {Path: filepath.Join(b, "y")}}
	if got := drain(w); !slices.Equal(got, want) {
		t.Fatalf("pending %v, want only b's %v (positive control) and none of a's", got, want)
	}
}

// Nested roots share their watches: a directory has one owner. Dropping
// either root of a nested pair must leave the other one fully watched.
func TestSetRootsNested(t *testing.T) {
	for _, tc := range []struct {
		name        string
		order       func(outer, inner string) []string
		keep        func(outer, inner string) string
		wantWatched func(outer, inner string) []string
	}{
		{
			name:  "outer dropped, inner kept",
			order: func(o, i string) []string { return []string{o, i} }, // outer owns everything
			keep:  func(o, i string) string { return i },
			wantWatched: func(o, i string) []string {
				return []string{i, filepath.Join(i, "s")}
			},
		},
		{
			name:  "inner dropped, outer kept",
			order: func(o, i string) []string { return []string{i, o} }, // inner owns its tree
			keep:  func(o, i string) string { return o },
			wantWatched: func(o, i string) []string {
				return []string{o, filepath.Join(o, "b"), i, filepath.Join(i, "s")}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outer := resolvedTemp(t)
			inner := filepath.Join(outer, "inner")
			mkdirs(t, outer, "inner/s", "b")
			w, fb, _, _ := newUnit(t, tc.order(outer, inner)...)
			setup(t, w, fb)

			keep := tc.keep(outer, inner)
			w.reroot(context.Background(), []string{keep}, "")
			want := tc.wantWatched(outer, inner)
			slices.Sort(want)
			if got := fb.watched(); !slices.Equal(got, want) {
				t.Fatalf("watched %v, want %v", got, want)
			}
			if st := w.Status(); len(st) != 1 || st[0].Root != keep || !st[0].Watching || st[0].Dirs != len(want) {
				t.Fatalf("Status %+v, want %s watching %d directories", st, keep, len(want))
			}
		})
	}
}

func TestSetRootsBeforeRun(t *testing.T) {
	base := resolvedTemp(t)
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	mkdirs(t, base, "a", "b")
	w, fb, _, _ := newUnit(t, a)
	w.SetRoots([]string{b, b}) // a duplicate is one root
	start(t, w)
	waitRoot(t, w, 0, "b watched", func(s RootStatus) bool { return s.Root == b && s.Watching })
	if got := statusRoots(w); !slices.Equal(got, []string{b}) {
		t.Fatalf("Status roots %v, want [b]", got)
	}
	if n := fb.addCalls(a); n != 0 {
		t.Fatalf("a, replaced before Run, was watched (%d adds)", n)
	}
}

func TestSetRootsWithoutWatching(t *testing.T) {
	base := resolvedTemp(t)
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	mkdirs(t, base, "a", "b")

	t.Run("no backend", func(t *testing.T) {
		log, _ := testLogger(t)
		w := New(&fakeScanner{}, []string{a}, log)
		w.mkBackend = func() (backend, error) { return nil, syscall.EMFILE }
		start(t, w)
		waitRoot(t, w, 0, "a's status", func(s RootStatus) bool { return s.Error != "" })
		w.SetRoots([]string{a, b})
		waitFor(t, 5e9, "b listed", func() bool { return len(w.Status()) == 2 })
		for _, s := range w.Status() {
			if s.Watching || !strings.Contains(s.Error, "cannot watch") {
				t.Fatalf("Status %+v, want every root not watched, and why", w.Status())
			}
		}
	})

	t.Run("backend stopped", func(t *testing.T) {
		w, fb, _, _ := newUnit(t, a)
		start(t, w)
		waitRoot(t, w, 0, "a watched", func(s RootStatus) bool { return s.Watching })
		close(fb.ev)
		waitRoot(t, w, 0, "a not watched", func(s RootStatus) bool { return !s.Watching })
		w.SetRoots([]string{a, b})
		waitFor(t, 5e9, "b listed", func() bool { return len(w.Status()) == 2 })
		if s := w.Status()[1]; s.Watching || s.Error != "file watcher stopped" {
			t.Fatalf("new root's status %+v", s)
		}
		if n := fb.addCalls(b); n != 0 {
			t.Fatalf("b was watched on a stopped backend (%d adds)", n)
		}
	})
}
