package api

import (
	"context"
	"fmt"
	"testing"

	"ply/internal/store"
)

// GET /api/people answers with at most 20 people, however many match.
func TestPeopleSearchIsLimitedToTwenty(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	ctx := context.Background()
	in := &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 7001, Title: "Crowded", Year: 2020, Source: store.SourceTMDB}}
	for i := 0; i < 25; i++ {
		in.Credits = append(in.Credits, store.Credit{Person: store.Person{TMDBID: int64(9000 + i), Name: fmt.Sprintf("Extra %02d", i)},
			Kind: store.CreditCast, Order: i})
	}
	id, err := m.store.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetItemMatch(ctx, m.multi, store.Match{TitleID: id, State: store.MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}

	// Positive control: 25 people match, and a narrower query finds the ones it should.
	if got := decode[[]PersonCard](t, m.get("/api/people?q=Extra+0")); len(got) != 10 {
		t.Fatalf("q=Extra 0 found %d people, want 10", len(got))
	}
	got := decode[[]PersonCard](t, m.get("/api/people?q=Extra"))
	if len(got) != peopleLimit || peopleLimit != 20 {
		t.Errorf("q=Extra found %d people (limit %d), want exactly 20", len(got), peopleLimit)
	}
	seen := map[int64]bool{}
	for _, p := range got {
		if seen[p.ID] {
			t.Errorf("person %d is listed twice", p.ID)
		}
		seen[p.ID] = true
	}
}
