package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// ErrIndex is returned (wrapped) when the keyframe index of a file turned out
	// not to be what the file has, and either could not be made again or was found
	// out too late to: the session cannot go on. A new one scans the file.
	ErrIndex = errors.New("stream: keyframe index disagrees with the file")
	// ErrFFmpeg is what an error from ffmpeg not producing what was asked of it
	// is (errors.Is).
	ErrFFmpeg = errors.New("stream: ffmpeg failed")
)

// ffmpegError says what ffmpeg did not produce, and what it said.
type ffmpegError struct{ what, msg string }

func (e ffmpegError) Error() string        { return "ffmpeg did not produce " + e.what + ": " + e.msg }
func (e ffmpegError) Is(target error) bool { return target == ErrFFmpeg }

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
	// ExtractTimeout bounds one keyframe scan [15m]; a file too big to be read in
	// that time at scanRate gets what it needs (see scanTimeout).
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
	stopping int // sessions taken out of sessions whose ffmpeg and files are not gone yet
	closed   bool
	wg       sync.WaitGroup // session monitors

	kfMu    sync.Mutex
	kfCalls map[string]*kfCall
	// Files whose container index disagreed with the stream (see planGuard), as
	// they were then, and when: the scan is used for them for a day.
	kfDistrust map[distrustKey]time.Time
	kfLocks    map[string]*pathLock
	clock      func() time.Time // time.Now; a test's
}

type kfCall struct {
	done   chan struct{}
	kf     []float64
	source string // "cache", or where keyframesVia got it
	err    error
}

// NewManager prepares the temp area, removing what crashed predecessors left
// there (including any ffmpeg they left running), and returns an idle
// manager. It fails if the temp root is not a directory of ours.
func NewManager(o Options) (*Manager, error) {
	o.defaults()
	if err := prepareRoot(o.TempRoot); err != nil {
		return nil, fmt.Errorf("stream temp dir: %w", err)
	}
	removeStale(o.TempRoot, o.Logger)
	dir := filepath.Join(o.TempRoot, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("stream temp dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		o: o, log: o.Logger, dir: dir, ctx: ctx, cancel: cancel,
		sessions: map[string]*Session{}, kfCalls: map[string]*kfCall{},
		kfDistrust: map[distrustKey]time.Time{}, kfLocks: map[string]*pathLock{}, clock: time.Now,
	}, nil
}

// prepareRoot makes sure the temp root is a real directory that belongs to
// us. The default (os.TempDir()/ply-sessions) is a predictable name in a
// possibly shared directory, and everything under it gets deleted, so a
// symlink or somebody else's directory there is refused, not followed.
func prepareRoot(root string) error {
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(root, 0o700)
	}
	switch {
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link; refusing to use it (remove it or choose another temp directory)", root)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", root)
	case !ownedByUs(fi):
		return fmt.Errorf("%s belongs to another user; refusing to use it", root)
	}
	if fi.Mode().Perm()&0o077 != 0 { // ours, so we may tighten it
		return os.Chmod(root, 0o700)
	}
	return nil
}

// removeStale deletes the per-process directories (named by pid) of processes
// that are gone, and any leftover of an earlier process with our own pid,
// after killing the ffmpeg they may have left running. Anything in the root
// that is not named like a pid is not ours and stays.
func removeStale(root string, log *slog.Logger) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || strconv.Itoa(pid) != e.Name() {
			continue
		}
		if pid != os.Getpid() && processAlive(pid) {
			continue // another live ply sharing the temp area
		}
		dir := filepath.Join(root, e.Name())
		killLeftoverFFmpeg(dir, log)
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("remove stale session dir", "dir", e.Name(), "err", err)
		}
	}
}

// pidFile is written into every session directory: the process group of the
// session's ffmpeg and the program it runs, for killLeftoverFFmpeg.
const pidFile = "ffmpeg.pid"

// killLeftoverFFmpeg kills the ffmpeg process groups recorded in the session
// directories under dir, the work of a ply that crashed (a throttled ffmpeg is
// stopped, and would hold its source file open forever).
func killLeftoverFFmpeg(dir string, log *slog.Logger) {
	sessions, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range sessions {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), pidFile))
		if err != nil {
			continue
		}
		pgidText, program, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
		if pgid, err := strconv.Atoi(pgidText); err == nil && program != "" && killStaleGroup(pgid, program) {
			log.Warn("killed an ffmpeg left behind by a crashed ply", "pgid", pgid, "session", e.Name())
		}
	}
}

// Dir is the directory holding this manager's session directories.
func (m *Manager) Dir() string { return m.dir }

// Count returns the number of sessions that still hold resources: live ones,
// and ones being stopped. When it drops, their ffmpeg has exited and their
// directory is gone.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions) + m.stopping
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
	if ok {
		delete(m.sessions, id)
		m.stopping++
	}
	m.mu.Unlock()
	if ok {
		s.stop()
		m.stopped()
	}
	return ok
}

// stopped records that a session taken out of the map is completely gone.
func (m *Manager) stopped() {
	m.mu.Lock()
	m.stopping--
	m.mu.Unlock()
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
	m.stopping += len(all)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() { defer wg.Done(); s.stop(); m.stopped() }()
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
	if len(m.sessions)+m.reserved+m.stopping >= m.o.MaxSessions {
		m.mu.Unlock()
		return nil, ErrTooManySessions
	}
	m.reserved++
	m.mu.Unlock()
	release := func() { m.mu.Lock(); m.reserved--; m.mu.Unlock() }

	var kf []float64
	var source string
	var err error
	if r.transcode() {
		kf, err = uniformKeyframes(r.DurationSec, m.o.SegmentSec)
	} else {
		kf, source, err = m.keyframes(ctx, r, false)
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
	if source == sourceCues || source == sourceSampleTable {
		// Read from the container, not measured: checked against the first
		// fragments ffmpeg writes before it is trusted or cached.
		s.guard = newPlanGuard(kf, r.ItemID, time.Now().Add(m.o.WaitTimeout))
	}

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

// keyframes returns the keyframe index of the requested file and where it came
// from (sourceCues, sourceSampleTable, sourceScan, or "cache"), reading it at
// most once at a time per file: from the index the container already has, or
// by scanning the whole file when it has none, or when the index of this file
// has been found wrong.
//
// Scans are cached. An index read from the container is not, yet: Create hands
// it to a planGuard, which caches it once the first fragments have agreed with
// it. Concurrent requests for a file share one read per kind (an index read, or
// a scan), whatever the stream they want: the key is the path (a file with two
// video streams gets the index of whichever asked first; the cache is per item
// in any case).
//
// force is for a file whose index was found wrong, by a session that needs the
// scan now: the cache is not read (what is in it is that index, or being
// dropped) and the result is a scan's, never a read of the index another call
// is making. The scan is the manager's, not the caller's: it is detached from
// ctx (which only says how long to wait) and cached when it succeeds, for
// whoever asks next.
func (m *Manager) keyframes(ctx context.Context, r Request, force bool) ([]float64, string, error) {
	scan := force || m.indexDistrusted(r.Path)
	if !force && m.o.Cache != nil && r.ItemID > 0 {
		if kf, ok, err := m.o.Cache.Keyframes(ctx, r.ItemID); err != nil {
			m.log.Warn("keyframe cache read", "item", r.ItemID, "err", err)
		} else if ok && len(kf) > 0 {
			return kf, "cache", nil
		}
	}

	key := r.Path
	if scan {
		key += "\x00scan"
	}
	m.kfMu.Lock()
	c, running := m.kfCalls[key]
	if !running {
		c = &kfCall{done: make(chan struct{})}
		m.kfCalls[key] = c
	}
	m.kfMu.Unlock()

	if !running {
		go func() {
			// Detached from the request: a client giving up must not waste
			// the scan other waiters (and the cache) will use.
			var size int64
			if fi, err := os.Stat(r.Path); err == nil {
				size = fi.Size()
			}
			sctx, cancel := context.WithTimeout(m.ctx, scanTimeout(m.o.ExtractTimeout, size))
			defer cancel()
			start := time.Now()
			var res keyframeResult
			var err error
			if scan {
				res, err = keyframesScan(sctx, m.o.FFprobe, r.Path, m.o.ProbeNice)
				res.skip = errIndexDistrusted
			} else {
				res, err = keyframesVia(sctx, m.o.FFprobe, r.Path, m.o.ProbeNice, r.VideoIndex)
			}
			c.kf, c.source, c.err = res.times, res.source, err
			if c.err == nil {
				m.logKeyframes(r.Path, res, time.Since(start))
				if res.source == sourceScan {
					m.cacheKeyframes(sctx, r.ItemID, c.kf)
				}
			}
			m.kfMu.Lock()
			delete(m.kfCalls, key)
			m.kfMu.Unlock()
			close(c.done)
		}()
	}
	select {
	case <-c.done:
		return c.kf, c.source, c.err
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

var errIndexDistrusted = errors.New("the container index disagreed with the stream")

// cacheKeyframes stores the keyframes of an item, if there is a cache and an item.
func (m *Manager) cacheKeyframes(ctx context.Context, itemID int64, kf []float64) {
	if m.o.Cache == nil || itemID <= 0 {
		return
	}
	if err := m.o.Cache.SetKeyframes(ctx, itemID, kf); err != nil {
		m.log.Warn("keyframe cache write", "item", itemID, "err", err)
	}
}

// A scan reads the whole file, and a share gives it a megabyte a second at the
// worst: scanRate is what is assumed of it. The time allowed is whatever
// ExtractTimeout is, or what the size of the file needs at that rate if that is
// more, and a third of ExtractTimeout (5 minutes of its 15) on top for the rest;
// never more than maxScanTimeout.
const (
	scanRate       = 1 << 20 // bytes a second
	maxScanTimeout = 6 * time.Hour
)

// scanTimeout is how long the scan of a file of size bytes may take, where base
// is Options.ExtractTimeout: with 15 minutes, 20 for a file of up to 900 MB,
// 42 minutes for 2.2 GB, and 6 hours from some 21 GB on.
func scanTimeout(base time.Duration, size int64) time.Duration {
	need := time.Duration(size/scanRate) * time.Second
	return max(base, min(max(base, need)+base/3, maxScanTimeout))
}

// distrustTTL is how long the index of a file stays distrusted.
const distrustTTL = 24 * time.Hour

// distrustKey is a file as it was when its index was found wrong: another size or
// modification time is another file.
type distrustKey struct {
	path  string
	size  int64
	mtime int64 // ns
}

func distrustKeyOf(path string) distrustKey {
	k := distrustKey{path: path}
	if fi, err := os.Stat(path); err == nil {
		k.size, k.mtime = fi.Size(), fi.ModTime().UnixNano()
	}
	return k
}

// distrustIndex makes the next reads of the keyframes of path scan the file, as
// it is now, for distrustTTL.
func (m *Manager) distrustIndex(path string) {
	k := distrustKeyOf(path)
	m.kfMu.Lock()
	defer m.kfMu.Unlock()
	for old := range m.kfDistrust { // the file as it was is gone
		if old.path == path {
			delete(m.kfDistrust, old)
		}
	}
	m.kfDistrust[k] = m.clock()
}

// indexDistrusted reports whether the index of the file at path, as it is now,
// was found wrong less than distrustTTL ago.
func (m *Manager) indexDistrusted(path string) bool {
	k := distrustKeyOf(path)
	m.kfMu.Lock()
	defer m.kfMu.Unlock()
	at, ok := m.kfDistrust[k]
	if ok && m.clock().Sub(at) >= distrustTTL {
		delete(m.kfDistrust, k)
		return false
	}
	return ok
}

// lockIndexCache serialises the writes to the cache of the keyframes of the file
// at path that depend on whether its index is trusted: the write of an index a
// session has confirmed (which looks first, inside the lock, whether another
// found it wrong meanwhile) and the drop of one found wrong. Whichever session
// does either, a dropped index is not written back after the drop. It returns
// the unlock.
func (m *Manager) lockIndexCache(path string) (unlock func()) {
	m.kfMu.Lock()
	l := m.kfLocks[path]
	if l == nil {
		l = &pathLock{}
		m.kfLocks[path] = l
	}
	l.users++
	m.kfMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.kfMu.Lock()
		if l.users--; l.users == 0 {
			delete(m.kfLocks, path)
		}
		m.kfMu.Unlock()
	}
}

type pathLock struct {
	mu    sync.Mutex
	users int // under Manager.kfMu
}

// logKeyframes says how a keyframe index was obtained and how long it took.
func (m *Manager) logKeyframes(path string, res keyframeResult, took time.Duration) {
	attrs := []any{"path", filepath.Base(path), "keyframes", len(res.times), "took", took.Round(time.Millisecond)}
	switch res.source {
	case sourceCues:
		m.log.Info("keyframe index read from cues", attrs...)
	case sourceSampleTable:
		m.log.Info("keyframe index read from sample table", attrs...)
	default:
		if res.skip != nil { // why the container's index was not used
			attrs = append(attrs, "index_unusable", res.skip)
		}
		m.log.Info("keyframe index built by packet scan", attrs...)
	}
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
