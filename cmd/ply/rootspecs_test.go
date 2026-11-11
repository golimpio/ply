package main

import (
	"testing"

	"ply/internal/library"
	"ply/internal/watch"
)

// A movies root needs only itself: a film is a directory of its own under it, so a new film fires an
// event on the root. A shows root needs its show directories too, because a season (and the episode
// in it) sits below the show. See internal/watch.RootSpec for why the depth matters at all.
func TestRootSpecsByKind(t *testing.T) {
	got := rootSpecs([]library.Root{
		{Path: "/media/movies", Kind: library.KindMovies},
		{Path: "/media/tv", Kind: library.KindShows},
	})
	want := []watch.RootSpec{
		{Path: "/media/movies", Depth: 0},
		{Path: "/media/tv", Depth: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("rootSpecs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rootSpecs[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
