package stream

import (
	"slices"
	"strings"
)

// PlaybackLoad is what playback is doing right now, for the tier governor
// (internal/tier has the same type), which holds background work while viewers
// need the machine.
type PlaybackLoad struct {
	Sessions   int      // plays in progress (HLS sessions and direct plays), paused ones included
	Active     int      // of them, not paused
	Transcodes int      // video transcodes running, any encoder
	Software   int      // of them, on the CPU (libx264)
	Waiting    int      // requests for a segment, init segment or playlist blocked on ffmpeg right now: a viewer is stalled
	Paths      []string // the media files being read, sorted, each once
}

// PlaybackLoad reports what is being played, from what the manager already
// keeps: it takes the manager's lock for a short while and never waits for ffmpeg.
// Sessions being stopped are not counted, as in Playing. A play being set up is:
// from the moment Create reserves its place, whatever the keyframe scan of a first
// play (minutes, over a slow share) is reading is a play's reading, and the files it
// will play are in Paths. A player that has not said it is paused counts as active.
func (m *Manager) PlaybackLoad() PlaybackLoad {
	m.mu.Lock()
	m.pruneDirectLocked()
	var l PlaybackLoad
	add := func(path string, paused bool) {
		l.Sessions++
		if !paused {
			l.Active++
		}
		if path != "" && !slices.Contains(l.Paths, path) {
			l.Paths = append(l.Paths, path)
		}
	}
	for _, s := range m.sessions {
		add(s.req.Path, s.paused())
		if s.kind == KindTranscode {
			l.Transcodes++
			if s.soft.Load() {
				l.Software++
			}
		}
	}
	for _, d := range m.direct {
		add(d.path, d.paused)
	}
	for path, n := range m.reservedPaths {
		for range n {
			add(path, false)
		}
	}
	l.Waiting = int(m.waiting.Load())
	m.unlock()
	slices.Sort(l.Paths)
	return l
}

// OnLoadChange sets the function called when PlaybackLoad may have changed
// enough for a governor to look again: a play starts or ends (a session is
// created or removed, a direct play begins or runs out), a session begins to be set
// up or that ends without one (Create reserves its place, and gives it up), or the first request
// starts to wait for ffmpeg (Waiting goes from 0 to 1; more waiting is no news).
// f runs on the goroutine that made the change, with no lock of the manager
// held, so it may call back into the manager, but it must be quick and must not
// block: a request, or the housekeeping, is held up for as long as it runs. nil
// removes the hook.
func (m *Manager) OnLoadChange(f func()) {
	if f == nil {
		m.onLoad.Store(nil)
		return
	}
	m.onLoad.Store(&f)
}

func (m *Manager) fireLoadChange() {
	if f := m.onLoad.Load(); f != nil {
		(*f)()
	}
}

// unlock releases m.mu and then, if what plays changed while it was held
// (loadDirty), calls the hook of OnLoadChange. Before it lets go, it tells
// OnPlayChange what started and what ended.
func (m *Manager) unlock() {
	changed := m.loadDirty
	m.loadDirty = false
	if changed {
		m.reconcileLocked()
	}
	m.mu.Unlock()
	if changed {
		m.fireLoadChange()
	}
}

// paused is whether the player said it is paused (see Heartbeat).
func (s *Session) paused() bool {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	return s.hbPaused
}

// A waiter is one request, counted in PlaybackLoad.Waiting from the moment it has
// to wait for ffmpeg (block) until it is answered (done): a request that finds
// what it needs at once is never counted. It belongs to the goroutine serving the
// request.
type waiter struct {
	m  *Manager
	on bool
}

func (s *Session) waiter() waiter { return waiter{m: s.m} }

func (w *waiter) block() {
	if w.on {
		return
	}
	w.on = true
	if w.m.waiting.Add(1) == 1 {
		w.m.fireLoadChange()
	}
}

func (w *waiter) done() {
	if w.on {
		w.on = false
		w.m.waiting.Add(-1)
	}
}

// gpuEncoderSuffixes end the names of the encoders that run on the GPU, which
// are named after their API: h264_videotoolbox, h264_vaapi, h264_qsv, h264_nvenc.
var gpuEncoderSuffixes = []string{"_videotoolbox", "_vaapi", "_qsv", "_nvenc"}

// softwareEncoder reports whether the transcode v encodes on the CPU: the encoder
// it asks for (the -c:v of Encode) is not a GPU one. One that is not named counts
// as software, the side a governor should err on.
func softwareEncoder(v *VideoSpec) bool {
	if v == nil || v.copy() {
		return false
	}
	var name string
	for i := 0; i+1 < len(v.Encode); i++ {
		if v.Encode[i] == "-c:v" || v.Encode[i] == "-vcodec" {
			name = v.Encode[i+1]
			break
		}
	}
	return !slices.ContainsFunc(gpuEncoderSuffixes, func(suffix string) bool { return strings.HasSuffix(name, suffix) })
}
