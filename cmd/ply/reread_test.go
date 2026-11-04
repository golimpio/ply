package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
)

// videoProber says every file is a video.
type videoProber struct{}

func (videoProber) Probe(ctx context.Context, path string) (probe.Result, error) {
	return probe.Result{Container: "mkv", DurationSec: 60, Bitrate: 1000,
		Streams: []probe.Stream{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}}}, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// legacyShow makes a TV folder as an earlier version read it: the show of
// "Lost (2004)/Season 1/Lost.S01E01.mkv" is "Lost".
func legacyShow(t *testing.T, st *store.Store) (root string, files []string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "tv")
	for i, n := range []string{"Lost.S01E01.mkv", "Lost.S01E02.mkv"} {
		p := filepath.Join(root, "Lost (2004)", "Season 1", n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, i+1), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	if err := library.NewScanner(context.Background(), st, videoProber{}, []string{root}, nil).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return root, files
}

func showsOf(t *testing.T, st *store.Store, root string) []string {
	t.Helper()
	items, err := st.RootItems(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var shows []string
	for _, it := range items {
		shows = append(shows, it.Show)
	}
	return shows
}

// start does what a start does with the folders and the scanner, up to the point where
// the reading again is asked for; it returns the scanner so that the test can end the
// "process" there (a crash) or go on.
func start(t *testing.T, st *store.Store, flags []library.Root) *library.Scanner {
	t.Helper()
	ctx := context.Background()
	roots, before, err := prepareFolders(ctx, st, flags, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	scanner := library.NewScanner(ctx, st, videoProber{}, rootPaths(roots), quietLog())
	t.Cleanup(scanner.Wait)
	scanner.SetRoots(previousRoots(roots, before))
	scanner.SetRoots(roots)
	scanner.Wait()
	return scanner
}

// A folder typed on the command line for the first time is read again as its type says,
// and the note that it was to be is forgotten.
func TestAFirstTypedFolderIsReadAgainAtStart(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root, _ := legacyShow(t, st)
	if got := showsOf(t, st, root); !slices.Equal(got, []string{"Lost", "Lost"}) {
		t.Fatalf("control: the old reader's shows are %v", got)
	}
	scanner := start(t, st, []library.Root{{Path: root, Kind: library.KindShows}})
	rereadPending(ctx, st, scanner, quietLog())
	scanner.Wait()
	if got := showsOf(t, st, root); !slices.Equal(got, []string{"Lost (2004)", "Lost (2004)"}) {
		t.Errorf("shows after the start = %v; want what a shows folder says", got)
	}
	if got, _ := pendingRereads(ctx, st); len(got) != 0 {
		t.Errorf("still noted as to be read again after it was: %v", got)
	}
}

// The start ends after the folder is saved and before it is read again: the next start
// reads it, though the folder is no longer new.
func TestARereadSurvivesAStartThatEndsAfterTheFolderIsSaved(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root, _ := legacyShow(t, st)
	flags := []library.Root{{Path: root, Kind: library.KindShows}}

	// First start: the folder is saved (and noted); the process ends before Reread.
	if _, _, err := prepareFolders(ctx, st, flags, quietLog()); err != nil {
		t.Fatal(err)
	}
	if got := showsOf(t, st, root); !slices.Equal(got, []string{"Lost", "Lost"}) {
		t.Fatalf("control: read again by the first start: %v", got)
	}
	saved, _ := storedKinds(ctx, st)
	if got := firstTyped(flags, saved); len(got) != 0 {
		t.Fatalf("control: the saved folder is still new: %v", got)
	}

	// Second start.
	scanner := start(t, st, flags)
	rereadPending(ctx, st, scanner, quietLog())
	scanner.Wait()
	if got := showsOf(t, st, root); !slices.Equal(got, []string{"Lost (2004)", "Lost (2004)"}) {
		t.Errorf("shows after the second start = %v; the re-reading was lost", got)
	}
	if got, _ := pendingRereads(ctx, st); len(got) != 0 {
		t.Errorf("still noted: %v", got)
	}
}

// The folders are saved one after the other; the start fails at the second (a type that
// cannot be saved) and ends. The first was saved and must still be read again.
func TestARereadIsNotLostWhenSavingALaterFolderFails(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root, _ := legacyShow(t, st)
	flags := []library.Root{{Path: root, Kind: library.KindShows}, {Path: filepath.Join(t.TempDir(), "x"), Kind: "bogus"}}
	if _, _, err := prepareFolders(ctx, st, flags, quietLog()); err == nil {
		t.Fatal("control: the start did not fail")
	}
	saved, _ := storedKinds(ctx, st)
	if _, ok := saved[root]; !ok {
		t.Fatal("control: the first folder was not saved before the failure")
	}

	scanner := start(t, st, flags[:1])
	rereadPending(ctx, st, scanner, quietLog())
	scanner.Wait()
	if got := showsOf(t, st, root); !slices.Equal(got, []string{"Lost (2004)", "Lost (2004)"}) {
		t.Errorf("shows after the next start = %v; the re-reading was lost", got)
	}
}

// The note of a folder goes when it was read, and stays when it could not be.
func TestTheNoteOfAFolderThatCouldNotBeReadStays(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := markRereads(ctx, st, []string{"/a", "/b"}); err != nil {
		t.Fatal(err)
	}
	done := forgetReread(ctx, st, quietLog())
	done("/a", nil)
	done("/b", errors.New("disk"))
	if got, _ := pendingRereads(ctx, st); !slices.Equal(got, []string{"/b"}) {
		t.Errorf("still noted = %v, want only the one that failed", got)
	}
}

// A note for a folder that is no longer one is forgotten without reading anything.
func TestARereadNoteOfAFolderThatIsGoneIsForgotten(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	gone := filepath.Join(t.TempDir(), "gone")
	if err := markRereads(ctx, st, []string{gone}); err != nil {
		t.Fatal(err)
	}
	scanner := start(t, st, nil)
	rereadPending(ctx, st, scanner, quietLog())
	scanner.Wait()
	if got, _ := pendingRereads(ctx, st); len(got) != 0 {
		t.Errorf("still noted: %v", got)
	}
}
