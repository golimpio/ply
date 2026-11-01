//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
)

// A 30 s clip with a keyframe every 2 s (the fixtures of the stream package).
func makeClip(t *testing.T, path string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	base := []string{"-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30"}
	out, err := exec.Command("ffmpeg", append(append(base, args...), path)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
}

type e2e struct {
	t      *testing.T
	ts     *httptest.Server
	store  *store.Store
	mgr    *stream.Manager
	root   string
	mkv    int64 // mkv/hevc/eac3
	mp4    int64 // mp4/h264/aac
	token  string
	client *http.Client
}

func newE2E(t *testing.T, token string) *e2e {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media root")
	makeClip(t, filepath.Join(root, "Movies", "Hevc Clip (2021).mkv"), "-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "keyint=48:min-keyint=48:scenecut=0:open-gop=0:log-level=error", "-pix_fmt", "yuv420p",
		"-c:a", "eac3", "-b:a", "128k", "-ac", "2")
	makeClip(t, filepath.Join(root, "Movies", "H264 Clip (2022).mp4"), "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k")

	st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sc := library.NewScanner(t.Context(), st, probe.FFprobe{}, []string{root}, nil)
	if err := sc.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	mgr, err := stream.NewManager(stream.Options{TempRoot: t.TempDir(), Cache: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	srv := New(Options{Store: st, Scanner: sc, ID: "it", Name: "it", Version: "test", Token: token, Streams: mgr})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	e := &e2e{t: t, ts: ts, store: st, mgr: mgr, root: root, token: token, client: ts.Client()}
	entries, err := st.ListItems(t.Context(), store.ListFilter{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("scan found %d items, %v", len(entries), err)
	}
	for _, en := range entries {
		switch en.Container {
		case "mkv":
			e.mkv = en.ID
		case "mp4":
			e.mp4 = en.ID
		}
	}
	return e
}

func (e *e2e) do(method, path string, body string, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

var extinf = regexp.MustCompile(`#EXTINF:([0-9.]+),\n(\S+)`)

type plSeg struct {
	uri        string
	start, dur float64
}

func parsePlaylist(t *testing.T, pl string) (initURI string, segs []plSeg) {
	t.Helper()
	m := regexp.MustCompile(`#EXT-X-MAP:URI="([^"]+)"`).FindStringSubmatch(pl)
	if m == nil {
		t.Fatalf("no EXT-X-MAP in\n%s", pl)
	}
	var at float64
	for _, x := range extinf.FindAllStringSubmatch(pl, -1) {
		d, _ := strconv.ParseFloat(x[1], 64)
		segs = append(segs, plSeg{uri: x[2], start: at, dur: d})
		at += d
	}
	return m[1], segs
}

// span returns the first and last+duration presentation time of the video.
func videoSpan(t *testing.T, init, seg []byte) (first, end float64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.mp4")
	if err := os.WriteFile(p, append(append([]byte{}, init...), seg...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	first, end = math.Inf(1), math.Inf(-1)
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(strings.TrimRight(l, ","), ",")
		pts, e1 := strconv.ParseFloat(f[0], 64)
		dur, e2 := 0.0, error(nil)
		if len(f) > 1 {
			dur, e2 = strconv.ParseFloat(f[1], 64)
		}
		if e1 != nil || e2 != nil {
			continue
		}
		first, end = math.Min(first, pts), math.Max(end, pts+dur)
	}
	return first, end
}

func ffmpegRunning(t *testing.T, path string) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "ffmpeg") && strings.Contains(l, path) {
			found = append(found, strings.TrimSpace(l))
		}
	}
	return found
}

func TestHLSEndToEnd(t *testing.T) {
	for _, token := range []string{"", "t0k&en"} {
		for _, item := range []string{"mkv", "mp4"} {
			t.Run(fmt.Sprintf("%s token=%q", item, token), func(t *testing.T) {
				e := newE2E(t, token)
				id := e.mkv
				body := `{}` // avplayer: mkv remuxes, eac3 audio is copied
				if item == "mp4" {
					id = e.mp4
					body = `{"custom": {"containers": ["mkv"], "videoCodecs": ["h264"], "audioCodecs": ["aac"]}}`
				}
				auth := map[string]string{}
				if token != "" {
					auth["Authorization"] = "Bearer " + token
				}

				resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", id), body, auth)
				if resp.StatusCode != 200 {
					t.Fatalf("POST playback = %d: %s", resp.StatusCode, b)
				}
				var pb Playback
				if err := json.Unmarshal(b, &pb); err != nil {
					t.Fatal(err)
				}
				if pb.Method != "remux" || pb.SessionID == "" || !strings.HasPrefix(pb.URL, "/api/sessions/"+pb.SessionID+"/index.m3u8") {
					t.Fatalf("playback = %+v", pb)
				}
				if _, ok, _ := e.store.Keyframes(t.Context(), id); !ok {
					t.Error("keyframes were not cached")
				}

				// The playlist URL is all a token-less player (AirPlay, Cast)
				// has: it must work as given and so must every URI inside.
				resp, b = e.do("GET", pb.URL, "", nil)
				if resp.StatusCode != 200 {
					t.Fatalf("GET playlist = %d: %s", resp.StatusCode, b)
				}
				initURI, segs := parsePlaylist(t, string(b))
				if len(segs) != 5 {
					t.Fatalf("playlist has %d segments:\n%s", len(segs), b)
				}
				if token != "" && !strings.Contains(initURI, "?token=t0k%26en") {
					t.Errorf("init URI %q lacks the escaped token", initURI)
				}
				base := "/api/sessions/" + pb.SessionID + "/"

				resp, initBytes := e.do("GET", base+initURI, "", nil)
				if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" || len(initBytes) < 100 {
					t.Fatalf("init: %d %q %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(initBytes))
				}
				if !bytes.Contains(initBytes[:64], []byte("ftyp")) {
					t.Error("init does not start with ftyp")
				}

				// Segment 0, a far segment (seek), and back.
				for _, i := range []int{0, 4, 2, 0} {
					resp, seg := e.do("GET", base+segs[i].uri, "", nil)
					if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" {
						t.Fatalf("segment %d: %d %q", i, resp.StatusCode, resp.Header.Get("Content-Type"))
					}
					if resp.Header.Get("Content-Length") != strconv.Itoa(len(seg)) {
						t.Errorf("segment %d: Content-Length %q for %d bytes", i, resp.Header.Get("Content-Length"), len(seg))
					}
					first, end := videoSpan(t, initBytes, seg)
					wantStart := segs[i].start + stream.TimelineOffset
					if math.Abs(first-wantStart) > 0.1 || math.Abs(end-(wantStart+segs[i].dur)) > 0.1 {
						t.Errorf("segment %d: media %.3f..%.3f, playlist says %.3f..%.3f", i, first, end, wantStart, wantStart+segs[i].dur)
					}
				}

				// Range requests work on segments.
				resp, part := e.do("GET", base+segs[1].uri, "", map[string]string{"Range": "bytes=0-99"})
				if resp.StatusCode != 206 || len(part) != 100 || !bytes.Contains(part[:16], []byte("styp")) {
					t.Errorf("range: %d, %d bytes", resp.StatusCode, len(part))
				}

				// Not a path to anything else.
				for _, name := range []string{"ffmpeg.m3u8", "gop00000.m4s", "init1.mp4"} {
					if resp, _ := e.do("GET", base+name, "", auth); resp.StatusCode != 404 {
						t.Errorf("GET %s = %d, want 404", name, resp.StatusCode)
					}
				}

				// DELETE ends it and removes the directory. (That ffmpeg is gone
				// too cannot be seen here: this 30 s clip was finished long
				// before. TestDeleteKillsAThrottledFFmpeg covers it with a live
				// process.)
				sess, _ := e.mgr.Get(pb.SessionID)
				dir := sess.Dir()
				resp, _ = e.do("DELETE", "/api/sessions/"+pb.SessionID, "", auth)
				if resp.StatusCode != 204 {
					t.Fatalf("DELETE = %d", resp.StatusCode)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("session dir still there: %v", err)
				}
				if resp, _ := e.do("GET", pb.URL, "", nil); resp.StatusCode != 404 {
					t.Errorf("playlist after DELETE = %d, want 404", resp.StatusCode)
				}
			})
		}
	}
}

func TestStartSecResumesAtThatSegment(t *testing.T) {
	e := newE2E(t, "")
	resp, b := e.do("POST", fmt.Sprintf("/api/items/%d/playback", e.mkv), `{"startSec": 20}`, nil)
	var pb Playback
	if err := json.Unmarshal(b, &pb); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%d %s %v", resp.StatusCode, b, err)
	}
	if pb.StartSec != 20 {
		t.Errorf("startSec = %v", pb.StartSec)
	}
	_, pl := e.do("GET", pb.URL, "", nil)
	initURI, segs := parsePlaylist(t, string(pl))
	base := "/api/sessions/" + pb.SessionID + "/"
	_, initBytes := e.do("GET", base+initURI, "", nil)
	// 20 s is in segment 3 (18 s..24 s): the first request is a cold one for
	// the segment ffmpeg was started on.
	_, seg := e.do("GET", base+segs[3].uri, "", nil)
	first, _ := videoSpan(t, initBytes, seg)
	if math.Abs(first-(segs[3].start+stream.TimelineOffset)) > 0.1 {
		t.Errorf("segment 3 starts at %.3f, want %.3f", first, segs[3].start+stream.TimelineOffset)
	}
}

// longE2E is a library of one 25 minute clip and a stream manager that
// throttles hard: ffmpeg is still alive, and stopped by SIGSTOP, when the test
// ends the session. A 30 s clip would be finished long before, and "no ffmpeg
// afterwards" would prove nothing.
type longE2E struct {
	ts    *httptest.Server
	mgr   *stream.Manager
	entry store.Entry
}

func newLongE2E(t *testing.T, mod func(*stream.Options)) *longE2E {
	t.Helper()
	path := filepath.Join(t.TempDir(), "media root", "Long Clip (2023).mkv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=gray:size=64x36:rate=5",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000", "-t", "1500", "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "10", "-keyint_min", "10", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "16k", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sc := library.NewScanner(t.Context(), st, probe.FFprobe{}, []string{filepath.Dir(path)}, nil)
	if err := sc.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := st.ListItems(t.Context(), store.ListFilter{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("scan found %d items, %v", len(entries), err)
	}
	o := stream.Options{TempRoot: t.TempDir(), Cache: st, Tick: 20 * time.Millisecond, ThrottleHigh: 2, ThrottleLow: 1}
	if mod != nil {
		mod(&o)
	}
	mgr, err := stream.NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	ts := httptest.NewServer(New(Options{Store: st, Scanner: sc, ID: "it", Name: "it", Version: "test", Streams: mgr}))
	t.Cleanup(ts.Close)
	return &longE2E{ts: ts, mgr: mgr, entry: entries[0]}
}

func (l *longE2E) request() stream.Request {
	return stream.Request{ItemID: l.entry.ID, Path: l.entry.Path, Container: "mkv", DurationSec: l.entry.DurationSec,
		VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true}
}

// waitStopped waits until n ffmpeg processes read the clip and all are stopped
// by the throttle (state T), and returns them.
func (l *longE2E) waitStopped(t *testing.T, n int) []string {
	t.Helper()
	var procs []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		procs = ffmpegRunning(t, l.entry.Path)
		stopped := 0
		for _, p := range procs {
			out, _ := exec.Command("ps", "-o", "state=", "-p", strings.Fields(p)[0]).Output()
			if strings.HasPrefix(strings.TrimSpace(string(out)), "T") {
				stopped++
			}
		}
		if len(procs) == n && stopped == n {
			return procs
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("wanted %d stopped ffmpeg processes, have %v", n, procs)
	return nil
}

// DELETE has to kill an ffmpeg that the throttle has stopped, which only
// SIGKILL does, before it answers.
func TestDeleteKillsAThrottledFFmpeg(t *testing.T) {
	l := newLongE2E(t, nil)
	resp, err := http.Post(fmt.Sprintf("%s/api/items/%d/playback", l.ts.URL, l.entry.ID), "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var pb Playback
	err = json.NewDecoder(resp.Body).Decode(&pb)
	resp.Body.Close()
	if err != nil || pb.SessionID == "" {
		t.Fatalf("playback = %+v, %v", pb, err)
	}
	sess, ok := l.mgr.Get(pb.SessionID)
	if !ok {
		t.Fatal("no session")
	}
	dir := sess.Dir()
	l.waitStopped(t, 1) // positive control: there is a process to kill

	req, _ := http.NewRequest("DELETE", l.ts.URL+"/api/sessions/"+pb.SessionID, nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != 204 {
		t.Fatalf("DELETE = %d", del.StatusCode)
	}
	if p := ffmpegRunning(t, l.entry.Path); len(p) != 0 {
		t.Errorf("ffmpeg still running after DELETE: %v", p)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir still there: %v", err)
	}
}

func TestIdleSessionEndsAndShutdownCleans(t *testing.T) {
	l := newLongE2E(t, func(o *stream.Options) { o.IdleTimeout = 1500 * time.Millisecond })
	s, err := l.mgr.Create(t.Context(), l.request())
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir()
	l.waitStopped(t, 1) // positive control: a live (stopped) ffmpeg to be killed

	// Count() is what says the session is over. It must not drop before the
	// process is dead and the directory gone, so look at the very moment it does.
	deadline := time.Now().Add(15 * time.Second)
	for l.mgr.Count() > 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if l.mgr.Count() != 0 {
		t.Fatal("idle session was not ended")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("idle session dir still there: %v", err)
	}
	if p := ffmpegRunning(t, l.entry.Path); len(p) != 0 {
		t.Errorf("ffmpeg survived the idle expiry: %v", p)
	}

	// Shutdown: every session's ffmpeg is killed and everything removed.
	var dirs []string
	for range 2 {
		s, err := l.mgr.Create(t.Context(), l.request())
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, s.Dir())
	}
	l.waitStopped(t, 2)
	l.mgr.Close()
	if p := ffmpegRunning(t, l.entry.Path); len(p) != 0 {
		t.Errorf("ffmpeg survived shutdown: %v", p)
	}
	for _, d := range append(dirs, l.mgr.Dir()) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived shutdown: %v", d, err)
		}
	}
}
