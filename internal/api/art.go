package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"ply/internal/art"
	"ply/internal/playback"
	"ply/internal/store"
	"ply/internal/tier"
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

// artDiskRetry is the Retry-After of a frame grab put off for a film that plays from the
// same disk: nobody knows how long a film is, so it is not a promise.
const artDiskRetry = 30 * time.Second

// artHold says whether the file of e is not for a frame grab now, and for how long it is
// certain to stay so: a play of e is being set up (and for a few seconds after), when the
// grab would share the link with the first seconds of the play; or a film plays from the
// same disk, or a viewer is stalled, and the grab would take its bandwidth (the same
// question the queue asks of the art.grab job it leaves the image to). A person browsing
// does not hold it: browsing is what asks.
//
// The disk is asked of the item's library folder, not of its file: a page of a library asks
// for the pictures of a hundred films, and the governor would look up (stat, on a slow share)
// the disk of each, and forget them all when it has 512.
func (s *Server) artHold(e store.Entry) (time.Duration, bool) {
	if left, held := s.plays.holding(e.ID, s.clock()); held {
		return left, true
	}
	if s.Governor != nil {
		if h := s.Governor.Check(tier.Need{Disks: []string{cmp.Or(e.Root, e.Path)}}); h.Held && !h.Browsing {
			return artDiskRetry, true
		}
	}
	return 0, false
}

// artFile is the image of kind for e. It is the cached one, or one grabbed from the file
// now, except while artHold says the file is not for it: then the grab is left to the
// queue (ArtLater) and the answer is art.ErrDeferred.
func (s *Server) artFile(ctx context.Context, e store.Entry, kind art.Kind) (string, error) {
	src := artSource(e)
	if _, held := s.artHold(e); held {
		if p, ok := s.Art.Cached(src, kind); ok {
			return p, nil
		}
		s.deferArt(src, kind, false)
		return "", art.ErrDeferred
	}
	return s.Art.Path(ctx, src, kind)
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
	p, err := s.artFile(r.Context(), e, kind)
	switch {
	case errors.Is(err, art.ErrNoFrame):
		writeError(w, http.StatusNotFound, "no artwork")
		return
	case errors.Is(err, art.ErrDeferred):
		// The file is not for a frame grab now. The queue makes the image when nothing plays
		// from the disk; the page shows its placeholder. (At least a second: 0 would say "now".)
		left, held := s.artHold(e)
		if !held { // the grab was stopped by a play that is over already
			left = artHoldFor
		}
		retry := max(1, int(math.Ceil(left.Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeError(w, http.StatusServiceUnavailable, "artwork is made after the video has started")
		return
	case errors.Is(err, art.ErrUnavailable), errors.Is(err, art.ErrClosed):
		// The file is not reachable right now (an unmounted share), ffmpeg timed out, or we are
		// shutting down: not the same as "this file has no picture".
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "artwork temporarily unavailable")
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
