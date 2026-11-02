package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func dueIDs(t *testing.T, s *Store, now int64, limit int) []int64 {
	t.Helper()
	due, err := s.DueItems(context.Background(), now, limit)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, d := range due {
		ids = append(ids, d.ID)
	}
	return ids
}

func dueShows(t *testing.T, s *Store, now int64) []string {
	t.Helper()
	due, err := s.DueSeries(context.Background(), now, 100)
	if err != nil {
		t.Fatal(err)
	}
	var shows []string
	for _, d := range due {
		shows = append(shows, d.Show)
	}
	return shows
}

func TestSetItemMatchAndDueItems(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(path string) int64 {
		id, err := s.UpsertItem(ctx, movie(path, path), nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	fresh, later, matched, unmatched, manual, disabled := add("/m/fresh.mkv"), add("/m/later.mkv"),
		add("/m/matched.mkv"), add("/m/unmatched.mkv"), add("/m/manual.mkv"), add("/m/disabled.mkv")
	epID, _ := s.UpsertItem(ctx, episode("/m/ep.mkv", "Show", 1, 1), nil)

	titleID, err := s.UpsertTitle(ctx, tmdbMovie(1, "A", 2000))
	if err != nil {
		t.Fatal(err)
	}
	set := func(id int64, m Match) {
		t.Helper()
		if err := s.SetItemMatch(ctx, id, m); err != nil {
			t.Fatal(err)
		}
	}
	set(later, Match{State: MatchPending, Attempts: 2, NextAttemptAt: 1000})
	set(matched, Match{TitleID: titleID, State: MatchMatched, NeedsReview: true, Score: 0.7})
	set(unmatched, Match{State: MatchUnmatched, Score: 0.3})
	set(manual, Match{State: MatchManual})
	set(disabled, Match{State: MatchDisabled})

	// Everything pending and due, and nothing else: not matched, unmatched,
	// manual or disabled files, not episodes, and not what waits for a retry.
	if got := dueIDs(t, s, 999, 10); !slices.Equal(got, []int64{fresh}) {
		t.Errorf("due at 999 = %v, want only the fresh movie %d (episode %d is for DueSeries)", got, fresh, epID)
	}
	// Positive control for the retry: it is due once its time has come.
	if got := dueIDs(t, s, 1000, 10); !slices.Equal(got, []int64{fresh, later}) {
		t.Errorf("due at 1000 = %v, want [%d %d]", got, fresh, later)
	}
	if got := dueIDs(t, s, 1000, 1); len(got) != 1 {
		t.Errorf("limit 1 returned %v", got)
	}

	e, _ := s.GetEntry(ctx, matched)
	if e.TitleID != titleID || e.MatchState != MatchMatched || !e.NeedsReview {
		t.Errorf("matched item: %+v", e.Item)
	}
	row := func(id int64) (score float64, attempts int, next int64) {
		s.db.QueryRow(`SELECT match_score, match_attempts, next_attempt_at FROM items WHERE id = ?`, id).Scan(&score, &attempts, &next)
		return
	}
	if score, attempts, next := row(later); score != 0 || attempts != 2 || next != 1000 {
		t.Errorf("later: %v %v %v", score, attempts, next)
	}
	if score, _, _ := row(matched); score != 0.7 {
		t.Errorf("score = %v", score)
	}

	// A transient failure: still pending, attempts + 1, due again later.
	if err := s.DeferItemMatch(ctx, fresh, 5000); err != nil {
		t.Fatal(err)
	}
	if _, attempts, next := row(fresh); attempts != 1 || next != 5000 {
		t.Errorf("after defer: attempts %d, next %d", attempts, next)
	}
	if err := s.DeferItemMatch(ctx, fresh, 9000); err != nil {
		t.Fatal(err)
	}
	if _, attempts, _ := row(fresh); attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if got := dueIDs(t, s, 4999, 10); slices.Contains(got, fresh) {
		t.Errorf("a deferred movie is due before its time: %v", got)
	}
	due, _ := s.DueItems(ctx, 9000, 10)
	if len(due) != 2 || due[0].ID != later || due[1].ID != fresh || due[1].Attempts != 2 || due[1].Path != "/m/fresh.mkv" {
		t.Errorf("due at 9000 = %+v; want later (1000) then fresh (9000) with attempts", due)
	}

	// Un-linking, and the errors.
	set(matched, Match{State: MatchManual})
	if e, _ = s.GetEntry(ctx, matched); e.TitleID != 0 || e.MatchState != MatchManual || e.NeedsReview {
		t.Errorf("after unmatch: %+v", e.Item)
	}
	if err := s.SetItemMatch(ctx, 9999, Match{State: MatchPending}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown item = %v, want ErrNotFound", err)
	}
	if err := s.DeferItemMatch(ctx, 9999, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("defer unknown item = %v, want ErrNotFound", err)
	}
	if err := s.SetItemMatch(ctx, fresh, Match{State: "maybe"}); err == nil {
		t.Error("SetItemMatch accepted state maybe")
	}
	if err := s.SetItemMatch(ctx, fresh, Match{TitleID: 424242, State: MatchMatched}); err == nil {
		t.Error("SetItemMatch linked a title that does not exist")
	}

	// A title that goes away leaves its items unlinked, not deleted.
	set(matched, Match{TitleID: titleID, State: MatchMatched})
	if _, err := s.db.Exec(`DELETE FROM titles WHERE id = ?`, titleID); err != nil {
		t.Fatal(err)
	}
	if e, err = s.GetEntry(ctx, matched); err != nil || e.TitleID != 0 {
		t.Errorf("item after its title was deleted: %+v, %v", e.Item, err)
	}
}

func TestDueSeriesAndSeriesMatches(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, ep := range []*Item{
		episode("/m/Brand New/S01E01.mkv", "Brand New", 1, 1),
		episode("/m/Brand New/S01E02.mkv", "Brand New", 1, 2),
		episode("/m/Waiting/S01E01.mkv", "Waiting", 1, 1),
		episode("/m/Done/S01E01.mkv", "Done", 1, 1),
		episode("/m/Lost/S01E01.mkv", "Lost", 1, 1),
		episode("/m/Anon/S01E01.mkv", "", 1, 1),
	} {
		if _, err := s.UpsertItem(ctx, ep, nil); err != nil {
			t.Fatal(err)
		}
	}
	titleID, _ := s.UpsertTitle(ctx, tmdbSeries(1, "Done", 2000))
	for show, m := range map[string]Match{
		"Waiting": {State: MatchPending, Attempts: 3, NextAttemptAt: 200},
		"Done":    {TitleID: titleID, State: MatchMatched, Score: 0.9},
		"Lost":    {State: MatchUnmatched},
	} {
		if err := s.SetSeriesMatch(ctx, show, m); err != nil {
			t.Fatal(err)
		}
	}

	// A show nobody has looked at has no row and is due; a nameless one is not a show.
	if got := dueShows(t, s, 100); !slices.Equal(got, []string{"Brand New"}) {
		t.Errorf("due at 100 = %v, want [Brand New]", got)
	}
	got, _ := s.DueSeries(ctx, 100, 10)
	if got[0].Path != "/m/Brand New/S01E01.mkv" || got[0].Attempts != 0 {
		t.Errorf("due = %+v; want the first episode's path", got[0])
	}
	// Positive control: Waiting is due once its time comes.
	if got := dueShows(t, s, 200); !slices.Equal(got, []string{"Brand New", "Waiting"}) {
		t.Errorf("due at 200 = %v", got)
	}
	if due, _ := s.DueSeries(ctx, 200, 10); due[1].Attempts != 3 {
		t.Errorf("attempts = %d, want 3", due[1].Attempts)
	}

	// Deferring a show without a row creates one; it is then not due until next.
	if err := s.DeferSeriesMatch(ctx, "Brand New", 700); err != nil {
		t.Fatal(err)
	}
	sm, err := s.GetSeriesMatch(ctx, "Brand New")
	if err != nil || sm.Attempts != 1 || sm.NextAttemptAt != 700 || sm.State != MatchPending {
		t.Errorf("after defer: %+v, %v", sm, err)
	}
	if got := dueShows(t, s, 699); slices.Contains(got, "Brand New") {
		t.Errorf("due at 699 = %v; Brand New waits until 700", got)
	}
	if got := dueShows(t, s, 700); !slices.Contains(got, "Brand New") {
		t.Errorf("due at 700 = %v; Brand New is due again", got)
	}
	s.DeferSeriesMatch(ctx, "Brand New", 900)
	if sm, _ = s.GetSeriesMatch(ctx, "Brand New"); sm.Attempts != 2 || sm.NextAttemptAt != 900 {
		t.Errorf("second defer: %+v", sm)
	}

	// Errors, and the lookups.
	if err := s.SetSeriesMatch(ctx, "No Such Show", Match{State: MatchPending}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSeriesMatch(no episodes) = %v, want ErrNotFound", err)
	}
	if err := s.DeferSeriesMatch(ctx, "No Such Show", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeferSeriesMatch(no episodes) = %v, want ErrNotFound", err)
	}
	if err := s.SetSeriesMatch(ctx, "Done", Match{State: "maybe"}); err == nil {
		t.Error("accepted state maybe")
	}
	if _, err := s.GetSeriesMatch(ctx, "Anon"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSeriesMatch(no row) = %v, want ErrNotFound", err)
	}
	all, err := s.SeriesMatches(ctx)
	if err != nil || len(all) != 4 || all["Done"].TitleID != titleID || all["Lost"].State != MatchUnmatched {
		t.Errorf("SeriesMatches = %+v, %v", all, err)
	}
	if _, ok := all["Anon"]; ok {
		t.Error("SeriesMatches has a row for a show nobody matched")
	}
}

func TestCountMatchesAndReviewList(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	addItem := func(path, title string, year int, m *Match) int64 {
		it := movie(path, title)
		it.Year = year
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m != nil {
			if err := s.SetItemMatch(ctx, id, *m); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	candidate, _ := s.UpsertTitle(ctx, tmdbMovie(10, "Dune", 2021))
	other, _ := s.UpsertTitle(ctx, tmdbMovie(11, "Fine", 2000))

	addItem("/m/pending.mkv", "Pending", 2000, nil)
	okID := addItem("/m/fine.mkv", "Fine", 2000, &Match{TitleID: other, State: MatchMatched, Score: 0.95})
	_ = okID
	reviewID := addItem("/m/dune.mkv", "Dune", 1984, &Match{TitleID: candidate, State: MatchMatched, NeedsReview: true, Score: 0.7})
	lostID := addItem("/m/zzz.mkv", "Zzz Unknown", 2010, &Match{State: MatchUnmatched, Score: 0.2})
	addItem("/m/manual.mkv", "Manual", 2000, &Match{State: MatchManual})

	for _, ep := range []*Item{
		episode("/m/A/S01E01.mkv", "Aaa", 1, 1), episode("/m/B/S01E01.mkv", "Bbb", 1, 1),
		episode("/m/C/S01E01.mkv", "Ccc", 1, 1), episode("/m/D/S01E01.mkv", "Ddd", 1, 1),
	} {
		s.UpsertItem(ctx, ep, nil)
	}
	s.SetSeriesMatch(ctx, "Bbb", Match{TitleID: candidate, State: MatchMatched, NeedsReview: true, Score: 0.6})
	s.SetSeriesMatch(ctx, "Ccc", Match{State: MatchUnmatched})
	s.SetSeriesMatch(ctx, "Ddd", Match{TitleID: other, State: MatchMatched, Score: 1})
	// A row for a show whose episodes are gone is not counted or listed.
	s.db.Exec(`INSERT INTO series_matches (show, match_state) VALUES ('Vanished', 'unmatched')`)

	c, err := s.CountMatches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Pending: 1 movie + show Aaa (no row). Unmatched: 1 movie + Ccc. Review: Dune + Bbb.
	if want := (MatchCounts{Pending: 2, Unmatched: 2, NeedsReview: 2}); c != want {
		t.Errorf("CountMatches = %+v, want %+v", c, want)
	}

	list, err := s.ReviewList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Kind, Title, State string
		Review             bool
		Candidate          int64
	}
	var got []row
	for _, e := range list {
		r := row{Kind: e.Kind, Title: e.Title, State: e.MatchState, Review: e.NeedsReview}
		if e.Candidate != nil {
			r.Candidate = e.Candidate.ID
		}
		got = append(got, r)
	}
	// Unmatched first (by title), then the ones to review (by title).
	want := []row{
		{KindSeries, "Ccc", MatchUnmatched, false, 0},
		{KindMovie, "Zzz Unknown", MatchUnmatched, false, 0},
		{KindSeries, "Bbb", MatchMatched, true, candidate},
		{KindMovie, "Dune", MatchMatched, true, candidate},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ReviewList:\n got %+v\nwant %+v", got, want)
	}
	for _, e := range list {
		switch e.Title {
		case "Dune":
			if e.ItemID != reviewID || e.Year != 1984 || e.Score != 0.7 || e.Show != "" {
				t.Errorf("Dune entry = %+v", e)
			}
			if e.Candidate == nil || e.Candidate.Title != "Dune" || e.Candidate.Year != 2021 {
				t.Errorf("Dune candidate = %+v (the TMDB title, not the file's parse)", e.Candidate)
			}
		case "Zzz Unknown":
			if e.ItemID != lostID || e.Year != 2010 {
				t.Errorf("Zzz entry = %+v", e)
			}
		case "Bbb":
			if e.Show != "Bbb" || e.ItemID != 0 {
				t.Errorf("Bbb entry = %+v", e)
			}
		}
	}
}

func TestSidecarArtwork(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/m/a.mkv", "A"), nil)
	s.UpsertItem(ctx, episode("/m/Show/S01E01.mkv", "Show", 1, 1), nil)
	changes := func() int64 {
		var n int64
		s.db.QueryRow(`SELECT total_changes()`).Scan(&n)
		return n
	}

	if _, err := s.LocalArt(ctx, ArtItem, id, "poster"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LocalArt before any art = %v, want ErrNotFound", err)
	}
	art := Art{Poster: "/m/poster.jpg", Backdrop: "/m/fanart.jpg", Thumb: "/m/a-thumb.jpg"}
	if err := s.SetItemArt(ctx, id, art); err != nil {
		t.Fatal(err)
	}
	for slot, want := range map[string]string{"poster": art.Poster, "backdrop": art.Backdrop, "thumb": art.Thumb} {
		if got, err := s.LocalArt(ctx, ArtItem, id, slot); err != nil || got != want {
			t.Errorf("LocalArt(item, %s) = %q, %v; want %q", slot, got, err, want)
		}
	}
	e, _ := s.GetEntry(ctx, id)
	if e.LocalPoster != art.Poster || e.LocalBackdrop != art.Backdrop || e.LocalThumb != art.Thumb {
		t.Errorf("entry art: %+v", e.Item)
	}
	before := changes()
	if err := s.SetItemArt(ctx, id, art); err != nil {
		t.Fatal(err)
	}
	if after := changes(); after != before {
		t.Errorf("an unchanged SetItemArt wrote %d rows", after-before)
	}
	// Positive control: the probe sees a real change.
	if err := s.SetItemArt(ctx, id, Art{Poster: art.Poster}); err != nil {
		t.Fatal(err)
	}
	if after := changes(); after == before {
		t.Error("SetItemArt with a changed record wrote nothing, or total_changes() does not see writes")
	}
	if _, err := s.LocalArt(ctx, ArtItem, id, "backdrop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a removed backdrop is still served: %v", err)
	}
	if err := s.SetItemArt(ctx, 9999, art); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetItemArt(unknown) = %v", err)
	}
	if _, err := s.LocalArt(ctx, ArtItem, id, "logo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown slot = %v", err)
	}
	if _, err := s.LocalArt(ctx, "movie", id, "poster"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown kind = %v", err)
	}

	// Shows.
	if err := s.SetSeriesArt(ctx, "Show", Art{Poster: "/m/Show/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	sm, err := s.GetSeriesMatch(ctx, "Show")
	if err != nil || sm.LocalPoster != "/m/Show/poster.jpg" || sm.State != MatchPending {
		t.Fatalf("series after art: %+v, %v", sm, err)
	}
	if got, err := s.LocalArt(ctx, ArtSeries, sm.ID, "poster"); err != nil || got != "/m/Show/poster.jpg" {
		t.Errorf("LocalArt(series) = %q, %v", got, err)
	}
	if _, err := s.LocalArt(ctx, ArtSeries, sm.ID, "backdrop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("series backdrop = %v", err)
	}
	if _, err := s.LocalArt(ctx, ArtSeries, sm.ID, "thumb"); !errors.Is(err, ErrNotFound) {
		t.Errorf("series thumb = %v", err)
	}
	before = changes()
	if err := s.SetSeriesArt(ctx, "Show", Art{Poster: "/m/Show/poster.jpg"}); err != nil {
		t.Fatalf("unchanged SetSeriesArt: %v", err)
	}
	if after := changes(); after != before {
		t.Errorf("an unchanged SetSeriesArt wrote %d rows", after-before)
	}
	if err := s.SetSeriesArt(ctx, "Show", Art{Poster: "/m/Show/poster.jpg", Backdrop: "/m/Show/fanart.jpg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LocalArt(ctx, ArtSeries, sm.ID, "backdrop"); err != nil {
		t.Errorf("series backdrop after the change: %v", err)
	}
	if err := s.SetSeriesArt(ctx, "Nope", art); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSeriesArt(no episodes) = %v", err)
	}
}
