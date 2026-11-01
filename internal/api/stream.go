package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"time"

	"ply/internal/hwaccel"
	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
)

// attachSession starts an HLS session for a remux or transcode decision and
// fills in pb. It reports false after writing an error response.
func (s *Server) attachSession(w http.ResponseWriter, r *http.Request, e store.Entry, d playback.Decision,
	profile playback.Profile, startSec float64, pb *Playback) bool {
	if s.Streams == nil {
		pb.Reasons = append(pb.Reasons, "HLS streaming is not enabled on this server")
		return true
	}
	if fi, err := os.Stat(e.Path); err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "file not found")
		return false
	}
	video, ok := playback.VideoStream(e.Streams)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "item has no video stream")
		return false
	}
	if d.Method == playback.MethodTranscode && e.DurationSec <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "item has no known duration; it cannot be transcoded")
		return false
	}
	req := stream.Request{
		ItemID:      e.ID,
		Path:        e.Path,
		Container:   e.Container,
		DurationSec: e.DurationSec,
		VideoIndex:  video.Index,
		VideoCodec:  video.Codec,
		AudioIndex:  d.AudioIndex,
		AudioCopy:   d.AudioAction == playback.ActionCopy,
		StartSec:    startSec,
	}
	if a, ok := playback.AudioStream(e.Streams, d.AudioIndex); ok && d.AudioIndex >= 0 {
		req.AudioCodec = a.Codec
	}
	if d.Method == playback.MethodTranscode {
		var reasons []string
		req.Video, req.Fallback, reasons = s.videoSpecs(video, d, profile)
		pb.Reasons = append(pb.Reasons, reasons...)
	}

	sess, err := s.Streams.Create(r.Context(), req)
	switch {
	case errors.Is(err, stream.ErrTooManySessions):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too many active streams")
		return false
	case r.Context().Err() != nil:
		return false // the client gave up (the keyframe scan may take a while)
	case err != nil:
		s.internalError(w, r, err)
		return false
	}
	pb.URL = s.withToken("/api/sessions/" + sess.ID + "/index.m3u8")
	pb.SessionID = sess.ID
	pb.StartSec = startSec
	return true
}

// videoSpecs says how to transcode video for decision d: the spec for the
// encoder in use, a software fallback when that is a GPU encoder, and the
// reasons to show the client.
func (s *Server) videoSpecs(video probe.Stream, d playback.Decision, profile playback.Profile) (
	primary, fallback *stream.VideoSpec, reasons []string) {
	info := s.HW.Info()
	hreq := hwaccel.Request{
		Codec:        video.Codec,
		BitDepth:     video.BitDepth(),
		Width:        video.Width,
		Height:       video.Height,
		HDR:          video.HDR() != "",
		VideoIndex:   video.Index,
		BurnSubtitle: d.BurnSubtitle,
		MaxHeight:    profile.MaxHeight,
	}
	spec := info.Plan(hreq)
	primary = videoSpec(video.Codec, spec)
	if info.Encoder.Hardware() {
		fallback = videoSpec(video.Codec, info.SoftwareOnly().Plan(hreq))
	}
	return primary, fallback, spec.Reasons
}

func videoSpec(codec string, s hwaccel.Spec) *stream.VideoSpec {
	return &stream.VideoSpec{Codec: codec, Encode: s.Encode, Pre: s.Pre, Filter: s.Filter, Complex: s.Complex}
}

// session resolves {sid}, or writes a 404.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*stream.Session, bool) {
	if s.Streams != nil {
		if sess, ok := s.Streams.Get(r.PathValue("sid")); ok {
			return sess, true
		}
	}
	writeError(w, http.StatusNotFound, "session not found")
	return nil, false
}

// uriSuffix is appended to the URIs inside a playlist: players that cannot
// send headers (AirPlay, Cast receivers) need the token on every request.
func (s *Server) uriSuffix() string {
	if s.Token == "" {
		return ""
	}
	return "?token=" + url.QueryEscape(s.Token)
}

func (s *Server) handleSessionPlaylist(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Cache-Control", "no-cache")
	fmt.Fprint(w, sess.Playlist(s.uriSuffix()))
}

func (s *Server) handleSessionFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !stream.ValidName(name) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	sess, ok := s.session(w, r)
	if !ok {
		return
	}

	var (
		media io.ReadSeekCloser
		err   error
	)
	if name == stream.InitName {
		media, err = sess.OpenInit(r.Context())
	} else {
		idx, _ := stream.ParseSegmentName(name)
		var m *stream.Media
		if m, err = sess.OpenSegment(r.Context(), idx); err == nil {
			media = m
		}
	}
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		return // the player moved on
	case errors.Is(err, stream.ErrNoSegment), errors.Is(err, stream.ErrClosed), errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, stream.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "segment not ready")
		return
	default:
		s.internalError(w, r, err)
		return
	}
	defer media.Close()

	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "private, max-age=600")
	http.ServeContent(w, r, name, time.Time{}, media)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.Streams == nil || !s.Streams.Delete(r.PathValue("sid")) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
