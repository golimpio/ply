package metadatatest

import (
	"slices"
	"testing"

	"ply/internal/metadata"
	"ply/internal/store"
)

func TestFakeMatchesLikeTheRealThing(t *testing.T) {
	f := &Fake{
		MovieSearch: map[string][]metadata.Candidate{
			"the matrix": {{ID: 603, Title: "The Matrix", Year: 1999, Popularity: 80}},
		},
		Movies: map[int64]*store.TitleInput{603: {Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix"}}},
		Shows: map[int64]*store.TitleInput{1396: {Title: store.Title{Kind: store.KindSeries, TMDBID: 1396},
			Seasons: []store.Season{{Number: 1}, {Number: 2}, {Number: 3}}}},
	}
	res, err := metadata.Match(t.Context(), f, metadata.Query{Kind: store.KindMovie, Title: "The Matrix", Year: 1999})
	if err != nil || res.ID != 603 || res.State != store.MatchMatched {
		t.Fatalf("Match = %+v, %v", res, err)
	}
	if want := []string{`SearchMovies("The Matrix", 1999)`}; !slices.Equal(f.Calls(), want) {
		t.Errorf("calls = %q, want %q", f.Calls(), want)
	}

	if in, err := f.Movie(t.Context(), 603); err != nil || in.Title.Title != "The Matrix" {
		t.Errorf("Movie = %+v, %v", in, err)
	}
	if _, err := f.Movie(t.Context(), 1); !metadata.IsNotFound(err) || metadata.IsTransient(err) {
		t.Errorf("unknown movie: %v", err)
	}
	show, err := f.Series(t.Context(), 1396, "", []int{2, 3, 9})
	if err != nil || len(show.Seasons) != 2 || show.Seasons[0].Number != 2 {
		t.Errorf("Series = %+v, %v", show, err)
	}
	if len(f.Shows[1396].Seasons) != 3 {
		t.Error("Series changed the fake's own data")
	}

	f.Offline()
	if _, err := f.Movie(t.Context(), 603); !metadata.IsTransient(err) {
		t.Errorf("offline: %v", err)
	}
	if res, err := metadata.Match(t.Context(), f, metadata.Query{Kind: store.KindMovie, Title: "The Matrix"}); err == nil || res.State == store.MatchUnmatched {
		t.Errorf("offline Match = %+v, %v; it must fail, not unmatch", res, err)
	}
	f.Online()
	if _, err := f.Movie(t.Context(), 603); err != nil {
		t.Errorf("back online: %v", err)
	}
	if f.Count("Movie(") != 4 || f.Count("SearchMovies") != 2 {
		t.Errorf("counts: %q", f.Calls())
	}
	if got := f.ImageURL("w342", "/a.jpg"); got != "/w342/a.jpg" {
		t.Errorf("ImageURL = %q", got)
	}
}
