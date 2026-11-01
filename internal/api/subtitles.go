package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"ply/internal/playback"
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
		if st.Type == "subtitle" && st.Index == idx && subs.IsText(st.Codec) && !playback.IsImageSubtitle(st.Codec) {
			return subs.Source{ItemID: e.ID, Key: key, Input: e.Path, Stream: idx, MTime: e.MTime}, true
		}
	}
	return subs.Source{}, false
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
	path, err := s.Subs.Path(r.Context(), src)
	switch {
	case errors.Is(err, subs.ErrConvert):
		s.Logger.Warn("subtitle conversion failed", "item", e.ID, "key", key, "err", err)
		writeError(w, http.StatusUnprocessableEntity, "subtitle could not be converted")
		return
	case errors.Is(err, context.Canceled) || r.Context().Err() != nil:
		return // the client went away
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
