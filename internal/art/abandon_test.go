package art

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// A fast scroll asks for hundreds of images and the browser gives up on most of
// them a frame later. A frame grab that no request waits for any more is stopped
// (dequeued, or ffmpeg killed), unless another request, or a background job
// (art.grab), still waits for the same image.

func (g *Generator) waiters(id, mtime int64, k Kind) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.inflight[imageName(id, k, mtime)]; ok {
		return c.refs.Waiters()
	}
	return -1
}

// abandoned reports whether the grab of the image is under way and nobody waits for it.
func (g *Generator) abandoned(id, mtime int64, k Kind) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.inflight[imageName(id, k, mtime)]
	return ok && c.refs.Abandoned()
}

func (g *Generator) running() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inflight)
}

func (g *Generator) remembered() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.failed)
}

// pathAsync asks for an image in the background and reports how it ended.
func pathAsync(ctx context.Context, g *Generator, id int64) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := g.Path(ctx, src(id, 10), Thumb); done <- err }()
	return done
}

func TestLastRequestLeavingStopsTheGrab(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)

	ctx, leave := context.WithCancel(context.Background())
	done := pathAsync(ctx, g, 1)
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 }) // ffmpeg is running
	if s.cancels() != 0 {
		t.Fatal("setup: the run was cancelled before anyone left")
	}

	leave()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request that left: %v", err)
	}
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 0 && s.cancels() == 1 }) // ffmpeg was stopped
	waitFor(t, func() bool { return g.running() == 0 })
	if g.remembered() != 0 {
		t.Errorf("a grab nobody waited for any more is remembered as a failure")
	}

	// Positive control for the above: asked again, the image is made.
	close(s.block)
	p, err := g.Path(context.Background(), src(1, 10), Thumb)
	if err != nil {
		t.Fatalf("asked again: %v", err)
	}
	if _, err := os.Stat(p); err != nil || g.Runs() != 2 {
		t.Errorf("image %v, runs %d; want a second run that made it", err, g.Runs())
	}
}

func TestOneOfTwoRequestsLeavingKeepsTheGrab(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)

	ctx, leave := context.WithCancel(context.Background())
	first := pathAsync(ctx, g, 1)
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })
	second := pathAsync(context.Background(), g, 1)
	waitFor(t, func() bool { return g.waiters(1, 10, Thumb) == 2 })

	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request that left: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if a, _ := s.snapshot(); a != 1 || s.cancels() != 0 {
		t.Fatalf("the grab was stopped while a request waited for it (running %d, cancelled %d)", a, s.cancels())
	}
	close(s.block)
	if err := <-second; err != nil {
		t.Fatalf("the request that stayed: %v", err)
	}
	if g.Runs() != 1 {
		t.Errorf("runs = %d, want the one both requests shared", g.Runs())
	}
}

// A grab the queue makes (art.grab calls Path with the job's context) goes on when
// the clients that asked for the same image leave, whichever asked first.
func TestBackgroundGrabSurvivesTheClientLeaving(t *testing.T) {
	for name, jobFirst := range map[string]bool{"client first": false, "job first": true} {
		t.Run(name, func(t *testing.T) {
			s := &stub{block: make(chan struct{})}
			g := newTestGenerator(t, t.TempDir(), s)
			ctx, leave := context.WithCancel(context.Background())
			var client, job <-chan error
			if jobFirst {
				job = pathAsync(context.Background(), g, 1)
				waitFor(t, func() bool { return g.waiters(1, 10, Thumb) == 1 })
				client = pathAsync(ctx, g, 1)
			} else {
				client = pathAsync(ctx, g, 1)
				waitFor(t, func() bool { return g.waiters(1, 10, Thumb) == 1 })
				job = pathAsync(context.Background(), g, 1)
			}
			waitFor(t, func() bool { return g.waiters(1, 10, Thumb) == 2 })

			leave()
			if err := <-client; !errors.Is(err, context.Canceled) {
				t.Fatalf("the client that left: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			if s.cancels() != 0 {
				t.Fatal("the grab of the job was stopped when a client left")
			}
			close(s.block)
			if err := <-job; err != nil {
				t.Fatalf("the job: %v", err)
			}
			if _, ok := g.Cached(src(1, 10), Thumb); !ok || g.Runs() != 1 {
				t.Errorf("cached %v, runs %d; want the image made by the one run", ok, g.Runs())
			}
		})
	}
}

// A fling: grabs queued for a slot (the slots are taken by others) are dropped when
// their requests leave. They never start ffmpeg, free no slot they did not take, and
// leave no goroutine behind.
func TestQueuedGrabsOfClientsThatLeftNeverRun(t *testing.T) {
	s := &stub{block: make(chan struct{})}
	g := newTestGenerator(t, t.TempDir(), s)
	slots := cap(g.sem)
	start := runtime.NumGoroutine()

	// Two grabs that a job waits for hold the slots.
	held := make([]<-chan error, slots)
	for i := range held {
		held[i] = pathAsync(context.Background(), g, int64(i+1))
	}
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == slots })
	busy := runtime.NumGoroutine()

	const fling = 30
	ctx, leave := context.WithCancel(context.Background())
	flung := make([]<-chan error, fling)
	for i := range flung {
		flung[i] = pathAsync(ctx, g, int64(100+i))
	}
	waitFor(t, func() bool { return g.running() == slots+fling })
	leave()
	for i, done := range flung {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("request %d of the fling: %v", i, err)
		}
	}
	waitFor(t, func() bool { return g.running() == slots })
	waitFor(t, func() bool { return runtime.NumGoroutine() <= busy })
	if len(g.sem) != slots {
		t.Errorf("%d slots taken after the fling left, want the %d of the grabs under way", len(g.sem), slots)
	}
	if g.remembered() != 0 {
		t.Error("the grabs that nobody waited for are remembered as failures")
	}

	close(s.block)
	for i, done := range held {
		if err := <-done; err != nil {
			t.Errorf("held grab %d: %v", i, err)
		}
	}
	if _, peak := s.snapshot(); g.Runs() != int64(slots) || peak != slots {
		t.Errorf("ffmpeg ran %d times, %d at once; want %d and %d: the %d images of the fling were never grabbed", g.Runs(), peak, slots, slots, fling)
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= start })
}

// A slot that is free and a context that has ended are both ready to a grab that
// waits for a slot: it must not start ffmpeg for nobody.
func TestGrabWithAnEndedContextNeverRuns(t *testing.T) {
	s := &stub{}
	g := newTestGenerator(t, t.TempDir(), s)
	ctx, leave := context.WithCancel(context.Background())
	leave()
	for range 200 {
		if err := g.generate(ctx, src(1, 10), Thumb, t.TempDir()+"/x.jpg"); !errors.Is(err, context.Canceled) {
			t.Fatalf("generate = %v, want context.Canceled", err)
		}
	}
	if g.Runs() != 0 || len(g.sem) != 0 {
		t.Errorf("%d runs, %d slots taken after 200 grabs for nobody", g.Runs(), len(g.sem))
	}
	// Positive control: with a live context the same call runs.
	if err := g.generate(context.Background(), src(1, 10), Thumb, t.TempDir()+"/x.jpg"); err != nil || g.Runs() != 1 {
		t.Errorf("live grab: %v, %d runs", err, g.Runs())
	}
}

// An image asked for while the run nobody wanted is still being stopped (ffmpeg
// takes its time to die) waits for it to be gone: the two would write the same
// temporary file. The request is not given the error of the old run.
func TestAskedAgainWhileTheAbandonedGrabIsBeingStopped(t *testing.T) {
	s := &stub{block: make(chan struct{}), slowStop: 300 * time.Millisecond}
	g := newTestGenerator(t, t.TempDir(), s)

	ctx, leave := context.WithCancel(context.Background())
	first := pathAsync(ctx, g, 1)
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })
	leave()
	<-first
	waitFor(t, func() bool { return s.cancels() == 1 }) // the old run is dying, slowly

	second := pathAsync(context.Background(), g, 1)
	select {
	case err := <-second:
		t.Fatalf("answered (%v) while the old run was still being stopped", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(s.block)
	if err := <-second; err != nil {
		t.Fatalf("asked again: %v", err)
	}
	if _, peak := s.snapshot(); g.Runs() != 2 || peak != 1 {
		t.Errorf("%d runs, %d at once; want 2, one after the other", g.Runs(), peak)
	}
}

// The last request leaves in the window between ffmpeg failing for a reason of the file
// (no video in it) and the grab writing the failure down; here the failure is handed
// over after the leave, so that the order is certain. The grab is abandoned, but the
// failure is the file's own and is remembered, as it would have been a moment earlier.
func TestFailureOfTheFileIsRememberedEvenIfTheLastRequestLeftJustAfterIt(t *testing.T) {
	g := newTestGenerator(t, t.TempDir(), &stub{})
	entered, fail := make(chan struct{}, 1), make(chan struct{})
	g.run = func(ctx context.Context, args []string) ([]byte, error) {
		entered <- struct{}{}
		<-fail
		return nil, fmt.Errorf("%w: exit status 1: no video", ErrNoFrame)
	}

	ctx, leave := context.WithCancel(context.Background())
	done := pathAsync(ctx, g, 1)
	<-entered
	leave()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request that left: %v", err)
	}
	if !g.abandoned(1, 10, Thumb) || g.remembered() != 0 { // positive control: abandoned, and nothing is remembered yet
		t.Fatalf("setup: abandoned %v, %d failures remembered", g.abandoned(1, 10, Thumb), g.remembered())
	}

	close(fail)
	waitFor(t, func() bool { return g.running() == 0 })
	if g.remembered() != 1 {
		t.Fatalf("%d failures remembered, want the one of the file", g.remembered())
	}
	if _, err := g.Path(context.Background(), src(1, 10), Thumb); !errors.Is(err, ErrNoFrame) || g.Runs() != 1 {
		t.Errorf("asked again: %v after %d runs; want the remembered ErrNoFrame, with no new run", err, g.Runs())
	}
}

// The grab nobody waits for any more may still make the image while ffmpeg is being
// stopped. A request that came meanwhile and waited for it is given that image: it is
// not grabbed twice.
func TestAskedAgainAsTheAbandonedGrabMakesTheImage(t *testing.T) {
	s := &stub{}
	g := newTestGenerator(t, t.TempDir(), s)
	var started atomic.Bool
	entered, finish := make(chan struct{}, 1), make(chan struct{})
	g.run = func(ctx context.Context, args []string) ([]byte, error) {
		if started.CompareAndSwap(false, true) {
			entered <- struct{}{}
			<-ctx.Done() // nobody waits for it any more...
			<-finish     // ...but ffmpeg gets the frame all the same
		}
		return s.run(ctx, args)
	}

	ctx, leave := context.WithCancel(context.Background())
	first := pathAsync(ctx, g, 1)
	<-entered
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the request that left: %v", err)
	}

	second := pathAsync(context.Background(), g, 1)
	select {
	case err := <-second:
		t.Fatalf("answered (%v) while the old grab was still being stopped", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := <-second; err != nil {
		t.Fatalf("asked again: %v", err)
	}
	if _, ok := g.Cached(src(1, 10), Thumb); !ok || g.Runs() != 1 {
		t.Errorf("cached %v, runs %d; want the image made by the one grab", ok, g.Runs())
	}
}

// Defer hands back the grabs it stopped, to be made when the play is over: not one
// that nobody waits for.
func TestDeferLeavesAnAbandonedGrabAlone(t *testing.T) {
	s := &stub{block: make(chan struct{}), slowStop: 500 * time.Millisecond}
	g := newTestGenerator(t, t.TempDir(), s)

	ctx, leave := context.WithCancel(context.Background())
	gone := pathAsync(ctx, g, 1)
	stays := pathAsync(context.Background(), g, 2)
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 2 })
	leave()
	<-gone
	waitFor(t, func() bool { return s.cancels() == 1 }) // item 1's run is being stopped

	if got := g.Defer(1); len(got) != 0 {
		t.Errorf("Defer(1) = %+v: the grab nobody waited for is not to be made again", got)
	}
	if got := g.Defer(2); len(got) != 1 { // positive control: a grab with a waiter is handed back
		t.Errorf("Defer(2) = %+v, want the grab of item 2", got)
	}
	if err := <-stays; !errors.Is(err, ErrDeferred) {
		t.Errorf("the waiter of item 2: %v, want ErrDeferred", err)
	}
}
