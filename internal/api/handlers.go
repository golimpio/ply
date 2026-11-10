package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ply/internal/library"
	"ply/internal/playback"
	"ply/internal/store"
	"ply/internal/watch"
)

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           s.ID,
		"name":         s.Name,
		"version":      s.Version,
		"authRequired": s.Token != "",
	})
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "" && kind != store.KindMovie && kind != store.KindEpisode {
		writeError(w, http.StatusBadRequest, `kind must be "movie" or "episode"`)
		return
	}
	rq, err := parseRatingQuery(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fields := q.Get("fields")
	if fields != "" && fields != fieldsCard {
		writeError(w, http.StatusBadRequest, `fields must be "card"`)
		return
	}
	if fields == fieldsCard && kind == "" {
		writeError(w, http.StatusBadRequest, `fields=card needs kind=movie or kind=episode`)
		return
	}
	limit, err := parseLimit(q.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	shape := ""
	if fields == fieldsCard {
		shape = fieldsCard
		if kind == store.KindEpisode {
			shape = projectionEpisodeCard
		}
	}
	// The limit is part of the key as well: a limited list and the whole one never share a validator.
	key := shape
	if limit > 0 {
		if key == "" {
			key = "limit" + strconv.Itoa(limit)
		} else {
			key += ".limit" + strconv.Itoa(limit)
		}
	}
	etag, notModified := s.listCache(w, r, key)
	if notModified {
		return
	}
	// A limit the database can apply is applied there: the cut is of the same order, so the rows are
	// the ones the Go cut would have kept, and only the listed rows' streams and summaries are read
	// (the matching item rows are still scanned and sorted by the order: 20 rows of 5,000 cost 16 ms
	// against 159). With sort=rating or minRating the whole list must be read first (the filters are
	// Go code over the summaries), so the push-down is off and the cut below does the work.
	filter := store.ListFilter{Kind: kind, Show: q.Get("show")}
	if limit > 0 && !rq.active() {
		filter.Limit = limit
	}
	entries, err := s.Store.ListItems(r.Context(), filter)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sums, degraded := s.summariesChecked(r.Context(), entries)
	out := applyRatingQuery(sums, rq, func(it ItemSummary) *RatingEntry { return it.Rating })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	switch shape {
	case projectionEpisodeCard:
		writeList(w, etag, episodeCards(out), degraded)
	case fieldsCard:
		writeList(w, etag, itemCards(out), degraded)
	default:
		writeList(w, etag, out, degraded)
	}
}

// limitMax is the most rows one request of a list answers.
const limitMax = 500

// parseLimit reads limit: how many rows of the list to answer, 0 for all of them. It must be a number
// from 1 to limitMax: no page asks for nothing, and one request never takes a whole big library.
func parseLimit(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > limitMax {
		return 0, fmt.Errorf("limit must be a number from 1 to %d", limitMax)
	}
	return n, nil
}

// loadEntry resolves {id}; it writes the error response and reports false if
// the item cannot be served.
func (s *Server) loadEntry(w http.ResponseWriter, r *http.Request) (store.Entry, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "item not found")
		return store.Entry{}, false
	}
	e, err := s.Store.GetEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return store.Entry{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return store.Entry{}, false
	}
	return e, true
}

func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "avplayer"
	}
	if _, ok := playback.Builtin(target); !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown profile %q", target))
		return
	}
	s.writeDetail(w, r, e, target)
}

// writeDetail answers with the full view of an item, deciding playback for the
// built-in profile target.
func (s *Server) writeDetail(w http.ResponseWriter, r *http.Request, e store.Entry, target string) {
	profile, _ := playback.Builtin(target)
	pb := s.playbackFor(e, profile, playback.NoOptions, 0)
	d := s.detail(e, pb)
	d.Art = s.artOf(e)
	v := s.newMetaView(r.Context(), []store.Entry{e})
	v.apply(&d.ItemSummary, e)
	d.EpisodeInfo = v.episodeInfo(e)
	if id := v.titleIDOf(e); id != 0 {
		if td, err := s.Store.GetTitleDetail(r.Context(), id); err == nil {
			d.Meta = s.titleDetail(td, v.primary)
		} else if !errors.Is(err, store.ErrNotFound) {
			s.Logger.Warn("metadata: reading a title", "id", id, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, d)
}

type playbackRequest struct {
	Profile       string            `json:"profile"`
	Custom        *playback.Profile `json:"custom"`
	StartSec      float64           `json:"startSec"`
	AudioIndex    *int              `json:"audioIndex"`
	SubtitleIndex *int              `json:"subtitleIndex"`
}

func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}

	var req playbackRequest
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.StartSec < 0 || math.IsNaN(req.StartSec) || math.IsInf(req.StartSec, 0) {
		writeError(w, http.StatusBadRequest, "startSec must be a non-negative number")
		return
	}

	var profile playback.Profile
	switch {
	case req.Custom != nil:
		profile = *req.Custom
		if profile.Name == "" {
			profile.Name = "custom"
		}
	default:
		name := req.Profile
		if name == "" {
			name = "avplayer"
		}
		if profile, ok = playback.Builtin(name); !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown profile %q", name))
			return
		}
	}

	opts := playback.NoOptions
	if req.AudioIndex != nil {
		if !hasStream(e, "audio", *req.AudioIndex) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no audio stream with index %d", *req.AudioIndex))
			return
		}
		opts.AudioIndex = *req.AudioIndex
	}
	if req.SubtitleIndex != nil {
		if !hasStream(e, "subtitle", *req.SubtitleIndex) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no subtitle stream with index %d", *req.SubtitleIndex))
			return
		}
		opts.SubtitleIndex = *req.SubtitleIndex
	}
	// From here until the first seconds of the play are over, no frame is grabbed from this file:
	// the grab would read it at the same time as the play.
	endPlay := s.beginPlay(e.ID)
	started := false
	defer func() { endPlay(started) }()

	d := decide(e, profile, opts)
	pb := s.playbackFrom(e, d, req.StartSec)
	if d.Method == playback.MethodDirect {
		// Counted when the file is asked for; a full house is said now, with the list.
		if !s.checkDirect(w, r, e) {
			return
		}
	} else if !s.attachSession(w, r, e, d, profile, req.StartSec, &pb) {
		return
	}
	started = true
	writeJSON(w, http.StatusOK, pb)
}

func hasStream(e store.Entry, typ string, index int) bool {
	for _, st := range e.Streams {
		if st.Type == typ && st.Index == index {
			return true
		}
	}
	return false
}

func decide(e store.Entry, p playback.Profile, opts playback.Options) playback.Decision {
	return playback.Decide(playback.Item{Container: e.Container}, e.Streams, p, opts)
}

// playbackFor describes how e would be delivered to p. Only direct play has a
// URL: a remux or transcode needs a session, which only POST /playback
// creates.
func (s *Server) playbackFor(e store.Entry, p playback.Profile, opts playback.Options, startSec float64) Playback {
	d := decide(e, p, opts)
	pb := s.playbackFrom(e, d, startSec)
	if v, ok := playback.VideoStream(e.Streams); ok && d.Method == playback.MethodTranscode {
		_, _, reasons := s.videoSpecs(v, d, p)
		pb.Reasons = append(pb.Reasons, reasons...)
	}
	return pb
}

func (s *Server) playbackFrom(e store.Entry, d playback.Decision, startSec float64) Playback {
	pb := Playback{
		Method:      d.Method,
		VideoAction: d.VideoAction,
		AudioAction: d.AudioAction,
		AudioIndex:  d.AudioIndex,
		Reasons:     d.Reasons,
		StartSec:    startSec,
	}
	if d.Method == playback.MethodDirect {
		// The file is served whole: stream time 0 is file time 0, and the client seeks to startSec
		// itself. startSec stays what was asked for (DESIGN.md: "echoes the requested resume position").
		pb.URL = s.withToken(fmt.Sprintf("/api/items/%d/file", e.ID))
	}
	return pb
}

var contentTypes = map[string]string{
	"mp4":  "video/mp4",
	"mov":  "video/quicktime",
	"m4v":  "video/x-m4v",
	"mkv":  "video/x-matroska",
	"webm": "video/webm",
	"ts":   "video/mp2t",
	"avi":  "video/x-msvideo",
}

func contentType(container string) string {
	if ct, ok := contentTypes[container]; ok {
		return ct
	}
	return "application/octet-stream"
}

// handleFile serves the original file with Range support. The path comes from
// the database only. http.ServeContent hands the *os.File to the connection
// so the kernel can sendfile it.
//
// Every GET counts as a direct play of the item by this client, for as long as
// the limit on plays is concerned: while it is being served (a player may read
// the whole file over one connection, for hours) and stream.DirectTTL after the
// last one ended; a client that is not counted yet is refused with a 503 when the
// limit is reached. A HEAD asks about the file, it does not play it: it is not
// counted, and not refused.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	f, err := os.Open(e.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "file not found")
		} else {
			s.internalError(w, r, err)
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if r.Method != http.MethodHead {
		done, ok := s.admitDirect(w, r, e)
		if !ok {
			return
		}
		defer done()
	}

	h := w.Header()
	h.Set("Content-Type", contentType(e.Container))
	h.Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	http.ServeContent(w, r, filepath.Base(e.Path), fi.ModTime(), f)
}

func (s *Server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	if !s.Scanner.Trigger() {
		writeError(w, http.StatusConflict, "scan already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// scanStatus is GET /api/scan: the scanner's state and what the watcher watches.
type scanStatus struct {
	library.Status
	Watching []watch.RootStatus `json:"watching"`
}

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	out := scanStatus{Status: s.Scanner.Status(r.Context()), Watching: []watch.RootStatus{}}
	if s.Watcher != nil {
		if roots := s.Watcher.Status(); roots != nil {
			out.Watching = roots
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// spaHandler serves the embedded SPA: files that exist, otherwise index.html
// so client-side routes survive a reload.
func spaHandler(ui fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+r.URL.Path)), "/")
		if ui != nil && name != "" {
			if fi, err := fs.Stat(ui, name); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFileFS(w, r, ui, name)
				return
			}
		}
		if ui == nil {
			http.Error(w, "web UI not available", http.StatusNotFound)
			return
		}
		if _, err := fs.Stat(ui, "index.html"); err != nil {
			http.Error(w, "web UI not built (run `npm run build` in web/)", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, ui, "index.html")
	})
}
