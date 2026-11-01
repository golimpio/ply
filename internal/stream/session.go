package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// keepBehind is how many segments behind the player are kept on disk.
const keepBehind = 3

// Session is one playback of one file. Every method is safe for concurrent
// use; players fetch several segments at once.
type Session struct {
	ID   string
	m    *Manager
	req  Request
	plan Plan
	dir  string

	lastUsed atomic.Int64 // unix nanoseconds of the last request

	mu       sync.Mutex
	run      *run
	runSeq   int
	initPath string // the init segment served to players (from the first run that wrote one)
	lastSeg  int    // segment most recently requested
	pruned   int    // GOP files below this index are already removed
	closed   bool
	fellBack bool // the GPU transcode failed and the software one took over

	// Segment requests in progress, by arrival number. Only the newest one may
	// replace a running ffmpeg (see ensureGOP).
	reqSeq  uint64
	waiting map[uint64]struct{}

	stopCh   chan struct{}
	stopOnce sync.Once
}

// run is one ffmpeg process producing GOP fragments from GOP start onwards.
type run struct {
	id       int
	start    int
	next     int // first GOP not yet on disk (maintained by advanceLocked)
	initName string
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{} // closed when the process has exited
	err      error         // exit status, valid once done is closed
	stderr   *tailBuffer
	paused   bool
	replaced bool // stopped on purpose (seek or session end)
}

func (r *run) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *run) failure() string {
	msg := r.stderr.String()
	if r.err != nil {
		if msg != "" {
			return fmt.Sprintf("%v: %s", r.err, msg)
		}
		return r.err.Error()
	}
	if msg != "" {
		return msg
	}
	return "ffmpeg exited without output"
}

func newSession(m *Manager, id, dir string, r Request, plan Plan) *Session {
	s := &Session{ID: id, m: m, req: r, plan: plan, dir: dir, stopCh: make(chan struct{})}
	s.touch()
	return s
}

// Plan returns the segment layout (read-only).
func (s *Session) Plan() Plan { return s.plan }

// Dir is the session's temp directory.
func (s *Session) Dir() string { return s.dir }

func (s *Session) touch() { s.lastUsed.Store(time.Now().UnixNano()) }

// Playlist renders the VOD playlist; suffix is appended to every URI.
func (s *Session) Playlist(suffix string) string {
	s.touch()
	return s.plan.Playlist(suffix)
}

func (s *Session) gopPath(g int) string { return filepath.Join(s.dir, fmt.Sprintf(gopPattern, g)) }

// startRunLocked replaces the running ffmpeg, if any, by one that produces
// fragments from GOP g on. Fragments from g on that an earlier run left
// behind are removed so the new run's progress can be read off the directory.
func (s *Session) startRunLocked(g int) error {
	if s.closed {
		return ErrClosed
	}
	// A run that died by itself before anything was served is the signal that
	// the GPU path does not work for this file.
	if old := s.run; old != nil && old.exited() && old.err != nil && !old.replaced {
		s.fallBackLocked(old.failure())
	}
	s.stopRunLocked()
	s.clearFromLocked(g)
	if g < s.pruned {
		s.pruned = g
	}

	s.runSeq++
	r := &run{id: s.runSeq, start: g, next: g, done: make(chan struct{}), stderr: &tailBuffer{}}
	r.initName = fmt.Sprintf("init%d.mp4", r.id)

	spec := RunSpec{
		Input:      s.req.Path,
		VideoIndex: s.req.VideoIndex,
		AudioIndex: s.req.AudioIndex,
		Video:      VideoSpec{Codec: s.req.VideoCodec},
		Audio:      AudioSpec{Codec: s.req.AudioCodec, Copy: s.req.AudioCopy},
		SeekSec:    SeekTarget(s.req.Container, s.plan.Keyframes, g),
		StartGOP:   g,
		InitName:   r.initName,
		Offset:     TimelineOffset,
		SegmentSec: s.m.o.SegmentSec,
	}
	if s.req.Video != nil {
		spec.Video = *s.req.Video
	}
	if !spec.Video.copy() { // seek exactly: the keyframes are ours, not the source's
		spec.SeekSec = 0
		if g > 0 {
			spec.SeekSec = s.plan.TimeOf(g)
		}
	}

	ctx, cancel := context.WithCancel(s.m.ctx)
	cmd := exec.CommandContext(ctx, s.m.o.FFmpeg, BuildArgs(spec)...)
	cmd.Dir = s.dir
	cmd.Stderr = r.stderr
	cmd.WaitDelay = 3 * time.Second
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	r.cmd, r.cancel = cmd, cancel
	// If ply itself dies without cleaning up, the next one finds this and kills
	// the ffmpeg (it has its own process group, so nobody else will).
	if err := os.WriteFile(filepath.Join(s.dir, pidFile), []byte(fmt.Sprintf("%d %s\n", cmd.Process.Pid, cmd.Path)), 0o600); err != nil {
		s.m.log.Warn("record ffmpeg pid", "session", s.ID, "err", err)
	}
	started := time.Now()
	go func() {
		r.err = cmd.Wait()
		diedByItself := r.err != nil && ctx.Err() == nil // not stopped by us
		cancel()
		cpu, rss := usage(cmd.ProcessState)
		s.m.log.Info("ffmpeg stopped", "session", s.ID, "run", r.id, "from_gop", g,
			"wall", time.Since(started).Round(time.Millisecond), "cpu", cpu.Round(time.Millisecond),
			"max_rss_mb", rss>>20, "exit", exitText(r.err))
		if diedByItself {
			// What ffmpeg said is the only account of why (a client just gets a 500).
			s.m.log.Warn("ffmpeg failed", "session", s.ID, "run", r.id, "exit", exitText(r.err), "stderr", r.stderr.String())
		}
		close(r.done)
	}()
	s.run = r
	s.m.log.Debug("ffmpeg started", "session", s.ID, "run", r.id, "gop", g, "seek", spec.SeekSec)
	return nil
}

// fallBackLocked switches a failed GPU transcode to its software fallback,
// once, and only while no init segment has been served (the software
// encoder's headers would not match it). The caller starts the new run.
func (s *Session) fallBackLocked(failure string) bool {
	fb := s.req.Fallback
	if fb == nil || s.fellBack || s.initPath != "" || s.closed {
		return false
	}
	s.fellBack = true
	s.req.Video = fb
	s.m.log.Warn("GPU transcode failed, falling back to software", "session", s.ID, "err", failure)
	return true
}

// stopRunLocked kills the current ffmpeg (even a paused one) and waits for
// it to be gone.
func (s *Session) stopRunLocked() {
	r := s.run
	if r == nil {
		return
	}
	r.replaced = true
	r.cancel()
	<-r.done
	s.run = nil
	_ = os.Remove(filepath.Join(s.dir, pidFile))
	if p := filepath.Join(s.dir, r.initName); p != s.initPath {
		_ = os.Remove(p)
	}
}

// clearFromLocked removes fragment files (finished or not) from GOP g on.
func (s *Session) clearFromLocked(g int) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if idx, ok := gopIndex(e.Name()); ok && idx >= g {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// gopIndex parses gop00042.m4s and gop00042.m4s.tmp.
func gopIndex(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "gop")
	if !ok {
		return 0, false
	}
	digits, ext, ok := strings.Cut(rest, ".m4s")
	if !ok || (ext != "" && ext != ".tmp") {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// advanceLocked moves the current run's progress marker past the fragments
// that have appeared on disk.
func (s *Session) advanceLocked() {
	r := s.run
	if r == nil {
		return
	}
	for r.next < len(s.plan.Keyframes) {
		if _, err := os.Stat(s.gopPath(r.next)); err != nil {
			return
		}
		r.next++
	}
}

func (s *Session) pauseLocked(r *run, pause bool) {
	if r.paused == pause || r.exited() {
		return
	}
	if err := signalGroup(r.cmd, pause); err != nil {
		s.m.log.Warn("signal ffmpeg", "session", s.ID, "stop", pause, "err", err)
		return
	}
	r.paused = pause
}

// monitor enforces the idle timeout, throttles ffmpeg when it is far ahead of
// the player and prunes fragments the player is done with.
func (s *Session) monitor() {
	t := time.NewTicker(s.m.o.Tick)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, s.lastUsed.Load())) > s.m.o.IdleTimeout {
			s.m.log.Info("session idle, stopping", "session", s.ID)
			s.m.Delete(s.ID)
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.advanceLocked()
		if r := s.run; r != nil && !r.exited() {
			ahead := s.plan.SegmentOfGOP(min(r.next, len(s.plan.Keyframes)-1)) - s.lastSeg
			switch throttle(ahead, r.paused, s.m.o.ThrottleHigh, s.m.o.ThrottleLow) {
			case actionPause:
				s.pauseLocked(r, true)
			case actionResume:
				s.pauseLocked(r, false)
			}
		}
		limit := s.plan.Segments[max(s.lastSeg-keepBehind, 0)].First
		for g := s.pruned; g < limit; g++ {
			_ = os.Remove(s.gopPath(g))
		}
		s.pruned = max(s.pruned, limit)
		s.mu.Unlock()
	}
}

type throttleAction int

const (
	actionNone throttleAction = iota
	actionPause
	actionResume
)

// throttle decides whether ffmpeg, ahead segments in front of the player,
// should be paused or resumed. The gap between the two thresholds keeps it
// from flapping.
func throttle(ahead int, paused bool, high, low int) throttleAction {
	switch {
	case !paused && ahead > high:
		return actionPause
	case paused && ahead < low:
		return actionResume
	}
	return actionNone
}

// stop ends the session: the process group is killed and awaited, then the
// temp directory removed. It is idempotent.
func (s *Session) stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.mu.Lock()
		s.closed = true
		s.stopRunLocked()
		s.mu.Unlock()
		if err := os.RemoveAll(s.dir); err != nil {
			s.m.log.Warn("remove session dir", "session", s.ID, "err", err)
		}
	})
}

// gopFile returns the finished fragment for GOP g.
func (s *Session) gopFile(g int) (fileInfo, bool) { return checkFile(s.gopPath(g), "gop") }

// beginRequest registers a segment request and returns its arrival number
// and the function that ends it.
func (s *Session) beginRequest(idx int) (id uint64, end func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeg = idx
	s.reqSeq++
	id = s.reqSeq
	if s.waiting == nil {
		s.waiting = map[uint64]struct{}{}
	}
	s.waiting[id] = struct{}{}
	return id, func() {
		s.mu.Lock()
		delete(s.waiting, id)
		s.mu.Unlock()
	}
}

// newestLocked reports whether request id is the most recent one still in
// progress.
func (s *Session) newestLocked(id uint64) bool {
	for other := range s.waiting {
		if other > id {
			return false
		}
	}
	return true
}

// beyondRunLocked reports whether the running ffmpeg r will not produce GOP g
// soon enough, or ever: g is before its start, too far past its output, or
// was written and has since been pruned (ffmpeg never rewrites a fragment).
func (s *Session) beyondRunLocked(r *run, g int) bool {
	switch {
	case g < r.start:
		return true
	case s.plan.TimeOf(g) > s.plan.TimeOf(r.next)+s.m.o.Lookahead:
		return true
	case g < r.next:
		_, err := os.Stat(s.gopPath(g))
		return errors.Is(err, fs.ErrNotExist)
	}
	return false
}

// ensureGOP blocks until fragment g is complete on disk, starting or
// restarting ffmpeg when it is not going to get there by itself. req is the
// arrival number of the segment request g belongs to.
//
// One ffmpeg serves the session, so concurrent requests for places far apart
// cannot all be served at once. The newest request owns it: it alone may
// replace a running ffmpeg, and older ones wait (they are served if the run
// happens to reach them, or take over when the newer requests are done, or
// time out). Without this rule two such waiters would kill each other's ffmpeg
// on every poll, forever.
func (s *Session) ensureGOP(ctx context.Context, g int, req uint64) error {
	timeout := time.NewTimer(s.m.o.WaitTimeout)
	defer timeout.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()

	var mine *run // the run this call started
	for {
		if _, ok := s.gopFile(g); ok {
			return nil
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return ErrClosed
		}
		s.advanceLocked()
		r := s.run
		switch {
		case mine != nil && mine.exited() && !mine.replaced:
			msg := mine.failure()
			if _, ok := s.gopFile(g); ok { // finished just before it exited
				s.mu.Unlock()
				return nil
			}
			if s.fallBackLocked(msg) {
				if err := s.startRunLocked(g); err != nil {
					s.mu.Unlock()
					return err
				}
				mine = s.run
				break
			}
			s.mu.Unlock()
			return fmt.Errorf("ffmpeg did not produce segment data: %s", msg)
		case r == nil || r.exited():
			if err := s.startRunLocked(g); err != nil {
				s.mu.Unlock()
				return err
			}
			mine = s.run
		case s.beyondRunLocked(r, g):
			// Outside what the running ffmpeg will produce soon: seek, unless a
			// newer request is the one ffmpeg is working for.
			if !s.newestLocked(req) {
				break
			}
			if err := s.startRunLocked(g); err != nil {
				s.mu.Unlock()
				return err
			}
			mine = s.run
		case r.paused:
			s.pauseLocked(r, false)
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return ErrTimeout
		case <-s.stopCh:
			return ErrClosed
		case <-poll.C:
		}
	}
}

// OpenSegment returns segment idx once all its fragments are complete. The
// caller closes it.
func (s *Session) OpenSegment(ctx context.Context, idx int) (*Media, error) {
	if idx < 0 || idx >= len(s.plan.Segments) {
		return nil, ErrNoSegment
	}
	s.touch()
	req, end := s.beginRequest(idx)
	defer end()
	seg := s.plan.Segments[idx]

	for attempt := 0; ; attempt++ {
		for g := seg.First; g < seg.End; g++ {
			if err := s.ensureGOP(ctx, g, req); err != nil {
				return nil, err
			}
		}
		m, err := s.openGOPs(seg)
		if err == nil {
			s.touch()
			return m, nil
		}
		// A fragment vanished between the check and the open (a seek
		// cleared it). Ask for it again, once or twice.
		if !os.IsNotExist(err) || attempt >= 2 {
			return nil, err
		}
	}
}

func (s *Session) openGOPs(seg Segment) (*Media, error) {
	p := &pieces{}
	for g := seg.First; g < seg.End; g++ {
		info, ok := s.gopFile(g)
		if !ok {
			p.Close()
			return nil, os.ErrNotExist
		}
		f, err := os.Open(info.path)
		if err != nil {
			p.Close()
			return nil, err
		}
		off := info.mediaOff // drop the styp of every fragment but the first
		if g == seg.First {
			off = 0
		}
		p.add(f, off, info.size-off)
	}
	return &Media{pieces: p}, nil
}

// Media is a finished segment or init segment: an io.ReadSeekCloser.
type Media struct{ *pieces }

// OpenInit returns the init segment. It is the one written by the first
// ffmpeg run that produced one; later runs write identical headers.
func (s *Session) OpenInit(ctx context.Context) (io.ReadSeekCloser, error) {
	s.touch()
	timeout := time.NewTimer(s.m.o.WaitTimeout)
	defer timeout.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()

	var mine *run
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if s.initPath == "" && s.run != nil {
			p := filepath.Join(s.dir, s.run.initName)
			if _, ok := checkFile(p, "init"); ok {
				s.initPath = p
			}
		}
		path, r := s.initPath, s.run
		if path == "" {
			switch {
			case mine != nil && mine.exited() && !mine.replaced:
				msg := mine.failure()
				if s.fallBackLocked(msg) {
					if err := s.startRunLocked(s.plan.Segments[min(s.lastSeg, len(s.plan.Segments)-1)].First); err != nil {
						s.mu.Unlock()
						return nil, err
					}
					mine = s.run
					break
				}
				s.mu.Unlock()
				return nil, fmt.Errorf("ffmpeg did not produce an init segment: %s", msg)
			case r == nil || r.exited():
				if err := s.startRunLocked(s.plan.Segments[min(s.lastSeg, len(s.plan.Segments)-1)].First); err != nil {
					s.mu.Unlock()
					return nil, err
				}
				mine = s.run
			}
		}
		s.mu.Unlock()

		if path != "" {
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			return f, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, ErrTimeout
		case <-s.stopCh:
			return nil, ErrClosed
		case <-poll.C:
		}
	}
}

func exitText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}
