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
	ID          string    `json:"id"`
	SessionID   string    `json:"sessionId"`
	ItemID      int64     `json:"itemId"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind"` // transcode | remux | direct
	Client      string    `json:"client"`
	StartedAt   time.Time `json:"startedAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	PositionSec float64   `json:"positionSec"`
	Paused      bool      `json:"paused"`
}

func playingEntries(list []stream.PlayInfo) []PlayingEntry {
	out := make([]PlayingEntry, len(list))
	for i, p := range list {
		out[i] = PlayingEntry{ID: p.ID, SessionID: p.ID, ItemID: p.ItemID, Title: p.Title, Kind: p.Kind, Client: p.Client,
			StartedAt: p.StartedAt.UTC(), LastSeenAt: p.LastSeenAt.UTC(), PositionSec: p.PositionSec, Paused: p.Paused}
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
}

// handleSessionHeartbeat is POST /api/sessions/{sid}/heartbeat: the player says
// it is alive, where it is and whether it is paused. A session that has had
// neither a heartbeat nor a request for a segment in StreamIdle is stopped, so a
// player beats while it lives, paused or not (every 10 s). The body may be
// empty. 204, or 404 if the session is gone (the player then starts another).
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
	if s.Streams == nil || !s.Streams.Heartbeat(r.PathValue("sid"), stream.Beat{Position: req.PositionSec, Paused: req.Paused}) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// directKey is who is playing which item directly: the item, the client's
// address and its User-Agent.
func directKey(e store.Entry, r *http.Request) stream.DirectKey {
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
	var pos float64
	if e.Progress != nil {
		pos = e.Progress.PositionSec
	}
	_, done, err := s.Streams.Direct(directKey(e, r), playTitle(e), clientLabel(r), pos)
	var le *stream.LimitError
	if errors.As(err, &le) {
		writeLimit(w, r, le)
		return nothing, false
	}
	if err != nil {
		return nothing, true // a manager that is closing: let the file go
	}
	return done, true
}

// checkDirect is admitDirect for the request that only announces a direct play
// (POST /playback): the play is counted when its file is asked for.
func (s *Server) checkDirect(w http.ResponseWriter, r *http.Request, e store.Entry) bool {
	if s.Streams == nil {
		return true
	}
	var le *stream.LimitError
	if errors.As(s.Streams.CheckDirect(directKey(e, r)), &le) {
		writeLimit(w, r, le)
		return false
	}
	return true
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
		name, flag, source string
		value              *int
		ceiling            int
	}{
		{"maxStreams", "-max-streams or PLY_MAX_STREAMS", cur.streamsSource, req.MaxStreams, config.MaxStreamsCeiling},
		{"maxTranscodes", "-max-transcodes or PLY_MAX_TRANSCODES", cur.transcodesSource, req.MaxTranscodes, config.MaxTranscodesCeiling},
	} {
		switch {
		case c.value == nil:
		case *c.value < 1 || *c.value > c.ceiling:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a whole number from 1 to %d", c.name, c.ceiling))
			return false
		case c.source == limitFromFlag:
			writeError(w, http.StatusConflict, fmt.Sprintf("%s is set by %s and cannot be changed here", c.name, c.flag))
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
