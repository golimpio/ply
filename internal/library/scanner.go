// Package library scans media roots into the store and parses file names.
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
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

// Status is the scanner state reported by GET /api/scan. Running is true while any
// pass is running or queued on the scan semaphore: a full scan, the scan of a folder that
// was just added, the re-reading of a folder whose type changed (or of a first typed
// one), a partial scan. It is not true while the scan of a new folder sleeps before it
// looks again at files that were still being written. LastStarted, LastFinished and
// LastError are those of the full scan.
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
	workers int
	log     *slog.Logger
	settle  time.Duration
	now     func() time.Time

	// The media roots (see SetRoots). A pass works on a copy taken when it starts.
	rootMu   sync.Mutex // guards roots, managed, retired and lastScan
	roots    []Root
	managed  bool                 // SetRoots was called: untyped roots are guessed (see guessKind)
	retired  map[string]bool      // roots taken out whose items are yet to be dropped (see removeOrphans)
	lastScan map[string]time.Time // root -> when a scan last covered all of it
	due      map[string]*arrivals // typed root -> the rows it took over that are yet to be read again (guarded by rootMu)

	sem      chan struct{} // one pass at a time, full or partial; DropRoot and the re-parse take it too
	full     atomic.Bool   // a full scan is running or waiting for its turn
	fpCursor int64         // fingerprint backfill: the last item id done (guarded by sem)

	// The background work (see spawn). Nothing starts once the scanner is closed, so
	// that Wait, at shutdown, is never racing an Add from a request goroutine.
	lifeMu sync.Mutex
	closed bool
	wg     sync.WaitGroup

	// Seams the tests replace.
	fileStates func(ctx context.Context) (map[string]store.FileState, error) // Store.FileStates
	queued     func()                                                        // called when a pass has to wait for its turn

	devOf        func(path string) (dev uint64, ok bool) // device of a directory (tests replace it)
	mountMu      sync.Mutex
	mounts       map[string]bool // mount boundaries met so far, in configured spelling (persisted)
	mountsLoaded bool

	mu     sync.Mutex // guards status and busy
	status Status
	busy   int // passes and background work under way or waiting (see enter); Status.Running is busy > 0

	hookMu     sync.Mutex
	onAdded    func(ids []int64)
	onSidecars func(ids []int64)
	onRestore  func(itemID, tmdbID int64, kind string)
	onChanged  func()
	matchGate  func() bool                                      // nil, or says whether archived matches can be applied now
	gated      atomic.Bool                                      // a report was held back because the gate was closed
	pace       func(ctx context.Context, done, total int) error // see SetPace

	// restoreArchived gives a file's archive row back (Store.RestoreArchived); tests
	// replace it to count the calls.
	restoreArchived func(ctx context.Context, itemID int64, path string, size int64) (tmdbID int64, kind string, ok bool, err error)
}

// NewScanner returns a scanner for roots, which must be absolute and clean.
// They have no type yet: files are read as ParseName does, until SetRoots gives
// the folders their types. ctx bounds the scans the scanner starts itself
// (Trigger, SetRoots). The settle time starts at zero (files are indexed however
// recently they changed); the server sets DefaultSettle.
func NewScanner(ctx context.Context, st *store.Store, p Prober, roots []string, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Scanner{ctx: ctx, store: st, prober: p, workers: defaultWorkers, log: log,
		now: time.Now, sem: make(chan struct{}, 1), devOf: deviceOf, restoreArchived: st.RestoreArchived,
		fileStates: st.FileStates, retired: map[string]bool{}, lastScan: map[string]time.Time{}, due: map[string]*arrivals{}}
	for _, r := range roots {
		s.roots = append(s.roots, Root{Path: r})
	}
	return s
}

// SetSettle sets how long a new or changed file must go unmodified before it
// is indexed. Files that are too fresh are skipped (their old row, if any, is
// kept) and ScanDirs reports their directories in Result.Deferred. Call it
// before the first scan.
func (s *Scanner) SetSettle(d time.Duration) { s.settle = d }

// SetPace sets the function a pass calls before it probes each file (done of
// total files so far) and every pacePerDirs directories of a walk (0, 0). It
// blocks while the machine is needed for something more important (playback,
// browsing: the tier governor) and reports progress; an error, which only the
// end of ctx should cause, stops the pass as a cancellation would. nil (the
// default) never waits.
func (s *Scanner) SetPace(fn func(ctx context.Context, done, total int) error) {
	s.hookMu.Lock()
	s.pace = fn
	s.hookMu.Unlock()
}

// pacePerDirs is how many directories a walk reads between two calls of the pace
// function: reading a directory of a network share is cheap, but a library has thousands.
const pacePerDirs = 100

func (s *Scanner) paced(ctx context.Context, done, total int) error {
	s.hookMu.Lock()
	fn := s.pace
	s.hookMu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, done, total)
}

// OnAdded registers fn to be called after each scan that created items or
// moved existing ones to a new path, with their ids, and after items were read
// again because their folder got a type (SetRoots, the guess): they are pending
// again and want matching. fn runs on the scanning goroutine: it must return
// quickly and must not start a scan.
func (s *Scanner) OnAdded(fn func(ids []int64)) {
	s.hookMu.Lock()
	s.onAdded = fn
	s.hookMu.Unlock()
}

// OnSidecarsChanged registers fn to be called with the ids of items whose
// sidecar files (.nfo, artwork) may have changed: after a ScanDirs whose Dirs
// carried Sidecars, the items under those directories (other than the ones
// just created); after any scan, the items that moved, since their sidecars
// are wherever they moved to. Same rules as OnAdded.
func (s *Scanner) OnSidecarsChanged(fn func(ids []int64)) {
	s.hookMu.Lock()
	s.onSidecars = fn
	s.hookMu.Unlock()
}

// OnRestoreMatch registers fn to be called, after each scan and after items were
// read again, for every manual match the archive of a removed folder (or of a
// type change) keeps for an item that is in the library and has no manual match
// yet: the item, the TMDB id the person had chosen, and the kind of that title
// (store.KindMovie, or store.KindSeries for the item's show). The progress is
// already back; applying the match is up to fn (the worker's MatchMovie or
// MatchShow), and when it has, it calls Store.ForgetArchivedMatch with the item's
// path. One that is not forgotten (no TMDB key, no network) is reported again
// after the next scan, so a match is never lost for want of an answer. Same rules
// as OnAdded; a show with many files is reported once per file.
func (s *Scanner) OnRestoreMatch(fn func(itemID, tmdbID int64, kind string)) {
	s.hookMu.Lock()
	s.onRestore = fn
	s.hookMu.Unlock()
}

// OnChanged registers fn to be called after each scan, full or partial, that
// indexed, moved, removed or read again any file, and after items were read
// again because their folder got a type: what the library shows has changed.
// Same rules as OnAdded.
func (s *Scanner) OnChanged(fn func()) {
	s.hookMu.Lock()
	s.onChanged = fn
	s.hookMu.Unlock()
}

// changed calls the OnChanged hook if p changed anything.
func (s *Scanner) changed(p *pass) {
	if p.sum.indexed.Load() > 0 || p.sum.moved > 0 || p.sum.removed.Load() > 0 || len(p.reparsed) > 0 {
		s.fireChanged()
	}
}

func (s *Scanner) fireChanged() {
	s.hookMu.Lock()
	fn := s.onChanged
	s.hookMu.Unlock()
	if fn != nil {
		fn()
	}
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

// SetMatchGate sets a function that says whether the archived matches can be
// applied now (online lookups are on). While it says no, OnRestoreMatch hears of
// nothing: there is nothing it could do, and the matches wait in the archive. Call
// ReportMatchesIfDue now and then to report them once it says yes.
func (s *Scanner) SetMatchGate(ok func() bool) {
	s.hookMu.Lock()
	s.matchGate = ok
	s.hookMu.Unlock()
}

// ReportMatchesIfDue reports the archived matches that a closed gate held back, if
// it is open now. It is cheap when nothing was held back.
func (s *Scanner) ReportMatchesIfDue(ctx context.Context) {
	if s.gated.Load() {
		s.gated.Store(false)
		s.reportMatches(ctx) // puts the flag back if the gate is still closed
	}
}

// reportMatches tells the OnRestoreMatch hook about the archived manual matches
// that are due (Store.PendingMatches). Run at the end of every pass and of every
// re-reading, it is what keeps a match from being lost: restoring a file does
// not use its match up, only applying it does.
func (s *Scanner) reportMatches(ctx context.Context) {
	s.hookMu.Lock()
	fn, gate := s.onRestore, s.matchGate
	s.hookMu.Unlock()
	if fn == nil || ctx.Err() != nil {
		return
	}
	if gate != nil && !gate() {
		s.gated.Store(true) // ReportMatchesIfDue reports them when the gate opens
		return
	}
	list, err := s.store.PendingMatches(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("listing the manual matches waiting to be applied", "err", err)
		}
		return
	}
	for _, m := range list {
		fn(m.ItemID, m.TMDBID, m.Kind)
	}
}

func (s *Scanner) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err // an ended context gets no turn, however free the semaphore is
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	default:
	}
	if s.queued != nil {
		s.queued() // a pass is running: this one waits for its turn
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enter and leave count what is under way, each pass and each piece of background work
// once, so that overlapping ones are handled: Status.Running is true until the last
// of them has left.
func (s *Scanner) enter() {
	s.mu.Lock()
	s.busy++
	s.mu.Unlock()
}

func (s *Scanner) leave() {
	s.mu.Lock()
	s.busy--
	s.mu.Unlock()
}

// spawn runs fn in the background, where Wait waits for it, and reports false, running
// nothing, once the scanner is closed (Close, or Wait after its context ended). The
// check and the WaitGroup's Add are one step under a mutex, so that a request goroutine
// putting a folder back cannot race the Wait of a shutdown. The work counts as running
// from the moment spawn returns, while it waits for its turn too: a caller that polls the
// status after adding a folder must not see an idle scanner before the scan has begun.
func (s *Scanner) spawn(fn func()) bool {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	s.enter()
	go func() {
		defer s.wg.Done()
		defer s.leave()
		fn()
	}()
	return true
}

func (s *Scanner) release() { <-s.sem }

// Trigger starts a full scan in the background. It reports false, without
// starting anything, when a full scan is already running or queued. A partial
// scan in progress only delays it.
func (s *Scanner) Trigger() bool {
	if !s.full.CompareAndSwap(false, true) {
		return false
	}
	if !s.spawn(func() {
		defer s.full.Store(false)
		if err := s.acquire(s.ctx); err != nil {
			return
		}
		defer s.release()
		s.logResult(s.run(s.ctx))
	}) {
		s.full.Store(false)
		return false
	}
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

// Wait blocks until the work the scanner started itself has finished: the scans
// started by Trigger, and what SetRoots started (the scan of a new folder, the
// re-reading of items). Once the scanner's context has ended it also closes the
// scanner, as Close does: this is the Wait of a shutdown.
func (s *Scanner) Wait() {
	if s.ctx.Err() != nil {
		s.Close()
		return
	}
	s.wg.Wait()
}

// Close stops the scanner from starting any more background work (Trigger, SetRoots, a
// folder put back, Reread then do nothing) and waits for what is running. The passes
// the caller runs itself (Scan, ScanDirs) are its own business.
func (s *Scanner) Close() {
	s.lifeMu.Lock()
	s.closed = true
	s.lifeMu.Unlock()
	s.wg.Wait()
}

// Busy is Status().Running without counting the items: whether a pass is under
// way or waiting for its turn. It is cheap enough to ask every second.
func (s *Scanner) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy > 0
}

// Status returns the current scanner state.
func (s *Scanner) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := s.status
	st.Running = s.busy > 0
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

func (s *Scanner) run(ctx context.Context) (err error) {
	start := time.Now()
	s.mu.Lock()
	s.busy++
	s.status.LastStarted = &start
	s.mu.Unlock()
	var end time.Time
	defer func() { // however the scan ends, it is not running any more
		if end.IsZero() {
			end = time.Now()
		}
		s.mu.Lock()
		s.busy-- // with LastFinished: whoever sees the scan idle sees it finished
		s.status.LastFinished = &end
		s.status.LastError = ""
		if err != nil {
			s.status.LastError = err.Error()
		}
		s.mu.Unlock()
	}()

	var p *pass
	states, err := s.fileStates(ctx)
	if err != nil {
		err = fmt.Errorf("load library state: %w", err)
	} else {
		s.loadMounts(ctx)
		roots, retired := s.snapshot()
		p = s.newPass(states, roots, retired)
		p.loadArchived(ctx)
		err = s.scanAll(ctx, p)
		p.saveMounts(ctx)
	}
	end = time.Now()

	if p != nil {
		s.log.Info("scan complete", "indexed", p.sum.indexed.Load(), "moved", p.sum.moved,
			"removed", p.sum.removed.Load(), "duration", end.Sub(start).Round(time.Millisecond))
		// A moved item's sidecar artwork is somewhere else now. Re-read items are
		// pending again, like new ones.
		s.notify(slices.Concat(p.added, p.reparsed), p.moved)
		s.changed(p)
		s.reportMatches(ctx)
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
	deferred map[string]bool     // directories to look at again soon (true: with their subdirectories)
	settling map[int64]bool      // sizes of the files left for later because they are still settling
	listing  dirNames
	added    []int64 // created or moved
	moved    []int64
	reparsed []int64 // read again after a folder got its type (the guess)
	sum      summary

	archived    map[string]bool // paths whose archive row holds progress to give back
	restoreOnly []restoreJob    // unchanged rows that have such an archive row
	// arrived are, by typed root, the items the pass found under a root that is gone from
	// the configuration and gave to it: read before types or under another type, they are
	// read again as the root's type says (rereadArrived). noted are the roots whose arrival
	// has been noted in the settings already (noteArrival).
	arrived map[string][]int64
	noted   map[string]bool
	// skipped are the roots whose files the pass left alone because they were taken out
	// (DropRoot is waiting for the pass). Their rows were not looked at: whatever became
	// of the root since, a file of it that was not seen is not a file that is gone.
	skipped map[string]bool

	roots   []Root            // the configured roots, as of the start of the pass
	kinds   map[string]string // root path -> its type ("" until a -media folder is guessed)
	retired map[string]bool   // see Scanner.retired

	available map[string]bool            // root -> mounted and non-empty (move pairing)
	failed    map[string]bool            // roots that did not scan cleanly
	names     map[string]map[string]bool // directory -> the exact names in it
	devs      map[string]uint64          // walked directory (real path) -> its device
	down      map[string]bool            // mount boundary -> it is not mounted now

	mountsChanged bool // a boundary was learned or forgotten: save them
}

// futureSkew is how far in the future a file's mtime may be and still be
// taken as a write in progress. Beyond it the clock is off (FAT and exFAT
// drives keep local time; a NAS clock drifts): such a file counts as settled,
// or it would never be indexed.
const futureSkew = time.Minute

// recheckWindow is how long after a file was last written a partial scan asks
// for its directory to be looked at again (see Result.Deferred): a copy that
// stalled for longer than the settle time may not be finished, and on darwin
// writes to a file make no directory event.
const recheckWindow = time.Minute

func (s *Scanner) newPass(states map[string]store.FileState, roots []Root, retired map[string]bool) *pass {
	kinds := make(map[string]string, len(roots))
	for _, r := range roots {
		kinds[r.Path] = r.Kind
	}
	p := &pass{s: s, states: states, owners: map[string]string{}, now: s.now(), roots: roots, kinds: kinds, retired: retired,
		archived: map[string]bool{}, skipped: map[string]bool{}, arrived: map[string][]int64{}, noted: map[string]bool{},
		seen: map[string]struct{}{}, seenBy: map[string]int{}, deferred: map[string]bool{},
		settling: map[int64]bool{}, available: map[string]bool{}, failed: map[string]bool{},
		names: map[string]map[string]bool{}, devs: map[string]uint64{}, down: map[string]bool{}}
	// Which configured root indexes which file. Roots may nest (/m and
	// /m/movies): the root that already has the row keeps it, otherwise the
	// first one to reach the file takes it, so a file is probed once and does
	// not move between roots from scan to scan.
	for path, st := range states {
		if p.configured(st.Root) && within(st.Root, path) {
			p.owners[path] = st.Root
		}
	}
	return p
}

// configured reports whether root is one of the roots this pass works on.
func (p *pass) configured(root string) bool {
	_, ok := p.kinds[root]
	return ok
}

// left reports whether the pass left the files of root alone: it was taken out
// (DropRoot is waiting), now or earlier in the pass. What the pass did not see of it
// says nothing about the files, so it removes nothing of it and notes no scan of it.
func (p *pass) left(root string) bool { return p.skipped[root] || p.s.rootRetired(root) }

func (s *Scanner) scanAll(ctx context.Context, p *pass) error {
	var (
		errs   []error
		walked []string // roots whose walk was clean: eligible for removals
	)
	for _, r := range p.roots {
		if ctx.Err() != nil {
			break
		}
		root := r.Path
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
	p.restoreExisting(ctx)
	errs = append(errs, p.rereadArrived(ctx)...)
	if err := ctx.Err(); err != nil {
		if !errors.Is(errors.Join(errs...), err) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	for _, root := range walked {
		if p.left(root) {
			continue // taken out meanwhile (DropRoot): the files it skipped are not gone
		}
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
	var (
		failed  []string
		scanned []string // roots seen whole, with nothing wrong
	)
	for _, r := range p.roots {
		if p.failed[r.Path] {
			failed = append(failed, r.Path)
		} else if slices.Contains(walked, r.Path) && !p.left(r.Path) {
			scanned = append(scanned, r.Path)
		}
	}
	errs = append(errs, p.removeOrphans(ctx, failed))
	errs = append(errs, p.finishRoots(ctx, scanned)...)
	p.backfill(ctx)
	return errors.Join(errs...)
}

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
//
// The rows of a root that was just taken out (p.retired: SetRoots or DropRoot
// did it, and DropRoot has yet to drop its items) are never removed here: DropRoot
// is the only thing that removes them, because it archives what a person made of
// them first, and a scan that gets in between must not undo that with files that
// happen to be gone (an unmounted disk, say).
func (p *pass) removeOrphans(ctx context.Context, failed []string) error {
	var gone []int64
	for path, st := range p.states {
		if p.configured(st.Root) || p.retired[st.Root] ||
			slices.ContainsFunc(failed, func(r string) bool { return within(r, path) }) {
			continue
		}
		// A file is gone when its disk is there and it is not: the rows of a share that is not
		// mounted now (the root they were indexed under, or a volume inside it) are kept, as a
		// scan of that root would keep them.
		if !p.rootAvailable(st.Root) || p.unmounted(path, st.Root) != "" {
			continue
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, st.ID)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := p.s.store.DeleteItems(ctx, gone); err != nil {
		return fmt.Errorf("remove items of unconfigured roots: %w", err)
	}
	p.sum.removed.Add(int64(len(gone)))
	p.s.log.Info("removed missing files of roots that are no longer configured", "count", len(gone))
	return nil
}

type job struct {
	path, root string
	kind       string // the root's type when the pass began
	size       int64
	mtime      int64
	subs       []store.ExternalSubtitle // sidecar subtitles found next to path
	isNew      bool                     // no row has this path yet
	archived   bool                     // the archive of a removed folder holds progress for this path
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
	walkErrors, dirs := 0, 0
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
			if dirs++; dirs%pacePerDirs == 0 {
				if err := p.s.paced(ctx, 0, 0); err != nil {
					return err
				}
			}
			if rel, err := filepath.Rel(walkDir, walked); err == nil {
				p.noteDir(walked, filepath.Join(dir, rel))
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
	if r := claimant(path); r != "" && p.s.rootRetired(r) {
		p.skipped[r] = true
		return nil // DropRoot is waiting for this pass: do not look at the folder any more
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
	age := p.now.Sub(info.ModTime()) // negative: written since the pass started, or a skewed clock
	if p.s.settle > 0 && age > -futureSkew && age < recheckWindow {
		p.recheck(filepath.Dir(path), false)
	}
	if indexed && st.Size == info.Size() && st.MTime == mtime {
		if st.Root != root {
			// Indexed under a root that is gone from the configuration.
			p.noteArrival(ctx, root)
			if err := p.s.store.SetItemRoot(ctx, st.ID, root); err != nil {
				p.s.log.Warn("move item to its new root", "path", path, "err", err)
			} else {
				st.Root = root
				p.states[path] = st
				p.arrived[root] = append(p.arrived[root], st.ID)
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
		if p.archived[path] {
			p.restoreOnly = append(p.restoreOnly, restoreJob{id: st.ID, path: path, size: info.Size()})
		}
		return nil
	}
	if p.s.settle > 0 && age > -futureSkew && age < p.s.settle {
		// Still being written (or just finished): a later scan takes it. Its
		// old row, if any, stays: the file is there. A gone row of the same
		// size stays too: this may be it, copied without its mtime.
		p.recheck(filepath.Dir(path), false)
		p.settling[info.Size()] = true
		return nil
	}
	if p.s.settle > 0 && age <= -futureSkew {
		// A skewed clock: its age says nothing. Look once more after indexing
		// it, so that a copy still growing is seen to change; once it stops,
		// it is unchanged and not looked at again.
		p.recheck(filepath.Dir(path), false)
	}
	if indexed && st.Root != root {
		p.noteArrival(ctx, root)
		p.arrived[root] = append(p.arrived[root], st.ID) // the file changed: indexing it moves the row
	}
	p.work = append(p.work, job{path: path, root: root, kind: p.kinds[root], size: info.Size(), mtime: mtime,
		subs: subs, isNew: !indexed, archived: p.archived[path]})
	return nil
}

// RereadNotePrefix starts the settings that note a typed folder whose items are to be
// read again as its type says: the command line's first typed folder (cmd/ply), and a
// folder that took over rows another root left (noteArrival). The server reads them at
// every start and has the scanner read the folders again (Reread), so that a start that
// ends before the re-reading is done does not lose it.
const RereadNotePrefix = "m7.reread."

// arrivalNote is the value of the notes the scanner makes itself, and clears itself. A
// note with another value was made by someone else, who clears it.
const arrivalNote = "arrived"

// arrivals are the rows a typed root took over that have yet to be read again. The
// scanner keeps them until they are (a pass that fails to read them is followed by
// another that tries again); the settings note is what outlives the process.
type arrivals struct {
	ids   map[int64]bool
	owned bool // the settings note is the scanner's to clear
}

// noteArrival notes, once per pass and root, that a typed root is about to take over rows:
// a settings note, written before the first row moves, that stays until they have been read
// again. A start after a pass that was cut short (shutdown, an error) reads the folder
// again from it. An untyped root takes rows over as they are, and notes nothing.
func (p *pass) noteArrival(ctx context.Context, root string) {
	if p.kinds[root] == "" || p.noted[root] {
		return
	}
	p.noted[root] = true
	s := p.s
	owned := false
	switch v, ok, err := s.store.GetSetting(ctx, RereadNotePrefix+root); {
	case err != nil:
		s.log.Warn("noting that a folder took over items", "folder", root, "err", err)
	case !ok:
		if err := s.store.SetSetting(ctx, RereadNotePrefix+root, arrivalNote); err != nil {
			s.log.Warn("noting that a folder took over items", "folder", root, "err", err)
		} else {
			owned = true
		}
	default:
		owned = v == arrivalNote // another note stands: whoever made it clears it
	}
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	a := s.due[root]
	if a == nil {
		a = &arrivals{ids: map[int64]bool{}}
		s.due[root] = a
	}
	a.owned = a.owned || owned
}

// rereadArrived reads again, as their root's type says, the items typed roots have taken
// over (p.arrived, and what earlier passes could not read): an item keeps the reading it
// had when its row is moved (SetItemRoot) or indexed again (UpsertItem), because what a
// path says is SetParsed's to change, and a folder that takes over what an older
// configuration indexed (a -shows /m/TV over a -media /m) would otherwise hold items read
// the old way next to new files read its way. Only those items are touched, and only those
// whose reading differs. When they are read, the settings note goes; when the pass ends
// first (shutdown) or the reading fails, the note stays and the scanner tries again at
// its next pass, and the next start reads the folder from the note. The caller holds the
// scan semaphore.
func (p *pass) rereadArrived(ctx context.Context) []error {
	s := p.s
	s.rootMu.Lock()
	for root, ids := range p.arrived {
		if p.kinds[root] == "" {
			continue // not typed yet: the guess leaves such items as they are
		}
		a := s.due[root]
		if a == nil {
			a = &arrivals{ids: map[int64]bool{}}
			s.due[root] = a
		}
		for _, id := range ids {
			a.ids[id] = true
		}
	}
	roots := slices.Sorted(maps.Keys(s.due))
	s.rootMu.Unlock()

	var errs []error
	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		if _, ok := s.rootKind(root); !ok || s.rootRetired(root) {
			s.rootMu.Lock() // the folder is gone: nothing to read (its note, if any, is found and forgotten at the next start)
			delete(s.due, root)
			s.rootMu.Unlock()
			continue
		}
		if kind, _ := s.rootKind(root); kind == "" || p.left(root) {
			continue
		}
		s.rootMu.Lock()
		a := s.due[root]
		var (
			only  map[int64]bool
			owned bool
		)
		if a != nil {
			only, owned = maps.Clone(a.ids), a.owned
		}
		s.rootMu.Unlock()
		if a == nil {
			continue
		}
		ids, err := s.reparse(ctx, root, only)
		p.reparsed = append(p.reparsed, ids...)
		if err != nil {
			p.failed[root] = true
			errs = append(errs, fmt.Errorf("root %s: read the items it took over again: %w", root, err))
			continue
		}
		s.rootMu.Lock()
		delete(s.due, root) // rows whose file could not be indexed are still under the old root: the next pass notes them again
		s.rootMu.Unlock()
		if owned {
			if err := s.store.DeleteSetting(ctx, RereadNotePrefix+root); err != nil && ctx.Err() == nil {
				s.log.Warn("forgetting that a folder took over items", "folder", root, "err", err)
			}
		}
	}
	return errs
}

// recheck asks for dir (and, when recursive, its subdirectories) to be
// scanned again soon.
func (p *pass) recheck(dir string, recursive bool) {
	p.deferred[dir] = p.deferred[dir] || recursive
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

// removeMissing deletes the rows selected by in whose files were not seen. It
// keeps those under a mount boundary that is not mounted now (a volume mounted
// inside a root, not a root itself) and those that a file still settling may
// turn out to be.
func (p *pass) removeMissing(ctx context.Context, in func(path string, st store.FileState) bool) error {
	var (
		gone  []int64
		paths []string
		kept  = map[string]int{}
		held  int
	)
	for path, st := range p.states {
		if !in(path, st) {
			continue
		}
		if _, ok := p.seen[path]; ok {
			continue
		}
		if dir := p.unmounted(path, st.Root); dir != "" {
			kept[dir]++
			continue
		}
		if p.settling[st.Size] {
			// Held, not dropped: the next look (once the copy settles) decides.
			// That look must cover this row's directory, or what is left of it.
			held++
			dir := filepath.Dir(path)
			for within(st.Root, dir) && !exists(dir) {
				dir = filepath.Dir(dir)
			}
			p.recheck(dir, dir != filepath.Dir(path))
			continue
		}
		gone = append(gone, st.ID)
		paths = append(paths, path)
	}
	for dir, n := range kept {
		p.s.log.Warn("a volume mounted here is not mounted now; keeping its items", "dir", dir, "items", n)
	}
	if held > 0 {
		p.s.log.Info("keeping missing files that may be a copy still settling", "count", held)
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
// not seen in this pass, missing on disk, on a root that is mounted. A path
// that a case-insensitive (or normalising) file system still answers for but
// that is not spelled that way in its directory any more is gone too: that is
// a rename that changed only the case.
func (p *pass) gone(candidates []string) []string {
	var out []string
	for _, old := range candidates {
		if _, ok := p.seen[old]; ok {
			continue
		}
		if !p.rootAvailable(p.states[old].Root) {
			continue // its drive may just be unmounted: the file is not known to be gone
		}
		_, err := os.Lstat(old)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && !p.spelled(old, p.states[old].Root)) {
			out = append(out, old)
		}
	}
	return out
}

// spelled reports whether every directory from root down to path lists the
// next part of it exactly as path spells it (a case-insensitive file system
// also opens a path whose folder, or file, was renamed to another case).
func (p *pass) spelled(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return p.nameIn(filepath.Dir(path), filepath.Base(path))
	}
	dir := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if !p.nameIn(dir, part) {
			return false
		}
		dir = filepath.Join(dir, part)
	}
	return true
}

// nameIn reports whether dir lists name exactly (an unreadable dir: assume so).
func (p *pass) nameIn(dir, name string) bool {
	names, ok := p.names[dir]
	if !ok {
		names = map[string]bool{}
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				names[e.Name()] = true
			}
		} else {
			names = nil // unreadable: assume it is there
		}
		p.names[dir] = names
	}
	return names == nil || names[name]
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// noteDir records, for a directory met by a walk (real path walked, configured
// spelling path), whether it is a mount boundary: on another device than its
// parent. The scanner remembers boundaries, so that when the volume goes away
// its rows are kept (see unmounted).
func (p *pass) noteDir(walked, path string) {
	dev, ok := p.s.devOf(walked)
	if !ok {
		return
	}
	p.devs[walked] = dev
	parent, ok := p.devs[filepath.Dir(walked)]
	if !ok {
		parent, ok = p.s.devOf(filepath.Dir(walked))
		if !ok {
			return
		}
		p.devs[filepath.Dir(walked)] = parent
	}
	if dev != parent {
		p.s.mountMu.Lock()
		if !p.s.mounts[path] {
			if p.s.mounts == nil {
				p.s.mounts = map[string]bool{}
			}
			p.s.mounts[path] = true
			p.mountsChanged = true
		}
		p.s.mountMu.Unlock()
	}
}

// unmounted returns the remembered mount boundary between root and path that
// is not mounted now (its directory is back on its parent's device, or cannot
// be looked at), or "". A boundary whose directory is gone altogether, from a
// parent that is there, was removed rather than unmounted: it is forgotten,
// and the rows under it may go.
func (p *pass) unmounted(path, root string) string {
	p.s.mountMu.Lock()
	defer p.s.mountMu.Unlock()
	for b := range p.s.mounts {
		if !within(b, path) || !within(root, b) {
			continue
		}
		down, known := p.down[b]
		if !known {
			_, err := os.Lstat(b)
			if errors.Is(err, fs.ErrNotExist) && exists(filepath.Dir(b)) {
				delete(p.s.mounts, b)
				p.mountsChanged = true
				p.s.log.Info("a mount point inside a media root was removed; forgetting it", "dir", b)
				continue
			}
			dev, ok := p.s.devOf(b)
			parent, pok := p.s.devOf(filepath.Dir(b))
			down = !ok || (pok && dev == parent)
			p.down[b] = down
		}
		if down {
			return b
		}
	}
	return ""
}

// mountsKey is the settings key the mount boundaries are kept under, so that
// a volume that is down when ply starts still keeps its rows.
const mountsKey = "scan.mounts"

// loadMounts reads the remembered mount boundaries, once.
func (s *Scanner) loadMounts(ctx context.Context) {
	s.mountMu.Lock()
	defer s.mountMu.Unlock()
	if s.mountsLoaded {
		return
	}
	v, ok, err := s.store.GetSetting(ctx, mountsKey)
	if err != nil {
		s.log.Warn("read the remembered mount points", "err", err)
		return // try again next pass
	}
	s.mountsLoaded = true
	if !ok {
		return
	}
	var list []string
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		s.log.Warn("the remembered mount points are unreadable; starting afresh", "err", err)
		return
	}
	if s.mounts == nil {
		s.mounts = map[string]bool{}
	}
	for _, b := range list {
		s.mounts[b] = true
	}
}

// saveMounts stores the mount boundaries if the pass changed them.
func (p *pass) saveMounts(ctx context.Context) {
	if !p.mountsChanged {
		return
	}
	p.s.mountMu.Lock()
	list := make([]string, 0, len(p.s.mounts))
	for b := range p.s.mounts {
		list = append(list, b)
	}
	p.s.mountMu.Unlock()
	slices.Sort(list)
	data, _ := json.Marshal(list)
	if err := p.s.store.SetSetting(ctx, mountsKey, string(data)); err != nil {
		p.s.log.Warn("remember the mount points", "err", err)
	}
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
	p.moved = append(p.moved, st.ID)
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
				if p.s.rootRetired(j.root) {
					mu.Lock()
					p.skipped[j.root] = true
					mu.Unlock()
					continue // DropRoot is waiting for this pass: leave the folder's files alone
				}
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
	for i, j := range p.work {
		if err := p.s.paced(ctx, i, len(p.work)); err != nil {
			break feed // like the end of ctx: what was probed stays, the rest waits for the next pass
		}
		select {
		case jobs <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	for _, r := range p.roots {
		if f := failures[r.Path]; f != nil {
			errs = append(errs, fmt.Errorf("root %s: %d files could not be probed, first: %w", r.Path, f.n, f.first))
			p.failed[r.Path] = true
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

// itemFor is the row for j as far as its path tells: kind, names and numbers,
// read the way its root's type says (ParseNameIn).
func itemFor(j job) store.Item {
	p := ParseNameIn(Root{Path: j.root, Kind: j.kind}, j.path)
	return store.Item{
		Kind:        p.Kind,
		Title:       p.Title,
		Year:        p.Year,
		Show:        p.Show,
		Season:      p.Season,
		Episode:     p.Episode,
		NeedsReview: p.NeedsReview,
		Path:        j.path,
		Root:        j.root,
		Size:        j.size,
		MTime:       j.mtime,
	}
}

// index probes one file and upserts it, returning its id. Files without a
// video stream are skipped silently (id 0). A file with a row of progress in
// the archive of a removed folder (j.archived) gets it back at once; its manual
// match, if it had one, waits in the archive for reportMatches.
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
	if j.archived {
		s.restore(ctx, id, j.path, j.size)
	}
	return id, nil
}

// restore gives the row id what ArchiveAndDeleteRoot kept of the file at path
// (RestoreArchived). A failure is logged, not returned: the file is indexed, and
// a scan must not fail for want of a progress mark. The archive row stays, so the
// next scan tries again (restoreExisting).
func (s *Scanner) restore(ctx context.Context, id int64, path string, size int64) {
	_, _, ok, err := s.restoreArchived(ctx, id, path, size)
	if err != nil {
		s.log.Warn("restore what a removed folder kept of this file", "path", path, "err", err)
		return
	}
	if ok {
		s.log.Info("restored what a removed folder kept of this file", "path", path, "id", id)
	}
}

// restoreJob is a row that is in the library as it should be, whose archive row
// still has progress to give back.
type restoreJob struct {
	id   int64
	path string
	size int64
}

// loadArchived reads which paths have progress waiting in the archive: only files
// like these are worth the write of a restore.
func (p *pass) loadArchived(ctx context.Context) {
	m, err := p.s.store.ArchivedProgress(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.s.log.Warn("listing the progress waiting in the archive", "err", err)
		}
		return
	}
	p.archived = m
}

// restoreExisting gives the progress back to rows that were in the library and
// unchanged (the process ended between the indexing of the file and its restore).
// A row that moved is not one of them: its archive row belongs to the old file at
// that path, and MoveItem has dropped it.
func (p *pass) restoreExisting(ctx context.Context) {
	for _, j := range p.restoreOnly {
		if ctx.Err() != nil {
			return
		}
		p.s.restore(ctx, j.id, j.path, j.size)
	}
}

func hasVideo(streams []probe.Stream) bool {
	for _, st := range streams {
		if st.Type == "video" {
			return true
		}
	}
	return false
}
