// Package hwaccel chooses how video is transcoded: which H.264 encoder to use
// (a GPU encoder when one really works on this machine, libx264 otherwise)
// and the matching decode, scale and encode arguments for ffmpeg.
//
// Detect probes once at startup by running ffmpeg: what it lists as
// encoders, hwaccels and filters, and a real one-frame test encode per
// candidate, because a listed encoder says nothing about a usable GPU.
package hwaccel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Kind identifies an encoder family.
type Kind string

// Encoder families, in the order Detect tries them per platform.
const (
	VideoToolbox Kind = "videotoolbox"
	VAAPI        Kind = "vaapi"
	QSV          Kind = "qsv"
	NVENC        Kind = "nvenc"
	Software     Kind = "software"
)

// DefaultVAAPIDevice is the render node tried for VAAPI and QSV.
const DefaultVAAPIDevice = "/dev/dri/renderD128"

// Encoder is one H.264 encoder ffmpeg can drive.
type Encoder struct {
	Kind   Kind
	Name   string // ffmpeg encoder name
	Device string // render node, for VAAPI and QSV
}

// Hardware reports whether the encoder runs on a GPU.
func (e Encoder) Hardware() bool { return e.Kind != Software && e.Kind != "" }

// SoftwareEncoder is libx264, which every ffmpeg build has.
func SoftwareEncoder() Encoder { return Encoder{Kind: Software, Name: "libx264"} }

// RefScale is the pseudo filter name under which Info.Filters records that
// this ffmpeg's scale filter takes a second, reference input (scale=rw:rh),
// the successor of the deprecated scale2ref.
const RefScale = "scale:ref"

// Info is what the probe found. The zero value means "software only, nothing
// known about this ffmpeg's filters".
type Info struct {
	Encoder Encoder
	// Filters and HWAccels are the filter and hwaccel names ffmpeg lists.
	Filters  map[string]bool
	HWAccels map[string]bool
	// Probe is a human-readable account of every candidate tried.
	Probe []string
}

// SoftwareOnly returns i with the encoder forced to libx264, keeping what is
// known about the filters. Used as the fallback when a GPU run fails.
func (i Info) SoftwareOnly() Info {
	i.Encoder = SoftwareEncoder()
	return i
}

func (i Info) encoder() Encoder {
	if i.Encoder.Name == "" {
		return SoftwareEncoder()
	}
	return i.Encoder
}

// Options configures Detect.
type Options struct {
	FFmpeg string // ["ffmpeg"]
	// Mode is "auto" (probe the platform's GPU encoders), "none" (libx264
	// only) or one of videotoolbox, vaapi, qsv, nvenc. [auto]
	Mode        string
	GOOS        string        // [runtime.GOOS]
	Timeout     time.Duration // bound on every probe command [10s]
	VAAPIDevice string        // [DefaultVAAPIDevice]
	Logger      *slog.Logger

	run  runner             // test seam
	stat func(string) error // test seam
}

type runner func(ctx context.Context, bin string, args ...string) ([]byte, error)

func execRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = time.Second
	return cmd.CombinedOutput()
}

func (o *Options) defaults() {
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Mode == "" {
		o.Mode = "auto"
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.VAAPIDevice == "" {
		o.VAAPIDevice = DefaultVAAPIDevice
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.run == nil {
		o.run = execRun
	}
	if o.stat == nil {
		o.stat = func(p string) error { _, err := os.Stat(p); return err }
	}
}

// ValidMode reports whether mode is a value for Options.Mode.
func ValidMode(mode string) bool {
	switch mode {
	case "auto", "none", string(VideoToolbox), string(VAAPI), string(QSV), string(NVENC):
		return true
	}
	return false
}

// Detect probes ffmpeg and returns the encoder to use. It never fails: if
// nothing else works the answer is libx264, and Info.Probe says why.
func Detect(ctx context.Context, o Options) Info {
	o.defaults()
	info := Info{Encoder: SoftwareEncoder()}
	note := func(format string, args ...any) { info.Probe = append(info.Probe, fmt.Sprintf(format, args...)) }

	encoders, err := o.list(ctx, "-encoders")
	if err != nil {
		note("cannot run %s: %v", o.FFmpeg, err)
		return info
	}
	info.HWAccels = parseHWAccels(o.output(ctx, "-hwaccels"))
	info.Filters = parseNames(o.output(ctx, "-filters"))
	if info.Filters["scale"] && strings.Contains(string(o.output(ctx, "-h", "filter=scale")), "dynamic (depending on the options)") {
		info.Filters[RefScale] = true
	}
	if !encoders["libx264"] {
		note("libx264 is not available in this ffmpeg build")
	}
	if o.Mode == "none" {
		note("hardware encoding disabled (-hwaccel none)")
		return info
	}

	for _, c := range o.candidates() {
		if !encoders[c.Name] {
			note("%s: not built into this ffmpeg", c.Name)
			continue
		}
		if c.Device != "" {
			if err := o.stat(c.Device); err != nil {
				note("%s: no device %s", c.Name, c.Device)
				continue
			}
		}
		start := time.Now()
		if out, err := o.testEncode(ctx, c); err != nil {
			note("%s: test encode failed: %s", c.Name, firstLine(out, err))
			continue
		}
		note("%s: test encode ok (%s)", c.Name, time.Since(start).Round(time.Millisecond))
		info.Encoder = c
		return info
	}
	note("no hardware encoder usable, using libx264")
	return info
}

// candidates are the encoders to try, best first.
func (o *Options) candidates() []Encoder {
	vt := Encoder{Kind: VideoToolbox, Name: "h264_videotoolbox"}
	va := Encoder{Kind: VAAPI, Name: "h264_vaapi", Device: o.VAAPIDevice}
	qsv := Encoder{Kind: QSV, Name: "h264_qsv", Device: o.VAAPIDevice}
	nv := Encoder{Kind: NVENC, Name: "h264_nvenc"}
	all := []Encoder{vt}
	if o.GOOS != "darwin" {
		all = []Encoder{va, qsv, nv}
	}
	if o.Mode == "auto" {
		return all
	}
	// Forcing an encoder skips the platform filter: the user knows better,
	// and the test encode still has to pass.
	for _, e := range []Encoder{vt, va, qsv, nv} {
		if string(e.Kind) == o.Mode {
			return []Encoder{e}
		}
	}
	return nil
}

// testEncode encodes one frame of lavfi testsrc with c, through the same
// kind of upload the real chain uses for software frames.
func (o *Options) testEncode(ctx context.Context, c Encoder) ([]byte, error) {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	var vf string
	switch c.Kind {
	case VAAPI:
		args = append(args, "-init_hw_device", "vaapi=va:"+c.Device, "-filter_hw_device", "va")
		vf = "format=nv12,hwupload"
	case QSV:
		args = append(args, "-init_hw_device", "qsv=hw", "-filter_hw_device", "hw")
		vf = "format=nv12,hwupload=extra_hw_frames=64"
	default:
		vf = "format=yuv420p"
	}
	args = append(args, "-f", "lavfi", "-i", "testsrc=size=1280x720:rate=1:duration=1",
		"-vf", vf, "-frames:v", "1", "-c:v", c.Name, "-f", "null", "-")
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	return o.run(ctx, o.FFmpeg, args...)
}

// list runs ffmpeg with a listing option and returns the names it prints.
func (o *Options) list(ctx context.Context, opt string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	out, err := o.run(ctx, o.FFmpeg, "-hide_banner", opt)
	if err != nil {
		return nil, err
	}
	return parseNames(out), nil
}

// output is list for the optional listings: a failure means "unknown".
func (o *Options) output(ctx context.Context, opts ...string) []byte {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	out, err := o.run(ctx, o.FFmpeg, append([]string{"-hide_banner"}, opts...)...)
	if err != nil {
		return nil
	}
	return out
}

// parseNames reads the "-encoders" / "-filters" tables: a line is a flags
// column, a name, then a description. Legend lines ("T.. = Timeline") have
// "=" in the name column.
func parseNames(out []byte) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] == "=" || strings.Trim(f[0], "-") == "" {
			continue
		}
		names[f[1]] = true
	}
	return names
}

// parseHWAccels reads "-hwaccels": a header, then one method per line.
func parseHWAccels(out []byte) map[string]bool {
	m := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		m[line] = true
	}
	return m
}

func firstLine(out []byte, err error) string {
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}

// Detector runs Detect in the background so the server can start listening
// at once; Info waits for the result.
type Detector struct {
	done chan struct{}
	info Info
}

// Start begins probing and returns immediately. The result is logged.
func Start(ctx context.Context, o Options) *Detector {
	d := &Detector{done: make(chan struct{})}
	log := o.Logger
	go func() {
		defer close(d.done)
		start := time.Now()
		d.info = Detect(ctx, o)
		if log != nil {
			log.Info("video encoder", "encoder", d.info.Encoder.Name, "hardware", d.info.Encoder.Hardware(),
				"mode", o.Mode, "took", time.Since(start).Round(time.Millisecond), "probe", strings.Join(d.info.Probe, "; "))
		}
	}()
	return d
}

// Static returns a Detector that already holds info (tests, "no probing").
func Static(info Info) *Detector {
	d := &Detector{done: make(chan struct{}), info: info}
	close(d.done)
	return d
}

// Info waits for the probe to finish and returns its result. A nil Detector
// means software encoding with no known filters.
func (d *Detector) Info() Info {
	if d == nil {
		return Info{Encoder: SoftwareEncoder()}
	}
	<-d.done
	return d.info
}
