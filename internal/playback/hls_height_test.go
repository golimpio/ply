package playback

import (
	"strings"
	"testing"
)

// A Chromecast plays a 1080p file, but HLS only to 720p (docs/M11-CLIENTS.md): what it
// cannot play as a file it gets as HLS, transcoded down when taller than 720p.
func TestAChromecastGetsHLSAt720pAtMost(t *testing.T) {
	cc := Chromecast()
	for _, c := range []struct {
		name      string
		container string
		ss        []Stream
		method    string
		why       string // a reason that must be given; "" none in particular
	}{
		{"a 1080p mp4 it plays as a file (control)", "mp4", streams(vid("h264", "avc1", 1080), aud(1, "aac", true)), MethodDirect, ""},
		{"a 1080p mkv: HLS, too tall to copy", "mkv", streams(vid("h264", "avc1", 1080), aud(1, "eac3", true)), MethodTranscode, "exceeds chromecast maximum 720 over HLS"},
		{"a 1080p mp4 with E-AC-3: HLS, too tall to copy", "mp4", streams(vid("h264", "avc1", 1080), aud(1, "eac3", true)), MethodTranscode, "over HLS"},
		{"a 720p mkv: HLS, copied (control)", "mkv", streams(vid("h264", "avc1", 720), aud(1, "eac3", true)), MethodRemux, ""},
	} {
		d := Decide(Item{Container: c.container}, c.ss, cc, NoOptions)
		if d.Method != c.method {
			t.Errorf("%s: %s (%v), want %s", c.name, d.Method, d.Reasons, c.method)
		}
		if c.why != "" && !strings.Contains(strings.Join(d.Reasons, "; "), c.why) {
			t.Errorf("%s: reasons %v, want one saying %q", c.name, d.Reasons, c.why)
		}
		if c.method == MethodTranscode && d.VideoAction != ActionTranscode {
			t.Errorf("%s: video %s, want transcode", c.name, d.VideoAction)
		}
	}
	// A profile with no HLS limit of its own remuxes the same 1080p mkv.
	if d := Decide(Item{Container: "mkv"}, streams(vid("h264", "avc1", 1080), aud(1, "aac", true)), AVPlayer(), NoOptions); d.Method != MethodRemux {
		t.Errorf("avplayer, 1080p mkv: %s (%v), want remux", d.Method, d.Reasons)
	}
}

func TestHLSHeight(t *testing.T) {
	for _, c := range []struct {
		p    Profile
		want int
	}{
		{Profile{MaxHeight: 1080, MaxHLSHeight: 720}, 720},
		{Profile{MaxHeight: 1080}, 1080},
		{Profile{MaxHLSHeight: 720}, 720},
		{Profile{MaxHeight: 480, MaxHLSHeight: 720}, 480}, // never more than the file limit
		{Profile{}, 0},
	} {
		if got := c.p.HLSHeight(); got != c.want {
			t.Errorf("%+v: HLSHeight = %d, want %d", c.p, got, c.want)
		}
	}
	if Chromecast().HLSHeight() != 720 {
		t.Errorf("the chromecast profile's HLS height = %d, want 720", Chromecast().HLSHeight())
	}
}
