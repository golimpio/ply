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

	"ply/internal/playback"
	"ply/internal/store"
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
	entries, err := s.Store.ListItems(r.Context(), store.ListFilter{Kind: kind, Show: q.Get("show")})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]ItemSummary, len(entries))
	for i, e := range entries {
		out[i] = summarize(e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleShows(w http.ResponseWriter, r *http.Request) {
	shows, err := s.Store.Shows(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]ShowSummary, len(shows))
	for i, sh := range shows {
		out[i] = ShowSummary{Show: sh.Name, Seasons: sh.Seasons, EpisodeCount: sh.EpisodeCount}
	}
	writeJSON(w, http.StatusOK, out)
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
	profile, ok := playback.Builtin(target)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown profile %q", target))
		return
	}
	pb := s.playbackFor(e, profile, playback.NoOptions, 0)
	writeJSON(w, http.StatusOK, detail(e, pb))
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
	writeJSON(w, http.StatusOK, s.playbackFor(e, profile, opts, req.StartSec))
}

func hasStream(e store.Entry, typ string, index int) bool {
	for _, st := range e.Streams {
		if st.Type == typ && st.Index == index {
			return true
		}
	}
	return false
}

// playbackFor turns a decision into the API's Playback. Only direct play has
// a URL until HLS sessions exist (M3).
func (s *Server) playbackFor(e store.Entry, p playback.Profile, opts playback.Options, startSec float64) Playback {
	d := playback.Decide(playback.Item{Container: e.Container}, e.Streams, p, opts)
	pb := Playback{
		Method:      d.Method,
		VideoAction: d.VideoAction,
		AudioAction: d.AudioAction,
		AudioIndex:  d.AudioIndex,
		Reasons:     d.Reasons,
		StartSec:    startSec,
	}
	if d.Method == playback.MethodDirect {
		pb.URL = s.withToken(fmt.Sprintf("/api/items/%d/file", e.ID))
		pb.StartSec = 0 // stream time 0 is file time 0; the client seeks itself
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

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Scanner.Status(r.Context()))
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
