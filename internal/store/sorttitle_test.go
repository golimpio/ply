package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// asciiLower is SQLite's LOWER(): ASCII letters only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// The SortTitle of a card is the key Titles orders by, so a client that sorts by it (ASCII
// case-insensitively, then year, then id) gets the order the server gave.
func TestTitleCardSortTitleIsTheKeyTitlesIsOrderedBy(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	want := map[int64]string{} // title id -> the SortTitle it must have
	for i, c := range []struct{ title, sort string }{
		{"The Matrix", "Matrix, The"},
		{"A Quiet Place", "Quiet Place, A"},
		{"An Education", "Education, An"},
		{"Amélie", "Amélie"},
		{"Zodiac", ""},       // no sort title: listed under its title
		{"alien", ""},        // and without regard to case
		{"Theatre", ""},      // not an article
		{"The Matrix", "M2"}, // a sort title a person wrote; same title, same year
		{"Le Fabuleux Destin d'Amélie Poulain", ""},
	} {
		tid, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, TMDBID: int64(100 + i), Title: c.title, SortTitle: c.sort, Year: 1999,
			Source: SourceTMDB}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.UpsertItem(ctx, &Item{Kind: KindMovie, Title: c.title, Path: fmt.Sprintf("/m/%d.mkv", i), Root: "/m"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetItemMatch(ctx, id, Match{TitleID: tid, State: MatchMatched}); err != nil {
			t.Fatal(err)
		}
		want[tid] = c.sort
		if c.sort == "" {
			want[tid] = c.title
		}
	}

	cards, err := s.Titles(ctx, TitleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != len(want) {
		t.Fatalf("%d titles, want %d", len(cards), len(want))
	}
	for _, c := range cards {
		if c.SortTitle != want[c.ID] {
			t.Errorf("card %q has SortTitle %q, want %q", c.Title, c.SortTitle, want[c.ID])
		}
	}

	// The order the server gave is the order of the key it carries.
	sorted := slices.Clone(cards)
	slices.SortStableFunc(sorted, func(a, b TitleCard) int {
		if c := strings.Compare(asciiLower(a.SortTitle), asciiLower(b.SortTitle)); c != 0 {
			return c
		}
		if a.Year != b.Year {
			return a.Year - b.Year
		}
		return int(a.ID - b.ID)
	})
	for i := range cards {
		if cards[i].ID != sorted[i].ID {
			t.Fatalf("Titles is not in the order of SortTitle at %d: %q (%q) where the key says %q (%q)",
				i, cards[i].Title, cards[i].SortTitle, sorted[i].Title, sorted[i].SortTitle)
		}
	}
	// The control: the order is not the order of the plain titles, or this proves nothing.
	byTitle := slices.Clone(cards)
	slices.SortStableFunc(byTitle, func(a, b TitleCard) int { return strings.Compare(asciiLower(a.Title), asciiLower(b.Title)) })
	if slices.EqualFunc(cards, byTitle, func(a, b TitleCard) bool { return a.ID == b.ID }) {
		t.Error("control: sorting by title gives the same order as sorting by sort title; the articles are not tested")
	}
}
