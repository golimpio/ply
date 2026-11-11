package watch

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"ply/internal/library"
)

// backendCase is a backend the real-file-system tests run against.
type backendCase struct {
	name  string
	mk    func() (backend, error)
	named bool // its events say which entry changed
}

// backendCases lists the backends to test here: fsnotify everywhere, and on
// darwin (where the default is kqueue) fsnotify too.
var backendCases = []backendCase{{"fsnotify", newFsnotify, true}}

// eachBackend runs fn once for each backend, in parallel.
func eachBackend(t *testing.T, fn func(t *testing.T, bc backendCase)) {
	t.Helper()
	for _, bc := range backendCases {
		t.Run(bc.name, func(t *testing.T) {
			t.Parallel()
			fn(t, bc)
		})
	}
}

// rig is a Watcher on a real backend, with short timings, and a fake scanner.
type rig struct {
	t    *testing.T
	w    *Watcher
	sc   *fakeScanner
	stop func() // cancels Run and waits for it
}

func startRig(t *testing.T, bc backendCase, roots []string, tune func(*Watcher)) *rig {
	t.Helper()
	log, _ := testLogger(t)
	sc := &fakeScanner{}
	w := New(sc, roots, log)
	w.mkBackend = bc.mk
	w.maxDirs = -1
	w.quiet, w.maxWait, w.retry, w.recheck = 100*time.Millisecond, time.Second, 200*time.Millisecond, time.Hour
	if tune != nil {
		tune(w)
	}
	stop := start(t, w)
	for i, root := range roots {
		if _, err := os.Stat(root); err == nil {
			waitRoot(t, w, i, "root watched", func(s RootStatus) bool { return s.Watching })
		}
	}
	return &rig{t: t, w: w, sc: sc, stop: stop}
}

func (r *rig) waitDirs(i, n int) {
	r.t.Helper()
	waitRoot(r.t, r.w, i, "directory count", func(s RootStatus) bool { return s.Dirs == n })
}

// waitDir waits for a scan of a directory that matches.
func (r *rig) waitDir(what string, match func(library.Dir) bool) {
	r.t.Helper()
	waitFor(r.t, 10*time.Second, what, func() bool { return r.sc.saw(match) })
}

func at(path string) func(library.Dir) bool {
	return func(d library.Dir) bool { return d.Path == path }
}

func recursiveAt(path string) func(library.Dir) bool {
	return func(d library.Dir) bool { return d.Path == path && d.Recursive }
}

// settle waits long enough for anything pending to have been scanned.
func (r *rig) settle() { time.Sleep(4 * r.w.quiet) }

func TestFSCreateFile(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root := t.TempDir()
		movies, quiet := filepath.Join(root, "Movies"), filepath.Join(root, "Quiet")
		mkdirs(t, root, "Movies", "Quiet")
		r := startRig(t, bc, []string{root}, nil)

		// Files nobody cares about go into Quiet while a video lands in Movies.
		if bc.named {
			for _, name := range []string{".hidden.mkv", "._a.mkv", "notes.txt", "a.mkv.part", ".a.mkv.Xk2dQ"} {
				touch(t, filepath.Join(quiet, name), 1)
			}
		}
		touch(t, filepath.Join(movies, "Movie (2020).mkv"), 10)
		r.waitDir("a scan of Movies", at(movies))
		r.settle()
		if bc.named && r.sc.sawPath(quiet) {
			t.Fatalf("Quiet was scanned for files that are not media: %v", r.sc.snapshot())
		}

		// Sidecars: a subtitle is a plain scan, an .nfo or artwork says so.
		r.sc.reset()
		touch(t, filepath.Join(movies, "Movie (2020).en.srt"), 10)
		r.waitDir("a scan for the subtitle", at(movies))
		r.settle()
		r.sc.reset()
		touch(t, filepath.Join(movies, "Movie (2020).nfo"), 10)
		r.waitDir("a sidecars scan for the nfo", func(d library.Dir) bool { return d.Path == movies && d.Sidecars })
	})
}

func TestFSNewDirWithFiles(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root := t.TempDir()
		shows := filepath.Join(root, "Shows")
		mkdirs(t, root, "Shows")
		r := startRig(t, bc, []string{root}, nil)

		n := filepath.Join(shows, "New Show")
		touch(t, filepath.Join(n, "S01E01.mkv"), 10)
		touch(t, filepath.Join(n, "S01E01.srt"), 10)
		r.waitDir("a recursive scan of the new directory", recursiveAt(n))
		r.waitDirs(0, 3)
		r.settle()

		// The watch on the new directory works: a later file is seen.
		r.sc.reset()
		touch(t, filepath.Join(n, "S01E02.mkv"), 10)
		r.waitDir("a scan for a file in the new directory", at(n))
	})
}

func TestFSRenameDir(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root := t.TempDir()
		mkdirs(t, root, "A/sub", "Keep")
		touch(t, filepath.Join(root, "A", "sub", "f.mkv"), 10)
		r := startRig(t, bc, []string{root}, nil)
		r.waitDirs(0, 4)

		b := filepath.Join(root, "B")
		if err := os.Rename(filepath.Join(root, "A"), b); err != nil {
			t.Fatal(err)
		}
		// The parent is scanned as a whole (that is what lets the scanner
		// see the files moved), and the new name too.
		r.waitDir("a recursive scan of the parent", recursiveAt(root))
		r.waitDir("a recursive scan of the new name", recursiveAt(b))
		// The watches moved with it: root, Keep, B, B/sub; none left for A.
		r.waitDirs(0, 4)
		r.settle()
		if n := r.w.Status()[0].Dirs; n != 4 {
			t.Fatalf("%d directories watched after the rename, want 4", n)
		}
		r.sc.reset()
		touch(t, filepath.Join(b, "sub", "g.mkv"), 10)
		r.waitDir("a scan for a file under the renamed directory", at(filepath.Join(b, "sub")))
	})
}

func TestFSDeleteDir(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root := t.TempDir()
		mkdirs(t, root, "A/sub", "Keep")
		touch(t, filepath.Join(root, "A", "sub", "f.mkv"), 10)
		r := startRig(t, bc, []string{root}, nil)
		r.waitDirs(0, 4)

		if err := os.RemoveAll(filepath.Join(root, "A")); err != nil {
			t.Fatal(err)
		}
		r.waitDir("a recursive scan of the parent", recursiveAt(root))
		r.waitDirs(0, 2) // root and Keep
		r.settle()
		if n := r.w.Status()[0].Dirs; n != 2 {
			t.Fatalf("%d directories watched, want 2", n)
		}
		// Positive control: Keep is still watched.
		r.sc.reset()
		keep := filepath.Join(root, "Keep")
		touch(t, filepath.Join(keep, "x.mkv"), 10)
		r.waitDir("a scan for a file in Keep", at(keep))
	})
}

func TestFSNestedNewDirs(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root := t.TempDir()
		r := startRig(t, bc, []string{root}, nil)

		deep := filepath.Join(root, "a", "b", "c", "d")
		mkdirs(t, root, "a/b/c/d")
		r.waitDirs(0, 5)
		r.waitDir("a recursive scan of the top new directory", recursiveAt(filepath.Join(root, "a")))
		r.settle()
		r.sc.reset()
		touch(t, filepath.Join(deep, "f.mkv"), 10)
		r.waitDir("a scan for a file four levels down", at(deep))
	})
}

func TestFSRootRemovedAndBack(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		base := t.TempDir()
		r1, r2 := filepath.Join(base, "one"), filepath.Join(base, "two")
		mkdirs(t, r1, "sub")
		mkdirs(t, r2, "sub")
		r := startRig(t, bc, []string{r1, r2}, func(w *Watcher) { w.recheck = 100 * time.Millisecond })
		r.waitDirs(0, 2)

		if err := os.RemoveAll(r1); err != nil {
			t.Fatal(err)
		}
		waitRoot(t, r.w, 0, "the root lost", func(s RootStatus) bool { return !s.Watching })
		if got := r.w.Status(); got[0].Dirs != 0 || got[0].Error != "root not found" {
			t.Fatalf("lost root: %+v", got[0])
		}
		// Positive control: the other root is unaffected and still works.
		if got := r.w.Status(); !got[1].Watching || got[1].Dirs != 2 {
			t.Fatalf("other root: %+v", got[1])
		}
		r.settle()
		r.sc.reset()
		touch(t, filepath.Join(r2, "sub", "x.mkv"), 10)
		r.waitDir("a scan in the other root", at(filepath.Join(r2, "sub")))

		// It comes back (a volume mounted again): watched and scanned.
		r.sc.reset()
		mkdirs(t, r1, "sub")
		waitRoot(t, r.w, 0, "the root back", func(s RootStatus) bool { return s.Watching && s.Dirs == 2 })
		r.waitDir("a scan of the returned root", recursiveAt(r1))
		r.settle()
		r.sc.reset()
		touch(t, filepath.Join(r1, "sub", "y.mkv"), 10)
		r.waitDir("a scan for a file in the returned root", at(filepath.Join(r1, "sub")))
	})
}

func TestFSStatusCountsDirs(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		root, other := t.TempDir(), t.TempDir()
		mkdirs(t, root, "a/b", "c", ".hidden/x")
		mkdirs(t, other, "elsewhere")
		if err := os.Symlink(other, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(root, "missing")
		r := startRig(t, bc, []string{root, missing}, nil)

		// root, a, a/b, c: not .hidden and its children, not the symlink's target.
		want := []RootStatus{
			{Root: root, Watching: true, Dirs: 4, Depth: DepthAll},
			{Root: missing, Watching: false, Dirs: 0, Depth: DepthAll, Error: "root not found"},
		}
		waitFor(t, 5*time.Second, "statuses", func() bool { return slices.Equal(r.w.Status(), want) })

		mkdirs(t, root, "c/d")
		r.waitDirs(0, 5)
	})
}

func TestFSSymlinkedRootKeepsItsSpelling(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		target := resolvedTemp(t)
		mkdirs(t, target, "Movies")
		link := filepath.Join(resolvedTemp(t), "media")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		r := startRig(t, bc, []string{link}, nil)
		r.waitDirs(0, 2)

		touch(t, filepath.Join(target, "Movies", "a.mkv"), 10)
		r.waitDir("a scan of Movies", at(filepath.Join(link, "Movies")))
		for _, c := range r.sc.snapshot() {
			for _, d := range c.dirs {
				if !within(link, d.Path) {
					t.Fatalf("scanned %s, not spelled under the configured root %s", d.Path, link)
				}
			}
		}
		// New directories keep the spelling too.
		r.sc.reset()
		mkdirs(t, target, "Shows/New")
		r.waitDir("a recursive scan of the new directory", recursiveAt(filepath.Join(link, "Shows")))
		r.waitDirs(0, 4)
		for _, c := range r.sc.snapshot() {
			for _, d := range c.dirs {
				if !within(link, d.Path) {
					t.Fatalf("scanned %s, not spelled under the configured root %s", d.Path, link)
				}
			}
		}
	})
}

// openFDs counts this process's open descriptors (reading the directory takes
// one, the same before and after).
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot count descriptors: %v", err)
	}
	return len(ents)
}

// These do not run in parallel: they count goroutines and descriptors of the
// whole process.
func TestFSRunReturnsAndLeavesNothingBehind(t *testing.T) {
	for _, bc := range backendCases {
		t.Run(bc.name, func(t *testing.T) {
			root := t.TempDir()
			mkdirs(t, root, "A/sub", "B")
			fds, base := openFDs(t), runtime.NumGoroutine()

			r := startRig(t, bc, []string{root, filepath.Join(root, "missing")}, nil)
			r.waitDirs(0, 4)
			touch(t, filepath.Join(root, "A", "x.mkv"), 10)
			mkdirs(t, root, "C/d")
			r.waitDir("a scan of the new directory", recursiveAt(filepath.Join(root, "C")))
			r.waitDirs(0, 6)

			began := time.Now()
			r.stop()
			if d := time.Since(began); d > time.Second {
				t.Fatalf("Run took %v to return", d)
			}
			settles(t, base)
			waitFor(t, 3*time.Second, "descriptors to be closed", func() bool { return openFDs(t) <= fds })
		})
	}
}
