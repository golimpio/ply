package art

import (
	"context"
	"errors"
	"os"
	"testing"
)

// A play that is starting takes the file back from a frame grab that is reading it: the
// grab is stopped, its waiters are told it was deferred, nothing is held against the
// file, and the grabs of other items go on.
func TestDeferStopsTheGrabsOfOneItem(t *testing.T) {
	s := &stub{block: make(chan struct{}), fill: [3]byte{10, 120, 200}}
	g := newTestGenerator(t, t.TempDir(), s)

	type result struct {
		item int64
		err  error
	}
	results := make(chan result, 3)
	grab := func(id int64, k Kind) {
		_, err := g.Path(context.Background(), src(id, 10), k)
		results <- result{id, err}
	}
	go grab(1, Thumb)
	go grab(2, Thumb)
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 2 }) // both are running

	got := g.Defer(1)
	if len(got) != 1 || got[0].Src.ID != 1 || got[0].Kind != Thumb || got[0].Src.Path != fixtureMedia {
		t.Fatalf("Defer(1) = %+v, want the thumb grab of item 1", got)
	}
	if r := <-results; r.item != 1 || !errors.Is(r.err, ErrDeferred) {
		t.Fatalf("the waiter of the deferred grab got %+v, want item 1 and ErrDeferred", r)
	}
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 }) // the stopped run is gone, the other is not

	// Item 2's grab was not touched (positive control: Defer is about one item).
	select {
	case r := <-results:
		t.Fatalf("the grab of item 2 ended early: %+v", r)
	default:
	}
	if got := g.Defer(3); len(got) != 0 {
		t.Errorf("Defer of an item with no grab = %+v", got)
	}
	close(s.block)
	if r := <-results; r.item != 2 || r.err != nil {
		t.Fatalf("item 2 = %+v, want its image", r)
	}

	// Nothing was remembered: asked again, item 1 is grabbed.
	runs := g.Runs()
	if _, err := g.Path(context.Background(), src(1, 10), Thumb); err != nil {
		t.Fatalf("the deferred item's next request = %v, want an image", err)
	}
	if g.Runs() != runs+1 {
		t.Errorf("runs %d -> %d, want one more run", runs, g.Runs())
	}
}

// Cached answers from the cache directory and never starts ffmpeg.
func TestCachedNeverGrabs(t *testing.T) {
	g := newTestGenerator(t, t.TempDir(), &stub{fill: [3]byte{200, 30, 30}})
	if _, ok := g.Cached(src(1, 10), Thumb); ok {
		t.Fatal("cached before anything was made")
	}
	if g.Runs() != 0 {
		t.Fatalf("Cached ran ffmpeg (%d runs)", g.Runs())
	}
	want, err := g.Path(context.Background(), src(1, 10), Thumb)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := g.Cached(src(1, 10), Thumb)
	if !ok || got != want {
		t.Errorf("Cached = %q, %v; want %q", got, ok, want)
	}
	if _, ok := g.Cached(src(1, 10), Backdrop); ok {
		t.Error("the backdrop is cached but only the thumb was made")
	}
	if _, ok := g.Cached(src(1, 11), Thumb); ok {
		t.Error("an image of an older mtime is reported for a changed file")
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
	if g.Runs() != 1 {
		t.Errorf("runs = %d, want only the one that made the image", g.Runs())
	}
}
