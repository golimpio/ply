package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"ply/internal/art"
	"ply/internal/playback"
	"ply/internal/store"
)

// ArtURLs are an item's artwork links; Color is "#rrggbb" once an image exists.
type ArtURLs struct {
	Backdrop string `json:"backdrop"`
	Thumb    string `json:"thumb"`
	Color    string `json:"color,omitempty"`
}

func artSource(e store.Entry) art.Source {
	src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime, DurationSec: e.DurationSec}
	if v, ok := playback.VideoStream(e.Streams); ok {
		src.HDR = v.HDR() != ""
	}
	return src
}

// artOf returns e's artwork links, or nil when art is disabled or e has no video.
func (s *Server) artOf(e store.Entry) *ArtURLs {
	if s.Art == nil {
		return nil
	}
	if _, ok := playback.VideoStream(e.Streams); !ok {
		return nil
	}
	u := func(k art.Kind) string {
		return s.withToken(fmt.Sprintf("/api/items/%d/art/%s?v=%d", e.ID, k, e.MTime))
	}
	a := &ArtURLs{Backdrop: u(art.Backdrop), Thumb: u(art.Thumb)}
	a.Color, _ = s.Art.Colour(artSource(e))
	return a
}

func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	kind, ok := art.ParseKind(r.PathValue("kind"))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if s.Art == nil || !ok || err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	e, err := s.Store.GetEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p, err := s.Art.Path(r.Context(), artSource(e), kind)
	switch {
	case errors.Is(err, art.ErrNoFrame):
		writeError(w, http.StatusNotFound, "no artwork")
		return
	case r.Context().Err() != nil:
		return // the client went away
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", art.ContentType)
	// The URL carries ?v=<mtime>: a changed file gets a new URL, so cache forever.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
