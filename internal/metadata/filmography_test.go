package metadata

import (
	"testing"

	"ply/internal/store"
)

func TestNormalizeDepartment(t *testing.T) {
	cases := []struct {
		dept       string
		creditKind string
		want       string
	}{
		{"", store.CreditCast, "Acting"},
		{"Acting", store.CreditCast, "Acting"},
		{"Directing", store.CreditCrew, "Directing"},
		{"directing", store.CreditCrew, "Directing"},
		{"Writing", store.CreditCrew, "Writing"},
		{"Production", store.CreditCrew, "Production"},
		{"Producer", store.CreditCrew, "Production"},
		{"Camera", store.CreditCrew, "Crew"},
		{"Sound", store.CreditCrew, "Crew"},
		{"Editing", store.CreditCrew, "Crew"},
		{"Art", store.CreditCrew, "Crew"},
		{"Costume & Make-Up", store.CreditCrew, "Crew"},
		{"Visual Effects", store.CreditCrew, "Crew"},
		{"Lighting", store.CreditCrew, "Crew"},
		{"Crew", store.CreditCrew, "Crew"},
		{"Unusual Department 123", store.CreditCrew, "Other"},
	}
	for _, tc := range cases {
		if got := normalizeDepartment(tc.dept, tc.creditKind); got != tc.want {
			t.Errorf("normalizeDepartment(%q, %q) = %q, want %q", tc.dept, tc.creditKind, got, tc.want)
		}
	}
}

func TestRankFilmography(t *testing.T) {
	// Actor with both real character and talk show cameo ("Self")
	comb := &PersonCombinedCredits{
		Cast: []PersonCreditEntry{
			{ID: 1, MediaType: "movie", Title: "Top Movie", Character: "Hero", Popularity: 100, VoteAverage: 8.0, ReleaseDate: "2020-01-01"},
			{ID: 2, MediaType: "tv", Name: "Talk Show", Character: "Self - Guest", Popularity: 90, VoteAverage: 5.0, FirstAirDate: "2021-01-01"},
			{ID: 3, MediaType: "movie", Title: "Indie Film", Character: "Villain", Popularity: 40, VoteAverage: 7.0, ReleaseDate: "2018-01-01"},
		},
		Crew: []PersonCreditEntry{
			{ID: 4, MediaType: "movie", Title: "Directed Movie", Department: "Directing", Job: "Director", Popularity: 60, VoteAverage: 7.5, ReleaseDate: "2019-01-01"},
		},
	}

	ranked := rankFilmography(comb)
	if len(ranked) != 3 {
		t.Fatalf("len(ranked) = %d, want 3 (talk show filtered out)", len(ranked))
	}
	// Popularity order: 100, 60, 40
	if ranked[0].Title != "Top Movie" || ranked[1].Title != "Directed Movie" || ranked[2].Title != "Indie Film" {
		t.Errorf("wrong order: %+v", ranked)
	}

	// Documentary subject with ONLY "Self" role: must NOT be filtered out!
	docHost := &PersonCombinedCredits{
		Cast: []PersonCreditEntry{
			{ID: 10, MediaType: "tv", Name: "Planet Earth", Character: "Self - Host", Popularity: 80, ReleaseDate: "2006-01-01"},
		},
	}
	docRanked := rankFilmography(docHost)
	if len(docRanked) != 1 || docRanked[0].Title != "Planet Earth" {
		t.Errorf("doc host was filtered out: %+v", docRanked)
	}
}
