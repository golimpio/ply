package playback

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuiltinProfiles(t *testing.T) {
	for _, name := range BuiltinNames {
		p, ok := Builtin(strings.ToUpper(name))
		if !ok || p.Name != name || !p.HLS || (len(p.Containers) == 0 && name != "airplay") || len(p.VideoCodecs) == 0 || len(p.AudioCodecs) == 0 {
			t.Errorf("Builtin(%q) = %+v, %v", name, p, ok)
		}
	}
	cc := Chromecast()
	if cc.MaxHeight != 1080 || contains(cc.VideoCodecs, "vp9") || !contains(cc.VideoCodecs, "h264") {
		t.Errorf("chromecast = %+v", cc)
	}
	br := Browser()
	for _, c := range []string{"h264", "vp9", "av1"} {
		if !contains(br.VideoCodecs, c) {
			t.Errorf("browser lacks %s", c)
		}
	}
	if br.MaxHeight != 0 || br.RequireHVC1 {
		t.Errorf("browser = %+v", br)
	}
}

// TestBuiltinMatrix pins the decision for typical files on every built-in.
func TestBuiltinMatrix(t *testing.T) {
	type file struct {
		container string
		video     Stream
		audio     Stream
	}
	files := map[string]file{
		"mp4 h264 aac":      {"mp4", vid("h264", "avc1", 1080), aud(1, "aac", true)},
		"mp4 h264 ac3":      {"mp4", vid("h264", "avc1", 1080), aud(1, "ac3", true)},
		"mp4 h264 4k":       {"mp4", vid("h264", "avc1", 2160), aud(1, "aac", true)},
		"mp4 hevc hvc1 aac": {"mp4", vid("hevc", "hvc1", 1080), aud(1, "aac", true)},
		"mkv h264 aac":      {"mkv", vid("h264", "avc1", 1080), aud(1, "aac", true)},
		"mkv h264 eac3":     {"mkv", vid("h264", "avc1", 1080), aud(1, "eac3", true)},
		"mkv hevc eac3":     {"mkv", vid("hevc", "hevc", 2160), aud(1, "eac3", true)},
		"webm vp9 opus":     {"webm", vid("vp9", "", 1080), aud(1, "opus", true)},
		"webm av1 opus":     {"webm", vid("av1", "", 1080), aud(1, "opus", true)},
		"avi mpeg4 mp3":     {"avi", vid("mpeg4", "", 480), aud(1, "mp3", true)},
		"mp4 h264 10-bit":   {"mp4", func() Stream { v := vid("h264", "avc1", 1080); v.PixFmt = "yuv420p10le"; return v }(), aud(1, "aac", true)},
	}
	want := map[string]map[string]string{ // file -> profile -> method[/audio action]
		"mp4 h264 aac":      {"avplayer": "direct", "browser": "direct", "chromecast": "direct", "airplay": "remux"},
		"mp4 h264 ac3":      {"avplayer": "direct", "browser": "remux/transcode", "chromecast": "remux/transcode", "airplay": "remux/transcode"},
		"mp4 h264 4k":       {"avplayer": "direct", "browser": "direct", "chromecast": "transcode", "airplay": "transcode"},
		"mp4 hevc hvc1 aac": {"avplayer": "direct", "browser": "transcode", "chromecast": "transcode", "airplay": "transcode"},
		"mkv h264 aac":      {"avplayer": "remux", "browser": "remux", "chromecast": "remux", "airplay": "remux"},
		"mkv h264 eac3":     {"avplayer": "remux", "browser": "remux/transcode", "chromecast": "remux/transcode", "airplay": "remux/transcode"},
		"mkv hevc eac3":     {"avplayer": "remux", "browser": "transcode", "chromecast": "transcode", "airplay": "transcode"},
		"webm vp9 opus":     {"avplayer": "transcode", "browser": "direct", "chromecast": "transcode", "airplay": "transcode"},
		"webm av1 opus":     {"avplayer": "transcode", "browser": "direct", "chromecast": "transcode", "airplay": "transcode"},
		"avi mpeg4 mp3":     {"avplayer": "transcode", "browser": "transcode", "chromecast": "transcode", "airplay": "transcode"},
		"mp4 h264 10-bit":   {"avplayer": "transcode", "browser": "transcode", "chromecast": "transcode", "airplay": "transcode"},
	}
	for name, f := range files {
		for _, pn := range BuiltinNames {
			p, _ := Builtin(pn)
			d := Decide(Item{Container: f.container}, streams(f.video, f.audio), p, NoOptions)
			got := d.Method
			if d.Method == MethodRemux && d.AudioAction == ActionTranscode {
				got += "/transcode"
			}
			if got != want[name][pn] {
				t.Errorf("%s on %s: %s, want %s (reasons %v)", name, pn, got, want[name][pn], d.Reasons)
			}
		}
	}
}

// TestDecisionOrder checks, on every built-in profile and a grid of files,
// that Decide returns the cheapest method that is possible, judged by an
// independent oracle: direct > remux (copy all) > audio-only transcode >
// full transcode.
func TestDecisionOrder(t *testing.T) {
	containers := []string{"mp4", "mkv", "webm", "mov", "avi", "ts"}
	videos := []Stream{
		vid("h264", "avc1", 720), vid("h264", "avc1", 2160), vid("hevc", "hvc1", 1080), vid("hevc", "hev1", 1080),
		vid("vp9", "", 1080), vid("av1", "", 2160), vid("mpeg4", "", 480),
	}
	audios := []string{"aac", "ac3", "eac3", "opus", "flac", "mp3", "dts", "truehd"}

	oracle := func(p Profile, c string, v Stream, a string) (method string, audioCopy bool) {
		videoOK := contains(p.VideoCodecs, v.Codec) && (p.MaxHeight == 0 || v.Height <= p.MaxHeight)
		audioOK := contains(p.AudioCodecs, a)
		tagOK := !(p.RequireHVC1 && v.Codec == "hevc" && v.CodecTag != "hvc1")
		switch {
		case !videoOK:
			return MethodTranscode, audioOK
		case contains(p.Containers, c) && audioOK && tagOK:
			return MethodDirect, true
		}
		return MethodRemux, audioOK
	}

	for _, pn := range BuiltinNames {
		p, _ := Builtin(pn)
		for _, c := range containers {
			for _, v := range videos {
				for _, a := range audios {
					name := fmt.Sprintf("%s/%s/%s%d/%s", pn, c, v.Codec, v.Height, a)
					d := Decide(Item{Container: c}, streams(v, aud(1, a, true)), p, NoOptions)
					wantM, wantCopy := oracle(p, c, v, a)
					if d.Method != wantM {
						t.Errorf("%s: method %s, want %s (%v)", name, d.Method, wantM, d.Reasons)
						continue
					}
					if gotCopy := d.AudioAction == ActionCopy; gotCopy != wantCopy && d.Method != MethodDirect {
						t.Errorf("%s: audio copy = %v, want %v", name, gotCopy, wantCopy)
					}
					if d.Method == MethodDirect && (d.VideoAction != ActionCopy || d.AudioAction != ActionCopy) {
						t.Errorf("%s: direct play must copy everything: %+v", name, d)
					}
					if d.Method != MethodTranscode && d.VideoAction != ActionCopy {
						t.Errorf("%s: %s must copy the video", name, d.Method)
					}
					if d.Method != MethodDirect && len(d.Reasons) == 0 {
						t.Errorf("%s: %s without a reason", name, d.Method)
					}
				}
			}
		}
	}
}

// An image subtitle forces a transcode on every profile, even for a file
// that would otherwise play directly.
func TestBurnInForcesTranscodeOnEveryProfile(t *testing.T) {
	for _, pn := range BuiltinNames {
		p, _ := Builtin(pn)
		st := streams(vid("h264", "avc1", 720), aud(1, "aac", true), sub(2, "hdmv_pgs_subtitle"), sub(3, "subrip"))
		control := MethodDirect
		if pn == "airplay" {
			control = MethodRemux
		}
		if d := Decide(Item{Container: "mp4"}, st, p, NoOptions); d.Method != control {
			t.Fatalf("%s: control: %+v", pn, d) // positive control
		}
		d := Decide(Item{Container: "mp4"}, st, p, Options{AudioIndex: -1, SubtitleIndex: 2})
		if d.Method != MethodTranscode || d.BurnSubtitle != 2 || d.VideoAction != ActionTranscode || d.AudioAction != ActionCopy {
			t.Errorf("%s: %+v", pn, d)
		}
		// A text subtitle is delivered as a sidecar, not burned.
		if d := Decide(Item{Container: "mp4"}, st, p, Options{AudioIndex: -1, SubtitleIndex: 3}); d.Method != control || d.BurnSubtitle != -1 {
			t.Errorf("%s: text subtitle: %+v", pn, d)
		}
	}
}
