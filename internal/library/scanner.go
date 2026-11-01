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

// ErrRunning is returned by Scan when another scan is in progress.
var ErrRunning = errors.New("scan already running")

const defaultWorkers = 4

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

	lock sync.Mutex // held for the duration of a scan (TryLock)
	wg   sync.WaitGroup

	mu     sync.Mutex // guards status
	status Status
}

// NewScanner returns a scanner for roots, which must be absolute and clean.
// ctx bounds scans started by Trigger.
func NewScanner(ctx context.Context, st *store.Store, p Prober, roots []string, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Scanner{ctx: ctx, store: st, prober: p, roots: roots, workers: defaultWorkers, log: log}
}

// Trigger starts a scan in the background. It reports false, without
// starting anything, when a scan is already running.
func (s *Scanner) Trigger() bool {
	if !s.lock.TryLock() {
		return false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.lock.Unlock()
		s.logResult(s.run(s.ctx))
	}()
	return true
}

// Scan runs a scan and waits for it. It returns ErrRunning if another scan is
// in progress.
func (s *Scanner) Scan(ctx context.Context) error {
	if !s.lock.TryLock() {
		return ErrRunning
	}
	defer s.lock.Unlock()
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

	var sum summary
	err := s.scanAll(ctx, &sum)

	end := time.Now()
	s.mu.Lock()
	s.status.Running = false
	s.status.LastFinished = &end
	s.status.LastError = ""
	if err != nil {
		s.status.LastError = err.Error()
	}
	s.mu.Unlock()

	s.log.Info("scan complete", "indexed", sum.indexed.Load(), "removed", sum.removed.Load(),
		"duration", end.Sub(start).Round(time.Millisecond))
	return err
}

type summary struct {
	indexed atomic.Int64
	removed atomic.Int64
}

func (s *Scanner) scanAll(ctx context.Context, sum *summary) error {
	states, err := s.store.FileStates(ctx)
	if err != nil {
		return fmt.Errorf("load library state: %w", err)
	}
	// Which configured root indexes which file. Roots may nest (/m and
	// /m/movies): the root that already has the row keeps it, otherwise the
	// first one to reach the file takes it, so a file is probed once and does
	// not move between roots from scan to scan.
	owners := map[string]string{}
	for path, st := range states {
		if s.configured(st.Root) && within(st.Root, path) {
			owners[path] = st.Root
		}
	}
	var (
		errs   []error
		failed []string // roots that did not scan cleanly
	)
	for _, root := range s.roots {
		if ctx.Err() != nil {
			break
		}
		if err := s.scanRoot(ctx, root, states, owners, sum); err != nil {
			errs = append(errs, err)
			failed = append(failed, root)
		}
	}
	if err := ctx.Err(); err != nil && !errors.Is(errors.Join(errs...), err) {
		errs = append(errs, err)
	} else if err == nil {
		errs = append(errs, s.removeOrphans(ctx, states, failed, sum))
	}
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
}

// scanRoot walks one root, indexes new and changed files, and removes rows
// for files that have disappeared, but only if the root exists, the walk
// completed without a single error and it found something: an unmounted
// volume (an empty mountpoint, a symlink farm whose targets are gone) or an
// unreadable directory must never wipe the library.
//
// owners maps each file to the configured root that indexes it; states is the
// library as it was when the scan started.
func (s *Scanner) scanRoot(ctx context.Context, root string, states map[string]store.FileState,
	owners map[string]string, sum *summary) error {
	fi, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("root %s: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("root %s: not a directory", root)
	}
	// WalkDir does not descend into a root that is a symlink, which would look
	// like an empty library. Walk the real directory, but keep the paths (and
	// so the rows) under the configured name.
	walkRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("root %s: %w", root, err)
	}

	var (
		jobs     = make(chan job)
		wg       sync.WaitGroup
		mu       sync.Mutex // guards errs, failed and firstErr
		errs     []error
		failed   int
		firstErr error
	)
	for range s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				err := s.index(ctx, j)
				if err == nil {
					sum.indexed.Add(1)
					continue
				}
				mu.Lock()
				if isProbeError(err) {
					failed++
					if firstErr == nil {
						firstErr = err
					}
				} else {
					errs = append(errs, err)
				}
				mu.Unlock()
			}
		}()
	}

	seen := map[string]struct{}{}
	var listing dirNames
	walkErrors := 0
	walkErr := filepath.WalkDir(walkRoot, func(walked string, d fs.DirEntry, err error) error {
		if err != nil {
			walkErrors++
			s.log.Warn("walk error", "path", walked, "err", err)
			return nil // keep going, but the root is now ineligible for deletions
		}
		if walked != walkRoot && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !IsVideoFile(d.Name()) {
			return nil
		}
		path := walked
		if walkRoot != root {
			rel, err := filepath.Rel(walkRoot, walked)
			if err != nil {
				walkErrors++
				s.log.Warn("walk error", "path", walked, "err", err)
				return nil
			}
			path = filepath.Join(root, rel)
		}
		info, err := os.Stat(path) // follows file symlinks
		if err != nil {
			switch {
			case !errors.Is(err, fs.ErrNotExist):
				walkErrors++
				s.log.Warn("stat error", "path", path, "err", err)
			case d.Type()&fs.ModeSymlink != 0:
				// A dangling link is not a raced delete: in a symlink farm it is
				// what an unmounted target drive looks like. One that never
				// made it into the library is just noise.
				if _, indexed := states[path]; indexed {
					walkErrors++
					s.log.Warn("indexed file is now a dangling symlink", "path", path)
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if owner, ok := owners[path]; ok && owner != root {
			return nil // another configured root (a nested one) indexes this file
		}
		owners[path] = root
		seen[path] = struct{}{}
		mtime := info.ModTime().UnixNano()
		subs := sidecars(path, listing.list(filepath.Dir(path)))
		if st, ok := states[path]; ok && st.Size == info.Size() && st.MTime == mtime {
			if st.Root != root {
				// Indexed under a root that is gone from the configuration.
				if err := s.store.SetItemRoot(ctx, st.ID, root); err != nil {
					s.log.Warn("move item to its new root", "path", path, "err", err)
				} else {
					st.Root = root
					states[path] = st
				}
			}
			// The video is unchanged, but subtitles come and go on their own.
			if !slices.Equal(st.Subs, subs) {
				if err := s.store.SetExternalSubtitles(ctx, st.ID, subs); err != nil {
					s.log.Warn("update subtitles", "path", path, "err", err)
				}
			}
			return nil
		}
		select {
		case jobs <- job{path: path, root: root, size: info.Size(), mtime: mtime, subs: subs}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(jobs)
	wg.Wait()

	if failed > 0 {
		errs = append(errs, fmt.Errorf("root %s: %d files could not be probed, first: %w", root, failed, firstErr))
	}
	if walkErr != nil {
		return errors.Join(append(errs, fmt.Errorf("root %s: walk: %w", root, walkErr))...)
	}
	if walkErrors > 0 {
		errs = append(errs, fmt.Errorf("root %s: %d errors while walking; removals skipped", root, walkErrors))
	} else if ctx.Err() == nil {
		if n := s.rowsOf(root, states); len(seen) == 0 && n > 0 {
			// Looks like an unmounted volume: its mountpoint is an empty directory.
			errs = append(errs, fmt.Errorf("root %s: no video files found, but %d indexed items came from it; "+
				"refusing to remove them (is the volume mounted?)", root, n))
		} else {
			errs = append(errs, s.removeMissing(ctx, root, states, seen, sum))
		}
	}
	return errors.Join(errs...)
}

// rowsOf counts the rows that belong to root.
func (s *Scanner) rowsOf(root string, states map[string]store.FileState) int {
	n := 0
	for _, st := range states {
		if st.Root == root {
			n++
		}
	}
	return n
}

func (s *Scanner) removeMissing(ctx context.Context, root string, states map[string]store.FileState,
	seen map[string]struct{}, sum *summary) error {
	var gone []int64
	for path, st := range states {
		if st.Root != root {
			continue
		}
		if _, ok := seen[path]; !ok {
			gone = append(gone, st.ID)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	if err := s.store.DeleteItems(ctx, gone); err != nil {
		return fmt.Errorf("root %s: remove missing items: %w", root, err)
	}
	sum.removed.Add(int64(len(gone)))
	s.log.Info("removed missing files", "root", root, "count", len(gone))
	return nil
}

type probeError struct{ err error }

func (e probeError) Error() string { return e.err.Error() }
func (e probeError) Unwrap() error { return e.err }

func isProbeError(err error) bool {
	var pe probeError
	return errors.As(err, &pe)
}

// index probes one file and upserts it. Files without a video stream are
// skipped silently.
func (s *Scanner) index(ctx context.Context, j job) error {
	res, err := s.prober.Probe(ctx, j.path)
	if err != nil {
		s.log.Warn("probe failed", "path", j.path, "err", err)
		return probeError{err}
	}
	if !hasVideo(res.Streams) {
		s.log.Info("skipping file without a video stream", "path", j.path)
		return nil
	}
	p := ParseName(j.path)
	it := store.Item{
		Kind:        p.Kind,
		Title:       p.Title,
		Year:        p.Year,
		Show:        p.Show,
		Season:      p.Season,
		Episode:     p.Episode,
		Path:        j.path,
		Root:        j.root,
		Size:        j.size,
		MTime:       j.mtime,
		Container:   res.Container,
		DurationSec: res.DurationSec,
		Bitrate:     res.Bitrate,
	}
	id, err := s.store.UpsertItem(ctx, &it, res.Streams)
	if err != nil {
		return fmt.Errorf("store %s: %w", j.path, err)
	}
	if err := s.store.SetExternalSubtitles(ctx, id, j.subs); err != nil {
		s.log.Warn("store subtitles", "path", j.path, "err", err)
	}
	return nil
}

func hasVideo(streams []probe.Stream) bool {
	for _, st := range streams {
		if st.Type == "video" {
			return true
		}
	}
	return false
}
