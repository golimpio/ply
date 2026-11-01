//go:build integration && unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	e2eToken   = "e2e-token_1"
	e2eName    = "ply-e2e-test"
	movieRel   = "Movies/Test Movie (2020).mp4"
	episodeRel = "Shows/Show/Season 1/Show S01E02.mkv"
	srtRel     = "Shows/Show/Season 1/Show S01E02.en.srt"

	// The episode is long on purpose: a transcode of it cannot finish before
	// the session's throttle (more than 20 segments ahead of the player)
	// stops ffmpeg, so a live ffmpeg child exists to look for.
	episodeSeconds = 900
)

const srt = "1\n00:00:01,000 --> 00:00:02,500\nOlá, ply\n\n2\n00:00:03,000 --> 00:00:04,000\nSecond cue\n"

// TestEndToEnd runs the real binary the way a user would: a data directory
// with a space in its name, a token, an ephemeral port, real ffmpeg. It ends
// with SIGTERM and checks that nothing is left behind.
func TestEndToEnd(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media root")
	data := filepath.Join(base, "data dir", "ply data")
	tmp := filepath.Join(base, "tmp") // TMPDIR of the server: where ply-sessions goes
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := buildPly(t, base)
	makeFixtures(t, media)

	p := startPly(t, base, bin, tmp, "-media", media, "-addr", "127.0.0.1:0", "-data", data,
		"-token", e2eToken, "-name", e2eName, "-scan-interval", "0", "-hwaccel", "none")
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}

	// Auth: /api/info is public, everything else needs the token.
	resp, body := c.do("GET", "/api/info", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/info = %d: %s", resp.StatusCode, body)
	}
	var info struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		AuthRequired bool   `json:"authRequired"`
	}
	mustJSON(t, body, &info)
	if info.Name != e2eName || !info.AuthRequired || len(info.ID) != 32 {
		t.Errorf("info = %+v", info)
	}
	if id, err := os.ReadFile(filepath.Join(data, "server-id")); err != nil || strings.TrimSpace(string(id)) != info.ID {
		t.Errorf("server-id in the data dir = %q (%v), want %q", id, err, info.ID)
	}
	if _, err := os.Stat(filepath.Join(data, "ply.db")); err != nil {
		t.Errorf("database not in the data dir: %v", err)
	}
	for name, hdr := range map[string]map[string]string{
		"no token":    nil,
		"wrong token": {"Authorization": "Bearer nope"},
	} {
		resp, body := c.do("GET", "/api/items", "", hdr)
		if resp.StatusCode != 401 || !strings.Contains(string(body), `"error"`) {
			t.Errorf("GET /api/items with %s = %d %s, want 401 with a JSON error", name, resp.StatusCode, body)
		}
	}
	if resp, _ := c.do("GET", "/api/items?token="+e2eToken, "", nil); resp.StatusCode != 200 {
		t.Errorf("GET /api/items?token= = %d, want 200", resp.StatusCode)
	}

	// The startup scan completes and finds both files.
	c.waitForScan(2)
	var items []item
	resp, body = c.do("GET", "/api/items", "", bearer)
	mustJSON(t, body, &items)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(items), body)
	}
	var movie, episode item
	for _, it := range items {
		switch it.Kind {
		case "movie":
			movie = it
		case "episode":
			episode = it
		}
	}
	if movie.ID == 0 || episode.ID == 0 {
		t.Fatalf("want one movie and one episode, got %s", body)
	}
	if movie.Title != "Test Movie" || movie.Year == nil || *movie.Year != 2020 || movie.Container != "mp4" ||
		movie.VideoCodec != "h264" || movie.AudioCodec != "aac" {
		t.Errorf("movie = %+v", movie)
	}
	// "Show S01E02" has no episode title: the summary falls back to SxxExx.
	if episode.Title != "S01E02" || episode.Show != "Show" || episode.Season == nil || *episode.Season != 1 ||
		episode.Episode == nil || *episode.Episode != 2 || episode.Container != "mkv" ||
		episode.VideoCodec != "hevc" || episode.AudioCodec != "eac3" {
		t.Errorf("episode = %+v", episode)
	}

	// Direct play: the movie's file URL carries the token and honours Range.
	var det detail
	resp, body = c.do("GET", fmt.Sprintf("/api/items/%d", movie.ID), "", bearer)
	mustJSON(t, body, &det)
	if det.Playback.Method != "direct" || !strings.Contains(det.Playback.URL, "token="+e2eToken) {
		t.Fatalf("movie playback = %+v", det.Playback)
	}
	moviePath := filepath.Join(media, movieRel)
	want, err := os.ReadFile(moviePath)
	if err != nil {
		t.Fatal(err)
	}
	resp, body = c.do("GET", det.Playback.URL, "", map[string]string{"Range": "bytes=0-99"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range request = %d, want 206", resp.StatusCode)
	}
	if got, wantCR := resp.Header.Get("Content-Range"), fmt.Sprintf("bytes 0-99/%d", len(want)); got != wantCR {
		t.Errorf("Content-Range = %q, want %q", got, wantCR)
	}
	if !bytes.Equal(body, want[:100]) || resp.Header.Get("Content-Type") != "video/mp4" {
		t.Errorf("range body is not the first 100 bytes of the file (%d bytes, %s)", len(body), resp.Header.Get("Content-Type"))
	}
	if resp, _ := c.do("GET", strings.Split(det.Playback.URL, "?")[0], "", nil); resp.StatusCode != 401 {
		t.Errorf("file URL without a token = %d, want 401", resp.StatusCode)
	}

	// The sidecar subtitle is listed and converted to WebVTT.
	resp, body = c.do("GET", fmt.Sprintf("/api/items/%d", episode.ID), "", bearer)
	det = detail{}
	mustJSON(t, body, &det)
	var sub *subtitle
	for i := range det.Subtitles {
		if det.Subtitles[i].Key == "ext0" {
			sub = &det.Subtitles[i]
		}
	}
	if sub == nil || sub.Language != "en" || sub.Format != "srt" || !strings.Contains(sub.URL, "token="+e2eToken) {
		t.Fatalf("episode subtitles = %+v", det.Subtitles)
	}
	resp, body = c.do("GET", sub.URL, "", nil)
	vtt := string(body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/vtt") ||
		!strings.HasPrefix(vtt, "WEBVTT") || !strings.Contains(vtt, "00:01.000 --> 00:02.500") || // ffmpeg drops the zero hour
		!strings.Contains(vtt, "Olá, ply") {
		t.Errorf("subtitle = %d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), vtt)
	}
	if vtts, _ := filepath.Glob(filepath.Join(data, "subs", "*.vtt")); len(vtts) != 1 {
		t.Errorf("cached WebVTT files in the data dir = %v, want 1", vtts)
	}

	// Remux session: playlist, init segment and segment 0.
	remux := c.startSession(episode.ID, `{"profile": "avplayer"}`, bearer, "remux")
	pl := c.getPlaylist(remux)
	if want := episodeSeconds / 6; len(pl.segs) < want-2 || len(pl.segs) > want+2 { // a keyframe every 2 s, segments of 6 s
		t.Errorf("remux playlist has %d segments for a %d s file, want about %d", len(pl.segs), episodeSeconds, want)
	}
	if d := pl.total - episodeSeconds; d < -1 || d > 1 {
		t.Errorf("playlist durations add up to %.3f s, want about %d", pl.total, episodeSeconds)
	}
	c.checkMedia(pl.init, "ftyp", "init segment")
	c.checkMedia(pl.segs[0], "styp", "segment 0")

	// Transcode session of the same file: its ffmpeg is the live child that
	// the shutdown checks look for.
	transcode := c.startSession(episode.ID, `{"custom": {"name": "small", "containers": ["mp4"], `+
		`"videoCodecs": ["h264"], "audioCodecs": ["aac"], "maxHeight": 120}}`, bearer, "transcode")
	tpl := c.getPlaylist(transcode)
	c.checkMedia(tpl.init, "ftyp", "transcode init segment")
	c.checkMedia(tpl.segs[0], "styp", "transcode segment 0")

	// Progress shows up in /api/continue (which starts empty).
	resp, body = c.do("GET", "/api/continue", "", bearer)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("continue before any progress = %s, want []", body)
	}
	resp, body = c.do("PUT", fmt.Sprintf("/api/items/%d/progress", episode.ID),
		fmt.Sprintf(`{"positionSec": 90, "durationSec": %d}`, episodeSeconds), bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"positionSec":90`) || !strings.Contains(string(body), `"watched":false`) {
		t.Fatalf("PUT progress = %d %s", resp.StatusCode, body)
	}
	var cont []item
	resp, body = c.do("GET", "/api/continue", "", bearer)
	mustJSON(t, body, &cont)
	if len(cont) != 1 || cont[0].ID != episode.ID || cont[0].Progress == nil || cont[0].Progress.PositionSec != 90 {
		t.Errorf("continue = %s, want the episode at 90 s", body)
	}

	// Positive controls for the shutdown checks, taken while sessions run.
	ffmpegs := p.children("ffmpeg")
	if len(ffmpegs) == 0 {
		t.Fatalf("positive control: no ffmpeg child of ply (pid %d) is running", p.pid)
	}
	for _, f := range ffmpegs {
		if f.pgid != f.pid {
			t.Errorf("ffmpeg %d runs in process group %d, want its own", f.pid, f.pgid)
		}
	}
	sessions := filepath.Join(tmp, "ply-sessions", strconv.Itoa(p.pid))
	if dirs, _ := os.ReadDir(sessions); len(dirs) != 2 {
		t.Fatalf("positive control: %s holds %d session dirs, want 2", sessions, len(dirs))
	}
	if found := mentioning(t, base); len(found) < 2 { // ply itself and the ffmpeg
		t.Fatalf("positive control: the process scan finds %v for %s", found, base)
	}

	// SIGTERM: a clean, prompt exit.
	start := time.Now()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("ply still running 10 s after SIGTERM\n%s", p.log.String())
	}
	if p.waitErr != nil || p.cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("ply exited with %v (code %d), want a clean exit\n%s", p.waitErr, p.cmd.ProcessState.ExitCode(), p.log.String())
	}
	t.Logf("exited %s after SIGTERM", time.Since(start).Round(time.Millisecond))
	if log := p.log.String(); !strings.Contains(log, "shutting down") || strings.Contains(log, "level=ERROR") ||
		strings.Contains(log, "panic") {
		t.Errorf("server log is not that of a clean run:\n%s", log)
	}

	// Nothing left behind.
	for _, f := range ffmpegs {
		if processAlive(f.pid) || groupAlive(f.pgid) {
			t.Errorf("ffmpeg %d (group %d) survived ply", f.pid, f.pgid)
		}
	}
	if left := mentioning(t, base); len(left) != 0 {
		t.Errorf("processes still reference %s: %v", base, left)
	}
	if _, err := os.Stat(sessions); err == nil {
		t.Errorf("session temp dir %s still exists", sessions)
	}
	if rest, _ := os.ReadDir(filepath.Join(tmp, "ply-sessions")); len(rest) != 0 {
		t.Errorf("ply-sessions still holds %d entries", len(rest))
	}
}

// buildPly compiles this package into a temp dir (the binary is what users
// run, flags and signal handling included).
func buildPly(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "ply-bin")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// makeFixtures writes the library: a short mp4/h264/aac movie, and a long but
// small mkv/hevc/eac3 episode with an English .srt next to it.
func makeFixtures(t *testing.T, media string) {
	t.Helper()
	for _, f := range []struct {
		rel  string
		in   []string
		args []string
	}{
		{movieRel, []string{"testsrc=duration=3:size=320x240:rate=25", "sine=frequency=440:duration=3"},
			[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest"}},
		{episodeRel, []string{"testsrc2=size=160x120:rate=10", "sine=frequency=440:sample_rate=48000"},
			[]string{"-t", strconv.Itoa(episodeSeconds), "-c:v", "libx265", "-preset", "ultrafast", "-b:v", "60k",
				"-x265-params", "keyint=20:min-keyint=20:scenecut=0:open-gop=0:log-level=error", "-pix_fmt", "yuv420p",
				"-c:a", "eac3", "-b:a", "32k", "-ac", "1"}},
	} {
		out := filepath.Join(media, f.rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		args := []string{"-nostdin", "-v", "error", "-y"}
		for _, in := range f.in {
			args = append(args, "-f", "lavfi", "-i", in)
		}
		args = append(append(args, f.args...), out)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		output, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(media, srtRel), []byte(srt), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ply is a running server process.
type ply struct {
	t       *testing.T
	cmd     *exec.Cmd
	pid     int
	log     *logSink
	exited  chan struct{} // closed once the process has been reaped
	waitErr error
}

func startPly(t *testing.T, base, bin, tmp string, args ...string) *ply {
	t.Helper()
	p := &ply{t: t, log: &logSink{url: make(chan string, 1)}, exited: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	p.cmd.Stdout, p.cmd.Stderr = p.log, p.log
	// A leaked grandchild holding the log pipe must not hang the test.
	p.cmd.WaitDelay = 3 * time.Second
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.pid = p.cmd.Process.Pid
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
		// Whatever a failed run left behind must not outlive the test.
		for _, pr := range mentioning(t, base) {
			_ = syscall.Kill(-pr.pgid, syscall.SIGKILL)
			_ = syscall.Kill(pr.pid, syscall.SIGKILL)
		}
		if t.Failed() {
			t.Logf("server log:\n%s", p.log.String())
		}
	})
	return p
}

// waitForURL returns the address from the "reachable at" log line.
func (p *ply) waitForURL() string {
	p.t.Helper()
	select {
	case u := <-p.log.url:
		return u
	case <-p.exited:
		p.t.Fatalf("ply exited before it was listening: %v\n%s", p.waitErr, p.log.String())
	case <-time.After(30 * time.Second):
		p.t.Fatalf("no \"reachable at\" line after 30 s\n%s", p.log.String())
	}
	return ""
}

// children returns the direct children of ply whose program is named name.
func (p *ply) children(name string) []procInfo {
	var out []procInfo
	for _, pr := range listProcs(p.t) {
		if pr.ppid == p.pid && filepath.Base(strings.Fields(pr.command)[0]) == name {
			out = append(out, pr)
		}
	}
	return out
}

// logSink collects ply's log and extracts the listen address from it.
type logSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	url  chan string
	sent bool
}

var reachable = regexp.MustCompile(`msg="reachable at" url=(http://\S+)\n`)

func (l *logSink) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(b)
	if !l.sent {
		if m := reachable.FindSubmatch(l.buf.Bytes()); m != nil {
			l.sent = true
			l.url <- string(m[1])
		}
	}
	return len(b), nil
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type procInfo struct {
	pid, ppid, pgid int
	command         string
}

func (p procInfo) String() string {
	return fmt.Sprintf("%d(ppid %d, pgid %d): %.120s", p.pid, p.ppid, p.pgid, p.command)
}

func listProcs(t *testing.T) []procInfo {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var procs []procInfo
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		procs = append(procs, procInfo{pid, ppid, pgid, strings.Join(f[3:], " ")})
	}
	return procs
}

// mentioning lists the processes (other than this test and the ps it ran)
// whose command line contains s.
func mentioning(t *testing.T, s string) []procInfo {
	t.Helper()
	var out []procInfo
	for _, pr := range listProcs(t) {
		if pr.pid != os.Getpid() && strings.Contains(pr.command, s) && !strings.HasPrefix(pr.command, "ps ") {
			out = append(out, pr)
		}
	}
	return out
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
func groupAlive(pgid int) bool  { return syscall.Kill(-pgid, 0) == nil }

// client talks to the server under test.
type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, target, body string, hdr map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+target, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("%s %s: reading the body: %v", method, target, err)
	}
	return resp, b
}

// waitForScan waits until a scan has finished with n items in the library.
func (c *client) waitForScan(n int) {
	c.t.Helper()
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, body := c.do("GET", "/api/scan", "", bearer)
		var st struct {
			Running      bool    `json:"running"`
			LastFinished *string `json:"lastFinished"`
			LastError    string  `json:"lastError"`
			ItemCount    int     `json:"itemCount"`
		}
		mustJSON(c.t, body, &st)
		if !st.Running && st.LastFinished != nil {
			if st.LastError != "" || st.ItemCount != n {
				c.t.Fatalf("scan finished with error %q and %d items, want none and %d", st.LastError, st.ItemCount, n)
			}
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("scan did not finish in 60 s: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startSession POSTs /playback and checks it made a session of the given method.
func (c *client) startSession(id int64, body string, hdr map[string]string, method string) playbackResp {
	c.t.Helper()
	resp, b := c.do("POST", fmt.Sprintf("/api/items/%d/playback", id), body, hdr)
	var pb playbackResp
	mustJSON(c.t, b, &pb)
	if resp.StatusCode != 200 || pb.Method != method || pb.SessionID == "" ||
		pb.URL != "/api/sessions/"+pb.SessionID+"/index.m3u8?token="+e2eToken {
		c.t.Fatalf("POST playback (%s) = %d %s", method, resp.StatusCode, b)
	}
	return pb
}

type playlist struct {
	init  string   // absolute path (with query) of the init segment
	segs  []string // absolute paths of the segments
	total float64  // sum of EXTINF
}

var (
	mapRE    = regexp.MustCompile(`#EXT-X-MAP:URI="([^"]+)"`)
	extinfRE = regexp.MustCompile(`#EXTINF:([0-9.]+),\n(\S+)`)
)

// getPlaylist fetches a session's playlist and resolves its URIs. Players
// that cannot send headers rely on the token being in every one of them.
func (c *client) getPlaylist(pb playbackResp) playlist {
	c.t.Helper()
	resp, b := c.do("GET", pb.URL, "", nil)
	pl := string(b)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		!strings.Contains(pl, "#EXT-X-PLAYLIST-TYPE:VOD") || !strings.Contains(pl, "#EXT-X-ENDLIST") {
		c.t.Fatalf("playlist = %d %s\n%.400s", resp.StatusCode, resp.Header.Get("Content-Type"), pl)
	}
	baseURL, _ := url.Parse(pb.URL)
	resolve := func(ref string) string {
		if !strings.Contains(ref, "token="+e2eToken) {
			c.t.Errorf("playlist URI %q carries no token", ref)
		}
		u, err := baseURL.Parse(ref)
		if err != nil {
			c.t.Fatal(err)
		}
		return u.RequestURI()
	}
	var out playlist
	m := mapRE.FindStringSubmatch(pl)
	if m == nil {
		c.t.Fatalf("no EXT-X-MAP in\n%.400s", pl)
	}
	out.init = resolve(m[1])
	for _, x := range extinfRE.FindAllStringSubmatch(pl, -1) {
		d, _ := strconv.ParseFloat(x[1], 64)
		out.total += d
		out.segs = append(out.segs, resolve(x[2]))
	}
	if len(out.segs) == 0 {
		c.t.Fatalf("playlist has no segments:\n%.400s", pl)
	}
	return out
}

// checkMedia fetches an fMP4 piece and checks that it starts with the given box.
func (c *client) checkMedia(target, box, what string) {
	c.t.Helper()
	resp, b := c.do("GET", target, "", nil)
	if resp.StatusCode != 200 || len(b) < 100 || string(b[4:8]) != box {
		c.t.Errorf("%s: status %d, %d bytes, box %q; want 200 and a %q box", what, resp.StatusCode, len(b), safePrefix(b), box)
	}
}

func safePrefix(b []byte) string {
	if len(b) >= 8 {
		return string(b[4:8])
	}
	return string(b)
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("bad JSON %q: %v", b, err)
	}
}

type item struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Year         *int   `json:"year"`
	Show         string `json:"show"`
	Season       *int   `json:"season"`
	Episode      *int   `json:"episode"`
	EpisodeTitle string `json:"episodeTitle"`
	Container    string `json:"container"`
	VideoCodec   string `json:"videoCodec"`
	AudioCodec   string `json:"audioCodec"`
	Progress     *struct {
		PositionSec float64 `json:"positionSec"`
		Watched     bool    `json:"watched"`
	} `json:"progress"`
}

type subtitle struct {
	Key      string `json:"key"`
	Language string `json:"language"`
	Format   string `json:"format"`
	URL      string `json:"url"`
}

type playbackResp struct {
	Method    string `json:"method"`
	URL       string `json:"url"`
	SessionID string `json:"sessionId"`
}

type detail struct {
	item
	Subtitles []subtitle   `json:"subtitles"`
	Playback  playbackResp `json:"playback"`
}
