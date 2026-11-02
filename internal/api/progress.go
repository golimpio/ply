package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"ply/internal/store"
)

// continueLimit is how many items GET /api/continue returns.
const continueLimit = 20

type progressRequest struct {
	PositionSec float64 `json:"positionSec"`
	DurationSec float64 `json:"durationSec"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// handleProgress records the playback position of an item. The response is the
// resulting state: null when the position was too early to count as started.
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var req progressRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !finite(req.PositionSec) || !finite(req.DurationSec) || req.PositionSec < 0 || req.DurationSec < 0 {
		writeError(w, http.StatusBadRequest, "positionSec and durationSec must be non-negative numbers")
		return
	}
	dur := req.DurationSec
	if dur == 0 {
		dur = e.DurationSec // the client did not know it yet
	}
	pos := req.PositionSec
	if dur > 0 && pos > dur {
		pos = dur
	}
	p, err := s.Store.SetProgress(r.Context(), e.ID, pos, dur)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var out *Progress
	if p != nil {
		out = &Progress{PositionSec: p.PositionSec, Watched: p.Watched}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContinue lists the started, unfinished items, most recently played first.
func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.Continue(r.Context(), continueLimit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.summaries(r.Context(), entries))
}
