package playback

import (
	"encoding/json"
	"strings"
	"testing"
)

func vid(codec, tag string, height int) Stream {
	return Stream{Index: 0, Type: "video", Codec: codec, CodecTag: tag, Width: height * 16 / 9, Height: height, Default: true}
}

func aud(index int, codec string, def bool) Stream {
	return Stream{Index: index, Type: "audio", Codec: codec, Channels: 2, Default: def}
}

func sub(index int, codec string) Stream {
	return Stream{Index: index, Type: "subtitle", Codec: codec}
}

func streams(ss ...Stream) []Stream { return ss }

func TestDecideAVPlayer(t *testing.T) {
	av := AVPlayer()
	tests := []struct {
		name      string
		container string
		streams   []Stream
		opts      Options

		method, video, audio string
		audioIndex, burn     int
		reasons              []string // substrings, in order; exact count
	}{
		{
			name: "mp4 h264 aac", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "mov h264 ac3", container: "mov",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "ac3", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "m4v hevc hvc1 eac3", container: "m4v",
			streams: streams(vid("hevc", "hvc1", 2160), aud(1, "eac3", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "mp4 h264 alac and mp3 are fine", container: "mp4",
			streams: streams(vid("h264", "avc1", 720), aud(1, "alac", true), aud(2, "mp3", false)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "mp4 hevc hev1 needs retag", container: "mp4",
			streams: streams(vid("hevc", "hev1", 1080), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"hev1"},
		},
		{
			name: "mp4 hevc untagged needs retag", container: "mp4",
			streams: streams(vid("hevc", "", 1080), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"hvc1"},
		},
		{
			name: "mkv h264 aac", container: "mkv",
			streams: streams(vid("h264", "", 1080), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"container mkv not supported by avplayer"},
		},
		{
			// The integration fixture: only the container is wrong; the hevc
			// tag is not mentioned because a remux retags anyway.
			name: "mkv hevc eac3", container: "mkv",
			streams: streams(vid("hevc", "", 1080), aud(1, "eac3", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"container mkv not supported by avplayer"},
		},
		{
			name: "mkv h264 dts is an audio-only transcode", container: "mkv",
			streams: streams(vid("h264", "", 1080), aud(1, "dts", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "transcode", audioIndex: 1, burn: -1,
			reasons: []string{"audio codec dts", "container mkv"},
		},
		{
			name: "mp4 h264 truehd is an audio-only transcode", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "truehd", true)),
			opts:    NoOptions,
			method:  "remux", video: "copy", audio: "transcode", audioIndex: 1, burn: -1,
			reasons: []string{"audio codec truehd"},
		},
		{
			name: "mp4 vp9 needs full transcode", container: "mp4",
			streams: streams(vid("vp9", "", 1080), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "transcode", video: "transcode", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"video codec vp9"},
		},
		{
			name: "mkv av1 flac transcodes both", container: "mkv",
			streams: streams(vid("av1", "", 2160), aud(1, "flac", true)),
			opts:    NoOptions,
			method:  "transcode", video: "transcode", audio: "transcode", audioIndex: 1, burn: -1,
			reasons: []string{"video codec av1", "audio codec flac", "container mkv"},
		},
		{
			name: "avi mpeg4 mp3", container: "avi",
			streams: streams(vid("mpeg4", "xvid", 480), aud(1, "mp3", true)),
			opts:    NoOptions,
			method:  "transcode", video: "transcode", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"video codec mpeg4", "container avi"},
		},
		{
			name: "mp4 video-only has no audio index", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: -1, burn: -1,
		},
		{
			name: "no video stream transcodes", container: "mp4",
			streams: streams(aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "transcode", video: "transcode", audio: "copy", audioIndex: 1, burn: -1,
			reasons: []string{"no video stream"},
		},
		{
			name: "unlimited height by default", container: "mp4",
			streams: streams(vid("h264", "avc1", 4320), aud(1, "aac", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "container and codec compare case-insensitively", container: "MP4",
			streams: streams(vid("H264", "avc1", 1080), aud(1, "AAC", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},

		// Audio selection.
		{
			name: "default-flagged audio wins over first", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "dts", false), aud(2, "aac", true)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 2, burn: -1,
		},
		{
			name: "first audio when none is flagged default", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(3, "aac", false), aud(4, "dts", false)),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 3, burn: -1,
		},
		{
			name: "explicit audio index is honoured", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), aud(2, "dts", false)),
			opts:    Options{AudioIndex: 2, SubtitleIndex: -1},
			method:  "remux", video: "copy", audio: "transcode", audioIndex: 2, burn: -1,
			reasons: []string{"audio codec dts"},
		},
		{
			name: "unknown audio index falls back to default", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), aud(2, "dts", false)),
			opts:    Options{AudioIndex: 9, SubtitleIndex: -1},
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "audio index pointing at a video stream falls back", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true)),
			opts:    Options{AudioIndex: 0, SubtitleIndex: -1},
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},

		// Subtitles.
		{
			name: "text subtitle selected does not force transcode", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), sub(2, "subrip")),
			opts:    Options{AudioIndex: -1, SubtitleIndex: 2},
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "pgs subtitle selected forces burn-in even on compatible file", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), sub(2, "hdmv_pgs_subtitle")),
			opts:    Options{AudioIndex: -1, SubtitleIndex: 2},
			method:  "transcode", video: "transcode", audio: "copy", audioIndex: 1, burn: 2,
			reasons: []string{"image subtitle (hdmv_pgs_subtitle, stream 2) must be burned"},
		},
		{
			name: "dvd subtitle on mkv", container: "mkv",
			streams: streams(vid("h264", "", 1080), aud(1, "aac", true), sub(3, "dvd_subtitle")),
			opts:    Options{AudioIndex: -1, SubtitleIndex: 3},
			method:  "transcode", video: "transcode", audio: "copy", audioIndex: 1, burn: 3,
			reasons: []string{"image subtitle", "container mkv"},
		},
		{
			name: "dvb subtitle burn-in with audio transcode", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "dts", true), sub(2, "dvb_subtitle")),
			opts:    Options{AudioIndex: -1, SubtitleIndex: 2},
			method:  "transcode", video: "transcode", audio: "transcode", audioIndex: 1, burn: 2,
			reasons: []string{"audio codec dts", "image subtitle"},
		},
		{
			name: "image subtitle present but not selected", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), sub(2, "hdmv_pgs_subtitle")),
			opts:    NoOptions,
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
		{
			name: "subtitle index that is not a subtitle is ignored", container: "mp4",
			streams: streams(vid("h264", "avc1", 1080), aud(1, "aac", true), sub(2, "hdmv_pgs_subtitle")),
			opts:    Options{AudioIndex: -1, SubtitleIndex: 1},
			method:  "direct", video: "copy", audio: "copy", audioIndex: 1, burn: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(Item{Container: tt.container}, tt.streams, av, tt.opts)
			if got.Method != tt.method || got.VideoAction != tt.video || got.AudioAction != tt.audio ||
				got.AudioIndex != tt.audioIndex || got.BurnSubtitle != tt.burn {
				t.Errorf("got %+v\nwant method=%s video=%s audio=%s audioIndex=%d burn=%d",
					got, tt.method, tt.video, tt.audio, tt.audioIndex, tt.burn)
			}
			checkReasons(t, got.Reasons, tt.reasons)
		})
	}
}

func checkReasons(t *testing.T, got, want []string) {
	t.Helper()
	if got == nil {
		t.Error("Reasons is nil; want a non-nil slice so it serialises as []")
	}
	if len(got) != len(want) {
		t.Errorf("reasons = %q, want %d reasons containing %q", got, len(want), want)
		return
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("reason[%d] = %q, want it to contain %q", i, got[i], want[i])
		}
	}
}

func TestDecideCustomProfiles(t *testing.T) {
	chromecast := Profile{
		Name:        "chromecast",
		Containers:  []string{"mp4", "webm"},
		VideoCodecs: []string{"h264"},
		AudioCodecs: []string{"aac", "mp3", "opus", "flac"},
		MaxHeight:   1080,
		HLS:         true,
	}
	mkvPlayer := Profile{ // e.g. a client that plays mkv natively
		Name:        "mkvplayer",
		Containers:  []string{"mkv", "mp4"},
		VideoCodecs: []string{"hevc", "h264"},
		AudioCodecs: []string{"aac"},
	}

	tests := []struct {
		name      string
		profile   Profile
		container string
		streams   []Stream
		method    string
		video     string
		audio     string
		reasons   []string
	}{
		{"1080p within limit", chromecast, "mp4", streams(vid("h264", "avc1", 1080), aud(1, "aac", true)),
			"direct", "copy", "copy", nil},
		{"2160p exceeds max height", chromecast, "mp4", streams(vid("h264", "avc1", 2160), aud(1, "aac", true)),
			"transcode", "transcode", "copy", []string{"height 2160 exceeds chromecast maximum 1080"}},
		{"just over the limit", chromecast, "mp4", streams(vid("h264", "avc1", 1081), aud(1, "aac", true)),
			"transcode", "transcode", "copy", []string{"height 1081"}},
		{"flac is fine for chromecast", chromecast, "webm", streams(vid("h264", "", 720), aud(1, "flac", true)),
			"direct", "copy", "copy", nil},
		{"hevc is not supported by chromecast", chromecast, "mp4", streams(vid("hevc", "hvc1", 1080), aud(1, "aac", true)),
			"transcode", "transcode", "copy", []string{"video codec hevc"}},
		{"hvc1 not required when RequireHVC1 is false", mkvPlayer, "mp4", streams(vid("hevc", "hev1", 1080), aud(1, "aac", true)),
			"direct", "copy", "copy", nil},
		{"mkv played directly when the profile allows it", mkvPlayer, "mkv", streams(vid("hevc", "", 1080), aud(1, "aac", true)),
			"direct", "copy", "copy", nil},
		{"empty profile transcodes everything", Profile{}, "mp4", streams(vid("h264", "avc1", 1080), aud(1, "aac", true)),
			"transcode", "transcode", "transcode", []string{"video codec h264 not supported by profile", "audio codec aac", "container mp4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(Item{Container: tt.container}, tt.streams, tt.profile, NoOptions)
			if got.Method != tt.method || got.VideoAction != tt.video || got.AudioAction != tt.audio {
				t.Errorf("got %+v, want %s/%s/%s", got, tt.method, tt.video, tt.audio)
			}
			checkReasons(t, got.Reasons, tt.reasons)
		})
	}
}

func TestDecideDoesNotMutateInputs(t *testing.T) {
	ss := streams(vid("hevc", "hev1", 1080), aud(1, "aac", true), aud(2, "dts", false))
	before, _ := json.Marshal(ss)
	av := AVPlayer()
	pBefore, _ := json.Marshal(av)
	Decide(Item{Container: "mkv"}, ss, av, Options{AudioIndex: 2, SubtitleIndex: -1})
	after, _ := json.Marshal(ss)
	pAfter, _ := json.Marshal(av)
	if string(before) != string(after) || string(pBefore) != string(pAfter) {
		t.Error("Decide modified its inputs")
	}
}

func TestBuiltin(t *testing.T) {
	for _, name := range []string{"avplayer", "AVPlayer"} {
		p, ok := Builtin(name)
		if !ok || p.Name != "avplayer" {
			t.Errorf("Builtin(%q) = %+v, %v", name, p, ok)
		}
	}
	if _, ok := Builtin("nope"); ok {
		t.Error("unknown profile should not resolve")
	}
	// Each call returns an independent copy.
	a, _ := Builtin("avplayer")
	a.Containers[0] = "changed"
	if b, _ := Builtin("avplayer"); b.Containers[0] != "mp4" {
		t.Error("built-in profile shares state between calls")
	}
	av := AVPlayer()
	if !av.RequireHVC1 || !av.HLS || av.MaxHeight != 0 {
		t.Errorf("avplayer profile = %+v", av)
	}
}

func TestIsImageSubtitle(t *testing.T) {
	for codec, want := range map[string]bool{
		"hdmv_pgs_subtitle": true, "dvd_subtitle": true, "dvb_subtitle": true, "xsub": true,
		"HDMV_PGS_SUBTITLE": true,
		"subrip":            false, "ass": false, "webvtt": false, "mov_text": false, "": false,
	} {
		if got := IsImageSubtitle(codec); got != want {
			t.Errorf("IsImageSubtitle(%q) = %v, want %v", codec, got, want)
		}
	}
}

func TestProfileJSONAcceptsEitherHVC1Spelling(t *testing.T) {
	for _, body := range []string{`{"requireHvc1": true}`, `{"requireHVC1": true}`} {
		var p Profile
		if err := json.Unmarshal([]byte(body), &p); err != nil || !p.RequireHVC1 {
			t.Errorf("%s -> %+v, %v", body, p, err)
		}
	}
}
