package store

import (
	"context"
	"slices"
	"testing"
)

// A refresh keeps the artwork the library shows: the provider's current picture
// fills an empty slot, but never swaps one that is set (docs/M6-METADATA.md).
func TestRefreshPinsTitleArtwork(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, err := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the first fetch stored its art.
	got, err := s.GetTitle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Poster != "tmdb:/p603.jpg" || got.Backdrop != "tmdb:/b603.jpg" || got.Logo != "tmdb:/l603.png" {
		t.Fatalf("first fetch art = %q %q %q", got.Poster, got.Backdrop, got.Logo)
	}
	// TMDB later returns different art; a refresh must keep the stored one.
	next := tmdbMovie(603, "The Matrix", 1999)
	next.Poster, next.Backdrop, next.Logo = "tmdb:/new.jpg", "tmdb:/newb.jpg", "tmdb:/newl.png"
	if _, err := s.UpsertTitle(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTitle(ctx, id)
	if got.Poster != "tmdb:/p603.jpg" || got.Backdrop != "tmdb:/b603.jpg" || got.Logo != "tmdb:/l603.png" {
		t.Errorf("after refresh art = %q %q %q, want the pinned first ones", got.Poster, got.Backdrop, got.Logo)
	}
}

func TestRefreshFillsEmptyArtwork(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tmdbMovie(1, "A", 2000)
	in.Poster, in.Backdrop, in.Logo = "", "", ""
	id, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTitle(ctx, tmdbMovie(1, "A", 2000)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTitle(ctx, id)
	if got.Poster != "tmdb:/p1.jpg" || got.Backdrop != "tmdb:/b1.jpg" || got.Logo != "tmdb:/l1.png" {
		t.Errorf("empty art not filled: %q %q %q", got.Poster, got.Backdrop, got.Logo)
	}
}

func TestRefreshPinsSeasonPosterAndEpisodeStill(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tmdbSeries(1396, "Breaking Bad", 2008)
	in.Seasons = []Season{{Number: 1, Poster: "tmdb:/s1.jpg", Episodes: []Episode{{Season: 1, Number: 1, Still: "tmdb:/e1.jpg"}}}}
	id, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the first fetch stored the season poster and the still.
	se, err := s.Seasons(ctx, id)
	if err != nil || len(se) != 1 || se[0].Poster != "tmdb:/s1.jpg" {
		t.Fatalf("season = %+v, %v", se, err)
	}
	eps, _ := s.Episodes(ctx, id)
	if len(eps) != 1 || eps[0].Still != "tmdb:/e1.jpg" {
		t.Fatalf("episode = %+v", eps)
	}
	// A refresh with different art keeps both.
	next := tmdbSeries(1396, "Breaking Bad", 2008)
	next.Seasons = []Season{{Number: 1, Poster: "tmdb:/s1new.jpg", Episodes: []Episode{{Season: 1, Number: 1, Still: "tmdb:/e1new.jpg"}}}}
	if _, err := s.UpsertTitle(ctx, next); err != nil {
		t.Fatal(err)
	}
	se, _ = s.Seasons(ctx, id)
	if se[0].Poster != "tmdb:/s1.jpg" {
		t.Errorf("season poster swapped on refresh: %q", se[0].Poster)
	}
	eps, _ = s.Episodes(ctx, id)
	if eps[0].Still != "tmdb:/e1.jpg" {
		t.Errorf("episode still swapped on refresh: %q", eps[0].Still)
	}
	// A manual reset renumbers from the provider: it takes the fresh art.
	next.ResetSeasons = true
	if _, err := s.UpsertTitle(ctx, next); err != nil {
		t.Fatal(err)
	}
	se, _ = s.Seasons(ctx, id)
	if se[0].Poster != "tmdb:/s1new.jpg" {
		t.Errorf("reset season poster = %q, want the provider's new one", se[0].Poster)
	}
	eps, _ = s.Episodes(ctx, id)
	if eps[0].Still != "tmdb:/e1new.jpg" {
		t.Errorf("reset still = %q, want the provider's new one", eps[0].Still)
	}
}

func TestSeasonLocalArt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tmdbSeries(1, "Show", 2010)
	in.Seasons = []Season{{Number: 1, Poster: "tmdb:/s1.jpg"}}
	id, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LocalArt(ctx, ArtSeason, id, "1"); err != ErrNotFound {
		t.Fatalf("no season art yet: %v", err)
	}
	if err := s.SetSeasonArt(ctx, id, 1, "/media/Show/season01-poster.jpg"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LocalArt(ctx, ArtSeason, id, "1")
	if err != nil || got != "/media/Show/season01-poster.jpg" {
		t.Fatalf("season art = %q, %v", got, err)
	}
	// A refresh that rewrites the season keeps the local poster.
	if _, err := s.UpsertTitle(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LocalArt(ctx, ArtSeason, id, "1"); got != "/media/Show/season01-poster.jpg" {
		t.Errorf("refresh dropped the local season poster: %q", got)
	}
	if _, err := s.LocalArt(ctx, ArtSeason, id, "not-a-number"); err != ErrNotFound {
		t.Errorf("a bad season slot: %v", err)
	}
}

func TestLiveImageRefs(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	// titles: poster, backdrop, logo
	if _, err := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999)); err != nil {
		t.Fatal(err)
	}
	// seasons.poster, episodes.still, people.profile, collections.poster/backdrop
	show := tmdbSeries(1396, "Breaking Bad", 2008)
	show.Seasons = []Season{{Number: 1, Poster: "tmdb:/s1.jpg", Episodes: []Episode{{Season: 1, Number: 1, Still: "tmdb:/e1.jpg"}}}}
	show.Credits = []Credit{{Person: Person{TMDBID: 6384, Name: "Keanu", Profile: "tmdb:/prof.jpg"}, Kind: CreditCast, Character: "Neo"}}
	show.Collection = &Collection{TMDBID: 1, Name: "C", Poster: "tmdb:/cp.jpg", Backdrop: "tmdb:/cb.jpg"}
	if _, err := s.UpsertTitle(ctx, show); err != nil {
		t.Fatal(err)
	}
	// person_filmography.poster/backdrop (the cached external credits)
	if _, err := s.rw.Exec(`INSERT INTO people (id, tmdb_id, name) VALUES (900, 900, 'F')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO person_filmography (person_id, ord, kind, tmdb_id, title, poster, backdrop)
		VALUES (900, 0, 'movie', 901, 'X', 'tmdb:/fp.jpg', 'tmdb:/fb.jpg')`); err != nil {
		t.Fatal(err)
	}
	refs, err := s.LiveImageRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// One ref per UNION leg: dropping any leg from LiveImageRefs must fail this.
	for _, want := range []string{
		"tmdb:/p603.jpg", "tmdb:/b603.jpg", "tmdb:/l603.png", // titles
		"tmdb:/s1.jpg",   // seasons
		"tmdb:/e1.jpg",   // episodes
		"tmdb:/prof.jpg", // people
		"tmdb:/cp.jpg", "tmdb:/cb.jpg", // collections
		"tmdb:/fp.jpg", "tmdb:/fb.jpg", // person_filmography
	} {
		if !refs[want] {
			t.Errorf("live ref missing: %q", want)
		}
	}
	if refs["tmdb:/gone.jpg"] {
		t.Errorf("a ref no title uses is live")
	}
}

// Migration 020 adds the season's local poster column, keeping the stored seasons.
func TestMigration020SeasonArt(t *testing.T) {
	old, path := openAtVersion(t, 19)
	if cols := columns(t, old.rw, "seasons"); slices.Contains(cols, "local_poster") {
		t.Fatal("positive control failed: version 19 already has local_poster")
	}
	if _, err := old.rw.Exec(`INSERT INTO titles (id, kind, tmdb_id, title, source, created_at, updated_at)
		VALUES (1, 'series', 1, 'Show', 'tmdb', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.rw.Exec(`INSERT INTO seasons (title_id, season, name, poster) VALUES (1, 1, 'Season 1', 'tmdb:/s1.jpg')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if cols := columns(t, s.rw, "seasons"); !slices.Contains(cols, "local_poster") {
		t.Fatalf("after migration: no local_poster column (%v)", cols)
	}
	se, err := s.Seasons(context.Background(), 1)
	if err != nil || len(se) != 1 || se[0].Poster != "tmdb:/s1.jpg" || se[0].LocalPoster != "" {
		t.Fatalf("stored season lost or changed: %+v, %v", se, err)
	}
}