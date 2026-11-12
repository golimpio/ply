package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/text/language"

	"ply/internal/store"
	"ply/internal/stream"
)

// airplaySubtitle resolves a rendition through its session, so it cannot be
// retargeted to another item or survive an explicit stop of the video.
func (s *Server) airplaySubtitle(w http.ResponseWriter, r *http.Request) (*stream.Session, store.Entry, Subtitle, bool) {
	sess, ok := s.session(w, r)
	if !ok {
		return nil, store.Entry{}, Subtitle{}, false
	}
	id, key := sess.Subtitle()
	if key == "" || s.Subs == nil {
		writeError(w, http.StatusNotFound, "subtitle not found")
		return nil, store.Entry{}, Subtitle{}, false
	}
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	e, ok := s.loadEntry(w, r)
	if !ok {
		return nil, store.Entry{}, Subtitle{}, false
	}
	for _, sub := range s.subtitlesOf(e) {
		if sub.Key == key && !sub.Image && sub.URL != "" {
			return sess, e, sub, true
		}
	}
	writeError(w, http.StatusNotFound, "subtitle not found")
	return nil, store.Entry{}, Subtitle{}, false
}

// HLS quoted attributes cannot contain a quote or a line break.
func hlsAttribute(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

func (s *Server) handleAirPlayMaster(w http.ResponseWriter, r *http.Request) {
	sess, e, sub, ok := s.airplaySubtitle(w, r)
	if !ok {
		return
	}
	if _, err := sess.Playlist(r.Context(), s.uriSuffix(r)); err != nil {
		s.streamError(w, r, err, "playlist")
		return
	}
	info, err := sess.HLSInfo(r.Context())
	if err != nil {
		s.streamError(w, r, err, "stream metadata")
		return
	}
	version := 7
	if sess.MPEGTS() {
		version = 4
	}
	lang, err := language.Parse(sub.Language)
	if err != nil {
		lang = language.Und
	}
	name := hlsAttribute(sub.Title)
	if name == "" && lang != language.Und {
		name = lang.String()
	}
	if name == "" {
		name = "Subtitles"
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"%s\",DEFAULT=YES,AUTOSELECT=YES,LANGUAGE=\"%s\",URI=\"subtitles.m3u8%s\"\n#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q,RESOLUTION=%dx%d,SUBTITLES=\"subs\"\nindex.m3u8%s\n",
		version, name, lang.String(), s.uriSuffix(r), max(e.Bitrate, 12000000), info.Codecs, info.Width, info.Height, s.uriSuffix(r))
}

func (s *Server) handleAirPlaySubtitles(w http.ResponseWriter, r *http.Request) {
	sess, _, _, ok := s.airplaySubtitle(w, r)
	if !ok {
		return
	}
	if _, err := sess.Playlist(r.Context(), s.uriSuffix(r)); err != nil {
		s.streamError(w, r, err, "playlist")
		return
	}
	duration := sess.Plan().Duration
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		writeError(w, http.StatusUnprocessableEntity, "item has no known duration")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	// A single WebVTT segment covers the complete file timeline, including
	// resumed playback. Cue timestamps are never rebased to the seek position.
	fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:%.0f\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:%.3f,\nsubtitles.vtt%s\n#EXT-X-ENDLIST\n",
		math.Ceil(duration), duration, s.uriSuffix(r))
}

func (s *Server) handleAirPlayVTT(w http.ResponseWriter, r *http.Request) {
	sess, _, sub, ok := s.airplaySubtitle(w, r)
	if !ok {
		return
	}
	release := sess.Hold()
	defer release()
	// Reuse normal subtitle extraction, including Follow's progressive response
	// on slow shares. This rendition always serves a complete WebVTT resource:
	// byte ranges in the source would not match the inserted timestamp map.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-sess.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	r = r.Clone(ctx)
	r.Header.Del("Range")
	r.Header.Del("If-Range")
	r.SetPathValue("file", sub.Key+".vtt")
	s.handleSubtitle(&hlsVTTWriter{ResponseWriter: w}, r)
}

// hlsVTTWriter maps full-file subtitle times to ply's media clock. It only buffers
// the first line, so cues still reach the receiver while extraction is running.
type hlsVTTWriter struct {
	http.ResponseWriter
	first     []byte
	started   bool
	transform bool
	injected  bool
}

var hlsTimestampMap = fmt.Sprintf("X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", int64(stream.TimelineOffset*90000))

func (w *hlsVTTWriter) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started = true
	w.transform = status == http.StatusOK && strings.HasPrefix(w.Header().Get("Content-Type"), "text/vtt")
	if w.transform {
		if n, err := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(n+int64(len(hlsTimestampMap)), 10))
		}
		w.Header().Del("Accept-Ranges")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *hlsVTTWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if !w.transform || w.injected {
		return w.ResponseWriter.Write(p)
	}
	w.first = append(w.first, p...)
	i := bytes.IndexByte(w.first, '\n')
	if i < 0 {
		if len(w.first) > 4096 {
			return 0, fmt.Errorf("WebVTT header too long")
		}
		return len(p), nil
	}
	if _, err := w.ResponseWriter.Write(w.first[:i+1]); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(w.ResponseWriter, hlsTimestampMap); err != nil {
		return 0, err
	}
	_, err := w.ResponseWriter.Write(w.first[i+1:])
	w.first = nil
	w.injected = true
	return len(p), err
}

func (w *hlsVTTWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
