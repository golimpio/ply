// Package subs converts text subtitles to WebVTT and caches the result on
// disk, so a browser <track> can show them.
//
// Embedded streams are extracted from the container with one ffmpeg run, which
// reads the whole file once and writes every track asked for; sidecar files are
// converted one run each. Concurrent requests for the same track share one run.
//
// There are two kinds of caller (docs/M9-ARCHITECTURE.md, "Time-to-first-frame"):
//   - The player wants a track now: Path and Follow. The run is not demoted, and
//     Follow hands out the WebVTT while ffmpeg is still writing it, so the
//     viewer does not wait for a whole film to be read over a slow share.
//   - The library wants every track ready before anybody asks: Fill. The run is
//     demoted, and suspended while the caller says a play needs the disk. A play
//     that asks for a track Fill is making promotes the run; when the last request
//     that waits for a run leaves, the run is background work again.
package subs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"ply/internal/proc"
)

var (
	// ErrConvert reports that ffmpeg could not turn the subtitle into WebVTT, or did
	// not in the time it was given. It is remembered (for the generator's life, and
	// on disk until the file changes).
	ErrConvert = errors.New("subs: conversion failed")
	// ErrUnavailable reports that the file could not be read: it is not there (a share
	// that is not mounted yet, asleep, or gone), or the run was cut to the track that was
	// asked for. It says nothing about the subtitle: nothing is remembered, and asking
	// again later is right.
	ErrUnavailable = errors.New("subs: the file cannot be read now")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("subs: closed")
)

// errDropped is what a request for a track gets whose run was cut to the track
// somebody else asked for (see Generator.convert).
var errDropped = fmt.Errorf("%w: ffmpeg failed with this track in its run, which was cut to the first track", ErrUnavailable)

// errTimeout is why a run that used its time was stopped: a failure, not retried (reading
// the film again for hours would not end differently).
var errTimeout = fmt.Errorf("%w: %w", ErrConvert, context.DeadlineExceeded)

// failureTTL is how long a failed conversion is remembered in memory (unless the file
// changes first). A failure can be the disk or the share having a bad moment, and a
// conversion can take minutes, so it is not retried on every request, but not held
// against the file for good either. (Fill does not retry it at all: see Generator.Failed.)
const failureTTL = 5 * time.Minute

// foregroundRuns is how many runs for requests that wait can be going at once: a person
// is behind each, and a person stopped by a run that background work has not finished is
// what the split of the two kinds is for.
const foregroundRuns = 8

// paceEvery is how often a run asks whether it must wait.
var paceEvery = 250 * time.Millisecond

// textCodecs are the embedded subtitle codecs that become WebVTT.
var textCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// IsText reports whether an embedded subtitle codec can be converted to WebVTT.
func IsText(codec string) bool { return textCodecs[strings.ToLower(codec)] }

// Source says which subtitle to convert.
type Source struct {
	ItemID int64
	Key    string // "3" (stream index) or "ext0" (sidecar); part of the cache name
	Input  string // the media file (Stream >= 0) or the sidecar file (Stream < 0)
	Stream int    // ffprobe stream index of the subtitle in Input; negative for a sidecar
	MTime  int64  // unix nanoseconds of Input; part of the cache name
}

// Pace says, every so often, whether a run must wait (a play reads the disk it reads)
// and, if so, why in words for the Activity page.
type Pace func() (wait bool, why string)

// Options configures New.
type Options struct {
	Dir         string        // cache directory, created if missing
	FFmpeg      string        // default "ffmpeg"
	Niceness    int           // unix nice value for background runs; default 10
	Concurrency int           // simultaneous background runs; default 2
	Timeout     time.Duration // running time per conversion; default 3 hours (a 7 GB remux at 2.5 MB/s is 50 minutes)
	// Reaper is the program that runs `--reap` (ply itself; stream.ReapMain): where there
	// is no parent-death signal (macOS) one is started for every ffmpeg group, and kills
	// the group, suspended or not, if ply dies. Empty starts none (Linux has Pdeathsig).
	Reaper string
	// Hold paces a run for input that nobody waits for any more and that no Fill is
	// pacing (the viewer who asked for it left): it is the same question as Fill's pace.
	// nil: such a run goes on at full speed, demoted.
	Hold   func(input string) (wait bool, why string)
	Logger *slog.Logger
}

// Generator converts subtitles on demand.
type Generator struct {
	dir      string
	ffmpeg   string
	niceness int
	timeout  time.Duration
	sem      chan struct{} // background runs
	fgSem    chan struct{} // runs somebody waits for
	reaper   string
	holdFn   func(input string) (bool, string)
	log      *slog.Logger

	ctx    context.Context // ends with Close; every conversion runs under it
	cancel context.CancelFunc
	wg     sync.WaitGroup // conversions in flight

	mu       sync.Mutex
	closed   bool
	inflight map[string]*call   // by cache name; a run that writes several tracks is under each name
	failed   map[string]failure // cache names that could not be generated
	failTTL  time.Duration

	// run executes ffmpeg and returns its stderr. Tests replace it. It tells p when
	// ffmpeg has started and when it has been waited for.
	run func(ctx context.Context, args []string, p *ctl) (string, error)
}

// call is one ffmpeg run, and what it wrote.
type call struct {
	done   chan struct{}
	err    error            // how the run ended; set before done is closed
	miss   map[string]error // tracks a run that ended well did not write
	ctl    ctl
	cancel context.CancelCauseFunc
	pace   Pace // of the Fill that started it; nil for a run a request started
	note   func(why string)

	// A run that failed with several tracks in it is tried again with the first alone:
	// dropped are the names it gave up on, and gen counts the starts, so that a Follower
	// knows the file it is reading is not the one that will be finished.
	dropped map[string]bool
	gen     atomic.Int32

	// How far into the film ffmpeg has read (a container's run only), and who wants to know.
	progMu   sync.Mutex
	at       time.Duration
	hasAt    bool
	watchers []func(at time.Duration)
}

// errFor is why the track with cache name was not written, nil if it was. Call it once
// done is closed.
func (c *call) errFor(name string) error {
	if c.dropped[name] {
		return errDropped
	}
	if c.err != nil {
		return c.err
	}
	return c.miss[name]
}

// doneCall stands for a conversion that was finished before anyone asked.
var doneCall = func() *call {
	c := &call{done: make(chan struct{})}
	close(c.done)
	return c
}()

type failure struct {
	err error
	at  time.Time
}

// New returns a Generator caching under o.Dir.
func New(o Options) (*Generator, error) {
	if o.Dir == "" {
		return nil, errors.New("subs: no cache directory")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Niceness == 0 {
		o.Niceness = 10
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Hour
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Generator{
		ctx:      ctx,
		cancel:   cancel,
		failTTL:  failureTTL,
		dir:      o.Dir,
		ffmpeg:   o.FFmpeg,
		niceness: o.Niceness,
		timeout:  o.Timeout,
		sem:      make(chan struct{}, o.Concurrency),
		fgSem:    make(chan struct{}, foregroundRuns),
		reaper:   o.Reaper,
		holdFn:   o.Hold,
		log:      o.Logger,
		inflight: map[string]*call{},
		failed:   map[string]failure{},
	}
	g.run = g.exec
	return g, nil
}

// cacheName is "<id>-<key>-<mtime>.vtt". A sidecar's name also carries a hash
// of its path: the list of sidecars can change under the same ext<N> key.
func cacheName(src Source) string {
	name := fmt.Sprintf("%d-%s-%d", src.ItemID, src.Key, src.MTime)
	if src.Stream < 0 {
		sum := sha256.Sum256([]byte(src.Input))
		name += "-" + hex.EncodeToString(sum[:4])
	}
	return name + ".vtt"
}

// failName is the name of the file that records, next to the cached file of the same
// name, that its conversion failed.
func failName(name string) string { return strings.TrimSuffix(name, ".vtt") + ".fail" }

// Close stops the conversions in progress (killing their ffmpeg and removing
// their half-written files) and waits for them. Path fails afterwards.
func (g *Generator) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.cancel()
	g.wg.Wait()
}

// Cached reports whether the WebVTT of src is in the cache: serving it needs no
// ffmpeg.
func (g *Generator) Cached(src Source) bool { return g.cached(cacheName(src)) }

func (g *Generator) cached(name string) bool {
	fi, err := os.Stat(filepath.Join(g.dir, name))
	return err == nil && fi.Size() > 0
}

// Failed says why the conversion of src failed before, in this run of ply or an
// earlier one, if it did and the file has not changed since; nil otherwise. The
// failure is recorded on disk (in the cache directory, pruned with the rest), because
// reading a film for an hour to find out again must not happen at every start or scan.
// Fill does not try a track that failed; a request that waits for a track (Path,
// Follow) does, after a few minutes, and a success clears the record.
func (g *Generator) Failed(src Source) error {
	b, err := os.ReadFile(filepath.Join(g.dir, failName(cacheName(src))))
	if err != nil {
		return nil
	}
	return fmt.Errorf("%w: it failed before: %s", ErrConvert, firstLine(string(b)))
}

func (g *Generator) writeFail(name string, err error) {
	if werr := os.WriteFile(filepath.Join(g.dir, failName(name)), []byte(err.Error()+"\n"), 0o644); werr != nil {
		g.log.Warn("subtitles: recording a failure", "name", name, "err", werr)
	}
}

// Path returns the cached WebVTT file for src, converting first if needed, as
// work somebody waits for. A source that failed is not retried until its mtime
// changes or a few minutes have passed, except when its file could not be reached
// at all (an unmounted share is not the subtitle's fault: ErrUnavailable).
func (g *Generator) Path(ctx context.Context, src Source) (string, error) {
	c, err := g.begin([]Source{src}, true, nil, nil)
	if err != nil {
		return "", err
	}
	if c != nil {
		defer c.ctl.unwait()
		select {
		case <-c.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if err := c.errFor(cacheName(src)); err != nil {
			return "", err
		}
	}
	return filepath.Join(g.dir, cacheName(src)), nil
}

// Follow starts the conversion of src, or joins the one under way (promoting it to
// work somebody waits for), and returns a reader of the WebVTT that gives out cues
// as ffmpeg writes them. siblings are other embedded tracks of the same container:
// a run that has to start extracts them too, since the container is read whole
// either way. Close the Follower: when the last one of a run leaves it is background
// work again.
func (g *Generator) Follow(ctx context.Context, src Source, siblings []Source) (*Follower, error) {
	c, err := g.begin(append([]Source{src}, siblings...), true, nil, nil)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = doneCall
	}
	name := cacheName(src)
	return &Follower{ctx: ctx, c: c, name: name, gen: c.gen.Load(),
		dst: filepath.Join(g.dir, name), tmp: filepath.Join(g.dir, name+".tmp")}, nil
}

// Fill converts every track of srcs that is not cached yet, as background work, and
// returns when they are (or the file cannot be read). Sidecars go first, a run each:
// they are small. The embedded tracks follow in one run, which reads the container
// once. A track that fails does not stop the others (the errors are joined), and one
// that failed before is not tried again (Failed).
//
// pace is asked every so often whether the run must wait (a play reads the same disk);
// while it says yes, ffmpeg is suspended, and the time does not count against the
// conversion's limit. note, if not nil, is told why it waits, and "" when it goes on or
// when a play started to wait for it. A play that asks for one of the tracks promotes
// the run: it stops waiting for pace. If ctx ends first the run is stopped, unless a
// play is waiting for it.
func (g *Generator) Fill(ctx context.Context, srcs []Source, pace Pace, note func(why string)) error {
	return g.FillProgress(ctx, srcs, pace, note, nil)
}

// FillProgress is Fill, and tells progress, if not nil, how far into the film ffmpeg has
// read the container: the time of the last subtitle packet it has written, about every half
// second once there is one, and again from the start if the run is tried again with fewer
// tracks. It is told by the goroutine that reads ffmpeg's output, one call at a time, and
// must not call the Generator. A sidecar is a few kilobytes and has no progress, and a run
// that a play started and that this one joins is told the same.
func (g *Generator) FillProgress(ctx context.Context, srcs []Source, pace Pace, note func(why string), progress func(at time.Duration)) error {
	var embedded []Source
	var errs []error
	for _, s := range srcs {
		if s.Stream >= 0 {
			embedded = append(embedded, s)
			continue
		}
		if err := g.fillRun(ctx, []Source{s}, pace, note, progress); err != nil {
			if ctx.Err() != nil || errors.Is(err, ErrUnavailable) {
				return err
			}
			errs = append(errs, err)
		}
	}
	if err := g.fillRun(ctx, embedded, pace, note, progress); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// fillRun converts batch in as few runs as it takes.
func (g *Generator) fillRun(ctx context.Context, batch []Source, pace Pace, note func(string), progress func(time.Duration)) error {
	var errs []error
	for {
		i := slices.IndexFunc(batch, func(s Source) bool { return !g.Cached(s) })
		if i < 0 {
			return errors.Join(errs...)
		}
		batch = batch[i:]
		c, err := g.begin(batch, false, pace, note)
		if err == nil && c != nil {
			c.watch(progress)
			select {
			case <-c.done:
			case <-ctx.Done():
				cause := context.Cause(ctx)
				if c.ctl.cancelUnlessWaited(func() { c.cancel(cause) }) {
					<-c.done
				}
				return cause
			}
			err = c.errFor(cacheName(batch[0]))
		}
		switch {
		case err == nil:
			batch = batch[1:]
		case errors.Is(err, errDropped):
			// Its run was cut to another track: this one goes in a run of its own.
		case errors.Is(err, ErrUnavailable) || errors.Is(err, ErrClosed) || ctx.Err() != nil:
			return err
		default: // this track failed; the others may not
			errs = append(errs, err)
			batch = batch[1:]
		}
	}
}

// begin returns the run that writes batch[0], starting one if none is under way
// (nil if the track is cached already). The rest of batch ride along in a new run
// when they are embedded tracks of the same container and are not cached, under way
// or failed before (whoever asks: only batch[0] is a request). waiter says that
// somebody waits for batch[0] (the caller then releases the run with c.ctl.unwait);
// pace and note belong to a run that nobody does. batch[0] itself, if it failed before,
// is refused to a run nobody waits for.
func (g *Generator) begin(batch []Source, waiter bool, pace Pace, note func(string)) (*call, error) {
	first := batch[0]
	name := cacheName(first)
	if g.cached(name) {
		return nil, nil
	}
	if !waiter {
		if err := g.Failed(first); err != nil {
			return nil, err
		}
	}
	// Cached, or failed before. Only batch[0] is somebody's request: a track that rides along
	// is not tried again because a person asked for another one (it is when asked for itself).
	have := make([]bool, len(batch))
	for i := 1; i < len(batch); i++ {
		have[i] = g.cached(cacheName(batch[i])) || g.Failed(batch[i]) != nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if f, ok := g.failed[name]; ok {
		if time.Since(f.at) < g.failTTL {
			return nil, f.err
		}
		delete(g.failed, name)
	}
	if g.closed {
		return nil, ErrClosed
	}
	if c, ok := g.inflight[name]; ok {
		if waiter {
			c.ctl.wait()
		}
		return c, nil
	}

	run := []Source{first}
	if first.Stream >= 0 {
		for i, s := range batch[1:] {
			n := cacheName(s)
			if s.Stream < 0 || s.Input != first.Input || have[i+1] {
				continue
			}
			if slices.ContainsFunc(run, func(r Source) bool { return cacheName(r) == n }) {
				continue // listed twice
			}
			if _, going := g.inflight[n]; going {
				continue
			}
			if f, ok := g.failed[n]; ok && time.Since(f.at) < g.failTTL {
				continue
			}
			run = append(run, s)
		}
	}

	// What a run that ply did not live to end left is not for the follower that comes with this
	// one: gone before anybody can read it (ffmpeg would write over it, but later).
	for _, s := range run {
		_ = os.Remove(filepath.Join(g.dir, cacheName(s)+".tmp"))
	}

	// The work outlives the first request (a reload must not restart a conversion
	// that took minutes), but not the generator.
	ctx, cancel := context.WithCancelCause(g.ctx)
	c := &call{done: make(chan struct{}), cancel: cancel, pace: pace, note: note}
	c.ctl.progress = c.report
	c.gen.Store(1)
	if waiter {
		c.ctl.waiters = 1
	}
	for _, s := range run {
		g.inflight[cacheName(s)] = c
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.fill(ctx, c, run)
	}()
	return c, nil
}

func (g *Generator) fill(ctx context.Context, c *call, run []Source) {
	all := slices.Clone(run)
	c.miss, c.err = g.convert(ctx, c, run)
	type failedTrack struct {
		name string
		err  error
	}
	var toRecord []failedTrack
	g.mu.Lock()
	for _, s := range all {
		name := cacheName(s)
		if g.inflight[name] == c { // a dropped track may be some other run's by now
			delete(g.inflight, name)
		}
		if c.dropped[name] {
			continue // it was not tried alone: nothing is known against it
		}
		if err := c.errFor(name); errors.Is(err, ErrConvert) {
			if _, serr := os.Stat(s.Input); serr == nil {
				g.failed[name] = failure{err, time.Now()}
				toRecord = append(toRecord, failedTrack{name, err})
			} // else the input is unreachable (share asleep or unmounted): ask again next time
		}
	}
	g.mu.Unlock()
	for _, r := range toRecord {
		g.writeFail(r.name, r.err)
	}
	c.cancel(nil)
	close(c.done)
}

// convert runs ffmpeg for run: the sidecar, or the embedded tracks of one container.
// It returns the tracks that were not written although ffmpeg was happy, or why the
// run failed. A run of several tracks that fails because ffmpeg did is tried again
// with the first track alone: one track it cannot do must not cost the one that was
// asked for.
func (g *Generator) convert(ctx context.Context, c *call, run []Source) (map[string]error, error) {
	sem := g.sem
	if c.ctl.isUrgent() {
		sem = g.fgSem
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	defer func() { <-sem }()

	bud := newBudget(g.timeout, func() { c.cancel(errTimeout) })
	defer bud.close()
	c.ctl.mu.Lock()
	c.ctl.bud = bud
	c.ctl.mu.Unlock()

	stop, paced, input := make(chan struct{}), make(chan struct{}), run[0].Input
	go func() { defer close(paced); g.paceLoop(c, input, stop) }()
	defer func() { close(stop); <-paced }()

	for {
		miss, err := g.attempt(ctx, c, run)
		if err != nil && len(run) > 1 && errors.Is(err, ErrConvert) && ctx.Err() == nil {
			g.log.Warn("subtitles: ffmpeg failed with several tracks; trying the first alone",
				"file", filepath.Base(run[0].Input), "tracks", len(run), "err", err)
			g.narrow(c, run[1:])
			run = run[:1]
			continue
		}
		return miss, err
	}
}

// narrow gives up the tracks drop, and counts a new start. The names are let go at once: a
// request for one of them starts a run of its own instead of joining this one and waiting for
// the whole of its second read, which is not going to write the track.
func (g *Generator) narrow(c *call, drop []Source) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.dropped == nil {
		c.dropped = map[string]bool{}
	}
	for _, s := range drop {
		name := cacheName(s)
		c.dropped[name] = true
		if g.inflight[name] == c {
			delete(g.inflight, name)
		}
	}
	c.gen.Add(1)
}

// attempt starts ffmpeg once for run and moves what it wrote into place.
func (g *Generator) attempt(ctx context.Context, c *call, run []Source) (map[string]error, error) {
	first := run[0]
	args := []string{"-nostdin", "-v", "error", "-y"}
	if first.Stream >= 0 {
		// How far the container has been read, on stdout (exec reads it): the whole file is read,
		// and over a slow share that is the wait.
		args = append(args, "-progress", "pipe:1")
	} else {
		// ffmpeg exits 0 after dropping the rest of a file that is not
		// UTF-8, so say what it is.
		if enc := legacyCharset(first.Input); enc != "" {
			args = append(args, "-sub_charenc", enc)
		}
	}
	args = append(args, "-i", first.Input)
	tmps := make([]string, len(run))
	for i, s := range run {
		tmps[i] = filepath.Join(g.dir, cacheName(s)+".tmp")
		defer os.Remove(tmps[i])
		if s.Stream >= 0 {
			// Flushed after every cue: the file is read while it is written (Follow).
			args = append(args, "-map", "0:"+strconv.Itoa(s.Stream), "-c:s", "webvtt", "-flush_packets", "1", "-f", "webvtt", tmps[i])
		} else {
			args = append(args, "-c:s", "webvtt", "-f", "webvtt", tmps[i])
		}
	}

	stderr, err := g.run(ctx, args, &c.ctl)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		if _, serr := os.Stat(first.Input); serr != nil { // the share went to sleep, or is not mounted yet
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, serr)
		}
		return nil, fmt.Errorf("%w: %v: %s", ErrConvert, err, firstLine(stderr))
	}
	var miss map[string]error
	for i, s := range run {
		name := cacheName(s)
		var err error
		if fi, serr := os.Stat(tmps[i]); serr != nil || fi.Size() == 0 {
			err = fmt.Errorf("%w: ffmpeg wrote nothing: %s", ErrConvert, firstLine(stderr))
		} else if err = os.Rename(tmps[i], filepath.Join(g.dir, name)); err == nil {
			_ = os.Remove(filepath.Join(g.dir, failName(name))) // it works now
		}
		if err != nil {
			if miss == nil {
				miss = map[string]error{}
			}
			miss[name] = err
		}
	}
	return miss, nil
}

// paceLoop holds the run while its pace says so, until stop is closed. It asks
// whether or not somebody waits (a run can lose its last waiter at any moment, and
// is then held at once if it must be), but a run somebody waits for is not
// suspended (ctl) and says nothing about waiting (note).
func (g *Generator) paceLoop(c *call, input string, stop <-chan struct{}) {
	t := time.NewTicker(paceEvery)
	defer t.Stop()
	shown := ""
	for {
		pace := c.pace
		if pace == nil && g.holdFn != nil {
			pace = func() (bool, string) { return g.holdFn(input) }
		}
		if pace != nil {
			wait, why := pace()
			c.ctl.hold(wait)
			if c.note != nil {
				want := ""
				if wait && !c.ctl.isUrgent() {
					want = why
				}
				if want != shown {
					c.note(want)
					shown = want
				}
			}
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// legacyCharset returns the encoding to assume for a sidecar that is neither
// UTF-8 nor UTF-16 with a byte order mark, or "" when no flag is needed.
func legacyCharset(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil || bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return ""
	}
	if utf8.Valid(data) {
		return ""
	}
	return "WINDOWS-1252" // the usual encoding of old Western European .srt files
}

func (g *Generator) exec(ctx context.Context, args []string, p *ctl) (string, error) {
	cmd := exec.CommandContext(ctx, g.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if p.progress != nil {
		cmd.Stdout = &progressWriter{emit: p.progress}
	}
	proc.OwnGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	exited := make(chan error, 1)
	if err := proc.Start(cmd, func(err error) { exited <- err }); err != nil {
		return "", fmt.Errorf("start %s: %w", g.ffmpeg, err)
	}
	// If ply is killed from here on, the group goes with it: by the kernel (Linux), or by
	// the reaper (macOS), suspended or not.
	var reaper *exec.Cmd
	if g.reaper != "" {
		var err error
		if reaper, err = proc.StartReaper(g.reaper, cmd.Process.Pid); err != nil {
			g.log.Warn("subtitles: starting the ffmpeg reaper", "err", err)
		}
	}
	p.started(cmd.Process.Pid, g.niceness) // best effort
	err := <-exited
	p.ended()
	proc.StopReaper(reaper)
	return stderr.String(), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Prune deletes cached files of items that are gone, and of embedded tracks
// whose file has changed. live maps each current item id to its mtime. It
// returns the number of files removed.
func (g *Generator) Prune(live map[int64]int64) (int, error) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return 0, err
	}
	// The file of a conversion that is under way keeps its name, however long it
	// has been suspended or quiet.
	g.mu.Lock()
	writing := make(map[string]bool, len(g.inflight))
	for name := range g.inflight {
		writing[name+".tmp"] = true
	}
	g.mu.Unlock()
	removed := 0
	for _, e := range entries {
		if keepCached(e.Name(), live) || writing[e.Name()] {
			continue
		}
		if fi, err := e.Info(); strings.HasSuffix(e.Name(), ".tmp") && err == nil && time.Since(fi.ModTime()) < g.timeout {
			continue // being written by an in-flight conversion
		}
		if err := os.Remove(filepath.Join(g.dir, e.Name())); err == nil {
			removed++
		}
	}
	g.mu.Lock()
	for name := range g.failed {
		if !keepCached(name, live) {
			delete(g.failed, name)
		}
	}
	g.mu.Unlock()
	return removed, nil
}

// keepCached reports whether the cache file name (the WebVTT, or the record of its
// failure) still belongs to a live item. Sidecar tracks (ext<N>) only need their item
// to exist; embedded ones also need the item's mtime to be unchanged.
func keepCached(name string, live map[int64]int64) bool {
	base, ok := strings.CutSuffix(name, ".vtt")
	if !ok {
		base, ok = strings.CutSuffix(name, ".fail")
	}
	parts := strings.Split(base, "-")
	if !ok || len(parts) < 3 {
		return false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	mtime, ok := live[id]
	if !ok {
		return false
	}
	if strings.HasPrefix(parts[1], "ext") {
		return true
	}
	m, err := strconv.ParseInt(parts[2], 10, 64)
	return err == nil && m == mtime
}
