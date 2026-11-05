package store

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestRootItems(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if got, err := s.RootItems(ctx, "/lib"); err != nil || len(got) != 0 {
		t.Fatalf("empty store = %v, %v", got, err)
	}
	a := movie("/lib/a.mp4", "A")
	a.Root, a.Year = "/lib", 1999
	b := episode("/lib/Show/S01E02.mkv", "Show", 1, 2)
	b.Root, b.NeedsReview = "/lib", true
	c := movie("/other/c.mp4", "C")
	c.Root = "/other"
	ids := map[string]int64{}
	for _, it := range []*Item{a, b, c} {
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[it.Path] = id
	}

	got, err := s.RootItems(ctx, "/lib")
	if err != nil || len(got) != 2 {
		t.Fatalf("RootItems(/lib) = %+v, %v; want 2 items", got, err)
	}
	since, err := s.TypedSince(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i].AddedAt < since { // added now, by this version: not before the stamp
			t.Errorf("%s added at %d, before the stamp %d", got[i].Path, got[i].AddedAt, since)
		}
		got[i].AddedAt = 0
	}
	wantA := RootItem{Path: "/lib/a.mp4", Root: "/lib", ParsedRow: ParsedRow{ID: ids["/lib/a.mp4"], Kind: KindMovie, Title: "A", Year: 1999}}
	wantB := RootItem{Path: "/lib/Show/S01E02.mkv", Root: "/lib", ParsedRow: ParsedRow{ID: ids["/lib/Show/S01E02.mkv"], Kind: KindEpisode,
		Title: "Ep", Show: "Show", Season: 1, Episode: 2, NeedsReview: true}}
	if got[0] != wantA || got[1] != wantB {
		t.Errorf("RootItems(/lib) = %+v, want %+v and %+v", got, wantA, wantB)
	}
}

// ItemsBelow lists the items whose file is inside a folder, whatever root they are
// indexed under, and only those: a folder with the same start is another folder.
func TestItemsBelow(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(path, root string) int64 {
		it := movie(path, "T")
		it.Root = root
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	in1 := add("/m/TV/Lost (2004)/a.mkv", "/m") // an earlier configuration's root
	in2 := add("/m/TV/été/b.mkv", "/m/TV")
	add("/m/TVshows/c.mkv", "/m") // not inside /m/TV
	add("/m/Movies/d.mkv", "/m")
	for _, dir := range []string{"/m/TV", "/m/TV/"} {
		got, err := s.ItemsBelow(ctx, dir)
		if err != nil || len(got) != 2 || got[0].ID != in1 || got[0].Root != "/m" || got[1].ID != in2 || got[1].Root != "/m/TV" {
			t.Errorf("ItemsBelow(%q) = %+v, %v; want the two items inside, with their roots", dir, got, err)
		}
	}
	if got, _ := s.ItemsBelow(ctx, "/m/TV/été"); len(got) != 1 || got[0].ID != in2 {
		t.Errorf("ItemsBelow of a folder with multi-byte characters = %+v", got)
	}
}

// The time this version first ran on a database is stamped once, by the first Open,
// and no later start changes it: what is older than it was left by an earlier version.
func TestTypedSinceIsStampedOnce(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	now := time.Now().Unix()
	since, err := s.TypedSince(ctx)
	if err != nil || since < now-10 || since > now+1 {
		t.Fatalf("TypedSince on a new database = %d, %v; want about now (%d)", since, err, now)
	}
	if _, err := s.rw.ExecContext(ctx, `UPDATE settings SET value = '5' WHERE key = 'm7.since'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, err := s2.TypedSince(ctx); err != nil || got != 5 {
		t.Errorf("TypedSince after a restart = %d, %v; want the stamp as it was, 5", got, err)
	}

	// A database an earlier version left: the stamp is made on its first Open here.
	old, oldPath := openAtVersion(t, 5)
	var stamps int
	if err := old.rw.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'm7.since'`).Scan(&stamps); err != nil || stamps != 0 {
		t.Fatalf("control: a version 5 database has the stamp (%d, %v)", stamps, err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if got, err := migrated.TypedSince(ctx); err != nil || got < now-10 {
		t.Errorf("TypedSince on an upgraded database = %d, %v", got, err)
	}
}

// Settings are listed by the start of their key.
func TestSettingsWithPrefix(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for k, v := range map[string]string{"m7.reread./tv": "1", "m7.reread./films é": "2", "m7.rereadx": "3", "other": "4"} {
		if err := s.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SettingsWithPrefix(ctx, "m7.reread.")
	want := map[string]string{"m7.reread./tv": "1", "m7.reread./films é": "2"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("SettingsWithPrefix = %v, %v; want %v", got, err, want)
	}
	if got, _ := s.SettingsWithPrefix(ctx, "m7.reread.é"); len(got) != 0 {
		t.Errorf("a prefix with a multi-byte character matched %v", got)
	}
}

// UpsertItem writes NeedsReview for a row it creates, and leaves it alone on a
// row that exists (the matcher and SetParsed own it from then on).
func TestUpsertItemNeedsReviewOnInsertOnly(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := episode("/lib/Show/Extras.mkv", "Show", 1, 0)
	it.Root, it.NeedsReview = "/lib", true
	id, err := s.UpsertItem(ctx, it, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); !e.NeedsReview {
		t.Fatalf("a new row did not get the flag: %+v", e.Item)
	}

	// The file changed and is indexed again: the row keeps its flag whatever the
	// new reading says, and a reset flag (a person looked) is not set again.
	if _, err := s.rw.ExecContext(ctx, `UPDATE items SET needs_review = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	again := *it
	again.Size++
	if _, err := s.UpsertItem(ctx, &again, nil); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); e.NeedsReview || e.Size != again.Size {
		t.Errorf("an existing row after an upsert: %+v, want the flag as it was (off) and the new size", e.Item)
	}

	plain := movie("/lib/m.mp4", "M")
	plain.Root = "/lib"
	pid, _ := s.UpsertItem(ctx, plain, nil)
	if e, _ := s.GetEntry(ctx, pid); e.NeedsReview {
		t.Errorf("a movie got the flag: %+v", e.Item)
	}
}

// The archive keeps a manual match until whoever applies it says so, and lists the
// ones whose item is in the library and has no manual match yet.
func TestPendingMatchesLastUntilForgotten(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(it *Item) int64 {
		it.Root, it.Size = "/lib", 100
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	movieID := add(movie("/lib/m.mp4", "M"))
	epID := add(episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1))
	mt, _ := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetItemMatch(ctx, movieID, Match{TitleID: mt, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, movieID, 600, 7200); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ArchivedProgress(ctx); err != nil || len(got) != 1 || !got["/lib/m.mp4"] {
		t.Fatalf("ArchivedProgress = %v, %v; want the movie's path only", got, err)
	}
	if got, err := s.PendingMatches(ctx); err != nil || len(got) != 0 {
		t.Fatalf("PendingMatches with the files gone = %v, %v", got, err)
	}
	if _, err := s.rw.ExecContext(ctx, `DELETE FROM series_matches`); err != nil { // what pruning does with a show nobody has
		t.Fatal(err)
	}

	// The files come back and are restored; the matches wait.
	movieID = add(movie("/lib/m.mp4", "M"))
	epID = add(episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1))
	for _, p := range []string{"/lib/m.mp4", "/lib/Lost/S01E01.mkv"} {
		id := map[string]int64{"/lib/m.mp4": movieID, "/lib/Lost/S01E01.mkv": epID}[p]
		if _, _, ok, err := s.RestoreArchived(ctx, id, p, 100); err != nil || !ok {
			t.Fatalf("RestoreArchived(%s) = %v, %v", p, ok, err)
		}
	}
	if got, err := s.ArchivedProgress(ctx); err != nil || len(got) != 0 {
		t.Errorf("ArchivedProgress after the restore = %v, %v; want none", got, err)
	}
	want := []PendingMatch{{movieID, "/lib/m.mp4", 603, KindMovie}, {epID, "/lib/Lost/S01E01.mkv", 4607, KindSeries}}
	got, err := s.PendingMatches(ctx)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("PendingMatches = %+v, %v; want %+v", got, err, want)
	}
	// Listed again until forgotten.
	if again, _ := s.PendingMatches(ctx); !slices.Equal(again, want) {
		t.Errorf("PendingMatches the second time = %+v", again)
	}

	// Applying the movie's match (it is manual now) takes it off the list; forgetting
	// it takes the row away.
	if err := s.SetItemMatch(ctx, movieID, Match{TitleID: mt, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PendingMatches(ctx); !slices.Equal(got, want[1:]) {
		t.Errorf("PendingMatches with the movie matched = %+v, want the episode's only", got)
	}
	for p, kind := range map[string]string{"/lib/m.mp4": KindMovie, "/lib/Lost/S01E01.mkv": KindSeries} {
		if err := s.ForgetArchivedMatch(ctx, p, kind); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.PendingMatches(ctx); len(got) != 0 {
		t.Errorf("PendingMatches after forgetting = %+v", got)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("%d archive rows left", n)
	}
}

func countArchive(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM item_archive`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A second removal before the match was applied does not forget it.
func TestArchiveAndDeleteRootKeepsAPendingMatch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/m.mp4", "M")
	it.Root, it.Size = "/lib", 100
	id, _ := s.UpsertItem(ctx, it, nil)
	title, _ := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	s.SetItemMatch(ctx, id, Match{TitleID: title, State: MatchManual, Score: 1})
	s.SetProgress(ctx, id, 600, 7200)
	s.ArchiveAndDeleteRoot(ctx, "/lib", false)
	id, _ = s.UpsertItem(ctx, it, nil)
	s.RestoreArchived(ctx, id, "/lib/m.mp4", 100) // back, the match not applied
	s.SetProgress(ctx, id, 900, 7200)             // and played a bit more
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	r := archived(t, s)["/lib/m.mp4"]
	if r.tmdbID != 603 || r.tmdbKind != KindMovie || r.position != 900 {
		t.Errorf("archive row = %+v, want the match kept and the new progress", r)
	}
}

// SetParsed keeps the manual matches it drops, by path, so that the folder's old
// type brings them back.
func TestSetParsedArchivesTheManualMatchesItDrops(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(it *Item) int64 {
		it.Root, it.Size = "/lib", 100
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	dune := add(movie("/lib/Dune (2021).mkv", "Dune"))
	auto := add(movie("/lib/Auto (2020).mkv", "Auto"))
	e1 := add(episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1))
	e2 := add(episode("/lib/Lost/S01E02.mkv", "Lost", 1, 2))
	mt, _ := s.UpsertTitle(ctx, tmdbMovie(438631, "Dune", 2021))
	at, _ := s.UpsertTitle(ctx, tmdbMovie(1, "Auto", 2020))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	s.SetItemMatch(ctx, dune, Match{TitleID: mt, State: MatchManual, Score: 1})
	s.SetItemMatch(ctx, auto, Match{TitleID: at, State: MatchMatched, Score: 0.9})
	s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1})
	s.SetProgress(ctx, dune, 600, 7200)

	// To a shows folder: the movies become episodes, the show's episodes movies.
	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: dune, Kind: KindEpisode, Show: "Dune", Season: 1}, {ID: auto, Kind: KindEpisode, Show: "Auto", Season: 1},
		{ID: e1, Kind: KindMovie, Title: "Lost S01E01"}, {ID: e2, Kind: KindMovie, Title: "Lost S01E02"},
	}); err != nil {
		t.Fatal(err)
	}
	got := archived(t, s)
	if len(got) != 3 || got["/lib/Dune (2021).mkv"].tmdbID != 438631 || got["/lib/Dune (2021).mkv"].tmdbKind != KindMovie ||
		got["/lib/Lost/S01E01.mkv"].seriesID != 4607 || got["/lib/Lost/S01E01.mkv"].seriesShow != "Lost" ||
		got["/lib/Lost/S01E02.mkv"].seriesID != 4607 {
		t.Fatalf("archive = %+v; want the manual matches of Dune and of both Lost episodes, and not the worker's", got)
	}
	if e, _ := s.GetEntry(ctx, dune); e.Progress == nil || e.Progress.PositionSec != 600 || e.MatchState != MatchPending {
		t.Errorf("Dune after the change = %+v, progress %+v: the progress stays, the match is gone", e.Item, e.Progress)
	}
	if pending, _ := s.PendingMatches(ctx); len(pending) != 0 {
		t.Errorf("PendingMatches while the kinds do not fit = %+v", pending)
	}

	// And back: the matches are due again.
	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: dune, Kind: KindMovie, Title: "Dune", Year: 2021}, {ID: auto, Kind: KindMovie, Title: "Auto", Year: 2020},
		{ID: e1, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 1}, {ID: e2, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 2},
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingMatches(ctx)
	if err != nil || len(pending) != 3 || pending[0].ItemID != dune || pending[0].TMDBID != 438631 ||
		pending[1].ItemID != e1 || pending[1].TMDBID != 4607 || pending[1].Kind != KindSeries || pending[2].ItemID != e2 {
		t.Errorf("PendingMatches after flipping back = %+v, %v", pending, err)
	}
}

// A move follows what the new name says about needs_review (an episode's), leaves a
// movie's alone, and drops what the archive kept for the path it moved onto.
func TestMoveItemNeedsReviewAndArchive(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := episode("/lib/Show/Extras.mkv", "Show", 1, 0)
	it.Root, it.NeedsReview = "/lib", true
	id, _ := s.UpsertItem(ctx, it, nil)

	numbered := episode("/lib/Show/S01E03.mkv", "Show", 1, 3)
	numbered.Root = "/lib"
	if err := s.MoveItem(ctx, id, numbered); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); e.NeedsReview || e.Episode != 3 {
		t.Errorf("renamed with a number: %+v, want the flag off", e.Item)
	}
	bare := episode("/lib/Show/Making Of.mkv", "Show", 1, 0)
	bare.Root, bare.NeedsReview = "/lib", true
	if err := s.MoveItem(ctx, id, bare); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); !e.NeedsReview {
		t.Errorf("renamed to no number: %+v, want the flag on", e.Item)
	}
	film := movie("/lib/Film (2000).mkv", "Film")
	film.Root = "/lib"
	if err := s.MoveItem(ctx, id, film); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); e.NeedsReview || e.Kind != KindMovie {
		t.Errorf("an episode that became a movie: %+v, want the flag off", e.Item)
	}
	// A movie's flag is the matcher's.
	if _, err := s.rw.ExecContext(ctx, `UPDATE items SET needs_review = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	film2 := movie("/lib/Film 2 (2001).mkv", "Film 2")
	film2.Root = "/lib"
	if err := s.MoveItem(ctx, id, film2); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); !e.NeedsReview {
		t.Errorf("a movie that moved lost the matcher's flag: %+v", e.Item)
	}

	if _, err := s.rw.ExecContext(ctx, `INSERT INTO item_archive (path, size, progress_at, archived_at) VALUES ('/lib/Taken.mkv', 1, 5, 1)`); err != nil {
		t.Fatal(err)
	}
	taken := movie("/lib/Taken.mkv", "Taken")
	taken.Root = "/lib"
	if err := s.MoveItem(ctx, id, taken); err != nil {
		t.Fatal(err)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("the archive row of the path the file moved onto is still there")
	}
}

// A show with an episode that has no number is in the review list and the count,
// whatever its match says.
func TestReviewListHasShowsWithAFlaggedEpisode(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, it := range []*Item{episode("/m/A/S01E01.mkv", "Aaa", 1, 1), episode("/m/B/Extras.mkv", "Bbb", 1, 0), episode("/m/B/S01E01.mkv", "Bbb", 1, 1)} {
		if it.Episode == 0 {
			it.NeedsReview = true
		}
		if _, err := s.UpsertItem(ctx, it, nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ReviewList(ctx)
	if err != nil || len(list) != 1 || list[0].Kind != KindSeries || list[0].Show != "Bbb" || !list[0].NeedsReview || list[0].MatchState != MatchPending {
		t.Fatalf("ReviewList = %+v, %v; want Bbb, for review", list, err)
	}
	if c, err := s.CountMatches(ctx); err != nil || c.NeedsReview != 1 || c.Pending != 2 {
		t.Errorf("CountMatches = %+v, %v; want 1 for review, 2 pending", c, err)
	}
}

// A result the worker found for a movie is not written on an item that was read
// again as an episode in the meantime (SetParsed makes it pending again).
func TestWorkerResultsAreForMoviesOnly(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/Dune (2021).mkv", "Dune")
	it.Root = "/lib"
	id, _ := s.UpsertItem(ctx, it, nil)
	title := tmdbMovie(438631, "Dune", 2021)
	m := Match{State: MatchMatched, Score: 0.9}

	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindEpisode, Show: "Dune (2021)", Season: 1, NeedsReview: true}}); err != nil {
		t.Fatal(err)
	}
	tid, _ := s.UpsertTitle(ctx, title)
	m.TitleID = tid
	if ok, err := s.SetItemMatchIfPending(ctx, id, m); err != nil || ok {
		t.Errorf("SetItemMatchIfPending on an episode = %v, %v; want not written", ok, err)
	}
	if _, ok, err := s.LinkItemTitle(ctx, id, title, m, MatchPending); err != nil || ok {
		t.Errorf("LinkItemTitle on an episode = %v, %v; want not written", ok, err)
	}
	if e, _ := s.GetEntry(ctx, id); e.TitleID != 0 || e.MatchState != MatchPending || !e.NeedsReview {
		t.Errorf("the episode after the worker's stale results: %+v", e.Item)
	}

	// Control: as a movie again, the same results are written.
	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindMovie, Title: "Dune", Year: 2021}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetItemMatchIfPending(ctx, id, m); err != nil || !ok {
		t.Errorf("SetItemMatchIfPending on a movie = %v, %v; want written", ok, err)
	}
}
