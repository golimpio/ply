package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestTitleBriefs(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := fullMatrix()
	_, matrixID := addMovie(t, s, "/media/matrix.mkv", in)
	ghost, _ := s.UpsertTitle(ctx, tmdbMovie(9, "Ghost", 2020)) // no file: not in the library

	briefs, err := s.TitleBriefs(ctx, []int64{matrixID, ghost, 4242})
	if err != nil {
		t.Fatal(err)
	}
	if len(briefs) != 1 {
		t.Fatalf("briefs = %+v; only library titles", briefs)
	}
	b := briefs[matrixID]
	if b.Title != "The Matrix" || b.Overview != "About The Matrix" || b.Logo != "tmdb:/l603.png" || b.Certification != "R" ||
		!slices.Equal(b.Genres, []string{"Action", "Science Fiction"}) || b.Poster != "tmdb:/p603.jpg" || b.ItemID == 0 {
		t.Errorf("brief = %+v", b)
	}
	if got, err := s.TitleBriefs(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("no ids = %v, %v", got, err)
	}

	// More ids than one chunk.
	ids := make([]int64, 0, 3*briefChunk)
	for i := int64(1); i <= 3*briefChunk; i++ {
		ids = append(ids, i+100000)
	}
	ids = append(ids, matrixID)
	if got, err := s.TitleBriefs(ctx, ids); err != nil || len(got) != 1 {
		t.Errorf("chunked = %d, %v", len(got), err)
	}
}

func TestShowSeasonQueries(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	show := tmdbSeries(1, "Show", 2010)
	show.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1}}}}
	id := addShow(t, s, "Show", []int{1, 2, 4}, show)
	other := addShow(t, s, "Other", []int{1, 3}, tmdbSeries(2, "Other", 2011))
	nfo, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindSeries, Title: "Local only"}})
	if err != nil {
		t.Fatal(err)
	}
	s.UpsertItem(ctx, episode("/media/Local/S09E01.mkv", "Local", 9, 1), nil)
	if err := s.SetSeriesMatch(ctx, "Local", Match{TitleID: nfo, State: MatchMatched}); err != nil {
		t.Fatal(err)
	}

	if got, _ := s.ShowSeasons(ctx, "Show"); !slices.Equal(got, []int{1, 2, 4}) {
		t.Errorf("ShowSeasons = %v", got)
	}
	if got, _ := s.ShowSeasons(ctx, "Nope"); len(got) != 0 {
		t.Errorf("ShowSeasons(unknown) = %v", got)
	}
	if got, _ := s.SeasonsWithFiles(ctx, id); !slices.Equal(got, []int{1, 2, 4}) {
		t.Errorf("SeasonsWithFiles = %v", got)
	}
	items, err := s.ShowItems(ctx, "Show")
	if err != nil || len(items) != 3 || items[0].Season != 1 || items[2].Season != 4 {
		t.Errorf("ShowItems = %+v, %v", items, err)
	}

	// Seasons with files and no stored episodes; titles without a TMDB id are
	// not fetched at all.
	all, err := s.SeasonsNeedingFetchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(all[id], []int{2, 4}) || !slices.Equal(all[other], []int{1, 3}) || len(all) != 2 {
		t.Errorf("SeasonsNeedingFetchAll = %v (season 1 of Show has episodes; the local-only show is not fetched)", all)
	}
}

func TestReleasePendingAndRequeueUnmatched(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(path string) int64 {
		id, _ := s.UpsertItem(ctx, movie(path, path), nil)
		return id
	}
	waiting, matched, lost, manual := add("/m/waiting.mkv"), add("/m/matched.mkv"), add("/m/lost.mkv"), add("/m/manual.mkv")
	s.UpsertItem(ctx, episode("/m/S/S01E01.mkv", "S", 1, 1), nil)
	s.UpsertItem(ctx, episode("/m/T/S01E01.mkv", "T", 1, 1), nil)
	set := func(id int64, m Match) {
		t.Helper()
		if err := s.SetItemMatch(ctx, id, m); err != nil {
			t.Fatal(err)
		}
	}
	set(waiting, Match{State: MatchPending, Attempts: 3, NextAttemptAt: 9999})
	set(matched, Match{State: MatchMatched, NextAttemptAt: 9999})
	set(lost, Match{State: MatchUnmatched, Attempts: 2, NeedsReview: true, NextAttemptAt: 9999})
	set(manual, Match{State: MatchManual})
	s.SetSeriesMatch(ctx, "S", Match{State: MatchPending, NextAttemptAt: 9999})
	s.SetSeriesMatch(ctx, "T", Match{State: MatchUnmatched})

	if err := s.ReleasePending(ctx); err != nil {
		t.Fatal(err)
	}
	next := func(id int64) (n int64) {
		s.db.QueryRow(`SELECT next_attempt_at FROM items WHERE id = ?`, id).Scan(&n)
		return
	}
	if next(waiting) != 0 || next(matched) != 9999 || next(lost) != 9999 {
		t.Errorf("ReleasePending: waiting %d (want 0), matched %d, lost %d (untouched, 9999)", next(waiting), next(matched), next(lost))
	}
	if sm, _ := s.GetSeriesMatch(ctx, "S"); sm.NextAttemptAt != 0 || sm.Attempts != 0 {
		t.Errorf("series S = %+v", sm)
	}
	if sm, _ := s.GetSeriesMatch(ctx, "T"); sm.State != MatchUnmatched {
		t.Errorf("an unmatched show was released: %+v", sm)
	}
	var attempts int
	s.db.QueryRow(`SELECT match_attempts FROM items WHERE id = ?`, waiting).Scan(&attempts)
	if attempts != 3 {
		t.Errorf("ReleasePending reset attempts to %d", attempts)
	}

	n, err := s.RequeueUnmatched(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RequeueUnmatched = %d, %v; want the movie and the show", n, err)
	}
	e, _ := s.GetEntry(ctx, lost)
	if e.MatchState != MatchPending || e.NeedsReview || next(lost) != 0 {
		t.Errorf("requeued movie: %+v", e.Item)
	}
	if sm, _ := s.GetSeriesMatch(ctx, "T"); sm.State != MatchPending {
		t.Errorf("requeued show: %+v", sm)
	}
	if e, _ = s.GetEntry(ctx, manual); e.MatchState != MatchManual {
		t.Errorf("a manual match was requeued: %+v", e.Item)
	}
	if e, _ = s.GetEntry(ctx, matched); e.MatchState != MatchMatched {
		t.Errorf("a matched movie was requeued: %+v", e.Item)
	}
}

func TestEmbeddedCover(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	withTMDB, titleID := addMovie(t, s, "/m/with.mkv", tmdbMovie(1, "With", 2000))
	bare, _ := s.UpsertItem(ctx, movie("/m/bare.mkv", "Bare"), nil)
	withSidecar, _ := s.UpsertItem(ctx, movie("/m/side.mkv", "Side"), nil)
	if err := s.SetItemArt(ctx, withSidecar, Art{Poster: "/m/poster.jpg"}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{withTMDB, bare, withSidecar} {
		if err := s.SetItemEmbeddedCover(ctx, id, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetItemEmbeddedCover(ctx, 9999, 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown item = %v", err)
	}
	if got, _ := s.LocalArt(ctx, ArtItem, bare, "poster"); got != "embedded:3" {
		t.Errorf("bare poster = %q", got)
	}
	if got, _ := s.LocalArt(ctx, ArtItem, withSidecar, "poster"); got != "/m/poster.jpg" {
		t.Errorf("a sidecar poster was replaced by the embedded cover: %q", got)
	}
	if n, ok := ParseEmbeddedRef("embedded:3"); !ok || n != 3 {
		t.Errorf("ParseEmbeddedRef = %d, %v", n, ok)
	}
	for _, bad := range []string{"", "embedded:", "embedded:x", "embedded:-1", "/m/embedded:3"} {
		if _, ok := ParseEmbeddedRef(bad); ok {
			t.Errorf("ParseEmbeddedRef(%q) accepted", bad)
		}
	}

	// The scanner reports sidecars only: "no poster" from it keeps the cover,
	// and a poster from it replaces the cover.
	if err := s.SetItemArt(ctx, bare, Art{Backdrop: "/m/fanart.jpg"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LocalArt(ctx, ArtItem, bare, "poster"); got != "embedded:3" {
		t.Errorf("SetItemArt without a poster dropped the embedded cover: %q", got)
	}
	if got, _ := s.LocalArt(ctx, ArtItem, bare, "backdrop"); got != "/m/fanart.jpg" {
		t.Errorf("backdrop = %q", got)
	}
	if err := s.SetItemArt(ctx, bare, Art{Poster: "/m/poster.png"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LocalArt(ctx, ArtItem, bare, "poster"); got != "/m/poster.png" {
		t.Errorf("a sidecar poster did not replace the cover: %q", got)
	}

	// TMDB's poster outranks the embedded cover on cards; with none, the cover shows.
	cards, _ := s.TitleCards(ctx, []int64{titleID})
	if c := cards[titleID]; c.Poster != "tmdb:/p1.jpg" {
		t.Errorf("card poster = %q; TMDB beats an embedded cover", c.Poster)
	}
	noPoster := tmdbMovie(1, "With", 2000)
	noPoster.Poster = ""
	if _, err := s.db.Exec(`UPDATE titles SET poster = '' WHERE id = ?`, titleID); err != nil {
		t.Fatal(err)
	}
	cards, _ = s.TitleCards(ctx, []int64{titleID})
	if c := cards[titleID]; c.Poster != LocalRef(ArtItem, withTMDB, "poster") {
		t.Errorf("card poster = %q; the embedded cover shows when TMDB has no poster", c.Poster)
	}
}

func TestAiringTitlesDue(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	airing := tmdbSeries(1, "Airing", 2020)
	airing.NextAirDate, airing.FetchedAt = "2026-11-01", now.Add(-48*time.Hour).Unix() // aired, fetched before
	addShow(t, s, "Airing", []int{1}, airing)
	stale := tmdbSeries(2, "Stale but quiet", 2020) // old data, no air date: only the age rule wants it
	stale.FetchedAt = now.Add(-20 * 24 * time.Hour).Unix()
	addShow(t, s, "Stale", []int{1}, stale)

	got, err := s.AiringTitlesDue(ctx, now, 10)
	if err != nil || len(got) != 1 || got[0].Title != "Airing" {
		t.Errorf("AiringTitlesDue = %+v, %v; want only the show whose episode aired", got, err)
	}
	// Positive control: the age rule alone does list the stale show.
	all, _ := s.TitlesDueForRefresh(ctx, now, 10)
	if len(all) != 2 {
		t.Errorf("TitlesDueForRefresh = %d titles, want both", len(all))
	}
}

func TestSetMatchIfPending(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/m/a.mkv", "A"), nil)
	s.UpsertItem(ctx, episode("/m/S/S01E01.mkv", "S", 1, 1), nil)
	title, _ := s.UpsertTitle(ctx, tmdbMovie(1, "A", 2000))

	// Pending: written.
	if ok, err := s.SetItemMatchIfPending(ctx, id, Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil || !ok {
		t.Fatalf("pending item: %v, %v", ok, err)
	}
	e, _ := s.GetEntry(ctx, id)
	if e.MatchState != MatchMatched || e.TitleID != title {
		t.Errorf("item = %+v", e.Item)
	}
	// Not pending any more (a person fixed it, say): left alone.
	if err := s.SetItemMatch(ctx, id, Match{State: MatchManual}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetItemMatchIfPending(ctx, id, Match{TitleID: title, State: MatchMatched}); err != nil || ok {
		t.Errorf("manual item: %v, %v; want it left alone", ok, err)
	}
	if e, _ = s.GetEntry(ctx, id); e.MatchState != MatchManual || e.TitleID != 0 {
		t.Errorf("a manual match was overwritten: %+v", e.Item)
	}
	if _, err := s.SetItemMatchIfPending(ctx, 9999, Match{State: MatchMatched}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown item = %v", err)
	}
	if _, err := s.SetItemMatchIfPending(ctx, id, Match{State: "maybe"}); err == nil {
		t.Error("accepted a bad state")
	}

	// A show with no row is pending.
	if ok, err := s.SetSeriesMatchIfPending(ctx, "S", Match{State: MatchUnmatched, Score: 0.2}); err != nil || !ok {
		t.Fatalf("show without a row: %v, %v", ok, err)
	}
	if sm, _ := s.GetSeriesMatch(ctx, "S"); sm.State != MatchUnmatched {
		t.Errorf("series = %+v", sm)
	}
	if ok, err := s.SetSeriesMatchIfPending(ctx, "S", Match{State: MatchMatched}); err != nil || ok {
		t.Errorf("an unmatched show is no longer pending: %v, %v", ok, err)
	}
	if _, err := s.SetSeriesMatchIfPending(ctx, "Nope", Match{State: MatchMatched}); !errors.Is(err, ErrNotFound) {
		t.Errorf("show without episodes = %v", err)
	}
}
