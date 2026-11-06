package api

import (
	"errors"
	"net/http"
	"strconv"

	"ply/internal/store"
)

// The calls that mark things watched or not (docs/DESIGN.md, "Watch state"). All take
// {"watched": true|false} and answer with the files they covered and the state each is in.

type watchedRequest struct {
	Watched *bool `json:"watched"`
}

// ItemWatch is the state of one file after a change: its watch state, and its progress, which is
// null for a file that is not started (marking a file unwatched clears its position, marking it
// watched sets it to 0). For a film, watched is the file's own, and all the film's files were covered.
type ItemWatch struct {
	ID int64 `json:"id"`
	WatchState
	Progress *Progress `json:"progress"`
}

// WatchResult is the answer of PUT .../watched: what was asked for, and every file it covered.
type WatchResult struct {
	Watched bool        `json:"watched"`
	Items   []ItemWatch `json:"items"`
}

func apiProgress(p *store.Progress) *Progress {
	if p == nil {
		return nil
	}
	return &Progress{PositionSec: p.PositionSec, Watched: p.Watched()}
}

// readWatched reads {"watched": bool}; it has answered 400 if it reports false.
func readWatched(w http.ResponseWriter, r *http.Request) (watched, ok bool) {
	var req watchedRequest
	if !decodeBody(w, r, &req) {
		return false, false
	}
	if req.Watched == nil {
		writeError(w, http.StatusBadRequest, `give "watched": true or false`)
		return false, false
	}
	return *req.Watched, true
}

// answerWatched writes the result of a change, or the error: 404 for what was not found.
func (s *Server) answerWatched(w http.ResponseWriter, r *http.Request, what string, watched bool, items []store.WatchedItem, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, what+" not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := WatchResult{Watched: watched, Items: make([]ItemWatch, len(items))}
	for i, it := range items {
		out.Items[i] = ItemWatch{ID: it.ID, WatchState: watchState(it.Progress), Progress: apiProgress(it.Progress)}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleItemWatched is PUT /api/items/{id}/watched. An episode, or a film with no title, is
// marked alone; a film with a title is marked in all of its versions, as PUT /api/titles/{id}/watched.
func (s *Server) handleItemWatched(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "item")
	if !ok {
		return
	}
	watched, ok := readWatched(w, r)
	if !ok {
		return
	}
	items, err := s.Store.SetItemWatched(r.Context(), id, watched)
	s.answerWatched(w, r, "item", watched, items, err)
}

// handleTitleWatched is PUT /api/titles/{id}/watched: every version of a film, or every episode of a series.
func (s *Server) handleTitleWatched(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r, "title")
	if !ok {
		return
	}
	watched, ok := readWatched(w, r)
	if !ok {
		return
	}
	items, err := s.Store.SetTitleWatched(r.Context(), id, watched)
	s.answerWatched(w, r, "title", watched, items, err)
}

// handleShowWatched is PUT /api/shows/{name}/watched: every episode of the show, matched or not.
func (s *Server) handleShowWatched(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok {
		return
	}
	watched, ok := readWatched(w, r)
	if !ok {
		return
	}
	items, err := s.Store.SetShowWatched(r.Context(), sh.Name, watched)
	s.answerWatched(w, r, "show", watched, items, err)
}

// handleSeasonWatched is PUT /api/shows/{name}/seasons/{n}/watched: every episode of the season
// (0 is the specials).
func (s *Server) handleSeasonWatched(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.findShow(w, r)
	if !ok {
		return
	}
	season, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || season < 0 {
		writeError(w, http.StatusNotFound, "season not found")
		return
	}
	watched, ok := readWatched(w, r)
	if !ok {
		return
	}
	items, err := s.Store.SetSeasonWatched(r.Context(), sh.Name, season, watched)
	s.answerWatched(w, r, "season", watched, items, err)
}
