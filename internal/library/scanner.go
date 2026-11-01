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
	var errs []error
	for _, root := range s.roots {
		if ctx.Err() != nil {
			break
		}
		if err := s.scanRoot(ctx, root, states, sum); err != nil {
			errs = append(errs, err)
		}
	}
	if err := ctx.Err(); err != nil && !errors.Is(errors.Join(errs...), err) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

type job struct {
	path, root string
	size       int64
	mtime      int64
}

// scanRoot walks one root, indexes new and changed files, and removes rows
// for files that have disappeared, but only if the root exists and the walk
// completed without a single error: an unmounted volume or an unreadable
// directory must never wipe the library.
func (s *Scanner) scanRoot(ctx context.Context, root string, states map[string]store.FileState, sum *summary) error {
	fi, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("root %s: %w", root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("root %s: not a directory", root)
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
	walkErrors := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			walkErrors++
			s.log.Warn("walk error", "path", path, "err", err)
			return nil // keep going, but the root is now ineligible for deletions
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !IsVideoFile(d.Name()) {
			return nil
		}
		info, err := os.Stat(path) // follows file symlinks
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) { // dangling symlink or raced delete
				walkErrors++
				s.log.Warn("stat error", "path", path, "err", err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		seen[path] = struct{}{}
		mtime := info.ModTime().UnixNano()
		if st, ok := states[path]; ok && st.Size == info.Size() && st.MTime == mtime {
			return nil
		}
		select {
		case jobs <- job{path: path, root: root, size: info.Size(), mtime: mtime}:
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
		errs = append(errs, s.removeMissing(ctx, root, states, seen, sum))
	}
	return errors.Join(errs...)
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
	if _, err := s.store.UpsertItem(ctx, &it, res.Streams); err != nil {
		return fmt.Errorf("store %s: %w", j.path, err)
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
