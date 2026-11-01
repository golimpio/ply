package stream

import (
	"slices"
	"strings"
	"testing"
)

func baseSpec() RunSpec {
	return RunSpec{
		Input: "/media/Movie (2020).mkv", VideoIndex: 0, AudioIndex: 1,
		Video:    VideoSpec{Codec: "hevc"},
		Audio:    AudioSpec{Codec: "eac3", Copy: true},
		SeekSec:  180.14,
		StartGOP: 90, InitName: "init3.mp4", Offset: TimelineOffset,
	}
}

// has reports whether args contains the consecutive words.
func has(args []string, words ...string) bool {
	for i := 0; i+len(words) <= len(args); i++ {
		if slices.Equal(args[i:i+len(words)], words) {
			return true
		}
	}
	return false
}

func TestBuildArgsHEVCCopy(t *testing.T) {
	a := BuildArgs(baseSpec())
	for _, want := range [][]string{
		{"-ss", "180.140", "-i", "/media/Movie (2020).mkv"},
		{"-map", "0:0"}, {"-map", "0:1"},
		{"-c:v", "copy"}, {"-tag:v", "hvc1"}, {"-c:a", "copy"},
		{"-copyts"}, {"-start_at_zero"}, {"-avoid_negative_ts", "disabled"}, {"-output_ts_offset", "2.000"},
		{"-f", "hls"}, {"-hls_segment_type", "fmp4"}, {"-hls_fmp4_init_filename", "init3.mp4"},
		{"-start_number", "90"}, {"-hls_segment_filename", "gop%05d.m4s"}, {"-hls_flags", "temp_file"},
		{"-hls_segment_options", "movflags=frag_discont+skip_sidx+"},
	} {
		if !has(a, want...) {
			t.Errorf("args lack %v:\n%s", want, strings.Join(a, " "))
		}
	}
	if a[len(a)-1] != "ffmpeg.m3u8" {
		t.Errorf("last arg = %q, want the playlist name", a[len(a)-1])
	}
	if slices.Contains(a, "-bsf:a") || slices.Contains(a, "-an") {
		t.Errorf("unexpected audio filter in %v", a)
	}
	// The seek is an input option: it must precede -i, and the mapping follow it.
	if ss, i, m := slices.Index(a, "-ss"), slices.Index(a, "-i"), slices.Index(a, "-map"); !(ss < i && i < m) {
		t.Errorf("option order wrong: -ss@%d -i@%d -map@%d", ss, i, m)
	}
}

func TestBuildArgsVariants(t *testing.T) {
	t.Run("no seek from the start", func(t *testing.T) {
		s := baseSpec()
		s.SeekSec, s.StartGOP = 0, 0
		a := BuildArgs(s)
		if slices.Contains(a, "-ss") || !has(a, "-start_number", "0") {
			t.Errorf("args = %v", a)
		}
	})
	t.Run("h264 copy is not retagged", func(t *testing.T) {
		s := baseSpec()
		s.Video.Codec = "h264"
		if a := BuildArgs(s); slices.Contains(a, "-tag:v") {
			t.Errorf("h264 got a tag: %v", a)
		}
	})
	t.Run("audio transcode", func(t *testing.T) {
		s := baseSpec()
		s.Audio = AudioSpec{Codec: "dts", Copy: false}
		a := BuildArgs(s)
		if !has(a, "-c:a", "aac", "-b:a", "192k", "-ac", "2") || has(a, "-c:a", "copy") {
			t.Errorf("args = %v", a)
		}
	})
	t.Run("aac copy gets the ADTS fix", func(t *testing.T) {
		s := baseSpec()
		s.Audio = AudioSpec{Codec: "aac", Copy: true}
		if a := BuildArgs(s); !has(a, "-bsf:a", "aac_adtstoasc") {
			t.Errorf("args = %v", a)
		}
	})
	t.Run("no audio", func(t *testing.T) {
		s := baseSpec()
		s.AudioIndex = -1
		a := BuildArgs(s)
		if !slices.Contains(a, "-an") || has(a, "-map", "0:1") || has(a, "-c:a", "copy") {
			t.Errorf("args = %v", a)
		}
	})
	t.Run("video encoder plugs in (M4)", func(t *testing.T) {
		s := baseSpec()
		s.Video = VideoSpec{
			Codec:  "hevc",
			Encode: []string{"-c:v", "h264_videotoolbox", "-force_key_frames", "expr:gte(t,n_forced*6)"},
			Pre:    []string{"-hwaccel", "videotoolbox"},
		}
		a := BuildArgs(s)
		if has(a, "-c:v", "copy") || slices.Contains(a, "-tag:v") {
			t.Errorf("copy args survived a transcode: %v", a)
		}
		if !has(a, "-c:v", "h264_videotoolbox") || !has(a, "-force_key_frames", "expr:gte(t,n_forced*6)") {
			t.Errorf("encoder args missing: %v", a)
		}
		if hw, i := slices.Index(a, "-hwaccel"), slices.Index(a, "-i"); hw < 0 || hw > i {
			t.Errorf("-hwaccel must come before -i: %v", a)
		}
	})
	t.Run("input path is one argument", func(t *testing.T) {
		s := baseSpec()
		s.Input = "/media/-weird name; rm -rf.mkv"
		if a := BuildArgs(s); !has(a, "-i", s.Input) {
			t.Errorf("args = %v", a)
		}
	})
}

func TestSeekTarget(t *testing.T) {
	kf := []float64{0, 2, 4, 4.1, 8, 10}
	tests := []struct {
		name      string
		container string
		g         int
		want      float64
	}{
		{"first GOP starts at the beginning", "mkv", 0, 0},
		{"mkv aims past the keyframe", "mkv", 1, 2.14},
		{"mp4 too", "mp4", 4, 8.14},
		{"keyframe followed closely by another", "mkv", 2, 4.01},
		{"ts aims before the keyframe", "ts", 1, 1.95},
		{"ts never goes negative", "ts", 1, 1.95},
		{"out of range", "mkv", 99, 0},
	}
	for _, tt := range tests {
		got := SeekTarget(tt.container, kf, tt.g)
		if d := got - tt.want; d > 1e-9 || d < -1e-9 {
			t.Errorf("%s: SeekTarget(%s, %d) = %v, want %v", tt.name, tt.container, tt.g, got, tt.want)
		}
	}
	if got := SeekTarget("ts", []float64{0, 0.02}, 1); got <= 0 {
		t.Errorf("ts target = %v, want > 0 so that -ss is still passed", got)
	}
}

func TestThrottleDecision(t *testing.T) {
	tests := []struct {
		ahead  int
		paused bool
		want   throttleAction
	}{
		{0, false, actionNone}, {20, false, actionNone}, {21, false, actionPause}, {500, false, actionPause},
		{21, true, actionNone}, {10, true, actionNone}, {15, true, actionNone}, {9, true, actionResume}, {-3, true, actionResume},
		{5, false, actionNone}, // below the low mark but running: nothing to do
	}
	for _, tt := range tests {
		if got := throttle(tt.ahead, tt.paused, 20, 10); got != tt.want {
			t.Errorf("throttle(ahead=%d, paused=%v) = %v, want %v", tt.ahead, tt.paused, got, tt.want)
		}
	}
}

func TestResolveFFmpeg(t *testing.T) {
	dir := t.TempDir()
	exe := dir + "/jellyfin-ffmpeg"
	if err := writeExecutable(exe, "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	if got := resolveFFmpeg("ffmpeg", []string{dir + "/missing", exe}); got != exe {
		t.Errorf("default with jellyfin installed = %q, want %q", got, exe)
	}
	if got := resolveFFmpeg("/opt/mine/ffmpeg", []string{exe}); got != "/opt/mine/ffmpeg" {
		t.Errorf("explicit choice overridden: %q", got)
	}
	if got := resolveFFmpeg("", []string{dir + "/missing"}); got != "ffmpeg" {
		t.Errorf("fallback = %q", got)
	}
	if got := resolveFFmpeg("ffmpeg", []string{dir}); got != "ffmpeg" {
		t.Errorf("a directory is not a binary: %q", got)
	}
}
