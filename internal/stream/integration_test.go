//go:build integration

package stream

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	fixtureOnce sync.Once
	fixtureDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureDir != "" {
		os.RemoveAll(fixtureDir)
	}
	os.Exit(code)
}

// fixtures are generated once per test run: 30 s, keyframe every 2 s.
type fixture struct {
	name, file, container, vcodec, acodec string
	audioCopy                             bool
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	fixtureOnce.Do(func() {
		var err error
		if fixtureDir, err = os.MkdirTemp("", "ply-stream-fx-"); err != nil {
			panic(err)
		}
	})
	gen := func(file string, args ...string) string {
		p := filepath.Join(fixtureDir, file)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		base := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30"}
		out, err := exec.Command("ffmpeg", append(append(base, args...), p)...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate %s: %v\n%s", file, err, out)
		}
		return p
	}
	return []fixture{
		{"mkv-hevc-eac3-copy", gen("hevc.mkv", "-c:v", "libx265", "-preset", "ultrafast", "-x265-params",
			"keyint=48:min-keyint=48:scenecut=0:open-gop=0:log-level=error", "-pix_fmt", "yuv420p",
			"-c:a", "eac3", "-b:a", "128k", "-ac", "2"), "mkv", "hevc", "eac3", true},
		{"mkv-hevc-eac3-to-aac", filepath.Join(fixtureDir, "hevc.mkv"), "mkv", "hevc", "eac3", false},
		{"mp4-h264-aac-copy", gen("h264.mp4", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48",
			"-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k"),
			"mp4", "h264", "aac", true},
		// MPEG-TS cannot seek to a keyframe, and starts at a non-zero time.
		{"ts-h264-aac-copy", gen("h264.ts", "-c:v", "libx264", "-preset", "ultrafast", "-g", "48",
			"-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k",
			"-output_ts_offset", "1.4"), "ts", "h264", "aac", true},
	}
}

func newManager(t *testing.T, mod func(*Options)) *Manager {
	t.Helper()
	o := Options{TempRoot: t.TempDir(), Tick: 50 * time.Millisecond}
	if mod != nil {
		mod(&o)
	}
	m, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func request(f fixture) Request {
	return Request{Path: f.file, Container: f.container, DurationSec: 30, VideoIndex: 0,
		VideoCodec: f.vcodec, AudioIndex: 1, AudioCodec: f.acodec, AudioCopy: f.audioCopy}
}

func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// timing holds the presentation span of one stream in a segment.
type timing struct{ first, end float64 }

// probeSpan concatenates init and segment and reports the presentation span
// of the video and audio packets.
func probeSpan(t *testing.T, init, seg []byte) (video, audio timing) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(path, append(append([]byte{}, init...), seg...), 0o644); err != nil {
		t.Fatal(err)
	}
	span := func(sel string) timing {
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", sel,
			"-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatalf("ffprobe %s: %v", sel, err)
		}
		tm := timing{first: math.Inf(1), end: math.Inf(-1)}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			f := strings.Split(strings.TrimRight(line, ","), ",")
			if len(f) < 2 {
				continue
			}
			pts, err1 := strconv.ParseFloat(f[0], 64)
			dur, err2 := strconv.ParseFloat(f[1], 64)
			if err1 != nil || err2 != nil {
				continue
			}
			tm.first = math.Min(tm.first, pts)
			tm.end = math.Max(tm.end, pts+dur)
		}
		if math.IsInf(tm.first, 0) {
			t.Fatalf("no %s packets in segment", sel)
		}
		return tm
	}
	return span("v:0"), span("a:0")
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f (+-%.2f)", what, got, want, tol)
	}
}

func TestSegmentTimingsMatchPlaylist(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			m := newManager(t, nil)
			s, err := m.Create(context.Background(), request(f))
			if err != nil {
				t.Fatal(err)
			}
			plan := s.Plan()
			if len(plan.Segments) != 5 { // 30 s, keyframes every 2 s, >= 6 s segments
				t.Fatalf("segments = %d, want 5", len(plan.Segments))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			initR, err := s.OpenInit(ctx)
			if err != nil {
				t.Fatal(err)
			}
			init := readAll(t, initR)

			// Segment 0, then a far one (a seek: ffmpeg restarts there), then
			// a neighbour of the far one and the first again (seek back).
			var prev *timing
			for _, idx := range []int{0, 1, 4, 3, 0} {
				mr, err := s.OpenSegment(ctx, idx)
				if err != nil {
					t.Fatalf("segment %d: %v", idx, err)
				}
				v, a := probeSpan(t, init, readAll(t, mr))
				seg := plan.Segments[idx]
				t.Logf("seg %d: planned %.3f+%.3f; video %.3f..%.3f audio %.3f..%.3f (offset %.1f)",
					idx, seg.Start, seg.Duration, v.first, v.end, a.first, a.end, TimelineOffset)
				near(t, fmt.Sprintf("seg %d video start", idx), v.first, seg.Start+TimelineOffset, 0.1)
				near(t, fmt.Sprintf("seg %d video end", idx), v.end, seg.Start+seg.Duration+TimelineOffset, 0.1)
				near(t, fmt.Sprintf("seg %d audio start", idx), a.first, seg.Start+TimelineOffset, 0.3)
				near(t, fmt.Sprintf("seg %d audio end", idx), a.end, seg.Start+seg.Duration+TimelineOffset, 0.3)
				if prev != nil && idx == 1 {
					near(t, "seg 0 end meets seg 1 start", prev.end, v.first, 0.05)
				}
				if idx == 0 {
					prev = &v
				}
			}
		})
	}
}

// memCache is an in-memory KeyframeCache that counts writes.
type memCache struct {
	mu     sync.Mutex
	kf     map[int64][]float64
	writes int
}

func (c *memCache) Keyframes(_ context.Context, id int64) ([]float64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kf, ok := c.kf[id]
	return kf, ok, nil
}

func (c *memCache) SetKeyframes(_ context.Context, id int64, kf []float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kf == nil {
		c.kf = map[int64][]float64{}
	}
	c.kf[id] = kf
	c.writes++
	return nil
}

// Keyframes that are not evenly spaced: the playlist must still match what
// is served, which is what cutting at ffmpeg's own idea of 6 s would not do.
func TestIrregularKeyframes(t *testing.T) {
	fixtures(t)
	path := filepath.Join(fixtureDir, "irregular.mp4")
	out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30", "-c:v", "libx264",
		"-preset", "ultrafast", "-force_key_frames", "0,2,4,6.5,8,12.4,14,18.6,20,25,28", "-sc_threshold", "0",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", path).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	cache := &memCache{}
	m := newManager(t, func(o *Options) { o.Cache = cache })
	req := Request{ItemID: 7, Path: path, Container: "mp4", DurationSec: 30, VideoIndex: 0, VideoCodec: "h264",
		AudioIndex: 1, AudioCodec: "aac", AudioCopy: true}
	s, err := m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// The index of an mp4 comes from its sample tables, and is cached once the
	// first fragments ffmpeg writes have agreed with it, which the playlist waits for.
	playlistOf(t, s)
	waitFor(t, "the keyframes to be cached", 5*time.Second, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return cache.writes == 1
	})
	cache.mu.Lock()
	cached := len(cache.kf[7])
	cache.mu.Unlock()
	if cached != 11 {
		t.Fatalf("keyframes cached: %d times; want 11", cached)
	}
	plan := s.Plan()
	t.Logf("segments: %+v", plan.Segments)
	if n := len(plan.Segments); n != 4 {
		t.Fatalf("segments = %d, want 4 (the 2 s tail is merged)", n)
	}
	near(t, "first segment duration", plan.Segments[0].Duration, 6.5, 0.01)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initR, err := s.OpenInit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	init := readAll(t, initR)
	for _, idx := range []int{0, 2, 1, 3} { // out of order: seeks
		mr, err := s.OpenSegment(ctx, idx)
		if err != nil {
			t.Fatalf("segment %d: %v", idx, err)
		}
		v, _ := probeSpan(t, init, readAll(t, mr))
		seg := plan.Segments[idx]
		near(t, fmt.Sprintf("seg %d video start", idx), v.first, seg.Start+TimelineOffset, 0.1)
		near(t, fmt.Sprintf("seg %d video end", idx), v.end, seg.Start+seg.Duration+TimelineOffset, 0.1)
	}

	// A second session for the same item uses the cache instead of ffprobe.
	m2 := newManager(t, func(o *Options) { o.Cache = cache; o.FFprobe = "/nonexistent/ffprobe" })
	s2, err := m2.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("create with cached keyframes: %v", err)
	}
	if len(s2.Plan().Segments) != 4 {
		t.Errorf("cached plan differs: %d segments", len(s2.Plan().Segments))
	}
}

// ffmpegProcs lists running processes whose command line mentions dir and
// ffmpeg, whoever their parent is (an orphan is reparented to init).
func ffmpegProcs(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(line, "ffmpeg") && strings.Contains(line, dir) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

// The ffmpeg run in a session directory shows up with its cwd-relative
// arguments only, so processes are found by the input path instead.
func procsFor(t *testing.T, f fixture) []string { return ffmpegProcs(t, f.file) }

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// slowFixture is long enough that ffmpeg is still busy (paused, in fact)
// while the test looks at it.
func slowFixture(t *testing.T) fixture {
	t.Helper()
	fixtures(t)
	path := filepath.Join(fixtureDir, "long.mkv")
	if _, err := os.Stat(path); err != nil {
		out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1500", "-c:v", "libx264",
			"-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "64k", path).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	return fixture{name: "long", file: path, container: "mkv", vcodec: "h264", acodec: "aac", audioCopy: true}
}

func TestNoOrphanFFmpeg(t *testing.T) {
	f := slowFixture(t)
	req := request(f)
	req.DurationSec = 1500
	// Small thresholds so ffmpeg is paused (SIGSTOP) soon after it starts:
	// a stopped process is the hardest one to clean up.
	opts := func(o *Options) { o.ThrottleHigh, o.ThrottleLow = 2, 1; o.IdleTimeout = time.Hour }

	t.Run("delete", func(t *testing.T) {
		m := newManager(t, opts)
		s, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "ffmpeg running", 10*time.Second, func() bool { return len(procsFor(t, f)) > 0 }) // positive control
		dir := s.Dir()
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("session dir missing while running: %v", err)
		}
		if !m.Delete(s.ID) {
			t.Fatal("Delete found no session")
		}
		// Delete kills at once but waits for the exit StopWait at most: a real ffmpeg
		// sometimes takes longer than that to be gone (measured: 0.6 s).
		waitFor(t, "ffmpeg to be gone after DELETE", 10*time.Second, func() bool { return len(procsFor(t, f)) == 0 })
		waitFor(t, "the session to be let go of after DELETE", 10*time.Second, func() bool { return m.Count() == 0 })
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("session dir still exists after DELETE: %v", err)
		}
		if m.Delete(s.ID) {
			t.Error("second Delete reported a session")
		}
	})

	t.Run("idle", func(t *testing.T) {
		m := newManager(t, func(o *Options) { opts(o); o.IdleTimeout = 600 * time.Millisecond })
		s, err := m.Create(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "ffmpeg running", 10*time.Second, func() bool { return len(procsFor(t, f)) > 0 })
		dir := s.Dir()
		waitFor(t, "idle kill", 10*time.Second, func() bool { return m.Count() == 0 && len(procsFor(t, f)) == 0 })
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("session dir still exists after idle timeout: %v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		m := newManager(t, opts)
		var dirs []string
		for range 2 {
			s, err := m.Create(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, s.Dir())
		}
		waitFor(t, "two ffmpeg running", 10*time.Second, func() bool { return len(procsFor(t, f)) == 2 })
		m.Close()
		if p := procsFor(t, f); len(p) > 0 {
			t.Errorf("ffmpeg still running after Close: %v", p)
		}
		for _, d := range dirs {
			if _, err := os.Stat(d); !os.IsNotExist(err) {
				t.Errorf("session dir %s still exists after Close: %v", d, err)
			}
		}
		if _, err := os.Stat(m.Dir()); !os.IsNotExist(err) {
			t.Errorf("manager dir still exists after Close: %v", err)
		}
		if _, err := m.Create(context.Background(), req); err != ErrClosed {
			t.Errorf("Create after Close = %v, want ErrClosed", err)
		}
	})
}

// ffmpeg is paused when far ahead of the player and resumed when the player
// catches up.
func TestThrottle(t *testing.T) {
	f := slowFixture(t)
	req := request(f)
	req.DurationSec = 1500
	m := newManager(t, func(o *Options) { o.ThrottleHigh, o.ThrottleLow = 3, 1; o.IdleTimeout = time.Hour })
	s, err := m.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	state := func() string {
		p := procsFor(t, f)
		if len(p) != 1 {
			return "none"
		}
		pid := strings.Fields(p[0])[0]
		out, _ := exec.Command("ps", "-o", "state=", "-p", pid).Output()
		return strings.TrimSpace(string(out))
	}
	waitFor(t, "ffmpeg paused", 15*time.Second, func() bool { return strings.HasPrefix(state(), "T") })

	// Segment 0 was produced before the pause. The one past the produced
	// range needs new output, which a stopped process cannot make, so getting
	// it proves ffmpeg was resumed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.OpenSegment(ctx, 0); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	produced := s.plan.SegmentOfGOP(s.run.next)
	s.mu.Unlock()
	if produced < 4 {
		t.Fatalf("expected ffmpeg to be several segments ahead, got %d", produced)
	}
	mr, err := s.OpenSegment(ctx, produced+1) // beyond what was produced, within lookahead
	if err != nil {
		t.Fatal(err)
	}
	mr.Close()
}
