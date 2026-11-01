package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/stream"
)

// kfCache serves a fixed keyframe index (a keyframe every 2 s for 2400 s), so
// no ffprobe is involved.
type kfCache struct {
	mu   sync.Mutex
	hits int
}

func (c *kfCache) Keyframes(_ context.Context, id int64) ([]float64, bool, error) {
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
	kf := make([]float64, 1200)
	for i := range kf {
		kf[i] = float64(i) * 2
	}
	return kf, true, nil
}

func (c *kfCache) SetKeyframes(context.Context, int64, []float64) error { return nil }

// fakeFFmpeg writes a script standing in for ffmpeg.
func fakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// withStreams gives the env a session manager whose ffmpeg is the fake.
func (e *env) withStreams(ffmpeg string, mod func(*stream.Options)) *stream.Manager {
	e.t.Helper()
	o := stream.Options{
		FFmpeg: ffmpeg, TempRoot: e.t.TempDir(), Cache: &kfCache{},
		WaitTimeout: 300 * time.Millisecond, Tick: 20 * time.Millisecond,
	}
	if mod != nil {
		mod(&o)
	}
	m, err := stream.NewManager(o)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(m.Close)
	e.srv.Streams = m
	return m
}

func (e *env) startSession(token string, body string) Playback {
	e.t.Helper()
	hdr := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), hdr, body)
	wantStatus(e.t, rec, 200)
	return decode[Playback](e.t, rec)
}

func TestRemuxCreatesSession(t *testing.T) {
	for _, token := range []string{"", "s3cret"} {
		t.Run("token="+token, func(t *testing.T) {
			e := newEnv(t, token)
			m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)

			pb := e.startSession(token, `{"startSec": 120}`)
			if pb.Method != "remux" || pb.SessionID == "" || pb.StartSec != 120 {
				t.Fatalf("playback = %+v", pb)
			}
			want := "/api/sessions/" + pb.SessionID + "/index.m3u8"
			if token != "" {
				want += "?token=" + token
			}
			if pb.URL != want {
				t.Errorf("url = %q, want %q", pb.URL, want)
			}
			if m.Count() != 1 {
				t.Errorf("sessions = %d, want 1", m.Count())
			}
		})
	}
}

func TestSessionPlaylist(t *testing.T) {
	for _, token := range []string{"", "s3 cret&x"} { // the second needs escaping
		t.Run("token="+token, func(t *testing.T) {
			e := newEnv(t, token)
			e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
			pb := e.startSession(token, `{}`)

			path := "/api/sessions/" + pb.SessionID + "/index.m3u8"
			hdr := map[string]string{}
			if token != "" {
				hdr["Authorization"] = "Bearer " + token
			}
			rec := e.do("GET", path, hdr, "")
			wantStatus(t, rec, 200)
			if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
				t.Errorf("Content-Type = %q", ct)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("Cache-Control = %q", cc)
			}
			body := rec.Body.String()
			for _, want := range []string{"#EXTM3U", "#EXT-X-PLAYLIST-TYPE:VOD", "#EXT-X-ENDLIST", "#EXT-X-TARGETDURATION:"} {
				if !strings.Contains(body, want) {
					t.Errorf("playlist lacks %s", want)
				}
			}

			suffix := ""
			if token != "" {
				suffix = "?token=s3+cret%26x"
			}
			if !strings.Contains(body, `#EXT-X-MAP:URI="init.mp4`+suffix+`"`) {
				t.Errorf("init URI wrong (suffix %q) in:\n%.300s", suffix, body)
			}
			var total float64
			segs := 0
			extinf := regexp.MustCompile(`^#EXTINF:([0-9.]+),$`)
			lines := strings.Split(strings.TrimSpace(body), "\n")
			for i, l := range lines {
				if m := extinf.FindStringSubmatch(l); m != nil {
					d, _ := strconv.ParseFloat(m[1], 64)
					total += d
					if d < 6 {
						t.Errorf("segment %d is %.3f s, want >= 6", segs, d)
					}
					if want := fmt.Sprintf("seg%05d.m4s%s", segs, suffix); lines[i+1] != want {
						t.Errorf("URI %q, want %q", lines[i+1], want)
					}
					segs++
				}
			}
			if segs != 400 || total < 2399.9 || total > 2400.1 { // 2400 s in 6 s segments
				t.Errorf("segments = %d, total %.3f s; want 400 and 2400 s", segs, total)
			}
		})
	}
}

func TestSessionNotFoundAndNames(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("", `{}`)
	base := "/api/sessions/" + pb.SessionID + "/"

	// Positive control: the session exists and its playlist is served.
	wantStatus(t, e.get(base+"index.m3u8"), 200)

	t.Run("unknown session", func(t *testing.T) {
		for _, p := range []string{"index.m3u8", "init.mp4", "seg00000.m4s"} {
			wantJSONError(t, e.get("/api/sessions/nosuchsession/"+p), 404)
		}
		wantJSONError(t, e.do("DELETE", "/api/sessions/nosuchsession", nil, ""), 404)
	})
	t.Run("names outside the whitelist", func(t *testing.T) {
		for _, name := range []string{"ffmpeg.m3u8", "gop00000.m4s", "seg1.m4s", "seg000000.m4s", "seg00000.m4s.tmp",
			"init1.mp4", "..%2Fsecret", "seg00000.m4s%0A", "INIT.MP4"} {
			rec := e.get(base + name)
			if rec.Code != 404 {
				t.Errorf("GET %s = %d, want 404", name, rec.Code)
			}
		}
	})
	t.Run("segment out of range", func(t *testing.T) {
		wantJSONError(t, e.get(base+"seg00400.m4s"), 404)
		wantJSONError(t, e.get(base+"seg99999.m4s"), 404)
	})
	t.Run("segment not produced in time", func(t *testing.T) {
		wantJSONError(t, e.get(base+"seg00000.m4s"), 504)
	})
}

func TestSessionDelete(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("", `{}`)
	sess, ok := m.Get(pb.SessionID)
	if !ok {
		t.Fatal("session missing")
	}
	dir := sess.Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("setup: session dir: %v", err)
	}

	rec := e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, "")
	wantStatus(t, rec, 204)
	if m.Count() != 0 {
		t.Errorf("sessions = %d after DELETE", m.Count())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir still exists: %v", err)
	}
	wantJSONError(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8"), 404)
	wantJSONError(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), 404)
}

func TestSessionLimit(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 2 })
	e.startSession("", `{}`)
	e.startSession("", `{}`)
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), nil, "{}")
	wantJSONError(t, rec, 503)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
}

func TestSessionAuth(t *testing.T) {
	e := newEnv(t, "tok")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("tok", `{}`)
	base := "/api/sessions/" + pb.SessionID
	for _, p := range []string{"/index.m3u8", "/init.mp4", "/seg00000.m4s"} {
		wantJSONError(t, e.get(base+p), 401)
	}
	wantJSONError(t, e.do("DELETE", base, nil, ""), 401)
	wantStatus(t, e.get(base+"/index.m3u8?token=tok"), 200)
	wantStatus(t, e.do("DELETE", base+"?token=tok", nil, ""), 204)
}

func TestFFmpegFailureIsAServerError(t *testing.T) {
	e := newEnv(t, "")
	// The first exec of a fresh script can take a few hundred ms on macOS.
	e.withStreams(fakeFFmpeg(t, "echo 'boom: bad input' >&2; exit 1"), func(o *stream.Options) { o.WaitTimeout = 10 * time.Second })
	pb := e.startSession("", `{}`)
	rec := e.get("/api/sessions/" + pb.SessionID + "/seg00000.m4s")
	wantJSONError(t, rec, 500)
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("ffmpeg's stderr leaked to the client: %s", rec.Body.String())
	}
}

func TestRemuxWithoutStreamingAndTranscode(t *testing.T) {
	e := newEnv(t, "") // no stream manager
	pb := e.startSession("", `{}`)
	if pb.Method != "remux" || pb.URL != "" || pb.SessionID != "" {
		t.Errorf("playback = %+v", pb)
	}
	if last := pb.Reasons[len(pb.Reasons)-1]; !strings.Contains(last, "streaming is not enabled") {
		t.Errorf("reasons = %q", pb.Reasons)
	}
	wantJSONError(t, e.get("/api/sessions/x/index.m3u8"), 404)
	wantJSONError(t, e.do("DELETE", "/api/sessions/x", nil, ""), 404)

	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), nil, `{"custom": {"containers": ["webm"]}}`)
	tc := decode[Playback](t, rec)
	if tc.Method != "transcode" || tc.URL != "" || tc.SessionID != "" {
		t.Errorf("transcode playback = %+v", tc)
	}
	if last := tc.Reasons[len(tc.Reasons)-1]; !strings.Contains(last, "not available yet") {
		t.Errorf("reasons = %q", tc.Reasons)
	}
	if e.srv.Streams.Count() != 0 {
		t.Error("a transcode created a session")
	}
}

func TestWithToken(t *testing.T) {
	tests := []struct{ token, in, want string }{
		{"", "/api/x", "/api/x"},
		{"", "/api/x?v=1", "/api/x?v=1"},
		{"t", "/api/x", "/api/x?token=t"},
		{"t", "/api/x?v=123", "/api/x?v=123&token=t"},
		{"a b&c=d", "/api/x", "/api/x?token=a+b%26c%3Dd"},
		{"a b&c=d", "/api/x?v=1", "/api/x?v=1&token=a+b%26c%3Dd"},
	}
	for _, tt := range tests {
		s := &Server{Options: Options{Token: tt.token}}
		if got := s.withToken(tt.in); got != tt.want {
			t.Errorf("withToken(%q) with token %q = %q, want %q", tt.in, tt.token, got, tt.want)
		}
	}
}
