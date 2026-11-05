package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/subs"
)

func extKey(n int) string { return "ext" + strconv.Itoa(n) }

func (s *Server) subtitleURL(itemID int64, key string) string {
	return s.withToken(fmt.Sprintf("/api/items/%d/subtitles/%s.vtt", itemID, key))
}

// canonicalIndex parses a non-negative decimal without sign or leading zeros,
// so every track has exactly one URL.
func canonicalIndex(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && strconv.Itoa(n) == s
}

// subtitleSource resolves a key to something the converter can read. Image
// tracks, unknown streams and unknown sidecars have none.
func subtitleSource(e store.Entry, key string) (subs.Source, bool) {
	if rest, ok := strings.CutPrefix(key, "ext"); ok {
		n, ok := canonicalIndex(rest)
		if !ok || n >= len(e.External) {
			return subs.Source{}, false
		}
		fi, err := os.Stat(e.External[n].Path)
		if err != nil || !fi.Mode().IsRegular() {
			return subs.Source{}, false
		}
		return subs.Source{ItemID: e.ID, Key: key, Input: e.External[n].Path, Stream: -1, MTime: fi.ModTime().UnixNano()}, true
	}
	idx, ok := canonicalIndex(key)
	if !ok {
		return subs.Source{}, false
	}
	for _, st := range e.Streams {
		if st.Index == idx {
			return embeddedSource(e, st)
		}
	}
	return subs.Source{}, false
}

// embeddedSource is the converter's view of the text subtitle stream st of e.
func embeddedSource(e store.Entry, st probe.Stream) (subs.Source, bool) {
	if st.Type != "subtitle" || !subs.IsText(st.Codec) || playback.IsImageSubtitle(st.Codec) {
		return subs.Source{}, false
	}
	return subs.Source{ItemID: e.ID, Key: strconv.Itoa(st.Index), Input: e.Path, Stream: st.Index, MTime: e.MTime}, true
}

// EmbeddedSubtitles lists the text subtitle streams of e: what the converter can
// make of its container. It reads nothing from disk.
func EmbeddedSubtitles(e store.Entry) []subs.Source {
	var out []subs.Source
	for _, st := range e.Streams {
		if src, ok := embeddedSource(e, st); ok {
			out = append(out, src)
		}
	}
	return out
}

// SubtitleSources lists every subtitle of e the converter can make: its sidecar
// files that are there, then its text streams. (Sidecars cost a stat each.)
func SubtitleSources(e store.Entry) []subs.Source {
	var out []subs.Source
	for n := range e.External {
		if src, ok := subtitleSource(e, extKey(n)); ok {
			out = append(out, src)
		}
	}
	return append(out, EmbeddedSubtitles(e)...)
}

// handleSubtitle serves one text subtitle track as WebVTT. A bitmap track
// (PGS, VobSub, DVB) has no text: it can only be burned in, so it is a 404 here.
func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	key, isVTT := strings.CutSuffix(r.PathValue("file"), ".vtt")
	if !isVTT || s.Subs == nil {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return
	}
	src, ok := subtitleSource(e, key)
	if !ok {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return
	}
	var path string
	var err error
	switch {
	case src.Stream >= 0 && r.Method != http.MethodGet && !s.Subs.Cached(src):
		// A HEAD asks about the track, it does not start the read of a whole film for it.
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	case src.Stream < 0 || r.Method != http.MethodGet:
		// A sidecar is a small file; a HEAD of a track that is cached is the file.
		path, err = s.Subs.Path(r.Context(), src)
	default:
		// An embedded track means reading the container end to end, which over a slow share
		// takes many minutes: the WebVTT goes out as ffmpeg writes it.
		fo, ferr := s.Subs.Follow(r.Context(), src, EmbeddedSubtitles(e))
		if ferr != nil {
			err = ferr
			break
		}
		defer fo.Close()
		grace := time.NewTimer(s.subsGrace)
		defer grace.Stop()
		select {
		case <-fo.Done(): // cached, or quick, or failed at once: answered as any file is
			path, err = fo.Result()
		case <-grace.C:
			s.streamSubtitle(w, r, e.ID, key, fo)
			return
		case <-r.Context().Done():
			return
		}
	}
	switch {
	case r.Context().Err() != nil:
		return // the client went away
	case errors.Is(err, subs.ErrConvert):
		s.Logger.Warn("subtitle conversion failed", "item", e.ID, "key", key, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "subtitle could not be converted")
		return
	case errors.Is(err, subs.ErrUnavailable):
		// The file cannot be read now (a share that is not mounted yet or asleep): not the
		// subtitle's fault, and not for good.
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "the file is not available right now")
		return
	case errors.Is(err, context.Canceled), errors.Is(err, subs.ErrClosed):
		// Not the client (it is here): the extraction this request waited for was stopped by
		// someone else (a job cancelled in Activity) or by ply stopping.
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "the subtitle extraction was stopped; try again")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	f, err := os.Open(path)
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
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// streamSubtitle answers while the track is still being extracted: 200 at once, then
// the cues as they are written, each flushed, until the file is complete. The browser
// parses a <track> as it arrives, so the first minutes of a film show their subtitles
// long before the last has been read. The answer is not cached (it may be cut
// short), and does not honour Range.
//
// A failure after the first byte cannot change the status: the cues so far stay,
// the answer ends, and the next request meets the remembered failure (422).
func (s *Server) streamSubtitle(w http.ResponseWriter, r *http.Request, id int64, key string, fo io.Reader) {
	h := w.Header()
	h.Set("Content-Type", "text/vtt; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // a proxy in front must not hold the cues back
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	buf := make([]byte, 32<<10)
	for {
		n, err := fo.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // the client went away; the extraction goes on
			}
			_ = rc.Flush()
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF), r.Context().Err() != nil:
			return
		default:
			s.Logger.Warn("subtitle conversion failed while it was served", "item", id, "key", key, "err", err)
			return
		}
	}
}
