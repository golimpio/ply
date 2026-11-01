package art

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stub replaces ffmpeg: it writes a fake image to the -f mjpeg output path and
// returns an RGB sample filled with fill.
type stub struct {
	fill     [3]byte
	fail     func(args []string) bool // when true, behave like ffmpeg failing
	failErr  error                    // the failure to return; default: a "no video" ErrNoFrame
	partial  bool                     // write the image file before failing
	block    chan struct{}            // when non-nil, every run waits on it
	slowStop time.Duration            // how long a cancelled run takes to exit

	mu        sync.Mutex
	active    int
	maxActive int
	calls     [][]string
}

func (s *stub) run(ctx context.Context, args []string) ([]byte, error) {
	s.mu.Lock()
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.calls = append(s.calls, args)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			time.Sleep(s.slowStop)
			return nil, ctx.Err()
		}
	}
	out := args[slices.IndexFunc(args, func(a string) bool { return strings.HasSuffix(a, ".tmp") })]
	if s.fail != nil && s.fail(args) {
		if s.partial {
			os.WriteFile(out, []byte("\xff\xd8half"), 0o644)
		}
		if s.failErr != nil {
			return nil, s.failErr
		}
		return nil, fmt.Errorf("%w: exit status 1: no video", ErrNoFrame)
	}
	if err := os.WriteFile(out, []byte("\xff\xd8fake"), 0o644); err != nil {
		return nil, err
	}
	return bytes.Repeat(s.fill[:], 16*9), nil
}

func (s *stub) snapshot() (active, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.maxActive
}

func newTestGenerator(t *testing.T, dir string, s *stub) *Generator {
	t.Helper()
	g, err := New(context.Background(), Options{Dir: dir, FFmpeg: filepath.Join(dir, "no-such-ffmpeg")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = s.run
	t.Cleanup(g.Close)
	return g
}

// fixtureMedia is an existing file standing in for a media file: generate
// checks that its input exists before running ffmpeg.
var fixtureMedia string

func TestMain(m *testing.M) {
	f, err := os.CreateTemp("", "art-media-*.mkv")
	if err != nil {
		panic(err)
	}
	f.Close()
	fixtureMedia = f.Name()
	code := m.Run()
	os.Remove(fixtureMedia)
	os.Exit(code)
}

func src(id, mtime int64) Source {
	return Source{ID: id, Path: fixtureMedia, MTime: mtime, DurationSec: 0}
}

func TestPathGeneratesOnceThenServesFromCache(t *testing.T) {
	g := newTestGenerator(t, t.TempDir(), &stub{fill: [3]byte{200, 30, 30}})
	ctx := context.Background()

	p1, err := g.Path(ctx, src(1, 10), Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if g.Runs() != 1 {
		t.Fatalf("first request: runs = %d, want 1", g.Runs())
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("image not on disk: %v", err)
	}
	p2, err := g.Path(ctx, src(1, 10), Thumb)
	if err != nil || p2 != p1 {
		t.Fatalf("second request = %q, %v; want %q", p2, err, p1)
	}
	if g.Runs() != 1 {
		t.Fatalf("cache hit ran ffmpeg again: runs = %d", g.Runs())
	}
	// A different kind is a different image.
	if _, err := g.Path(ctx, src(1, 10), Backdrop); err != nil {
		t.Fatal(err)
	}
	if g.Runs() != 2 {
		t.Fatalf("backdrop: runs = %d, want 2", g.Runs())
	}
}

func TestConcurrentRequestsShareOneRun(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)

	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Path(context.Background(), src(1, 10), Thumb); err != nil {
				failures.Add(1)
			}
		}()
	}
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })
	close(s.block)
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d requests failed", failures.Load())
	}
	if g.Runs() != 1 {
		t.Fatalf("8 concurrent requests ran ffmpeg %d times, want 1", g.Runs())
	}
}

func TestConcurrencyIsCapped(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)

	var wg sync.WaitGroup
	for id := range int64(6) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Path(context.Background(), src(id+1, 10), Thumb)
		}()
	}
	// Positive control: the cap is actually reached...
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 2 })
	// ...and nothing gets past it while the two runs hold their slots.
	time.Sleep(50 * time.Millisecond)
	if _, m := s.snapshot(); m != 2 {
		t.Fatalf("max concurrent ffmpeg runs = %d, want 2", m)
	}
	close(s.block)
	wg.Wait()
	if _, m := s.snapshot(); m != 2 || g.Runs() != 6 {
		t.Fatalf("after release: max = %d, runs = %d; want 2, 6", m, g.Runs())
	}
}

func TestCancelledRequestDoesNotAbandonTheWork(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := g.Path(ctx, src(1, 10), Thumb); done <- err }()
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request err = %v, want context.Canceled", err)
	}
	close(s.block)
	p, err := g.Path(context.Background(), src(1, 10), Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil || g.Runs() != 1 {
		t.Fatalf("image %v, runs %d; want the first run's image reused", err, g.Runs())
	}
}

func TestMTimeChangeRegenerates(t *testing.T) {
	s := &stub{fill: [3]byte{10, 120, 200}}
	g := newTestGenerator(t, t.TempDir(), s)
	ctx := context.Background()

	old, _ := g.Path(ctx, src(1, 10), Thumb)
	if hex, ok := g.Colour(src(1, 10)); !ok || hex != "#0a78c8" {
		t.Fatalf("colour = %q, %v; want #0a78c8", hex, ok)
	}
	s.fill = [3]byte{250, 200, 0}
	cur, err := g.Path(ctx, src(1, 20), Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if cur == old || g.Runs() != 2 {
		t.Fatalf("changed file reused %q (runs %d)", cur, g.Runs())
	}
	if _, ok := g.Colour(src(1, 10)); ok {
		t.Fatal("colour of the old mtime still reported")
	}
	if hex, ok := g.Colour(src(1, 20)); !ok || hex != "#fac800" {
		t.Fatalf("new colour = %q, %v; want #fac800", hex, ok)
	}
}

func TestFailureIsRememberedPerMTime(t *testing.T) {
	s := &stub{fail: func([]string) bool { return true }}
	g := newTestGenerator(t, t.TempDir(), s)
	ctx := context.Background()

	if _, err := g.Path(ctx, src(1, 10), Thumb); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("err = %v, want ErrNoFrame", err)
	}
	if g.Runs() != 1 {
		t.Fatalf("runs = %d, want 1", g.Runs())
	}
	if _, err := g.Path(ctx, src(1, 10), Thumb); !errors.Is(err, ErrNoFrame) || g.Runs() != 1 {
		t.Fatalf("retry: err = %v, runs = %d; want remembered ErrNoFrame without a run", err, g.Runs())
	}
	s.fail = nil
	if _, err := g.Path(ctx, src(1, 11), Thumb); err != nil || g.Runs() != 2 {
		t.Fatalf("changed file: err = %v, runs = %d; want a fresh, successful run", err, g.Runs())
	}
}

func TestSeekPastEndRetriesFromStart(t *testing.T) {
	s := &stub{fail: func(args []string) bool { return slices.Contains(args, "-ss") }}
	g := newTestGenerator(t, t.TempDir(), s)

	in := src(1, 10)
	in.DurationSec = 100
	if _, err := g.Path(context.Background(), in, Thumb); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (seek, then from the start)", len(s.calls))
	}
	if i := slices.Index(s.calls[0], "-ss"); i < 0 || s.calls[0][i+1] != "10.000" {
		t.Fatalf("first call should seek to 10%%: %q", s.calls[0])
	}
}

func TestToneMapOnlyForHDR(t *testing.T) {
	s := &stub{}
	g := newTestGenerator(t, t.TempDir(), s)
	g.toneMap = "TONEMAP"

	sdr, hdr := src(1, 10), src(2, 10)
	hdr.HDR = true
	g.Path(context.Background(), sdr, Thumb)
	g.Path(context.Background(), hdr, Thumb)

	graph := func(args []string) string { return args[slices.Index(args, "-filter_complex")+1] }
	if strings.Contains(graph(s.calls[0]), "TONEMAP") {
		t.Error("SDR source was tone-mapped")
	}
	if !strings.Contains(graph(s.calls[1]), "TONEMAP") {
		t.Error("HDR source was not tone-mapped")
	}
}

func TestColoursSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	g := newTestGenerator(t, dir, &stub{fill: [3]byte{40, 160, 90}})
	g.Path(context.Background(), src(7, 99), Thumb)

	g2 := newTestGenerator(t, dir, &stub{})
	if hex, ok := g2.Colour(src(7, 99)); !ok || hex != "#28a05a" {
		t.Fatalf("after restart colour = %q, %v; want #28a05a", hex, ok)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	g := newTestGenerator(t, dir, &stub{fill: [3]byte{1, 2, 3}})
	ctx := context.Background()
	keep, _ := g.Path(ctx, src(1, 10), Thumb)
	gone, _ := g.Path(ctx, src(2, 10), Thumb)
	changed, _ := g.Path(ctx, src(3, 10), Backdrop)
	for _, p := range []string{keep, gone, changed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("precondition: %s missing", p)
		}
	}

	n, err := g.Prune(map[int64]int64{1: 10, 3: 11})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("live item's image was pruned: %v", err)
	}
	for _, p := range []string{gone, changed} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived prune", filepath.Base(p))
		}
	}
	if n != 4 { // two images + two colour files
		t.Errorf("removed %d files, want 4", n)
	}
	if _, ok := g.Colour(src(2, 10)); ok {
		t.Error("deleted item's colour still reported")
	}
	if _, ok := g.Colour(src(1, 10)); !ok {
		t.Error("live item's colour was dropped")
	}
}

func TestDominant(t *testing.T) {
	grey := bytes.Repeat([]byte{128, 128, 128}, 144)
	if hex, _ := dominant(grey); hex != "#808080" {
		t.Errorf("grey = %s", hex)
	}
	// Letterboxed: mostly black bars with a red picture in the middle.
	boxed := bytes.Repeat([]byte{0, 0, 0}, 100)
	boxed = append(boxed, bytes.Repeat([]byte{220, 20, 20}, 44)...)
	hex, _ := dominant(boxed)
	if hex[1:3] < "c0" {
		t.Errorf("letterboxed red = %s; black bars should barely count", hex)
	}
	if _, ok := dominant(nil); ok {
		t.Error("empty sample produced a colour")
	}
}

func TestDominantKeepsAHueWhenColoursCompete(t *testing.T) {
	px := func(c [3]byte, n int) []byte { return bytes.Repeat(c[:], n) }
	sat := func(hex string) float64 {
		var r, g, b int
		fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
		hi, lo := max(r, g, b), min(r, g, b)
		return float64(hi-lo) / float64(max(hi, 1))
	}

	// Colour bars: six strong hues. A plain mean of these is grey (the old bug).
	bars := slices.Concat(
		px([3]byte{230, 30, 30}, 34), // red has the most area
		px([3]byte{30, 200, 30}, 22), px([3]byte{30, 30, 220}, 22),
		px([3]byte{220, 220, 30}, 22), px([3]byte{30, 220, 220}, 22), px([3]byte{220, 30, 220}, 22),
	)
	hex, _ := dominant(bars)
	if s := sat(hex); s < 0.6 {
		t.Errorf("colour bars = %s (saturation %.2f); want a strong hue, not grey", hex, s)
	}
	if hex[1:3] < "c0" {
		t.Errorf("colour bars = %s; want the largest hue (red) to win", hex)
	}

	// A mostly grey frame with a clear blue area takes the blue as its accent.
	sky := slices.Concat(px([3]byte{120, 120, 120}, 110), px([3]byte{40, 90, 210}, 34))
	if hex, _ := dominant(sky); hex[5:7] < "b0" || sat(hex) < 0.5 {
		t.Errorf("grey with blue = %s; want the blue", hex)
	}

	// Positive control for the grey fallback: a hint of colour below the
	// threshold must not hijack an otherwise neutral frame.
	neutral := slices.Concat(px([3]byte{120, 120, 120}, 142), px([3]byte{200, 40, 40}, 2))
	if hex, _ := dominant(neutral); sat(hex) > 0.15 {
		t.Errorf("neutral frame with a speck of red = %s; want near grey", hex)
	}
}

// writeJPEG writes a solid-colour JPEG, standing in for an image cached earlier.
func writeJPEG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := range 36 {
		for x := range 64 {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
}

func TestOldFormulaColourIsReplacedFromTheCachedImage(t *testing.T) {
	dir := t.TempDir()
	in := src(1, 10)
	thumb := filepath.Join(dir, imageName(in.ID, Thumb, in.MTime))
	writeJPEG(t, thumb, color.RGBA{30, 60, 200, 255})
	legacy := filepath.Join(dir, "1-10.color") // written by the averaging formula
	if err := os.WriteFile(legacy, []byte("#808080"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := newTestGenerator(t, dir, &stub{})
	if hex, ok := g.Colour(in); ok {
		t.Fatalf("old-formula colour %s was loaded", hex)
	}
	if _, err := g.Path(context.Background(), in, Thumb); err != nil {
		t.Fatal(err)
	}
	if g.Runs() != 0 {
		t.Fatalf("cached image ran ffmpeg %d times", g.Runs())
	}
	hex, ok := g.Colour(in)
	if !ok || hex[5:7] < "b8" || hex[1:3] > "30" {
		t.Fatalf("colour from cached image = %q, %v; want the image's blue", hex, ok)
	}

	n, err := g.Prune(map[int64]int64{1: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) || n != 1 {
		t.Errorf("prune removed %d files, legacy colour file err = %v; want just it gone", n, err)
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Errorf("live image pruned: %v", err)
	}
	if _, ok := g.Colour(in); !ok {
		t.Error("current colour dropped by prune")
	}
}

func TestParseName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		id, mtime int64
		ok        bool
	}{
		{"12-thumb-1730000000000000000.jpg", 12, 1730000000000000000, true},
		{"12-backdrop-5.jpg", 12, 5, true},
		{"12-5.color", 12, 5, true},
		{"12-thumb-5.jpg.tmp", 0, 0, false},
		{"notes.txt", 0, 0, false},
		{"x-thumb-5.jpg", 0, 0, false},
	} {
		id, mtime, ok := parseName(tc.name)
		if id != tc.id || mtime != tc.mtime || ok != tc.ok {
			t.Errorf("parseName(%q) = %d, %d, %v", tc.name, id, mtime, ok)
		}
	}
}

func TestParseKind(t *testing.T) {
	for _, s := range []string{"thumb", "backdrop"} {
		if _, ok := ParseKind(s); !ok {
			t.Errorf("ParseKind(%q) rejected", s)
		}
	}
	for _, s := range []string{"", "poster", "../x"} {
		if _, ok := ParseKind(s); ok {
			t.Errorf("ParseKind(%q) accepted", s)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 5s")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
