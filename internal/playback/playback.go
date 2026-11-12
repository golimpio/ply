// Package playback decides how a file is delivered to a client: direct play,
// remux, or transcode.
package playback

import (
	"fmt"
	"strings"

	"ply/internal/probe"
)

// Stream is the stream description the decision is made from.
type Stream = probe.Stream

// Delivery methods.
const (
	MethodDirect    = "direct"
	MethodRemux     = "remux"
	MethodTranscode = "transcode"
)

// Stream actions.
const (
	ActionCopy      = "copy"
	ActionTranscode = "transcode"
)

// Profile describes what a client can play.
type Profile struct {
	Name        string   `json:"name"`
	Containers  []string `json:"containers"`
	VideoCodecs []string `json:"videoCodecs"`
	AudioCodecs []string `json:"audioCodecs"`
	RequireHVC1 bool     `json:"requireHvc1"` // avplayer: hevc must be tagged hvc1 for direct play
	MaxHeight   int      `json:"maxHeight"`   // 0 = unlimited
	// MaxHLSHeight is the tallest video the client plays over HLS, when that is less than
	// what it plays as a file (MaxHeight); 0 = MaxHeight.
	MaxHLSHeight   int      `json:"maxHlsHeight,omitempty"`
	ForceTranscode bool     `json:"forceTranscode,omitempty"` // always re-encode video and the selected audio
	HLS            bool     `json:"hls"`                      // client can play HLS (all of ours can)
	TextSubs       []string `json:"textSubs"`                 // subtitle formats renderable as sidecar (vtt)
}

// HLSHeight is the tallest video p plays over HLS (a remux or a transcode); 0 = no limit.
func (p Profile) HLSHeight() int {
	if p.MaxHLSHeight > 0 && (p.MaxHeight == 0 || p.MaxHLSHeight < p.MaxHeight) {
		return p.MaxHLSHeight
	}
	return p.MaxHeight
}

// Item is the per-file input to Decide.
type Item struct {
	Container string
}

// Options selects streams. A negative index means "default / none".
type Options struct {
	AudioIndex    int // requested audio stream (ffprobe index)
	SubtitleIndex int // requested subtitle stream (ffprobe index)
}

// NoOptions requests the default audio and no subtitle.
var NoOptions = Options{AudioIndex: -1, SubtitleIndex: -1}

// Decision is how to deliver the file to the profile.
//
// Order of preference: direct, remux (copy everything), remux with audio
// transcode (copy video), full transcode.
type Decision struct {
	Method       string // direct | remux | transcode
	VideoAction  string // copy | transcode
	AudioAction  string // copy | transcode ("copy" when there is no audio)
	AudioIndex   int    // chosen audio stream (ffprobe index), -1 none
	BurnSubtitle int    // ffprobe index of the image subtitle to burn in, -1 none
	Reasons      []string
}

// BuiltinNames lists the built-in profiles.
var BuiltinNames = []string{"avplayer", "browser", "chromecast", "airplay"}

// Builtin returns a built-in profile by name.
func Builtin(name string) (Profile, bool) {
	switch strings.ToLower(name) {
	case "avplayer":
		return AVPlayer(), true
	case "browser":
		return Browser(), true
	case "chromecast":
		return Chromecast(), true
	case "airplay":
		return AirPlay(), true
	}
	return Profile{}, false
}

// Browser is a generic modern browser. The web client normally POSTs a
// profile it measured itself (MediaSource.isTypeSupported); this is the
// default for other callers.
func Browser() Profile {
	return Profile{
		Name:        "browser",
		Containers:  []string{"mp4", "webm"},
		VideoCodecs: []string{"h264", "vp9", "av1"},
		AudioCodecs: []string{"aac", "mp3", "opus", "flac"},
		HLS:         true,
		TextSubs:    []string{"vtt"},
	}
}

// Chromecast is a Google Cast receiver. VP9 is left out on purpose: only some
// models decode it, and a transcode is always safe. It plays a 1080p file, but HLS
// only up to 720p: the Default Media Receiver answers LOAD_FAILED for 1080p HLS,
// whatever the audio, frame rate or muxing (measured 2026-11-12, docs/M11-CLIENTS.md).
func Chromecast() Profile {
	return Profile{
		Name:         "chromecast",
		Containers:   []string{"mp4", "webm"},
		VideoCodecs:  []string{"h264"},
		AudioCodecs:  []string{"aac", "mp3", "opus", "flac"},
		MaxHeight:    1080,
		MaxHLSHeight: 720,
		HLS:          true,
		TextSubs:     []string{"vtt"},
	}
}

// AirPlay uses MPEG-TS HLS compatible with Apple TV 3. Safari cannot report the
// receiver model, and source metadata cannot establish its level/frame-rate limits,
// so re-encode to bounded H.264 and stereo AAC even for an otherwise compatible MP4.
func AirPlay() Profile {
	return Profile{
		Name: "airplay", VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
		MaxHeight: 1080, ForceTranscode: true, HLS: true, TextSubs: []string{"vtt"},
	}
}

// AVPlayer is the profile for Apple's AVPlayer (iOS/macOS/tvOS, AirPlay).
func AVPlayer() Profile {
	return Profile{
		Name:        "avplayer",
		Containers:  []string{"mp4", "mov", "m4v"},
		VideoCodecs: []string{"h264", "hevc"},
		AudioCodecs: []string{"aac", "ac3", "eac3", "alac", "mp3"},
		RequireHVC1: true,
		HLS:         true,
		TextSubs:    []string{"vtt"},
	}
}

// VideoStream returns the first video stream.
func VideoStream(streams []Stream) (Stream, bool) {
	for _, s := range streams {
		if s.Type == "video" {
			return s, true
		}
	}
	return Stream{}, false
}

// AudioStream returns the audio stream with the given ffprobe index; if index
// is not an audio stream (including negative), the default-flagged audio
// stream, else the first one.
func AudioStream(streams []Stream, index int) (Stream, bool) {
	var first, def *Stream
	for i := range streams {
		s := &streams[i]
		if s.Type != "audio" {
			continue
		}
		if index >= 0 && s.Index == index {
			return *s, true
		}
		if first == nil {
			first = s
		}
		if def == nil && s.Default {
			def = s
		}
	}
	switch {
	case def != nil:
		return *def, true
	case first != nil:
		return *first, true
	}
	return Stream{}, false
}

// IsImageSubtitle reports whether a subtitle codec is bitmap based (cannot be
// converted to text; must be burned in).
func IsImageSubtitle(codec string) bool {
	switch strings.ToLower(codec) {
	case "hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle", "xsub":
		return true
	}
	return false
}

func imageSubtitle(streams []Stream, index int) (Stream, bool) {
	if index < 0 {
		return Stream{}, false
	}
	for _, s := range streams {
		if s.Type == "subtitle" && s.Index == index && IsImageSubtitle(s.Codec) {
			return s, true
		}
	}
	return Stream{}, false
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// Decide picks the cheapest way to deliver item to a client with profile p.
// Every reason a cheaper method was not possible is listed in Reasons.
func Decide(item Item, streams []Stream, p Profile, opts Options) Decision {
	d := Decision{
		VideoAction:  ActionCopy,
		AudioAction:  ActionCopy,
		AudioIndex:   -1,
		BurnSubtitle: -1,
		Reasons:      []string{},
	}
	reason := func(format string, args ...any) {
		d.Reasons = append(d.Reasons, fmt.Sprintf(format, args...))
	}
	name := p.Name
	if name == "" {
		name = "profile"
	}

	// Video: can the stream be copied at all?
	video, hasVideo := VideoStream(streams)
	videoCopyable := hasVideo
	switch {
	case !hasVideo:
		reason("no video stream")
	case p.ForceTranscode:
		videoCopyable = false
		reason("%s requires video conversion for receiver compatibility", name)
	case !contains(p.VideoCodecs, video.Codec):
		videoCopyable = false
		reason("video codec %s not supported by %s", video.Codec, name)
	case p.MaxHeight > 0 && video.Height > p.MaxHeight:
		videoCopyable = false
		reason("video height %d exceeds %s maximum %d", video.Height, name, p.MaxHeight)
	case video.Codec == "h264" && video.BitDepth() > 8:
		// No browser, AVPlayer or Cast receiver decodes High 10 profile.
		videoCopyable = false
		reason("h264 %d-bit (High 10) is not decodable by %s", video.BitDepth(), name)
	}

	// Audio: the chosen stream must be playable as is.
	audioCopyable := true
	if audio, ok := AudioStream(streams, opts.AudioIndex); ok {
		d.AudioIndex = audio.Index
		if p.ForceTranscode {
			audioCopyable = false
			reason("%s requires audio conversion for receiver compatibility", name)
		} else if !contains(p.AudioCodecs, audio.Codec) {
			audioCopyable = false
			reason("audio codec %s not supported by %s", audio.Codec, name)
		}
	}

	// Subtitles: bitmap subtitles can only be burned into the video.
	burn := false
	if sub, ok := imageSubtitle(streams, opts.SubtitleIndex); ok {
		burn = true
		d.BurnSubtitle = sub.Index
		reason("image subtitle (%s, stream %d) must be burned into the video", sub.Codec, sub.Index)
	}

	containerOK := contains(p.Containers, item.Container)
	if !containerOK {
		reason("container %s not supported by %s", item.Container, name)
	}

	// hevc must carry the hvc1 tag for AVPlayer to open it directly. Remuxing
	// retags, so this only blocks direct play (and is moot when the container
	// already forces a remux).
	tagOK := true
	if containerOK && videoCopyable && p.RequireHVC1 && strings.EqualFold(video.Codec, "hevc") &&
		!strings.EqualFold(video.CodecTag, "hvc1") {
		tagOK = false
		reason("hevc is tagged %q, %s requires hvc1", video.CodecTag, name)
	}

	// A player of the file as it is hears the file's default track: choosing
	// another one takes a session that maps it.
	otherAudio := false
	if def, ok := AudioStream(streams, -1); ok && d.AudioIndex >= 0 && d.AudioIndex != def.Index &&
		videoCopyable && !burn && containerOK && tagOK && audioCopyable {
		otherAudio = true
		reason("audio stream %d is not the file's default track, which is all a direct play can offer", d.AudioIndex)
	}

	switch {
	case burn || !videoCopyable:
		d.Method = MethodTranscode
		d.VideoAction = ActionTranscode
		if !audioCopyable {
			d.AudioAction = ActionTranscode
		}
	case containerOK && tagOK && audioCopyable && !otherAudio:
		d.Method = MethodDirect
	case hasVideo && p.HLSHeight() > 0 && video.Height > p.HLSHeight():
		// A remux is HLS, and the video is too tall for the client's HLS.
		reason("video height %d exceeds %s maximum %d over HLS", video.Height, name, p.HLSHeight())
		d.Method = MethodTranscode
		d.VideoAction = ActionTranscode
		if !audioCopyable {
			d.AudioAction = ActionTranscode
		}
	default:
		d.Method = MethodRemux
		if !audioCopyable {
			d.AudioAction = ActionTranscode
		}
	}
	return d
}
