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
	Dirs     int    `json:"dirs"` // directories watched
	Error    string `json:"error,omitempty"`
}

// File kinds, by extension, that matter to the scanner. The subtitle list is
// the one in library/sidecar.go.
var (
	subtitleExts = map[string]bool{".srt": true, ".vtt": true, ".ass": true}
	metadataExts = map[string]bool{".nfo": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".tbn": true}
)

const rootNotFound = "root not found"

// errTooManyDirs is why a root is not even tried: it has more directories than
// there are descriptors to watch them with.
var errTooManyDirs = errors.New("not watching, too many directories")

type root struct {
	path string // configured spelling: absolute and clean

	// Run's goroutine only:
	info     os.FileInfo // the root when it was watched; nil when it was not found
	missing  bool        // not found (or unreadable) at the last look: look again
	logged   bool        // the watch failure was logged; stay quiet until it works
	hopeless bool        // too many directories: looking again can only give the same answer
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
	recheck time.Duration // between looks at the roots themselves
	maxDirs int           // directories the process may watch; <0 is no limit

	mkBackend func() (backend, error)

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
}

// New returns a watcher for roots, which must be absolute and clean, as the
// scanner has them. Nothing happens until Run.
func New(sc Scanner, roots []string, log *slog.Logger) *Watcher {
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
		dirs:      map[string]int{},
		snaps:     map[string]map[string]stamp{},
		pending:   map[string]library.Dir{},
		deferred:  map[string]deferredDir{},
		wake:      make(chan struct{}, 1),
	}
	for _, p := range roots {
		w.roots = append(w.roots, &root{path: p})
		w.status = append(w.status, RootStatus{Root: p})
	}
	return w
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
	be, err := w.mkBackend()
	if err != nil {
		w.log.Error("file watching is off; relying on periodic scans", "err", err)
		for i := range w.roots {
			w.setStatus(i, false, "cannot watch: "+err.Error())
		}
		<-ctx.Done()
		return
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
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// The backend broke (it only closes its channels in close).
				w.log.Error("file watching stopped; relying on periodic scans")
				for i, r := range w.roots {
					w.dropTree(r.path)
					w.setStatus(i, false, "file watcher stopped")
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

// snapshot records the files of d, for a backend without names.
func (w *Watcher) snapshot(d string) {
	if !w.unnamed {
		return
	}
	if entries, err := os.ReadDir(d); err == nil {
		w.snaps[d] = listing(entries)
	}
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
		if r.missing || (!w.watching(i) && !r.hopeless) || !os.SameFile(r.info, fi) {
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
	if err := w.addTree(ctx, r.path, i, false); err != nil {
		if ctx.Err() == nil {
			w.failRoot(i, err)
		}
		return false
	}
	r.logged = false
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
	dirs, err := w.listDirs(ctx, top, all)
	if err != nil {
		return err
	}
	if w.maxDirs >= 0 && len(w.dirs)+len(dirs) > w.maxDirs {
		return fmt.Errorf("%w: %d directories to watch, more than the %d this process can",
			errTooManyDirs, len(w.dirs)+len(dirs), w.maxDirs)
	}
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.be.add(d); err != nil {
			if d != w.roots[i].path && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)) {
				w.log.Debug("directory not watched", "dir", d, "err", err) // gone, or private
				continue
			}
			return err
		}
		w.dirs[d] = i
		w.snapshot(d)
		w.adjust(i, 1)
	}
	return nil
}

// listDirs lists top and the unwatched directories below it, spelled under
// top. It walks the symlink-resolved top: WalkDir does not follow a symlink
// at its root.
func (w *Watcher) listDirs(ctx context.Context, top string, all bool) ([]string, error) {
	resolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // vanished
		}
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == resolved {
				return err
			}
			return nil // unreadable or gone: its directory is listed, its children are not
		}
		if !d.IsDir() {
			return nil
		}
		if path != resolved && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		spelled := filepath.Join(top, rel)
		if _, watched := w.dirs[spelled]; watched {
			if all {
				return nil
			}
			return filepath.SkipDir // its own events cover what is below
		}
		out = append(out, spelled)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	return out, err
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

// deferDirs schedules dirs, which had files still settling, for another scan.
func (w *Watcher) deferDirs(dirs []library.Dir) {
	if len(dirs) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	due := time.Now().Add(w.retry)
	for _, d := range dirs {
		w.deferred[d.Path] = deferredDir{dir: d, due: due}
	}
}
