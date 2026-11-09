package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		rec := e.get(base + "seg00000.m4s")
		wantJSONError(t, rec, 504)
		if !strings.Contains(rec.Body.String(), "segment not ready") {
			t.Errorf("the 504 for a segment says %s", rec.Body.String())
		}
	})
}

// processAlive reports whether the process exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func TestSessionDelete(t *testing.T) {
	e := newEnv(t, "")
	pidFile := filepath.Join(t.TempDir(), "pid")
	m := e.withStreams(fakeFFmpeg(t, fmt.Sprintf("echo $$ > %q; exec sleep 60", pidFile)), nil)
	pb := e.startSession("", `{}`)
	sess, ok := m.Get(pb.SessionID)
	if !ok {
		t.Fatal("session missing")
	}
	dir := sess.Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("setup: session dir: %v", err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 || !processAlive(pid) { // positive control: there is a process to kill
		t.Fatalf("setup: ffmpeg did not start (pid %d)", pid)
	}

	rec := e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, "")
	wantStatus(t, rec, 204)
	if m.Count() != 0 {
		t.Errorf("sessions = %d after DELETE", m.Count())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir still exists: %v", err)
	}
	if processAlive(pid) {
		t.Errorf("ffmpeg (pid %d) still running after DELETE", pid)
	}
	wantJSONError(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8"), 410) // stopped on purpose: not "lost"
	wantJSONError(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), 404)
}

// An ffmpeg stuck in a read from a share that is slow or gone is killed at once but
// is not seen to exit until the read returns (18 s measured): the DELETE must not wait
// for that. It answers, the session counts until its ffmpeg has really gone, and the
// directory is removed then.
func TestSessionDeleteAnswersWhileFFmpegIsStillExiting(t *testing.T) {
	e := newEnv(t, "")
	pidFile := filepath.Join(t.TempDir(), "pid")
	m := e.withStreams(fakeFFmpeg(t, fmt.Sprintf("echo $$ > %q; exec sleep 60", pidFile)), nil)
	release := m.HoldExits()
	t.Cleanup(release) // before the manager's Close
	pb := e.startSession("", `{}`)
	sess, ok := m.Get(pb.SessionID)
	if !ok {
		t.Fatal("session missing")
	}
	dir := sess.Dir()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 || !processAlive(pid) { // positive control: there is a process to kill
		t.Fatalf("setup: ffmpeg did not start (pid %d)", pid)
	}

	answer := make(chan int, 1)
	start := time.Now()
	go func() { answer <- e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, "").Code }()
	select {
	case code := <-answer:
		if code != 204 {
			t.Errorf("DELETE = %d, want 204", code)
		}
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Errorf("DELETE took %v while ffmpeg's exit was not seen", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DELETE did not answer while ffmpeg's exit was not seen")
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("ffmpeg (pid %d) still running after DELETE", pid)
	}
	if m.Count() != 1 { // positive control: the exit really was held back
		t.Errorf("sessions = %d while the exit is not seen, want 1", m.Count())
	}
	if _, err := os.Stat(dir); err != nil { // positive control for its removal below
		t.Errorf("session dir removed before the exit was seen: %v", err)
	}
	wantJSONError(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8"), 410) // stopped, not lost

	release()
	deadline = time.Now().Add(5 * time.Second)
	for m.Count() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Count() != 0 {
		t.Fatal("the session was not let go of after ffmpeg's exit was seen")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir still exists: %v", err)
	}
}

// The player recovers from an expired session by starting a new one, and
// tells it apart from other failures by the 404 (web/src/lib/session.ts).
func TestExpiredSessionAnswers404(t *testing.T) {
	e := newEnv(t, "")
	m := e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.IdleTimeout = 200 * time.Millisecond })
	pb := e.startSession("", `{}`)
	base := "/api/sessions/" + pb.SessionID + "/"
	wantStatus(t, e.get(base+"index.m3u8"), 200) // positive control: alive until it has been idle

	deadline := time.Now().Add(5 * time.Second)
	for m.Count() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Count() != 0 {
		t.Fatal("the idle session did not expire")
	}
	for _, name := range []string{"index.m3u8", "init.mp4", "seg00003.m4s"} {
		wantJSONError(t, e.get(base+name), 404)
	}
}

func TestSessionLimit(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), func(o *stream.Options) { o.MaxSessions = 2 })
	e.startSession("", `{}`)
	e.startSession("", `{}`)
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), nil, "{}")
	wantStatus(t, rec, 503)
	if rec.Header().Get("Retry-After") != "5" {
		t.Errorf("503 with Retry-After %q, want 5", rec.Header().Get("Retry-After"))
	}
	// The error is still {"error": ...}, with what is using the limit beside it (plays_test.go).
	if lr := decode[LimitReached](t, rec); lr.Error != "2 of 2 plays in use" || len(lr.Playing) != 2 {
		t.Errorf("503 body = %s", rec.Body)
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

// An ffmpeg that fails is a bad gateway: the server is fine, what it ran is not.
func TestFFmpegFailureIsAServerError(t *testing.T) {
	e := newEnv(t, "")
	// The first exec of a fresh script can take a few hundred ms on macOS.
	e.withStreams(fakeFFmpeg(t, "echo 'boom: bad input' >&2; exit 1"), func(o *stream.Options) { o.WaitTimeout = 10 * time.Second })
	pb := e.startSession("", `{}`)
	rec := e.get("/api/sessions/" + pb.SessionID + "/seg00000.m4s")
	wantJSONError(t, rec, 502)
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("ffmpeg's stderr leaked to the client: %s", rec.Body.String())
	}
}

// What a session's playlist, init segment and segments fail with, by status:
// not there is 404, too slow 504, a stream the server could not make (ffmpeg
// failing, a keyframe index that was wrong and could not be put right) 502, and
// anything else 500. A client that has left gets nothing.
func TestStreamErrorStatuses(t *testing.T) {
	e := newEnv(t, "")
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"no such segment", stream.ErrNoSegment, 404},
		{"session closed", stream.ErrClosed, 404},
		{"closed, wrapped", fmt.Errorf("playlist: %w", stream.ErrClosed), 404},
		{"file gone", fs.ErrNotExist, 404},
		{"timeout", stream.ErrTimeout, 504},
		{"index wrong", fmt.Errorf("%w: scan failed", stream.ErrIndex), 502},
		{"ffmpeg failed", fmt.Errorf("%w: boom", stream.ErrFFmpeg), 502},
		{"anything else", errors.New("disk on fire"), 500},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.srv.streamError(rec, httptest.NewRequest("GET", "/api/sessions/x/index.m3u8", nil), c.err, "playlist")
			wantJSONError(t, rec, c.want)
			if c.want == 504 && !strings.Contains(rec.Body.String(), "playlist not ready") {
				t.Errorf("the 504 for a playlist says %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "disk") || strings.Contains(rec.Body.String(), "scan failed") {
				t.Errorf("the cause leaked to the client: %s", rec.Body.String())
			}
		})
	}

	t.Run("client gone", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		e.srv.streamError(rec, httptest.NewRequest("GET", "/x", nil).WithContext(ctx), errors.New("context canceled"), "segment")
		if rec.Body.Len() != 0 {
			t.Errorf("answered a client that had left: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The playlist of a session that is gone is a 404, not an empty playlist.
func TestPlaylistOfAGoneSession(t *testing.T) {
	e := newEnv(t, "")
	e.withStreams(fakeFFmpeg(t, "exec sleep 60"), nil)
	pb := e.startSession("", `{}`)
	wantStatus(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8"), 200)
	wantStatus(t, e.do("DELETE", "/api/sessions/"+pb.SessionID, nil, ""), 204)
	wantJSONError(t, e.get("/api/sessions/"+pb.SessionID+"/index.m3u8"), 410)
}

func TestPlaybackWithoutStreaming(t *testing.T) {
	e := newEnv(t, "") // no stream manager
	pb := e.startSession("", `{}`)
	if pb.Method != "remux" || pb.URL != "" || pb.SessionID != "" {
		t.Errorf("playback = %+v", pb)
	}
	if last := pb.Reasons[len(pb.Reasons)-1]; !strings.Contains(last, "streaming is not enabled") {
		t.Errorf("reasons = %q", pb.Reasons)
	}
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mp4), nil, `{"custom": {"containers": ["webm"]}}`)
	tc := decode[Playback](t, rec)
	if tc.Method != "transcode" || tc.URL != "" || tc.SessionID != "" {
		t.Errorf("transcode playback = %+v", tc)
	}
	wantJSONError(t, e.get("/api/sessions/x/index.m3u8"), 404)
	wantJSONError(t, e.do("DELETE", "/api/sessions/x", nil, ""), 404)
}

// recordingFFmpeg is a fake ffmpeg that writes its arguments, one per line,
// to the returned file and then idles.
func recordingFFmpeg(t *testing.T) (bin, argsFile string) {
	t.Helper()
	argsFile = filepath.Join(t.TempDir(), "argv")
	return fakeFFmpeg(t, fmt.Sprintf(`printf '%%s\n' "$@" > %q; exec sleep 60`, argsFile)), argsFile
}

func argsOf(t *testing.T, file string) []string {
	t.Helper()
	var b []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var err error
		if b, err = os.ReadFile(file); err == nil && len(b) > 0 {
			break
		}
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func hasArgs(args []string, want ...string) bool {
	for i := range args {
		if i+len(want) <= len(args) && strings.Join(args[i:i+len(want)], "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func TestTranscodeCreatesSession(t *testing.T) {
	e := newEnv(t, "")
	bin, argsFile := recordingFFmpeg(t)
	mgr := e.withStreams(bin, nil)

	// 4K HDR10 hevc to a 720p-limited client: video and audio are transcoded.
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), nil,
		`{"custom": {"name": "tv", "containers": ["mp4"], "videoCodecs": ["h264"], "audioCodecs": ["aac"], "maxHeight": 720}}`)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if pb.Method != "transcode" || pb.VideoAction != "transcode" || pb.AudioAction != "transcode" ||
		pb.SessionID == "" || !strings.HasSuffix(pb.URL, "/index.m3u8") {
		t.Fatalf("playback = %+v", pb)
	}
	joined := strings.Join(pb.Reasons, "|")
	for _, want := range []string{"video transcoded to h264 with libx264", "scaled from 2160p to 720p", "capped at 4 Mbps", "HDR"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reasons lack %q: %q", want, pb.Reasons)
		}
	}
	sess, ok := mgr.Get(pb.SessionID)
	if !ok {
		t.Fatal("no session")
	}
	// Equal 6 s segments, no keyframe index needed: 2400 s is 400 segments.
	if n := len(sess.Plan().Segments); n != 400 {
		t.Errorf("%d segments, want 400", n)
	}

	// The playlist is served at once; the first segment request starts ffmpeg.
	args := argsOf(t, argsFile)
	for _, want := range [][]string{
		{"-c:v", "libx264"}, {"-vf"}, {"-force_key_frames"}, {"-c:a", "aac", "-b:a", "192k", "-ac", "2"}, {"-profile:v", "high"},
	} {
		if !hasArgs(args, want...) {
			t.Errorf("ffmpeg args lack %v:\n%s", want, strings.Join(args, " "))
		}
	}
	for i, a := range args {
		if a == "-vf" && !strings.HasPrefix(args[i+1], "scale=-2:720,") {
			t.Errorf("-vf %q", args[i+1])
		}
	}
}

// Selecting an image subtitle forces a transcode whose filter graph overlays
// that stream.
func TestBurnInSession(t *testing.T) {
	e := newEnv(t, "")
	bin, argsFile := recordingFFmpeg(t)
	e.withStreams(bin, nil)
	rec := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.multi), nil, `{"profile": "browser", "subtitleIndex": 3}`)
	wantStatus(t, rec, 200)
	pb := decode[Playback](t, rec)
	if pb.Method != "transcode" || pb.VideoAction != "transcode" || pb.AudioAction != "copy" || pb.SessionID == "" {
		t.Fatalf("playback = %+v", pb)
	}
	if !strings.Contains(strings.Join(pb.Reasons, "|"), "must be burned into the video") {
		t.Errorf("reasons = %q", pb.Reasons)
	}
	args := argsOf(t, argsFile)
	var graph string
	for i, a := range args {
		if a == "-filter_complex" {
			graph = args[i+1]
		}
		if a == "-vf" {
			t.Errorf("burn-in must use -filter_complex, found -vf %q", args[i+1])
		}
	}
	if !strings.Contains(graph, "[0:3]") || !strings.Contains(graph, "overlay") || !strings.HasSuffix(graph, "[v]") {
		t.Errorf("filter graph %q", graph)
	}
	if !hasArgs(args, "-map", "[v]") || hasArgs(args, "-map", "0:0") || !hasArgs(args, "-c:a", "copy") {
		t.Errorf("mapping: %s", strings.Join(args, " "))
	}

	// The same file without the subtitle plays directly: the burn-in is what
	// forced the transcode.
	rec = e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.multi), nil, `{"profile": "browser", "subtitleIndex": 4}`)
	if pb := decode[Playback](t, rec); pb.Method != "direct" {
		t.Errorf("text subtitle: %+v", pb)
	}
}

// The GET description of a transcode carries the same reasons as the POST.
func TestItemDetailDescribesTranscode(t *testing.T) {
	e := newEnv(t, "")
	for target, wantMethod := range map[string]string{"chromecast": "transcode", "browser": "transcode", "avplayer": "remux"} {
		rec := e.get(fmt.Sprintf("/api/items/%d?target=%s", e.mkv, target))
		wantStatus(t, rec, 200)
		pb := decode[ItemDetail](t, rec).Playback
		if pb.Method != wantMethod || pb.URL != "" {
			t.Errorf("%s: %+v", target, pb)
		}
		if wantMethod == "transcode" && !strings.Contains(strings.Join(pb.Reasons, "|"), "libx264") {
			t.Errorf("%s: reasons = %q", target, pb.Reasons)
		}
	}
	wantJSONError(t, e.get(fmt.Sprintf("/api/items/%d?target=nope", e.mkv)), 400)
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
