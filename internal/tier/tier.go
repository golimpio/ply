// Package tier decides when background work may run. ply serves three tiers, in
// order: playback, browsing, and the background queue (scans, metadata, caches).
// A Governor watches the first two and holds the third while they need the
// machine: the queue asks Check before it starts a job, and a job asks Wait
// between its steps (docs/M9-ARCHITECTURE.md, "Tiers").
package tier

import (
	"context"
	"path/filepath"
	"sync"
	"time"
)

// PlaybackLoad is what playback is doing right now, as the stream manager sees it.
type PlaybackLoad struct {
	Sessions   int      // plays in progress (HLS sessions and direct plays), paused ones included
	Active     int      // of them, not paused
	Transcodes int      // video transcodes running, any encoder
	Software   int      // of them, on the CPU (libx264)
	Waiting    int      // segment requests blocked on ffmpeg right now: a viewer is stalled
	Paths      []string // the media files being read
}

// Playback is the stream manager as the governor reads it.
type Playback interface {
	PlaybackLoad() PlaybackLoad
}

// Need says what a piece of background work uses, so that it is held only by
// what it would get in the way of.
type Need struct {
	// CPU is set for CPU-heavy work: probing, frame grabs, transcoding.
	CPU bool
	// Disks are paths the work reads from end to end (a folder to scan, a file to
	// index): it is held while a play reads from the same disk (for a network
	// share, the same server: they share the link).
	Disks []string
}

// Hold is the governor's answer: Held, and why, in words for the Activity page.
type Hold struct {
	Held   bool   `json:"held"`
	Reason string `json:"reason,omitempty"`
	// Browsing is set when browsing alone holds it: a hold that must not last for
	// ever (see Options.BrowseMaxHold).
	Browsing bool `json:"-"`
}

// State is what the Activity page shows about the tiers above the queue.
type State struct {
	Playback PlaybackLoad `json:"playback"`
	Browsing bool         `json:"browsing"`
	// Hold is what holds work that needs nothing in particular (no CPU, no disk):
	// a stalled viewer or someone browsing. Heavier work may be held for more.
	Hold Hold `json:"hold"`
}

// Options configures a Governor. Only Playback is required (nil means nothing plays).
type Options struct {
	Playback Playback
	// BrowseQuiet is how long background work stays held after the last browse
	// request ends (default 300 ms): a person going through pages makes requests in
	// bursts, and the queue must not slip in between them.
	BrowseQuiet time.Duration
	// BrowseMaxHold is the longest browsing alone holds a piece of work (default
	// 2 s): someone (or something) browsing without a pause must not starve the
	// queue, so held work then takes one step and asks again. Playback has no limit.
	BrowseMaxHold time.Duration
	// Poll is how often a held Wait looks again (default 250 ms). Notify makes it
	// look at once.
	Poll time.Duration
	// Disk names the disk a path is on, ok false when the path cannot be read
	// (missing, or a share not mounted yet). The default is diskOf.
	Disk func(path string) (key string, ok bool)
	Now  func() time.Time
}

// Governor is safe for concurrent use.
type Governor struct {
	o      Options
	browse browseMeter

	mu     sync.Mutex
	wake   chan struct{} // closed and replaced by Notify
	disks  map[string]diskEntry
	lastPB PlaybackLoad
}

type diskEntry struct {
	key     string
	ok      bool
	expires time.Time // for a path that could not be read: when to try it again
}

// missingRetry is how long a path that could not be read is taken as missing
// before it is looked at again: a stat of a dead mount can be slow.
const missingRetry = 10 * time.Second

// New returns a Governor.
func New(o Options) *Governor {
	if o.BrowseQuiet <= 0 {
		o.BrowseQuiet = 300 * time.Millisecond
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	if o.BrowseMaxHold <= 0 {
		o.BrowseMaxHold = 2 * time.Second
	}
	if o.Disk == nil {
		o.Disk = diskOf
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Governor{o: o, wake: make(chan struct{}), disks: map[string]diskEntry{}}
}

// Notify tells the governor that playback changed (a session started or ended, a
// viewer is waiting): held work looks again at once instead of at its next poll.
func (g *Governor) Notify() {
	g.mu.Lock()
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

// Changed returns a channel that is closed at the next Notify.
func (g *Governor) Changed() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.wake
}

// Poll is how often held work should look again without a Notify.
func (g *Governor) Poll() time.Duration { return g.o.Poll }

// BrowseMaxHold is the longest browsing alone may hold a piece of work.
func (g *Governor) BrowseMaxHold() time.Duration { return g.o.BrowseMaxHold }

func (g *Governor) playback() PlaybackLoad {
	if g.o.Playback == nil {
		return PlaybackLoad{}
	}
	pb := g.o.Playback.PlaybackLoad()
	g.mu.Lock()
	g.lastPB = pb
	g.mu.Unlock()
	return pb
}

// Check answers whether work with need n may run now.
func (g *Governor) Check(n Need) Hold {
	pb := g.playback()
	if pb.Waiting > 0 {
		return Hold{Held: true, Reason: "a viewer is waiting for video"}
	}
	if n.CPU && pb.Software > 0 {
		return Hold{Held: true, Reason: "a video is being converted on the CPU"}
	}
	if len(n.Disks) > 0 && len(pb.Paths) > 0 && g.sameDisk(n.Disks, pb.Paths) {
		return Hold{Held: true, Reason: "a video is playing from the same disk"}
	}
	if g.browse.busy(g.o.Now(), g.o.BrowseQuiet) {
		return Hold{Held: true, Reason: "someone is browsing", Browsing: true}
	}
	return Hold{}
}

// State reports the tiers above the queue, for the Activity page.
func (g *Governor) State() State {
	h := g.Check(Need{})
	g.mu.Lock()
	pb := g.lastPB
	g.mu.Unlock()
	return State{Playback: pb, Browsing: g.browse.busy(g.o.Now(), g.o.BrowseQuiet), Hold: h}
}

// Wait blocks until work with need n may run, or ctx ends (its error is returned).
// held, if not nil, is told each time the reason changes, and "" when it may run.
// Browsing alone holds it for at most BrowseMaxHold.
func (g *Governor) Wait(ctx context.Context, n Need, held func(reason string)) error {
	last := "\x00" // nothing reported yet
	report := func(r string) {
		if held != nil && r != last {
			held(r)
		}
		last = r
	}
	var browsedSince time.Time // when browsing alone began to hold this Wait
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := g.Check(n)
		if h.Browsing {
			now := g.o.Now()
			if browsedSince.IsZero() {
				browsedSince = now
			} else if now.Sub(browsedSince) >= g.o.BrowseMaxHold {
				h = Hold{} // browsing that never stops: one step, then ask again
			}
		} else {
			browsedSince = time.Time{}
		}
		if !h.Held {
			if last != "\x00" {
				report("")
			}
			return nil
		}
		report(h.Reason)
		wake := g.Changed()
		t := time.NewTimer(g.o.Poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// sameDisk reports whether any of disks is on the disk of any of playing.
func (g *Governor) sameDisk(disks, playing []string) bool {
	want := make(map[string]bool, len(playing))
	for _, p := range playing {
		if k, ok := g.disk(p); ok {
			want[k] = true
		}
	}
	for _, p := range disks {
		if k, ok := g.disk(p); ok && want[k] {
			return true
		}
	}
	return false
}

// disk is the disk of path, remembered: a path's disk does not change while it is
// mounted, and a stat of a slow share must not be paid on every check. A path that
// cannot be read (a folder about to be created, a share not mounted yet) is on its
// nearest readable parent's disk; that answer is not remembered, so that the path
// is looked at again once it may exist (after missingRetry).
func (g *Governor) disk(path string) (string, bool) {
	now := g.o.Now()
	g.mu.Lock()
	e, hit := g.disks[path]
	g.mu.Unlock()
	if !hit || (!e.ok && now.After(e.expires)) {
		k, ok := g.o.Disk(path)
		e = diskEntry{key: k, ok: ok}
		if !ok {
			e.expires = now.Add(missingRetry)
		}
		g.mu.Lock()
		if len(g.disks) >= 512 {
			g.disks = map[string]diskEntry{} // a long run plays many files: start over
		}
		g.disks[path] = e
		g.mu.Unlock()
	}
	if e.ok {
		return e.key, true
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", false
	}
	return g.disk(parent)
}
