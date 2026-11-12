//go:build integration && unix

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// killWithin is how long an ffmpeg may outlive a ply that was killed: the reaper
// (macOS) or the kernel (Linux) does it, nothing waits for a next start.
const killWithin = 2 * time.Second

func stateOf(pid int) string {
	out, _ := exec.Command("ps", "-o", "state=", "-p", fmt.Sprint(pid)).Output()
	return strings.TrimSpace(string(out))
}

// reapers lists the helper processes (ply --reap) that ply started for its ffmpeg.
func reapers(t *testing.T, ply int) []procInfo {
	t.Helper()
	var out []procInfo
	for _, pr := range listProcs(t) {
		if pr.ppid == ply && strings.Contains(pr.command, " --reap ") {
			out = append(out, pr)
		}
	}
	return out
}

// plays starts two transcodes of the long episode (the server is started with
// -max-transcodes 2) and returns the client and the two ffmpeg processes that are
// producing them. (A remux of the small fixture ends in a third of a second.)
func plays(t *testing.T, p *ply) (*client, []procInfo) {
	t.Helper()
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	c.waitForScan(2)
	var items []item
	_, body := c.do("GET", "/api/items", "", bearer)
	mustJSON(t, body, &items)
	var episode item
	for _, it := range items {
		if it.Kind == "episode" {
			episode = it
		}
	}
	if episode.ID == 0 {
		t.Fatalf("no episode in %s", body)
	}
	// The limit from the command line is what the settings say, and cannot be changed there.
	_, body = c.do("GET", "/api/settings", "", bearer)
	var st struct {
		MaxTranscodes       int    `json:"maxTranscodes"`
		MaxTranscodesSource string `json:"maxTranscodesSource"`
		MaxStreams          int    `json:"maxStreams"`
		MaxStreamsSource    string `json:"maxStreamsSource"`
	}
	mustJSON(t, body, &st)
	if st.MaxTranscodes != 2 || st.MaxTranscodesSource != "flag" || st.MaxStreams != 4 || st.MaxStreamsSource != "default" {
		t.Fatalf("settings = %s, want 2 transcodes from the flag and 4 streams by default", body)
	}
	if resp, b := c.do("PATCH", "/api/settings", `{"maxTranscodes": 1}`, bearer); resp.StatusCode != http.StatusConflict {
		t.Fatalf("PATCH of a limit given by a flag = %d %s, want 409", resp.StatusCode, b)
	}
	for _, height := range []int{120, 144} {
		pb := c.startSession(episode.ID, fmt.Sprintf(`{"custom": {"name": "small%d", "containers": ["mp4"], `+
			`"videoCodecs": ["h264"], "audioCodecs": ["aac"], "maxHeight": %d}}`, height, height), bearer, "transcode")
		pl := c.getPlaylist(pb)
		c.checkMedia(pl.init, "ftyp", "transcode init segment")
		c.checkMedia(pl.segs[0], "styp", "transcode segment 0")
	}
	// A third is over the limit, and says who is using it.
	resp, b := c.do("POST", fmt.Sprintf("/api/items/%d/playback", episode.ID), `{"custom": {"name": "s", "containers": ["mp4"], `+
		`"videoCodecs": ["h264"], "audioCodecs": ["aac"], "maxHeight": 96}}`, bearer)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" || !strings.Contains(string(b), "2 of 2 transcodes in use") {
		t.Fatalf("third transcode = %d (Retry-After %q) %s", resp.StatusCode, resp.Header.Get("Retry-After"), b)
	}

	ffmpegs := p.children("ffmpeg")
	if len(ffmpegs) != 2 {
		t.Fatalf("positive control: %d ffmpeg children of ply (pid %d), want 2 transcodes", len(ffmpegs), p.pid)
	}
	for _, f := range ffmpegs {
		if !processAlive(f.pid) || f.pgid != f.pid {
			t.Fatalf("positive control: ffmpeg %v is not alive and leading a group of its own", f)
		}
	}
	return c, ffmpegs
}

// stopGroups SIGSTOPs the ffmpeg groups and waits until they are stopped.
func stopGroups(t *testing.T, ffmpegs []procInfo) {
	t.Helper()
	for _, f := range ffmpegs {
		if err := syscall.Kill(-f.pgid, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range ffmpegs {
		deadline := time.Now().Add(5 * time.Second)
		for !strings.HasPrefix(stateOf(f.pid), "T") {
			if time.Now().After(deadline) {
				t.Fatalf("ffmpeg %d did not stop: state %q", f.pid, stateOf(f.pid))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// waitDead waits until every ffmpeg and its group is gone, and returns how long it took.
func waitDead(t *testing.T, ffmpegs []procInfo, within time.Duration, from time.Time) time.Duration {
	t.Helper()
	for {
		alive := false
		for _, f := range ffmpegs {
			if processAlive(f.pid) || groupAlive(f.pgid) {
				alive = true
			}
		}
		if !alive {
			return time.Since(from)
		}
		if time.Since(from) > within {
			var left []string
			for _, f := range ffmpegs {
				if processAlive(f.pid) {
					left = append(left, fmt.Sprintf("%d (state %q)", f.pid, stateOf(f.pid)))
				}
			}
			t.Fatalf("ffmpeg still alive %v after ply was killed with SIGKILL: %v", within, left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A ply that is killed (kill -9, the OOM killer) must not leave an ffmpeg running
// for hours with the library file open: the reaper that ply started on macOS, or
// the kernel on Linux, ends it within 2 s. That holds for an ffmpeg that is
// stopped (the throttle stops one that is ahead of the player) as much as for one
// that is running. A ply that starts later, with another TMPDIR, then has nothing
// left to kill.
func TestKillMinus9LeavesNoFFmpeg(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media")
	bin := buildPly(t, base)
	makeFixtures(t, media)

	for _, v := range []struct {
		name    string
		stopped bool // SIGSTOP the ffmpeg groups first
	}{{"running", false}, {"stopped", true}} {
		t.Run(v.name, func(t *testing.T) {
			dir := filepath.Join(base, v.name)
			data, tmpA, tmpB := filepath.Join(dir, "data"), filepath.Join(dir, "tmpA"), filepath.Join(dir, "tmpB")
			for _, d := range []string{tmpA, tmpB} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-movies", filepath.Join(media, "Movies"), "-shows", filepath.Join(media, "Shows"),
				"-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-name", e2eName, "-scan-interval", "0", "-hwaccel", "none",
				"-max-transcodes", "2"}
			p := startPly(t, base, bin, tmpA, args...)
			_, ffmpegs := plays(t, p)

			if runtime.GOOS == "darwin" { // ply starts one reaper per ffmpeg, detached
				rs := reapers(t, p.pid)
				if len(rs) != len(ffmpegs) {
					t.Fatalf("positive control: %d reapers for %d ffmpeg: %v", len(rs), len(ffmpegs), rs)
				}
				for _, r := range rs {
					if r.pgid == p.pid || r.pgid != r.pid {
						t.Errorf("reaper %v is not detached in a session of its own", r)
					}
					// Not named ply: `killall ply` must not take the reapers with the server.
					if name := filepath.Base(strings.Fields(r.command)[0]); name != "ply-reaper" {
						t.Errorf("reaper %v runs as %q, want ply-reaper", r, name)
					}
				}
			}
			files, _ := filepath.Glob(filepath.Join(data, "sessions", "*.json"))
			if len(files) != 2 {
				t.Fatalf("positive control: registry files %v, want 2", files)
			}
			if v.stopped {
				// Stopped, as ply's own throttle leaves an ffmpeg that is far ahead of the player.
				stopGroups(t, ffmpegs)
			} else {
				// Running: the throttle stops it again at its next tick (every 250 ms), so ply is
				// killed straight after the check.
				for _, f := range ffmpegs {
					_ = syscall.Kill(-f.pgid, syscall.SIGCONT)
				}
				for _, f := range ffmpegs {
					if s := stateOf(f.pid); s == "" || strings.HasPrefix(s, "T") {
						t.Fatalf("positive control: ffmpeg %d is %q, want it running", f.pid, s)
					}
				}
			}

			killed := time.Now()
			if err := p.cmd.Process.Kill(); err != nil { // SIGKILL: no handler, no cleanup
				t.Fatal(err)
			}
			<-p.exited
			took := waitDead(t, ffmpegs, killWithin, killed)
			t.Logf("%s: both ffmpeg groups gone %v after kill -9 of ply", v.name, took.Round(time.Millisecond))

			// The reapers have nothing left to do and go.
			deadline := time.Now().Add(3 * time.Second)
			for len(mentioning(t, base)) != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("processes still reference %s: %v", base, mentioning(t, base))
				}
				time.Sleep(20 * time.Millisecond)
			}

			// What ply could not remove is still on disk (positive control), and a new ply,
			// with another TMPDIR, finds nothing alive in it.
			stale := filepath.Join(data, "tmp", "sessions", fmt.Sprint(p.pid))
			files, _ = filepath.Glob(filepath.Join(data, "sessions", "*.json"))
			if len(files) != 2 || !exists(stale) {
				t.Fatalf("positive control: after kill -9 the registry holds %v and %s exists: %v", files, stale, exists(stale))
			}
			p2 := startPly(t, base, bin, tmpB, args...)
			p2.waitForURL()
			if log := p2.log.String(); strings.Contains(log, "left behind") || strings.Contains(log, "level=ERROR") {
				t.Errorf("the new ply found something to kill, or failed:\n%s", log)
			}
			if files, _ = filepath.Glob(filepath.Join(data, "sessions", "*.json")); len(files) != 0 {
				t.Errorf("the dead ply's registry files were not cleared: %v", files)
			}
			if exists(stale) {
				t.Errorf("the dead ply's session directory %s was not removed", stale)
			}
			if err := p2.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-p2.exited:
			case <-time.After(10 * time.Second):
				t.Fatal("the second ply did not exit on SIGTERM")
			}
		})
	}
}

// A stand-in for ffmpeg that ply can start and that then runs for a long time
// doing nothing: a running orphan that does not finish by itself.
const sleepyFFmpeg = `#!/bin/sh
for a in "$@"; do
  case "$a" in -encoders|-hwaccels|-filters|-version|-h) exit 0;; esac
done
exec sleep 600
`

// registered returns the processes the registry in the data directory names.
func registered(t *testing.T, data string) []procInfo {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(data, "sessions", "*.json"))
	var out []procInfo
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var e struct{ PID, PGID int }
		mustJSON(t, b, &e) // (field names match whatever their case)
		out = append(out, procInfo{pid: e.PID, pgid: e.PGID})
	}
	return out
}

// Without the reaper (here: killed first) a kill -9 does leave the ffmpeg, which is
// what the registry is for: the next ply finds it by the file in the data directory,
// whatever its TMPDIR, checks that the pid is the process that was recorded, and
// kills the group. (On Linux the kernel does not let it be left, and a group that
// is stopped is ended by the kernel when it is orphaned: only a running one lives.)
func TestRegistrySweepFindsAnOrphanFromAnotherTmpdir(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS can leave an orphan: Linux kills the ffmpeg with its parent")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH")
	}
	// The remux needs the keyframe index of a real file; ffmpeg itself is the stand-in.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}
	base := t.TempDir()
	media := filepath.Join(base, "media")
	bin := buildPly(t, base)
	makeFixtures(t, media)
	fake := filepath.Join(base, "ffmpeg")
	if err := os.WriteFile(fake, []byte(sleepyFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}
	data, tmpA, tmpB := filepath.Join(base, "data"), filepath.Join(base, "tmpA"), filepath.Join(base, "tmpB")
	for _, d := range []string{tmpA, tmpB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-movies", filepath.Join(media, "Movies"), "-shows", filepath.Join(media, "Shows"),
		"-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-name", e2eName, "-scan-interval", "0", "-hwaccel", "none",
		"-ffmpeg", fake}
	p := startPly(t, base, bin, tmpA, args...)
	c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	c.waitForScan(2)
	var items []item
	_, body := c.do("GET", "/api/items", "", bearer)
	mustJSON(t, body, &items)
	var episode item
	for _, it := range items {
		if it.Kind == "episode" {
			episode = it
		}
	}
	for range 2 {
		c.startSession(episode.ID, `{"profile": "avplayer"}`, bearer, "remux")
	}

	orphans := registered(t, data)
	if len(orphans) != 2 {
		t.Fatalf("positive control: the registry names %v, want the two ffmpeg", orphans)
	}
	for _, o := range orphans {
		if !processAlive(o.pid) || o.pgid != o.pid {
			t.Fatalf("positive control: registered process %v is not alive and leading its group", o)
		}
	}
	rs := reapers(t, p.pid)
	if len(rs) != 2 {
		t.Fatalf("positive control: %d reapers, want 2: %v", len(rs), rs)
	}
	for _, r := range rs {
		_ = syscall.Kill(r.pid, syscall.SIGKILL)
	}
	for deadline := time.Now().Add(3 * time.Second); len(reapers(t, p.pid)) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reapers did not go")
		}
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-p.exited

	// The orphans: this is the bug, and the starting point.
	time.Sleep(500 * time.Millisecond)
	for _, o := range orphans {
		if !processAlive(o.pid) {
			t.Fatalf("positive control: ffmpeg %d died with ply although nothing was watching it", o.pid)
		}
	}

	p2 := startPly(t, base, bin, tmpB, args...)
	p2.waitForURL()
	waitDead(t, orphans, 5*time.Second, time.Now())
	if log := p2.log.String(); strings.Count(log, "killed an ffmpeg left behind by a ply that is gone") != 2 {
		t.Errorf("the log does not say that two were killed:\n%s", log)
	}
	if files, _ := filepath.Glob(filepath.Join(data, "sessions", "*.json")); len(files) != 0 {
		t.Errorf("registry files left: %v", files)
	}
	if err := p2.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p2.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the second ply did not exit on SIGTERM")
	}
}

// The ffmpeg of the subtitles is guarded like the ones of playback: a ply that is killed (kill -9, the
// OOM killer) must not leave a film being read for an hour by an extraction nobody is waiting for. One
// extraction is the background job that the scan of a film with a text track in its container queues;
// the other is a request for a sidecar, which a viewer waits for; and a request for the container's
// track joins the first. Running or suspended (a play holds it), they are gone within killWithin.
func TestKillMinus9LeavesNoSubtitleFFmpeg(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	base := t.TempDir()
	media := filepath.Join(base, "media")
	bin := buildPly(t, base)
	makeFixtures(t, media)
	// A film with a text track in its container.
	subbed := filepath.Join(media, "Movies", "Subbed (2023).mkv")
	srtFile := filepath.Join(base, "subbed.srt")
	if err := os.WriteFile(srtFile, []byte(srt), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=3:size=160x120:rate=10",
		"-i", srtFile, "-map", "0", "-map", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:s", "srt", subbed).CombinedOutput(); err != nil {
		t.Fatalf("making the film: %v\n%s", err, out)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(subbed, old, old); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(base, "ffmpeg")
	if err := os.WriteFile(fake, []byte(sleepyFFmpeg), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, v := range []struct {
		name    string
		stopped bool // SIGSTOP the extractions first
	}{{"running", false}, {"stopped", true}} {
		t.Run(v.name, func(t *testing.T) {
			dir := filepath.Join(base, v.name)
			data, tmp := filepath.Join(dir, "data"), filepath.Join(dir, "tmp")
			if err := os.MkdirAll(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			p := startPly(t, base, bin, tmp, "-movies", filepath.Join(media, "Movies"), "-shows", filepath.Join(media, "Shows"),
				"-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-name", e2eName, "-scan-interval", "0", "-hwaccel", "none",
				"-ffmpeg", fake)
			c := &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 60 * time.Second}}
			bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
			c.waitForScan(3)
			var items []item
			_, body := c.do("GET", "/api/items", "", bearer)
			mustJSON(t, body, &items)
			var film, episode item
			for _, it := range items {
				switch {
				case it.Title == "Subbed":
					film = it
				case it.Kind == "episode":
					episode = it
				}
			}
			if film.ID == 0 || episode.ID == 0 {
				t.Fatalf("no film with subtitles, or no episode, in %s", body)
			}

			// The scan queued the background extraction of the film's track: an ffmpeg (here a sleep).
			sleepers := func() []procInfo { return p.children("sleep") }
			waitUntil(t, "the background extraction to start", 30*time.Second, func() bool { return len(sleepers()) == 1 })

			// A viewer asks for the container's track (joins that run) and for a sidecar (a run of its own);
			// neither answer ever comes, as the fake ffmpeg never writes.
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			for _, id := range []int64{film.ID, episode.ID} {
				_, body := c.do("GET", fmt.Sprintf("/api/items/%d", id), "", bearer)
				var det detail
				mustJSON(t, body, &det)
				if len(det.Subtitles) == 0 || det.Subtitles[0].URL == "" {
					t.Fatalf("item %d lists no subtitle: %s", id, body)
				}
				req, err := http.NewRequestWithContext(ctx, "GET", c.base+det.Subtitles[0].URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					if resp, err := http.DefaultClient.Do(req); err == nil {
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
				}()
			}
			waitUntil(t, "the viewer's two extractions to be going", 30*time.Second, func() bool { return len(sleepers()) == 2 })
			ffmpegs := sleepers()
			for _, f := range ffmpegs {
				if !processAlive(f.pid) || f.pgid != f.pid {
					t.Fatalf("positive control: %v is not alive and leading a group of its own", f)
				}
			}
			if runtime.GOOS == "darwin" { // one reaper each, detached
				if rs := reapers(t, p.pid); len(rs) != len(ffmpegs) {
					t.Fatalf("positive control: %d reapers for %d extractions: %v", len(rs), len(ffmpegs), rs)
				}
			}
			if v.stopped {
				stopGroups(t, ffmpegs)
			} else {
				for _, f := range ffmpegs {
					_ = syscall.Kill(-f.pgid, syscall.SIGCONT)
				}
				for _, f := range ffmpegs {
					if s := stateOf(f.pid); s == "" || strings.HasPrefix(s, "T") {
						t.Fatalf("positive control: extraction %d is %q, want it running", f.pid, s)
					}
				}
			}

			killed := time.Now()
			if err := p.cmd.Process.Kill(); err != nil { // SIGKILL: no handler, no cleanup
				t.Fatal(err)
			}
			<-p.exited
			took := waitDead(t, ffmpegs, killWithin, killed)
			t.Logf("%s: both extractions gone %v after kill -9 of ply", v.name, took.Round(time.Millisecond))

			deadline := time.Now().Add(3 * time.Second)
			for len(mentioning(t, dir)) != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("processes still reference %s: %v", dir, mentioning(t, dir))
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
