package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	profile playback.Profile, startSec float64, subtitleKey string, pb *Playback) bool {
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
	if profile.Name == "airplay" && (video.Width <= 0 || video.Height <= 0) {
		writeError(w, http.StatusUnprocessableEntity, "video dimensions are unknown; rescan this item before using AirPlay")
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
		Title:       playTitle(e),
		Client:      clientLabel(r),
		Device:      s.playDevice(r),
		Wireless:    profile.Name == "airplay",
		MPEGTS:      profile.Name == "airplay",
		SubtitleKey: subtitleKey,
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
	var limit *stream.LimitError
	switch {
	case errors.As(err, &limit):
		writeLimit(w, r, limit)
		return false
	case r.Context().Err() != nil:
		return false // the client gave up (the keyframe scan may take a while)
	case err != nil:
		s.internalError(w, r, err)
		return false
	}
	playlist := "index.m3u8"
	if subtitleKey != "" {
		playlist = "master.m3u8"
	}
	pb.URL = s.withToken("/api/sessions/" + sess.ID + "/" + playlist)
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
		MaxHeight:    profile.HLSHeight(), // a transcode is played over HLS
	}
	// The legacy receiver has a width limit too; reuse the existing aspect-ratio
	// scaler by lowering its height cap for DCI and ultrawide sources.
	const airPlayMaxWidth = 1920
	if profile.Name == "airplay" && video.Width > airPlayMaxWidth && video.Height > 0 {
		hreq.MaxHeight = min(hreq.MaxHeight, max(2, int(float64(video.Height)*airPlayMaxWidth/float64(video.Width))))
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

// session resolves {sid}, or writes a 410 (stopped on purpose a moment ago) or a 404.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*stream.Session, bool) {
	if s.Streams != nil {
		if sess, ok := s.Streams.Get(r.PathValue("sid")); ok {
			return sess, true
		}
		if s.stoppedOnPurpose(w, r.PathValue("sid")) {
			return nil, false
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
	// The playlist waits for the plan to be settled (it can take a scan of the
	// file), so it can fail: the status says how, before any of it is written.
	playlist, err := sess.Playlist(r.Context(), s.uriSuffix())
	if err != nil {
		s.streamError(w, r, err, "playlist")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Cache-Control", "no-cache")
	fmt.Fprint(w, playlist)
}

// streamError answers a request to a session that failed: 404 for what is not
// there (a segment out of range, a session that is gone), 504 for what took too
// long, 502 for what the stream could not make (ffmpeg failed, or the keyframe
// index of the file was wrong and a scan did not put it right), 500 for the
// rest. Nothing is answered if the client left. what is what was asked for
// ("playlist", "segment"), for the 504.
func (s *Server) streamError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case r.Context().Err() != nil:
		return // the player moved on
	case errors.Is(err, stream.ErrNoSegment), errors.Is(err, stream.ErrClosed), errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, stream.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, what+" not ready")
	case errors.Is(err, stream.ErrIndex), errors.Is(err, stream.ErrFFmpeg):
		s.Logger.Error("stream failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadGateway, "stream failed")
	default:
		s.internalError(w, r, err)
	}
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

	if sess.MPEGTS() != strings.HasSuffix(name, ".ts") {
		writeError(w, http.StatusNotFound, "not found")
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
	if err != nil {
		s.streamError(w, r, err, "segment")
		return
	}
	defer media.Close()

	h := w.Header()
	if sess.MPEGTS() {
		h.Set("Content-Type", "video/mp2t")
	} else {
		h.Set("Content-Type", "video/mp4")
	}
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
