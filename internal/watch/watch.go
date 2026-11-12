// Package watch keeps the library in step with the file system between full
// scans: it watches the directories under the media roots and, once events
// have died down, asks the scanner to rescan only what changed.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ply/internal/library"
)

// Scanner is the part of library.Scanner the watcher drives.
type Scanner interface {
	ScanDirs(ctx context.Context, dirs []library.Dir) (library.Result, error)
}

// RootStatus is what the watcher knows about one media root.
type RootStatus struct {
	Root     string `json:"root"`
	Watching bool   `json:"watching"`
	Dirs     int    `json:"dirs"`  // directories watched
	Depth    int    `json:"depth"` // how deep below the root it watches: 0 only it, -1 everything
	Error    string `json:"error,omitempty"`
}

// File kinds, by extension, that matter to the scanner. The subtitle list is
// the one in library/sidecar.go.
var (
	subtitleExts = map[string]bool{".srt": true, ".vtt": true, ".ass": true}
	metadataExts = map[string]bool{".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".tbn": true}
)

const rootNotFound = "root not found"

// statusSettingUp is the reason a root carries while its first walk runs. A big tree on a slow
// share can take a while, and until it finishes the root is neither watched nor refused: without
// this, Settings says "Not watched" with no reason, which reads like a failure it has not had.
const statusSettingUp = "setting up"

// setupConcurrency is how many directories the setup of a tree lists, and
// watches and snapshots, at once. On a network share each of those waits for the
// server: on an SMB NAS (measured 2026-11-11, 440 show folders) the walk took 65 s,
// and the watch and the snapshot (a stat per file) 0.9 s a folder, one at a time:
// with a scan beside it, a library of 700 folders took 26 minutes to be watched.
// 16 at a time made the watches 18 times faster and the snapshots 11.
const setupConcurrency = 16

// errTooManyDirs is why a root is not even tried: it has more directories than
// there are descriptors to watch them with.
var errTooManyDirs = errors.New("not watching, too many directories")

type root struct {
	path  string // configured spelling: absolute and clean
	depth int    // how deep below it to watch (DepthAll for everything)

	// Run's goroutine only:
	info     os.FileInfo   // the root when it was watched; nil when it was not found
	missing  bool          // not found (or unreadable) at the last look: look again
	logged   bool          // the watch failure was logged; stay quiet until it works
	hopeless bool          // too many directories: looking again can only give the same answer
	backoff  time.Duration // watches failed: wait this long before trying again (doubles, capped)
	retryAt  time.Time
}

// stamp is what a directory snapshot keeps of a file.
type stamp struct {
	size  int64
	mtime int64
}

// namelessBackend is implemented by backends whose events never name the
// entry that changed.
type namelessBackend interface{ nameless() bool }

type deferredDir struct {
	dir library.Dir
	due time.Time
}

// Watcher watches the directories under the media roots.
type Watcher struct {
	sc    Scanner
	log   *slog.Logger
	roots []*root

	// Timing and limits; New sets the defaults, tests change them before Run.
	quiet   time.Duration // after the last event, before scanning
	maxWait time.Duration // after the first pending event, at most
	retry   time.Duration // before scanning again a directory with files still settling
	now     func() time.Time
	recheck time.Duration // between looks at the roots themselves
	maxDirs int           // directories the process may watch; <0 is no limit

	mkBackend func() (backend, error)
	readDir   func(dir string) ([]os.DirEntry, error) // os.ReadDir, for the setup's walks and snapshots

	// Run's goroutine only.
	be   backend
	dirs map[string]int // watched directory -> index of the root that owns it
	// For a backend whose events carry no names: the files of each watched
	// directory as last seen, to tell what an event was about.
	unnamed bool
	snaps   map[string]map[string]stamp

	mu       sync.Mutex // guards the rest
	status   []RootStatus
	pending  map[string]library.Dir
	first    time.Time // first event of the pending set
	last     time.Time // latest event of the pending set
	ready    bool      // scan the pending set without waiting
	deferred map[string]deferredDir
	wake     chan struct{}
	want     []RootSpec    // roots asked for by SetRoots, not applied yet (nil: none)
	rootsC   chan struct{} // SetRoots was called
}

// RootSpec is a media root and how deep below it to watch. Depth 0 watches the root alone, 1 the
// root and its own subdirectories, 2 one level further, and DepthAll the whole tree. A media library
// is deep (a show, a season, an episode), and every watched directory is a descriptor on kqueue and
// an inotify watch on Linux, so the depth is what keeps a large library inside the process's budget;
// what it leaves out is left to the periodic scan (docs/M7-FOLDERS.md).
type RootSpec struct {
	Path  string
	Depth int
}

// DepthAll watches every directory below a root.
const DepthAll = -1

// specsOf is the roots of New and SetRoots: watched whole, as they always were.
func specsOf(paths []string) []RootSpec {
	out := make([]RootSpec, len(paths))
	for i, p := range paths {
		out[i] = RootSpec{Path: p, Depth: DepthAll}
	}
	return out
}

// New returns a watcher for roots, which must be absolute and clean, as the
// scanner has them. Nothing happens until Run. Each root is watched whole; see
// NewSpecs for a depth per root.
func New(sc Scanner, roots []string, log *slog.Logger) *Watcher {
	return NewSpecs(sc, specsOf(roots), log)
}

// NewSpecs is New with a depth per root: a root is watched down to its depth, not the whole tree.
func NewSpecs(sc Scanner, roots []RootSpec, log *slog.Logger) *Watcher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	w := &Watcher{
		sc:        sc,
		log:       log,
		quiet:     2 * time.Second,
		maxWait:   10 * time.Second,
		retry:     library.DefaultSettle + time.Second,
		recheck:   time.Minute,
		maxDirs:   dirLimit(),
		mkBackend: newBackend,
		readDir:   os.ReadDir,
		now:       time.Now,
		dirs:      map[string]int{},
		snaps:     map[string]map[string]stamp{},
		pending:   map[string]library.Dir{},
		deferred:  map[string]deferredDir{},
		wake:      make(chan struct{}, 1),
		rootsC:    make(chan struct{}, 1),
	}
	for _, s := range roots {
		w.roots = append(w.roots, &root{path: s.Path, depth: s.Depth})
		w.status = append(w.status, RootStatus{Root: s.Path, Depth: s.Depth})
	}
	return w
}

// SetRoots replaces the media roots, which must be absolute and clean, as the
// scanner has them. It is safe to call at any time, before or during Run, which
// applies it on its own goroutine: a dropped root loses its watches and the
// directories queued under it, a new one is watched. Scanning a new root is the
// scanner's job, not the watcher's.
func (w *Watcher) SetRoots(paths []string) { w.SetRootSpecs(specsOf(paths)) }

// SetRootSpecs is SetRoots with a depth per root. A root whose depth changed is watched again: its
// watches at the old depth are dropped first.
func (w *Watcher) SetRootSpecs(specs []RootSpec) {
	want := []RootSpec{}
	for _, s := range specs {
		if !slices.ContainsFunc(want, func(o RootSpec) bool { return o.Path == s.Path }) {
			want = append(want, s)
		}
	}
	w.mu.Lock()
	w.want = want
	w.mu.Unlock()
	select {
	case w.rootsC <- struct{}{}:
	default:
	}
}

// takeRoots returns the roots SetRoots asked for since the last call, or nil.
func (w *Watcher) takeRoots() []RootSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.want
	w.want = nil
	return r
}

// reroot makes paths the roots, on Run's goroutine. down, when not empty, is
// why nothing can be watched (the backend failed or stopped): a new root gets
// it as its status instead of watches. Before Run has a backend only the list
// changes.
func (w *Watcher) reroot(ctx context.Context, specs []RootSpec, down string) {
	old := w.roots
	kept := map[string]int{} // path -> index in old, when its depth is unchanged
	for i, r := range old {
		if slices.ContainsFunc(specs, func(s RootSpec) bool { return s.Path == r.path && s.Depth == r.depth }) {
			kept[r.path] = i
		}
	}
	var dropped []int
	for i, r := range old {
		if _, ok := kept[r.path]; !ok {
			dropped = append(dropped, i)
		}
	}
	// Nested roots share watches (a directory has one owner, the root that got
	// there first): when a dropped root and a kept one nest, every root is
	// watched afresh. Roots from the web UI never nest, so this is rare.
	overlap := false
	for _, i := range dropped {
		for p := range kept {
			overlap = overlap || within(old[i].path, p) || within(p, old[i].path)
		}
	}
	live := w.be != nil && down == ""
	if live && overlap {
		for _, r := range old {
			w.dropTree(r.path)
		}
	} else if w.be != nil {
		gone := map[int]bool{}
		for _, i := range dropped {
			gone[i] = true
		}
		for d, owner := range w.dirs {
			if gone[owner] {
				w.be.remove(d)
				delete(w.dirs, d)
				delete(w.snaps, d)
			}
		}
	}

	roots := make([]*root, len(specs))
	remap := map[int]int{} // old index -> new
	var added []int
	w.mu.Lock()
	status := make([]RootStatus, len(specs))
	for j, s := range specs {
		if i, ok := kept[s.Path]; ok {
			roots[j], status[j], remap[i] = old[i], w.status[i], j
		} else {
			roots[j], status[j] = &root{path: s.Path, depth: s.Depth}, RootStatus{Root: s.Path, Depth: s.Depth}
			added = append(added, j)
		}
	}
	for d, owner := range w.dirs {
		w.dirs[d] = remap[owner]
	}
	w.roots, w.status = roots, status
	// What was queued under a dropped root is out of the scanner's reach now.
	for p := range w.pending {
		if !w.inRoots(p) {
			delete(w.pending, p)
		}
	}
	for p := range w.deferred {
		if !w.inRoots(p) {
			delete(w.deferred, p)
		}
	}
	w.mu.Unlock()

	if len(added) > 0 || len(dropped) > 0 {
		w.log.Info("media roots changed", "roots", len(specs), "added", len(added), "dropped", len(dropped))
	}
	switch {
	case down != "":
		for _, j := range added {
			w.setStatus(j, false, down)
		}
	case w.be == nil:
	case overlap:
		for j := range w.roots {
			w.setupRoot(ctx, j)
		}
	default:
		for _, j := range added {
			w.setupRoot(ctx, j)
		}
	}
}

// Status reports, in the order of the roots, whether each is being watched.
// It is safe to call at any time.
func (w *Watcher) Status() []RootStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.status)
}

// Run watches until ctx is done. It adds the watches, turns events into
// partial scans and keeps an eye on the roots themselves; no goroutine or
// descriptor outlives it.
func (w *Watcher) Run(ctx context.Context) {
	if paths := w.takeRoots(); paths != nil {
		w.reroot(ctx, paths, "") // no backend yet: only the list changes
	}
	be, err := w.mkBackend()
	if err != nil {
		w.log.Error("file watching is off; relying on periodic scans", "err", err)
		down := "cannot watch: " + err.Error()
		for i := range w.roots {
			w.setStatus(i, false, down)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.rootsC:
				if paths := w.takeRoots(); paths != nil {
					w.reroot(ctx, paths, down)
				}
			}
		}
	}
	w.use(be)

	var scans sync.WaitGroup
	scans.Add(1)
	go func() {
		defer scans.Done()
		w.scanLoop(ctx)
	}()
	defer func() {
		be.close()
		scans.Wait()
	}()

	for i := range w.roots {
		w.setupRoot(ctx, i)
	}
	tick := time.NewTicker(w.recheck)
	defer tick.Stop()
	events, errs, recheckC := be.events(), be.errors(), tick.C
	down := "" // why nothing is watched any more
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.rootsC:
			if paths := w.takeRoots(); paths != nil {
				w.reroot(ctx, paths, down)
			}
		case ev, ok := <-events:
			if !ok {
				// The backend broke (it only closes its channels in close).
				w.log.Error("file watching stopped; relying on periodic scans")
				down = "file watcher stopped"
				for i, r := range w.roots {
					w.dropTree(r.path)
					w.setStatus(i, false, down)
				}
				events, errs, recheckC = nil, nil, nil
				continue
			}
			w.handle(ctx, ev)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errors.Is(err, errOverflow) {
				w.overflow(ctx)
			} else {
				w.log.Warn("file watcher error", "err", err)
			}
		case <-recheckC:
			w.recheckRoots(ctx)
		}
	}
}

// use makes be the backend.
func (w *Watcher) use(be backend) {
	w.be = be
	if nb, ok := be.(namelessBackend); ok {
		w.unnamed = nb.nameless()
	}
}

// handle turns one event into queued directories and watch changes.
func (w *Watcher) handle(ctx context.Context, ev event) {
	i, ok := w.dirs[ev.dir]
	if !ok {
		return // not watched, or not any more
	}
	switch {
	case ev.op == opDirGone:
		w.goneDir(ctx, ev.dir, i)
	case ev.name == "":
		w.relist(ctx, ev.dir, i)
	default:
		w.entry(ctx, ev, i)
	}
}

// entry handles an event about a named entry of a watched directory.
func (w *Watcher) entry(ctx context.Context, ev event, i int) {
	if strings.HasPrefix(ev.name, ".") || ev.op&^opChmod == 0 {
		return // dot files (rsync temp files, AppleDouble) and attribute changes
	}
	p := filepath.Join(ev.dir, ev.name)
	if ev.op&(opRemove|opRename) != 0 {
		if owner, watched := w.dirs[p]; watched {
			w.goneDir(ctx, p, owner)
			return
		}
	}
	if ev.op&opCreate != 0 {
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			if _, watched := w.dirs[p]; !watched {
				w.newDir(ctx, p, i)
			}
			return
		}
	}
	ext := strings.ToLower(filepath.Ext(ev.name))
	switch {
	case library.IsVideoFile(ev.name), subtitleExts[ext]:
		w.queue(library.Dir{Path: ev.dir})
	case metadataExts[ext]:
		w.queue(library.Dir{Path: ev.dir, Sidecars: true})
	}
}

// relist handles "something in dir changed" from a backend without names: the
// files of dir are compared with its snapshot and each difference is handled
// as a named event would be (without a snapshot, the whole directory is
// queued, sidecars included), and its subdirectories are compared with the
// watched ones.
func (w *Watcher) relist(ctx context.Context, dir string, i int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // gone: opDirGone will say so
	}
	files := listing(entries)
	if old, ok := w.snaps[dir]; ok {
		w.snaps[dir] = files
		for name, st := range files {
			if prev, had := old[name]; !had {
				w.entry(ctx, event{dir: dir, name: name, op: opCreate}, i)
			} else if prev != st {
				w.entry(ctx, event{dir: dir, name: name, op: opWrite}, i)
			}
		}
		for name := range old {
			if _, ok := files[name]; !ok {
				w.entry(ctx, event{dir: dir, name: name, op: opRemove}, i)
			}
		}
	} else {
		w.queue(library.Dir{Path: dir, Sidecars: true})
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		seen[p] = true
		if _, watched := w.dirs[p]; !watched {
			w.newDir(ctx, p, i)
		}
	}
	var vanished []string
	for p := range w.dirs {
		if filepath.Dir(p) == dir && !seen[p] {
			vanished = append(vanished, p)
		}
	}
	for _, p := range vanished {
		w.goneDir(ctx, p, i)
	}
}

// listing is the snapshot of the files (not directories, not hidden ones)
// among entries.
func listing(entries []os.DirEntry) map[string]stamp {
	files := map[string]stamp{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var st stamp
		if info, err := e.Info(); err == nil {
			st = stamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
		}
		files[e.Name()] = st
	}
	return files
}

// snapshot lists the files of d, for a backend without names; nil with names,
// or if d cannot be read. It is safe to call from any goroutine.
func (w *Watcher) snapshot(d string) map[string]stamp {
	if !w.unnamed {
		return nil
	}
	entries, err := w.readDir(d)
	if err != nil {
		return nil
	}
	return listing(entries)
}

// newDir watches the tree at p, which has appeared, and queues it. The queue
// comes last: the scan that follows must see everything the new watches
// cannot.
func (w *Watcher) newDir(ctx context.Context, p string, i int) {
	if err := w.addTree(ctx, p, i, false); err != nil && ctx.Err() == nil {
		w.failRoot(i, err)
	}
	w.queue(library.Dir{Path: p, Recursive: true})
}

// goneDir handles the removal of the watched directory p of root i, or its
// move out of the tree: its watches go, and its parent is scanned recursively,
// which lets the scanner pair the files that moved with their new paths.
func (w *Watcher) goneDir(ctx context.Context, p string, i int) {
	w.dropTree(p)
	if j := w.rootIndex(p); j >= 0 {
		w.rootGone(ctx, j)
		return
	}
	w.queue(library.Dir{Path: filepath.Dir(p), Recursive: true})
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
		w.newDir(ctx, p, i) // replaced by another directory
	}
}

// rootGone handles the loss of root j's own directory.
func (w *Watcher) rootGone(ctx context.Context, j int) {
	r := w.roots[j]
	if parent := filepath.Dir(r.path); w.inRoots(parent) {
		w.queue(library.Dir{Path: parent, Recursive: true})
	}
	if fi, err := os.Stat(r.path); err == nil && fi.IsDir() {
		// Back already (replaced, or remounted): watch it afresh.
		if w.setupRoot(ctx, j) {
			w.queue(library.Dir{Path: r.path, Recursive: true})
		}
		return
	}
	w.rootUnavailable(j, rootNotFound)
}

// overflow handles lost events: anything may have changed.
func (w *Watcher) overflow(ctx context.Context) {
	w.log.Warn("file event queue overflowed; rescanning all roots")
	for i, r := range w.roots {
		if w.watching(i) {
			w.resync(ctx, i)
		}
		w.queue(library.Dir{Path: r.path, Recursive: true})
	}
}

// resync makes the watches of root i match the directories on disk.
func (w *Watcher) resync(ctx context.Context, i int) {
	var stale []string
	for d, owner := range w.dirs {
		if fi, err := os.Lstat(d); owner == i && (err != nil || !fi.IsDir()) {
			stale = append(stale, d)
		}
	}
	for _, d := range stale {
		w.dropTree(d)
	}
	if err := w.addTree(ctx, w.roots[i].path, i, true); err != nil && ctx.Err() == nil {
		w.failRoot(i, err)
	}
}

// recheckRoots looks at the roots themselves, which no directory watch can
// tell about: one that was missing and is back, or that now names another
// directory (a volume mounted over it), is watched afresh and scanned; one
// that is gone loses its watches. A root whose watches failed is tried again
// (the limit may have been raised, descriptors freed), unless it was simply
// too big.
func (w *Watcher) recheckRoots(ctx context.Context) {
	for i, r := range w.roots {
		if ctx.Err() != nil {
			return
		}
		fi, err := os.Stat(r.path)
		if err != nil || !fi.IsDir() {
			if !r.missing {
				w.dropTree(r.path)
				w.rootUnavailable(i, rootNotFound)
			}
			continue
		}
		retry := !w.watching(i) && !r.hopeless && !w.now().Before(r.retryAt)
		if r.missing || retry || !os.SameFile(r.info, fi) {
			w.dropTree(r.path)
			if w.setupRoot(ctx, i) {
				w.queue(library.Dir{Path: r.path, Recursive: true})
			}
		}
	}
}

// setupRoot watches root i. It reports whether it is watched now. A root that
// cannot be watched is left to the periodic scan.
func (w *Watcher) setupRoot(ctx context.Context, i int) bool {
	r := w.roots[i]
	fi, err := os.Stat(r.path)
	switch {
	case err == nil && fi.IsDir():
	case err == nil:
		return w.rootUnavailable(i, "root is not a directory")
	case errors.Is(err, fs.ErrNotExist):
		return w.rootUnavailable(i, rootNotFound)
	default:
		return w.rootUnavailable(i, err.Error())
	}
	r.missing, r.hopeless, r.info = false, false, fi
	w.setStatus(i, false, statusSettingUp)
	if err := w.addTree(ctx, r.path, i, false); err != nil {
		if ctx.Err() == nil {
			w.failRoot(i, err)
		}
		return false
	}
	r.logged, r.backoff, r.retryAt = false, 0, time.Time{}
	w.setStatus(i, true, "")
	w.mu.Lock()
	n := w.status[i].Dirs
	w.mu.Unlock()
	w.log.Info("watching media root", "root", r.path, "dirs", n)
	return true
}

// rootUnavailable records that root i is missing and logs it once. It
// returns false.
func (w *Watcher) rootUnavailable(i int, msg string) bool {
	r := w.roots[i]
	r.missing, r.info = true, nil
	w.setStatus(i, false, msg)
	if !r.logged {
		r.logged = true
		w.log.Warn("media root cannot be watched; will look again", "root", r.path, "reason", msg)
	}
	return false
}

// failRoot gives up on watching root i: a watch could not be added (out of
// file descriptors or inotify watches, or anything else). Its watches are
// dropped and the periodic scan has to cover it.
func (w *Watcher) failRoot(i int, err error) {
	r := w.roots[i]
	w.dropTree(r.path)
	r.hopeless = errors.Is(err, errTooManyDirs)
	// A limit (inotify watches, descriptors) is not raised every minute: wait
	// longer each time, up to an hour, before walking the tree again.
	r.backoff = min(max(2*r.backoff, w.recheck), maxRootBackoff)
	r.retryAt = w.now().Add(r.backoff)
	msg := describe(err)
	w.setStatus(i, false, msg)
	if !r.logged {
		r.logged = true
		w.log.Warn("cannot watch media root; relying on periodic scans", "root", r.path, "err", msg)
	}
}

// describe adds the remedy to the errors that have one.
func describe(err error) string {
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return "inotify watch limit reached (raise fs.inotify.max_user_watches): " + err.Error()
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return "too many open files (raise the limit): " + err.Error()
	}
	return err.Error()
}

// addTree watches top and, below it, every directory that is not watched yet,
// leaving out hidden ones and symlinks. top is of root i. A watched directory
// is taken to have its subdirectories watched too, unless all says to look.
func (w *Watcher) addTree(ctx context.Context, top string, i int, all bool) error {
	dirs, err := w.listDirs(ctx, top, all, w.roots[i].depth)
	if err != nil {
		return err
	}
	if w.maxDirs >= 0 && len(w.dirs)+len(dirs) > w.maxDirs {
		return fmt.Errorf("%w: %d directories to watch, more than the %d this process can",
			errTooManyDirs, len(w.dirs)+len(dirs), w.maxDirs)
	}
	// Each directory is watched, then snapshotted (a change after the watch is
	// not missed), setupConcurrency at a time; the results are recorded here, on
	// Run's goroutine, every watch that was added included, so that a failure
	// drops them all (failRoot).
	rootPath := w.roots[i].path
	skippable := func(d string, err error) bool { // gone, or private
		return d != rootPath && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission))
	}
	type added struct {
		tried bool
		err   error
		files map[string]stamp
	}
	res := make([]added, len(dirs))
	var failed atomic.Bool
	parallel(len(dirs), setupConcurrency, func() bool { return failed.Load() || ctx.Err() != nil }, func(k int) {
		r := &res[k]
		r.tried = true
		if r.err = w.be.add(dirs[k]); r.err != nil {
			if !skippable(dirs[k], r.err) {
				failed.Store(true)
			}
			return
		}
		r.files = w.snapshot(dirs[k])
	})
	var first error
	for k, d := range dirs {
		switch r := res[k]; {
		case !r.tried:
		case r.err == nil:
			w.dirs[d] = i
			if r.files != nil {
				w.snaps[d] = r.files
			}
			w.adjust(i, 1)
		case skippable(d, r.err):
			w.log.Debug("directory not watched", "dir", d, "err", r.err)
		case first == nil:
			first = r.err
		}
	}
	if first != nil {
		return first
	}
	return ctx.Err()
}

// parallel calls f(k) for every k in [0, n), limit calls at a time, and returns
// once they are done. When stop (if not nil) reports true, no more calls start.
func parallel(n, limit int, stop func() bool, f func(k int)) {
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(limit, n) {
		wg.Go(func() {
			for k := range work {
				f(k)
			}
		})
	}
	for k := range n {
		if stop != nil && stop() {
			break
		}
		work <- k
	}
	close(work)
	wg.Wait()
}

// listDirs lists top and the unwatched directories below it, spelled under
// top, in path order, leaving out hidden ones and symlinks. It reads the
// symlink-resolved top, setupConcurrency directories at a time.
func (w *Watcher) listDirs(ctx context.Context, top string, all bool, maxDepth int) ([]string, error) {
	resolved, err := filepath.EvalSymlinks(top)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Lstat(resolved); err == nil && !fi.IsDir() {
			return nil, nil
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // vanished
		}
		return nil, err
	}

	type dir struct {
		path, spelled string
		level         int // how far below the root it is
	}
	var (
		mu      sync.Mutex
		more    = sync.NewCond(&mu)
		todo    = []dir{{path: resolved, spelled: filepath.Clean(top)}}
		reading int // directories taken from todo and not done yet
		out     []string
		topErr  error
	)
	// visit lists d and returns its subdirectories to visit. w.dirs is only read:
	// its writer, Run's goroutine, is waiting for listDirs.
	visit := func(d dir) []dir {
		_, watched := w.dirs[d.spelled]
		if watched && !all {
			return nil // its own events cover what is below
		}
		if !watched {
			mu.Lock()
			out = append(out, d.spelled)
			mu.Unlock()
		}
		if maxDepth >= 0 && d.level >= maxDepth {
			return nil // the directories below it are past the depth asked for
		}
		entries, err := w.readDir(d.path)
		if err != nil {
			if d.path == resolved {
				topErr = err // read after the walk
			}
			return nil // unreadable or gone: the directory is listed, its children are not
		}
		var sub []dir
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				sub = append(sub, dir{filepath.Join(d.path, e.Name()), filepath.Join(d.spelled, e.Name()), d.level + 1})
			}
		}
		return sub
	}
	var wg sync.WaitGroup
	for range setupConcurrency {
		wg.Go(func() {
			mu.Lock()
			defer mu.Unlock()
			for {
				for len(todo) == 0 && reading > 0 {
					more.Wait()
				}
				if len(todo) == 0 || ctx.Err() != nil {
					more.Broadcast() // done: wake the others to see it
					return
				}
				d := todo[len(todo)-1]
				todo = todo[:len(todo)-1]
				reading++
				mu.Unlock()
				sub := visit(d)
				mu.Lock()
				reading--
				todo = append(todo, sub...)
				more.Broadcast()
			}
		})
	}
	wg.Wait()
	slices.Sort(out)
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if topErr != nil && !errors.Is(topErr, fs.ErrNotExist) {
		return out, topErr
	}
	return out, nil
}

// dropTree stops watching p and everything below it.
func (w *Watcher) dropTree(p string) {
	for d, owner := range w.dirs {
		if within(p, d) {
			w.be.remove(d) // the watch may have died with the directory
			delete(w.dirs, d)
			delete(w.snaps, d)
			w.adjust(owner, -1)
		}
	}
}

func (w *Watcher) rootIndex(p string) int {
	return slices.IndexFunc(w.roots, func(r *root) bool { return r.path == p })
}

func (w *Watcher) inRoots(p string) bool {
	return slices.ContainsFunc(w.roots, func(r *root) bool { return within(r.path, p) })
}

// within reports whether p is dir or below it.
func within(dir, p string) bool {
	sep := string(filepath.Separator)
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, sep)+sep)
}

func (w *Watcher) watching(i int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status[i].Watching
}

func (w *Watcher) setStatus(i int, watching bool, errMsg string) {
	w.mu.Lock()
	w.status[i].Watching, w.status[i].Error = watching, errMsg
	w.mu.Unlock()
}

func (w *Watcher) adjust(i, delta int) {
	w.mu.Lock()
	w.status[i].Dirs += delta
	w.mu.Unlock()
}

// queue adds d to the pending set, merging its flags with those already there.
func (w *Watcher) queue(d library.Dir) {
	w.mu.Lock()
	now := time.Now()
	if cur, ok := w.pending[d.Path]; ok {
		d.Recursive = d.Recursive || cur.Recursive
		d.Sidecars = d.Sidecars || cur.Sidecars
	}
	if len(w.pending) == 0 {
		w.first = now
	}
	w.pending[d.Path] = d
	w.last = now
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// scanLoop scans the pending set whenever it is due. It is the only caller of
// ScanDirs, so a slow scan never delays the reading of events.
func (w *Watcher) scanLoop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var failDelay time.Duration // after scans that could not run
	for {
		batch, next := w.take(time.Now())
		if len(batch) > 0 {
			res, err := w.sc.ScanDirs(ctx, batch)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				w.log.Warn("scan after file changes", "dirs", len(batch), "err", err)
			}
			if errors.Is(err, library.ErrNotScanned) {
				// Nothing was looked at (the library could not be read): try the
				// same directories again later, waiting longer each time.
				failDelay = min(max(2*failDelay, w.retry), maxScanBackoff)
				w.deferFor(batch, failDelay)
			} else {
				failDelay = 0
			}
			w.deferDirs(res.Deferred)
			continue
		}
		var timeout <-chan time.Time
		if !next.IsZero() {
			timer.Reset(time.Until(next))
			timeout = timer.C
		} else {
			timer.Stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-timeout:
		}
	}
}

// take returns the whole pending set if it is due at now, emptying it. If it
// is not, next is when to look again (zero: when something is queued).
func (w *Watcher) take(now time.Time) (batch []library.Dir, next time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sooner := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	for p, e := range w.deferred {
		if now.Before(e.due) {
			sooner(e.due)
			continue
		}
		// Its files have had time to settle: scan it now, with whatever else
		// is pending.
		if cur, ok := w.pending[p]; ok {
			e.dir.Recursive = e.dir.Recursive || cur.Recursive
			e.dir.Sidecars = e.dir.Sidecars || cur.Sidecars
		}
		w.pending[p] = e.dir
		w.ready = true
		delete(w.deferred, p)
	}
	if len(w.pending) == 0 {
		return nil, next
	}
	due := w.last.Add(w.quiet)
	if limit := w.first.Add(w.maxWait); limit.Before(due) {
		due = limit
	}
	if !w.ready && now.Before(due) {
		sooner(due)
		return nil, next
	}
	for _, d := range w.pending {
		batch = append(batch, d)
	}
	slices.SortFunc(batch, func(a, b library.Dir) int { return strings.Compare(a.Path, b.Path) })
	clear(w.pending)
	w.ready = false
	return batch, time.Time{}
}

// Backoff caps: retrying a root whose watches failed, and a partial scan that
// could not run.
const (
	maxRootBackoff = time.Hour
	maxScanBackoff = 5 * time.Minute
)

// deferDirs schedules dirs, which had files still settling, for another scan.
func (w *Watcher) deferDirs(dirs []library.Dir) { w.deferFor(dirs, w.retry) }

// deferFor schedules dirs for another scan after delay, merging their flags
// with any already scheduled.
func (w *Watcher) deferFor(dirs []library.Dir, delay time.Duration) {
	if len(dirs) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	due := time.Now().Add(delay)
	for _, d := range dirs {
		if cur, ok := w.deferred[d.Path]; ok {
			d.Recursive = d.Recursive || cur.dir.Recursive
			d.Sidecars = d.Sidecars || cur.dir.Sidecars
		}
		w.deferred[d.Path] = deferredDir{dir: d, due: due}
	}
}
