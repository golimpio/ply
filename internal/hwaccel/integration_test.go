//go:build integration

package hwaccel

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The real probe against the ffmpeg on PATH. On a Mac the VideoToolbox
// encoder must be found and must really encode; elsewhere the answer depends
// on the machine, so only its consistency is checked.
func TestDetectRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	start := time.Now()
	info := Detect(t.Context(), Options{})
	t.Logf("encoder %s (hardware=%v) in %v", info.Encoder.Name, info.Encoder.Hardware(), time.Since(start).Round(time.Millisecond))
	for _, l := range info.Probe {
		t.Log("  " + l)
	}
	if runtime.GOOS == "darwin" && info.Encoder.Name != "h264_videotoolbox" {
		t.Errorf("on macOS want h264_videotoolbox, got %s: %v", info.Encoder.Name, info.Probe)
	}
	if len(info.Probe) == 0 || len(info.Filters) == 0 {
		t.Errorf("nothing probed: %+v", info)
	}
	if !info.Filters["scale"] || !info.Encoder.Hardware() && info.Encoder.Name != "libx264" {
		t.Errorf("filters/encoder inconsistent: %+v", info)
	}

	// -hwaccel none never uses a GPU, even where one works.
	none := Detect(t.Context(), Options{Mode: "none"})
	if none.Encoder.Hardware() || none.Encoder.Name != "libx264" || !strings.Contains(strings.Join(none.Probe, " "), "disabled") {
		t.Errorf("none = %+v", none)
	}

	// The chosen plan encodes real frames: run the planned encoder and
	// filter chain over a generated 10-bit clip.
	for _, mode := range []Info{info, info.SoftwareOnly()} {
		spec := mode.Plan(Request{Codec: "hevc", BitDepth: 10, Width: 1280, Height: 720, BurnSubtitle: -1, MaxHeight: 360})
		src := t.TempDir() + "/in.mkv"
		if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24",
			"-t", "2", "-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error", src).CombinedOutput(); err != nil {
			t.Fatalf("source: %v\n%s", err, out)
		}
		args := []string{"-nostdin", "-v", "error", "-y"}
		args = append(args, spec.Pre...)
		args = append(args, "-i", src)
		if spec.Filter != "" {
			args = append(args, "-vf", spec.Filter)
		}
		args = append(args, spec.Encode...)
		args = append(args, "-f", "null", "-")
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Errorf("%s: ffmpeg %s: %v\n%s", spec.Encoder, strings.Join(args, " "), err, out)
		}
	}
}
