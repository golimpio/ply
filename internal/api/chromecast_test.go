package api

import (
	"fmt"
	"strings"
	"testing"
)

// A Chromecast plays HLS only to 720p (docs/M11-CLIENTS.md): a transcode for it is scaled to
// 720p, although it plays 1080p files.
func TestAChromecastTranscodeIsScaledTo720p(t *testing.T) {
	e := newEnv(t, "")
	bin, argsFile := recordingFFmpeg(t)
	e.withStreams(bin, nil)
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), nil, `{"profile": "chromecast"}`) // 4K hevc
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if pb.Method != "transcode" || !strings.Contains(strings.Join(pb.Reasons, "|"), "scaled from 2160p to 720p") {
		t.Fatalf("playback = %+v", pb)
	}
	args := argsOf(t, argsFile)
	scaled := false
	for i, a := range args {
		if a == "-vf" {
			scaled = strings.HasPrefix(args[i+1], "scale=-2:720,")
			if !scaled {
				t.Errorf("-vf %q, want a scale to 720p", args[i+1])
			}
		}
	}
	if !scaled {
		t.Errorf("ffmpeg was not asked to scale:\n%s", strings.Join(args, " "))
	}
}
