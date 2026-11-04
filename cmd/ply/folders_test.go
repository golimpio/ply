package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ply/internal/library"
	"ply/internal/store"
)

func TestStartupRoots(t *testing.T) {
	ctx := context.Background()
	open := func(t *testing.T) *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	quiet := slog.New(slog.DiscardHandler)

	t.Run("no folders", func(t *testing.T) {
		roots, err := startupRoots(ctx, open(t), nil, quiet)
		if err != nil || len(roots) != 0 {
			t.Errorf("roots = %v, %v; want none and no error", roots, err)
		}
	})

	t.Run("typed flags are saved, -media is not until it has a type", func(t *testing.T) {
		st := open(t)
		flags := []library.Root{{Path: "/m", Kind: library.KindMovies}, {Path: "/t", Kind: library.KindShows}, {Path: "/x"}}
		roots, err := startupRoots(ctx, st, flags, quiet)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(roots, flags) {
			t.Errorf("roots = %v, want %v", roots, flags)
		}
		rows, _ := st.Folders(ctx)
		if len(rows) != 2 || rows[0].Path != "/m" || rows[0].Kind != "movies" || rows[0].Guessed || rows[1].Path != "/t" || rows[1].Kind != "shows" {
			t.Errorf("saved = %+v, want /m movies and /t shows", rows)
		}
	})

	t.Run("-media takes the saved type, and the UI folders follow the flags", func(t *testing.T) {
		st := open(t)
		if _, err := st.AddFolder(ctx, "/ui", library.KindShows, false); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.UpsertFlagFolder(ctx, "/x", "", library.KindMovies); err != nil { // guessed on an earlier run
			t.Fatal(err)
		}
		roots, err := startupRoots(ctx, st, []library.Root{{Path: "/x"}, {Path: "/m", Kind: library.KindMovies}}, quiet)
		if err != nil {
			t.Fatal(err)
		}
		want := []library.Root{{Path: "/x", Kind: library.KindMovies}, {Path: "/m", Kind: library.KindMovies}, {Path: "/ui", Kind: library.KindShows}}
		if !slices.Equal(roots, want) {
			t.Errorf("roots = %v, want %v", roots, want)
		}
		if rows, _ := st.Folders(ctx); len(rows) != 3 || !rows[1].Guessed {
			t.Errorf("-media changed its saved row: %+v", rows)
		}
	})

	t.Run("a folder that left the command line stays, as a UI folder", func(t *testing.T) {
		st := open(t)
		if _, err := startupRoots(ctx, st, []library.Root{{Path: "/m", Kind: library.KindMovies}}, quiet); err != nil {
			t.Fatal(err)
		}
		roots, err := startupRoots(ctx, st, nil, quiet)
		if err != nil || !slices.Equal(roots, []library.Root{{Path: "/m", Kind: library.KindMovies}}) {
			t.Errorf("roots = %v, %v", roots, err)
		}
	})

	t.Run("a flag that changes the type is saved and said", func(t *testing.T) {
		st := open(t)
		if _, err := startupRoots(ctx, st, []library.Root{{Path: "/m", Kind: library.KindMovies}}, quiet); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		roots, err := startupRoots(ctx, st, []library.Root{{Path: "/m", Kind: library.KindShows}}, slog.New(slog.NewTextHandler(&out, nil)))
		if err != nil || !slices.Equal(roots, []library.Root{{Path: "/m", Kind: library.KindShows}}) {
			t.Fatalf("roots = %v, %v", roots, err)
		}
		if rows, _ := st.Folders(ctx); len(rows) != 1 || rows[0].Kind != "shows" {
			t.Errorf("saved = %+v", rows)
		}
		if !strings.Contains(out.String(), "type of a folder changed") || !strings.Contains(out.String(), "/m") {
			t.Errorf("log = %q", out.String())
		}
		// Control: the same flags again say nothing.
		out.Reset()
		if _, err := startupRoots(ctx, st, []library.Root{{Path: "/m", Kind: library.KindShows}}, slog.New(slog.NewTextHandler(&out, nil))); err != nil || out.Len() != 0 {
			t.Errorf("second start: %v, log %q", err, out.String())
		}
	})
}

// A folder that was on the command line at the last start and is not now stays,
// as a Settings folder; the log says so once.
func TestStartupRootsSaysWhenAFolderLeavesTheCommandLine(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	start := func(flags ...library.Root) string {
		t.Helper()
		out.Reset()
		if _, err := startupRoots(ctx, st, flags, log); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	movies, shows := library.Root{Path: "/m", Kind: library.KindMovies}, library.Root{Path: "/t", Kind: library.KindShows}

	if got := start(movies, shows); strings.Contains(got, "before") {
		t.Errorf("first start: %q", got)
	}
	// A folder added in Settings was never on the command line: nothing to say about it.
	if _, err := st.AddFolder(ctx, "/ui", library.KindShows, false); err != nil {
		t.Fatal(err)
	}
	if got := start(movies, shows); strings.Contains(got, "before") {
		t.Errorf("same flags: %q", got)
	}

	got := start(movies) // /t left the command line
	if !strings.Contains(got, "given on the command line before") || !strings.Contains(got, "/t") {
		t.Errorf("a folder left the command line: log %q", got)
	}
	if strings.Contains(got, "/m") || strings.Contains(got, "/ui") {
		t.Errorf("the log names folders that did not leave: %q", got)
	}
	// Said once.
	if got := start(movies); strings.Contains(got, "before") {
		t.Errorf("next start: %q", got)
	}
	// A folder that left and was then removed in Settings has nothing to say either.
	start(movies, shows)
	rows, _ := st.Folders(ctx)
	for _, f := range rows {
		if f.Path == "/t" {
			st.DeleteFolder(ctx, f.ID)
		}
	}
	if got := start(movies); strings.Contains(got, "before") {
		t.Errorf("a removed folder: %q", got)
	}
}

// Folders that overlap are said so, unless they are both on the command line, which
// has always nested.
func TestStartupRootsSaysWhenFoldersOverlap(t *testing.T) {
	ctx := context.Background()
	open := func() *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	startLog := func(st *store.Store, flags ...library.Root) string {
		var out bytes.Buffer
		if _, err := startupRoots(ctx, st, flags, slog.New(slog.NewTextHandler(&out, nil))); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	// A folder kept from an earlier run holds one on the command line.
	st := open()
	if _, err := st.AddFolder(ctx, "/lib", library.KindMovies, false); err != nil {
		t.Fatal(err)
	}
	got := startLog(st, library.Root{Path: "/lib/more", Kind: library.KindMovies})
	if !strings.Contains(got, "overlap") || !strings.Contains(got, "/lib") || !strings.Contains(got, "/lib/more") {
		t.Errorf("a saved folder holding a flag folder: log %q", got)
	}

	// Two folders of Settings.
	st = open()
	st.AddFolder(ctx, "/a", library.KindMovies, false)
	st.AddFolder(ctx, "/a/b", library.KindMovies, false)
	if got := startLog(st); !strings.Contains(got, "overlap") {
		t.Errorf("two saved folders: log %q", got)
	}

	// Controls: both on the command line, or siblings (a name that merely starts alike).
	if got := startLog(open(), library.Root{Path: "/lib", Kind: library.KindMovies}, library.Root{Path: "/lib/more", Kind: library.KindShows}); strings.Contains(got, "overlap") {
		t.Errorf("two flag folders: log %q", got)
	}
	st = open()
	st.AddFolder(ctx, "/lib", library.KindMovies, false)
	if got := startLog(st, library.Root{Path: "/lib2", Kind: library.KindMovies}); strings.Contains(got, "overlap") {
		t.Errorf("siblings: log %q", got)
	}
}

// Folders overlap as directories, not as spelled: the API refuses them through a
// symlink, so the start says so too.
func TestStartupRootsSaysWhenFoldersOverlapThroughASymlink(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	for _, d := range []string{"movies/extras", "other"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	movies, into := filepath.Join(base, "movies"), filepath.Join(base, "into")
	if err := os.Symlink(filepath.Join(movies, "extras"), into); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(movies, alias); err != nil {
		t.Fatal(err)
	}
	startLog := func(saved []string, flags ...library.Root) string {
		st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		for _, p := range saved {
			if _, err := st.AddFolder(ctx, p, library.KindMovies, false); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		if _, err := startupRoots(ctx, st, flags, slog.New(slog.NewTextHandler(&out, nil))); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	for name, got := range map[string]string{
		"a flag folder that links into a saved one":  startLog([]string{movies}, library.Root{Path: into, Kind: library.KindMovies}),
		"a saved folder that links to a flag one":    startLog([]string{alias}, library.Root{Path: movies, Kind: library.KindMovies}),
		"two saved folders, one a link to the other": startLog([]string{movies, alias}),
	} {
		if !strings.Contains(got, "overlap") {
			t.Errorf("%s: log %q", name, got)
		}
	}
	// Controls: the same two through links, both on the command line (they have always
	// nested), and a link to a folder that has nothing to do with the others.
	if got := startLog(nil, library.Root{Path: movies, Kind: library.KindMovies}, library.Root{Path: into, Kind: library.KindMovies}); strings.Contains(got, "overlap") {
		t.Errorf("two flag folders: log %q", got)
	}
	if got := startLog([]string{movies}, library.Root{Path: filepath.Join(base, "other"), Kind: library.KindShows}); strings.Contains(got, "overlap") {
		t.Errorf("unrelated folders: log %q", got)
	}
}

func TestRootPaths(t *testing.T) {
	if got := rootPaths(nil); got == nil || len(got) != 0 {
		t.Errorf("rootPaths(nil) = %#v, want an empty list", got)
	}
	if got := rootPaths([]library.Root{{Path: "/a", Kind: "movies"}, {Path: "/b"}}); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Errorf("rootPaths = %v", got)
	}
}
