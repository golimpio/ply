// Package art generates and caches artwork for library items: frame grabs
// taken from the media file itself, since ply has no online metadata source.
//
// Images are made lazily on first request, one ffmpeg run per image, at reduced
// priority and with bounded concurrency so artwork never competes with a stream.
// The same run also samples the frame at 16×9 to derive a dominant colour that
// the UI uses to tint the page.
package art

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind is an artwork variant.
type Kind string

const (
	Backdrop Kind = "backdrop" // wide hero image
	Thumb    Kind = "thumb"    // card image
)

// ContentType is the media type of every generated image.
const ContentType = "image/jpeg"

// ErrNoFrame reports that no image could be made from the file: it has no
// video stream, it is unreadable, or ffmpeg failed on it.
var ErrNoFrame = errors.New("art: no frame")

// ParseKind returns the Kind named s.
func ParseKind(s string) (Kind, bool) {
	switch k := Kind(s); k {
	case Backdrop, Thumb:
		return k, true
	}
	return "", false
}

func (k Kind) width() int {
	if k == Backdrop {
		return 1600
	}
	return 640
}

// Source identifies the media file to grab a frame from.
type Source struct {
	ID          int64
	Path        string // absolute
	MTime       int64  // unix nanoseconds; part of the cache key
	DurationSec float64
	HDR         bool
}

// Options configures New.
type Options struct {
	Dir         string        // cache directory, created if missing
	FFmpeg      string        // default "ffmpeg"
	Niceness    int           // unix nice value for ffmpeg; default 10
	Concurrency int           // simultaneous ffmpeg runs; default 2
	Timeout     time.Duration // per image; default 30s
}

// Generator creates artwork on demand and caches it on disk.
type Generator struct {
	dir      string
	ffmpeg   string
	niceness int
	timeout  time.Duration
	sem      chan struct{}
	toneMap  string // filter chain applied to HDR sources; "" when ffmpeg lacks zscale

	mu       sync.Mutex
	inflight map[string]*call
	failed   map[string]error // cache keys that could not be generated
	colors   map[int64]colour

	runs atomic.Int64
	// run executes ffmpeg with args and returns its stdout. Tests replace it.
	run func(ctx context.Context, args []string) ([]byte, error)
	// started, when set, is told the pid of every ffmpeg process (tests).
	started func(pid int)
}

type colour struct {
	mtime int64
	hex   string
}

type call struct {
	done chan struct{}
	err  error
}

// New returns a Generator caching under o.Dir.
func New(ctx context.Context, o Options) (*Generator, error) {
	if o.Dir == "" {
		return nil, errors.New("art: no cache directory")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, err
	}
	if o.FFmpeg == "" {
		o.FFmpeg = "ffmpeg"
	}
	if o.Niceness == 0 {
		o.Niceness = 10
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	g := &Generator{
		dir:      o.Dir,
		ffmpeg:   o.FFmpeg,
		niceness: o.Niceness,
		timeout:  o.Timeout,
		sem:      make(chan struct{}, o.Concurrency),
		inflight: map[string]*call{},
		failed:   map[string]error{},
		colors:   map[int64]colour{},
	}
	g.run = g.exec
	g.toneMap = g.detectToneMap(ctx)
	if err := g.loadColours(); err != nil {
		return nil, err
	}
	return g, nil
}

// Runs reports how many ffmpeg runs the generator has started.
func (g *Generator) Runs() int64 { return g.runs.Load() }

// Path returns the cached image of kind k for src, generating it first if
// needed. Concurrent requests for the same image share one ffmpeg run, and a
// file that failed once is not retried until its mtime changes.
func (g *Generator) Path(ctx context.Context, src Source, k Kind) (string, error) {
	name := filepath.Join(g.dir, imageName(src.ID, k, src.MTime))
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	key := filepath.Base(name)

	g.mu.Lock()
	if err, ok := g.failed[key]; ok {
		g.mu.Unlock()
		return "", err
	}
	c, ok := g.inflight[key]
	if !ok {
		c = &call{done: make(chan struct{})}
		g.inflight[key] = c
		// The work outlives the first request: a client navigating away must not
		// throw away an image the next request will want.
		go g.fill(context.WithoutCancel(ctx), c, key, src, k, name)
	}
	g.mu.Unlock()

	select {
	case <-c.done:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if c.err != nil {
		return "", c.err
	}
	return name, nil
}

func (g *Generator) fill(ctx context.Context, c *call, key string, src Source, k Kind, name string) {
	c.err = g.generate(ctx, src, k, name)
	g.mu.Lock()
	delete(g.inflight, key)
	if errors.Is(c.err, ErrNoFrame) {
		g.failed[key] = c.err
	}
	g.mu.Unlock()
	close(c.done)
}

// Colour returns the dominant colour ("#rrggbb") of src's artwork if an image
// for its current mtime has been generated.
func (g *Generator) Colour(src Source) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.colors[src.ID]
	if !ok || c.mtime != src.MTime {
		return "", false
	}
	return c.hex, true
}

// Prune deletes cached files of items that are gone or have changed. live maps
// each current item id to its mtime. It returns the number of files removed.
func (g *Generator) Prune(live map[int64]int64) (int, error) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") && recent(e) {
			continue // being written by an in-flight generation
		}
		if id, mtime, ok := parseName(e.Name()); ok {
			if m, current := live[id]; current && m == mtime {
				continue
			}
		}
		if err := os.Remove(filepath.Join(g.dir, e.Name())); err == nil {
			removed++
		}
	}
	g.mu.Lock()
	for id, c := range g.colors {
		if m, ok := live[id]; !ok || m != c.mtime {
			delete(g.colors, id)
		}
	}
	for key := range g.failed {
		if id, mtime, ok := parseName(key); !ok || live[id] != mtime {
			delete(g.failed, key)
		}
	}
	g.mu.Unlock()
	return removed, nil
}

func (g *Generator) generate(ctx context.Context, src Source, k Kind, dst string) error {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.sem }()

	sample, err := g.grab(ctx, src, k, dst, seekPoint(src.DurationSec))
	if errors.Is(err, ErrNoFrame) && seekPoint(src.DurationSec) > 0 {
		// Seeking can land past the last keyframe of a short file.
		sample, err = g.grab(ctx, src, k, dst, 0)
	}
	if err != nil {
		return err
	}
	if hex, ok := dominant(sample); ok {
		g.saveColour(src, hex)
	}
	return nil
}

// grab runs ffmpeg once: it writes the image to dst and returns a 16×9 RGB
// sample of the same frame.
func (g *Generator) grab(ctx context.Context, src Source, k Kind, dst string, ss float64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	tmp := dst + ".tmp"
	defer os.Remove(tmp)

	// thumbnail=12 picks the most representative of 12 keyframes, which skips
	// fades to black and title cards at almost no decoding cost.
	chain := fmt.Sprintf("[0:v:0]thumbnail=12,scale=w=min(%d\\,iw):h=-2", k.width()) // never upscale
	if src.HDR && g.toneMap != "" {
		chain += "," + g.toneMap
	}
	chain += ",format=yuv420p,split=2[img][s];[s]scale=16:9:flags=area,format=rgb24[sample]"

	args := []string{"-v", "error", "-nostdin", "-y"}
	if ss > 0 {
		args = append(args, "-ss", strconv.FormatFloat(ss, 'f', 3, 64))
	}
	args = append(args,
		"-skip_frame", "nokey", "-i", src.Path,
		"-filter_complex", chain,
		"-map", "[img]", "-frames:v", "1", "-c:v", "mjpeg", "-q:v", "4", "-f", "mjpeg", tmp,
		"-map", "[sample]", "-frames:v", "1", "-f", "rawvideo", "pipe:1",
	)
	g.runs.Add(1)
	out, err := g.run(ctx, args)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return nil, fmt.Errorf("%w: ffmpeg wrote no image for %s", ErrNoFrame, filepath.Base(src.Path))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *Generator) exec(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, g.ffmpeg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	ownProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", g.ffmpeg, err)
	}
	setNice(cmd.Process.Pid, g.niceness) // best effort
	if g.started != nil {
		g.started(cmd.Process.Pid)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v: %s", ErrNoFrame, err, firstLine(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// detectToneMap returns an HDR→SDR filter chain if this ffmpeg has the zscale
// and tonemap filters (Homebrew's build lacks zscale; jellyfin-ffmpeg has it).
// Without it HDR frames are converted as-is and look washed out.
func (g *Generator) detectToneMap(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, g.ffmpeg, "-hide_banner", "-filters").Output()
	if err != nil {
		return ""
	}
	has := func(name string) bool { return bytes.Contains(out, []byte(" "+name+" ")) }
	if !has("zscale") || !has("tonemap") {
		return ""
	}
	return "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv"
}

func (g *Generator) saveColour(src Source, hex string) {
	name := filepath.Join(g.dir, colourName(src.ID, src.MTime))
	if err := os.WriteFile(name, []byte(hex), 0o644); err != nil {
		return // the colour is decoration; the image is what matters
	}
	g.mu.Lock()
	g.colors[src.ID] = colour{mtime: src.MTime, hex: hex}
	g.mu.Unlock()
}

func (g *Generator) loadColours() error {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".color") {
			continue
		}
		id, mtime, ok := parseName(e.Name())
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(g.dir, e.Name()))
		if err != nil || !validHex(string(b)) {
			continue
		}
		if c, seen := g.colors[id]; !seen || mtime > c.mtime {
			g.colors[id] = colour{mtime: mtime, hex: string(b)}
		}
	}
	return nil
}

// seekPoint is where to look for a representative frame: 10% in, past most
// cold opens and studio logos.
func seekPoint(durationSec float64) float64 {
	if durationSec <= 0 {
		return 0
	}
	return durationSec * 0.1
}

// dominant derives a page tint from an RGB24 sample, weighting saturated
// pixels up and near-black ones (letterbox bars) down so the result is not mud.
func dominant(rgb []byte) (string, bool) {
	if len(rgb) < 3 {
		return "", false
	}
	var sr, sg, sb, sw float64
	for i := 0; i+2 < len(rgb); i += 3 {
		r, gr, b := float64(rgb[i]), float64(rgb[i+1]), float64(rgb[i+2])
		hi, lo := max(r, gr, b), min(r, gr, b)
		w := 0.05 + (hi-lo)/255
		if hi < 24 {
			w *= 0.1
		}
		sr, sg, sb, sw = sr+r*w, sg+gr*w, sb+b*w, sw+w
	}
	return fmt.Sprintf("#%02x%02x%02x", int(sr/sw+0.5), int(sg/sw+0.5), int(sb/sw+0.5)), true
}

func imageName(id int64, k Kind, mtime int64) string {
	return fmt.Sprintf("%d-%s-%d.jpg", id, k, mtime)
}

func colourName(id, mtime int64) string {
	return fmt.Sprintf("%d-%d.color", id, mtime)
}

// parseName extracts the item id and mtime from a cache file name
// ("<id>-<kind>-<mtime>.jpg", "<id>-<mtime>.color", and their .tmp leftovers).
func parseName(name string) (id, mtime int64, ok bool) {
	base, _, _ := strings.Cut(name, ".")
	parts := strings.Split(base, "-")
	if len(parts) < 2 {
		return 0, 0, false
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	mtime, err2 := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err1 != nil || err2 != nil || strings.HasSuffix(name, ".tmp") {
		return 0, 0, false
	}
	return id, mtime, true
}

func recent(e os.DirEntry) bool {
	fi, err := e.Info()
	return err == nil && time.Since(fi.ModTime()) < 5*time.Minute
}

func validHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 16, 32)
	return err == nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
