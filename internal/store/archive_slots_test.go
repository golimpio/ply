package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func addAt(t *testing.T, s *Store, it *Item) int64 {
	t.Helper()
	it.Root, it.Size = "/lib", 100
	id, err := s.UpsertItem(context.Background(), it, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// What a path says about a file (its kind, title, show, numbers) is not rewritten by
// the scan of a file that changed: that is SetParsed's, which knows the rules for
// the matches the new reading would break. The file's own columns are updated.
func TestUpsertItemLeavesWhatThePathSaid(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	ep := episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1)
	id := addAt(t, s, ep)
	title, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: title, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}

	// The scan of a library folder whose type is movies now reads the same file as a movie.
	changed := movie("/lib/Lost/S01E01.mkv", "Lost S01E01")
	changed.Root, changed.Size, changed.MTime, changed.Fingerprint = "/lib", 555, 42, "new"
	if again, err := s.UpsertItem(ctx, changed, nil); err != nil || again != id {
		t.Fatalf("UpsertItem = %d, %v; want id %d", again, err, id)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.Kind != KindEpisode || e.Show != "Lost" || e.Season != 1 || e.Episode != 1 || e.Title != ep.Title {
		t.Errorf("an upsert rewrote what the path said: %+v", e.Item)
	}
	if e.Size != 555 || e.MTime != 42 || e.Fingerprint != "new" {
		t.Errorf("the file's own columns were not updated: %+v", e.Item)
	}
	if m, err := s.GetSeriesMatch(ctx, "Lost"); err != nil || m.State != MatchManual {
		t.Errorf("the show's manual match after the upsert: %+v, %v", m, err)
	}
}

// A move that changes the kind of a file (a movie moved into a shows folder) is a
// re-reading: the match that does not fit goes to the archive under the new path,
// the progress stays, and the show's rows are cleaned. A plain rename keeps its match.
func TestMoveItemAcrossKindsAppliesTheReadingRules(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id := addAt(t, s, movie("/films/Dune (2021).mkv", "Dune"))
	auto := addAt(t, s, movie("/films/Heat (1995).mkv", "Heat"))
	dune, _ := s.UpsertTitle(ctx, tmdbMovie(438631, "Dune", 2021))
	heat, _ := s.UpsertTitle(ctx, tmdbMovie(949, "Heat", 1995))
	s.SetItemMatch(ctx, id, Match{TitleID: dune, State: MatchManual, Score: 1})
	s.SetItemMatch(ctx, auto, Match{TitleID: heat, State: MatchMatched, Score: 0.9})
	s.SetProgress(ctx, id, 600, 7200)

	// A plain rename keeps what it had.
	renamed := movie("/films/Dune.mkv", "Dune")
	renamed.Root = "/films"
	if err := s.MoveItem(ctx, id, renamed); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); e.MatchState != MatchManual || e.TitleID != dune {
		t.Errorf("a rename lost the match: %+v", e.Item)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("a rename archived something (%d rows)", n)
	}

	// Moved into a shows folder, both become episodes.
	asEp := episode("/tv/Dune/S01E01.mkv", "Dune", 1, 1)
	asEp.Root = "/tv"
	if err := s.MoveItem(ctx, id, asEp); err != nil {
		t.Fatal(err)
	}
	autoEp := episode("/tv/Heat/S01E01.mkv", "Heat", 1, 1)
	autoEp.Root = "/tv"
	if err := s.MoveItem(ctx, auto, autoEp); err != nil {
		t.Fatal(err)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.Kind != KindEpisode || e.Show != "Dune" || e.MatchState != MatchPending || e.TitleID != 0 {
		t.Errorf("the moved movie = %+v; want an episode with no match", e.Item)
	}
	if e.Progress == nil || e.Progress.PositionSec != 600 {
		t.Errorf("the progress of the moved file: %+v", e.Progress)
	}
	if a, ok := archived(t, s)["/tv/Dune/S01E01.mkv"]; !ok || a.tmdbID != 438631 || a.tmdbKind != KindMovie {
		t.Errorf("archive = %+v; want the manual match under the new path", archived(t, s))
	}
	if got := archived(t, s); len(got) != 1 {
		t.Errorf("archive = %+v; want only the manual match (the worker's is not kept)", got)
	}
	if e, _ := s.GetEntry(ctx, auto); e.Kind != KindEpisode || e.MatchState != MatchPending || e.TitleID != 0 {
		t.Errorf("the moved, automatically matched movie = %+v", e.Item)
	}
	if got, _ := s.PendingMatches(ctx); len(got) != 0 {
		t.Errorf("PendingMatches with the kind not fitting = %+v", got)
	}

	// Moved back to a movies folder: the match waits in the archive under the path it was
	// archived for, which is no file now; it is not offered to this one.
	back := movie("/films/Dune (2021).mkv", "Dune")
	back.Root = "/films"
	if err := s.MoveItem(ctx, id, back); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.GetEntry(ctx, id); e.Kind != KindMovie || e.Show != "" || e.Title != "Dune" || e.MatchState != MatchPending {
		t.Errorf("the file moved back = %+v; want a movie again", e.Item)
	}
	if got, _ := s.PendingMatches(ctx); len(got) != 0 {
		t.Errorf("PendingMatches = %+v; the match was archived for another path", got)
	}
}

// An episode that moves to another show takes its show's manual match to the archive,
// for that show; it is not offered to the other.
func TestMoveItemToAnotherShowArchivesTheShowsMatch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addAt(t, s, episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1))
	addAt(t, s, episode("/lib/Lost/S01E02.mkv", "Lost", 1, 2))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	moved := episode("/lib/Lost (2004)/S01E01.mkv", "Lost (2004)", 1, 1)
	moved.Root = "/lib"
	if err := s.MoveItem(ctx, e1, moved); err != nil {
		t.Fatal(err)
	}
	a, ok := archived(t, s)["/lib/Lost (2004)/S01E01.mkv"]
	if !ok || a.seriesID != 4607 || a.seriesShow != "Lost" {
		t.Errorf("archive = %+v; want the match of Lost kept for the moved episode", archived(t, s))
	}
	if m, err := s.GetSeriesMatch(ctx, "Lost"); err != nil || m.State != MatchManual {
		t.Errorf("the other episode's show lost its match: %+v, %v", m, err)
	}
	if got, _ := s.PendingMatches(ctx); len(got) != 0 {
		t.Errorf("PendingMatches = %+v; the show the match is for is not this one", got)
	}
}

// A manual match of a show goes to the archive when an episode stops being an episode
// of it and the show is not simply renamed: here two shows are read as one.
func TestSetParsedArchivesTheShowsMatchWhenTwoShowsAreReadAsOne(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addAt(t, s, episode("/lib/TV/Lost/S01E01.mkv", "Lost", 1, 1))
	e2 := addAt(t, s, episode("/lib/TV/Lost/S01E02.mkv", "Lost", 1, 2))
	h1 := addAt(t, s, episode("/lib/TV/Heroes/S01E01.mkv", "Heroes", 1, 1))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}

	// Still episodes, of one show with another name.
	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: e1, Kind: KindEpisode, Show: "TV", Season: 1, Episode: 1},
		{ID: e2, Kind: KindEpisode, Show: "TV", Season: 1, Episode: 2},
		{ID: h1, Kind: KindEpisode, Show: "TV", Season: 1, Episode: 3},
	}); err != nil {
		t.Fatal(err)
	}
	got := archived(t, s)
	for _, p := range []string{"/lib/TV/Lost/S01E01.mkv", "/lib/TV/Lost/S01E02.mkv"} {
		if got[p].seriesID != 4607 || got[p].seriesShow != "Lost" {
			t.Errorf("archive row of %s = %+v; want the match of Lost", p, got[p])
		}
	}
	if _, err := s.GetSeriesMatch(ctx, "Lost"); err == nil {
		t.Error("the series row of a show with no episodes is still there")
	}
	if _, err := s.GetSeriesMatch(ctx, "TV"); err == nil {
		t.Error("the merged show was given a match it was not")
	}
	if pending, _ := s.PendingMatches(ctx); len(pending) != 0 {
		t.Errorf("PendingMatches = %+v; the episodes are of another show", pending)
	}

	// Back to Lost: the match is due again, for each episode.
	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: e1, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 1},
		{ID: e2, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 2},
		{ID: h1, Kind: KindEpisode, Show: "Heroes", Season: 1, Episode: 1},
	}); err != nil {
		t.Fatal(err)
	}
	want := []PendingMatch{{e1, "/lib/TV/Lost/S01E01.mkv", 4607, KindSeries}, {e2, "/lib/TV/Lost/S01E02.mkv", 4607, KindSeries}}
	if pending, err := s.PendingMatches(ctx); err != nil || !slices.Equal(pending, want) {
		t.Errorf("PendingMatches = %+v, %v; want %+v", pending, err, want)
	}

	// An episode that stays in its show archives nothing.
	if err := s.SetParsed(ctx, []ParsedRow{{ID: e1, Kind: KindEpisode, Show: "Lost", Season: 2, Episode: 1}}); err != nil {
		t.Fatal(err)
	}
	if n := countArchive(t, s); n != 2 {
		t.Errorf("%d archive rows after a reading that kept the show", n)
	}
}

// A show whose episodes are ALL read as the episodes of ONE other show, which had none
// and gets no others, is renamed: its manual match follows it, with its title and art,
// and nothing is archived. ("Lost" in "Lost (2004)/Season 1/Lost.S01E01.mkv" is
// "Lost (2004)" to a shows folder.)
func TestSetParsedRenamesAShowWithItsManualMatch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addAt(t, s, episode("/lib/Lost (2004)/S01E01.mkv", "Lost", 1, 1))
	e2 := addAt(t, s, episode("/lib/Lost (2004)/S01E02.mkv", "Lost", 1, 2))
	addAt(t, s, episode("/lib/Heroes/S01E01.mkv", "Heroes", 1, 1))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesArt(ctx, "Lost", Art{Poster: "/lib/Lost (2004)/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetSeriesMatch(ctx, "Lost")
	// The new name has a row of the matcher's: it goes, the manual match takes its place.
	if _, err := s.rw.ExecContext(ctx, `INSERT INTO series_matches (show, match_state) VALUES ('Lost (2004)', 'unmatched')`); err != nil {
		t.Fatal(err)
	}

	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: e1, Kind: KindEpisode, Show: "Lost (2004)", Season: 1, Episode: 1},
		{ID: e2, Kind: KindEpisode, Show: "Lost (2004)", Season: 1, Episode: 2},
	}); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetSeriesMatch(ctx, "Lost (2004)")
	if err != nil || m.State != MatchManual || m.TitleID != st || m.LocalPoster != before.LocalPoster || m.LocalPoster == "" || m.ID != before.ID {
		t.Errorf("the renamed show's row = %+v, %v; want the manual match, its art and its row, as %+v", m, err, before)
	}
	if _, err := s.GetSeriesMatch(ctx, "Lost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old name still has a row: %v", err)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("%d archive rows; a renamed show's match is not archived", n)
	}
	if m, _ := s.GetSeriesMatch(ctx, "Heroes"); m.State == MatchManual {
		t.Errorf("another show took the match: %+v", m)
	}
}

// A rename of a show with more episodes than a batch holds: the match is at the new
// name before the first batch, and nothing is archived by any of them.
func TestSetParsedRenamesAShowAcrossBatches(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	const files = 2*chunkSpec + 200
	ids := seedItems(t, s, "/lib", files)
	episodesOf(t, s, "/lib", "Old Show")
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Old Show", 2004))
	if err := s.SetSeriesMatch(ctx, "Old Show", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	rows := make([]ParsedRow, len(ids))
	for i, id := range ids {
		rows[i] = ParsedRow{ID: id, Kind: KindEpisode, Show: "New Show", Season: 1, Episode: i + 1}
	}
	if err := s.SetParsed(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if m, err := s.GetSeriesMatch(ctx, "New Show"); err != nil || m.State != MatchManual || m.TitleID != st {
		t.Errorf("the renamed show's match: %+v, %v", m, err)
	}
	if _, err := s.GetSeriesMatch(ctx, "Old Show"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old name: %v", err)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("%d archive rows after a rename", n)
	}
}

// Everything that is not a plain rename keeps archiving the match for the old show.
func TestSetParsedDoesNotRenameWhatIsNotARename(t *testing.T) {
	seed := func(t *testing.T) (*Store, []int64) {
		s, _ := openTemp(t)
		ctx := context.Background()
		var ids []int64
		for i := 1; i <= 3; i++ {
			ids = append(ids, addAt(t, s, episode(fmt.Sprintf("/lib/Lost/S01E0%d.mkv", i), "Lost", 1, i)))
		}
		ids = append(ids, addAt(t, s, episode("/lib/Heroes/S01E01.mkv", "Heroes", 1, 1))) // ids[3]
		st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
		if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
			t.Fatal(err)
		}
		return s, ids
	}
	ep := func(id int64, show string, n int) ParsedRow {
		return ParsedRow{ID: id, Kind: KindEpisode, Show: show, Season: 1, Episode: n}
	}
	cases := []struct {
		name      string
		prepare   func(t *testing.T, s *Store)
		rows      func(ids []int64) []ParsedRow
		keepsOld  bool // the old show keeps its row (it has episodes left)
		archived  int  // archive rows with the match of Lost
		newManual bool // the new name has a manual match of its own, kept
	}{
		{name: "some of the episodes only", keepsOld: true, archived: 2,
			rows: func(ids []int64) []ParsedRow {
				return []ParsedRow{ep(ids[0], "Lost (2004)", 1), ep(ids[1], "Lost (2004)", 2)}
			}},
		{name: "split in two shows", archived: 3,
			rows: func(ids []int64) []ParsedRow {
				return []ParsedRow{ep(ids[0], "Lost A", 1), ep(ids[1], "Lost B", 2), ep(ids[2], "Lost B", 3)}
			}},
		{name: "becoming movies", archived: 3,
			rows: func(ids []int64) []ParsedRow {
				var r []ParsedRow
				for _, id := range ids {
					r = append(r, ParsedRow{ID: id, Kind: KindMovie, Title: "Lost"})
				}
				return r
			}},
		{name: "another show comes in too", archived: 3,
			rows: func(ids []int64) []ParsedRow {
				// ids[3] is a Heroes episode, read as the same new show.
				return []ParsedRow{ep(ids[0], "TV", 1), ep(ids[1], "TV", 2), ep(ids[2], "TV", 3), ep(ids[3], "TV", 4)}
			}},
		{name: "the new show has episodes of its own", archived: 3,
			prepare: func(t *testing.T, s *Store) {
				addAt(t, s, episode("/lib/Lost (2004)/S09E01.mkv", "Lost (2004)", 9, 1))
			},
			rows: func(ids []int64) []ParsedRow {
				return []ParsedRow{ep(ids[0], "Lost (2004)", 1), ep(ids[1], "Lost (2004)", 2), ep(ids[2], "Lost (2004)", 3)}
			}},
		{name: "the new show has a manual match of its own", archived: 3, newManual: true,
			prepare: func(t *testing.T, s *Store) {
				if _, err := s.rw.Exec(`INSERT INTO series_matches (show, match_state) VALUES ('Lost (2004)', 'manual')`); err != nil {
					t.Fatal(err)
				}
			},
			rows: func(ids []int64) []ParsedRow {
				return []ParsedRow{ep(ids[0], "Lost (2004)", 1), ep(ids[1], "Lost (2004)", 2), ep(ids[2], "Lost (2004)", 3)}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, ids := seed(t)
			ctx := context.Background()
			if tc.prepare != nil {
				tc.prepare(t, s)
			}
			if err := s.SetParsed(ctx, tc.rows(ids)); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, a := range archived(t, s) {
				if a.seriesID == 4607 && a.seriesShow == "Lost" {
					n++
				}
			}
			if n != tc.archived {
				t.Errorf("%d archive rows with the match of Lost, want %d (%+v)", n, tc.archived, archived(t, s))
			}
			_, err := s.GetSeriesMatch(ctx, "Lost")
			if tc.keepsOld != (err == nil) {
				t.Errorf("the old show's row: %v, want it kept: %v", err, tc.keepsOld)
			}
			if m, err := s.GetSeriesMatch(ctx, "Lost (2004)"); err == nil && m.TitleID != 0 {
				t.Errorf("the new name took the match of Lost: %+v", m)
			}
			if m, err := s.GetSeriesMatch(ctx, "Lost (2004)"); tc.newManual && (err != nil || m.State != MatchManual) {
				t.Errorf("the new name's own manual row was lost: %+v, %v", m, err)
			}
		})
	}
}

// A path can have a movie's match and a show's at once in the archive (it was a
// movie, then an episode, then a movie again): neither takes the other's place.
func TestTheArchiveKeepsAMovieAndAShowMatchOfOnePath(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id := addAt(t, s, movie("/lib/Lost.mkv", "Lost"))
	mt, _ := s.UpsertTitle(ctx, tmdbMovie(438631, "Dune", 2021))
	st, _ := s.UpsertTitle(ctx, tmdbSeries(4607, "Lost", 2004))
	if err := s.SetItemMatch(ctx, id, Match{TitleID: mt, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	// A movie, matched by hand, becomes an episode of Lost: the movie's match is archived.
	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Lost", Match{TitleID: st, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	// And becomes a movie again: the show's match is archived beside it.
	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindMovie, Title: "Lost"}}); err != nil {
		t.Fatal(err)
	}
	a := archived(t, s)["/lib/Lost.mkv"]
	if a.tmdbID != 438631 || a.tmdbKind != KindMovie || a.seriesID != 4607 || a.seriesShow != "Lost" {
		t.Fatalf("archive row = %+v; want both matches", a)
	}
	// As a movie, only the movie's match is due.
	want := []PendingMatch{{id, "/lib/Lost.mkv", 438631, KindMovie}}
	if got, _ := s.PendingMatches(ctx); !slices.Equal(got, want) {
		t.Errorf("PendingMatches as a movie = %+v, want %+v", got, want)
	}
	// Forgetting it leaves the show's.
	if err := s.ForgetArchivedMatch(ctx, "/lib/Lost.mkv", KindMovie); err != nil {
		t.Fatal(err)
	}
	if a := archived(t, s)["/lib/Lost.mkv"]; a.tmdbID != 0 || a.seriesID != 4607 {
		t.Errorf("archive row after forgetting the movie's = %+v; want the show's kept", a)
	}
	// An episode of Lost again: the show's match is due.
	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindEpisode, Show: "Lost", Season: 1, Episode: 1}}); err != nil {
		t.Fatal(err)
	}
	want = []PendingMatch{{id, "/lib/Lost.mkv", 4607, KindSeries}}
	if got, _ := s.PendingMatches(ctx); !slices.Equal(got, want) {
		t.Errorf("PendingMatches as an episode = %+v, want %+v", got, want)
	}
	if err := s.ForgetArchivedMatch(ctx, "/lib/Lost.mkv", KindSeries); err != nil {
		t.Fatal(err)
	}
	if n := countArchive(t, s); n != 0 {
		t.Errorf("%d archive rows after forgetting both", n)
	}
	if err := s.ForgetArchivedMatch(ctx, "/lib/Lost.mkv", "person"); err == nil {
		t.Error("ForgetArchivedMatch accepted a kind that is neither")
	}
}

// Forgetting one of the two matches of a path leaves the other, whichever it is, and
// the row goes when nothing is left in it (progress counts).
func TestForgetArchivedMatchTakesOnlyItsOwnSlot(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	row := func(progressAt int64) {
		t.Helper()
		if _, err := s.rw.ExecContext(ctx, `DELETE FROM item_archive`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.rw.ExecContext(ctx, `
			INSERT INTO item_archive (path, size, tmdb_id, tmdb_kind, series_id, series_show, progress_at, archived_at)
			VALUES ('/lib/x.mkv', 1, 603, 'movie', 4607, 'Lost', ?, 1)`, progressAt); err != nil {
			t.Fatal(err)
		}
	}
	row(0)
	if err := s.ForgetArchivedMatch(ctx, "/lib/x.mkv", KindSeries); err != nil {
		t.Fatal(err)
	}
	if a := archived(t, s)["/lib/x.mkv"]; a.tmdbID != 603 || a.tmdbKind != KindMovie || a.seriesID != 0 || a.seriesShow != "" {
		t.Errorf("after forgetting the show's match: %+v; want the movie's kept", a)
	}
	row(0)
	if err := s.ForgetArchivedMatch(ctx, "/lib/x.mkv", KindMovie); err != nil {
		t.Fatal(err)
	}
	if a := archived(t, s)["/lib/x.mkv"]; a.tmdbID != 0 || a.tmdbKind != "" || a.seriesID != 4607 || a.seriesShow != "Lost" {
		t.Errorf("after forgetting the movie's match: %+v; want the show's kept", a)
	}
	// Progress keeps the row.
	row(5)
	for _, kind := range []string{KindMovie, KindSeries} {
		if err := s.ForgetArchivedMatch(ctx, "/lib/x.mkv", kind); err != nil {
			t.Fatal(err)
		}
	}
	if a, ok := archived(t, s)["/lib/x.mkv"]; !ok || a.progressAt != 5 {
		t.Errorf("the row with progress after forgetting both matches: %+v, %v", a, ok)
	}
}

// Removing a folder with forget leaves nothing of its files in the archive, a match
// still waiting to be applied included; other folders' rows are not touched.
func TestArchiveAndDeleteRootForgetClearsTheArchiveOfItsFiles(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/lib/m.mp4", "M")
	id := addAt(t, s, it)
	other := movie("/other/o.mp4", "O")
	other.Root = "/other"
	oid, _ := s.UpsertItem(ctx, other, nil)
	mt, _ := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	s.SetItemMatch(ctx, id, Match{TitleID: mt, State: MatchManual, Score: 1})
	s.SetItemMatch(ctx, oid, Match{TitleID: mt, State: MatchManual, Score: 1})
	s.SetProgress(ctx, oid, 10, 100)
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/other", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}

	// The folder is added back; nobody applies the match; the person removes it with forget.
	id = addAt(t, s, it)
	if _, _, ok, err := s.RestoreArchived(ctx, id, "/lib/m.mp4", 100); err != nil || !ok {
		t.Fatalf("RestoreArchived = %v, %v", ok, err)
	}
	if got, _ := s.PendingMatches(ctx); len(got) != 1 {
		t.Fatalf("control: PendingMatches = %+v", got)
	}
	if n, err := s.ArchiveAndDeleteRoot(ctx, "/lib", true); err != nil || n != 1 {
		t.Fatalf("ArchiveAndDeleteRoot(forget) = %d, %v", n, err)
	}
	if _, ok := archived(t, s)["/lib/m.mp4"]; ok {
		t.Errorf("the archive still holds the removed folder's file: %+v", archived(t, s))
	}
	if _, ok := archived(t, s)["/other/o.mp4"]; !ok {
		t.Errorf("forgetting a folder took another's archive row: %+v", archived(t, s))
	}
	// Added back again: nothing to apply.
	addAt(t, s, it)
	if got, _ := s.PendingMatches(ctx); len(got) != 0 {
		t.Errorf("PendingMatches after forget and add back = %+v", got)
	}
}

// An archive written before the show's slot existed (one slot, tmdb_kind 'series')
// is moved to it on upgrade, for any show, and is still offered to an episode.
func TestMigration006MovesAShowsMatchToItsOwnSlot(t *testing.T) {
	s, path := openAtVersion(t, 5)
	ctx := context.Background()
	id := addAt(t, s, episode("/lib/Lost/S01E01.mkv", "Lost", 1, 1))
	mid := addAt(t, s, movie("/lib/m.mp4", "M"))
	for _, q := range []string{
		`INSERT INTO item_archive (path, size, tmdb_id, tmdb_kind, archived_at) VALUES ('/lib/Lost/S01E01.mkv', 100, 4607, 'series', 1)`,
		`INSERT INTO item_archive (path, size, tmdb_id, tmdb_kind, archived_at) VALUES ('/lib/m.mp4', 100, 603, 'movie', 1)`,
	} {
		if _, err := s.rw.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 5 database: %v", err)
	}
	defer s.Close()
	got := archived(t, s)
	if a := got["/lib/Lost/S01E01.mkv"]; a.tmdbID != 0 || a.tmdbKind != "" || a.seriesID != 4607 || a.seriesShow != "" {
		t.Errorf("the show's row = %+v; want the match in the show's slot, for any show", a)
	}
	if a := got["/lib/m.mp4"]; a.tmdbID != 603 || a.tmdbKind != KindMovie || a.seriesID != 0 {
		t.Errorf("the movie's row = %+v; want it as it was", a)
	}
	want := []PendingMatch{{mid, "/lib/m.mp4", 603, KindMovie}, {id, "/lib/Lost/S01E01.mkv", 4607, KindSeries}}
	slices.SortFunc(want, func(a, b PendingMatch) int { return int(a.ItemID - b.ItemID) })
	if pending, err := s.PendingMatches(ctx); err != nil || !slices.Equal(pending, want) {
		t.Errorf("PendingMatches = %+v, %v; want %+v", pending, err, want)
	}
}

// A show in the review list carries its flagged episodes, with their ids and files.
func TestReviewListCarriesTheFlaggedEpisodes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	var ids []int64
	for _, it := range []*Item{
		episode("/m/B/Extras.mkv", "Bbb", 1, 0), episode("/m/B/Making Of.mkv", "Bbb", 1, 0),
		episode("/m/B/S01E01.mkv", "Bbb", 1, 1), episode("/m/C/Bonus.mkv", "Ccc", 1, 0),
	} {
		it.NeedsReview = it.Episode == 0
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	list, err := s.ReviewList(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ReviewList = %+v, %v; want Bbb and Ccc", list, err)
	}
	want := map[string][]ReviewEpisode{
		"Bbb": {{ID: ids[0], Path: "/m/B/Extras.mkv"}, {ID: ids[1], Path: "/m/B/Making Of.mkv"}},
		"Ccc": {{ID: ids[3], Path: "/m/C/Bonus.mkv"}},
	}
	for _, e := range list {
		if !slices.Equal(e.Episodes, want[e.Show]) {
			t.Errorf("%s: episodes %+v, want %+v", e.Show, e.Episodes, want[e.Show])
		}
	}
	// A fixed episode (the person looked) is out of the list.
	if _, err := s.rw.ExecContext(ctx, `UPDATE items SET needs_review = 0 WHERE id IN (?, ?)`, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ReviewList(ctx)
	if len(list) != 1 || list[0].Show != "Ccc" {
		t.Errorf("ReviewList after fixing Bbb's = %+v", list)
	}
}
