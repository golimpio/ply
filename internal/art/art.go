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
	"image"
	"image/jpeg"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ply/internal/proc"
)

// Kind is an artwork variant.
type Kind string

const (
	Backdrop Kind = "backdrop" // wide hero image
	Thumb    Kind = "thumb"    // card image
)

// ContentType is the media type of every generated image.
const ContentType = "image/jpeg"

var (
	// ErrNoFrame reports that the file itself yields no image: it has no video
	// stream or is not decodable. It is remembered until the file changes.
	ErrNoFrame = errors.New("art: no frame")
	// ErrUnavailable reports a failure that may pass: the file is missing (an
	// unmounted share), ffmpeg timed out, or it failed for a reason that is not
	// about the content. Missing files are retried on every request (no ffmpeg
	// runs for them); the others after failureTTL.
	ErrUnavailable = errors.New("art: temporarily unavailable")
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("art: generator closed")
	// ErrDeferred reports a grab that was stopped (Defer) because the file is about to be
	// played: it is not a failure, and nothing is remembered.
	ErrDeferred = errors.New("art: deferred while the video starts")
)

// failureTTL is how long a transient failure is remembered, so a grid of tiles
// on a slow or sleeping share does not queue one 30 s ffmpeg run per request.
const failureTTL = 10 * time.Minute

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
	// Reaper is the program that runs `--reap` (ply itself): where there is no
	// parent-death signal (macOS) one is started for every ffmpeg group, and kills it if
	// ply dies. Empty starts none (Linux has Pdeathsig).
	Reaper string
	Logger *slog.Logger
}

// Generator creates artwork on demand and caches it on disk.
type Generator struct {
	dir      string
	ffmpeg   string
	niceness int
	timeout  time.Duration
	sem      chan struct{}
	toneMap  string // filter chain applied to HDR sources; "" when ffmpeg lacks zscale
	reaper   string
	log      *slog.Logger

	base   context.Context // parent of every generation; cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	inflight map[string]*call
	failed   map[string]failure // cache keys that could not be generated
	colors   map[int64]colour

	now  func() time.Time // replaced in tests
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
	done   chan struct{}
	err    error
	src    Source
	kind   Kind
	cancel context.CancelCauseFunc
}

// failure is a remembered generation error; a zero until never expires (it
// is dropped when the file changes or the item is pruned).
type failure struct {
	err   error
	until time.Time
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
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	g := &Generator{
		dir:      o.Dir,
		ffmpeg:   o.FFmpeg,
		niceness: o.Niceness,
		timeout:  o.Timeout,
		sem:      make(chan struct{}, o.Concurrency),
		reaper:   o.Reaper,
		log:      o.Logger,
		inflight: map[string]*call{},
		failed:   map[string]failure{},
		colors:   map[int64]colour{},
		now:      time.Now,
	}
	g.base, g.cancel = context.WithCancel(context.Background())
	g.run = g.exec
	g.toneMap = g.detectToneMap(ctx)
	if err := g.loadColours(); err != nil {
		return nil, err
	}
	return g, nil
}

// Runs reports how many ffmpeg runs the generator has started.
func (g *Generator) Runs() int64 { return g.runs.Load() }

// Close cancels every generation in flight (killing its ffmpeg process group)
// and waits for them to finish. Path returns ErrClosed afterwards.
func (g *Generator) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.cancel()
	g.wg.Wait()
}

// Path returns the cached image of kind k for src, generating it first if
// needed. Concurrent requests for the same image share one ffmpeg run. A file
// without usable video is not retried until its mtime changes; transient
// failures are retried after failureTTL.
func (g *Generator) Path(ctx context.Context, src Source, k Kind) (string, error) {
	name := filepath.Join(g.dir, imageName(src.ID, k, src.MTime))
	if _, err := os.Stat(name); err == nil {
		if _, ok := g.Colour(src); !ok {
			// Cached before the colour existed, or by an older colour formula.
			g.colourFromImage(src, name)
		}
		return name, nil
	}
	key := filepath.Base(name)

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return "", ErrClosed
	}
	if f, ok := g.failed[key]; ok {
		if f.until.IsZero() || g.now().Before(f.until) {
			g.mu.Unlock()
			return "", f.err
		}
		delete(g.failed, key)
	}
	c, ok := g.inflight[key]
	if !ok {
		c = &call{done: make(chan struct{}), src: src, kind: k}
		var ctx context.Context
		ctx, c.cancel = context.WithCancelCause(g.base)
		g.inflight[key] = c
		g.wg.Add(1)
		// The work outlives the first request (a client navigating away must not
		// throw away an image the next request will want) but not the generator.
		go g.fill(ctx, c, key, src, k, name)
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
	defer g.wg.Done()
	defer c.cancel(nil)
	c.err = g.generate(ctx, src, k, name)
	if c.err != nil && errors.Is(context.Cause(ctx), ErrDeferred) {
		c.err = ErrDeferred // whatever the killed ffmpeg said
	}
	g.mu.Lock()
	delete(g.inflight, key)
	switch {
	case c.err == nil, g.base.Err() != nil, errors.Is(c.err, fs.ErrNotExist), errors.Is(c.err, ErrDeferred):
		// Success, shutdown, a missing file (cheap to re-check: no ffmpeg runs), or a grab
		// that was put off.
	case errors.Is(c.err, ErrNoFrame):
		g.failed[key] = failure{err: c.err}
	default:
		g.failed[key] = failure{err: c.err, until: g.now().Add(failureTTL)}
	}
	g.mu.Unlock()
	close(c.done)
}

// Cached returns the image of kind k for src if it has been made. It never starts a grab.
func (g *Generator) Cached(src Source, k Kind) (string, bool) {
	name := filepath.Join(g.dir, imageName(src.ID, k, src.MTime))
	if _, err := os.Stat(name); err != nil {
		return "", false
	}
	return name, true
}

// Grab is an image to be made.
type Grab struct {
	Src  Source
	Kind Kind
}

// Defer stops the grabs under way for item id, so that a play that is starting has its
// file to itself: the people waiting for them get ErrDeferred, and nothing is remembered
// against the file. It returns the images that were being made, for the caller to make
// when the play is over.
func (g *Generator) Defer(id int64) []Grab {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Grab
	for _, c := range g.inflight {
		if c.src.ID == id {
			out = append(out, Grab{c.src, c.kind})
			c.cancel(ErrDeferred)
		}
	}
	return out
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
		if id, mtime, ok := parseName(e.Name()); ok && !staleColour(e.Name()) {
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
			delete(g.failed, key) // the item is gone or has changed: try again
		}
	}
	g.mu.Unlock()
	return removed, nil
}

func (g *Generator) generate(ctx context.Context, src Source, k Kind, dst string) error {
	if _, err := os.Stat(src.Path); err != nil {
		// Typically an unmounted share; ffmpeg would only report the same thing.
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
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
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: ffmpeg took longer than %s on %s", ErrUnavailable, g.timeout, filepath.Base(src.Path))
	}
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
	proc.OwnGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	exited := make(chan error, 1)
	if err := proc.Start(cmd, func(err error) { exited <- err }); err != nil {
		return nil, fmt.Errorf("start %s: %w", g.ffmpeg, err)
	}
	// If ply is killed from here on, the group goes with it: by the kernel (Linux), or by
	// the reaper (macOS).
	var reaper *exec.Cmd
	if g.reaper != "" {
		var err error
		if reaper, err = proc.StartReaper(g.reaper, cmd.Process.Pid); err != nil {
			g.log.Warn("artwork: starting the ffmpeg reaper", "err", err)
		}
	}
	setNice(cmd.Process.Pid, g.niceness) // best effort
	if g.started != nil {
		g.started(cmd.Process.Pid)
	}
	err := <-exited
	proc.StopReaper(reaper)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := firstLine(stderr.String())
		if contentFailure(stderr.String()) {
			return nil, fmt.Errorf("%w: %v: %s", ErrNoFrame, err, msg)
		}
		return nil, fmt.Errorf("%w: %v: %s", ErrUnavailable, err, msg)
	}
	return stdout.Bytes(), nil
}

// contentFailure reports ffmpeg errors that are about the file itself (no
// video stream, undecodable data) rather than the machine or the mount; only
// those are remembered until the file changes.
func contentFailure(stderr string) bool {
	for _, s := range []string{
		"matches no streams",
		"does not contain any stream",
		"Invalid data found when processing input",
	} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
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

// colourExt names colour cache files. Bump it whenever dominant() changes, so
// colours computed by an older formula are ignored, recomputed and pruned.
const colourExt = ".color2"

// staleColour reports a colour file written by an older formula.
func staleColour(name string) bool {
	return strings.Contains(name, ".color") && !strings.HasSuffix(name, colourExt)
}

// colourFromImage derives the colour from an already cached JPEG, for images
// cached before their colour was (re)computed. Pure Go, a few milliseconds,
// best effort.
func (g *Generator) colourFromImage(src Source, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return
	}
	if hex, ok := dominant(sample16x9(img)); ok {
		g.saveColour(src, hex)
	}
}

// sample16x9 reduces img to the same 16×9 RGB24 grid ffmpeg produces for new
// images, averaging a sparse lattice of pixels in each cell.
func sample16x9(img image.Image) []byte {
	b := img.Bounds()
	out := make([]byte, 0, 16*9*3)
	for cy := range 9 {
		for cx := range 16 {
			x0, x1 := b.Min.X+cx*b.Dx()/16, b.Min.X+(cx+1)*b.Dx()/16
			y0, y1 := b.Min.Y+cy*b.Dy()/9, b.Min.Y+(cy+1)*b.Dy()/9
			var r, g, bl, n uint64
			for y := y0; y < y1; y += 4 {
				for x := x0; x < x1; x += 4 {
					pr, pg, pb, _ := img.At(x, y).RGBA()
					r, g, bl, n = r+uint64(pr>>8), g+uint64(pg>>8), bl+uint64(pb>>8), n+1
				}
			}
			if n == 0 {
				n = 1
			}
			out = append(out, byte(r/n), byte(g/n), byte(bl/n))
		}
	}
	return out
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
		if !strings.HasSuffix(e.Name(), colourExt) {
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

// dominant derives a page tint from an RGB24 sample.
//
// Averaging every pixel turns a frame of several strong colours into grey, so
// colourful pixels are grouped into 12 hue buckets (weighted by saturation ×
// brightness) and only the strongest bucket and its neighbours are averaged.
// Frames with almost no colour fall back to their mean, with near-black pixels
// (letterbox bars) counting little.
func dominant(rgb []byte) (string, bool) {
	if len(rgb) < 3 {
		return "", false
	}
	type bucket struct{ r, g, b, w float64 }
	var hues [12]bucket
	var mean bucket
	var colourful float64
	n := 0
	for i := 0; i+2 < len(rgb); i += 3 {
		r, g, b := float64(rgb[i]), float64(rgb[i+1]), float64(rgb[i+2])
		hi, lo := max(r, g, b), min(r, g, b)
		n++
		w := 1.0
		if hi < 24 {
			w = 0.1
		}
		mean = bucket{mean.r + r*w, mean.g + g*w, mean.b + b*w, mean.w + w}
		if hi < 40 || (hi-lo)/hi < 0.2 {
			continue // too dark or too grey to carry a hue
		}
		cw := (hi - lo) / hi * hi / 255 // saturation × value
		k := int(hue(r, g, b, hi, lo)/30) % 12
		hues[k] = bucket{hues[k].r + r*cw, hues[k].g + g*cw, hues[k].b + b*cw, hues[k].w + cw}
		colourful += cw
	}

	pick := mean
	if colourful >= 0.05*float64(n) {
		best, bestScore := 0, -1.0
		for k := range hues {
			score := hues[k].w + 0.5*(hues[(k+11)%12].w+hues[(k+1)%12].w)
			if score > bestScore {
				best, bestScore = k, score
			}
		}
		pick = bucket{}
		for _, k := range []int{(best + 11) % 12, best, (best + 1) % 12} {
			pick = bucket{pick.r + hues[k].r, pick.g + hues[k].g, pick.b + hues[k].b, pick.w + hues[k].w}
		}
	}
	return fmt.Sprintf("#%02x%02x%02x", int(pick.r/pick.w+0.5), int(pick.g/pick.w+0.5), int(pick.b/pick.w+0.5)), true
}

// hue returns the HSV hue in degrees [0, 360) of a pixel with hi > lo.
func hue(r, g, b, hi, lo float64) float64 {
	d := hi - lo
	var h float64
	switch hi {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	if h *= 60; h < 0 {
		h += 360
	}
	return h
}

func imageName(id int64, k Kind, mtime int64) string {
	return fmt.Sprintf("%d-%s-%d.jpg", id, k, mtime)
}

func colourName(id, mtime int64) string {
	return fmt.Sprintf("%d-%d%s", id, mtime, colourExt)
}

// parseName extracts the item id and mtime from a cache file name
// ("<id>-<kind>-<mtime>.jpg", "<id>-<mtime>.color2", and their .tmp leftovers).
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
