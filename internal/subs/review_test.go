package subs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// What ffmpeg's webvtt muxer leaves on disk after a cue is "...one\n": the blank line comes
// before the NEXT cue. A follower that waited for it would show each cue when the next one
// began. The cue is whole once the file has not grown past it for a poll.
func TestFollowDoesNotHoldTheLastCueBackForTheNext(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	fo, err := g.Follow(t.Context(), embedded(t, 3), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)

	f.feed <- "WEBVTT\n\n00:01.000 --> 00:02.000\none\n" // exactly what is on disk after the first cue
	take(t, ch, "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n")
	quiet(t, ch, "the cue is given out once")

	// The next cue, as the muxer writes it: the blank line first. A cue still being written
	// (no line feed at its end yet) waits for it.
	f.feed <- "\n00:07.000 --> 00:08.000\ntw"
	quiet(t, ch, "half a cue is not given out")
	f.feed <- "o\n"
	take(t, ch, "\n00:07.000 --> 00:08.000\ntwo\n\n")
	close(f.feed)
	drain(t, ch)
}

// A run's end gives out the tail exactly as it is in the file (the cue the file ends with, with
// no blank line added), not a line more.
func TestFollowFinishGivesOutTheTail(t *testing.T) {
	old := followEvery
	followEvery = 400 * time.Millisecond // the run ends well before the poll that would release the tail
	t.Cleanup(func() { followEvery = old })
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)

	const whole = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n"
	f.feed <- whole
	take(t, ch, "WEBVTT\n\n") // read by a poll: the tail is in hand, a poll from being given out
	close(f.feed)
	take(t, ch, "00:01.000 --> 00:02.000\none\n")
	if p := <-ch; p.err != io.EOF || p.data != "" {
		t.Errorf("after the tail: %+v, want io.EOF with no line added", p)
	}
	if b, _ := os.ReadFile(filepath.Join(g.dir, cacheName(src))); string(b) != whole {
		t.Errorf("cache = %q", b)
	}
}

// The one-cue rule, step by step: a cue that ends in a line feed is given out (with the blank line
// added) once the file has not grown past it for a poll's time, and never at the end of a run, which
// gives it out as it is.
func TestPullGivesOutAnUnfinishedCueOnlyAfterAPollAndNotAtTheEnd(t *testing.T) {
	old := followEvery
	followEvery = 400 * time.Millisecond
	t.Cleanup(func() { followEvery = old })
	dir := t.TempDir()
	tmp := filepath.Join(dir, "x.vtt.tmp")
	const head, tail = "WEBVTT\n\n", "00:01.000 --> 00:02.000\none\n"
	if err := os.WriteFile(tmp, []byte(head+tail), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &call{done: make(chan struct{})}
	c.gen.Store(1)
	f := &Follower{ctx: t.Context(), c: c, tmp: tmp, dst: filepath.Join(dir, "x.vtt"), gen: 1}
	defer f.Close()

	f.pull(false)
	if string(f.out) != head {
		t.Fatalf("first look gave out %q, want the header only", f.out)
	}
	f.pull(false) // nothing new, but the cue was written just now
	if string(f.out) != head {
		t.Fatalf("a cue the file has not grown past for no time at all was given out: %q", f.out)
	}
	f.grewAt = time.Now().Add(-time.Second) // a poll has passed
	f.pull(true)                            // the run is over: finish gives it out, as it is
	if string(f.out) != head {
		t.Fatalf("a look at the end of the run added the blank line: %q", f.out)
	}
	f.pull(false)
	if string(f.out) != head+tail+"\n" {
		t.Errorf("after a poll: %q, want the cue with its blank line", f.out)
	}
}

// retryingRun is an ffmpeg that cannot do two tracks at once: with several outputs it writes
// the first cue of the first and fails; alone, the first track is done.
type retryingRun struct {
	calls     atomic.Int64
	started   chan struct{} // the first (failing) start has written its cue
	fail      chan struct{} // closed to let it fail
	mu        sync.Mutex
	argv      [][]string
	staleSeen atomic.Bool // a file of an earlier start was there when a new one began
}

func (r *retryingRun) run(ctx context.Context, args []string, _ *ctl) (string, error) {
	n := r.calls.Add(1)
	r.mu.Lock()
	r.argv = append(r.argv, args)
	r.mu.Unlock()
	var outs []string
	for _, a := range args {
		if strings.HasSuffix(a, ".tmp") {
			if _, err := os.Stat(a); err == nil {
				r.staleSeen.Store(true)
			}
			outs = append(outs, a)
		}
	}
	const head = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	if n == 1 { // several tracks: the first cue, then the failure
		if err := os.WriteFile(outs[0], []byte(head), 0o644); err != nil {
			return "", err
		}
		close(r.started)
		select {
		case <-r.fail:
		case <-ctx.Done():
		}
		return "Error while processing the subtitle\nmore", errors.New("exit status 1")
	}
	return "", os.WriteFile(outs[0], []byte(head+"00:03.000 --> 00:04.000\ntwo\n\n"), 0o644)
}

// One track ffmpeg cannot do must not cost the one that was asked for: the run is tried again
// with that track alone, a reader that has read the first start's cues gets the rest without a
// cue twice, and the track that was given up is not held against its file.
func TestAFailedRunOfSeveralTracksIsTriedAgainWithTheFirstAlone(t *testing.T) {
	r := &retryingRun{started: make(chan struct{}), fail: make(chan struct{})}
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = r.run
	t.Cleanup(g.Close)
	input := existingFile(t, "a.mkv")
	a, b := track(input, 3), track(input, 5)

	fo, err := g.Follow(t.Context(), a, []Source{b})
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)
	<-r.started
	const first = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n\n"
	take(t, ch, first) // read from the first start's file
	close(r.fail)

	take(t, ch, "00:03.000 --> 00:04.000\ntwo\n\n") // the second start's, from where the reader was
	if p := <-ch; p.err != io.EOF {
		t.Fatalf("end: %+v", p)
	}
	if n := r.calls.Load(); n != 2 {
		t.Errorf("ffmpeg started %d times, want 2 (both tracks, then the first alone)", n)
	}
	if !strings.Contains(strings.Join(r.argv[0], " "), "-map 0:5") || strings.Contains(strings.Join(r.argv[1], " "), "-map 0:5") {
		t.Errorf("starts = %v, want both tracks and then only the first", r.argv)
	}
	if !g.Cached(a) || g.Cached(b) {
		t.Errorf("cached: a %v, b %v; want the first only", g.Cached(a), g.Cached(b))
	}
	if r.staleSeen.Load() {
		t.Error("the second start found the first one's file")
	}

	// The one that was given up: asked for, it is not the file's fault (a 503 to retry, not a 422).
	if err := g.Failed(b); err != nil {
		t.Errorf("the dropped track is recorded as failed: %v", err)
	}
	g.mu.Lock()
	remembered := len(g.failed)
	g.mu.Unlock()
	if remembered != 0 {
		t.Errorf("%d failures remembered, want none: the first track worked alone", remembered)
	}
}

// A track that fails alone is a failure of that track: recorded, on disk too.
func TestAFailureIsRecordedOnDiskAndFillDoesNotTryItAgain(t *testing.T) {
	var runs atomic.Int64
	dir := filepath.Join(t.TempDir(), "subs")
	g, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var argv [][]string
	var mu sync.Mutex
	g.run = func(ctx context.Context, args []string, _ *ctl) (string, error) {
		runs.Add(1)
		mu.Lock()
		argv = append(argv, args)
		mu.Unlock()
		if strings.Contains(strings.Join(args, " "), "-map 0:3") {
			return "bad stream", errors.New("exit status 1")
		}
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") {
				if err := os.WriteFile(a, []byte("WEBVTT\n\n"), 0o644); err != nil {
					return "", err
				}
			}
		}
		return "", nil
	}
	t.Cleanup(g.Close)
	input := existingFile(t, "a.mkv")
	bad, good := track(input, 3), track(input, 5)

	err = g.Fill(t.Context(), []Source{bad}, nil, nil)
	if !errors.Is(err, ErrConvert) {
		t.Fatalf("Fill of a track that fails = %v, want ErrConvert", err)
	}
	if err := g.Failed(bad); !errors.Is(err, ErrConvert) {
		t.Fatalf("Failed = %v, want the failure on disk", err)
	}
	if g.Failed(good) != nil {
		t.Error("a track that did not fail is recorded as failed") // positive control
	}
	before := runs.Load()

	// A new generator (ply started again) on the same directory knows, and Fill does not read the
	// film again for it; the others of the same file still go.
	g2, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	g2.run = g.run
	t.Cleanup(g2.Close)
	err = g2.Fill(t.Context(), []Source{bad, good}, nil, nil)
	if !errors.Is(err, ErrConvert) {
		t.Errorf("Fill = %v, want the recorded failure of the bad track", err)
	}
	if !g2.Cached(good) {
		t.Error("the good track was not extracted after the bad one was skipped")
	}
	if got := strings.Join(argv[len(argv)-1], " "); runs.Load() != before+1 || strings.Contains(got, "-map 0:3") {
		t.Errorf("after the restart %d more runs, the last %q: want one, without the track that failed", runs.Load()-before, got)
	}

	// A person waiting for it is not refused (the failure may have been a bad moment): it is tried,
	// and when it works the record goes.
	g2.run = func(ctx context.Context, args []string, _ *ctl) (string, error) {
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") {
				return "", os.WriteFile(a, []byte("WEBVTT\n\n"), 0o644)
			}
		}
		return "", nil
	}
	if _, err := g2.Path(t.Context(), bad); err != nil {
		t.Fatalf("a person's request for the track that failed before: %v", err)
	}
	if g2.Failed(bad) != nil {
		t.Error("the record of the failure is still there after the track worked")
	}
}

// Prune keeps the record of a failure while the file is as it was, and drops it with the file.
func TestPruneKeepsFailureRecordsOfLiveItems(t *testing.T) {
	g, _ := New(Options{Dir: t.TempDir()})
	for _, name := range []string{"1-3-100.fail", "1-3-90.fail", "2-3-100.fail", "1-ext0-777-0a0b0c0d.fail"} {
		if err := os.WriteFile(filepath.Join(g.dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Prune(map[int64]int64{1: 100}); err != nil {
		t.Fatal(err)
	}
	for name, kept := range map[string]bool{"1-3-100.fail": true, "1-3-90.fail": false, "2-3-100.fail": false, "1-ext0-777-0a0b0c0d.fail": true} {
		_, err := os.Stat(filepath.Join(g.dir, name))
		if (err == nil) != kept {
			t.Errorf("%s kept = %v, want %v", name, err == nil, kept)
		}
	}
}

// A sidecar that fails does not keep the container's tracks from being extracted, and the
// sidecars go first (they are a few kilobytes: the viewer's file before the film's).
func TestAFailingSidecarDoesNotFailTheContainersTracks(t *testing.T) {
	var mu sync.Mutex
	var order []string
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = func(ctx context.Context, args []string, _ *ctl) (string, error) {
		joined := strings.Join(args, " ")
		mu.Lock()
		if strings.Contains(joined, "-map") {
			order = append(order, "container")
		} else {
			order = append(order, "sidecar")
		}
		mu.Unlock()
		if !strings.Contains(joined, "-map") {
			return "Invalid data found", errors.New("exit status 1")
		}
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") {
				if err := os.WriteFile(a, []byte("WEBVTT\n\n"), 0o644); err != nil {
					return "", err
				}
			}
		}
		return "", nil
	}
	t.Cleanup(g.Close)
	input, side := existingFile(t, "a.mkv"), existingFile(t, "a.srt")
	srcs := []Source{track(input, 3), {ItemID: 1, Key: "ext0", Input: side, Stream: -1, MTime: 7}, track(input, 5)}

	err = g.Fill(t.Context(), srcs, nil, nil)
	if !errors.Is(err, ErrConvert) {
		t.Errorf("Fill = %v, want the sidecar's failure", err)
	}
	if !g.Cached(srcs[0]) || !g.Cached(srcs[2]) {
		t.Errorf("tracks cached: %v %v; want both despite the sidecar", g.Cached(srcs[0]), g.Cached(srcs[2]))
	}
	if strings.Join(order, ",") != "sidecar,container" {
		t.Errorf("order of the runs = %v, want the sidecar first, then one for the container", order)
	}
}

// A request for a track while a background run has the only background slot does not wait for
// the slot: the person's runs and the library's are counted apart.
func TestARunSomebodyWaitsForDoesNotWaitForBackgroundSlots(t *testing.T) {
	f := newFeedRun()
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs"), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	g.run = f.run
	t.Cleanup(g.Close)

	filled := make(chan error, 1)
	bg := embedded(t, 3)
	go func() {
		filled <- g.Fill(t.Context(), []Source{bg}, func() (bool, string) { return true, "a play" }, nil)
	}()
	<-f.live // the background run holds the only slot, and is held

	other := embedded(t, 4)
	other.ItemID = 2
	fo, err := g.Follow(t.Context(), other, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	waitFor(t, "the person's run to start beside the background one", func() bool { return f.calls.Load() == 2 })

	// Positive control: a second background run does wait for the slot.
	third := embedded(t, 5)
	third.ItemID = 3
	go func() { _ = g.Fill(t.Context(), []Source{third}, nil, nil) }()
	time.Sleep(100 * time.Millisecond)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("a second background run started (%d runs) although the only background slot is taken", n)
	}
}

// What a background run says about waiting is cleared when a person starts to wait for it: the
// Activity page must not go on saying "waiting for a play" for a run a person is watching.
func TestNoteClearsWhenAPlayStartsToWaitForARun(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	var mu sync.Mutex
	var notes []string
	note := func(why string) { mu.Lock(); notes = append(notes, why); mu.Unlock() }
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(notes) == 0 {
			return "<none>"
		}
		return notes[len(notes)-1]
	}
	go func() {
		_ = g.Fill(t.Context(), []Source{src}, func() (bool, string) { return true, "a play reads the disk" }, note)
	}()
	<-f.live
	waitFor(t, "the run to say why it waits", func() bool { return last() == "a play reads the disk" })

	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	waitFor(t, "the run to stop saying it waits", func() bool { return last() == "" })
}

// Fill does not return while the pacer is still asking: the generator leaves no goroutine of a
// run behind.
func TestFillJoinsThePacer(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	src := embedded(t, 3)
	var calls atomic.Int64
	gate := make(chan struct{})
	pace := func() (bool, string) {
		if calls.Add(1) > 1 {
			<-gate // a slow answer (a stat of a share), still going when the run ends
		}
		return false, ""
	}
	filled := make(chan error, 1)
	go func() { filled <- g.Fill(t.Context(), []Source{src}, pace, nil) }()
	<-f.live
	waitFor(t, "the pacer to be in a slow question", func() bool { return calls.Load() >= 2 })
	f.feed <- "WEBVTT\n\n"
	close(f.feed) // the run ends

	select {
	case err := <-filled:
		t.Fatalf("Fill returned (%v) while the pacer was still asking", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	select {
	case err := <-filled:
		if err != nil {
			t.Errorf("Fill = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fill never returned")
	}
}

// A file that an earlier start of ply left half written is not what the new run's follower reads: it
// is removed before ffmpeg starts (ffmpeg would overwrite it, but a follower is quicker, and the
// viewer would be shown the cues of a run that is over).
func TestAStaleTempFileIsRemovedBeforeARunStarts(t *testing.T) {
	start := make(chan struct{}) // the moment ffmpeg gets to open its output
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = func(ctx context.Context, args []string, _ *ctl) (string, error) {
		select {
		case <-start:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") {
				return "", os.WriteFile(a, []byte("WEBVTT\n\n00:09.000 --> 00:10.000\nnew\n\n"), 0o644)
			}
		}
		return "", nil
	}
	t.Cleanup(g.Close)
	src := embedded(t, 3)
	stale := filepath.Join(g.dir, cacheName(src)+".tmp")
	if err := os.WriteFile(stale, []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nstale\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	ch := pump(fo)
	quiet(t, ch, "the cues of an earlier run are given out")
	close(start)
	var got string
	for p := range ch {
		got += p.data
		if p.err != nil {
			break
		}
	}
	if got != "WEBVTT\n\n00:09.000 --> 00:10.000\nnew\n\n" {
		t.Errorf("the viewer was given %q", got)
	}
}

// A track with a failure record on disk is not taken along in a viewer's run: only batch[0] is the
// viewer's request, and reading the film for hours with a track known to fail in it was what the
// record was made to stop. Asked for itself, the same track is tried.
func TestAKnownBadSiblingDoesNotRideAlongWithAViewersRun(t *testing.T) {
	f := newFeedRun()
	g := newFeedGen(t, f)
	input := existingFile(t, "a.mkv")
	a, bad, good := track(input, 3), track(input, 5), track(input, 6)
	if err := os.WriteFile(filepath.Join(g.dir, failName(cacheName(bad))), []byte("subs: conversion failed: exit status 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g.Failed(bad) == nil || g.Failed(good) != nil {
		t.Fatalf("setup: records: bad %v, good %v", g.Failed(bad), g.Failed(good))
	}
	fo, err := g.Follow(t.Context(), a, []Source{a, bad, good})
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	<-f.live
	args := f.lastArgs()
	if !strings.Contains(args, "-map 0:3") || !strings.Contains(args, "-map 0:6") { // positive control
		t.Errorf("the viewer's run lacks the track asked for or a sibling that did not fail: %s", args)
	}
	if strings.Contains(args, "-map 0:5") {
		t.Errorf("the viewer's run carries a track that failed before: %s", args)
	}
	f.feed <- "WEBVTT\n\n"
	close(f.feed)
	<-fo.Done()

	// A person who asks for that track is not refused (the failure may have been a bad moment).
	f2 := newFeedRun()
	g.run = f2.run
	fb, err := g.Follow(t.Context(), bad, nil)
	if err != nil {
		t.Fatalf("a viewer's request for the track that failed before: %v", err)
	}
	defer fb.Close()
	waitFor(t, "a run for the track that failed before", func() bool { return f2.calls.Load() == 1 })
	if !strings.Contains(f2.lastArgs(), "-map 0:5") {
		t.Errorf("the track that failed before was not tried when asked for: %s", f2.lastArgs())
	}
}

// A track the narrowed retry gave up is let go of at once: the viewer who asks for it meanwhile gets
// a run of its own, not the survivor's second read of the film. And when the survivor ends it does
// not take the name of that other run with it.
func TestADroppedTrackGetsARunOfItsOwnWhileTheSurvivorReads(t *testing.T) {
	var calls atomic.Int64
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = func(ctx context.Context, args []string, _ *ctl) (string, error) {
		calls.Add(1)
		joined := strings.Join(args, " ")
		var out string
		for _, a := range args {
			if strings.HasSuffix(a, ".tmp") && out == "" {
				out = a
			}
		}
		switch {
		case strings.Contains(joined, "-map 0:3") && strings.Contains(joined, "-map 0:5"):
			return "bad stream", errors.New("exit status 1")
		case strings.Contains(joined, "-map 0:3"): // the survivor, alone: a long film
			select {
			case <-releaseA:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		default: // the dropped track on its own
			select {
			case <-releaseB:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "", os.WriteFile(out, []byte("WEBVTT\n\n"), 0o644)
	}
	t.Cleanup(g.Close)
	input := existingFile(t, "a.mkv")
	a, b := track(input, 3), track(input, 5)

	fa, err := g.Follow(t.Context(), a, []Source{b})
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	waitFor(t, "the narrowed start", func() bool { return calls.Load() == 2 })

	fb, err := g.Follow(t.Context(), b, nil) // the viewer switches to the other language
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	waitFor(t, "a run of its own for the dropped track, beside the survivor's", func() bool { return calls.Load() == 3 })
	select {
	case <-fa.Done():
		t.Fatal("setup: the survivor's read is over") // positive control: it goes on
	default:
	}

	close(releaseA)
	<-fa.Done()
	if _, err := fa.Result(); err != nil {
		t.Fatalf("the survivor: %v", err)
	}
	// The survivor's end must leave the other run's name alone: a third request joins that run.
	fb2, err := g.Follow(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fb2.Close()
	if fb2.c != fb.c || calls.Load() != 3 {
		t.Errorf("a request after the survivor ended did not join the dropped track's run (%d starts)", calls.Load())
	}
	select {
	case <-fb.Done():
		t.Fatal("setup: the dropped track's run is over")
	default:
	}
	close(releaseB)
	<-fb.Done()
	if _, err := fb.Result(); err != nil {
		t.Errorf("the dropped track's own run: %v", err)
	}
	if !g.Cached(a) || !g.Cached(b) {
		t.Errorf("cached: a %v, b %v", g.Cached(a), g.Cached(b))
	}
	g.mu.Lock()
	left := len(g.inflight)
	g.mu.Unlock()
	if left != 0 {
		t.Errorf("%d runs still in flight", left)
	}
}

// A reader that was given a cue by the one-cue rule, and whose run is then started again, is not given
// that cue a second time: what the rule gave out counts as read.
func TestARestartDoesNotRepeatACueTheOneCueRuleGaveOut(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "x.vtt.tmp")
	const head, cue1, cue2 = "WEBVTT\n\n", "00:01.000 --> 00:02.000\none\n", "\n00:03.000 --> 00:04.000\ntwo\n"
	if err := os.WriteFile(tmp, []byte(head+cue1), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &call{done: make(chan struct{})}
	c.gen.Store(1)
	f := &Follower{ctx: t.Context(), c: c, tmp: tmp, dst: filepath.Join(dir, "x.vtt"), gen: 1}
	defer f.Close()
	f.pull(false)
	f.grewAt = time.Now().Add(-time.Second)
	f.pull(false)
	if got := string(f.out); got != head+cue1+"\n" {
		t.Fatalf("setup: first start gave out %q", got) // positive control: the rule fired
	}

	c.gen.Add(1) // the second start writes the same cues again
	if err := os.WriteFile(tmp, []byte(head+cue1+cue2), 0o644); err != nil {
		t.Fatal(err)
	}
	f.out = nil
	f.pull(false)
	f.grewAt = time.Now().Add(-time.Second)
	f.pull(false)
	if got := string(f.out); got != cue2+"\n" {
		t.Errorf("after the restart the reader was given %q, want the second cue only", got)
	}
}
