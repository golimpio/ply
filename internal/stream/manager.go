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
	"sync/atomic"
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
	FFmpeg  string // ["ffmpeg"]
	FFprobe string // ["ffprobe"]
	// Data is ply's data directory. With it, the session directories are made in
	// <Data>/tmp/sessions (not in the system's temp directory, which differs between
	// a terminal and launchd, and is cleaned by the system), and the registry of
	// running ffmpeg process groups is kept in <Data>/sessions. Anything that an
	// earlier version left in os.TempDir()/ply-sessions is cleaned up too.
	Data string
	// TempRoot is where the session directories are made [Data/tmp/sessions, or
	// os.TempDir()/ply-sessions].
	TempRoot string
	// RegistryDir is where the registry files are kept [Data/sessions; without
	// Data, there is no registry].
	RegistryDir string
	// Reaper is the program that runs ReapMain when started with "--reap" (ply
	// itself): where there is no parent-death signal (macOS), one is started for
	// every ffmpeg group, and kills the group if ply dies. Empty starts none.
	Reaper string
	// MaxSessions is the most plays at once, direct plays included [4]; and
	// MaxTranscodes the most of them that transcode [1]. SetLimits changes them
	// while running.
	MaxSessions   int
	MaxTranscodes int
	// IdleTimeout ends a session that has had no request for a segment (or the
	// playlist) for this long [60s]. That is for a player that sends no heartbeats
	// (it may have 40 s of buffer and need nothing for as long). One that has sent a
	// heartbeat says when it is gone, and BeatTimeout is its time.
	IdleTimeout time.Duration
	// BeatTimeout ends a session whose player has sent at least one heartbeat, that
	// long after its last heartbeat or request [30s; never more than IdleTimeout].
	BeatTimeout time.Duration
	// PausedBeatTimeout is BeatTimeout for a session whose last heartbeat said the
	// player is paused, until the next request or unpaused heartbeat [3 min; never
	// more than MaxPausedBeatTimeout]: a browser slows the timers of a hidden tab to
	// about one a minute, so a paused player in one beats too rarely for 30 s.
	PausedBeatTimeout time.Duration
	SegmentSec        float64       // minimum playlist segment duration [6]
	Cache             KeyframeCache // optional
	ProbeNice         int           // nice value of the keyframe scan [10]
	Logger            *slog.Logger
	// Clock is the time source [time.Now]; a test's.
	Clock func() time.Time
	// LeakCheck is how often the registry is compared with the sessions [5s].
	LeakCheck time.Duration

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
	if o.Data != "" {
		if o.TempRoot == "" {
			o.TempRoot = filepath.Join(o.Data, "tmp", "sessions")
		}
		if o.RegistryDir == "" {
			o.RegistryDir = filepath.Join(o.Data, "sessions")
		}
	}
	if o.TempRoot == "" {
		o.TempRoot = filepath.Join(os.TempDir(), "ply-sessions")
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = DefaultMaxStreams
	}
	if o.MaxTranscodes <= 0 {
		o.MaxTranscodes = DefaultMaxTranscodes
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 60 * time.Second
	}
	if o.BeatTimeout <= 0 {
		o.BeatTimeout = DefaultBeatTimeout
	}
	if o.PausedBeatTimeout <= 0 {
		o.PausedBeatTimeout = DefaultPausedBeatTimeout
	}
	o.PausedBeatTimeout = min(o.PausedBeatTimeout, MaxPausedBeatTimeout)
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.LeakCheck <= 0 {
		o.LeakCheck = 5 * time.Second
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
	// Title and Client are for the listing of what is playing (Playing): what to
	// call the item and who asked for it. They have no other use here.
	Title  string
	Client string
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
	// Sessions taken out of sessions whose ffmpeg and files are not gone yet. They
	// count in Count, and against the limits only once they have been stopping for
	// more than StopGrace.
	stopping map[string]stopInfo
	// Sessions (by id) and direct plays (by id and by client) that were stopped on
	// purpose, and when, for StoppedTTL: to tell a player that it was told to stop,
	// not that the session was lost (Stopped).
	stoppedIDs         map[string]time.Time
	stoppedDirect      map[DirectKey]time.Time
	reservedTranscodes int
	maxStreams         int
	maxTranscodes      int
	direct             map[DirectKey]*directPlay
	recentDirect       map[DirectKey]time.Time // direct plays that stopped being counted, by when they were last seen
	closed             bool
	wg                 sync.WaitGroup // session monitors and the housekeeping

	// What the tier governor reads and is told of (see PlaybackLoad).
	loadDirty bool                   // under mu: the plays changed since the last unlock
	waiting   atomic.Int32           // requests blocked on ffmpeg right now (see waiter)
	onLoad    atomic.Pointer[func()] // OnLoadChange

	reg       *registry     // nil without Options.RegistryDir
	reaperExe string        // what is run as the reaper (see reaperLink); empty for none
	stopGate  chan struct{} // a test's: Session.stop waits for it to be closed

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
// there (including any ffmpeg they left running, found through the registry in
// the data directory and, for the sessions of an earlier version, through the
// pid files in the temp directories), and returns an idle manager. It fails if
// the temp root is not a directory of ours.
func NewManager(o Options) (*Manager, error) {
	o.defaults()
	if err := prepareRoot(o.TempRoot); err != nil {
		return nil, fmt.Errorf("stream temp dir: %w", err)
	}
	reg, err := newRegistry(o.RegistryDir, o.Logger)
	if err != nil {
		return nil, fmt.Errorf("session registry: %w", err)
	}
	reg.sweep(o.TempRoot)
	// With a registry, what ran in this root is in it, and has been dealt with; the
	// pid files are for the roots that had none.
	removeStale(o.TempRoot, o.Logger, reg == nil)
	if o.Data != "" {
		// Where the sessions were before they moved into the data directory. One
		// release of this; the directory is not made, and a symlink is not followed.
		if legacy := filepath.Join(os.TempDir(), "ply-sessions"); legacy != o.TempRoot && legacyRootOK(legacy) {
			removeStale(legacy, o.Logger, true)
		}
	}
	dir := filepath.Join(o.TempRoot, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("stream temp dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		o: o, log: o.Logger, dir: dir, ctx: ctx, cancel: cancel,
		sessions: map[string]*Session{}, stopping: map[string]stopInfo{}, direct: map[DirectKey]*directPlay{},
		recentDirect: map[DirectKey]time.Time{}, stoppedIDs: map[string]time.Time{}, stoppedDirect: map[DirectKey]time.Time{},
		maxStreams: o.MaxSessions, maxTranscodes: o.MaxTranscodes, reg: reg,
		kfCalls: map[string]*kfCall{}, kfDistrust: map[distrustKey]time.Time{}, kfLocks: map[string]*pathLock{}, clock: o.Clock,
	}
	if reaperWanted && o.Reaper != "" {
		m.reaperExe = reaperLink(o)
	}
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.housekeeping() }()
	return m, nil
}

// reaperLink makes <data>/ply-reaper, a symbolic link to the program that is the
// reaper (ply itself), and returns it: the process is then named ply-reaper, and
// `killall ply` (or a script that kills every ply it finds) does not take the
// reapers down with the server they are there to outlive. Where it cannot be made,
// or there is no data directory to make it in, the program is run as it is.
func reaperLink(o Options) string {
	dir := o.Data
	if dir == "" && o.RegistryDir != "" {
		dir = filepath.Dir(o.RegistryDir)
	}
	if dir == "" {
		return o.Reaper
	}
	link, tmp := filepath.Join(dir, "ply-reaper"), filepath.Join(dir, fmt.Sprintf(".ply-reaper-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(o.Reaper, tmp); err != nil {
		o.Logger.Warn("cannot link the reaper; it will run under the name of the server", "err", err)
		return o.Reaper
	}
	if err := os.Rename(tmp, link); err != nil { // replaces a link an earlier start made, atomically
		_ = os.Remove(tmp)
		o.Logger.Warn("cannot link the reaper; it will run under the name of the server", "err", err)
		return o.Reaper
	}
	return link
}

// legacyRootOK reports whether root is a real directory of ours, one that
// removeStale can be trusted with (prepareRoot makes the same checks, and makes
// it if it is not there).
func legacyRootOK(root string) bool {
	fi, err := os.Lstat(root)
	return err == nil && fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 && ownedByUs(fi)
}

// housekeeping compares the registry with the sessions, every LeakCheck, until
// the manager is closed: what the registry holds that no session explains is an
// ffmpeg that got away, and is killed.
func (m *Manager) housekeeping() {
	t := time.NewTicker(m.o.LeakCheck)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.leakCheck()
			m.mu.Lock()
			m.pruneDirectLocked() // a direct play that ran out is an end, whoever asks
			m.unlock()
		}
	}
}

// leakCheck kills the ffmpeg process groups in the registry that belong to no
// session of this manager, and removes their files and directories.
func (m *Manager) leakCheck() {
	for _, f := range m.reg.entries() {
		if f.err != nil || foreignOwner(f.entry) {
			continue
		}
		m.mu.Lock()
		_, live := m.sessions[f.entry.SID]
		_, going := m.stopping[f.entry.SID]
		m.mu.Unlock()
		if live || going {
			continue
		}
		if m.reg.reap(f.entry, m.o.TempRoot) {
			m.log.Warn("an ffmpeg outlived its session", "session", f.entry.SID, "pgid", f.entry.PGID)
		}
		_ = os.Remove(f.path)
	}
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
		return fmt.Errorf("%s is a symbolic link; refusing to use it (remove it or choose another directory)", root)
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
// that is not named like a pid is not ours and stays. killLeftovers is whether
// to go by the pid files for the ffmpeg they may have left (see pidFile): not
// where the registry knows them.
func removeStale(root string, log *slog.Logger, killLeftovers bool) {
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
		if killLeftovers {
			killLeftoverFFmpeg(dir, log)
		}
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
	return len(m.sessions) + len(m.stopping)
}

// Get returns a live session.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Delete stops and removes a session, on purpose (someone asked: DELETE); false
// if there was none. It is remembered for StoppedTTL (Stopped), so that its player,
// when it asks again, is told that it was stopped and not that the session is
// lost. For a direct play (see Direct) it only drops it from the count: the client
// reading the file is not ours to stop, but for StoppedTTL its requests for the
// file are refused as stopped (ErrStopped), unless it announces a play again
// (CheckDirect).
func (m *Manager) Delete(id string) bool { return m.end(id, true) }

// end is Delete, and for a session that ply ends itself (idle, a Create that
// failed) with onPurpose false: that one is lost, not stopped.
func (m *Manager) end(id string, onPurpose bool) bool {
	m.mu.Lock()
	now := m.clock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		m.stopping[id] = stopInfo{since: now, kind: s.kind}
		if onPurpose {
			m.stoppedIDs[id] = now
		}
	}
	dropped := false
	if !ok {
		for k, d := range m.direct {
			if d.id == id {
				delete(m.direct, k)
				delete(m.recentDirect, k) // stopped on purpose: no grace
				if onPurpose {
					m.stoppedIDs[id] = now
					m.stoppedDirect[k] = now
				}
				dropped = true
			}
		}
	}
	m.pruneStoppedLocked(now)
	m.loadDirty = m.loadDirty || ok || dropped
	m.unlock()
	if ok {
		s.stop()
		m.stopped(id)
	}
	return ok || dropped
}

// stopped records that a session taken out of the map is completely gone.
func (m *Manager) stopped(id string) {
	m.mu.Lock()
	delete(m.stopping, id)
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
		m.stopping[id] = stopInfo{since: m.clock(), kind: s.kind}
	}
	m.loadDirty = m.loadDirty || len(all) > 0 || len(m.direct) > 0
	m.direct = map[DirectKey]*directPlay{}
	m.recentDirect = map[DirectKey]time.Time{}
	m.unlock()

	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() { defer wg.Done(); s.stop(); m.stopped(s.ID) }()
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
	transcode := r.transcode()
	m.mu.Lock()
	if m.closed {
		m.unlock()
		return nil, ErrClosed
	}
	if err := m.admitLocked(transcode); err != nil {
		m.unlock()
		return nil, err
	}
	m.reserved++
	if transcode {
		m.reservedTranscodes++
	}
	m.unlock()
	// unreserve gives back the place held for this session, with m.mu held.
	unreserve := func() {
		m.reserved--
		if transcode {
			m.reservedTranscodes--
		}
	}
	release := func() { m.mu.Lock(); unreserve(); m.mu.Unlock() }

	var kf []float64
	var source string
	var err error
	if transcode {
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
	unreserve()
	if m.closed {
		m.mu.Unlock()
		_ = os.RemoveAll(dir)
		return nil, ErrClosed
	}
	m.sessions[id] = s
	m.wg.Add(1)
	m.loadDirty = true
	m.unlock()
	go func() { defer m.wg.Done(); s.monitor() }()

	s.mu.Lock()
	err = s.startRunLocked(plan.Segments[startSeg].First)
	s.mu.Unlock()
	if err != nil {
		m.end(id, false)
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
