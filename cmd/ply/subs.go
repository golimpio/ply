package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/api"
	"ply/internal/events"
	"ply/internal/jobs"
	"ply/internal/store"
	"ply/internal/subs"
	"ply/internal/tier"
)

// subsMaxAttempts is how many times a job is tried while its file cannot be reached.
const subsMaxAttempts = 500

// kindSubsPreextract reads a film's container once and writes its text subtitles to
// the cache, so that the first play finds them there: extracting a track on demand
// reads the whole file, which over a slow share is many minutes
// (docs/M9-ARCHITECTURE.md, "Time-to-first-frame").
const kindSubsPreextract = "subs.preextract"

// subsPayload is the item to extract the subtitles of, and where its file is (the
// disk the job needs).
type subsPayload struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

// subsKey is the dedupe key of an item's job.
func subsKey(id int64) string { return "subs:" + strconv.FormatInt(id, 10) }

// pausedHoldFor is how long a play that is paused holds an extraction from its disk:
// the first minutes, while the person is back to it, and no longer. A film left paused
// overnight must not keep the library's subtitles from being read.
var pausedHoldFor = 2 * time.Minute

// diskHold answers, for a file an extraction reads, whether it must wait for the disk:
// a play reads from it, or a viewer is stalled. Browsing does not hold it (it would stop
// and start ffmpeg for every page of the library: the queue's own start has the
// browsing rule). When everything that plays is paused, it holds for pausedHoldFor,
// counted from when it was first seen so.
type diskHold struct {
	gov *tier.Governor // nil: nothing holds
	now func() time.Time

	mu          sync.Mutex
	pausedSince map[string]time.Time // by file
}

func newDiskHold(gov *tier.Governor) *diskHold {
	return &diskHold{gov: gov, now: time.Now, pausedSince: map[string]time.Time{}}
}

func (h *diskHold) check(file string) (wait bool, why string) {
	if h.gov == nil {
		return false, ""
	}
	hd := h.gov.Check(tier.Need{Disks: []string{file}})
	h.mu.Lock()
	defer h.mu.Unlock()
	if !hd.Held || hd.Browsing {
		delete(h.pausedSince, file)
		return false, ""
	}
	if pb := h.gov.State().Playback; pb.Sessions > 0 && pb.Active == 0 {
		t, seen := h.pausedSince[file]
		if !seen {
			t = h.now()
			h.pausedSince[file] = t
		}
		if h.now().Sub(t) >= pausedHoldFor {
			return false, ""
		}
	} else {
		delete(h.pausedSince, file)
	}
	return true, hd.Reason
}

// registerSubs declares the kind. d.subs nil: no text subtitles (tests).
//
// The job needs the item's disk, so it does not start while a play reads from it (a
// play being set up counts: stream.Manager.PlaybackLoad), steps aside between its
// reads (the sidecars, then the container) through the queue's yield, and has the run
// suspended if a play starts while ffmpeg reads (the generator asks the pace). It is in
// the light lane: it can take a whole film's read time and must not keep the folders
// the watcher saw change, or the lookups of new files, waiting behind it.
func registerSubs(q *jobs.Queue, d queueDeps) {
	if d.subs == nil {
		return
	}
	hold := newDiskHold(d.gov)
	payload := func(p json.RawMessage) (s subsPayload) {
		_ = json.Unmarshal(p, &s)
		return s
	}
	disks := func(p json.RawMessage) tier.Need { return tier.Need{Disks: []string{payload(p).Path}} }
	q.Register(jobs.Spec{
		Kind:     kindSubsPreextract,
		Priority: jobs.PriorityCache,
		Title: func(p json.RawMessage) string {
			if s := payload(p); s.Path != "" {
				return "Extract the subtitles of " + filepath.Base(s.Path)
			}
			return "Extract subtitles"
		},
		Need: disks,
		// The same disk: a job that is let in because no play reads from it must not go on
		// reading when one starts.
		YieldNeed: disks,
		Light:     true,
		// A file that cannot be reached (a share asleep for a weekend) is asked for again, with
		// the queue's backoff (30 minutes at most), for about ten days: the default five tries
		// would give up after fifteen minutes, and a new file the sweep does not list would
		// wait for its first play.
		MaxAttempts: subsMaxAttempts,
		Run: func(ctx context.Context, r *jobs.Run) error {
			var p subsPayload
			if err := r.Decode(&p); err != nil {
				return jobs.Permanent(err)
			}
			e, err := d.store.GetEntry(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				return nil // the file went away since
			}
			if err != nil {
				return err
			}
			srcs := api.SubtitleSources(e)
			if len(srcs) == 0 {
				return nil
			}
			// The bar and "Reading the file: 43%, about 12 min left" (subsMeter), from how far into
			// the film ffmpeg has read. Over a slow share the read takes the better part of an hour.
			var film time.Duration
			if s := e.DurationSec; s > 0 && s < 1e7 {
				film = time.Duration(s * float64(time.Second))
			}
			meter := newSubsMeter(r.Progress, film)
			pace := func() (bool, string) { return hold.check(e.Path) }
			var errs []error
			for _, step := range subsSteps(srcs) {
				if err := r.Yield(ctx); err != nil {
					return err
				}
				if err := d.subs.FillProgress(ctx, step, pace, meter.wait, meter.read); err != nil {
					if ctx.Err() != nil {
						return err
					}
					errs = append(errs, err) // a sidecar that fails does not keep the container's tracks from being read
				}
			}
			err = errors.Join(errs...)
			switch {
			case err == nil:
				return nil
			case errors.Is(err, subs.ErrUnavailable):
				return err // the share is not there (yet): asked again after a while, never given up
			case errors.Is(err, subs.ErrConvert):
				return jobs.Permanent(err) // ffmpeg cannot read it: asking again will not help
			}
			return err
		},
	})
}

// subsSteps are the reads of an item's subtitles, in order: each sidecar on its own (a
// few kilobytes), then every text track of the container in one read.
func subsSteps(srcs []subs.Source) [][]subs.Source {
	var steps [][]subs.Source
	var embedded []subs.Source
	for _, s := range srcs {
		if s.Stream >= 0 {
			embedded = append(embedded, s)
		} else {
			steps = append(steps, []subs.Source{s})
		}
	}
	if len(embedded) > 0 {
		steps = append(steps, embedded)
	}
	return steps
}

// What the sweep of the library queues, at most: the items somebody is part way through,
// and the next episode of the shows they are watching. Never the library: reading every
// film's container over a slow share is days of the link (263 films of 7 GB at 2.5 MB/s
// is about 8.5 days), and most of those films will not be played. A film that is played
// gets its tracks extracted then (the first play extracts all of them, in one read), and
// a new file is queued when the scanner adds it (addMax or fewer at a time).
const (
	sweepContinue = 20
	sweepNext     = 20
	// addMax is the most files one scan may add for them to count as new files somebody
	// will play soon. More is an import (a library just added, a disk copied in), which
	// says nothing about what will be played: they wait for their first play.
	addMax = 20
)

// subsFeeder queues the pre-extraction of subtitles, one item's job at a time: a job
// reads a whole film, and two would take the light lane from the lookups of new files.
// Items come from the scanner (a new file goes first, as the likelier to be played soon)
// and from a sweep that runs at start and after each scan (see sweepContinue).
//
// Whether an item needs a job is decided from the database and the cache directory
// alone, without touching the share: an item with a text track in its container whose
// WebVTT is not cached and did not fail before. (A sidecar subtitle is converted with the
// item's tracks, and on its first play otherwise: it is a small file.) An item whose job
// a person cancelled is not queued again until ply restarts.
type subsFeeder struct {
	st    *store.Store
	q     *jobs.Queue
	gen   *subs.Generator
	log   *slog.Logger
	every time.Duration // how often to see whether the last job is over

	mu        sync.Mutex
	todo      []int64
	cancelled map[int64]bool // items whose job was cancelled in Activity
	wake      chan struct{}
	scan      chan struct{}
}

func newSubsFeeder(st *store.Store, q *jobs.Queue, gen *subs.Generator, log *slog.Logger) *subsFeeder {
	return &subsFeeder{st: st, q: q, gen: gen, log: log, every: 2 * time.Second, cancelled: map[int64]bool{},
		wake: make(chan struct{}, 1), scan: make(chan struct{}, 1)}
}

// Add queues items the scanner just added, ahead of the rest. It does not block. More
// than addMax at once is an import, and none of them is queued.
func (f *subsFeeder) Add(ids ...int64) {
	if len(ids) > addMax {
		f.log.Info("subtitles: not extracting the subtitles of a bulk import ahead of time", "files", len(ids))
		return
	}
	f.mu.Lock()
	f.todo = append(slices.Clone(ids), f.todo...)
	f.mu.Unlock()
	poke(f.wake)
}

// Rescan asks for a sweep of the library. It does not block.
func (f *subsFeeder) Rescan() { poke(f.scan) }

func poke(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Run feeds the queue until ctx ends.
func (f *subsFeeder) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-f.q.Ready(): // what the last run left is queued: the sweep must not queue it twice
	}
	events, stop := f.subscribe()
	defer func() { stop() }()
	poke(f.scan) // at start
	t := time.NewTicker(f.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok { // too slow for the queue: it let go of us
				stop()
				events, stop = f.subscribe()
				continue
			}
			f.note(ev)
			continue
		case <-f.scan:
			f.sweep(ctx)
		case <-f.wake:
		case <-t.C:
		}
		f.feed(ctx)
	}
}

func (f *subsFeeder) subscribe() (<-chan events.Event, func()) {
	hub := f.q.Events()
	_, _, ch, cancel := hub.Subscribe(hub.Last(), events.Topics{events.Jobs: true})
	return ch, cancel
}

// note learns from the queue's events which of our jobs a person cancelled.
func (f *subsFeeder) note(ev events.Event) {
	d, ok := ev.Data.(jobs.EventData)
	if ev.Type != "job.cancelled" || !ok || d.Job == nil || d.Job.Kind != kindSubsPreextract {
		return
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(d.Job.Key, "subs:"), 10, 64); err == nil {
		f.mu.Lock()
		f.cancelled[id] = true
		f.mu.Unlock()
	}
}

// needs reports whether e has a text track in its container that is not cached and has
// not failed.
func (f *subsFeeder) needs(e store.Entry) bool {
	return slices.ContainsFunc(api.EmbeddedSubtitles(e), func(s subs.Source) bool {
		return !f.gen.Cached(s) && f.gen.Failed(s) == nil
	})
}

// wants reports whether e should get a job.
func (f *subsFeeder) wants(e store.Entry) bool {
	f.mu.Lock()
	cancelled := f.cancelled[e.ID]
	f.mu.Unlock()
	return !cancelled && f.needs(e)
}

// sweep queues, behind what is there, the items somebody is likely to play next that need
// a job: those they are part way through, most recent first, then the next episode of
// each show they watch.
func (f *subsFeeder) sweep(ctx context.Context) {
	cont, err := f.st.Continue(ctx, sweepContinue)
	if err != nil && ctx.Err() == nil {
		f.log.Warn("subtitles: listing what is being watched", "err", err)
	}
	eps, err := f.st.ListItems(ctx, store.ListFilter{Kind: store.KindEpisode})
	if err != nil && ctx.Err() == nil {
		f.log.Warn("subtitles: listing the episodes", "err", err)
	}
	for _, e := range append(cont, nextEpisodes(eps, sweepNext)...) {
		if !f.wants(e) {
			continue
		}
		f.mu.Lock()
		if !slices.Contains(f.todo, e.ID) {
			f.todo = append(f.todo, e.ID)
		}
		f.mu.Unlock()
	}
}

// nextEpisodes are the episodes that come after the one of each show that was played last
// (episodes in order, with their progress), if that one has not been watched to the end
// itself or the next has, the shows watched last first. At most limit.
func nextEpisodes(eps []store.Entry, limit int) []store.Entry {
	type last struct {
		idx int
		at  int64
	}
	lastOf := map[string]last{}
	for i, e := range eps {
		if p := e.Progress; p != nil && p.UpdatedAt >= lastOf[e.Show].at {
			lastOf[e.Show] = last{i, p.UpdatedAt}
		}
	}
	type pick struct {
		e  store.Entry
		at int64
	}
	var picks []pick
	for show, l := range lastOf {
		if next := l.idx + 1; next < len(eps) && eps[next].Show == show && (eps[next].Progress == nil || !eps[next].Progress.Watched()) {
			picks = append(picks, pick{eps[next], l.at})
		}
	}
	slices.SortFunc(picks, func(a, b pick) int {
		if a.at != b.at {
			return int(b.at - a.at) // watched last first
		}
		return int(a.e.ID - b.e.ID)
	})
	out := make([]store.Entry, 0, min(limit, len(picks)))
	for _, p := range picks[:min(limit, len(picks))] {
		out = append(out, p.e)
	}
	return out
}

// feed queues the next item that needs a job, if the last one is over.
func (f *subsFeeder) feed(ctx context.Context) {
	for ctx.Err() == nil && !f.q.Busy(kindSubsPreextract) {
		f.mu.Lock()
		if len(f.todo) == 0 {
			f.mu.Unlock()
			return
		}
		id := f.todo[0]
		f.todo = f.todo[1:]
		f.mu.Unlock()

		e, err := f.st.GetEntry(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !f.wants(e)) {
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				f.log.Warn("subtitles: reading an item", "id", id, "err", err)
			}
			continue
		}
		if _, err := f.q.Enqueue(ctx, kindSubsPreextract, jobs.Opt{Key: subsKey(id), Payload: subsPayload{ID: id, Path: e.Path}}); err != nil && ctx.Err() == nil {
			f.log.Warn("queue: subtitles", "id", id, "err", err)
		}
	}
}
