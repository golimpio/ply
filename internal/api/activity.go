package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"ply/internal/jobs"
)

// The Activity page's endpoints (docs/M9-ARCHITECTURE.md, "Live activity feed"):
//
//	GET  /api/jobs                       the queue: running, queued, history, paused, the tiers
//	POST /api/jobs/{id}/cancel|retry|bump
//	POST /api/queue/pause|resume
//	GET  /api/events                     the same, live (Server-Sent Events)
//
// They are private (see private): job titles name the library's folders.

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "the background queue is not running")
		return
	}
	writeJSON(w, http.StatusOK, s.Jobs.Snapshot())
}

func (s *Server) handleJobAction(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "the background queue is not running")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	var job *jobs.Job
	switch r.PathValue("action") {
	case "cancel":
		err = s.Jobs.Cancel(id)
	case "bump":
		err = s.Jobs.Bump(id)
	case "retry":
		var j jobs.Job
		j, err = s.Jobs.Retry(r.Context(), id)
		job = &j
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "job not found (finished jobs are kept for a week)")
	case errors.Is(err, jobs.ErrState):
		writeError(w, http.StatusConflict, "not possible now: the job has moved on (reload the page)")
	case err != nil:
		s.internalError(w, r, err)
	case job != nil:
		writeJSON(w, http.StatusOK, job)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "the background queue is not running")
		return
	}
	paused := strings.HasSuffix(r.URL.Path, "/pause")
	if err := s.Jobs.SetPaused(r.Context(), paused); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sseHeartbeat keeps the stream alive through proxies and lets a client notice a
// dead server.
const sseHeartbeat = 15 * time.Second

// handleEvents streams the queue's events. The first event is a snapshot (the
// whole queue), unless the client reconnects with a Last-Event-ID that is still
// in the replay buffer: then it gets what it missed. The token, for a server that
// needs one, comes as ?token= (EventSource cannot send headers).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "the background queue is not running")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	var after int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	missed, resync, ch, cancel := s.Jobs.Subscribe(after)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)

	send := func(id int64, typ string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, typ, b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	// Tell the browser how long to wait before reconnecting.
	fmt.Fprint(w, "retry: 3000\n\n")
	if resync {
		// The snapshot carries the id of the newest event it includes: replay after it.
		if err := send(s.Jobs.LastSeq(), "snapshot", s.Jobs.Snapshot()); err != nil {
			return
		}
	} else {
		for _, e := range missed {
			if err := send(e.Seq, e.Type, e); err != nil {
				return
			}
		}
	}
	beat := time.NewTicker(sseHeartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return // too slow to keep up: the browser reconnects and gets a snapshot
			}
			if e.Type == "snapshot" {
				if send(e.Seq, "snapshot", s.Jobs.Snapshot()) != nil {
					return
				}
				continue
			}
			if send(e.Seq, e.Type, e) != nil {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// isBrowse picks the requests that are someone browsing the library: they hold
// the background queue for a moment (see tier.Governor.Track). Polling, the
// activity feed, and playback (files, segments, subtitles, progress) are not.
func isBrowse(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := path.Clean(r.URL.Path)
	if !strings.HasPrefix(p, "/api/") {
		return false
	}
	for _, prefix := range []string{"/api/events", "/api/jobs", "/api/queue", "/api/scan", "/api/metadata/status",
		"/api/sessions", "/api/info"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return false
		}
	}
	if rest, ok := strings.CutPrefix(p, "/api/items/"); ok {
		_, sub, _ := strings.Cut(rest, "/")
		if sub == "file" || strings.HasPrefix(sub, "subtitles/") {
			return false
		}
	}
	return true
}
