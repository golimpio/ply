package subs

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// blocks is what ffmpeg 9 writes for -progress pipe:1: one before the first packet of the
// track has been written (the time is not known), two that repeat it, one that moves, and the
// last. out_time_ms is in microseconds too.
const blocks = `frame=0
bitrate=N/A
total_size=N/A
out_time_us=N/A
out_time_ms=N/A
out_time=N/A
speed=N/A
progress=continue
bitrate=   0.1kbits/s
total_size=7
out_time_us=600000
out_time_ms=600000
out_time=00:00:00.600000
dup_frames=0
drop_frames=0
speed=1.19x
progress=continue
bitrate=   0.1kbits/s
total_size=7
out_time_us=600000
out_time_ms=600000
out_time=00:00:00.600000
speed=0.594x
progress=continue
bitrate=   0.0kbits/s
total_size=211
out_time_us=10600000
out_time_ms=10600000
out_time=00:00:10.600000
speed=1.02x
progress=end
`

func collect(chunks ...string) []time.Duration {
	var got []time.Duration
	w := &progressWriter{emit: func(at time.Duration) { got = append(got, at) }}
	for _, c := range chunks {
		if n, err := w.Write([]byte(c)); n != len(c) || err != nil {
			panic("progressWriter must take all it is given")
		}
	}
	return got
}

// The time of every block that has one, in order, the repeats and the last block included;
// the block that does not know the time says nothing.
func TestProgressWriterGivesTheTimeOfEachBlock(t *testing.T) {
	want := []time.Duration{600 * time.Millisecond, 600 * time.Millisecond, 10600 * time.Millisecond}
	if got := collect(blocks); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// ffmpeg writes in pieces that end anywhere, and it did not always have out_time_us.
func TestProgressWriterGathersLinesAcrossWrites(t *testing.T) {
	var pieces []string
	for i := 0; i < len(blocks); i += 7 {
		pieces = append(pieces, blocks[i:min(i+7, len(blocks))])
	}
	want := []time.Duration{600 * time.Millisecond, 600 * time.Millisecond, 10600 * time.Millisecond}
	if got := collect(pieces...); !slices.Equal(got, want) {
		t.Errorf("7 bytes at a time: got %v, want %v", got, want)
	}

	old := "bitrate=  1.0kbits/s\r\nout_time_ms=2500000\r\nout_time=00:00:02.500000\r\nprogress=continue\r\n"
	if got := collect(old); !slices.Equal(got, []time.Duration{2500 * time.Millisecond}) {
		t.Errorf("out_time_ms only, with CRLF: got %v", got)
	}
}

// A time that is not a time is not taken for one, and does not carry over to the next block.
func TestProgressWriterIgnoresWhatIsNotATime(t *testing.T) {
	in := "out_time_us=N/A\nprogress=continue\n" + // not known yet
		"out_time_us=-9223372036854775807\nprogress=continue\n" + // what older versions wrote for it
		"out_time_us=oops\nprogress=continue\n" +
		"out_time_us=9223372036854775807\nprogress=continue\n" + // would overflow a Duration
		"out_time_us=4000000\nprogress=continue\n" + // positive control: a time does come through
		"progress=continue\n" // and was told once
	if got := collect(in); !slices.Equal(got, []time.Duration{4 * time.Second}) {
		t.Errorf("got %v, want only 4s", got)
	}
}

// A line that never ends is not kept for ever, and the lines after it still count.
func TestProgressWriterDropsAnEndlessLine(t *testing.T) {
	junk := strings.Repeat("x", 3*maxProgressLine)
	got := collect(junk[:maxProgressLine], junk, "\nout_time_us=1000000\nprogress=continue\n")
	if !slices.Equal(got, []time.Duration{time.Second}) {
		t.Errorf("got %v", got)
	}
}

// Not even a single big write is kept: what is held of a line never passes maxProgressLine.
func TestProgressWriterKeepsNoMoreThanALineOfAnyWrite(t *testing.T) {
	var got []time.Duration
	w := &progressWriter{emit: func(at time.Duration) { got = append(got, at) }}

	big := []byte(strings.Repeat("x", 1<<20))
	if n, err := w.Write(big); n != len(big) || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(w.line) > maxProgressLine || cap(w.line) > 2*maxProgressLine {
		t.Errorf("kept %d bytes (room for %d) of one write of %d", len(w.line), cap(w.line), len(big))
	}
	// Positive control: what comes after the end of that line is read.
	w.Write([]byte("\nout_time_us=1000000\nprogress=continue\n"))
	if !slices.Equal(got, []time.Duration{time.Second}) {
		t.Errorf("got %v after the long line", got)
	}

	pieces := &progressWriter{} // and a line that comes in pieces
	for range 4 {
		pieces.Write(big[:maxProgressLine/2+1])
		if len(pieces.line) > maxProgressLine || cap(pieces.line) > 2*maxProgressLine {
			t.Fatalf("kept %d bytes (room for %d) of a line in pieces", len(pieces.line), cap(pieces.line))
		}
	}
}

// The rest of a line that was too long is dropped with it, even when it looks like a line, and a
// line of exactly the longest size is read.
func TestProgressWriterDropsTheRestOfALongLine(t *testing.T) {
	got := collect(strings.Repeat("x", maxProgressLine+1), "out_time_us=9000000\nprogress=continue\n",
		"out_time_us=1000000\nprogress=continue\n")
	if !slices.Equal(got, []time.Duration{time.Second}) {
		t.Errorf("got %v, want only 1s (the 9 s was the end of a long line)", got)
	}

	const tm = "out_time_us=2000000"
	exact := tm + strings.Repeat(" ", maxProgressLine-len(tm)) // as long as a line may be
	if got := collect(exact + "\nprogress=continue\n"); !slices.Equal(got, []time.Duration{2 * time.Second}) {
		t.Errorf("a line of %d bytes: got %v, want 2s", len(exact), got)
	}
	if got := collect(exact + " \nprogress=continue\n"); len(got) != 0 {
		t.Errorf("a line of %d bytes: got %v, want it dropped", len(exact)+1, got)
	}
}

// The run is told by whoever started ffmpeg; it passes on in order to every watcher, and a
// watcher that comes late is told where the run is first.
func TestCallTellsEveryWatcherInOrder(t *testing.T) {
	c := &call{}
	var mu sync.Mutex
	var early, late []time.Duration
	add := func(to *[]time.Duration) func(time.Duration) {
		return func(at time.Duration) { mu.Lock(); *to = append(*to, at); mu.Unlock() }
	}
	c.watch(nil) // nobody: nothing happens
	c.watch(add(&early))
	c.report(time.Second)
	c.report(2 * time.Second)
	c.watch(add(&late))
	c.report(3 * time.Second)

	if want := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}; !slices.Equal(early, want) {
		t.Errorf("early watcher: %v, want %v", early, want)
	}
	if want := []time.Duration{2 * time.Second, 3 * time.Second}; !slices.Equal(late, want) {
		t.Errorf("late watcher: %v, want %v (where the run was, then on)", late, want)
	}
}

// What Fill's caller hears of a run: the times ffmpeg reports, from the first; the arguments
// that make ffmpeg report; nothing for a sidecar, which is a few kilobytes.
func TestFillProgressTellsHowFarTheContainerIsRead(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)

	var mu sync.Mutex
	var got []time.Duration
	filled := make(chan error, 1)
	go func() {
		filled <- g.FillProgress(t.Context(), []Source{src}, nil, nil, func(at time.Duration) {
			mu.Lock()
			got = append(got, at)
			mu.Unlock()
		})
	}()
	<-f.live
	f.mu.Lock()
	p := f.ctl
	f.mu.Unlock()
	if p.progress == nil {
		t.Fatal("the run was not given a way to report")
	}
	p.progress(time.Minute)
	p.progress(2 * time.Minute)

	f.feed <- "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	close(f.feed)
	if err := <-filled; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []time.Duration{time.Minute, 2 * time.Minute}; !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
	if a := f.lastArgs(); !strings.Contains(a, "-progress pipe:1 -i ") {
		t.Errorf("ffmpeg is not asked for its progress, before the input: %s", a)
	}
}

// A Fill that finds the track being extracted for a play hears of the run too.
func TestFillProgressJoinsARunThatAPlayStarted(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)

	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	<-f.live
	f.mu.Lock()
	p := f.ctl
	f.mu.Unlock()
	p.progress(90 * time.Second) // before the Fill comes

	heard := make(chan time.Duration, 8)
	filled := make(chan error, 1)
	go func() {
		filled <- g.FillProgress(t.Context(), []Source{src}, nil, nil, func(at time.Duration) { heard <- at })
	}()
	select {
	case at := <-heard:
		if at != 90*time.Second {
			t.Errorf("first heard %v, want where the run was: 1m30s", at)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a Fill that joined the run heard nothing")
	}
	p.progress(100 * time.Second)
	if at := <-heard; at != 100*time.Second {
		t.Errorf("then heard %v, want 1m40s", at)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1", n)
	}
	f.feed <- "WEBVTT\n\n"
	close(f.feed)
	if err := <-filled; err != nil {
		t.Fatal(err)
	}
}

// A sidecar is read whole in a moment: ffmpeg is not asked for progress, so that the bar of
// the job is the container's alone.
func TestFillProgressIsNotAskedForASidecar(t *testing.T) {
	f := &fakeRun{}
	g := newGen(t, f)
	side := existingFile(t, "a.en.srt")
	src := Source{ItemID: 1, Key: "ext0", Input: side, Stream: -1, MTime: 7}

	if err := g.FillProgress(t.Context(), []Source{src}, nil, nil, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.args) != 1 {
		t.Fatalf("%d ffmpeg runs, want 1 (setup)", len(f.args)) // positive control: it did run
	}
	if a := strings.Join(f.args[0], " "); strings.Contains(a, "-progress") {
		t.Errorf("a sidecar's args ask for progress: %s", a)
	}
}
