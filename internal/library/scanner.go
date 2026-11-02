// Package library scans media roots into the store and parses file names.
package library

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
	"time"

	"ply/internal/probe"
	"ply/internal/store"
)

// ErrRunning is returned by Scan when another full scan is in progress.
var ErrRunning = errors.New("scan already running")

const defaultWorkers = 4

// DefaultSettle is the settle time the server uses: a file modified more
// recently than this is left alone (a copy may still be in progress) and
// picked up by a later scan.
const DefaultSettle = 5 * time.Second

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
	".webm": true, ".avi": true, ".ts": true, ".m2ts": true,
}

// IsVideoFile reports whether name has a known video extension
// (case-insensitive).
func IsVideoFile(name string) bool {
	return videoExts[strings.ToLower(filepath.Ext(name))]
}

// Prober extracts stream and container information from a file.
type Prober interface {
	Probe(ctx context.Context, path string) (probe.Result, error)
}

// Status is the scanner state reported by GET /api/scan.
type Status struct {
	Running      bool       `json:"running"`
	LastStarted  *time.Time `json:"lastStarted"`
	LastFinished *time.Time `json:"lastFinished"`
	LastError    string     `json:"lastError"`
	ItemCount    int        `json:"itemCount"`
}

// Scanner keeps the store in sync with the media roots.
type Scanner struct {
	ctx     context.Context // bounds background scans started by Trigger
	store   *store.Store
	prober  Prober
	roots   []string
	workers int
	log     *slog.Logger
	settle  time.Duration
	now     func() time.Time

	sem      chan struct{} // one pass at a time, full or partial
	full     atomic.Bool   // a full scan is running or waiting for its turn
	wg       sync.WaitGroup
	fpCursor int64 // fingerprint backfill: the last item id done (guarded by sem)

	mu     sync.Mutex // guards status
	status Status

	hookMu     sync.Mutex
	onAdded    func(ids []int64)
	onSidecars func(ids []int64)
}

// NewScanner returns a scanner for roots, which must be absolute and clean.
// ctx bounds scans started by Trigger. The settle time starts at zero (files
// are indexed however recently they changed); the server sets DefaultSettle.
func NewScanner(ctx context.Context, st *store.Store, p Prober, roots []string, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Scanner{ctx: ctx, store: st, prober: p, roots: roots, workers: defaultWorkers, log: log,
		now: time.Now, sem: make(chan struct{}, 1)}
}

// SetSettle sets how long a new or changed file must go unmodified before it
// is indexed. Files that are too fresh are skipped (their old row, if any, is
// kept) and ScanDirs reports their directories in Result.Deferred. Call it
// before the first scan.
func (s *Scanner) SetSettle(d time.Duration) { s.settle = d }

// OnAdded registers fn to be called after each scan that created items or
// moved existing ones to a new path, with their ids. fn runs on the scanning
// goroutine: it must return quickly and must not start a scan.
func (s *Scanner) OnAdded(fn func(ids []int64)) {
	s.hookMu.Lock()
	s.onAdded = fn
	s.hookMu.Unlock()
}

// OnSidecarsChanged registers fn to be called after a ScanDirs whose Dirs
// carried Sidecars, with the ids of the items under those directories (items
// reported to OnAdded in the same scan are left out). Same rules as OnAdded.
func (s *Scanner) OnSidecarsChanged(fn func(ids []int64)) {
	s.hookMu.Lock()
	s.onSidecars = fn
	s.hookMu.Unlock()
}

func (s *Scanner) notify(added, sidecars []int64) {
	s.hookMu.Lock()
	onAdded, onSidecars := s.onAdded, s.onSidecars
	s.hookMu.Unlock()
	if len(added) > 0 && onAdded != nil {
		onAdded(added)
	}
	if len(sidecars) > 0 && onSidecars != nil {
		onSidecars(sidecars)
	}
}

func (s *Scanner) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scanner) release() { <-s.sem }

// Trigger starts a full scan in the background. It reports false, without
// starting anything, when a full scan is already running or queued. A partial
// scan in progress only delays it.
func (s *Scanner) Trigger() bool {
	if !s.full.CompareAndSwap(false, true) {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.full.Store(false)
		if err := s.acquire(s.ctx); err != nil {
			return
		}
		defer s.release()
		s.logResult(s.run(s.ctx))
	}()
	return true
}

// Scan runs a full scan and waits for it. It returns ErrRunning if another
// full scan is in progress; a partial scan in progress only delays it.
func (s *Scanner) Scan(ctx context.Context) error {
	if !s.full.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer s.full.Store(false)
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	return s.run(ctx)
}

// Run scans once immediately and then every interval until ctx is done.
// A non-positive interval disables the periodic rescan.
func (s *Scanner) Run(ctx context.Context, interval time.Duration) {
	scan := func() {
		if err := s.Scan(ctx); !errors.Is(err, ErrRunning) {
			s.logResult(err)
		}
	}
	scan()
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}

// Wait blocks until scans started by Trigger have finished.
func (s *Scanner) Wait() { s.wg.Wait() }

// Status returns the current scanner state.
func (s *Scanner) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := s.status
	s.mu.Unlock()
	if n, err := s.store.CountItems(ctx); err == nil {
		st.ItemCount = n
	}
	return st
}

func (s *Scanner) logResult(err error) {
	if err != nil {
		s.log.Warn("scan finished with errors", "err", err)
	}
}

func (s *Scanner) run(ctx context.Context) error {
	start := time.Now()
	s.mu.Lock()
	s.status.Running = true
	s.status.LastStarted = &start
	s.mu.Unlock()

	var (
		p   *pass
		err error
	)
	states, err := s.store.FileStates(ctx)
	if err != nil {
		err = fmt.Errorf("load library state: %w", err)
	} else {
		p = s.newPass(states)
		err = s.scanAll(ctx, p)
	}

	end := time.Now()
	s.mu.Lock()
	s.status.Running = false
	s.status.LastFinished = &end
	s.status.LastError = ""
	if err != nil {
		s.status.LastError = err.Error()
	}
	s.mu.Unlock()

	if p != nil {
		s.log.Info("scan complete", "indexed", p.sum.indexed.Load(), "moved", p.sum.moved,
			"removed", p.sum.removed.Load(), "duration", end.Sub(start).Round(time.Millisecond))
		s.notify(p.added, nil)
	}
	return err
}

type summary struct {
	indexed atomic.Int64
	removed atomic.Int64
	moved   int
}

// pass is one full or partial scan. It walks first, then pairs moved files
// with their rows, then probes what is new or changed, then removes rows whose
// files are gone, so a file moved between two walked directories keeps its id.
type pass struct {
	s      *Scanner
	states map[string]store.FileState // the library, kept up to date as rows move
	owners map[string]string          // file -> the configured root that indexes it
	now    time.Time

	seen     map[string]struct{} // every video file found, whichever root owns it
	seenBy   map[string]int      // root -> files it owns that were found
	work     []job               // new and changed files to probe
	deferred map[string]struct{} // directories holding files that are still settling
	listing  dirNames
	added    []int64
	sum      summary

	available map[string]bool // root -> mounted and non-empty (move pairing)
	failed    map[string]bool // roots that did not scan cleanly
}

func (s *Scanner) newPass(states map[string]store.FileState) *pass {
	p := &pass{s: s, states: states, owners: map[string]string{}, now: s.now(),
		seen: map[string]struct{}{}, seenBy: map[string]int{}, deferred: map[string]struct{}{},
		available: map[string]bool{}, failed: map[string]bool{}}
	// Which configured root indexes which file. Roots may nest (/m and
	// /m/movies): the root that already has the row keeps it, otherwise the
	// first one to reach the file takes it, so a file is probed once and does
	// not move between roots from scan to scan.
	for path, st := range states {
		if s.configured(st.Root) && within(st.Root, path) {
			p.owners[path] = st.Root
		}
	}
	return p
}

func (s *Scanner) scanAll(ctx context.Context, p *pass) error {
	var (
		errs   []error
		walked []string // roots whose walk was clean: eligible for removals
	)
	for _, root := range s.roots {
		if ctx.Err() != nil {
			break
		}
		walkErrors, err := p.walkRoot(ctx, root)
		switch {
		case err != nil:
			errs = append(errs, err)
			p.failed[root] = true
		case walkErrors > 0:
			errs = append(errs, fmt.Errorf("root %s: %d errors while walking; removals skipped", root, walkErrors))
			p.failed[root] = true
		default:
			walked = append(walked, root)
		}
	}
	if ctx.Err() == nil {
		errs = append(errs, p.pairMoves(ctx))
	}
	errs = append(errs, p.indexAll(ctx)...)
	if err := ctx.Err(); err != nil {
		if !errors.Is(errors.Join(errs...), err) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	for _, root := range walked {
		if n := p.rowsOf(root); p.seenBy[root] == 0 && n > 0 {
			// Looks like an unmounted volume: its mountpoint is an empty directory.
			errs = append(errs, fmt.Errorf("root %s: no video files found, but %d indexed items came from it; "+
				"refusing to remove them (is the volume mounted?)", root, n))
			p.failed[root] = true
			continue
		}
		if err := p.removeMissing(ctx, func(path string, st store.FileState) bool { return st.Root == root }); err != nil {
			errs = append(errs, fmt.Errorf("root %s: %w", root, err))
			p.failed[root] = true
		}
	}
	var failed []string
	for _, root := range s.roots {
		if p.failed[root] {
			failed = append(failed, root)
		}
	}
	errs = append(errs, s.removeOrphans(ctx, p.states, failed, &p.sum))
	p.backfill(ctx)
	return errors.Join(errs...)
}

func (s *Scanner) configured(root string) bool { return slices.Contains(s.roots, root) }

// within reports whether path is inside root.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// removeOrphans drops the rows of roots that are no longer configured (and
// were not taken over by a configured root that contains the file), but only
// those whose file is positively gone: an item that can still be reached stays
// until somebody decides otherwise. Files inside a root that failed to scan
// are left alone too: that root may be the one about to take them over.
func (s *Scanner) removeOrphans(ctx context.Context, states map[string]store.FileState, failed []string,
	sum *summary) error {
	var gone []int64
	for path, st := range states {
		if s.configured(st.Root) || slices.ContainsFunc(failed, func(r string) bool { return within(r, path) }) {
			continue
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, st.ID)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := s.store.DeleteItems(ctx, gone); err != nil {
		return fmt.Errorf("remove items of unconfigured roots: %w", err)
	}
	sum.removed.Add(int64(len(gone)))
	s.log.Info("removed missing files of roots that are no longer configured", "count", len(gone))
	return nil
}

type job struct {
	path, root string
	size       int64
	mtime      int64
	subs       []store.ExternalSubtitle // sidecar subtitles found next to path
	isNew      bool                     // no row has this path yet
	fp         string                   // content fingerprint, if already read
}

// walkRoot walks one configured root. It fails, so that nothing of the root is
// removed, when the root is missing or is not a directory.
func (p *pass) walkRoot(ctx context.Context, root string) (walkErrors int, err error) {
	fi, err := os.Stat(root)
	if err != nil {
		return 0, fmt.Errorf("root %s: %w", root, err)
	}
	if !fi.IsDir() {
		return 0, fmt.Errorf("root %s: not a directory", root)
	}
	walkErrors, err = p.walk(ctx, root, true, func(string) string { return root })
	if err != nil {
		return walkErrors, fmt.Errorf("root %s: walk: %w", root, err)
	}
	return walkErrors, nil
}

// walk visits the video files in dir (and below it if recursive). dir is
// spelled as configured; WalkDir does not descend into a directory that is a
// symlink, so the real directory is walked and the paths (and so the rows) keep
// the configured spelling. claimant names the root that would index a file
// nobody owns yet. A walk error makes the walked directory ineligible for
// removals; it is counted, logged and otherwise skipped.
func (p *pass) walk(ctx context.Context, dir string, recursive bool, claimant func(path string) string) (int, error) {
	walkDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		p.s.log.Warn("walk error", "path", dir, "err", err)
		return 1, nil
	}
	walkErrors := 0
	err = filepath.WalkDir(walkDir, func(walked string, d fs.DirEntry, err error) error {
		if err != nil {
			walkErrors++
			p.s.log.Warn("walk error", "path", walked, "err", err)
			return nil // keep going, but the directory is now ineligible for removals
		}
		if walked == walkDir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if !recursive {
				return fs.SkipDir
			}
			return nil
		}
		if !IsVideoFile(d.Name()) {
			return nil
		}
		path := walked
		if walkDir != dir {
			rel, err := filepath.Rel(walkDir, walked)
			if err != nil {
				walkErrors++
				p.s.log.Warn("walk error", "path", walked, "err", err)
				return nil
			}
			path = filepath.Join(dir, rel)
		}
		if err := p.visit(ctx, path, d, claimant); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			walkErrors++
		}
		return nil
	})
	return walkErrors, err
}

// visit records one video file: unchanged files get their root and subtitles
// brought up to date, new and changed files are queued for probing unless they
// are still settling. An error is a walk error.
func (p *pass) visit(ctx context.Context, path string, d fs.DirEntry, claimant func(path string) string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path) // follows file symlinks
	if err != nil {
		switch {
		case !errors.Is(err, fs.ErrNotExist):
			p.s.log.Warn("stat error", "path", path, "err", err)
			return err
		case d.Type()&fs.ModeSymlink != 0:
			// A dangling link is not a raced delete: in a symlink farm it is
			// what an unmounted target drive looks like. One that never
			// made it into the library is just noise.
			if _, indexed := p.states[path]; indexed {
				p.s.log.Warn("indexed file is now a dangling symlink", "path", path)
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	p.seen[path] = struct{}{}
	root, ok := p.owners[path]
	if !ok {
		root = claimant(path)
		if root == "" {
			return nil // no configured root indexes this file
		}
		p.owners[path] = root
	}
	if root != claimant(path) {
		return nil // another configured root (a nested one) indexes this file
	}
	p.seenBy[root]++
	mtime := info.ModTime().UnixNano()
	subs := sidecars(path, p.listing.list(filepath.Dir(path)))
	st, indexed := p.states[path]
	if indexed && st.Size == info.Size() && st.MTime == mtime {
		if st.Root != root {
			// Indexed under a root that is gone from the configuration.
			if err := p.s.store.SetItemRoot(ctx, st.ID, root); err != nil {
				p.s.log.Warn("move item to its new root", "path", path, "err", err)
			} else {
				st.Root = root
				p.states[path] = st
			}
		}
		// The video is unchanged, but subtitles come and go on their own.
		if !slices.Equal(st.Subs, subs) {
			if err := p.s.store.SetExternalSubtitles(ctx, st.ID, subs); err != nil {
				p.s.log.Warn("update subtitles", "path", path, "err", err)
			} else {
				st.Subs = subs
				p.states[path] = st
			}
		}
		return nil
	}
	if p.s.settle > 0 && p.now.Sub(info.ModTime()) < p.s.settle {
		// Still being written (or just finished): a later scan takes it. Its
		// old row, if any, stays: the file is there.
		p.deferred[filepath.Dir(path)] = struct{}{}
		return nil
	}
	p.work = append(p.work, job{path: path, root: root, size: info.Size(), mtime: mtime, subs: subs, isNew: !indexed})
	return nil
}

// rowsOf counts the rows that belong to root.
func (p *pass) rowsOf(root string) int {
	n := 0
	for _, st := range p.states {
		if st.Root == root {
			n++
		}
	}
	return n
}

// removeMissing deletes the rows selected by in whose files were not seen.
func (p *pass) removeMissing(ctx context.Context, in func(path string, st store.FileState) bool) error {
	var (
		gone  []int64
		paths []string
	)
	for path, st := range p.states {
		if !in(path, st) {
			continue
		}
		if _, ok := p.seen[path]; !ok {
			gone = append(gone, st.ID)
			paths = append(paths, path)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := p.s.store.DeleteItems(ctx, gone); err != nil {
		return fmt.Errorf("remove missing items: %w", err)
	}
	for _, path := range paths {
		delete(p.states, path)
	}
	p.sum.removed.Add(int64(len(gone)))
	p.s.log.Info("removed missing files", "count", len(gone))
	return nil
}

// pairMoves finds new files that are existing items moved or renamed, on a
// root that is mounted. The row takes the new path and keeps its id (and with
// it progress, matches and edits); the file is not probed again. A pair is the
// only new file with a given size and mtime and the only row with them whose
// file is gone; failing that (the move did not keep the mtime), the only new
// file and the only gone row with a given size and content fingerprint.
func (p *pass) pairMoves(ctx context.Context) error {
	type key struct{ size, mtime int64 }
	fresh := map[key][]int{} // new files by key, as indexes into p.work
	for i, j := range p.work {
		if j.isNew {
			k := key{j.size, j.mtime}
			fresh[k] = append(fresh[k], i)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	rows := map[key][]string{}
	for path, st := range p.states {
		k := key{st.Size, st.MTime}
		if _, ok := fresh[k]; ok {
			rows[k] = append(rows[k], path)
		}
	}
	moved := map[int]bool{}
	var errs []error
	for k, idx := range fresh {
		if len(idx) != 1 {
			continue
		}
		if gone := p.gone(rows[k]); len(gone) == 1 {
			if err := p.move(ctx, idx[0], gone[0]); err != nil {
				errs = append(errs, err)
			} else {
				moved[idx[0]] = true
			}
		}
	}
	errs = append(errs, p.pairByFingerprint(ctx, moved))
	if len(moved) > 0 {
		work := p.work[:0]
		for i, j := range p.work {
			if !moved[i] {
				work = append(work, j)
			}
		}
		p.work = work
	}
	return errors.Join(errs...)
}

// pairByFingerprint is the fallback of pairMoves for new files that are not
// paired yet. A fingerprint is read only for a file whose size some gone row
// has.
func (p *pass) pairByFingerprint(ctx context.Context, moved map[int]bool) error {
	unseen := map[int64]bool{} // sizes of rows whose files were not seen
	for path, st := range p.states {
		if _, ok := p.seen[path]; !ok {
			unseen[st.Size] = true
		}
	}
	type key struct {
		size int64
		fp   string
	}
	fresh := map[key][]int{}
	for i, j := range p.work {
		if !j.isNew || moved[i] || !unseen[j.size] {
			continue
		}
		fp, err := fingerprint(j.path)
		if err != nil {
			p.s.log.Warn("fingerprint", "path", j.path, "err", err)
			continue
		}
		p.work[i].fp = fp
		k := key{j.size, fp}
		fresh[k] = append(fresh[k], i)
	}
	var errs []error
	for k, idx := range fresh {
		if len(idx) != 1 {
			continue
		}
		paths, err := p.s.store.PathsWithFingerprint(ctx, k.size, k.fp)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var known []string
		for _, old := range paths {
			if _, ok := p.states[old]; ok { // not moved or removed in this pass
				known = append(known, old)
			}
		}
		if gone := p.gone(known); len(gone) == 1 {
			if err := p.move(ctx, idx[0], gone[0]); err != nil {
				errs = append(errs, err)
			} else {
				moved[idx[0]] = true
			}
		}
	}
	return errors.Join(errs...)
}

// gone returns the paths among candidates whose files are known to be gone:
// not seen in this pass, missing on disk, on a root that is mounted.
func (p *pass) gone(candidates []string) []string {
	var out []string
	for _, old := range candidates {
		if _, ok := p.seen[old]; ok {
			continue
		}
		if !p.rootAvailable(p.states[old].Root) {
			continue // its drive may just be unmounted: the file is not known to be gone
		}
		if _, err := os.Lstat(old); errors.Is(err, fs.ErrNotExist) {
			out = append(out, old)
		}
	}
	return out
}

// move gives the row of old the new file of p.work[i].
func (p *pass) move(ctx context.Context, i int, old string) error {
	j := p.work[i]
	st := p.states[old]
	it := itemFor(j)
	if err := p.s.store.MoveItem(ctx, st.ID, &it); err != nil {
		return fmt.Errorf("move %s to %s: %w", old, j.path, err) // indexed as a new file instead
	}
	if !slices.Equal(st.Subs, j.subs) {
		if err := p.s.store.SetExternalSubtitles(ctx, st.ID, j.subs); err != nil {
			p.s.log.Warn("update subtitles", "path", j.path, "err", err)
		}
	}
	p.s.log.Info("file moved", "from", old, "to", j.path, "id", st.ID)
	delete(p.states, old)
	st.Root, st.Subs, st.MTime = j.root, j.subs, j.mtime
	p.states[j.path] = st
	p.added = append(p.added, st.ID)
	p.sum.moved++
	return nil
}

// fingerprintBackfill is how many items indexed before fingerprints existed a
// full scan fingerprints, so that a big library catches up over a few scans
// without one long burst of reads.
const fingerprintBackfill = 200

// backfill fingerprints some items that have none, carrying on where the last
// full scan stopped.
func (p *pass) backfill(ctx context.Context) {
	list, err := p.s.store.WithoutFingerprint(ctx, p.s.fpCursor, fingerprintBackfill)
	if err != nil {
		p.s.log.Warn("list items without a fingerprint", "err", err)
		return
	}
	if len(list) < fingerprintBackfill {
		p.s.fpCursor = 0 // wrapped around: next time, retry the ones that failed
	} else {
		p.s.fpCursor = list[len(list)-1].ID
	}
	for _, u := range list {
		if ctx.Err() != nil {
			return
		}
		info, err := os.Stat(u.Path)
		if err != nil || info.Size() != u.Size || info.ModTime().UnixNano() != u.MTime {
			continue // changed or gone: the next scan indexes it, with a fingerprint
		}
		fp, err := fingerprint(u.Path)
		if err != nil {
			continue
		}
		if err := p.s.store.SetFingerprint(ctx, u.ID, u.Size, u.MTime, fp); err != nil {
			p.s.log.Warn("store fingerprint", "path", u.Path, "err", err)
		}
	}
}

// rootAvailable reports whether root is a directory with something in it:
// a missing or empty root is what an unmounted volume looks like.
func (p *pass) rootAvailable(root string) bool {
	ok, known := p.available[root]
	if !known {
		f, err := os.Open(root)
		if err == nil {
			names, _ := f.Readdirnames(1)
			ok = len(names) > 0
			f.Close()
		}
		p.available[root] = ok
	}
	return ok
}

// indexAll probes the queued files and upserts them, s.workers at a time.
func (p *pass) indexAll(ctx context.Context) []error {
	if len(p.work) == 0 {
		return nil
	}
	type failure struct {
		n     int
		first error
	}
	var (
		jobs     = make(chan job)
		wg       sync.WaitGroup
		mu       sync.Mutex // guards errs, failures and p.added
		errs     []error
		failures = map[string]*failure{} // root -> probe failures
	)
	for range p.s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				id, err := p.s.index(ctx, j)
				mu.Lock()
				switch {
				case err == nil:
					p.sum.indexed.Add(1)
					if j.isNew && id != 0 {
						p.added = append(p.added, id)
					}
				case isProbeError(err):
					f := failures[j.root]
					if f == nil {
						f = &failure{first: err}
						failures[j.root] = f
					}
					f.n++
				default:
					errs = append(errs, err)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, j := range p.work {
		select {
		case jobs <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	for _, root := range p.s.roots {
		if f := failures[root]; f != nil {
			errs = append(errs, fmt.Errorf("root %s: %d files could not be probed, first: %w", root, f.n, f.first))
			p.failed[root] = true
		}
	}
	return errs
}

type probeError struct{ err error }

func (e probeError) Error() string { return e.err.Error() }
func (e probeError) Unwrap() error { return e.err }

func isProbeError(err error) bool {
	var pe probeError
	return errors.As(err, &pe)
}

// itemFor is the row for j as far as its path tells: kind, names and numbers.
func itemFor(j job) store.Item {
	p := ParseName(j.path)
	return store.Item{
		Kind:    p.Kind,
		Title:   p.Title,
		Year:    p.Year,
		Show:    p.Show,
		Season:  p.Season,
		Episode: p.Episode,
		Path:    j.path,
		Root:    j.root,
		Size:    j.size,
		MTime:   j.mtime,
	}
}

// index probes one file and upserts it, returning its id. Files without a
// video stream are skipped silently (id 0).
func (s *Scanner) index(ctx context.Context, j job) (int64, error) {
	res, err := s.prober.Probe(ctx, j.path)
	if err != nil {
		s.log.Warn("probe failed", "path", j.path, "err", err)
		return 0, probeError{err}
	}
	if !hasVideo(res.Streams) {
		s.log.Info("skipping file without a video stream", "path", j.path)
		return 0, nil
	}
	it := itemFor(j)
	it.Container = res.Container
	it.DurationSec = res.DurationSec
	it.Bitrate = res.Bitrate
	it.Fingerprint = j.fp
	if it.Fingerprint == "" {
		if it.Fingerprint, err = fingerprint(j.path); err != nil {
			s.log.Warn("fingerprint", "path", j.path, "err", err) // only moves are worse off
		}
	}
	id, err := s.store.UpsertItem(ctx, &it, res.Streams)
	if err != nil {
		return 0, fmt.Errorf("store %s: %w", j.path, err)
	}
	if err := s.store.SetExternalSubtitles(ctx, id, j.subs); err != nil {
		s.log.Warn("store subtitles", "path", j.path, "err", err)
	}
	return id, nil
}

func hasVideo(streams []probe.Stream) bool {
	for _, st := range streams {
		if st.Type == "video" {
			return true
		}
	}
	return false
}
