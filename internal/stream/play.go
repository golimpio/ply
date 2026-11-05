package stream

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// Kinds of play, as listed by Playing.
const (
	KindTranscode = "transcode"
	KindRemux     = "remux"
	KindDirect    = "direct"
)

// DirectTTL is how long a direct play stays counted after its last request
// has ended (while a request is being served it is counted, however long that
// takes: a player may read the whole file over one connection).
const DirectTTL = 30 * time.Second

// DirectGrace is how long after it stopped being counted a client that comes back
// to the same item is admitted even at the limit: a paused or buffered player
// that asks again is carrying on, not starting another play.
const DirectGrace = 10 * time.Minute

// StopGrace is how long a session may take to stop before it counts against the
// limits again: its ffmpeg is not going, perhaps for a mount that has gone, and
// those must not pile up unnoticed.
const StopGrace = 10 * time.Second

// DefaultBeatTimeout is how long a session whose player sends heartbeats (every
// 10 s) is kept after its last heartbeat or request. See Options.BeatTimeout.
const DefaultBeatTimeout = 30 * time.Second

// Default limits (Options.MaxSessions, Options.MaxTranscodes).
const (
	DefaultMaxStreams    = 4
	DefaultMaxTranscodes = 1
)

// PlayInfo is one thing being played: a transcode, a remux or a direct play.
type PlayInfo struct {
	// ID is the session's id (DELETE /api/sessions/{id} stops it), or for a direct
	// play the id made up for it, with which it can only be dropped from the count.
	ID          string
	ItemID      int64
	Title       string
	Kind        string // KindTranscode, KindRemux or KindDirect
	Client      string
	StartedAt   time.Time
	LastSeenAt  time.Time
	PositionSec float64
	Paused      bool
}

// LimitError is what Create and Direct return when a limit is reached. It
// matches ErrTooManySessions.
type LimitError struct {
	// Limit is "streams" (all plays, direct ones included) or "transcodes".
	Limit string
	Max   int
	Used  int
	// Playing is what was being played at that moment: everything for "streams",
	// the transcodes for "transcodes" (stopping a remux would not make room).
	Playing []PlayInfo
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("stream: too many active sessions (%d of %d %s)", e.Used, e.Max, e.Limit)
}

func (e *LimitError) Is(target error) bool { return target == ErrTooManySessions }

// DirectKey identifies a direct play: one client playing one item.
type DirectKey struct {
	ItemID    int64
	IP        string
	UserAgent string
}

// A direct play is a client reading the original file with range requests. ply
// does not run anything for it and cannot stop it; it only counts it, so that
// the limit on plays is one limit.
type directPlay struct {
	id       string
	key      DirectKey
	title    string
	client   string
	path     string // the file being read, for PlaybackLoad; empty if not told
	started  time.Time
	seen     time.Time // the last request started, ended or was beaten
	inflight int       // requests being served
	pos      float64
	paused   bool
}

// stopInfo is a session on its way out: since when, and what it was.
type stopInfo struct {
	since time.Time
	kind  string
}

func (d *directPlay) info() PlayInfo {
	return PlayInfo{ID: d.id, ItemID: d.key.ItemID, Title: d.title, Kind: KindDirect, Client: d.client,
		StartedAt: d.started, LastSeenAt: d.seen, PositionSec: d.pos, Paused: d.paused}
}

// SetLimits changes the limits, at once and for what comes next: plays that are
// already running are never stopped for it. streams caps all plays together
// (sessions and direct plays), transcodes caps the transcoding sessions among
// them. A value below 1 leaves that limit as it is.
func (m *Manager) SetLimits(streams, transcodes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if streams >= 1 {
		m.maxStreams = streams
	}
	if transcodes >= 1 {
		m.maxTranscodes = transcodes
	}
}

// Limits returns the limits in force (see SetLimits).
func (m *Manager) Limits() (streams, transcodes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxStreams, m.maxTranscodes
}

// pruneDirectLocked forgets the direct plays that no request is being served for
// and whose last one ended DirectTTL ago (remembering them for DirectGrace), and
// returns the time it used.
func (m *Manager) pruneDirectLocked() time.Time {
	now := m.clock()
	for k, d := range m.direct {
		if d.inflight == 0 && now.Sub(d.seen) >= DirectTTL {
			delete(m.direct, k)
			m.recentDirect[k] = d.seen
			m.loadDirty = true
		}
	}
	for k, seen := range m.recentDirect {
		if now.Sub(seen) >= DirectGrace {
			delete(m.recentDirect, k)
		}
	}
	return now
}

// stuckStoppingLocked counts the sessions that have taken more than StopGrace to
// stop, and the transcodes among them.
func (m *Manager) stuckStoppingLocked(now time.Time) (all, transcodes int) {
	for _, s := range m.stopping {
		if now.Sub(s.since) > StopGrace {
			all++
			if s.kind == KindTranscode {
				transcodes++
			}
		}
	}
	return all, transcodes
}

// admitLocked decides whether one more play may start. What is being stopped
// does not count: it is on its way out, and a player that stops one play and
// starts the next must not be turned away by the first. (One that has been on
// its way out for more than StopGrace does: an ffmpeg that will not go must not
// be forgotten.)
func (m *Manager) admitLocked(transcode bool) *LimitError {
	now := m.pruneDirectLocked()
	stuck, stuckTranscodes := m.stuckStoppingLocked(now)
	if used := len(m.sessions) + m.reserved + len(m.direct) + stuck; used >= m.maxStreams {
		return &LimitError{Limit: "streams", Max: m.maxStreams, Used: used, Playing: m.playingLocked(nil)}
	}
	if transcode {
		used := m.reservedTranscodes + stuckTranscodes
		for _, s := range m.sessions {
			if s.kind == KindTranscode {
				used++
			}
		}
		if used >= m.maxTranscodes {
			only := func(p PlayInfo) bool { return p.Kind == KindTranscode }
			return &LimitError{Limit: "transcodes", Max: m.maxTranscodes, Used: used, Playing: m.playingLocked(only)}
		}
	}
	return nil
}

// CheckDirect says whether a direct play of k would be admitted now (a
// *LimitError if not), without counting it: for the answer to a request that
// only announces a play, whose files are asked for by a client that may not
// look the same.
func (m *Manager) CheckDirect(k DirectKey) error {
	m.mu.Lock()
	defer m.unlock()
	m.pruneDirectLocked()
	if _, ok := m.direct[k]; ok {
		return nil
	}
	if _, ok := m.recentDirect[k]; ok {
		return nil
	}
	if err := m.admitLocked(false); err != nil {
		return err
	}
	return nil
}

// Direct counts a request for the file of a direct play: it registers the play,
// or refreshes it if the same client is already playing the same item, and
// returns its id and the function to call when the request is over (done; it is
// safe to call twice). The play stays counted for as long as a request is being
// served, however long that is, and DirectTTL after the last one ended.
//
// A new play is refused with a *LimitError when the limit on plays is reached.
// One that is already counted is never refused, and neither is one that stopped
// being counted less than DirectGrace ago: a client that comes back is carrying
// on. title and client are for the listing; pos is where the play is thought to
// be, kept until a heartbeat says otherwise.
func (m *Manager) Direct(k DirectKey, title, client string, pos float64) (id string, done func(), err error) {
	return m.DirectFile(k, title, client, "", pos)
}

// DirectFile is Direct for a play that says which file it reads (path), so that
// PlaybackLoad can report it. A play already counted without a path takes it.
func (m *Manager) DirectFile(k DirectKey, title, client, path string, pos float64) (id string, done func(), err error) {
	m.mu.Lock()
	defer m.unlock()
	if m.closed {
		return "", nil, ErrClosed
	}
	now := m.pruneDirectLocked()
	d, counted := m.direct[k]
	if !counted {
		_, returning := m.recentDirect[k]
		if !returning {
			if err := m.admitLocked(false); err != nil {
				return "", nil, err
			}
		}
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		d = &directPlay{id: "d-" + hex.EncodeToString(b[:]), key: k, title: title, client: client, started: now, pos: pos}
		m.direct[k] = d
		delete(m.recentDirect, k)
		m.loadDirty = true
	}
	if d.path == "" {
		d.path = path
	}
	d.seen = now
	d.inflight++
	var once sync.Once
	done = func() {
		once.Do(func() {
			m.mu.Lock()
			d.inflight--
			d.seen = m.clock()
			m.mu.Unlock()
		})
	}
	return d.id, done, nil
}

// Beat is what a player tells with a heartbeat; nil is not told.
type Beat struct {
	Position *float64 // seconds
	Paused   *bool
}

// Heartbeat records that the player of session id is alive, and where it is and
// whether it is paused, if it says. It counts as a request for the idle timeout:
// a paused player makes no requests for as long as it has buffer, and its beats
// are what keep the session. From the first beat on the session is held to
// Options.BeatTimeout rather than IdleTimeout: a player that beats says when it
// has gone, one that does not might only be buffered. It reports false if there
// is no such session (or direct play: those can be beaten too, to keep them
// counted while paused).
func (m *Manager) Heartbeat(id string, b Beat) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	var d *directPlay
	if !ok {
		for _, x := range m.direct {
			if x.id == id {
				d = x
			}
		}
		if d != nil {
			d.seen = m.clock()
			if b.Position != nil {
				d.pos = finitePos(*b.Position)
			}
			if b.Paused != nil {
				d.paused = *b.Paused
			}
		}
	}
	m.mu.Unlock()
	if ok {
		s.heartbeat(b)
	}
	return ok || d != nil
}

// Playing lists everything being played, oldest first: sessions (a session that
// is being stopped is not listed) and direct plays.
func (m *Manager) Playing() []PlayInfo {
	m.mu.Lock()
	defer m.unlock()
	m.pruneDirectLocked()
	return m.playingLocked(nil)
}

// playingLocked is Playing for the plays keep accepts (all if nil).
func (m *Manager) playingLocked(keep func(PlayInfo) bool) []PlayInfo {
	out := make([]PlayInfo, 0, len(m.sessions)+len(m.direct))
	add := func(p PlayInfo) {
		if keep == nil || keep(p) {
			out = append(out, p)
		}
	}
	for _, s := range m.sessions {
		add(s.info())
	}
	for _, d := range m.direct {
		add(d.info())
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// finitePos keeps a position a client sent from being NaN or negative.
func finitePos(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
