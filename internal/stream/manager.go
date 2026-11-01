package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var (
	// ErrTooManySessions is returned by Create at the session limit.
	ErrTooManySessions = errors.New("stream: too many active sessions")
	// ErrClosed is returned once the manager or session has been closed.
	ErrClosed = errors.New("stream: closed")
	// ErrNoSegment is returned for a segment index outside the playlist.
	ErrNoSegment = errors.New("stream: no such segment")
	// ErrTimeout is returned when a segment is not produced in time.
	ErrTimeout = errors.New("stream: timed out waiting for ffmpeg")
)

// KeyframeCache persists keyframe indexes between runs (store.Store).
type KeyframeCache interface {
	Keyframes(ctx context.Context, itemID int64) ([]float64, bool, error)
	SetKeyframes(ctx context.Context, itemID int64, times []float64) error
}

// Options configures a Manager. Zero values pick the defaults in brackets.
type Options struct {
	FFmpeg      string        // ["ffmpeg"]
	FFprobe     string        // ["ffprobe"]
	TempRoot    string        // [os.TempDir()/ply-sessions]
	MaxSessions int           // [4]
	IdleTimeout time.Duration // no request for this long ends a session [60s]
	SegmentSec  float64       // minimum playlist segment duration [6]
	Cache       KeyframeCache // optional
	ProbeNice   int           // nice value of the keyframe scan [10]
	Logger      *slog.Logger

	// Throttling, in segments between ffmpeg's output and the player: pause
	// above ThrottleHigh [20], resume below ThrottleLow [10].
	ThrottleHigh, ThrottleLow int
	// Lookahead is how far past ffmpeg's output (seconds) a request may be
	// and still wait for it instead of restarting ffmpeg [18].
	Lookahead float64
	// WaitTimeout bounds the wait for one segment [30s].
	WaitTimeout time.Duration
	// Tick is the housekeeping interval (idle, throttle, pruning) [250ms].
	Tick time.Duration
	// ExtractTimeout bounds one keyframe scan [15m].
	ExtractTimeout time.Duration
}

func (o *Options) defaults() {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.FFprobe == "" {
		o.FFprobe = "ffprobe"
	}
	if o.TempRoot == "" {
		o.TempRoot = filepath.Join(os.TempDir(), "ply-sessions")
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = 4
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 60 * time.Second
	}
	if o.SegmentSec <= 0 {
		o.SegmentSec = DefaultSegmentSec
	}
	if o.ProbeNice == 0 {
		o.ProbeNice = 10
	}
	if o.ThrottleHigh <= 0 {
		o.ThrottleHigh = 20
	}
	if o.ThrottleLow <= 0 {
		o.ThrottleLow = 10
	}
	if o.Lookahead <= 0 {
		o.Lookahead = 18
	}
	if o.WaitTimeout <= 0 {
		o.WaitTimeout = 30 * time.Second
	}
	if o.Tick <= 0 {
		o.Tick = 250 * time.Millisecond
	}
	if o.ExtractTimeout <= 0 {
		o.ExtractTimeout = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// Request describes what to stream.
type Request struct {
	ItemID      int64
	Path        string // absolute path of the source file
	Container   string // normalised container (mkv, mp4, ts, ...)
	DurationSec float64
	VideoIndex  int // ffprobe stream index of the video to stream
	VideoCodec  string
	AudioIndex  int // ffprobe stream index, <0 for none
	AudioCodec  string
	AudioCopy   bool
	StartSec    float64
	// Video overrides the default stream copy: a transcode. Its segments are
	// equal, SegmentSec each, and need no keyframe index.
	Video *VideoSpec
	// Fallback replaces Video, once, if the first ffmpeg run fails before
	// the init segment has been served (a GPU path that does not work for
	// this file).
	Fallback *VideoSpec
}

func (r Request) transcode() bool { return r.Video != nil && !r.Video.copy() }

// Manager owns all streaming sessions.
type Manager struct {
	o   Options
	log *slog.Logger
	dir string // TempRoot/<pid>: everything this process created

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sessions map[string]*Session
	reserved int
	closed   bool
	wg       sync.WaitGroup // session monitors

	kfMu    sync.Mutex
	kfCalls map[string]*kfCall
}

type kfCall struct {
	done chan struct{}
	kf   []float64
	err  error
}

// NewManager prepares the temp area, removing what crashed predecessors left
// there, and returns an idle manager.
func NewManager(o Options) (*Manager, error) {
	o.defaults()
	removeStale(o.TempRoot, o.Logger)
	dir := filepath.Join(o.TempRoot, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("stream temp dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		o: o, log: o.Logger, dir: dir, ctx: ctx, cancel: cancel,
		sessions: map[string]*Session{}, kfCalls: map[string]*kfCall{},
	}, nil
}

// removeStale deletes the per-process directories of processes that are gone
// (and any leftover of an earlier process with our own pid).
func removeStale(root string, log *slog.Logger) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err == nil && pid != os.Getpid() && processAlive(pid) {
			continue // another live ply sharing the temp area
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			log.Warn("remove stale session dir", "dir", e.Name(), "err", err)
		}
	}
}

// Dir is the directory holding this manager's session directories.
func (m *Manager) Dir() string { return m.dir }

// Count returns the number of live sessions.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Get returns a live session.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Delete stops and removes a session; false if there was none.
func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok {
		s.stop()
	}
	return ok
}

// Close stops every session, waits for their processes to exit and removes
// the temp directory. Create fails afterwards.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	all := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() { defer wg.Done(); s.stop() }()
	}
	wg.Wait()
	m.cancel() // aborts keyframe scans
	m.wg.Wait()
	_ = os.RemoveAll(m.dir)
}

// Create starts a session for r. The keyframe index is read from the cache or
// scanned (slow on the first play of a big file); ffmpeg is started at the
// segment containing r.StartSec so the first segment is ready when asked for.
func (m *Manager) Create(ctx context.Context, r Request) (*Session, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if len(m.sessions)+m.reserved >= m.o.MaxSessions {
		m.mu.Unlock()
		return nil, ErrTooManySessions
	}
	m.reserved++
	m.mu.Unlock()
	release := func() { m.mu.Lock(); m.reserved--; m.mu.Unlock() }

	var kf []float64
	var err error
	if r.transcode() {
		kf, err = uniformKeyframes(r.DurationSec, m.o.SegmentSec)
	} else {
		kf, err = m.keyframes(ctx, r)
	}
	if err != nil {
		release()
		return nil, err
	}
	plan, err := NewPlan(kf, r.DurationSec, m.o.SegmentSec)
	if err != nil {
		release()
		return nil, fmt.Errorf("%s: %w", filepath.Base(r.Path), err)
	}

	id, err := newID()
	if err != nil {
		release()
		return nil, err
	}
	dir := filepath.Join(m.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		release()
		return nil, err
	}
	s := newSession(m, id, dir, r, plan)

	startSeg := plan.SegmentAt(r.StartSec)
	s.lastSeg = startSeg

	m.mu.Lock()
	m.reserved--
	if m.closed {
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
		return nil, ErrClosed
	}
	m.sessions[id] = s
	m.wg.Add(1)
	m.mu.Unlock()
	go func() { defer m.wg.Done(); s.monitor() }()

	s.mu.Lock()
	err = s.startRunLocked(plan.Segments[startSeg].First)
	s.mu.Unlock()
	if err != nil {
		m.Delete(id)
		return nil, err
	}
	return s, nil
}

// uniformKeyframes are the keyframes a transcode is forced to produce: one
// every sec seconds from 0.
func uniformKeyframes(duration, sec float64) ([]float64, error) {
	if duration <= 0 {
		return nil, errors.New("cannot transcode a file of unknown duration")
	}
	kf := make([]float64, 0, int(duration/sec)+1)
	for i := 0; float64(i)*sec < duration; i++ {
		kf = append(kf, float64(i)*sec)
	}
	return kf, nil
}

// keyframes returns the keyframe index of the requested file, scanning it at
// most once at a time per file.
func (m *Manager) keyframes(ctx context.Context, r Request) ([]float64, error) {
	if m.o.Cache != nil && r.ItemID > 0 {
		if kf, ok, err := m.o.Cache.Keyframes(ctx, r.ItemID); err != nil {
			m.log.Warn("keyframe cache read", "item", r.ItemID, "err", err)
		} else if ok && len(kf) > 0 {
			return kf, nil
		}
	}

	m.kfMu.Lock()
	c, running := m.kfCalls[r.Path]
	if !running {
		c = &kfCall{done: make(chan struct{})}
		m.kfCalls[r.Path] = c
	}
	m.kfMu.Unlock()

	if !running {
		go func() {
			// Detached from the request: a client giving up must not waste
			// the scan other waiters (and the cache) will use.
			sctx, cancel := context.WithTimeout(m.ctx, m.o.ExtractTimeout)
			defer cancel()
			start := time.Now()
			c.kf, c.err = ExtractKeyframes(sctx, m.o.FFprobe, r.Path, m.o.ProbeNice)
			if c.err == nil {
				m.log.Info("keyframe index built", "path", filepath.Base(r.Path), "keyframes", len(c.kf),
					"took", time.Since(start).Round(time.Millisecond))
				if m.o.Cache != nil && r.ItemID > 0 {
					if err := m.o.Cache.SetKeyframes(sctx, r.ItemID, c.kf); err != nil {
						m.log.Warn("keyframe cache write", "item", r.ItemID, "err", err)
					}
				}
			}
			m.kfMu.Lock()
			delete(m.kfCalls, r.Path)
			m.kfMu.Unlock()
			close(c.done)
		}()
	}
	select {
	case <-c.done:
		return c.kf, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
