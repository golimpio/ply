package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ply/internal/config"
	"ply/internal/store"
	"ply/internal/stream"
)

// What is playing, and how much may (docs/M9-ARCHITECTURE.md, "Maximum parallel
// playback" and "Orphan-proof playback").

// PlayingEntry is one thing being played: GET /api/sessions lists them, and the
// 503 at the limit says which are using it. The id is the session's (DELETE
// /api/sessions/{id} stops it; POST /api/sessions/{id}/heartbeat keeps it); a
// direct play has one too, made up by ply, with which it can only be dropped
// from the count. SessionID is the same value, under the name the 503 uses.
type PlayingEntry struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"sessionId"`
	ItemID      int64      `json:"itemId"`
	Title       string     `json:"title"`
	Kind        string     `json:"kind"` // transcode | remux | direct
	Client      string     `json:"client"`
	StartedAt   time.Time  `json:"startedAt"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	PositionSec float64    `json:"positionSec"`
	Paused      bool       `json:"paused"`
	Device      *DeviceRef `json:"device,omitempty"` // the device that started it, if it said (M11)
}

func playingEntry(p stream.PlayInfo) PlayingEntry {
	return PlayingEntry{ID: p.ID, SessionID: p.ID, ItemID: p.ItemID, Title: p.Title, Kind: p.Kind, Client: p.Client,
		StartedAt: p.StartedAt.UTC(), LastSeenAt: p.LastSeenAt.UTC(), PositionSec: p.PositionSec, Paused: p.Paused,
		Device: deviceRef(p.Device)}
}

func playingEntries(list []stream.PlayInfo) []PlayingEntry {
	out := make([]PlayingEntry, len(list))
	for i, p := range list {
		out[i] = playingEntry(p)
	}
	return out
}

// LimitReached is the body of the 503 that answers a play that would exceed a
// limit. Limit is "streams" (all plays together) or "transcodes"; Playing is what
// was playing, for "transcodes" the transcodes only.
type LimitReached struct {
	Error   string         `json:"error"` // "4 of 4 plays in use"
	Limit   string         `json:"limit"`
	Max     int            `json:"max"`
	InUse   int            `json:"inUse"`
	Playing []PlayingEntry `json:"playing"`
}

// writeLimit answers 503 with Retry-After and the list of what is playing. Nothing
// that is playing is stopped to make room. The list is what GET /api/sessions
// shows, which a page of another site may not see: to a request that a browser
// marks as cross-site (the file is open to them, for Cast) the list is empty.
func writeLimit(w http.ResponseWriter, r *http.Request, le *stream.LimitError) {
	what := "plays"
	if le.Limit == "transcodes" {
		what = "transcodes"
	}
	playing := playingEntries(le.Playing)
	if crossSite(r) {
		playing = []PlayingEntry{}
	}
	w.Header().Set("Retry-After", "5")
	writeJSON(w, http.StatusServiceUnavailable, LimitReached{
		Error: fmt.Sprintf("%d of %d %s in use", le.Used, le.Max, what), Limit: le.Limit, Max: le.Max, InUse: le.Used,
		Playing: playing,
	})
}

// handleSessions is GET /api/sessions: everything being played, oldest first.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	list := []PlayingEntry{}
	if s.Streams != nil {
		list = playingEntries(s.Streams.Playing())
	}
	writeJSON(w, http.StatusOK, list)
}

type heartbeatRequest struct {
	PositionSec *float64 `json:"positionSec"`
	Paused      *bool    `json:"paused"`
	Wireless    *bool    `json:"wireless"`
}

// writeStopped answers 410 Gone, {"error": "stopped", "stoppedBy": "another
// device"}: what a player is told when its session was stopped on purpose (by
// DELETE). Unlike 404 (unknown, or ended because nothing was heard from it), it
// does not mean "start another".
func writeStopped(w http.ResponseWriter) { writeGone(w, "another device") }

// writeGone answers 410: the play was stopped on purpose, by whom.
func writeGone(w http.ResponseWriter, by string) {
	writeJSON(w, http.StatusGone, map[string]string{"error": "stopped", "stoppedBy": by})
}

// stoppedOnPurpose answers 410 and reports true if the session or direct play id
// was stopped by DELETE a short while ago (stream.StoppedTTL); the routes of a
// session use it where they would answer 404.
func (s *Server) stoppedOnPurpose(w http.ResponseWriter, id string) bool {
	if s.Streams == nil || !s.Streams.Stopped(id) {
		return false
	}
	writeStopped(w)
	return true
}

// handleSessionHeartbeat is POST /api/sessions/{sid}/heartbeat: the player says
// it is alive, where it is and whether it is paused. A session whose player has
// beaten is stopped 30 s after its last heartbeat or request (3 min while the last
// heartbeat said it is paused), so a player beats while it lives (every 10 s). The
// body may be empty. 204; 404 if the session is unknown or ply ended it because
// nothing was heard from it (the player then starts another); 410 if it was
// stopped on purpose, by DELETE, less than two minutes ago (the player stops).
func (s *Server) handleSessionHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	body := http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if p := req.PositionSec; p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0) {
		writeError(w, http.StatusBadRequest, "positionSec must be a non-negative number")
		return
	}
	if s.Streams == nil || !s.Streams.Heartbeat(r.PathValue("sid"), stream.Beat{Position: req.PositionSec, Paused: req.Paused, Wireless: req.Wireless}) {
		if s.stoppedOnPurpose(w, r.PathValue("sid")) {
			return
		}
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// directKey is who is playing which item directly: the item and the device, or for
// a client that names none, its address and its User-Agent.
func directKey(e store.Entry, r *http.Request) stream.DirectKey {
	if id := deviceID(r); id != "" {
		return stream.DirectKey{ItemID: e.ID, Device: id}
	}
	return stream.DirectKey{ItemID: e.ID, IP: remoteIP(r), UserAgent: r.UserAgent()}
}

func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// admitDirect counts a direct play of e (this request is one more of its range
// requests) and reports whether it may go on; if not it has answered 503. The
// play is counted for as long as the request lasts: done, which the handler
// calls (deferred) when it has served the file, ends it.
func (s *Server) admitDirect(w http.ResponseWriter, r *http.Request, e store.Entry) (done func(), ok bool) {
	nothing := func() {}
	if s.Streams == nil {
		return nothing, true
	}
	// The URL POST /playback gave carries the play's id: the request counts for that
	// play whoever sends it (the Apple TV it was handed to). A play that ran out since
	// is counted as any other request is.
	if sid := r.URL.Query().Get("sid"); sid != "" {
		done, found, err := s.Streams.DirectByID(sid, e.ID)
		if errors.Is(err, stream.ErrStopped) {
			writeStopped(w)
			return nothing, false
		}
		if found {
			return done, true
		}
	}
	var pos float64
	if e.Progress != nil {
		pos = e.Progress.PositionSec
	}
	_, done, err := s.Streams.DirectFile(directKey(e, r), playTitle(e), clientLabel(r), e.Path, pos)
	var le *stream.LimitError
	if errors.As(err, &le) {
		writeLimit(w, r, le)
		return nothing, false
	}
	if errors.Is(err, stream.ErrStopped) { // dropped by DELETE a moment ago: not to come back by itself
		writeStopped(w)
		return nothing, false
	}
	if err != nil {
		return nothing, true // a manager that is closing: let the file go
	}
	return done, true
}

// startDirect starts the direct play of e that r asks for (POST /playback), counted
// from now, and returns its id: the file's URL carries it, and the player sends its
// heartbeats to it. "" without a session manager (nothing is counted). It reports
// false after it has answered: 503 at the limit, 410 for an attempt older than one
// already asked for (the client gave it up).
func (s *Server) startDirect(w http.ResponseWriter, r *http.Request, e store.Entry, startSec float64, attempt int64) (string, bool) {
	if s.Streams == nil {
		return "", true
	}
	id, err := s.Streams.StartDirect(directKey(e, r), playTitle(e), clientLabel(r), e.Path, s.playDevice(r), startSec, attempt)
	var le *stream.LimitError
	if errors.As(err, &le) {
		writeLimit(w, r, le)
		return "", false
	}
	if errors.Is(err, stream.ErrStopped) {
		writeGone(w, "a newer attempt")
		return "", false
	}
	if err != nil {
		return "", true // a manager that is closing: the file is served uncounted
	}
	return id, true
}

// playTitle is what to call an item in the list of what is playing.
func playTitle(e store.Entry) string {
	if e.Kind == store.KindEpisode {
		t := fmt.Sprintf("%s S%02dE%02d", e.Show, e.Season, e.Episode)
		if e.Title != "" {
			t += " " + e.Title
		}
		return strings.TrimSpace(t)
	}
	if e.Title != "" {
		return e.Title
	}
	base := filepath.Base(e.Path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// clientLabel says who is asking: the address and a short name for the program
// ("192.168.1.20 · Safari").
func clientLabel(r *http.Request) string {
	return remoteIP(r) + " · " + userAgentName(r.UserAgent())
}

// userAgentName is a short name for a User-Agent string: the browser or player,
// or its first word.
func userAgentName(ua string) string {
	for _, k := range []struct{ sub, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"CriOS/", "Chrome"},
		{"Safari/", "Safari"}, {"AppleCoreMedia", "AVPlayer"}, {"Infuse", "Infuse"}, {"VLC", "VLC"},
		{"Lavf", "ffmpeg"}, {"curl/", "curl"},
	} {
		if strings.Contains(ua, k.sub) {
			return k.name
		}
	}
	if f := strings.Fields(ua); len(f) > 0 {
		return strings.TrimSpace(strings.SplitN(f[0], "/", 2)[0])
	}
	return "unknown client"
}

// ---- the limits on plays ----

// Names of the rows of the settings table that hold the limits, and the bounds
// they are held to.
const (
	SettingMaxStreams    = "playback.max_streams"
	SettingMaxTranscodes = "playback.max_transcodes"

	// The default number of transcodes depends on the encoder: a software one
	// uses most of the CPU, a hardware one little.
	defaultTranscodesSoftware = 1
	defaultTranscodesHardware = 3
)

// Where a limit came from, strongest first.
const (
	limitFromFlag     = "flag" // -max-streams / -max-transcodes, or their environment variables
	limitFromSettings = "settings"
	limitFromDefault  = "default"
)

// playLimits are the limits in force and where each came from.
type playLimits struct {
	streams, transcodes             int
	streamsSource, transcodesSource string
}

// playLimits resolves the limits: the command line (or the environment), then the
// settings table, then the defaults. The default number of transcodes depends on
// the encoder; while the GPU is still being probed it is the software one's.
func (s *Server) playLimits(ctx context.Context) playLimits {
	l := playLimits{streams: stream.DefaultMaxStreams, transcodes: defaultTranscodesSoftware,
		streamsSource: limitFromDefault, transcodesSource: limitFromDefault}
	if s.HW.Ready() && s.HW.Info().Encoder.Hardware() {
		l.transcodes = defaultTranscodesHardware
	}
	resolve := func(key string, flag, ceiling int, into *int, source *string) {
		switch {
		case flag > 0:
			*into, *source = flag, limitFromFlag
		default:
			if v, ok := s.storedLimit(ctx, key, ceiling); ok {
				*into, *source = v, limitFromSettings
			}
		}
	}
	resolve(SettingMaxStreams, s.MaxStreamsFlag, config.MaxStreamsCeiling, &l.streams, &l.streamsSource)
	resolve(SettingMaxTranscodes, s.MaxTranscodesFlag, config.MaxTranscodesCeiling, &l.transcodes, &l.transcodesSource)
	return l
}

// storedLimit reads a limit from the settings table; a value that is not a
// number in 1..ceiling is ignored (as if it was not there).
func (s *Server) storedLimit(ctx context.Context, key string, ceiling int) (int, bool) {
	v, ok, err := s.Store.GetSetting(ctx, key)
	if err != nil {
		s.Logger.Warn("reading a limit from the settings", "key", key, "err", err)
		return 0, false
	}
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > ceiling {
		s.Logger.Warn("ignoring a limit in the settings that is out of range", "key", key, "value", v)
		return 0, false
	}
	return n, true
}

// checkLimitChange validates the limits in a change of settings: a number in
// range, and not one the command line fixed (409). It has answered, and reports
// false, if one is not.
func (s *Server) checkLimitChange(ctx context.Context, w http.ResponseWriter, req settingsRequest) bool {
	cur := s.playLimits(ctx)
	for _, c := range []struct {
		name, flag, env, source string
		value                   *int
		ceiling                 int
	}{
		{"maxStreams", "-max-streams", config.MaxStreamsEnv, cur.streamsSource, req.MaxStreams, config.MaxStreamsCeiling},
		{"maxTranscodes", "-max-transcodes", config.MaxTranscodesEnv, cur.transcodesSource, req.MaxTranscodes, config.MaxTranscodesCeiling},
	} {
		switch {
		case c.value == nil:
		case *c.value < 1 || *c.value > c.ceiling:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a whole number from 1 to %d", c.name, c.ceiling))
			return false
		case c.source == limitFromFlag:
			// (A flag and its variable give the same limit and ply is not told which one
			// it was: the one that is in the environment is named first.)
			by := c.flag + " or " + c.env
			if os.Getenv(c.env) != "" {
				by = c.env + " (or " + c.flag + ")"
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("%s is set by %s and cannot be changed here", c.name, by))
			return false
		}
	}
	return true
}

// storeLimits saves the limits that are in a change of settings (checked by
// checkLimitChange).
func (s *Server) storeLimits(ctx context.Context, req settingsRequest) error {
	if req.MaxStreams != nil {
		if err := s.Store.SetSetting(ctx, SettingMaxStreams, strconv.Itoa(*req.MaxStreams)); err != nil {
			return err
		}
	}
	if req.MaxTranscodes != nil {
		return s.Store.SetSetting(ctx, SettingMaxTranscodes, strconv.Itoa(*req.MaxTranscodes))
	}
	return nil
}

// ApplyLimits gives the session manager the limits the flags, the settings and
// the defaults add up to. It is called at start, when the GPU probe is done, and
// after every change of the settings.
func (s *Server) ApplyLimits(ctx context.Context) {
	if s.Streams == nil {
		return
	}
	l := s.playLimits(ctx)
	s.Streams.SetLimits(l.streams, l.transcodes)
}

// ApplyLimitsWhenReady is ApplyLimits now, and again once the GPU probe has
// finished if it had not: the default number of transcodes needs its answer.
func (s *Server) ApplyLimitsWhenReady(ctx context.Context) {
	s.ApplyLimits(ctx)
	if !s.HW.Ready() {
		go func() {
			s.HW.Info()
			s.ApplyLimits(ctx)
		}()
	}
}
