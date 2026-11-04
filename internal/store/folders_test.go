package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestMigration005CreatesFoldersAndKeepsData(t *testing.T) {
	s, path := openAtVersion(t, 4)
	ctx := context.Background()
	if _, err := s.rw.Exec(`SELECT 1 FROM folders`); err == nil {
		t.Fatal("control: a version 4 database already has folders")
	}
	id, err := s.UpsertItem(ctx, movie("/media/old.mp4", "Old"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, id, 100, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("Open on a version 4 database: %v", err)
	}
	defer s.Close()
	if fs, err := s.Folders(ctx); err != nil || len(fs) != 0 {
		t.Errorf("Folders after upgrade = %v, %v; want none", fs, err)
	}
	if e, err := s.GetEntry(ctx, id); err != nil || e.Title != "Old" || e.Progress == nil || e.Progress.PositionSec != 100 {
		t.Errorf("item after upgrade = %+v, %v", e, err)
	}
	for table, want := range map[string][]string{
		"folders":      {"id", "path", "kind", "guessed", "added_at"},
		"item_archive": {"path", "size", "tmdb_id", "tmdb_kind", "position_sec", "duration_sec", "watched", "progress_at", "archived_at", "series_id", "series_show"},
	} {
		if got := columns(t, s.rw, table); !slices.Equal(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}
	var versions []int
	rows, err := s.rw.Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if !slices.Equal(versions, []int{1, 2, 3, 4, 5, 6}) {
		t.Errorf("schema_version = %v", versions)
	}
	// The table itself refuses what the Go side would also refuse.
	if _, err := s.rw.Exec(`INSERT INTO folders (path, kind, added_at) VALUES ('/x', 'mixed', 1)`); err == nil {
		t.Error("the folders table accepted the kind 'mixed'")
	}
}

func TestFolderCRUD(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if fs, err := s.Folders(ctx); err != nil || len(fs) != 0 {
		t.Fatalf("Folders on an empty store = %v, %v", fs, err)
	}

	a, err := s.AddFolder(ctx, "/media/movies", FolderMovies, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddFolder(ctx, "/media/tv", FolderShows, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.ID == b.ID || a.Path != "/media/movies" || a.Kind != FolderMovies || a.Guessed || a.AddedAt == 0 {
		t.Errorf("AddFolder = %+v", a)
	}
	if !b.Guessed {
		t.Errorf("a guessed folder lost its flag: %+v", b)
	}
	if fs, err := s.Folders(ctx); err != nil || len(fs) != 2 || fs[0] != a || fs[1] != b {
		t.Errorf("Folders = %+v, %v; want [%+v %+v] in the order added", fs, err, a, b)
	}
	if got, err := s.GetFolder(ctx, b.ID); err != nil || got != b {
		t.Errorf("GetFolder = %+v, %v", got, err)
	}
	if _, err := s.GetFolder(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetFolder(unknown) = %v, want ErrNotFound", err)
	}

	if _, err := s.AddFolder(ctx, "/media/movies", FolderShows, false); !errors.Is(err, ErrFolderExists) {
		t.Errorf("a path twice = %v, want ErrFolderExists", err)
	}
	if got, _ := s.GetFolder(ctx, a.ID); got.Kind != FolderMovies {
		t.Errorf("the refused add changed the folder: %+v", got)
	}
	if _, err := s.AddFolder(ctx, "/media/x", "mixed", false); err == nil {
		t.Error("AddFolder accepted the kind 'mixed'")
	}
	if _, err := s.AddFolder(ctx, "/media/x", "", false); err == nil {
		t.Error("AddFolder accepted an empty kind")
	}

	// SetFolderKind is a person's choice: the guess is over.
	if err := s.SetFolderKind(ctx, b.ID, FolderMovies); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetFolder(ctx, b.ID); got.Kind != FolderMovies || got.Guessed || got.Path != b.Path {
		t.Errorf("after SetFolderKind: %+v", got)
	}
	if err := s.SetFolderKind(ctx, 999, FolderShows); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetFolderKind(unknown) = %v, want ErrNotFound", err)
	}
	if err := s.SetFolderKind(ctx, a.ID, "mixed"); err == nil {
		t.Error("SetFolderKind accepted 'mixed'")
	}
	if got, _ := s.GetFolder(ctx, a.ID); got.Kind != FolderMovies {
		t.Errorf("a refused kind changed the folder: %+v", got)
	}

	if err := s.DeleteFolder(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if fs, _ := s.Folders(ctx); len(fs) != 1 || fs[0].ID != b.ID {
		t.Errorf("after DeleteFolder: %+v", fs)
	}
	if err := s.DeleteFolder(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteFolder twice = %v, want ErrNotFound", err)
	}
	// The path can be added again once the row is gone.
	if _, err := s.AddFolder(ctx, "/media/movies", FolderShows, false); err != nil {
		t.Errorf("re-adding a deleted folder: %v", err)
	}
}

func TestUpsertFlagFolder(t *testing.T) {
	type stored struct {
		kind    string
		guessed bool
	}
	tests := []struct {
		name        string
		before      *stored // the row at the start, if any
		kind, guess string  // the arguments
		want        *stored // the row afterwards, nil = none
		changed     bool
	}{
		{name: "-movies, new", kind: FolderMovies, want: &stored{FolderMovies, false}},
		{name: "-shows, new", kind: FolderShows, want: &stored{FolderShows, false}},
		{name: "-shows replaces a stored kind", before: &stored{FolderMovies, false}, kind: FolderShows,
			want: &stored{FolderShows, false}, changed: true},
		{name: "-movies replaces a guess", before: &stored{FolderShows, true}, kind: FolderMovies,
			want: &stored{FolderMovies, false}, changed: true},
		{name: "-movies on the same kind changes nothing", before: &stored{FolderMovies, false}, kind: FolderMovies,
			want: &stored{FolderMovies, false}},
		{name: "-movies on the same guessed kind makes it a choice", before: &stored{FolderMovies, true}, kind: FolderMovies,
			want: &stored{FolderMovies, false}},
		{name: "-media keeps the stored kind", before: &stored{FolderShows, false}, want: &stored{FolderShows, false}},
		{name: "-media keeps the stored kind over a guess", before: &stored{FolderShows, false}, guess: FolderMovies,
			want: &stored{FolderShows, false}},
		{name: "-media keeps a stored guess", before: &stored{FolderShows, true}, guess: FolderMovies,
			want: &stored{FolderShows, true}},
		{name: "-media inserts the guess", guess: FolderShows, want: &stored{FolderShows, true}},
		{name: "-media with no guess yet stores nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := openTemp(t)
			ctx := context.Background()
			if tt.before != nil {
				if _, err := s.AddFolder(ctx, "/media/x", tt.before.kind, tt.before.guessed); err != nil {
					t.Fatal(err)
				}
			}
			f, changed, err := s.UpsertFlagFolder(ctx, "/media/x", tt.kind, tt.guess)
			if err != nil {
				t.Fatal(err)
			}
			rows, _ := s.Folders(ctx)
			if tt.want == nil {
				if len(rows) != 0 || f.ID != 0 {
					t.Errorf("stored %+v, returned %+v; want nothing", rows, f)
				}
				return
			}
			if len(rows) != 1 || (stored{rows[0].Kind, rows[0].Guessed}) != *tt.want || rows[0].Path != "/media/x" {
				t.Fatalf("rows = %+v, want %+v", rows, *tt.want)
			}
			if f != rows[0] {
				t.Errorf("returned %+v, stored %+v", f, rows[0])
			}
			if changed != tt.changed {
				t.Errorf("changed = %v, want %v", changed, tt.changed)
			}
			if tt.before != nil && rows[0].AddedAt == 0 {
				t.Error("added_at lost")
			}
		})
	}

	s, _ := openTemp(t)
	ctx := context.Background()
	for _, args := range [][2]string{{"mixed", ""}, {"", "mixed"}} {
		if _, _, err := s.UpsertFlagFolder(ctx, "/media/x", args[0], args[1]); err == nil {
			t.Errorf("UpsertFlagFolder(kind %q, guess %q) accepted it", args[0], args[1])
		}
	}
	if rows, _ := s.Folders(ctx); len(rows) != 0 {
		t.Errorf("a refused call stored %+v", rows)
	}
	// Every start upserts again: the row stays one row, with its id.
	first, _, _ := s.UpsertFlagFolder(ctx, "/media/x", FolderMovies, "")
	again, changed, err := s.UpsertFlagFolder(ctx, "/media/x", FolderMovies, "")
	if err != nil || again != first || changed {
		t.Errorf("second start: %+v, changed %v, %v; want the same row %+v", again, changed, err, first)
	}
}

func TestCountItemsByRoot(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if m, err := s.CountItemsByRoot(ctx); err != nil || len(m) != 0 {
		t.Fatalf("empty store = %v, %v", m, err)
	}
	for _, it := range []*Item{movie("/a/1.mp4", "1"), movie("/a/2.mp4", "2"), movie("/b/3.mp4", "3")} {
		it.Root = it.Path[:2]
		if _, err := s.UpsertItem(ctx, it, nil); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.CountItemsByRoot(ctx)
	if err != nil || len(m) != 2 || m["/a"] != 2 || m["/b"] != 1 {
		t.Errorf("CountItemsByRoot = %v, %v; want /a:2 /b:1", m, err)
	}
}

// archiveEnv has two roots: /lib (a movie with progress, a movie watched, a
// manually matched movie, a movie matched by the worker, a movie with nothing,
// and episodes of two shows) and /other, which the tests leave alone.
type archiveEnv struct {
	s                                     *Store
	progress, watched, manual, auto, bare int64
	epManual, epAuto, other               int64
	manualTitle, showTitle, autoTitle     int64
}

func newArchiveEnv(t *testing.T) *archiveEnv {
	t.Helper()
	s, _ := openTemp(t)
	ctx := context.Background()
	e := &archiveEnv{s: s}
	add := func(it *Item, root string, size int64) int64 {
		it.Root, it.Size = root, size
		id, err := s.UpsertItem(ctx, it, videoAudio)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.progress = add(movie("/lib/progress.mp4", "Progress"), "/lib", 100)
	e.watched = add(movie("/lib/watched.mp4", "Watched"), "/lib", 200)
	e.manual = add(movie("/lib/manual.mp4", "Manual"), "/lib", 300)
	e.auto = add(movie("/lib/auto.mp4", "Auto"), "/lib", 400)
	e.bare = add(movie("/lib/bare.mp4", "Bare"), "/lib", 500)
	e.epManual = add(episode("/lib/Fixed Show/S01E01.mkv", "Fixed Show", 1, 1), "/lib", 600)
	e.epAuto = add(episode("/lib/Auto Show/S01E01.mkv", "Auto Show", 1, 1), "/lib", 700)
	e.other = add(movie("/other/other.mp4", "Other"), "/other", 800)

	if _, err := s.SetProgress(ctx, e.progress, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, e.watched, 7100, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, e.other, 50, 100); err != nil {
		t.Fatal(err)
	}
	var err error
	if e.manualTitle, err = s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999)); err != nil {
		t.Fatal(err)
	}
	if e.autoTitle, err = s.UpsertTitle(ctx, tmdbMovie(604, "Reloaded", 2003)); err != nil {
		t.Fatal(err)
	}
	if e.showTitle, err = s.UpsertTitle(ctx, tmdbSeries(1396, "Breaking Bad", 2008)); err != nil {
		t.Fatal(err)
	}
	autoShowTitle, err := s.UpsertTitle(ctx, tmdbSeries(1399, "Game of Thrones", 2011))
	if err != nil {
		t.Fatal(err)
	}
	for id, m := range map[int64]Match{
		e.manual: {TitleID: e.manualTitle, State: MatchManual, Score: 1},
		e.auto:   {TitleID: e.autoTitle, State: MatchMatched, Score: 0.9},
	} {
		if err := s.SetItemMatch(ctx, id, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSeriesMatch(ctx, "Fixed Show", Match{TitleID: e.showTitle, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Auto Show", Match{TitleID: autoShowTitle, State: MatchMatched, Score: 0.8}); err != nil {
		t.Fatal(err)
	}
	return e
}

type archiveRow struct {
	size, tmdbID           int64
	tmdbKind               string
	seriesID               int64  // a show's match
	seriesShow             string // the show it was for
	position, duration     float64
	watched                bool
	progressAt, archivedAt int64
}

func archived(t *testing.T, s *Store) map[string]archiveRow {
	t.Helper()
	rows, err := s.rw.Query(`SELECT path, size, tmdb_id, tmdb_kind, series_id, series_show, position_sec, duration_sec, watched,
			progress_at, archived_at
		FROM item_archive`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]archiveRow{}
	for rows.Next() {
		var (
			p string
			r archiveRow
		)
		if err := rows.Scan(&p, &r.size, &r.tmdbID, &r.tmdbKind, &r.seriesID, &r.seriesShow, &r.position, &r.duration, &r.watched,
			&r.progressAt, &r.archivedAt); err != nil {
			t.Fatal(err)
		}
		m[p] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestArchiveAndDeleteRoot(t *testing.T) {
	e := newArchiveEnv(t)
	s := e.s
	ctx := context.Background()
	prog, _ := s.GetEntry(ctx, e.progress)
	if n := count(t, s, "streams"); n != 8*len(videoAudio) {
		t.Fatalf("control: %d streams before, want %d", n, 8*len(videoAudio))
	}

	n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("deleted %d items, want 7", n)
	}

	// Everything under the root is gone, with what hangs off it; the rest stays.
	if left, _ := s.ListItems(ctx, ListFilter{}); len(left) != 1 || left[0].ID != e.other || left[0].Progress == nil {
		t.Errorf("items left = %+v, want only /other/other.mp4 with its progress", left)
	}
	if n := count(t, s, "streams"); n != len(videoAudio) {
		t.Errorf("%d streams left, want %d (cascade)", n, len(videoAudio))
	}
	if n := count(t, s, "progress"); n != 1 {
		t.Errorf("%d progress rows left, want 1 (cascade)", n)
	}

	got := archived(t, s)
	want := map[string]archiveRow{
		"/lib/progress.mp4":          {size: 100, position: 600, duration: 7200, progressAt: prog.Progress.UpdatedAt},
		"/lib/watched.mp4":           {size: 200, position: 7100, duration: 7200, watched: true},
		"/lib/manual.mp4":            {size: 300, tmdbID: 603, tmdbKind: KindMovie},
		"/lib/Fixed Show/S01E01.mkv": {size: 600, seriesID: 1396, seriesShow: "Fixed Show"},
	}
	if len(got) != len(want) {
		t.Errorf("archived %d paths %v, want %d: progress, watched, the manual movie and the manual show's episode only", len(got), keys(got), len(want))
	}
	for path, w := range want {
		g, ok := got[path]
		if !ok {
			t.Errorf("%s is not archived", path)
			continue
		}
		if g.archivedAt == 0 {
			t.Errorf("%s: archived_at not set", path)
		}
		g.archivedAt = 0
		if w.watched && g.progressAt == 0 {
			t.Errorf("%s: no progress_at", path)
		}
		if w.watched {
			g.progressAt = 0
		}
		if g != w {
			t.Errorf("%s archived as %+v, want %+v", path, g, w)
		}
	}
	// Not archived: a match the worker made (the user did not choose it), a file
	// with nothing to keep, a show matched by the worker. And not /other.
	for _, p := range []string{"/lib/auto.mp4", "/lib/bare.mp4", "/lib/Auto Show/S01E01.mkv", "/other/other.mp4"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s was archived", p)
		}
	}

	// Removing a root that has nothing is not an error.
	if n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil || n != 0 {
		t.Errorf("second removal = %d, %v; want 0, nil", n, err)
	}
	if n, err := s.ArchiveAndDeleteRoot(ctx, "/nowhere", false); err != nil || n != 0 {
		t.Errorf("unknown root = %d, %v", n, err)
	}
	if len(archived(t, s)) != len(want) {
		t.Error("removing nothing changed the archive")
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestArchiveAndDeleteRootForget(t *testing.T) {
	e := newArchiveEnv(t)
	ctx := context.Background()
	n, err := e.s.ArchiveAndDeleteRoot(ctx, "/lib", true)
	if err != nil || n != 7 {
		t.Fatalf("forget = %d, %v; want 7 deleted", n, err)
	}
	if got := archived(t, e.s); len(got) != 0 {
		t.Errorf("forget left %v in the archive", keys(got))
	}
	if left, _ := e.s.ListItems(ctx, ListFilter{}); len(left) != 1 {
		t.Errorf("%d items left, want 1", len(left))
	}
	// Control: the same call without forget archives.
	e2 := newArchiveEnv(t)
	if _, err := e2.s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil || len(archived(t, e2.s)) == 0 {
		t.Errorf("control: archive is empty (%v)", err)
	}
}

// A second removal of the same path replaces the first one's row.
func TestArchiveReplacesAnEarlierRow(t *testing.T) {
	e := newArchiveEnv(t)
	ctx := context.Background()
	if _, err := e.s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	if r := archived(t, e.s)["/lib/progress.mp4"]; r.position != 600 {
		t.Fatalf("control: first archive = %+v", r)
	}
	// The folder comes back as a different file and is watched further.
	it := movie("/lib/progress.mp4", "Progress")
	it.Root, it.Size = "/lib", 111
	id, err := e.s.UpsertItem(ctx, it, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetProgress(ctx, id, 900, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	if r := archived(t, e.s)["/lib/progress.mp4"]; r.position != 900 || r.size != 111 {
		t.Errorf("second archive = %+v, want position 900 and size 111", r)
	}
}

func TestRestoreArchived(t *testing.T) {
	e := newArchiveEnv(t)
	s := e.s
	ctx := context.Background()
	before, _ := s.GetEntry(ctx, e.progress)
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}

	readd := func(path string, size int64) int64 {
		it := movie(path, "Back")
		it.Root, it.Size = "/lib", size
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Same size: the progress comes back, as it was.
	id := readd("/lib/progress.mp4", 100)
	if pr, _ := s.GetEntry(ctx, id); pr.Progress != nil {
		t.Fatalf("control: a new item has progress %+v", pr.Progress)
	}
	tmdb, kind, ok, err := s.RestoreArchived(ctx, id, "/lib/progress.mp4", 100)
	if err != nil || !ok || tmdb != 0 || kind != "" {
		t.Errorf("RestoreArchived = %d, %q, %v, %v; want 0, \"\", true, nil", tmdb, kind, ok, err)
	}
	got, _ := s.GetEntry(ctx, id)
	if got.Progress == nil || *got.Progress != *before.Progress {
		t.Errorf("progress = %+v, want %+v", got.Progress, before.Progress)
	}
	if _, ok := archived(t, s)["/lib/progress.mp4"]; ok {
		t.Error("the archive row is still there")
	}
	// Once only.
	if _, _, ok, err := s.RestoreArchived(ctx, id, "/lib/progress.mp4", 100); err != nil || ok {
		t.Errorf("second restore = %v, %v; want ok false", ok, err)
	}

	// A watched item comes back watched.
	wid := readd("/lib/watched.mp4", 200)
	if _, _, ok, _ := s.RestoreArchived(ctx, wid, "/lib/watched.mp4", 200); !ok {
		t.Fatal("watched: not restored")
	}
	if w, _ := s.GetEntry(ctx, wid); w.Progress == nil || !w.Progress.Watched {
		t.Errorf("watched item restored as %+v", w.Progress)
	}

	// A manual match is returned for the caller to apply, with the progress.
	mid := readd("/lib/manual.mp4", 300)
	tmdb, kind, ok, err = s.RestoreArchived(ctx, mid, "/lib/manual.mp4", 300)
	if err != nil || !ok || tmdb != 603 || kind != KindMovie {
		t.Errorf("manual movie = %d, %q, %v, %v; want 603, movie, true", tmdb, kind, ok, err)
	}
	// ...also a show's, with the kind to tell them apart.
	eid := readd("/lib/Fixed Show/S01E01.mkv", 600)
	tmdb, kind, ok, err = s.RestoreArchived(ctx, eid, "/lib/Fixed Show/S01E01.mkv", 600)
	if err != nil || !ok || tmdb != 1396 || kind != KindSeries {
		t.Errorf("manual show = %d, %q, %v, %v; want 1396, series, true", tmdb, kind, ok, err)
	}

	// A path that was never archived.
	nid := readd("/lib/new.mp4", 1)
	if tmdb, kind, ok, err := s.RestoreArchived(ctx, nid, "/lib/new.mp4", 1); err != nil || ok || tmdb != 0 || kind != "" {
		t.Errorf("unknown path = %d, %q, %v, %v; want zero, false", tmdb, kind, ok, err)
	}
	// What is left is the two matches, waiting to be applied; the rest is used up.
	left := archived(t, s)
	if len(left) != 2 || left["/lib/manual.mp4"].tmdbID != 603 || left["/lib/Fixed Show/S01E01.mkv"].seriesID != 1396 {
		t.Errorf("archive left: %v", left)
	}
	for path, r := range left {
		if r.progressAt != 0 || r.position != 0 {
			t.Errorf("%s still holds progress: %+v", path, r)
		}
	}
	for p, kind := range map[string]string{"/lib/manual.mp4": KindMovie, "/lib/Fixed Show/S01E01.mkv": KindSeries} {
		if err := s.ForgetArchivedMatch(ctx, p, kind); err != nil {
			t.Fatal(err)
		}
	}
	if left := archived(t, s); len(left) != 0 {
		t.Errorf("archive left after forgetting the matches: %v", keys(left))
	}
}

func TestRestoreArchivedWithADifferentSizeKeepsOnlyTheMatch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/m.mp4", "M")
	it.Root, it.Size = "/lib", 100
	id, _ := s.UpsertItem(ctx, it, nil)
	title, _ := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	if err := s.SetItemMatch(ctx, id, Match{TitleID: title, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, id, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	if r, ok := archived(t, s)["/lib/m.mp4"]; !ok || r.position != 600 || r.tmdbID != 603 {
		t.Fatalf("control: archived %+v, %v", r, ok)
	}

	it2 := movie("/lib/m.mp4", "M")
	it2.Root, it2.Size = "/lib", 101 // another file under the same name
	id2, _ := s.UpsertItem(ctx, it2, nil)
	tmdb, kind, ok, err := s.RestoreArchived(ctx, id2, "/lib/m.mp4", 101)
	if err != nil || !ok || tmdb != 603 || kind != KindMovie {
		t.Fatalf("RestoreArchived = %d, %q, %v, %v; want the match", tmdb, kind, ok, err)
	}
	if e, _ := s.GetEntry(ctx, id2); e.Progress != nil {
		t.Errorf("a different file got the old progress: %+v", e.Progress)
	}
	if r, ok := archived(t, s)["/lib/m.mp4"]; !ok || r.tmdbID != 603 || r.progressAt != 0 || r.position != 0 {
		t.Errorf("the archive row after the restore = %+v, %v; want the match only, until it is applied", r, ok)
	}
	if err := s.ForgetArchivedMatch(ctx, "/lib/m.mp4", KindMovie); err != nil {
		t.Fatal(err)
	}
	if _, ok := archived(t, s)["/lib/m.mp4"]; ok {
		t.Error("the archive row survived ForgetArchivedMatch")
	}
}

// Progress made since the file came back is newer than the archived one.
func TestRestoreArchivedKeepsNewerProgress(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/m.mp4", "M")
	it.Root, it.Size = "/lib", 100
	id, _ := s.UpsertItem(ctx, it, nil)
	if _, err := s.SetProgress(ctx, id, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.UpsertItem(ctx, it, nil)
	if _, err := s.SetProgress(ctx, id2, 1200, 7200); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.RestoreArchived(ctx, id2, "/lib/m.mp4", 100); err != nil || !ok {
		t.Fatalf("RestoreArchived = %v, %v", ok, err)
	}
	if e, _ := s.GetEntry(ctx, id2); e.Progress == nil || e.Progress.PositionSec != 1200 {
		t.Errorf("progress = %+v, want the newer 1200", e.Progress)
	}
}

// Archiving and deleting are one transaction per batch of chunkSpec items, and
// the restore is one transaction: when their last step fails, the first one is
// undone too, for that batch.
func TestArchiveAndRestoreAreAtomic(t *testing.T) {
	ctx := context.Background()
	t.Run("archive, one batch", func(t *testing.T) {
		e := newArchiveEnv(t)
		if _, err := e.s.rw.Exec(`CREATE TRIGGER no_delete BEFORE DELETE ON items
			BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		if n, err := e.s.ArchiveAndDeleteRoot(ctx, "/lib", false); err == nil || n != 0 {
			t.Fatalf("control: the delete was meant to fail, got %d, %v", n, err)
		}
		if got := archived(t, e.s); len(got) != 0 {
			t.Errorf("a failed removal left %v archived", keys(got))
		}
		if n, _ := e.s.CountItems(ctx); n != 8 {
			t.Errorf("%d items left, want all 8", n)
		}
	})

	// 1,200 items under /lib make three batches (500, 500, 200), by id. The delete of
	// one item of the second batch fails: the first batch stays archived and
	// deleted, the second is undone whole (nothing of it archived, nothing deleted),
	// the third never starts. Calling again finishes the job, and the archive rows
	// the first call wrote are the ones that stay.
	t.Run("archive, a failure in the second batch", func(t *testing.T) {
		s, _ := openTemp(t)
		const total = 2*chunkSpec + 200
		ids := seedItems(t, s, "/lib", total) // every one has progress, so every one is archived
		batch1, batch2, batch3 := ids[:chunkSpec], ids[chunkSpec:2*chunkSpec], ids[2*chunkSpec:]
		if _, err := s.rw.Exec(fmt.Sprintf(`CREATE TRIGGER fail_in_batch_two BEFORE DELETE ON items WHEN OLD.id = %d
			BEGIN SELECT RAISE(ABORT, 'refused'); END`, batch2[len(batch2)/2])); err != nil {
			t.Fatal(err)
		}
		if n := count(t, s, "items"); n != total || len(archived(t, s)) != 0 {
			t.Fatalf("control: %d items and %d archived before, want %d and 0", n, len(archived(t, s)), total)
		}

		n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false)
		if err == nil {
			t.Fatal("control: the delete was meant to fail")
		}
		if n != chunkSpec {
			t.Errorf("returned %d with the error, want the %d deleted before it", n, chunkSpec)
		}
		left := map[int64]bool{}
		rows, err := s.rw.Query(`SELECT id FROM items`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			left[id] = true
		}
		rows.Close()
		got := archived(t, s)
		for _, id := range batch1 {
			if left[id] {
				t.Fatalf("item %d of the first batch is still there", id)
			}
		}
		if len(got) != len(batch1) || len(left) != len(batch2)+len(batch3) {
			t.Errorf("after the failure: %d archived and %d items left, want %d and %d", len(got), len(left), len(batch1), len(batch2)+len(batch3))
		}
		for _, id := range batch1 {
			if _, ok := got[fmt.Sprintf("/lib/%d.mp4", id)]; !ok {
				t.Fatalf("item %d was deleted without its archive row", id)
			}
		}
		for _, id := range append(slices.Clone(batch2), batch3...) {
			if !left[id] {
				t.Fatalf("item %d of a later batch was deleted", id)
			}
			if _, ok := got[fmt.Sprintf("/lib/%d.mp4", id)]; ok {
				t.Fatalf("item %d of the failed batch has an archive row, but is still there", id)
			}
		}
		if n := count(t, s, "streams"); n != len(left) {
			t.Errorf("%d streams for %d items: the cascade of the first batch is off", n, len(left))
		}

		// Resume: the same call, after whatever stopped it is gone.
		if _, err := s.rw.Exec(`DROP TRIGGER fail_in_batch_two`); err != nil {
			t.Fatal(err)
		}
		n, err = s.ArchiveAndDeleteRoot(ctx, "/lib", false)
		if err != nil || n != len(batch2)+len(batch3) {
			t.Fatalf("second call = %d, %v; want %d, nil", n, err, len(batch2)+len(batch3))
		}
		after := archived(t, s)
		if c, _ := s.CountItems(ctx); c != 0 || len(after) != total {
			t.Errorf("after the second call: %d items and %d archived, want 0 and %d", c, len(after), total)
		}
		for path, row := range got {
			if after[path] != row {
				t.Errorf("%s: the archive row of the first call changed: %+v, now %+v", path, row, after[path])
			}
		}
	})
	t.Run("restore", func(t *testing.T) {
		s, _ := openTemp(t)
		it := movie("/lib/m.mp4", "M")
		it.Root, it.Size = "/lib", 100
		id, _ := s.UpsertItem(ctx, it, nil)
		if _, err := s.rw.Exec(`INSERT INTO item_archive (path, size, position_sec, duration_sec, progress_at, archived_at)
			VALUES ('/lib/m.mp4', 100, 600, 7200, 5, 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.rw.Exec(`CREATE TRIGGER no_delete BEFORE DELETE ON item_archive
			BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := s.RestoreArchived(ctx, id, "/lib/m.mp4", 100); err == nil {
			t.Fatal("control: the delete was meant to fail")
		}
		if e, _ := s.GetEntry(ctx, id); e.Progress != nil {
			t.Errorf("a failed restore left progress %+v", e.Progress)
		}
		if _, ok := archived(t, s)["/lib/m.mp4"]; !ok {
			t.Error("a failed restore lost the archive row")
		}
	})
}
