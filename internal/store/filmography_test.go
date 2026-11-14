package store

import (
	"context"
	"errors"
	"testing"
)

func TestPersonFilmographyRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// Create a person
	res, err := s.rw.Exec(`INSERT INTO people (name, tmdb_id) VALUES ('Bryan Cranston', 17419)`)
	if err != nil {
		t.Fatal(err)
	}
	personID, _ := res.LastInsertId()

	// Initial get: empty ref, 0 checkedAt, 0 entries
	got, err := s.GetPersonFilmography(ctx, personID)
	if err != nil {
		t.Fatalf("GetPersonFilmography: %v", err)
	}
	if got.Ref != "" || got.CheckedAt != 0 || len(got.Entries) != 0 {
		t.Errorf("initial filmography = %+v", got)
	}

	// Insert movie into library to test in_library detection
	tres, err := s.rw.Exec(`INSERT INTO titles (kind, tmdb_id, title, sort_title, created_at, updated_at)
		VALUES ('movie', 68718, 'Argo', 'Argo', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	titleID, _ := tres.LastInsertId()
	if _, err := s.rw.Exec(`INSERT INTO items (kind, path, root, size, mtime, added_at, updated_at, title_id)
		VALUES ('movie', '/movies/argo.mkv', '/movies', 100, 1, 1, 1, ?)`, titleID); err != nil {
		t.Fatal(err)
	}

	entries := []PersonFilmographyEntry{
		{
			Kind:         KindMovie,
			TMDBID:       68718, // In library!
			Title:        "Argo",
			Year:         2012,
			Character:    "Jack O'Donnell",
			Department:   "Acting",
			CreditKind:   CreditCast,
			Poster:       "tmdb:/argo.jpg",
			Rating:       7.5,
			Popularity:   50.2,
			EpisodeCount: 0,
		},
		{
			Kind:         KindMovie,
			TMDBID:       10001, // Not in library
			Title:        "Isle of Dogs",
			Year:         2018,
			Character:    "Chief",
			Department:   "Acting",
			CreditKind:   CreditCast,
			Poster:       "tmdb:/dogs.jpg",
			Rating:       7.9,
			Popularity:   40.1,
			EpisodeCount: 0,
		},
	}

	if err := s.SetPersonFilmography(ctx, personID, "tmdb:17419@en-US", entries, 1234567); err != nil {
		t.Fatalf("SetPersonFilmography: %v", err)
	}

	got, err = s.GetPersonFilmography(ctx, personID)
	if err != nil {
		t.Fatalf("GetPersonFilmography after set: %v", err)
	}
	if got.Ref != "tmdb:17419@en-US" || got.CheckedAt != 1234567 {
		t.Errorf("ref/checked = %q, %d", got.Ref, got.CheckedAt)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(got.Entries))
	}
	if !got.Entries[0].InLibrary {
		t.Errorf("Argo InLibrary = false, want true")
	}
	if got.Entries[1].InLibrary {
		t.Errorf("Isle of Dogs InLibrary = true, want false")
	}
	if got.Entries[0].Title != "Argo" || got.Entries[1].Title != "Isle of Dogs" {
		t.Errorf("ordering wrong: %+v", got.Entries)
	}

	// Positive control before deletion / cascade: assert rows exist in person_filmography
	var count int
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM person_filmography WHERE person_id = ?`, personID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("control: count before delete = %d, err = %v", count, err)
	}

	// Delete person: ON DELETE CASCADE must wipe person_filmography
	if _, err := s.rw.Exec(`DELETE FROM people WHERE id = ?`, personID); err != nil {
		t.Fatal(err)
	}
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM person_filmography WHERE person_id = ?`, personID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("after delete: count = %d, want 0", count)
	}

	// Non-existent person returns ErrNotFound
	if _, err := s.GetPersonFilmography(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPersonFilmography(99999) err = %v, want ErrNotFound", err)
	}
	if err := s.SetPersonFilmography(ctx, 99999, "ref", entries, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetPersonFilmography(99999) err = %v, want ErrNotFound", err)
	}
}
