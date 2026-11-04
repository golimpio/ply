package stream

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A plan built from the index a container carries (Cues, sync samples) is only
// as good as that index. If it leaves out keyframes, or lists others, ffmpeg
// writes a different number of fragments than the plan has GOPs, and every
// segment after the one it goes wrong at slips. The parsers refuse the indexes
// they can tell are wrong from the few reads they make; the planGuard checks the
// rest where it cannot be argued with: against the fragments ffmpeg writes.
//
// Every fragment of a run is compared with the plan as it lands: its video has
// to start where the plan says its GOP does (plus the timeline offset). Once the
// first two fragments of a run agree, the plan is final and the index is cached;
// the rest of the fragments are still compared, for the whole session, because
// an index can be wrong anywhere.
//
// Two things refine that:
//   - ffmpeg's timeline runs a constant number of frames ahead of the scan's when
//     the video has B-frames (+0.083 s for two frames at 24 fps, whatever the
//     number of B-frames), so the start of the first fragment compared in a run
//     is taken as the bias of that run (it has to be within fragTolerance), and
//     later starts are compared with the plan plus that bias.
//   - The first fragment may be enough: if it says how long it lasts, and that is
//     as long as the plan's GOP, the plan is final without waiting for the
//     second. The duration is only ever used to confirm. A timestamp gap before
//     the next keyframe (variable frame rate) makes a good fragment last less
//     than the plan's GOP; then it is the start of the next fragment that decides.
//
//   - A fragment that disagrees before the plan is final refutes it: the index
//     is distrusted for this file, the run is stopped and the plan is rebuilt
//     from a scan, as for a file with no index. Whoever waits for the plan
//     waits for that.
//   - One that disagrees afterwards fails the session: the plan is out, and with
//     it the playlist the player has. The index is distrusted and the cached copy
//     dropped, so that the next session scans.

// fragTolerance is how far a fragment's video may start from where the plan
// says (once the bias of the run is out), and last from how long it says, in
// seconds. The first sample's composition offset is accounted for; what is left
// is rounding of the timescales. It is the most, for a GOP of 0.75 s or more:
// shorter ones get a third of their length. It is also how big the bias of a run
// may be.
const fragTolerance = 0.25

// planGuard is the state of that check for one session.
type planGuard struct {
	kf       []float64 // the index as read: what is cached once confirmed
	itemID   int64
	deadline time.Time // the plan stands, unconfirmed and uncached, after this

	once sync.Once
	done chan struct{} // closed when the plan is final
	err  error         // why there is none; set before done is closed

	// refuted is set (under the session's mutex) when a fragment showed the index
	// wrong before the plan was final. From then on rebuildPlan, and nothing else,
	// finishes the guard, and the plan is the scan's, which nothing is compared
	// with.
	refuted bool
}

func newPlanGuard(kf []float64, itemID int64, deadline time.Time) *planGuard {
	return &planGuard{kf: kf, itemID: itemID, deadline: deadline, done: make(chan struct{})}
}

func (g *planGuard) finish(err error) {
	g.once.Do(func() {
		g.err = err
		close(g.done)
	})
}

func (g *planGuard) final() bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}

// awaitPlan blocks until the plan can be relied on: confirmed, rebuilt, or given
// up on after the deadline. It fails with the error of a session that has failed
// or whose plan could not be rebuilt, and with ctx's when the caller leaves.
func (s *Session) awaitPlan(ctx context.Context) error {
	g := s.guard
	if g == nil {
		return nil
	}
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err // the caller left: leave the session alone
		}
		s.mu.Lock()
		s.advanceLocked()
		s.verifyLocked()
		failure := s.failure
		s.mu.Unlock()
		if failure != nil {
			return failure
		}
		if g.final() {
			return g.err
		}
		s.touch() // somebody is waiting: the session is in use
		select {
		case <-g.done:
			return g.err
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopCh:
			return ErrClosed
		case <-poll.C:
		}
	}
}

// verified compares the fragments that have landed with the plan, and says why
// the session has failed, if it has. A segment is looked at before it is served.
func (s *Session) verified() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advanceLocked()
	s.verifyLocked()
	return s.failure
}

// verifyLocked compares the fragments of the current run with the plan. It is
// called whenever the fragments on disk may have changed: by the monitor, before
// a segment is served and by the wait for the plan.
func (s *Session) verifyLocked() {
	g := s.guard
	if g == nil || g.refuted || s.failure != nil {
		return
	}
	if s.closed {
		g.finish(ErrClosed)
		return
	}
	s.compareLocked()
	if g.final() || g.refuted || s.failure != nil {
		return
	}
	// The plan is not settled yet. A run that failed outright is not going to
	// settle it: let the wait for the segment report why.
	if r := s.run; r != nil && !r.replaced && r.exited() && r.err != nil {
		s.m.log.Warn("ffmpeg failed before the keyframe index could be checked", "session", s.ID, "path", filepath.Base(s.req.Path))
		g.finish(nil)
		return
	}
	if time.Now().After(g.deadline) {
		s.m.log.Warn("keyframe index not confirmed in time: the plan is used as it is, and not cached",
			"session", s.ID, "path", filepath.Base(s.req.Path))
		g.finish(nil)
	}
}

// compareLocked compares the fragments of the current run that have not been
// compared yet, in order, and settles the plan: refuted by any whose start
// disagrees (or, if it was final, the session fails), else confirmed by the first
// one if it says how long it lasts and that is as long as the plan's GOP, or by a
// pair. Whatever has landed is compared before the plan is confirmed, so that a
// bad fragment behind a good pair is found before anybody has the playlist.
func (s *Session) compareLocked() {
	r := s.run
	if r == nil || r.replaced {
		return
	}
	g := s.guard
	keyframes := s.plan.Keyframes
	settled := false
	for r.start+r.checked < r.next && r.start+r.checked < len(keyframes) {
		gop := r.start + r.checked
		if gop < s.pruned { // the player is past it, and the file is gone
			r.checked++
			continue
		}
		if r.video == nil {
			v, err := readFragInit(filepath.Join(s.dir, r.initName))
			if err != nil {
				return // not written yet, or not readable: try again
			}
			r.video = &v
		}
		span, err := fragmentVideoSpan(s.gopPath(gop), *r.video)
		if errors.Is(err, fs.ErrNotExist) { // pruned while we looked
			r.checked++
			continue
		}
		if err != nil {
			return
		}
		want := fragWant{start: keyframes[gop] + TimelineOffset}
		if gop+1 < len(keyframes) {
			want.span, want.haveSpan = keyframes[gop+1]-keyframes[gop], true
		}
		if !r.haveBias {
			// The bias of the run: how far its fragments are from the scan's times.
			r.bias, r.haveBias = span.start-want.start, true
			if math.Abs(r.bias) > fragTolerance {
				s.disagreeLocked(gop, startWhy(span.start, want.start))
				return
			}
		} else if why := startDisagrees(span.start-r.bias, want); why != "" {
			s.disagreeLocked(gop, why)
			return
		}
		r.checked++
		r.agreed++
		// The first fragment settles it if it was held to a duration and that is
		// the plan's; else it takes two, a pair of keyframes. Or the plan has no
		// more GOPs.
		if r.agreed >= 2 || durationAgrees(span, want) || r.start+r.checked >= len(keyframes) {
			settled = true
		}
	}
	if settled && !g.final() {
		s.confirmLocked()
	}
}

// fragWant is where the plan says a fragment is: its video starts at start and,
// if there is a next GOP, lasts span.
type fragWant struct {
	start, span float64
	haveSpan    bool
}

// tolerance is how far a fragment may be from the plan: fragTolerance, and a
// third of the GOP for one shorter than that.
func (w fragWant) tolerance() float64 {
	if w.haveSpan {
		return min(fragTolerance, w.span/3)
	}
	return fragTolerance
}

// startDisagrees says how the start of a fragment (less the bias of its run) is
// not where the plan has its GOP, or "" if it is.
func startDisagrees(start float64, want fragWant) string {
	if math.Abs(start-want.start) > want.tolerance() {
		return startWhy(start, want.start)
	}
	return ""
}

func startWhy(start, want float64) string {
	return fmt.Sprintf("the fragment starts at %.3f s and the plan has its GOP at %.3f s", start, want)
}

// durationAgrees reports whether the fragment says how long it lasts and that is
// as long as the plan's GOP, within the tolerance. It can only confirm: a gap
// before the next keyframe makes a good fragment last less (see the start of
// this file).
func durationAgrees(got fragSpan, want fragWant) bool {
	return got.haveDur && want.haveSpan && math.Abs(got.dur-want.span) <= want.tolerance()
}

// disagreeLocked handles a fragment whose start is not where the plan says: it
// refutes the plan if it is not final, and fails the session if it is.
func (s *Session) disagreeLocked(gop int, why string) {
	if s.guard.final() {
		s.failLocked(gop, why)
	} else {
		s.refuteLocked(gop, why)
	}
}

// confirmLocked makes the plan final and caches the index it came from (unless
// another session has found the index of the file wrong since).
func (s *Session) confirmLocked() {
	g := s.guard
	s.m.log.Info("keyframe index confirmed by the first fragments", "session", s.ID, "path", filepath.Base(s.req.Path))
	g.finish(nil)
	if s.m.o.Cache != nil && g.itemID > 0 {
		s.m.wg.Add(1) // the caller is a monitor or a waiter of a live session: the count is not zero
		go func() {
			defer s.m.wg.Done()
			ctx, cancel := context.WithTimeout(s.m.ctx, 30*time.Second)
			defer cancel()
			defer s.m.lockIndexCache(s.req.Path)() // against the drop by whichever session finds it wrong
			if !s.m.indexDistrusted(s.req.Path) {  // found wrong since, by this session or another
				s.m.cacheKeyframes(ctx, g.itemID, g.kf)
			}
		}()
	}
}

// refuteLocked handles a fragment that is not where the plan says before the plan
// is final: the index is not used for this file again, the run stops, and the
// plan is rebuilt from a scan in the background; whoever waits for the plan waits
// for that. Only rebuildPlan finishes the guard from here on.
func (s *Session) refuteLocked(gop int, why string) {
	g := s.guard
	s.m.log.Warn("keyframe index disagrees with the stream: rebuilding it with a scan",
		"session", s.ID, "path", filepath.Base(s.req.Path), "gop", gop, "why", why)
	g.refuted = true
	s.m.distrustIndex(s.req.Path)
	s.stopRunLocked()
	s.clearFromLocked(0) // the fragments of the old numbering are of no use to the new
	s.pruned = 0
	s.m.wg.Add(1)
	go func() {
		defer s.m.wg.Done()
		g.finish(s.rebuildPlan())
	}()
}

// failLocked handles a fragment that is not where the plan says after the plan
// is final, and was served from: there is no taking back the playlist. The index
// is not used for this file again and the cached copy is dropped, so that the
// next session scans; this one fails.
func (s *Session) failLocked(gop int, why string) {
	g := s.guard
	s.m.log.Warn("keyframe index disagrees with the stream after it was confirmed: the session fails",
		"session", s.ID, "path", filepath.Base(s.req.Path), "gop", gop, "why", why)
	s.m.distrustIndex(s.req.Path)
	s.failure = fmt.Errorf("%w: %s", ErrIndex, why)
	s.stopRunLocked()
	if s.m.o.Cache != nil && g.itemID > 0 {
		s.m.wg.Add(1)
		go func() {
			defer s.m.wg.Done()
			ctx, cancel := context.WithTimeout(s.m.ctx, 30*time.Second)
			defer cancel()
			defer s.m.lockIndexCache(s.req.Path)() // after the write of the index, if that is under way
			s.m.cacheKeyframes(ctx, g.itemID, nil)
		}()
	}
}

// rebuildPlan scans the file and restarts the session on the plan that makes.
// The scan is the manager's: one at a time per file, however many sessions want
// it, not stopped by a session leaving (the next play has its result), and
// cached when it is done. This only waits for it.
func (s *Session) rebuildPlan() error {
	ctx, cancel := context.WithCancel(s.m.ctx)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// Drop whatever is cached for the item: it is this index, or older than the
	// file. An empty entry reads as a miss. The scan's result replaces it.
	unlock := s.m.lockIndexCache(s.req.Path)
	s.m.cacheKeyframes(ctx, s.guard.itemID, nil)
	unlock()
	kf, _, err := s.m.keyframes(ctx, s.req, true)
	if err != nil {
		if ctx.Err() != nil {
			s.m.log.Debug("session over while its keyframes were being scanned", "session", s.ID)
			return ErrClosed
		}
		s.m.log.Error("keyframe scan after a bad index failed", "session", s.ID, "err", err)
		if errors.Is(err, context.DeadlineExceeded) { // the scan took longer than ExtractTimeout
			return fmt.Errorf("%w: scanning %s: %v", ErrTimeout, filepath.Base(s.req.Path), err)
		}
		return fmt.Errorf("%w: %v", ErrIndex, err)
	}
	plan, err := NewPlan(kf, s.req.DurationSec, s.m.o.SegmentSec)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrIndex, filepath.Base(s.req.Path), err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.plan = plan
	s.lastSeg = plan.SegmentAt(s.req.StartSec)
	if err := s.startRunLocked(plan.Segments[s.lastSeg].First); err != nil {
		return err
	}
	return nil
}

// ---- reading fragments -------------------------------------------------------

// fragVideo is the video track of an init segment.
type fragVideo struct {
	track uint32
	scale int64
}

var errFragFound = errors.New("found")

// readFragInit finds the video track of the init segment at path.
func readFragInit(path string) (fragVideo, error) {
	f, err := os.Open(path)
	if err != nil {
		return fragVideo{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fragVideo{}, err
	}
	p := &mp4Parser{x: &indexReader{r: f, size: fi.Size()}}
	ctx := context.Background()
	var v fragVideo
	err = p.walk(ctx, mp4Box{end: fi.Size()}, func(moov mp4Box) error {
		if moov.typ != "moov" {
			return nil
		}
		return p.walk(ctx, moov, func(trak mp4Box) error {
			if trak.typ != "trak" || v.track != 0 {
				return nil
			}
			t, err := p.readTrack(ctx, trak)
			if err != nil {
				return err
			}
			if h, err := p.handler(t.hdlr); err != nil || h != "vide" || t.mdhd.typ == "" {
				return err
			}
			var hdr [24]byte
			n := min(t.mdhd.size(), int64(len(hdr)))
			if err := p.x.readAt(hdr[:n], t.mdhd.body); err != nil {
				return err
			}
			scale, err := timescaleOf(hdr[:n])
			if err != nil {
				return err
			}
			// The track ID is in the tkhd, which readTrack does not keep: find it.
			id, err := p.trackID(ctx, trak)
			if err != nil {
				return err
			}
			v = fragVideo{track: id, scale: scale}
			return nil
		})
	})
	if err != nil {
		return fragVideo{}, err
	}
	if v.track == 0 {
		return fragVideo{}, errors.New("no video track in the init segment")
	}
	return v, nil
}

// trackID reads the track_ID of a trak.
func (p *mp4Parser) trackID(ctx context.Context, trak mp4Box) (uint32, error) {
	var id uint32
	err := p.walk(ctx, trak, func(b mp4Box) error {
		if b.typ != "tkhd" {
			return nil
		}
		var h [24]byte
		n := min(b.size(), int64(len(h)))
		if err := p.x.readAt(h[:n], b.body); err != nil {
			return err
		}
		at := 12
		if n > 0 && h[0] == 1 {
			at = 20
		}
		if int(n) < at+4 {
			return errMalformedMP4
		}
		id = binary.BigEndian.Uint32(h[at:])
		return errFragFound
	})
	if err != nil && err != errFragFound {
		return 0, err
	}
	return id, nil
}

// fragSpan is what a fragment file says of its video: when its first sample is
// presented, and, if its samples' durations are there, how long they last in all.
type fragSpan struct {
	start, dur float64 // seconds
	haveDur    bool
}

// fragmentVideoStart is when the first video sample of the fragment file at
// path is presented, in seconds.
func fragmentVideoStart(path string, v fragVideo) (float64, error) {
	s, err := fragmentVideoSpan(path, v)
	return s.start, err
}

// fragmentVideoSpan reads the span of the video of the fragment file at path:
// the base decode time of the track's fragment plus the composition offset of
// its first sample, and the sum of the durations of its samples (from the trun,
// or the default of the tfhd).
func fragmentVideoSpan(path string, v fragVideo) (fragSpan, error) {
	f, err := os.Open(path)
	if err != nil {
		return fragSpan{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fragSpan{}, err
	}
	p := &mp4Parser{x: &indexReader{r: f, size: fi.Size()}}
	ctx := context.Background()
	var out fragSpan
	found := false
	err = p.walk(ctx, mp4Box{end: fi.Size()}, func(moof mp4Box) error {
		if moof.typ != "moof" {
			return nil
		}
		return p.walk(ctx, moof, func(traf mp4Box) error {
			if traf.typ != "traf" {
				return nil
			}
			var (
				track         uint32
				decode        int64
				haveDecode    bool
				defaultDur    int64
				haveDefault   bool
				trun          []byte
				offset, total int64
				haveTotal     bool
			)
			err := p.walk(ctx, traf, func(b mp4Box) error {
				switch b.typ {
				case "tfhd", "tfdt":
					var d [32]byte
					n := min(b.size(), int64(len(d)))
					if err := p.x.readAt(d[:n], b.body); err != nil {
						return err
					}
					return parseTfhdTfdt(b.typ, d[:n], &track, &decode, &haveDecode, &defaultDur, &haveDefault)
				case "trun":
					data, err := p.x.readBody(b.body, b.size(), 1<<20)
					if err != nil {
						return err
					}
					trun = data
				}
				return nil
			})
			if err != nil {
				return err
			}
			if track != v.track || !haveDecode {
				return nil
			}
			offset, total, haveTotal = parseTrun(trun, defaultDur, haveDefault)
			out = fragSpan{start: float64(decode+offset) / float64(v.scale), dur: float64(total) / float64(v.scale), haveDur: haveTotal}
			found = true
			return errFragFound
		})
	})
	if err != nil && err != errFragFound {
		return fragSpan{}, err
	}
	if !found {
		return fragSpan{}, errors.New("no video fragment")
	}
	return out, nil
}

// parseTfhdTfdt reads the track ID and default sample duration of a tfhd, or the
// base media decode time of a tfdt, from the start of its payload.
func parseTfhdTfdt(typ string, d []byte, track *uint32, decode *int64, haveDecode *bool, defaultDur *int64, haveDefault *bool) error {
	switch typ {
	case "tfhd":
		if len(d) < 8 {
			return errMalformedMP4
		}
		*track = binary.BigEndian.Uint32(d[4:])
		flags := binary.BigEndian.Uint32(d) & 0xFFFFFF
		at := 8
		if flags&0x1 != 0 { // base data offset
			at += 8
		}
		if flags&0x2 != 0 { // sample description index
			at += 4
		}
		if flags&0x8 != 0 && len(d) >= at+4 {
			*defaultDur, *haveDefault = int64(binary.BigEndian.Uint32(d[at:])), true
		}
	case "tfdt":
		switch {
		case len(d) >= 12 && d[0] == 1:
			*decode = int64(binary.BigEndian.Uint64(d[4:]))
		case len(d) >= 8 && d[0] == 0:
			*decode = int64(binary.BigEndian.Uint32(d[4:]))
		default:
			return errMalformedMP4
		}
		*haveDecode = true
	}
	return nil
}

// parseTrun reads a trun payload (from the version and flags on): the
// composition time offset of its first sample (0 if it has none), and the sum of
// the durations of its samples, the default one for each if it has none of its
// own. haveDur is false if neither is there or the samples are cut off.
func parseTrun(b []byte, defaultDur int64, haveDefault bool) (offset, dur int64, haveDur bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	flags := binary.BigEndian.Uint32(b) & 0xFFFFFF
	count := int64(binary.BigEndian.Uint32(b[4:]))
	at := 8 // after the sample count
	if flags&0x1 != 0 {
		at += 4 // data offset
	}
	if flags&0x4 != 0 {
		at += 4 // first sample flags
	}
	// A sample is, in this order, a duration, a size, flags and a composition
	// offset, each if its flag is set.
	stride, durAt, ctoAt := 0, -1, -1
	if flags&0x100 != 0 {
		durAt = stride
		stride += 4
	}
	if flags&0x200 != 0 {
		stride += 4
	}
	if flags&0x400 != 0 {
		stride += 4
	}
	if flags&0x800 != 0 {
		ctoAt = stride
		stride += 4
	}
	if ctoAt >= 0 && len(b) >= at+ctoAt+4 {
		v := binary.BigEndian.Uint32(b[at+ctoAt:])
		offset = int64(v)
		if b[0] != 0 {
			offset = int64(int32(v))
		}
	}
	switch {
	case count == 0:
		return offset, 0, false
	case durAt >= 0 && stride > 0:
		if int64(len(b)-at)/int64(stride) < count {
			return offset, 0, false // the samples are cut off
		}
		for i := int64(0); i < count; i++ {
			dur += int64(binary.BigEndian.Uint32(b[at+int(i)*stride+durAt:]))
		}
		return offset, dur, true
	case haveDefault:
		return offset, count * defaultDur, true
	}
	return offset, 0, false
}
