package hwaccel

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const encodersOut = `Encoders:
 V..... = Video
 A..... = Audio
 ------
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 V....D h264_videotoolbox    VideoToolbox H.264 Encoder (codec h264)
 V....D h264_vaapi           H.264/AVC (VAAPI) (codec h264)
 A....D aac                  AAC (Advanced Audio Coding)
`

const filtersOut = `Filters:
  T.. = Timeline support
  .S. = Slice threading
  ..C = Command support
  A = Audio input/output
  V = Video input/output
  N = Dynamic number and/or type of input/output
  | = Source or sink filter
 ... scale_vt          V->V       Scale Videotoolbox frames
 .S. tonemap           V->V       Conversion to/from different dynamic ranges.
 T.. zscale            V->V       Apply resizing, colorspace and bit depth conversions.
`

func TestParseNames(t *testing.T) {
	enc := parseNames([]byte(encodersOut))
	for _, want := range []string{"libx264", "h264_videotoolbox", "h264_vaapi", "aac"} {
		if !enc[want] {
			t.Errorf("encoder %q not parsed: %v", want, enc)
		}
	}
	for _, not := range []string{"=", "Video", "------", "Encoders:", "V....."} {
		if enc[not] {
			t.Errorf("legend line %q parsed as an encoder", not)
		}
	}
	f := parseNames([]byte(filtersOut))
	if !f["scale_vt"] || !f["tonemap"] || !f["zscale"] || f["="] || f["V"] || f["|"] {
		t.Errorf("filters = %v", f)
	}
	hw := parseHWAccels([]byte("Hardware acceleration methods:\nvideotoolbox\ncuda\n"))
	if !hw["videotoolbox"] || !hw["cuda"] || len(hw) != 2 {
		t.Errorf("hwaccels = %v", hw)
	}
}

// fakeFFmpeg answers the listing options and decides test encodes by
// encoder name; calls records every test-encode encoder tried.
type fakeFFmpeg struct {
	works map[string]bool
	calls []string
	slow  map[string]bool // test encodes that hang until the context ends
}

func (f *fakeFFmpeg) run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.HasSuffix(joined, "-encoders"):
		return []byte(encodersOut + " V....D h264_qsv  Intel QSV\n V....D h264_nvenc NVENC\n"), nil
	case strings.HasSuffix(joined, "-hwaccels"):
		return []byte("Hardware acceleration methods:\nvideotoolbox\n"), nil
	case strings.HasSuffix(joined, "-filters"):
		return []byte(filtersOut), nil
	}
	for i, a := range args {
		if a == "-c:v" {
			name := args[i+1]
			f.calls = append(f.calls, name)
			if f.slow[name] {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if f.works[name] {
				return nil, nil
			}
			return []byte("Error initializing the encoder\nsecond line"), errors.New("exit status 1")
		}
	}
	return nil, errors.New("unexpected ffmpeg call: " + joined)
}

func detect(t *testing.T, f *fakeFFmpeg, o Options) Info {
	t.Helper()
	o.run = f.run
	if o.stat == nil {
		o.stat = func(string) error { return nil }
	}
	return Detect(t.Context(), o)
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		mode      string
		works     map[string]bool
		noDevice  bool
		want      string
		wantCalls []string
		probe     string // substring of the probe notes
	}{
		{name: "mac uses videotoolbox", goos: "darwin", works: map[string]bool{"h264_videotoolbox": true},
			want: "h264_videotoolbox", wantCalls: []string{"h264_videotoolbox"}, probe: "test encode ok"},
		{name: "mac videotoolbox that does not encode falls back", goos: "darwin", works: map[string]bool{},
			want: "libx264", wantCalls: []string{"h264_videotoolbox"}, probe: "Error initializing the encoder"},
		{name: "none never tries a GPU", goos: "darwin", mode: "none", works: map[string]bool{"h264_videotoolbox": true},
			want: "libx264", probe: "disabled"},
		{name: "linux prefers vaapi", goos: "linux", works: map[string]bool{"h264_vaapi": true, "h264_qsv": true, "h264_nvenc": true},
			want: "h264_vaapi", wantCalls: []string{"h264_vaapi"}},
		{name: "linux order is vaapi qsv nvenc", goos: "linux", works: map[string]bool{"h264_nvenc": true},
			want: "h264_nvenc", wantCalls: []string{"h264_vaapi", "h264_qsv", "h264_nvenc"}},
		{name: "linux without a render node skips vaapi and qsv", goos: "linux", noDevice: true, works: map[string]bool{"h264_nvenc": true},
			want: "h264_nvenc", wantCalls: []string{"h264_nvenc"}, probe: "no device"},
		{name: "linux with nothing", goos: "linux", noDevice: true, works: map[string]bool{},
			want: "libx264", wantCalls: []string{"h264_nvenc"}},
		{name: "forcing nvenc on a mac still has to pass the test", goos: "darwin", mode: "nvenc", works: map[string]bool{"h264_nvenc": true},
			want: "h264_nvenc", wantCalls: []string{"h264_nvenc"}},
		{name: "forced encoder that fails", goos: "linux", mode: "qsv", works: map[string]bool{},
			want: "libx264", wantCalls: []string{"h264_qsv"}},
		{name: "auto on a mac ignores vaapi", goos: "darwin", works: map[string]bool{"h264_vaapi": true},
			want: "libx264", wantCalls: []string{"h264_videotoolbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFFmpeg{works: tt.works}
			o := Options{GOOS: tt.goos, Mode: tt.mode}
			if tt.noDevice {
				o.stat = func(string) error { return os.ErrNotExist }
			}
			info := detect(t, f, o)
			if info.Encoder.Name != tt.want {
				t.Errorf("encoder = %s, want %s\nprobe: %v", info.Encoder.Name, tt.want, info.Probe)
			}
			if info.Encoder.Hardware() != (tt.want != "libx264") {
				t.Errorf("Hardware() = %v for %s", info.Encoder.Hardware(), tt.want)
			}
			if strings.Join(f.calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("test encodes = %v, want %v", f.calls, tt.wantCalls)
			}
			if tt.probe != "" && !strings.Contains(strings.Join(info.Probe, "|"), tt.probe) {
				t.Errorf("probe notes %q lack %q", info.Probe, tt.probe)
			}
			if !info.Filters["tonemap"] || !info.HWAccels["videotoolbox"] {
				t.Errorf("filters/hwaccels not recorded: %v %v", info.Filters, info.HWAccels)
			}
		})
	}
}

func TestDetectTestEncodeTimesOut(t *testing.T) {
	f := &fakeFFmpeg{slow: map[string]bool{"h264_videotoolbox": true}}
	start := time.Now()
	info := detect(t, f, Options{GOOS: "darwin", Timeout: 150 * time.Millisecond})
	if info.Encoder.Name != "libx264" {
		t.Errorf("a hanging test encode must not win: %s", info.Encoder.Name)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("probe took %v", d)
	}
}

func TestDetectBrokenFFmpeg(t *testing.T) {
	info := Detect(t.Context(), Options{FFmpeg: "/nonexistent/ffmpeg"})
	if info.Encoder.Name != "libx264" || len(info.Probe) == 0 {
		t.Errorf("info = %+v", info)
	}
	// The software answer still plans (with nothing known about filters).
	if sp := info.Plan(Request{Codec: "h264", Height: 1080, BurnSubtitle: -1}); sp.Encoder != "libx264" {
		t.Errorf("plan = %+v", sp)
	}
}

func TestDetectorNil(t *testing.T) {
	var d *Detector
	if d.Info().Encoder.Name != "libx264" {
		t.Error("a nil Detector means software")
	}
	if got := Static(Info{Encoder: Encoder{Kind: VAAPI, Name: "h264_vaapi"}}).Info().Encoder.Name; got != "h264_vaapi" {
		t.Errorf("Static = %s", got)
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range []string{"auto", "none", "videotoolbox", "vaapi", "qsv", "nvenc"} {
		if !ValidMode(m) {
			t.Errorf("%s should be valid", m)
		}
	}
	for _, m := range []string{"", "gpu", "libx264", "cuda"} {
		if ValidMode(m) {
			t.Errorf("%s should be invalid", m)
		}
	}
}
