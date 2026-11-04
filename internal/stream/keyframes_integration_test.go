//go:build integration

package stream

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kfCase is a file ffmpeg (or mkvmerge) writes, to check the keyframe index
// against the exact ffprobe packet scan.
type kfCase struct {
	name  string
	ext   string
	delay string   // -itsoffset for the video input
	secs  string   // the length of the media in seconds; the default is 60
	codec []string // codec options; the default is H.264 + AAC
	args  []string // the rest of the output options
	from  string   // the case whose file this one is made from, instead of the test sources
	mux   string   // "mkvmerge": remux `from` with it
	cut   bool     // stream-copy from 5.3 s on (a cut inside a GOP)

	source   string // where the index should come from
	fallback bool   // falling back to the scan is as good as an index
}

var (
	h264aac = []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k"}
	gop48   = []string{"-g", "48", "-keyint_min", "48", "-sc_threshold", "0"}
)

func kfInputs(delay, secs string) []string {
	v := []string{"-f", "lavfi", "-i", "testsrc2=size=480x270:rate=24"}
	if delay != "" {
		v = append([]string{"-itsoffset", delay}, v...)
	}
	if secs == "" {
		secs = "60"
	}
	return append(v, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", secs, "-map", "0:v", "-map", "1:a")
}

func hasEncoder(name string) bool {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	return err == nil && strings.Contains(string(out), " "+name+" ")
}

// generate writes the file of c in dir; made maps case names to their files.
func (c kfCase) generate(t *testing.T, dir string, made map[string]string) string {
	t.Helper()
	out := filepath.Join(dir, c.name+"."+c.ext)
	var cmd *exec.Cmd
	switch {
	case c.mux == "mkvmerge":
		cmd = exec.Command("mkvmerge", "-o", out, made[c.from])
	case c.cut:
		cmd = exec.Command("ffmpeg", "-v", "error", "-y", "-ss", "5.3", "-i", made[c.from], "-c", "copy", "-t", "40", out)
	default:
		codec := c.codec
		if codec == nil {
			codec = h264aac
		}
		args := append([]string{"-v", "error", "-y"}, kfInputs(c.delay, c.secs)...)
		args = append(append(args, codec...), c.args...)
		cmd = exec.Command("ffmpeg", append(args, out)...)
	}
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v\n%s", c.name, err, o)
	}
	made[c.name] = out
	return out
}

func kfCases() []kfCase {
	return []kfCase{
		{name: "mkv-g48", ext: "mkv", args: gop48, source: sourceCues},
		// 3 MB a second: a cluster of 5 s is 15 MB, of which a megabyte is read.
		{name: "mkv-24mbit", ext: "mkv", secs: "20", source: sourceCues,
			codec: []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k"},
			args:  append([]string{"-b:v", "24M", "-minrate", "24M", "-maxrate", "24M", "-bufsize", "24M", "-x264-params", "nal-hrd=cbr"}, gop48...)},
		{name: "mkv-bframes", ext: "mkv", args: append([]string{"-bf", "2"}, gop48...), source: sourceCues},
		{name: "mkv-irregular", ext: "mkv", source: sourceCues,
			args: []string{"-force_key_frames", "0,2,4,6.5,8,12.4,14,18.6,20,25,28,35,41.2,50", "-sc_threshold", "0"}},
		{name: "mkv-start-1.4", ext: "mkv", args: append([]string{"-bf", "2", "-output_ts_offset", "1.4"}, gop48...), source: sourceCues},
		{name: "mkv-video-delayed", ext: "mkv", delay: "0.5", args: gop48, source: sourceCues},
		{name: "mkv-cut-copy", ext: "mkv", from: "mkv-bframes", cut: true, source: sourceCues, fallback: true},
		{name: "mkv-from-mkvmerge", ext: "mkv", from: "mp4-bframes-faststart", mux: "mkvmerge", source: sourceCues},

		{name: "mp4-faststart", ext: "mp4", args: append([]string{"-movflags", "+faststart"}, gop48...), source: sourceSampleTable},
		{name: "mp4-moov-at-end", ext: "mp4", args: gop48, source: sourceSampleTable},
		{name: "mp4-bframes-faststart", ext: "mp4", args: append([]string{"-bf", "2", "-movflags", "+faststart"}, gop48...), source: sourceSampleTable},
		{name: "mp4-bframes-no-editlist", ext: "mp4", args: append([]string{"-bf", "2", "-use_editlist", "0"}, gop48...), source: sourceSampleTable},
		{name: "mp4-start-1.4", ext: "mp4", args: append([]string{"-bf", "2", "-output_ts_offset", "1.4"}, gop48...), source: sourceSampleTable},
		{name: "mp4-video-delayed", ext: "mp4", delay: "0.5", args: append([]string{"-bf", "2"}, gop48...), source: sourceSampleTable},
		{name: "mp4-irregular", ext: "mp4", source: sourceSampleTable,
			args: []string{"-force_key_frames", "0,2,4,6.5,8,12.4,14,18.6,20,25,28,35,41.2,50", "-sc_threshold", "0"}},
		{name: "mp4-cut-copy", ext: "mp4", from: "mp4-bframes-faststart", cut: true, source: sourceSampleTable, fallback: true},
		{name: "mov", ext: "mov", args: append([]string{"-bf", "2"}, gop48...), source: sourceSampleTable},
		{name: "webm-vp9", ext: "webm", source: sourceCues,
			codec: []string{"-c:v", "libvpx-vp9", "-deadline", "realtime", "-cpu-used", "8", "-g", "48", "-c:a", "libopus"}},
	}
}

// The index of every kind of file agrees with the exact packet scan: the same
// keyframes within a millisecond, the same plan, and a few reads of the file.
func TestKeyframeIndexMatchesScan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	made := map[string]string{}
	// Cases made from others come after them in the list, except the
	// mkvmerge one, which needs the MP4: generate in dependency order.
	var ordered []kfCase
	for _, c := range kfCases() {
		if c.from == "" {
			ordered = append(ordered, c)
		}
	}
	for _, c := range kfCases() {
		if c.from != "" {
			ordered = append(ordered, c)
		}
	}

	for _, c := range ordered {
		t.Run(c.name, func(t *testing.T) {
			if c.mux == "mkvmerge" {
				if _, err := exec.LookPath("mkvmerge"); err != nil {
					t.Skip("mkvmerge is not installed: the libmatroska layout is only covered by the hand-built files in kfmkv_test.go")
				}
			}
			if strings.HasPrefix(c.name, "webm") && !hasEncoder("libvpx-vp9") {
				t.Skip("no libvpx-vp9 encoder")
			}
			path := c.generate(t, dir, made)
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			scan, err := ExtractKeyframes(ctx, "ffprobe", path, 0)
			if err != nil {
				t.Fatal(err)
			}

			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			cr := &countingReaderAt{r: f}
			index, source, err := indexKeyframes(ctx, cr, fi.Size(), 0)
			if err != nil {
				if c.fallback {
					t.Logf("%s: no index (%v): the scan is used, as it should for this file", c.name, err)
					return
				}
				t.Fatalf("no index: %v", err)
			}
			if source != c.source {
				t.Errorf("source %q, want %q", source, c.source)
			}
			t.Logf("%s: %d keyframes from the %s; read %d bytes in %d reads of a %d byte file (%.2f%%)",
				c.name, len(index), source, cr.bytes, cr.reads, fi.Size(), 100*float64(cr.bytes)/float64(fi.Size()))

			// Exactly the keyframes of the scan.
			if len(index) != len(scan) {
				t.Fatalf("%d keyframes, the scan has %d\nindex %v\nscan  %v", len(index), len(scan), index, scan)
			}
			for i := range scan {
				if math.Abs(index[i]-scan[i]) > 0.001 {
					t.Errorf("keyframe %d at %.6f, the scan has %.6f", i, index[i], scan[i])
				}
			}

			// A plan from the index is the plan from the scan.
			duration := scan[len(scan)-1] + 2
			pi, err1 := NewPlan(index, duration, 6)
			ps, err2 := NewPlan(scan, duration, 6)
			if err1 != nil || err2 != nil {
				t.Fatalf("plans: %v, %v", err1, err2)
			}
			if len(pi.Segments) != len(ps.Segments) {
				t.Fatalf("%d segments from the index, %d from the scan", len(pi.Segments), len(ps.Segments))
			}
			for i, s := range ps.Segments {
				g := pi.Segments[i]
				if g.First != s.First || g.End != s.End || math.Abs(g.Start-s.Start) > 0.001 || math.Abs(g.Duration-s.Duration) > 0.002 {
					t.Errorf("segment %d: %+v from the index, %+v from the scan", i, g, s)
				}
			}

			// A few reads, and a small part of the file. The Matroska reader takes the
			// window at the start, the head of the next cluster and the Cues: three
			// reads (each is a round trip on a network share) and 200 kB, whatever the
			// size of the file.
			limit, reads := fi.Size()/20, 3
			if source == sourceCues {
				limit = 200 << 10
			}
			if cr.bytes > limit {
				t.Errorf("read %d bytes of a %d byte file: more than %d", cr.bytes, fi.Size(), limit)
			}
			if cr.reads > reads {
				t.Errorf("%d reads, want at most %d", cr.reads, reads)
			}
			if fi.Size() < 2<<20 {
				t.Errorf("fixture is only %d bytes: too small for the percentage to mean anything", fi.Size())
			}

			// And through the front door.
			kf, err := Keyframes(ctx, "/nonexistent/ffprobe", path, 0)
			if err != nil {
				t.Fatalf("Keyframes without an ffprobe: %v", err)
			}
			if len(kf) != len(index) {
				t.Errorf("Keyframes: %d, index: %d", len(kf), len(index))
			}
		})
	}
}

// Cues too sparse to be a keyframe index (here an honest 15 s GOP: the median
// gap is over the limit, a little over that of x264's default 250 frames, 10.4
// s) send the file to the scan, which finds the same keyframes; and the dense
// file of the same content does not.
func TestKeyframeIndexSparseCuesFallBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	made := map[string]string{}
	long := kfCase{name: "mkv-gop360", ext: "mkv", args: []string{"-g", "360", "-keyint_min", "360", "-sc_threshold", "0"}}
	default250 := kfCase{name: "mkv-gop250", ext: "mkv", args: []string{"-g", "250", "-keyint_min", "250", "-sc_threshold", "0"}}
	if r, err := keyframesVia(ctx, "ffprobe", default250.generate(t, dir, made), 0, -1); err != nil || r.source != sourceCues {
		t.Errorf("x264's default GOP of 250 frames (10.4 s): source %q (%v), err %v", r.source, r.skip, err)
	}
	dense := kfCase{name: "mkv-gop48", ext: "mkv", args: gop48}
	lp, dp := long.generate(t, dir, made), dense.generate(t, dir, made)

	r, err := keyframesVia(ctx, "ffprobe", lp, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ExtractKeyframes(ctx, "ffprobe", lp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.source != sourceScan || r.skip == nil || len(r.times) != len(scan) {
		t.Errorf("15 s GOP: source %q (%v), %d keyframes, scan has %d", r.source, r.skip, len(r.times), len(scan))
	}
	t.Logf("15 s GOP: %d keyframes, index refused: %v", len(scan), r.skip)

	r, err = keyframesVia(ctx, "ffprobe", dp, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	scan, _ = ExtractKeyframes(ctx, "ffprobe", dp, 0)
	if r.source != sourceCues || len(r.times) != len(scan) {
		t.Errorf("2 s GOP: source %q (%v), %d keyframes, scan has %d", r.source, r.skip, len(r.times), len(scan))
	}
}

// A session for a remuxed file takes the index from the container, says so in
// the log, and never starts ffprobe: the manager's ffprobe does not exist.
func TestKeyframeIndexInTheManager(t *testing.T) {
	dir := t.TempDir()
	made := map[string]string{}
	for _, c := range []struct {
		kfCase
		log, container, vcodec string
	}{
		{kfCase{name: "m-mkv", ext: "mkv", args: append([]string{"-bf", "2"}, gop48...)}, "keyframe index read from cues", "mkv", "h264"},
		{kfCase{name: "m-mp4", ext: "mp4", args: append([]string{"-bf", "2"}, gop48...)}, "keyframe index read from sample table", "mp4", "h264"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.generate(t, dir, made)
			var logs syncBuffer
			cache := &recCache{}
			m := newManager(t, func(o *Options) {
				o.FFprobe = "/nonexistent/ffprobe"
				o.Cache = cache
				o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			s, err := m.Create(context.Background(), Request{ItemID: 5, Path: path, Container: c.container, DurationSec: 60,
				VideoIndex: 0, VideoCodec: c.vcodec, AudioIndex: 1, AudioCodec: "aac", AudioCopy: true})
			if err != nil {
				t.Fatalf("%v\nlog: %s", err, logs.String())
			}
			if n := len(s.Plan().Keyframes); n != 30 {
				t.Errorf("%d keyframes in the plan, want 30", n)
			}
			// What ffmpeg writes confirms the plan, and then it is cached.
			if pl := playlistOf(t, s); strings.Count(pl, "#EXTINF") != len(s.Plan().Segments) {
				t.Fatalf("playlist:\n%s", pl)
			}
			waitFor(t, "the keyframes to be cached", 5*time.Second, func() bool { return len(cache.of(5)) == 1 })
			if w := cache.of(5)[0]; len(w) != 30 {
				t.Errorf("cached %d keyframes", len(w))
			}
			if !strings.Contains(logs.String(), "confirmed by the first fragments") {
				t.Errorf("the log does not say the index was confirmed:\n%s", logs.String())
			}
			line := ""
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, c.log) {
					line = l
				}
			}
			if line == "" || !strings.Contains(line, "took=") || !strings.Contains(line, "keyframes=30") {
				t.Errorf("no %q line with the time taken in the log:\n%s", c.log, logs.String())
			}
			t.Log(strings.TrimSpace(line))
		})
	}
}

// Every fragment ffmpeg writes is compared with the plan for the whole session,
// the runs that start after a seek included: playing a file through, and
// jumping about in it, never fails a session whose index is right.
func TestKeyframeIndexSessionPlaysThrough(t *testing.T) {
	dir := t.TempDir()
	made := map[string]string{}
	for _, c := range []struct {
		kfCase
		container string
	}{
		{kfCase{name: "p-mkv", ext: "mkv", args: append([]string{"-bf", "2"}, gop48...)}, "mkv"},
		{kfCase{name: "p-mp4", ext: "mp4", args: append([]string{"-bf", "2"}, gop48...)}, "mp4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.generate(t, dir, made)
			var logs syncBuffer
			cache := &recCache{}
			m := newManager(t, func(o *Options) {
				o.FFprobe = "/nonexistent/ffprobe"
				o.Cache = cache
				o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			s, err := m.Create(context.Background(), Request{ItemID: 5, Path: path, Container: c.container, DurationSec: 60,
				VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true})
			if err != nil {
				t.Fatal(err)
			}
			playlistOf(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			n := len(s.Plan().Segments)
			for _, idx := range append(append([]int{}, seq(n)...), n-2, 1, n/2, n-1, 0) {
				md, err := s.OpenSegment(ctx, idx)
				if err != nil {
					t.Fatalf("segment %d: %v\nlog: %s", idx, err, logs.String())
				}
				md.Close()
			}
			time.Sleep(300 * time.Millisecond)
			if _, err := s.OpenSegment(ctx, 0); err != nil {
				t.Errorf("the session failed after it was played: %v", err)
			}
			if l := logs.String(); strings.Contains(l, "disagrees") {
				t.Errorf("a right index was found wrong:\n%s", l)
			}
			if w := cache.of(5); len(w) != 1 || len(w[0]) != 30 {
				t.Errorf("writes to the cache: %d", len(w))
			}
		})
	}
}

// With B-frames ffmpeg's fragments start a constant couple of frames after the
// keyframe times of the scan (0.083 s at 24 fps). A GOP shorter than the 0.25 s
// that is too much for the tolerance of its length is no reason to find the index
// wrong: not for GOPs of 5 frames, and not for one such GOP in the middle of a
// file of long ones, where it shows when the plan has been final for a while.
func TestKeyframeIndexBFramesAndShortGOPs(t *testing.T) {
	dir := t.TempDir()
	made := map[string]string{}
	forced := "0,2,4,6,8,8.1,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38"
	for _, c := range []struct {
		kfCase
		container string
	}{
		{kfCase{name: "gop5-mkv", ext: "mkv", secs: "20", args: []string{"-bf", "2", "-g", "5", "-keyint_min", "5", "-sc_threshold", "0"}}, "mkv"},
		{kfCase{name: "gop5-mp4", ext: "mp4", secs: "20", args: []string{"-bf", "2", "-g", "5", "-keyint_min", "5", "-sc_threshold", "0"}}, "mp4"},
		{kfCase{name: "one-short-gop-mkv", ext: "mkv", secs: "40", args: []string{"-bf", "2", "-sc_threshold", "0", "-force_key_frames", forced}}, "mkv"},
		{kfCase{name: "one-short-gop-mp4", ext: "mp4", secs: "40", args: []string{"-bf", "2", "-sc_threshold", "0", "-force_key_frames", forced}}, "mp4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.generate(t, dir, made)
			scan, err := ExtractKeyframes(context.Background(), "ffprobe", path, 0)
			if err != nil {
				t.Fatal(err)
			}
			var dur float64
			fmt.Sscan(c.secs, &dur)
			var logs syncBuffer
			cache := &recCache{}
			m := newManager(t, func(o *Options) {
				o.FFprobe = "/nonexistent/ffprobe"
				o.Cache = cache
				o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			s, err := m.Create(context.Background(), Request{ItemID: 5, Path: path, Container: c.container, DurationSec: dur,
				VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true})
			if err != nil {
				t.Fatal(err)
			}
			if s.guard == nil || len(s.Plan().Keyframes) != len(scan) {
				t.Fatalf("%d keyframes in the plan, the scan has %d (a guard: %v)", len(s.Plan().Keyframes), len(scan), s.guard != nil)
			}
			playlistOf(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			n := len(s.Plan().Segments)
			for _, idx := range append(seq(n), n-1, 0, n/2, 1) {
				md, err := s.OpenSegment(ctx, idx)
				if err != nil {
					t.Fatalf("segment %d: %v\nlog: %s", idx, err, logs.String())
				}
				md.Close()
			}
			time.Sleep(300 * time.Millisecond)
			if l := logs.String(); strings.Contains(l, "disagrees") || !strings.Contains(l, "confirmed by") {
				t.Errorf("a good index was found wrong, or not confirmed:\n%s", l)
			}
			if w := cache.of(5); len(w) != 1 || len(w[0]) != len(scan) {
				t.Errorf("writes to the cache: %d", len(w))
			}
		})
	}
}

// seq is 0, 1, ... n-1.
func seq(n int) []int {
	v := make([]int, n)
	for i := range v {
		v[i] = i
	}
	return v
}

func ffmpegRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// ffmpegMKV writes an mkv of 60 s with a keyframe every 2 s and returns its path.
func ffmpegMKV(t *testing.T, name string, extra ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	args := append(kfInputs("", ""), h264aac...)
	ffmpegRun(t, append(append(append(args, gop48...), extra...), p)...)
	return p
}

// topLevel calls fn for each top-level element of the Segment of an mkv file.
func topLevel(t *testing.T, in []byte, fn func(id uint32, pos, n int, size int64)) {
	t.Helper()
	_, size, n, err := parseEBMLHeader(in)
	if err != nil {
		t.Fatal(err)
	}
	off := n + int(size)
	if _, _, n, err = parseEBMLHeader(in[off : off+12]); err != nil {
		t.Fatal(err)
	}
	for pos := off + n; pos < len(in); {
		id, size, n, err := parseEBMLHeader(in[pos:min(pos+12, len(in))])
		if err != nil {
			t.Fatal(err)
		}
		fn(id, pos, n, size)
		pos += n + int(size)
	}
}

// voidOf is a Void element of exactly g bytes (or nothing for 0).
func voidOf(g int) []byte {
	switch {
	case g == 0:
		return nil
	case g-2 <= 126:
		return append([]byte{0xEC, 0x80 | byte(g-2)}, make([]byte, g-2)...)
	}
	p := g - 3
	return append([]byte{0xEC, 0x40 | byte(p>>8), byte(p)}, make([]byte, p)...)
}

// pruneCues keeps the cue points of an mkv file that keep says to (by their
// number), padding the Cues with a Void so that nothing moves.
func pruneCues(t *testing.T, in []byte, keep func(i int) bool) []byte {
	t.Helper()
	out := append([]byte(nil), in...)
	topLevel(t, in, func(id uint32, pos, n int, size int64) {
		if id != idCues {
			return
		}
		body := in[pos+n : pos+n+int(size)]
		var kept []byte
		for i := 0; len(body) > 0; i++ {
			_, s, hn, err := parseEBMLHeader(body)
			if err != nil {
				t.Fatal(err)
			}
			if keep(i) {
				kept = append(kept, body[:hn+int(s)]...)
			}
			body = body[hn+int(s):]
		}
		copy(out[pos+n:], append(kept, voidOf(int(size)-len(kept))...))
	})
	return out
}

// everyOtherCue keeps the CRC and the cue points of the keyframes 0, 2, 4, ...
func everyOtherCue(i int) bool { return i == 0 || i%2 == 1 }

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Cues that leave out the second keyframe of the file are not an index: the
// file goes to the scan, which finds the keyframes there are (E1 of the review).
// The head of the second cluster is where that shows. Cues that add keyframes
// (E1b) are not seen from the start of the file; the first fragment is.
func TestKeyframeIndexRejectsCuesThatDisagreeWithTheBlocks(t *testing.T) {
	ctx := context.Background()
	src := ffmpegMKV(t, "src.mkv")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := keyframesVia(ctx, "ffprobe", src, 0, -1); err != nil || r.source != sourceCues || len(r.times) != 30 {
		t.Fatalf("the file as ffmpeg wrote it: source %q (%v), %d keyframes, err %v", r.source, r.skip, len(r.times), err)
	}

	// Every other cue removed: 15 of the 30 keyframes. (The Cues of ffmpeg start
	// with a CRC-32, which is element 0; the cue points are 1 and on.)
	pruned := writeTemp(t, "pruned.mkv", pruneCues(t, data, everyOtherCue))
	r, err := keyframesVia(ctx, "ffprobe", pruned, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	scan, _ := ExtractKeyframes(ctx, "ffprobe", pruned, 0)
	if r.source != sourceScan || r.skip == nil || len(r.times) != 30 || len(scan) != 30 {
		t.Errorf("every other cue removed: source %q, %d keyframes (the scan has %d), index refused: %v", r.source, len(r.times), len(scan), r.skip)
	}
	t.Logf("every other cue removed: %v", r.skip)

	// A cue every half second, most of them on no keyframe (and pointing at no
	// cluster). The blocks at the start of the file do not show it: the first
	// keyframe is there, and the second, with its cue, is 2 s on.
	var cues [][]byte
	for ms := 0; ms < 60000; ms += 500 {
		cues = append(cues, el(idCuePoint, elUint(idCueTime, uint64(ms)), el(idCueTrackPositions, elUint(idCueTrack, 1), elUint(idCueClusterPosition, 0))))
	}
	var at int
	topLevel(t, data, func(id uint32, pos, n int, size int64) {
		if id == idCues {
			at = pos
		}
	})
	more := writeTemp(t, "more.mkv", append(append([]byte(nil), data[:at]...), el(idCues, cues...)...))
	if idx, source, err := readKeyframeIndex(ctx, more, 0); err != nil || source != sourceCues || len(idx) != 120 {
		t.Fatalf("a cue every half second: %d keyframes from %q, err %v: the blocks at the start should not tell", len(idx), source, err)
	}

	// The playlist is the scan's, all the same: the second fragment starts at 2 s,
	// and the plan has its GOP at half a second.
	var logs syncBuffer
	cache := &recCache{}
	m := newManager(t, func(o *Options) {
		o.Cache = cache
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	s, err := m.Create(ctx, Request{ItemID: 9, Path: more, Container: "mkv", DurationSec: 60,
		VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Plan().Keyframes); n != 120 {
		t.Fatalf("the plan from the cues has %d keyframes", n)
	}
	playlistOf(t, s)
	if n := len(s.Plan().Keyframes); n != 30 {
		t.Errorf("after the first fragment: %d keyframes, want the 30 of the scan", n)
	}
	if !strings.Contains(logs.String(), "gop=1") || !strings.Contains(logs.String(), "the fragment starts at") {
		t.Errorf("the second fragment should have shown it:\n%s", logs.String())
	}
}

// What the blocks read cannot tell is told by the first fragments of a run:
// here the cues of the odd keyframes from 16 s to 46 s are gone, in the middle
// of the file where neither the first clusters nor the last cluster show it,
// and the player starts at 20 s. The first two fragments are two keyframes 2 s
// apart, and the plan has them 4 s apart. The playlist the client gets is the
// one of the scan, and the index is not cached.
func TestKeyframeIndexRefutedByTheFirstFragments(t *testing.T) {
	data, err := os.ReadFile(ffmpegMKV(t, "src.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	// Element 0 is the CRC; the cue point i is the keyframe i-1.
	path := writeTemp(t, "pruned.mkv", pruneCues(t, data, func(i int) bool { k := i - 1; return i == 0 || k < 8 || k >= 24 || k%2 == 0 }))
	idx, source, err := readKeyframeIndex(context.Background(), path, 0)
	if err != nil || source != sourceCues || len(idx) != 22 {
		t.Fatalf("the blocks should not tell: %d keyframes from %q, err %v", len(idx), source, err)
	}

	var logs syncBuffer
	cache := &recCache{}
	m := newManager(t, func(o *Options) {
		o.Cache = cache
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	s, err := m.Create(context.Background(), Request{ItemID: 9, Path: path, Container: "mkv", DurationSec: 60, StartSec: 20,
		VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac", AudioCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(s.Plan().Keyframes); n != 22 {
		t.Fatalf("the plan from the cues has %d keyframes", n)
	}
	pl := playlistOf(t, s)
	plan := s.Plan()
	if len(plan.Keyframes) != 30 || strings.Count(pl, "#EXTINF") != len(plan.Segments) || len(plan.Segments) != 10 {
		t.Fatalf("after the guard: %d keyframes, %d segments:\n%s\nlog:\n%s", len(plan.Keyframes), len(plan.Segments), pl, logs.String())
	}
	if !strings.Contains(logs.String(), "disagrees with the stream") {
		t.Errorf("no word of it in the log:\n%s", logs.String())
	}
	if w := cache.of(9); len(w) != 2 || len(w[0]) != 0 || len(w[1]) != 30 {
		t.Errorf("writes to the cache: %v", w)
	}

	// And what is served is what the playlist says. (Not around 20 s: ffmpeg
	// seeks by the cues of this file, and these are damaged there, so that its
	// seek to the keyframe at 18 s lands on the one at 16.)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	initR, err := s.OpenInit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	init := readAll(t, initR)
	for _, idx := range []int{0, 1} {
		mr, err := s.OpenSegment(ctx, idx)
		if err != nil {
			t.Fatalf("segment %d: %v", idx, err)
		}
		v, _ := probeSpan(t, init, readAll(t, mr))
		seg := plan.Segments[idx]
		near(t, fmt.Sprintf("seg %d video start", idx), v.first, seg.Start+TimelineOffset, 0.1)
		near(t, fmt.Sprintf("seg %d video end", idx), v.end, seg.Start+seg.Duration+TimelineOffset, 0.1)
	}
}

// A subtitle track that starts long before the media is not where the file
// starts, one that starts less than a second before it is.
func TestKeyframeIndexTextTracks(t *testing.T) {
	ctx := context.Background()
	srt := writeTemp(t, "s.srt", []byte("1\n00:00:00,000 --> 00:00:02,000\nhello\n\n2\n00:00:10,000 --> 00:00:12,000\nworld\n"))
	x264 := []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-pix_fmt", "yuv420p", "-bf", "2", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-c:a", "aac", "-b:a", "96k"}
	for _, c := range []struct {
		name, ext, codec string
		media            []string // the inputs of the media, and what is done to them
		extra            []string
	}{
		// E7: everything is moved 1.4 s, the subtitle is at 0.
		{"mp4, mov_text, offset 1.4", "mp4", "mov_text", nil, []string{"-output_ts_offset", "1.4"}},
		{"mkv, srt, offset 1.4", "mkv", "srt", nil, []string{"-output_ts_offset", "1.4"}},
		// E8: the subtitle at 0, the media at 3 s.
		{"mp4, mov_text, media at 3 s", "mp4", "mov_text", []string{"-itsoffset", "3"}, nil},
		{"mkv, srt, media at 3 s", "mkv", "srt", []string{"-itsoffset", "3"}, nil},
		// ... and the media at 0.5 s, within a second of it.
		{"mkv, srt, media at 0.5 s", "mkv", "srt", []string{"-itsoffset", "0.5"}, nil},
		{"mp4, mov_text, media at 0.5 s", "mp4", "mov_text", []string{"-itsoffset", "0.5"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "t."+c.ext)
			args := []string{"-i", srt}
			args = append(args, c.media...)
			args = append(args, "-f", "lavfi", "-i", "testsrc2=size=480x270:rate=24")
			args = append(args, c.media...)
			args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "30",
				"-map", "1:v", "-map", "2:a", "-map", "0:0")
			args = append(append(append(args, x264...), "-c:s", c.codec), c.extra...)
			ffmpegRun(t, append(args, p)...)

			scan, err := ExtractKeyframes(ctx, "ffprobe", p, 0)
			if err != nil {
				t.Fatal(err)
			}
			index, _, err := readKeyframeIndex(ctx, p, 0)
			if err != nil {
				t.Fatalf("no index: %v", err)
			}
			if len(index) != len(scan) {
				t.Fatalf("%d keyframes, the scan has %d\nindex %v\nscan  %v", len(index), len(scan), head(index), head(scan))
			}
			for i := range scan {
				if math.Abs(index[i]-scan[i]) > 0.001 {
					t.Fatalf("keyframe %d at %.6f, the scan has %.6f", i, index[i], scan[i])
				}
			}
		})
	}
}

// The stream asked for is the one whose index is read, whichever it is: here
// the audio is stream 0 and the video stream 1, in a file of each kind.
func TestKeyframeIndexAudioFirstInTheManager(t *testing.T) {
	for _, ext := range []string{"mkv", "mp4"} {
		t.Run(ext, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "af."+ext)
			args := append([]string{"-f", "lavfi", "-i", "testsrc2=size=480x270:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
				"-t", "60", "-map", "1:a", "-map", "0:v"}, append(h264aac, append([]string{"-bf", "2"}, gop48...)...)...)
			ffmpegRun(t, append(args, p)...)
			cache := &recCache{}
			m := newManager(t, func(o *Options) {
				o.FFprobe = "/nonexistent/ffprobe"
				o.Cache = cache
			})
			s, err := m.Create(context.Background(), Request{ItemID: 4, Path: p, Container: ext, DurationSec: 60,
				VideoIndex: 1, VideoCodec: "h264", AudioIndex: 0, AudioCodec: "aac", AudioCopy: true})
			if err != nil {
				t.Fatalf("video stream 1: %v", err)
			}
			if pl := playlistOf(t, s); strings.Count(pl, "#EXTINF") != len(s.Plan().Segments) || len(s.Plan().Keyframes) != 30 {
				t.Fatalf("%d keyframes, playlist:\n%s", len(s.Plan().Keyframes), pl)
			}
			waitFor(t, "the keyframes to be cached", 5*time.Second, func() bool { return len(cache.of(4)) == 1 })
		})
	}
}
