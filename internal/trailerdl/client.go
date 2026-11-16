// Package trailerdl downloads a title's trailer from YouTube or Vimeo, natively: no yt-dlp,
// no JavaScript runtime (docs/M13-TRAILERS.md, "The downloader").
package trailerdl

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// The YouTube app ply speaks as: the visionos innertube client, the one yt-dlp still uses
// without a JS runtime (no PO token, no signature or n challenge). This is the part YouTube
// breaks: when downloads start failing with "Sign in" or "no stream this client can read",
// compare with yt-dlp's yt_dlp/extractor/youtube/_base.py, INNERTUBE_CLIENTS['visionos'],
// and its default clients in _video.py.
const (
	ytClientName    = "VISIONOS"
	ytClientID      = "101"
	ytClientVersion = "1.02"
	ytClientUA      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15"
	ytOSName        = "visionOS"
	ytOSVersion     = "26.5.23O471"
	ytDeviceMake    = "Apple"
	ytDeviceModel   = "RealityDevice17,1"
)

// desktopUA is the browser the pages and the media requests say they come from.
const desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

// MaxHeight is the tallest video downloaded.
const MaxHeight = 1080

// ErrUnavailable is a video the site will not give: the reason is in the error's text.
// Asking again soon would get the same answer.
var ErrUnavailable = errors.New("not available")

// Stream is one media stream of a video, at an address that serves it.
type Stream struct {
	URL     string
	MIME    string // `video/mp4; codecs="avc1.640028"`
	Height  int    // 0 for audio
	Bitrate int64
	Size    int64 // 0 = not known
}

func (s Stream) isVideo() bool { return strings.HasPrefix(s.MIME, "video/") }
func (s Stream) isAudio() bool { return strings.HasPrefix(s.MIME, "audio/") }

// codec reports whether the stream's codecs start with prefix ("avc1", "mp4a").
func (s Stream) codec(prefix string) bool {
	_, codecs, _ := strings.Cut(s.MIME, "codecs=")
	return strings.HasPrefix(strings.Trim(codecs, `"`), prefix)
}

// Source is what a video is downloaded from.
type Source struct {
	Video  Stream // a muxed stream when Audio.URL is ""
	Audio  Stream
	HLS    string        // a master playlist ffmpeg reads instead of the streams ("" = none)
	Header http.Header   // what the media requests must send
	Wait   time.Duration // how long the site wants before the media is asked for (a start ad)
}

// pickVideo is the tallest video stream at most maxHeight high; at the same height, H.264
// (which plays everywhere without converting), then the higher bitrate.
func pickVideo(fs []Stream, maxHeight int) (Stream, bool) {
	var best Stream
	found := false
	for _, s := range fs {
		if !s.isVideo() || s.URL == "" || s.Height > maxHeight {
			continue
		}
		if !found || s.Height > best.Height ||
			(s.Height == best.Height && s.codec("avc1") && !best.codec("avc1")) ||
			(s.Height == best.Height && s.codec("avc1") == best.codec("avc1") && s.Bitrate > best.Bitrate) {
			best, found = s, true
		}
	}
	return best, found
}

// pickAudio is the AAC stream of the highest bitrate, else any audio stream of the highest.
func pickAudio(fs []Stream) (Stream, bool) {
	var best Stream
	found := false
	for _, s := range fs {
		if !s.isAudio() || s.URL == "" {
			continue
		}
		if !found || (s.codec("mp4a") && !best.codec("mp4a")) ||
			(s.codec("mp4a") == best.codec("mp4a") && s.Bitrate > best.Bitrate) {
			best, found = s, true
		}
	}
	return best, found
}
