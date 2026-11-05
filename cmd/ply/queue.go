package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"ply/internal/art"
	"ply/internal/jobs"
	"ply/internal/library"
	"ply/internal/metadata"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
	"ply/internal/tier"
)

// The background queue's kinds of job (docs/M9-ARCHITECTURE.md, "Unified
// persistent queue"). Everything the server does on its own, rather than for a
// request, runs as one of these, below playback and browsing.
const (
	kindScanFull   = "scan.full"   // walk every folder and read what changed
	kindScanDirs   = "scan.dirs"   // read the folders the watcher saw change
	kindMetaMatch  = "meta.match"  // look up details for what is pending or due
	kindCachePrune = "cache.prune" // drop cached artwork and subtitles of files that are gone
)

// playbackLoad shows the stream manager to the governor.
type playbackLoad struct{ m *stream.Manager }

func (p playbackLoad) PlaybackLoad() tier.PlaybackLoad { return tier.PlaybackLoad(p.m.PlaybackLoad()) }

type queueDeps struct {
	store   *store.Store
	gov     *tier.Governor
	scanner *library.Scanner
	worker  *metadata.Worker
	art     *art.Generator // may be nil
	subs    *subs.Generator
	roots   func() []string // the library folders now
	log     *slog.Logger
}

// newQueue builds the queue with ply's kinds of job, and makes the scanner's
// passes yield to playback and browsing (and report their progress) through it.
func newQueue(d queueDeps) *jobs.Queue {
	q := jobs.New(jobs.Options{
		Persist: d.store,
		Gate:    d.gov,
		Logger:  d.log,
		Extra:   scannerActivity(d.scanner),
	})

	// The scanner asks before every file it reads. Under a job it reports the
	// progress and yields through the job (the Activity page shows why it waits);
	// its own passes (a folder just added, a type changed) wait on the governor.
	d.scanner.SetPace(func(ctx context.Context, done, total int) error {
		if r := jobs.RunFrom(ctx); r != nil {
			if total > 0 {
				r.Progress(done, total, "Reading files")
			}
			return r.Yield(ctx)
		}
		return d.gov.Wait(ctx, tier.Need{CPU: true}, nil)
	})

	q.Register(jobs.Spec{
		Kind:     kindScanFull,
		Priority: jobs.PriorityScan,
		Title:    func(json.RawMessage) string { return "Scan the library" },
		// The whole library, end to end: it does not start while a video plays from
		// the same disk. Once it runs it holds the scanner, so it yields between files
		// only to what hurts a viewer now (a stalled player, a CPU transcode): held for
		// a whole film it would keep the folders the watcher saw change waiting too.
		Need:        func(json.RawMessage) tier.Need { return tier.Need{CPU: true, Disks: d.roots()} },
		YieldNeed:   func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
		MaxAttempts: 1, // it runs again on its schedule
		Run: func(ctx context.Context, r *jobs.Run) error {
			err := d.scanner.Scan(ctx)
			if errors.Is(err, library.ErrRunning) {
				return nil
			}
			return err
		},
	})
	q.Register(jobs.Spec{
		Kind:     kindScanDirs,
		Priority: jobs.PriorityDirs,
		Title: func(p json.RawMessage) string {
			var dirs []library.Dir
			_ = json.Unmarshal(p, &dirs)
			switch len(dirs) {
			case 0:
				return "Read new files"
			case 1:
				return "Read new files in " + filepath.Base(dirs[0].Path)
			}
			return fmt.Sprintf("Read new files in %s and %d more folders", filepath.Base(dirs[0].Path), len(dirs)-1)
		},
		// A few files: it reads only their headers, so a play from the same disk
		// does not hold it (only a CPU transcode, a stalled viewer or browsing do).
		Need:        func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
		MaxAttempts: 1, // the watcher retries what it needs to
		Run: func(ctx context.Context, r *jobs.Run) error {
			var dirs []library.Dir
			if err := r.Decode(&dirs); err != nil {
				return jobs.Permanent(err)
			}
			res, err := d.scanner.ScanDirs(ctx, dirs)
			r.SetResult(res)
			return err
		},
	})
	q.Register(jobs.Spec{
		Kind:     kindMetaMatch,
		Priority: jobs.PriorityMatchNew,
		Title:    func(json.RawMessage) string { return "Look up movie and show details" },
		// Light to start (mostly network), but each item may probe a file or decode an
		// image: between items it yields to a stalled viewer, a CPU transcode, browsing.
		YieldNeed: func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
		Run: func(ctx context.Context, r *jobs.Run) error {
			d.worker.RunOnce(ctx)
			return nil
		},
	})
	q.Register(jobs.Spec{
		Kind:     kindCachePrune,
		Priority: jobs.PriorityCache,
		Title:    func(json.RawMessage) string { return "Tidy the artwork and subtitle caches" },
		Run: func(ctx context.Context, r *jobs.Run) error {
			return pruneCaches(ctx, d.store, d.art, d.subs, d.log)
		},
	})
	return q
}

// paceFor is the pace of work that may run inside a job or outside one (the
// metadata worker's passes, the scanner's): within a job it yields through it, so
// the Activity page shows why it waits; outside one it waits on the governor.
func paceFor(gov *tier.Governor, outside tier.Need) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if r := jobs.RunFrom(ctx); r != nil {
			return r.Yield(ctx)
		}
		return gov.Wait(ctx, outside, nil)
	}
}

// scannerActivity shows the scanner's own passes (a folder just added, a type
// changed, a re-read) on the Activity page while no scan job is running.
func scannerActivity(s *library.Scanner) func(running []jobs.Job) []jobs.Job {
	var mu sync.Mutex
	var since time.Time // when the scanner was first seen busy on its own
	return func(running []jobs.Job) []jobs.Job {
		busy := s.Busy()
		for _, j := range running {
			if j.Kind == kindScanFull || j.Kind == kindScanDirs {
				busy = false // the job is what it is doing
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if !busy {
			since = time.Time{}
			return nil
		}
		if since.IsZero() {
			since = time.Now()
		}
		return []jobs.Job{{ID: -1, Kind: "scan.folders", Title: "Read library folders", State: jobs.Running,
			StartedAt: since}}
	}
}

// queuedScanner is the scanner as the watcher drives it, through the queue: each
// batch of changed folders is a scan.dirs job, and the watcher waits for its result.
type queuedScanner struct{ q *jobs.Queue }

func (s queuedScanner) ScanDirs(ctx context.Context, dirs []library.Dir) (library.Result, error) {
	v, err := s.q.Do(ctx, kindScanDirs, jobs.Opt{Payload: dirs, Attempts: 1})
	if v == nil && err != nil && !errors.Is(err, library.ErrNotScanned) && ctx.Err() == nil {
		// The job never ran (it could not be saved, or was cancelled): nothing was
		// scanned, so the watcher keeps the folders and tries again.
		err = fmt.Errorf("%w: %w", library.ErrNotScanned, err)
	}
	res, _ := v.(library.Result)
	return res, err
}

// scanControl is the scanner as the API sees it: "Scan now" queues a full scan
// as the person's request (a scheduled one already queued is moved to the front),
// and the status counts a queued scan as running.
type scanControl struct {
	q       *jobs.Queue
	scanner *library.Scanner
}

func (c scanControl) Trigger() bool {
	if c.q.Started(kindScanFull) {
		return false // one is running: the API answers "already running"
	}
	_, err := c.q.Enqueue(context.Background(), kindScanFull, jobs.Opt{Key: "full", User: true})
	return err == nil
}

func (c scanControl) Status(ctx context.Context) library.Status {
	s := c.scanner.Status(ctx)
	if c.q.Busy(kindScanFull) {
		s.Running = true
	}
	return s
}

// schedule queues the periodic work: a full scan now and every interval (none
// when interval <= 0 after the first), and a look at due metadata every tick.
func schedule(ctx context.Context, q *jobs.Queue, interval, tick time.Duration, log *slog.Logger) {
	enqueue := func(kind, key string) {
		if _, err := q.Enqueue(ctx, kind, jobs.Opt{Key: key}); err != nil && ctx.Err() == nil {
			log.Warn("queue: scheduling", "kind", kind, "err", err)
		}
	}
	enqueue(kindScanFull, "full")
	enqueue(kindMetaMatch, "match")
	var scanC <-chan time.Time
	if interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		scanC = t.C
	}
	metaT := time.NewTicker(tick)
	defer metaT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scanC:
			enqueue(kindScanFull, "full")
		case <-metaT.C:
			enqueue(kindMetaMatch, "match")
		}
	}
}

// pruneCaches removes cached artwork and subtitles of items that are gone or
// changed. Either generator may be nil.
func pruneCaches(ctx context.Context, st *store.Store, artGen *art.Generator, subGen *subs.Generator, log *slog.Logger) error {
	if artGen == nil && subGen == nil {
		return nil
	}
	states, err := st.FileStates(ctx)
	if err != nil {
		return fmt.Errorf("listing items: %w", err)
	}
	live := make(map[int64]int64, len(states))
	for _, fs := range states {
		live[fs.ID] = fs.MTime
	}
	var errs []error
	if artGen != nil {
		n, err := artGen.Prune(live)
		if err != nil {
			errs = append(errs, fmt.Errorf("artwork: %w", err))
		}
		if n > 0 {
			log.Info("artwork pruned", "files", n)
		}
	}
	if subGen != nil {
		n, err := subGen.Prune(live)
		if err != nil {
			errs = append(errs, fmt.Errorf("subtitles: %w", err))
		}
		if n > 0 {
			log.Info("subtitle cache pruned", "files", n)
		}
	}
	return errors.Join(errs...)
}
