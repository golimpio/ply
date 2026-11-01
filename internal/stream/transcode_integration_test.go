//go:build integration

package stream

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ply/internal/hwaccel"
)

// transcodeVariants runs fn once per available encoder: libx264 always, and
// the GPU encoder this machine really has.
func transcodeVariants(t *testing.T, fn func(t *testing.T, info hwaccel.Info)) {
	t.Helper()
	detected := hwaccel.Detect(t.Context(), hwaccel.Options{})
	t.Run("libx264", func(t *testing.T) { fn(t, detected.SoftwareOnly()) })
	if detected.Encoder.Hardware() {
		t.Run(detected.Encoder.Name, func(t *testing.T) { fn(t, detected) })
	}
}

func generate(t *testing.T, name string, args ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	base := []string{"-nostdin", "-v", "error", "-y"}
	if out, err := exec.Command("ffmpeg", append(append(base, args...), p)...).CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v\n%s", name, err, out)
	}
	return p
}

// firstSegment creates a transcode session and returns segment idx with the
// init segment prepended, ready for ffprobe.
func segmentFile(t *testing.T, r Request, idx int) string {
	t.Helper()
	m := newManager(t, nil)
	s, err := m.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ir, err := s.OpenInit(ctx)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	init := readAll(t, ir)
	sr, err := s.OpenSegment(ctx, idx)
	if err != nil {
		t.Fatalf("segment %d: %v", idx, err)
	}
	p := filepath.Join(t.TempDir(), "seg.mp4")
	if err := os.WriteFile(p, append(init, readAll(t, sr)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func probeVideo(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,pix_fmt,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// An image-subtitle burn-in graph: the stand-in subtitle is a text stream, as
// no tool here can author a bitmap one, but the mapping, graph and encoder
// plumbing are the real thing.
func TestBurnInGraphRuns(t *testing.T) {
	srt := filepath.Join(t.TempDir(), "s.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:04,000\nBurned in\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := generate(t, "burn.mkv", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-i", srt,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "12", "-map", "0:v", "-map", "2:a", "-map", "1:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-c:a", "aac", "-c:s", "srt")

	transcodeVariants(t, func(t *testing.T, info hwaccel.Info) {
		spec := info.Plan(hwaccel.Request{Codec: "h264", BitDepth: 8, Width: 1280, Height: 720, VideoIndex: 0,
			BurnSubtitle: 2, MaxHeight: 360})
		if spec.Complex == "" || spec.Filter != "" || spec.GPU {
			t.Fatalf("spec = %+v", spec)
		}
		t.Logf("reasons: %q", spec.Reasons)
		r := Request{Path: src, Container: "mkv", DurationSec: 12, VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1,
			AudioCodec: "aac", AudioCopy: true,
			Video: &VideoSpec{Codec: "h264", Encode: spec.Encode, Pre: spec.Pre, Filter: spec.Filter, Complex: spec.Complex}}
		got := probeVideo(t, segmentFile(t, r, 1))
		if !strings.Contains(got, "h264") || !strings.Contains(got, "360") || !strings.Contains(got, "yuv420p") {
			t.Errorf("video = %q, want h264 360p yuv420p", got)
		}
	})
}

// A PQ-tagged 10-bit source (this ffmpeg may lack zscale): the plan converts
// it, says whether colours will be washed out, and the result plays.
func TestHDRSourceTranscodes(t *testing.T) {
	src := generate(t, "hdr.mkv", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-t", "12",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params",
		"keyint=48:min-keyint=48:scenecut=0:open-gop=0:log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc")

	transcodeVariants(t, func(t *testing.T, info hwaccel.Info) {
		spec := info.Plan(hwaccel.Request{Codec: "hevc", BitDepth: 10, Width: 1280, Height: 720, HDR: true,
			VideoIndex: 0, BurnSubtitle: -1, MaxHeight: 480})
		joined := strings.Join(spec.Reasons, "|")
		toneMapped := info.Filters["zscale"] && info.Filters["tonemap"]
		if toneMapped != strings.Contains(joined, "tone-mapped to SDR") || toneMapped == strings.Contains(joined, "washed out") {
			t.Errorf("tone mapping %v but reasons = %q", toneMapped, spec.Reasons)
		}
		t.Logf("zscale+tonemap available: %v; reasons: %q", toneMapped, spec.Reasons)
		r := Request{Path: src, Container: "mkv", DurationSec: 12, VideoIndex: 0, VideoCodec: "hevc", AudioIndex: -1,
			Video: &VideoSpec{Codec: "hevc", Encode: spec.Encode, Pre: spec.Pre, Filter: spec.Filter}}
		got := probeVideo(t, segmentFile(t, r, 1))
		if !strings.Contains(got, "h264") || !strings.Contains(got, "yuv420p") || !strings.Contains(got, "480") {
			t.Errorf("video = %q", got)
		}
	})
}
