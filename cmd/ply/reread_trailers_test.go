package main

import (
	"context"
	"path/filepath"
	"testing"

	"ply/internal/library"
	"ply/internal/store"
)

// M13 reads every typed folder again once, to find the trailers an earlier version took
// for films; a later start does not.
func TestMarkTrailerRereadsOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	roots := []library.Root{{Path: "/m", Kind: library.KindMovies}, {Path: "/tv", Kind: library.KindShows}, {Path: "/x"}}
	if err := markTrailerRereads(ctx, st, roots); err != nil {
		t.Fatal(err)
	}
	if got, err := pendingRereads(ctx, st); err != nil || len(got) != 2 || got[0] != "/m" || got[1] != "/tv" {
		t.Fatalf("noted: %v, %v; want the two typed folders", got, err)
	}
	for _, p := range []string{"/m", "/tv"} {
		if err := st.DeleteSetting(ctx, rereadPrefix+p); err != nil {
			t.Fatal(err)
		}
	}
	if err := markTrailerRereads(ctx, st, roots); err != nil {
		t.Fatal(err)
	}
	if got, _ := pendingRereads(ctx, st); len(got) != 0 {
		t.Errorf("noted again at the next start: %v", got)
	}
}
