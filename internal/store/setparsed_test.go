package store

import (
	"context"
	"errors"
	"testing"
)

type parsedEnv struct {
	s *Store
	// movies
	auto, manualMovie, manualNone, manualFlip, manualNoneFlip int64
	autoStay, unmatchedStay                                   int64 // the worker's matches, on movies that stay movies
	// episodes
	oldShow1, oldShow2, shared, otherRoot, manualShow, manualOther, goneShow int64
	movieTitle, seriesTitle                                                  int64
}

func newParsedEnv(t *testing.T) *parsedEnv {
	t.Helper()
	s, _ := openTemp(t)
	ctx := context.Background()
	e := &parsedEnv{s: s}
	add := func(it *Item, root string) int64 {
		it.Root = root
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	var err error
	if e.movieTitle, err = s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999)); err != nil {
		t.Fatal(err)
	}
	if e.seriesTitle, err = s.UpsertTitle(ctx, tmdbSeries(1396, "Breaking Bad", 2008)); err != nil {
		t.Fatal(err)
	}
	e.auto = add(movie("/lib/auto.mp4", "Auto"), "/lib")
	e.manualMovie = add(movie("/lib/manual.mp4", "Manual"), "/lib")
	e.manualNone = add(movie("/lib/none.mp4", "None"), "/lib")
	e.manualFlip = add(movie("/lib/flip.mp4", "Flip"), "/lib")
	e.manualNoneFlip = add(movie("/lib/noneflip.mp4", "NoneFlip"), "/lib")
	e.autoStay = add(movie("/lib/autostay.mp4", "AutoStay"), "/lib")
	e.unmatchedStay = add(movie("/lib/unmatched.mp4", "Unmatched"), "/lib")
	for id, m := range map[int64]Match{
		e.auto:           {TitleID: e.movieTitle, State: MatchMatched, NeedsReview: true, Score: 0.7, Attempts: 2, NextAttemptAt: 500},
		e.manualMovie:    {TitleID: e.movieTitle, State: MatchManual, Score: 1},
		e.manualNone:     {State: MatchManual},
		e.manualFlip:     {TitleID: e.movieTitle, State: MatchManual, Score: 1},
		e.manualNoneFlip: {State: MatchManual},
		e.autoStay:       {TitleID: e.movieTitle, State: MatchMatched, Score: 0.8},
		e.unmatchedStay:  {State: MatchUnmatched, Attempts: 4, NextAttemptAt: 900},
	} {
		if err := s.SetItemMatch(ctx, id, m); err != nil {
			t.Fatal(err)
		}
	}

	e.oldShow1 = add(episode("/lib/Old/S01E01.mkv", "Old", 1, 1), "/lib")
	e.oldShow2 = add(episode("/lib/Old/S01E02.mkv", "Old", 1, 2), "/lib")
	e.shared = add(episode("/lib/Shared/S01E01.mkv", "Shared", 1, 1), "/lib")
	e.otherRoot = add(episode("/other/Shared/S01E02.mkv", "Shared", 1, 2), "/other")
	e.manualShow = add(episode("/lib/Fixed/S01E01.mkv", "Fixed", 1, 1), "/lib")
	e.manualOther = add(episode("/other/Fixed/S01E02.mkv", "Fixed", 1, 2), "/other")
	e.goneShow = add(episode("/lib/Gone/S01E01.mkv", "Gone", 1, 1), "/lib")
	for show, m := range map[string]Match{
		"Old":    {TitleID: e.seriesTitle, State: MatchMatched, Score: 0.9},
		"Shared": {TitleID: e.seriesTitle, State: MatchMatched, Score: 0.9, NeedsReview: true, Attempts: 3},
		"Fixed":  {TitleID: e.seriesTitle, State: MatchManual, Score: 1},
		"Gone":   {TitleID: e.seriesTitle, State: MatchManual, Score: 1},
	} {
		if err := s.SetSeriesMatch(ctx, show, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSeriesArt(ctx, "Shared", Art{Poster: "/lib/Shared/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *parsedEnv) entry(t *testing.T, id int64) Entry {
	t.Helper()
	en, err := e.s.GetEntry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return en
}

func (e *parsedEnv) match(t *testing.T, id int64) Match {
	t.Helper()
	m, err := e.s.GetItemMatch(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSetParsedMovies(t *testing.T) {
	e := newParsedEnv(t)
	s := e.s
	ctx := context.Background()

	// Controls: the states the test starts from.
	if m := e.match(t, e.auto); m.State != MatchMatched || m.TitleID != e.movieTitle || !m.NeedsReview || m.Attempts != 2 {
		t.Fatalf("control: auto = %+v", m)
	}

	err := s.SetParsed(ctx, []ParsedRow{
		// A movie folder turned shows folder: the movies become episodes.
		{ID: e.auto, Kind: KindEpisode, Title: "Pilot", Show: "Auto", Season: 2, Episode: 0, NeedsReview: true},
		{ID: e.manualFlip, Kind: KindEpisode, Title: "Flip", Show: "Flip", Season: 1, Episode: 4},
		{ID: e.manualNoneFlip, Kind: KindEpisode, Show: "NoneFlip", Season: 1},
		// Still movies (a re-parse of the same kind): only the names change.
		{ID: e.manualMovie, Kind: KindMovie, Title: "The Matrix", Year: 1999},
		{ID: e.manualNone, Kind: KindMovie, Title: "None", Year: 2001},
		{ID: e.autoStay, Kind: KindMovie, Title: "Auto Stay", Year: 2010},
		{ID: e.unmatchedStay, Kind: KindMovie, Title: "Unmatched Again"},
		{ID: 99999, Kind: KindMovie, Title: "No such item"},
	})
	if err != nil {
		t.Fatal(err)
	}

	auto := e.entry(t, e.auto)
	if auto.Kind != KindEpisode || auto.Title != "Pilot" || auto.Show != "Auto" || auto.Season != 2 || auto.Episode != 0 || auto.Year != 0 {
		t.Errorf("auto parsed fields: %+v", auto.Item)
	}
	// A match the worker made is gone, and the item is due at once.
	if m := e.match(t, e.auto); m != (Match{State: MatchPending, NeedsReview: true}) {
		t.Errorf("auto match = %+v, want a fresh pending one with NeedsReview from the parse", m)
	}
	// A manual movie match on what is now an episode no longer fits.
	if m := e.match(t, e.manualFlip); m != (Match{State: MatchPending}) {
		t.Errorf("manual movie match on an episode = %+v, want dropped", m)
	}
	if got := e.entry(t, e.manualFlip); got.TitleID != 0 || got.Kind != KindEpisode || got.Episode != 4 {
		t.Errorf("flipped: %+v", got.Item)
	}
	// A manual "no metadata" decided for a movie is not one for an episode.
	if m := e.match(t, e.manualNoneFlip); m.State != MatchPending {
		t.Errorf("manual no-match on an episode = %+v, want pending", m)
	}
	// A manual match that still fits stays, whatever the names say.
	if m := e.match(t, e.manualMovie); m != (Match{TitleID: e.movieTitle, State: MatchManual, Score: 1}) {
		t.Errorf("manual movie match kept as %+v", m)
	}
	if got := e.entry(t, e.manualMovie); got.Year != 1999 || got.Title != "The Matrix" {
		t.Errorf("manual movie not re-parsed: %+v", got.Item)
	}
	if m := e.match(t, e.manualNone); m.State != MatchManual {
		t.Errorf("manual no-match on a movie = %+v, want kept", m)
	}
	// A match the worker made is cleared although the item is still a movie: the
	// names it was made from have changed.
	if m := e.match(t, e.autoStay); m != (Match{State: MatchPending}) {
		t.Errorf("matched movie that stays a movie = %+v, want a fresh pending match", m)
	}
	if got := e.entry(t, e.autoStay); got.TitleID != 0 || got.Title != "Auto Stay" || got.Year != 2010 {
		t.Errorf("autoStay: %+v", got.Item)
	}
	if m := e.match(t, e.unmatchedStay); m != (Match{State: MatchPending}) {
		t.Errorf("unmatched movie that stays a movie = %+v, want a fresh pending match (due now)", m)
	}
	// The pending items are due.
	due, _ := s.DueItems(ctx, 1, 10)
	if len(due) != 2 || due[0].ID == due[1].ID { // the two cleared movies; the episodes are for DueSeries
		t.Errorf("DueItems = %+v, want the two cleared movies", due)
	}
}

// The other direction: episodes become movies.
func TestSetParsedEpisodesBecomeMovies(t *testing.T) {
	e := newParsedEnv(t)
	s := e.s
	ctx := context.Background()
	rows := []ParsedRow{
		{ID: e.oldShow1, Kind: KindMovie, Title: "Old S01E01"},
		{ID: e.oldShow2, Kind: KindMovie, Title: "Old S01E02", Year: 2010},
		{ID: e.shared, Kind: KindMovie, Title: "Shared S01E01"},
		{ID: e.manualShow, Kind: KindMovie, Title: "Fixed S01E01"},
		{ID: e.goneShow, Kind: KindMovie, Title: "Gone S01E01"},
	}
	series, _ := s.SeriesMatches(ctx)
	for _, show := range []string{"Old", "Shared", "Fixed", "Gone"} {
		if _, ok := series[show]; !ok {
			t.Fatalf("control: no series row for %s", show)
		}
	}
	if err := s.SetParsed(ctx, rows); err != nil {
		t.Fatal(err)
	}

	got := e.entry(t, e.oldShow2)
	if got.Kind != KindMovie || got.Title != "Old S01E02" || got.Year != 2010 || got.Show != "" || got.Season != 0 || got.Episode != 0 {
		t.Errorf("episode as a movie: %+v", got.Item)
	}
	if m := e.match(t, e.oldShow2); m.State != MatchPending || m.TitleID != 0 {
		t.Errorf("new movie match = %+v, want pending", m)
	}
	series, _ = s.SeriesMatches(ctx)
	// Shows left with no episode lose their row, a manual match included.
	for _, show := range []string{"Old", "Gone"} {
		if _, ok := series[show]; ok {
			t.Errorf("series row of %s survived with no episodes", show)
		}
	}
	// A show that still has an episode elsewhere: the worker's match is cleared,
	// the folder art stays; a manual match stands.
	if sh := series["Shared"]; sh.State != MatchPending || sh.TitleID != 0 || sh.NeedsReview || sh.Attempts != 0 || sh.LocalPoster != "/lib/Shared/poster.jpg" {
		t.Errorf("Shared = %+v, want pending, no title, art kept", sh)
	}
	if fx := series["Fixed"]; fx.State != MatchManual || fx.TitleID != e.seriesTitle {
		t.Errorf("Fixed = %+v, want its manual match", fx)
	}
	// The show with no row is due for matching again; the cleared one too.
	due, _ := s.DueSeries(ctx, 1, 10)
	var names []string
	for _, d := range due {
		names = append(names, d.Show)
	}
	if len(names) != 1 || names[0] != "Shared" {
		t.Errorf("DueSeries = %v, want [Shared]", names)
	}
}

// A kind change that only renames: the series row of the new name is cleared too.
func TestSetParsedRenamedShow(t *testing.T) {
	e := newParsedEnv(t)
	ctx := context.Background()
	if err := e.s.SetParsed(ctx, []ParsedRow{
		{ID: e.oldShow1, Kind: KindEpisode, Show: "Fixed", Season: 1, Episode: 1, Title: "x"},
		{ID: e.oldShow2, Kind: KindEpisode, Show: "Fixed", Season: 1, Episode: 2, Title: "y"},
	}); err != nil {
		t.Fatal(err)
	}
	series, _ := e.s.SeriesMatches(ctx)
	if _, ok := series["Old"]; ok {
		t.Error("the old name keeps its row")
	}
	if fx := series["Fixed"]; fx.State != MatchManual {
		t.Errorf("a manual match of the new name: %+v, want kept", fx)
	}
	if got := e.entry(t, e.oldShow1); got.Show != "Fixed" || got.Title != "x" {
		t.Errorf("renamed: %+v", got.Item)
	}
}

// A call is checked whole before its first batch is written: a bad row anywhere
// refuses it with nothing applied. (A failure while a later batch is written is
// another matter: TestSetParsedStopsAndResumesBetweenBatches.)
func TestSetParsedIsOneTransaction(t *testing.T) {
	e := newParsedEnv(t)
	ctx := context.Background()
	err := e.s.SetParsed(ctx, []ParsedRow{
		{ID: e.auto, Kind: KindEpisode, Title: "Pilot", Show: "Auto", Season: 1},
		{ID: e.manualMovie, Kind: "mixed"},
	})
	if err == nil {
		t.Fatal("a row with a bad kind was accepted")
	}
	if got := e.entry(t, e.auto); got.Kind != KindMovie || got.Title != "Auto" {
		t.Errorf("the first row was applied: %+v", got.Item)
	}
	if m := e.match(t, e.auto); m.State != MatchMatched {
		t.Errorf("the first row's match was cleared: %+v", m)
	}
	if err := e.s.SetParsed(ctx, nil); err != nil {
		t.Errorf("no rows: %v", err)
	}
	if _, err := e.s.GetEntry(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("control: %v", err)
	}
}
