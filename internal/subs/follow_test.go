package subs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	followEvery = 5 * time.Millisecond
	paceEvery = 10 * time.Millisecond
}

// feedRun stands in for ffmpeg reading a big container: it writes what the test
// feeds it, a piece at a time, to every output file it is asked for, and ends
// when the feed is closed (with fail, if set) or its context ends.
type feedRun struct {
	feed  chan string
	fail  error
	calls atomic.Int64

	mu   sync.Mutex
	args [][]string
	ctl  *ctl
	live chan struct{} // closed when the first run has created its files
	once sync.Once
}

func newFeedRun() *feedRun { return &feedRun{feed: make(chan string, 16), live: make(chan struct{})} }

func (f *feedRun) run(ctx context.Context, args []string, p *ctl) (string, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.args = append(f.args, args)
	f.ctl = p
	f.mu.Unlock()
	var outs []*os.File
	for _, a := range args {
		if strings.HasSuffix(a, ".tmp") {
			o, err := os.Create(a)
			if err != nil {
				return "", err
			}
			defer o.Close()
			outs = append(outs, o)
		}
	}
	f.once.Do(func() { close(f.live) })
	for {
		select {
		case s, ok := <-f.feed:
			if !ok {
				if f.fail != nil {
					return "bad stream\nmore", f.fail
				}
				return "", nil
			}
			for _, o := range outs {
				if _, err := o.WriteString(s); err != nil {
					return "", err
				}
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (f *feedRun) lastArgs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.args[len(f.args)-1], " ")
}

func newFeedGen(t *testing.T, f *feedRun) *Generator {
	t.Helper()
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = f.run
	t.Cleanup(g.Close)
	return g
}

// piece is what a pump read.
type piece struct {
	data string
	err  error
}

// pump reads fo in the background, so that a test can wait for data, and for the
// lack of it.
func pump(fo *Follower) <-chan piece {
	ch := make(chan piece, 64)
	go func() {
		buf := make([]byte, 7) // small, to be sure that Read gives what it has, not what p holds
		for {
			n, err := fo.Read(buf)
			if n > 0 {
				ch <- piece{data: string(buf[:n])}
			}
			if err != nil {
				ch <- piece{err: err}
				return
			}
		}
	}()
	return ch
}

// take reads from ch until it has want, or fails.
func take(t *testing.T, ch <-chan piece, want string) string {
	t.Helper()
	var got string
	timeout := time.After(5 * time.Second)
	for got != want {
		if len(got) > len(want) || !strings.HasPrefix(want, got) {
			t.Fatalf("read %q, want %q", got, want)
		}
		select {
		case p := <-ch:
			if p.err != nil {
				t.Fatalf("read %q then %v, want %q", got, p.err, want)
			}
			got += p.data
		case <-timeout:
			t.Fatalf("timed out with %q, want %q", got, want)
		}
	}
	return got
}

// drain waits for the pump to see the end of the stream (the Follower is not closed under its reader).
func drain(t *testing.T, ch <-chan piece) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case p := <-ch:
			if p.err != nil {
				return
			}
		case <-timeout:
			t.Fatal("the stream did not end")
		}
	}
}

// quiet fails if anything arrives within a few polls.
func quiet(t *testing.T, ch <-chan piece, what string) {
	t.Helper()
	select {
	case p := <-ch:
		t.Fatalf("%s: got %+v", what, p)
	case <-time.After(15 * followEvery):
	}
}

// track is the embedded subtitle with index stream (below 10) of the container input.
func track(input string, stream int) Source {
	return Source{ItemID: 1, Key: strconv.Itoa(stream), Input: input, Stream: stream, MTime: 5}
}

func embedded(t *testing.T, stream int) Source { return track(existingFile(t, "a.mkv"), stream) }

// While ffmpeg is still reading the container, the WebVTT is already being handed
// out, a whole cue at a time; it ends when the file is complete, and then the cache
// has the file.
func TestFollowGivesOutCuesWhileFFmpegIsWriting(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)

	if g.Cached(src) {
		t.Fatal("setup: the track is cached before anything ran") // positive control for the end
	}
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)

	const header, cue1, cue2 = "WEBVTT\n\n", "00:01.000 --> 00:02.000\none\n\n", "00:03.000 --> 00:04.000\ntwo\n\n"
	f.feed <- header + cue1
	f.feed <- cue2[:len(cue2)-3] // half a cue: "00:03.000 --> 00:04.000\ntw"
	take(t, ch, header+cue1)
	select {
	case <-fo.Done():
		t.Fatal("the run is over: this is not following anything")
	default:
	}
	quiet(t, ch, "the unfinished cue must wait for its end")

	f.feed <- cue2[len(cue2)-3:]
	take(t, ch, cue2)
	if g.Cached(src) {
		t.Error("cached while the run goes on")
	}

	close(f.feed)
	if p := <-ch; p.err != io.EOF || p.data != "" {
		t.Errorf("after the last cue: %+v, want io.EOF", p)
	}
	path, err := fo.Result()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != header+cue1+cue2 {
		t.Errorf("cache = %q", b)
	}
	if !g.Cached(src) {
		t.Error("not cached after the run")
	}
}

// A run that fails midway leaves what was said as valid WebVTT, cut at a cue, and
// then the error.
func TestFollowEndsWithTheRunsFailure(t *testing.T) {
	f := newFeedRun()
	f.fail = errors.New("exit status 1")
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)

	const first = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	f.feed <- first
	f.feed <- "00:03.000 --> 00:04.000\ntw"
	take(t, ch, first)
	close(f.feed)
	p := <-ch
	if !errors.Is(p.err, ErrConvert) || p.data != "" {
		t.Errorf("after the failure: %+v, want ErrConvert and no half cue", p)
	}
	if g.Cached(src) {
		t.Error("a failed run left a cached file")
	}
	if _, err := fo.Result(); !errors.Is(err, ErrConvert) {
		t.Errorf("Result = %v", err)
	}
}

// A follower whose reader gives up does not stop the conversion (a reload must not
// restart minutes of reading).
func TestFollowerLeavingDoesNotStopTheRun(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	ctx, cancel := context.WithCancel(t.Context())
	fo, err := g.Follow(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, err := fo.Read(make([]byte, 10)); read <- err }()
	cancel()
	if err := <-read; !errors.Is(err, context.Canceled) {
		t.Fatalf("Read = %v", err)
	}
	fo.Close()

	// The run is still there: a second request joins it, and gets everything.
	fo2, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo2.Close()
	ch := pump(fo2)
	f.feed <- "WEBVTT\n\n"
	take(t, ch, "WEBVTT\n\n")
	close(f.feed)
	if p := <-ch; p.err != io.EOF {
		t.Errorf("end: %+v", p)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", n)
	}
}

// A track that is cached is handed out whole, with no ffmpeg.
func TestFollowOfACachedTrack(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	const body = "WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n\n"
	if err := os.WriteFile(filepath.Join(g.dir, cacheName(src)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	b, err := io.ReadAll(fo)
	if err != nil || string(b) != body {
		t.Errorf("read %q, %v", b, err)
	}
	if f.calls.Load() != 0 {
		t.Errorf("ffmpeg ran for a cached track")
	}
}

// Siblings ride along in the run that has to be started anyway: the container is
// read once, and the other tracks are ready when the viewer switches.
func TestFollowExtractsTheSiblingsToo(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	input := existingFile(t, "a.mkv")
	a, b, cached := track(input, 3), track(input, 5), track(input, 6)
	if err := os.WriteFile(filepath.Join(g.dir, cacheName(cached)), []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fo, err := g.Follow(t.Context(), a, []Source{a, b, cached, {ItemID: 1, Key: "ext0", Input: "/m/a.srt", Stream: -1, MTime: 5}})
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	<-f.live
	args := f.lastArgs()
	for _, want := range []string{"-map 0:3", "-map 0:5", "-flush_packets 1"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if strings.Count(args, "-map 0:3") != 1 || strings.Contains(args, "-map 0:6") || strings.Contains(args, "a.srt") {
		t.Errorf("the run should map each missing embedded track of the container once, and no others: %s", args)
	}

	// The viewer switches to the other track while the run goes on: it joins it.
	fb, err := g.Follow(t.Context(), b, []Source{a, b})
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	ch := pump(fb)
	f.feed <- "WEBVTT\n\n"
	take(t, ch, "WEBVTT\n\n")
	close(f.feed)
	drain(t, ch)
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times for two tracks of one container, want 1", n)
	}
}

// Fill fills the cache in as few runs as it takes: a run per sidecar, one for all the
// embedded tracks; what is cached is not asked for again.
func TestFillWritesEveryTrackAndTheCacheServesThemAfter(t *testing.T) {
	var runs atomic.Int64
	var mu sync.Mutex
	var argv [][]string
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	g.run = func(ctx context.Context, args []string, p *ctl) (string, error) {
		runs.Add(1)
		mu.Lock()
		argv = append(argv, args)
		mu.Unlock()
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") {
				if err := os.WriteFile(a, []byte("WEBVTT\n\n00:01.000 --> 00:02.000\n"+filepath.Base(a)+"\n\n"), 0o644); err != nil {
					return "", err
				}
			}
		}
		return "", nil
	}
	input := existingFile(t, "a.mkv")
	side := existingFile(t, "a.en.srt")
	srcs := []Source{
		{ItemID: 1, Key: "ext0", Input: side, Stream: -1, MTime: 7},
		{ItemID: 1, Key: "2", Input: input, Stream: 2, MTime: 5},
		{ItemID: 1, Key: "3", Input: input, Stream: 3, MTime: 5},
		{ItemID: 1, Key: "4", Input: input, Stream: 4, MTime: 5},
	}
	// Track 4 is cached already (somebody played it).
	if err := os.WriteFile(filepath.Join(g.dir, cacheName(srcs[3])), []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs[:3] {
		if g.Cached(s) {
			t.Fatalf("setup: %s is cached before Fill", s.Key) // positive control
		}
	}

	if err := g.Fill(t.Context(), srcs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("%d ffmpeg runs, want 2 (the sidecar, and one for tracks 2 and 3)", n)
	}
	emb := strings.Join(argv[1], " ")
	if !strings.Contains(emb, "-map 0:2") || !strings.Contains(emb, "-map 0:3") || strings.Contains(emb, "-map 0:4") || strings.Contains(emb, "-sub_charenc") {
		t.Errorf("the embedded run = %s", emb)
	}
	for _, s := range srcs {
		if !g.Cached(s) {
			t.Errorf("%s is not cached after Fill", s.Key)
		}
		// The play path now serves from the cache.
		p, err := g.Path(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "WEBVTT") {
			t.Errorf("%s = %q", s.Key, b)
		}
	}
	if n := runs.Load(); n != 2 {
		t.Errorf("serving the cached tracks ran ffmpeg again (%d runs)", n)
	}

	// Again: nothing to do.
	if err := g.Fill(t.Context(), srcs, nil, nil); err != nil || runs.Load() != 2 {
		t.Errorf("second Fill: %v, %d runs", err, runs.Load())
	}
}

// A play that asks for a track a background run is making joins that run and
// promotes it: the library's work becomes the viewer's.
func TestAPlayJoinsAndPromotesABackgroundRun(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)

	filled := make(chan error, 1)
	go func() {
		filled <- g.Fill(t.Context(), []Source{src}, func() (bool, string) { return true, "a play" }, nil)
	}()
	<-f.live
	f.mu.Lock()
	p := f.ctl
	f.mu.Unlock()
	if p.isUrgent() {
		t.Fatal("setup: a background run starts urgent") // positive control
	}

	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	if !p.isUrgent() {
		t.Error("the play did not promote the run")
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1 (the play joins the background run)", n)
	}
	ch := pump(fo)
	f.feed <- "WEBVTT\n\n"
	take(t, ch, "WEBVTT\n\n")
	close(f.feed)
	drain(t, ch)
	if err := <-filled; err != nil {
		t.Errorf("Fill = %v", err)
	}
}

// Fill stops the run when its context ends (the job was cancelled, ply is
// stopping), unless a play is waiting for it.
func TestFillStopsItsRunUnlessAPlayWaits(t *testing.T) {
	t.Run("nobody waits", func(t *testing.T) {
		f := newFeedRun()
		g := newFeedGen(t, f)
		ctx, cancel := context.WithCancelCause(t.Context())
		src := embedded(t, 3)
		filled := make(chan error, 1)
		go func() { filled <- g.Fill(ctx, []Source{src}, nil, nil) }()
		<-f.live
		cancel(errors.New("cancelled"))
		if err := <-filled; err == nil || errors.Is(err, ErrConvert) {
			t.Errorf("Fill = %v, want the cancellation", err)
		}
		g.mu.Lock()
		running, remembered := len(g.inflight), len(g.failed)
		g.mu.Unlock()
		if running != 0 || remembered != 0 {
			t.Errorf("after the cancel: %d runs in flight, %d failures remembered", running, remembered)
		}
		if _, err := os.Stat(filepath.Join(g.dir, cacheName(src)+".tmp")); !os.IsNotExist(err) {
			t.Errorf("half-written file left: %v", err)
		}
	})
	t.Run("a play waits", func(t *testing.T) {
		f := newFeedRun()
		g := newFeedGen(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		src := embedded(t, 3)
		filled := make(chan error, 1)
		go func() { filled <- g.Fill(ctx, []Source{src}, nil, nil) }()
		<-f.live
		fo, err := g.Follow(t.Context(), src, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer fo.Close()
		cancel()
		<-filled
		ch := pump(fo)
		f.feed <- "WEBVTT\n\n"
		take(t, ch, "WEBVTT\n\n") // the run is alive
		close(f.feed)
		if p := <-ch; p.err != io.EOF {
			t.Errorf("end: %+v", p)
		}
	})
}

// The file of a run under way survives Prune however old it looks: a run suspended for
// the length of a film does not write for hours.
func TestPruneKeepsTheFileOfARunUnderWay(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	<-f.live
	f.feed <- "WEBVTT\n\n"

	mine := filepath.Join(g.dir, cacheName(src)+".tmp")
	stale := filepath.Join(g.dir, "1-9-5.vtt.tmp") // a crashed run's
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{mine, stale} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Prune(map[int64]int64{1: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("setup: Prune left the crashed run's file") // positive control: it does remove stale ones
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("Prune removed the file of a run under way: %v", err)
	}
	close(f.feed)
	if _, err := io.ReadAll(fo); err != nil {
		t.Errorf("the run did not finish: %v", err)
	}
}

// The time a run may take does not run while the run is suspended.
func TestBudgetStopsWhileSuspended(t *testing.T) {
	var expired atomic.Bool
	b := newBudget(250*time.Millisecond, func() { expired.Store(true) })
	defer b.close()
	time.Sleep(100 * time.Millisecond)
	b.stop()
	time.Sleep(500 * time.Millisecond) // twice the whole budget, suspended
	if expired.Load() {
		t.Fatal("the budget ran out while the run was suspended")
	}
	b.start()
	time.Sleep(50 * time.Millisecond)
	if expired.Load() {
		t.Fatal("the budget ran out early: 150 ms of 250 ms have been used")
	}
	waitFor(t, "the rest of the budget to run out", expired.Load)
}
