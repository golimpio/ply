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
				id, file := e.mkv, "Hevc Clip (2021).mkv"
				body := `{}` // avplayer: mkv remuxes, eac3 audio is copied
				if item == "mp4" {
					id, file = e.mp4, "H264 Clip (2022).mp4"
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

				// DELETE ends it: no ffmpeg, no directory.
				sess, _ := e.mgr.Get(pb.SessionID)
				dir := sess.Dir()
				resp, _ = e.do("DELETE", "/api/sessions/"+pb.SessionID, "", auth)
				if resp.StatusCode != 204 {
					t.Fatalf("DELETE = %d", resp.StatusCode)
				}
				if p := ffmpegRunning(t, filepath.Join(e.root, "Movies", file)); len(p) != 0 {
					t.Errorf("ffmpeg still running after DELETE: %v", p)
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

func TestIdleSessionEndsAndShutdownCleans(t *testing.T) {
	e := newE2E(t, "")
	mgr, err := stream.NewManager(stream.Options{TempRoot: t.TempDir(), Cache: e.store, IdleTimeout: 700 * time.Millisecond,
		Tick: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	entry, err := e.store.GetEntry(context.Background(), e.mkv)
	if err != nil {
		t.Fatal(err)
	}
	s, err := mgr.Create(context.Background(), stream.Request{ItemID: e.mkv, Path: entry.Path, Container: "mkv",
		DurationSec: entry.DurationSec, VideoIndex: 0, VideoCodec: "hevc", AudioIndex: 1, AudioCodec: "eac3", AudioCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir()
	deadline := time.Now().Add(10 * time.Second)
	for mgr.Count() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if mgr.Count() != 0 {
		t.Fatal("idle session was not ended")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("idle session dir still there: %v", err)
	}
}
