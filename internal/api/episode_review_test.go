package api

import (
	"context"
	"slices"
	"testing"

	"ply/internal/store"
)

// An episode whose file name has no episode number (the scanner flags it) needs
// review, whatever its show's match says.
func TestAnEpisodeWithNoNumberNeedsReview(t *testing.T) {
	e := newEnv(t, "")
	ctx := context.Background()
	ids := map[string]int64{}
	for name, it := range map[string]*store.Item{
		"numbered": {Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 1, Path: "/lib/Lost/S01E01.mkv", Root: "/lib", Size: 1},
		"bare":     {Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 0, Path: "/lib/Lost/Extras.mkv", Root: "/lib", Size: 2, NeedsReview: true},
	} {
		id, err := e.store.UpsertItem(ctx, it, streamsH264())
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	flags := func() map[int64]bool {
		t.Helper()
		out := map[int64]bool{}
		for _, it := range decode[[]map[string]any](t, e.get("/api/items?kind=episode")) {
			out[int64(it["id"].(float64))] = it["needsReview"].(bool)
		}
		return out
	}
	got := flags()
	if got[ids["numbered"]] || !got[ids["bare"]] {
		t.Errorf("needsReview = %v, want only the episode with no number", got)
	}

	// A show whose match is settled still has an episode to look at.
	title, err := e.store.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 4607, Title: "Lost"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: title, State: store.MatchMatched, Score: 0.95}); err != nil {
		t.Fatal(err)
	}
	got = flags()
	if got[ids["numbered"]] || !got[ids["bare"]] {
		t.Errorf("needsReview with the show matched = %v, want only the episode with no number", got)
	}
	// And a show the matcher is unsure of flags all its episodes, as it did.
	if err := e.store.SetSeriesMatch(ctx, "Lost", store.Match{TitleID: title, State: store.MatchMatched, NeedsReview: true, Score: 0.6}); err != nil {
		t.Fatal(err)
	}
	if got = flags(); !got[ids["numbered"]] || !got[ids["bare"]] {
		t.Errorf("needsReview with the show in doubt = %v, want both", got)
	}
}

// The review list says which episodes of a show are flagged (id and file), so that
// whoever opens the entry sees what to look at.
func TestTheReviewListNamesTheFlaggedEpisodes(t *testing.T) {
	e := newEnv(t, "")
	ctx := context.Background()
	ids := map[string]int64{}
	for name, it := range map[string]*store.Item{
		"numbered": {Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 1, Path: "/lib/Lost/S01E01.mkv", Root: "/lib", Size: 1},
		"bare":     {Kind: store.KindEpisode, Show: "Lost", Season: 1, Episode: 0, Path: "/lib/Lost/Pilot.mkv", Root: "/lib", Size: 2, NeedsReview: true},
		"other":    {Kind: store.KindEpisode, Show: "Heat", Season: 1, Episode: 1, Path: "/lib/Heat/S01E01.mkv", Root: "/lib", Size: 3},
	} {
		id, err := e.store.UpsertItem(ctx, it, streamsH264())
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	review := decode[[]ReviewEntry](t, e.get("/api/metadata/review"))
	var lost *ReviewEntry
	for i := range review {
		if review[i].Show == "Heat" {
			t.Errorf("Heat has no flagged episode and is in the review list: %+v", review[i])
		}
		if review[i].Show == "Lost" {
			lost = &review[i]
		}
	}
	if lost == nil || !lost.NeedsReview {
		t.Fatalf("review = %+v; want Lost for review", review)
	}
	if want := []ReviewEpisode{{ID: ids["bare"], Path: "/lib/Lost/Pilot.mkv"}}; !slices.Equal(lost.Episodes, want) {
		t.Errorf("episodes = %+v, want %+v", lost.Episodes, want)
	}
}
