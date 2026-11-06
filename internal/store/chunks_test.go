package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

// A chunk of ids is one query with an id for each placeholder: it must stay under SQLite's limit
// on variables (32,766 in the build modernc.org/sqlite carries), or a big library's list fails
// outright. (Other tests make briefChunk small and put it back; this reads the value they restore.)
func TestBriefChunkFitsInASQLiteQuery(t *testing.T) {
	const sqliteMaxVariables = 32766
	if briefChunk < 1 || briefChunk >= sqliteMaxVariables {
		t.Errorf("briefChunk = %d, want 1..%d", briefChunk, sqliteMaxVariables-1)
	}
	// And the limit is what it is believed to be: a query of that many variables runs, one with one more does not.
	s, _ := openTemp(t)
	for _, n := range []int{sqliteMaxVariables, sqliteMaxVariables + 1} {
		ids := make([]int64, n)
		var one int
		err := s.ro.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM items WHERE id IN (`+placeholders(n)+`)`, idArgs(ids)...).Scan(&one)
		if (n <= sqliteMaxVariables) != (err == nil) {
			t.Errorf("a query of %d variables: err = %v (the limit this test assumes is %d)", n, err, sqliteMaxVariables)
		}
	}
}

// The lists read their titles' ratings, watch state, genres and files in chunks of briefChunk
// ids. With the chunk made small, every kind of read crosses several boundaries: nothing may
// be lost or doubled at one.
func TestListsReadTitlesAcrossChunks(t *testing.T) {
	old := briefChunk
	briefChunk = 3
	t.Cleanup(func() { briefChunk = old })
	s, _ := openTemp(t)
	ctx := context.Background()

	const movies, shows = 8, 5
	var ids []int64
	watched, mdb := map[int64]bool{}, map[int64]bool{}
	for i := range movies {
		tid, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, TMDBID: int64(100 + i), Title: fmt.Sprintf("Film %d", i), Year: 2000 + i,
			Rating: 7, VoteCount: 10, Source: SourceTMDB}, Genres: []Genre{{ID: 28, Name: "Action"}, {ID: 18, Name: "Drama"}}})
		if err != nil {
			t.Fatal(err)
		}
		item, err := s.UpsertItem(ctx, &Item{Kind: KindMovie, Title: fmt.Sprintf("Film %d", i), Path: fmt.Sprintf("/m/%d.mkv", i), Root: "/m"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetItemMatch(ctx, item, Match{TitleID: tid, State: MatchMatched}); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if _, err := s.SetItemWatched(ctx, item, true); err != nil {
				t.Fatal(err)
			}
			watched[tid] = true
		}
		if i%2 == 0 {
			if err := s.ReplaceMDBListRatings(ctx, tid, []Rating{{Source: MDBListSources[0], Value: 80, Scale: 100}}, 1); err != nil {
				t.Fatal(err)
			}
			mdb[tid] = true
		}
		ids = append(ids, tid)
	}
	series := map[int64]EpisodeCounts{}
	for i := range shows {
		name := fmt.Sprintf("Show %d", i)
		tid, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindSeries, TMDBID: int64(900 + i), Title: name, Source: SourceTMDB}})
		if err != nil {
			t.Fatal(err)
		}
		var first int64
		for e := 1; e <= 2; e++ {
			id, err := s.UpsertItem(ctx, &Item{Kind: KindEpisode, Show: name, Season: 1, Episode: e, Path: fmt.Sprintf("/t/%s/%d.mkv", name, e), Root: "/t"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if e == 1 {
				first = id
			}
		}
		if err := s.SetSeriesMatch(ctx, name, Match{TitleID: tid, State: MatchMatched}); err != nil {
			t.Fatal(err)
		}
		c := EpisodeCounts{EpisodeCount: 2}
		if i%2 == 0 {
			if _, err := s.SetItemWatched(ctx, first, true); err != nil {
				t.Fatal(err)
			}
			c.Watched = 1
		}
		series[tid] = c
		ids = append(ids, tid)
	}

	briefs, err := s.TitleBriefs(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(briefs) != movies+shows {
		t.Fatalf("TitleBriefs = %d titles, want %d", len(briefs), movies+shows)
	}
	for _, id := range ids[:movies] {
		if b := briefs[id]; !slices.Equal(b.Genres, []string{"Action", "Drama"}) {
			t.Errorf("title %d: genres %v", id, b.Genres)
		}
	}
	films, err := s.FilmWatches(ctx, ids[:movies])
	if err != nil || len(films) != len(watched) {
		t.Errorf("FilmWatches = %d films, %v; want %d", len(films), err, len(watched))
	}

	cards, err := s.Titles(ctx, TitleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != movies+shows {
		t.Fatalf("Titles = %d cards, want %d", len(cards), movies+shows)
	}
	for _, c := range cards {
		if c.Kind == KindMovie {
			if c.Watched != watched[c.ID] {
				t.Errorf("film %q: watched %v, want %v", c.Title, c.Watched, watched[c.ID])
			}
			if want := 1 + btoi(mdb[c.ID]); len(c.Ratings) != want {
				t.Errorf("film %q: %d ratings, want %d", c.Title, len(c.Ratings), want)
			}
			continue
		}
		if c.Episodes == nil || *c.Episodes != series[c.ID] {
			t.Errorf("series %q: episodes %+v, want %+v", c.Title, c.Episodes, series[c.ID])
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
