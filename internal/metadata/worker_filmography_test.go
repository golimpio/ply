package metadata_test

import (
	"context"
	"errors"
	"testing"

	"ply/internal/metadata"
)

func TestWorkerFilmographyLookupsOff(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	d, err := e.st.GetTitleDetail(e.ctx, e.matrixTitle())
	if err != nil || len(d.Cast) == 0 {
		t.Fatalf("setup matrix: %v", err)
	}
	pid := d.Cast[0].PersonID

	e.src.Set(e.fake, false) // Turn lookups off!
	got, err := e.w.Filmography(context.Background(), pid)
	if !errors.Is(err, metadata.ErrLookupsOff) || got != nil {
		t.Fatalf("Filmography with lookups off err = %v, got = %v; want ErrLookupsOff and nil", err, got)
	}
}

func TestWorkerFilmographyRefFreshness(t *testing.T) {
	e := newWEnv(t)
	e.withMatrix()
	e.addMovie("Movies/The Matrix (1999).mkv", "The Matrix", 1999)
	e.once()
	d, err := e.st.GetTitleDetail(e.ctx, e.matrixTitle())
	if err != nil || len(d.Cast) == 0 {
		t.Fatalf("setup matrix: %v", err)
	}
	pid := d.Cast[0].PersonID
	p, err := e.st.GetPerson(e.ctx, pid)
	if err != nil || p.TMDBID == 0 {
		t.Fatalf("get person: %v", err)
	}
	tmdbID := p.TMDBID

	e.fake.Filmography = map[int64]*metadata.PersonCombinedCredits{
		tmdbID: {
			Cast: []metadata.PersonCreditEntry{
				{ID: 101, MediaType: "movie", Title: "Speed", Character: "Jack Traven", Popularity: 50},
			},
		},
	}

	// First fetch: hits provider
	res1, err := e.w.Filmography(e.ctx, pid)
	if err != nil {
		t.Fatalf("Filmography 1: %v", err)
	}
	if len(res1) != 1 || e.fake.Count("CombinedCredits(") != 1 {
		t.Fatalf("res1 = %v, reqs = %d", res1, e.fake.Count("CombinedCredits("))
	}

	// Second fetch with same ref and fresh TTL: returns cached, does NOT hit provider again
	res2, err := e.w.Filmography(e.ctx, pid)
	if err != nil {
		t.Fatalf("Filmography 2: %v", err)
	}
	if len(res2) != 1 || e.fake.Count("CombinedCredits(") != 1 {
		t.Fatalf("res2 = %v, reqs = %d (expected 1 from cache)", res2, e.fake.Count("CombinedCredits("))
	}

	// Change language in resolver: ref changes from tmdb:6384@en-US to tmdb:6384@fr-FR
	e.src.SetLanguage("fr-FR")
	res3, err := e.w.Filmography(e.ctx, pid)
	if err != nil {
		t.Fatalf("Filmography 3: %v", err)
	}
	if len(res3) != 1 || e.fake.Count("CombinedCredits(") != 2 {
		t.Fatalf("res3 = %v, reqs = %d (expected 2 due to ref change)", res3, e.fake.Count("CombinedCredits("))
	}
}
