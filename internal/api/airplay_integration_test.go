//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ply/internal/stream"
)

func TestAirPlayEndToEnd(t *testing.T) {
	e, _, ids := newSubsE2E(t, nil, makeSubtitleLibrary(t))
	// Reuse the real scanned subtitle fixture with authenticated receiver media.
	original := e.ts.Config.Handler.(*Server)
	t.Cleanup(original.Subs.Close)
	opts := original.Options
	opts.Token = "receiver token&value"
	ts := httptest.NewServer(New(opts))
	t.Cleanup(ts.Close)
	e.ts.Close()
	e.ts, e.client = ts, ts.Client()
	auth := map[string]string{"Authorization": "Bearer " + opts.Token}
	id := ids["Sub Movie (2021).mkv"]
	if id == 0 {
		t.Fatal("subtitle fixture was not scanned")
	}
	for _, tc := range []struct {
		key, cue string
	}{
		{"2", "Hello <i>world</i>"},
		{"ext0", "English sidecar"},
		{"ext1", "café"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", id),
				fmt.Sprintf(`{"profile":"airplay","subtitleKey":%q,"startSec":3,"audioIndex":1}`, tc.key), auth)
			if resp.StatusCode != 200 {
				t.Fatalf("playback = %d: %s", resp.StatusCode, b)
			}
			var pb Playback
			if err := json.Unmarshal(b, &pb); err != nil {
				t.Fatal(err)
			}
			if pb.Method != "transcode" || pb.SessionID == "" || !strings.Contains(pb.URL, "master.m3u8?token=") {
				t.Fatalf("playback: %+v", pb)
			}
			base := "/api/sessions/" + pb.SessionID + "/"
			suffix := "?token=receiver+token%26value"
			resp, master := e.do("GET", pb.URL, "", nil)
			if resp.StatusCode != 200 {
				t.Fatalf("master = %d: %s", resp.StatusCode, master)
			}
			for _, want := range []string{"#EXT-X-VERSION:4", `CODECS="avc1.`, "mp4a.40.2", "RESOLUTION=320x180", "subtitles.m3u8" + suffix, "index.m3u8" + suffix} {
				if !strings.Contains(string(master), want) {
					t.Errorf("master lacks %q: %s", want, master)
				}
			}
			resp, playlist := e.do("GET", base+"index.m3u8"+suffix, "", nil)
			if resp.StatusCode != 200 || !strings.Contains(string(playlist), "#EXT-X-START:TIME-OFFSET=3.000") {
				t.Fatalf("media playlist: %d %s", resp.StatusCode, playlist)
			}
			segs := extinf.FindAllStringSubmatch(string(playlist), -1)
			if len(segs) == 0 || !strings.Contains(string(playlist), "#EXT-X-VERSION:3") || !strings.Contains(segs[0][2], ".ts") {
				t.Fatalf("legacy media playlist: %s", playlist)
			}
			resp, segment := e.do("GET", base+segs[0][2], "", nil)
			if resp.StatusCode != 200 {
				t.Fatalf("segment = %d: %s", resp.StatusCode, segment)
			}
			if resp.Header.Get("Content-Type") != "video/mp2t" || len(segment) < 188 || segment[0] != 0x47 {
				t.Fatalf("TS segment: %v len=%d", resp.Header, len(segment))
			}
			start, _ := videoSpan(t, nil, segment)
			if math.Abs(start-stream.TimelineOffset) > 0.1 {
				t.Errorf("media timeline starts %.3f; want %.3f", start, stream.TimelineOffset)
			}
			resp, subtitles := e.do("GET", base+"subtitles.m3u8"+suffix, "", nil)
			if resp.StatusCode != 200 || !strings.Contains(string(subtitles), "subtitles.vtt"+suffix) {
				t.Fatalf("subtitle playlist: %d %s", resp.StatusCode, subtitles)
			}
			resp, vtt := e.do("GET", base+"subtitles.vtt"+suffix, "", nil)
			if resp.StatusCode != 200 || !strings.Contains(string(vtt), tc.cue) || !strings.Contains(string(vtt), "MPEGTS:180000") {
				t.Fatalf("subtitle resource: %d %s", resp.StatusCode, vtt)
			}
			// Decode actual media through the HTTP master, not a local fixture file.
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-i", ts.URL+pb.URL,
				"-map", "0:v:0", "-frames:v", "1", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Fatalf("decode HTTP master: %v\n%s", err, out)
			}
			resp, b = e.do("DELETE", strings.TrimSuffix(base, "/"), "", auth)
			if resp.StatusCode != 204 {
				t.Fatalf("stop: %d %s", resp.StatusCode, b)
			}
			for _, path := range []string{pb.URL, base + "subtitles.vtt" + suffix} {
				resp, b = e.do("GET", path, "", nil)
				if resp.StatusCode != 410 {
					t.Fatalf("stopped resource %s: %d %s", path, resp.StatusCode, b)
				}
			}
		})
	}
}
