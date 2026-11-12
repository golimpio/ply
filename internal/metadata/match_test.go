package metadata

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"ply/internal/store"
)

func movieQ(title string, year int) Query {
	return Query{Kind: store.KindMovie, Title: title, Year: year}
}
func showQ(title string, year int) Query {
	return Query{Kind: store.KindSeries, Title: title, Year: year}
}

// TestMatchFixtures runs the matcher over recorded TMDB search responses, end
// to end through the client and the fake server.
func TestMatchFixtures(t *testing.T) {
	type search struct{ path, params, file string }
	mv := func(query, year, file string) search {
		if year == "" {
			return search{"/search/movie", params("query", query), file}
		}
		return search{"/search/movie", params("query", query, "year", year), file}
	}
	for _, tc := range []struct {
		name       string
		q          Query
		routes     []search
		wantID     int64
		wantState  string
		wantReview bool
		wantScore  float64
		wantAsked  []string // the searches, in order; nil = not checked
	}{
		{name: "exact match", q: movieQ("The Matrix", 1999), routes: []search{mv("The Matrix", "1999", "search_movie_matrix.json")},
			wantID: 603, wantState: store.MatchMatched, wantScore: 1,
			wantAsked: []string{"/search/movie?query=The Matrix&year=1999"}},

		// The Thing: 2011 comes first in TMDB's list, 1982 is the more popular.
		{name: "remake 1982 by year", q: movieQ("The Thing", 1982), routes: []search{mv("The Thing", "1982", "search_movie_thing.json")},
			wantID: 1091, wantState: store.MatchMatched, wantScore: 1},
		{name: "remake 2011 by year", q: movieQ("The Thing", 2011), routes: []search{mv("The Thing", "2011", "search_movie_thing.json")},
			wantID: 60935, wantState: store.MatchMatched, wantScore: 0.966667},
		{name: "remake without a year goes by popularity", q: movieQ("The Thing", 0), routes: []search{mv("The Thing", "", "search_movie_thing.json")},
			wantID: 1091, wantState: store.MatchMatched, wantScore: 0.85},
		{name: "a year off by one is nearly as good", q: movieQ("The Thing", 1983), routes: []search{mv("The Thing", "1983", "search_movie_thing.json")},
			wantID: 1091, wantState: store.MatchMatched, wantScore: 0.94}, // 0.6 + 0.3*0.8 + 0.1

		// Titles that are numbers.
		{name: "1917 with year", q: movieQ("1917", 2019), routes: []search{mv("1917", "2019", "search_movie_1917.json")},
			wantID: 530915, wantState: store.MatchMatched, wantScore: 1},
		{name: "1917 without year", q: movieQ("1917", 0), routes: []search{mv("1917", "", "search_movie_1917.json")},
			wantID: 530915, wantState: store.MatchMatched, wantScore: 0.85},
		{name: "1917, the 1967 film by year", q: movieQ("1917", 1967), routes: []search{mv("1917", "1967", "search_movie_1917.json")},
			wantID: 337291, wantState: store.MatchMatched, wantScore: 0.9},
		{name: "2001 A Space Odyssey (colon folded away)", q: movieQ("2001 A Space Odyssey", 1968), routes: []search{mv("2001 A Space Odyssey", "1968", "search_movie_2001.json")},
			wantID: 62, wantState: store.MatchMatched, wantScore: 1},
		{name: "2001 A Space Odyssey without year", q: movieQ("2001 A Space Odyssey", 0), routes: []search{mv("2001 A Space Odyssey", "", "search_movie_2001.json")},
			wantID: 62, wantState: store.MatchMatched, wantScore: 0.85},

		// Accents.
		{name: "Amelie for Amélie", q: movieQ("Amelie", 2001), routes: []search{mv("Amelie", "2001", "search_movie_amelie.json")},
			wantID: 194, wantState: store.MatchMatched, wantScore: 1},
		{name: "Amélie for Amélie", q: movieQ("Amélie", 2001), routes: []search{mv("Amélie", "2001", "search_movie_amelie.json")},
			wantID: 194, wantState: store.MatchMatched, wantScore: 1},
		{name: "decomposed Amélie", q: movieQ("Ame\u0301lie", 2001), routes: []search{mv("Ame\u0301lie", "2001", "search_movie_amelie.json")},
			wantID: 194, wantState: store.MatchMatched, wantScore: 1},

		// Sequels.
		{name: "Blade Runner 2049", q: movieQ("Blade Runner 2049", 2017), routes: []search{mv("Blade Runner 2049", "2017", "search_movie_blade_runner_2049.json")},
			wantID: 335984, wantState: store.MatchMatched, wantScore: 1},
		{name: "Blade Runner is not its sequel", q: movieQ("Blade Runner", 1982), routes: []search{mv("Blade Runner", "1982", "search_movie_blade_runner.json")},
			wantID: 78, wantState: store.MatchMatched, wantScore: 0.95},
		{name: "Blade Runner without a year: exactly at the threshold", q: movieQ("Blade Runner", 0), routes: []search{mv("Blade Runner", "", "search_movie_blade_runner.json")},
			wantID: 78, wantState: store.MatchMatched, wantReview: false, wantScore: 0.8},

		// No year.
		{name: "no year, clear winner", q: movieQ("Inception", 0), routes: []search{mv("Inception", "", "search_movie_inception.json")},
			wantID: 27205, wantState: store.MatchMatched, wantScore: 0.85},
		{name: "no year, several films of that name", q: movieQ("Heat", 0), routes: []search{mv("Heat", "", "search_movie_heat.json")},
			wantID: 949, wantState: store.MatchMatched, wantScore: 0.85},

		// Original-language titles: TMDB's title is English.
		{name: "original title", q: movieQ("Cidade de Deus", 2002), routes: []search{mv("Cidade de Deus", "2002", "search_movie_cidade.json")},
			wantID: 598, wantState: store.MatchMatched, wantScore: 1},

		// Doubt.
		{name: "wrong year in the file name: found, flagged", q: movieQ("Dune", 1990), routes: []search{mv("Dune", "1990", "search_movie_dune.json")},
			wantID: 438631, wantState: store.MatchMatched, wantReview: true, wantScore: 0.65},
		{name: "wrong year, nothing for that year: retried without it", q: movieQ("The Matrix", 1990), routes: []search{mv("The Matrix", "", "search_movie_matrix.json")},
			wantID: 603, wantState: store.MatchMatched, wantReview: true, wantScore: 0.7,
			wantAsked: []string{"/search/movie?query=The Matrix&year=1990", "/search/movie?query=The Matrix"}},
		{name: "a poor match is unmatched", q: movieQ("Foo Bar Baz", 2010), routes: []search{mv("Foo Bar Baz", "2010", "search_movie_lowscore.json")},
			wantID: 0, wantState: store.MatchUnmatched, wantScore: 0.4},

		// No results, and the retries.
		{name: "no results, plain title: with and without the year", q: movieQ("Zzyzx Qwerty", 2020),
			wantState: store.MatchUnmatched, wantAsked: []string{"/search/movie?query=Zzyzx Qwerty&year=2020", "/search/movie?query=Zzyzx Qwerty"}},
		{name: "no results, article: also the simplified title", q: movieQ("The Zzyzx", 2020),
			wantState: store.MatchUnmatched, wantAsked: []string{
				"/search/movie?query=The Zzyzx&year=2020", "/search/movie?query=The Zzyzx",
				"/search/movie?query=zzyzx&year=2020", "/search/movie?query=zzyzx"}},
		{name: "no results, no year: one search, then the simplified", q: movieQ("The Zzyzx", 0),
			wantState: store.MatchUnmatched, wantAsked: []string{"/search/movie?query=The Zzyzx", "/search/movie?query=zzyzx"}},
		{name: "simplified title finds it", q: movieQ("The Grand Budapest Hotel", 2014), routes: []search{mv("grand budapest hotel", "2014", "search_movie_budapest.json")},
			wantID: 120467, wantState: store.MatchMatched, wantScore: 1,
			wantAsked: []string{"/search/movie?query=The Grand Budapest Hotel&year=2014", "/search/movie?query=The Grand Budapest Hotel", "/search/movie?query=grand budapest hotel&year=2014"}},
		{name: "ampersand and accents simplified", q: movieQ("Tom & Jérry", 0), routes: []search{mv("tom and jerry", "", "search_movie_matrix.json")},
			wantState: store.MatchUnmatched, wantScore: 0.25, // found something, but nothing like it
			wantAsked: []string{"/search/movie?query=Tom & Jérry", "/search/movie?query=tom and jerry"}},
		{name: "empty title", q: movieQ("  ", 1999), wantState: store.MatchUnmatched, wantAsked: []string{}},

		// Shows.
		{name: "show", q: showQ("Breaking Bad", 2008),
			routes: []search{{"/search/tv", params("query", "Breaking Bad", "first_air_date_year", "2008"), "search_tv_breaking_bad.json"}},
			wantID: 1396, wantState: store.MatchMatched, wantScore: 1,
			wantAsked: []string{"/search/tv?first_air_date_year=2008&query=Breaking Bad"}},
		{name: "show without year", q: showQ("Breaking Bad", 0),
			routes: []search{{"/search/tv", params("query", "Breaking Bad"), "search_tv_breaking_bad.json"}},
			wantID: 1396, wantState: store.MatchMatched, wantScore: 0.85},
	} {
		f := newFakeTMDB(t)
		for _, r := range tc.routes {
			f.on(r.path, r.params, r.file)
		}
		res, err := Match(t.Context(), newTMDB(t, f), tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if res.ID != tc.wantID || res.State != tc.wantState || res.NeedsReview != tc.wantReview ||
			math.Abs(res.Score-tc.wantScore) > 1e-6 || res.ByID {
			t.Errorf("%s: got id %d state %s review %v score %.6f byID %v; want id %d state %s review %v score %.6f",
				tc.name, res.ID, res.State, res.NeedsReview, res.Score, res.ByID, tc.wantID, tc.wantState, tc.wantReview, tc.wantScore)
		}
		if tc.wantAsked != nil && !slices.Equal(f.asked(), tc.wantAsked) && !(len(tc.wantAsked) == 0 && len(f.asked()) == 0) {
			t.Errorf("%s: searches\n got %q\nwant %q", tc.name, f.asked(), tc.wantAsked)
		}
		if res.ID != 0 && res.Candidates[0].ID != res.ID {
			t.Errorf("%s: best candidate %d is not the match %d", tc.name, res.Candidates[0].ID, res.ID)
		}
		if !slices.IsSortedFunc(res.Candidates, func(a, b Scored) int { return -compareFloat(a.Score, b.Score) }) {
			t.Errorf("%s: candidates not best first: %+v", tc.name, res.Candidates)
		}
	}
}

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestMatchByID(t *testing.T) {
	ctx := t.Context()
	find := func(f *fakeTMDB) {
		f.on("/find/tt0133093", params("external_source", "imdb_id"), "find_imdb_tt0133093.json")
		f.on("/find/tt0000001", params("external_source", "imdb_id"), "find_empty.json")
		f.on("/find/81189", params("external_source", "tvdb_id"), "find_tvdb_81189.json")
		f.on("/find/99999", params("external_source", "tvdb_id"), "find_empty.json")
	}
	for _, tc := range []struct {
		name      string
		q         Query
		wantID    int64
		wantByID  bool
		wantState string
		wantAsked []string
		routes    func(f *fakeTMDB)
	}{
		{name: "tmdb id", q: Query{Kind: store.KindMovie, Title: "whatever", Hints: Hints{TMDB: 603}}, wantID: 603, wantByID: true, wantState: store.MatchMatched, wantAsked: []string{}},
		{name: "tmdb id beats imdb id", q: Query{Kind: store.KindMovie, Hints: Hints{TMDB: 5, IMDb: "tt0133093"}}, wantID: 5, wantByID: true, wantState: store.MatchMatched, wantAsked: []string{}},
		{name: "imdb id for a movie", q: Query{Kind: store.KindMovie, Hints: Hints{IMDb: "tt0133093"}}, wantID: 603, wantByID: true, wantState: store.MatchMatched,
			wantAsked: []string{"/find/tt0133093?external_source=imdb_id"}},
		{name: "imdb id for a show that is a movie: search instead", q: Query{Kind: store.KindSeries, Title: "Breaking Bad", Year: 2008, Hints: Hints{IMDb: "tt0133093"}},
			wantID: 1396, wantState: store.MatchMatched,
			wantAsked: []string{"/find/tt0133093?external_source=imdb_id", "/search/tv?first_air_date_year=2008&query=Breaking Bad"},
			routes: func(f *fakeTMDB) {
				f.on("/search/tv", params("query", "Breaking Bad", "first_air_date_year", "2008"), "search_tv_breaking_bad.json")
			}},
		{name: "unknown imdb id: search instead", q: Query{Kind: store.KindMovie, Title: "The Matrix", Year: 1999, Hints: Hints{IMDb: "tt0000001"}},
			wantID: 603, wantState: store.MatchMatched,
			wantAsked: []string{"/find/tt0000001?external_source=imdb_id", "/search/movie?query=The Matrix&year=1999"},
			routes: func(f *fakeTMDB) {
				f.on("/search/movie", params("query", "The Matrix", "year", "1999"), "search_movie_matrix.json")
			}},
		{name: "tvdb id for a show", q: Query{Kind: store.KindSeries, Hints: Hints{TVDB: 81189}}, wantID: 1396, wantByID: true, wantState: store.MatchMatched,
			wantAsked: []string{"/find/81189?external_source=tvdb_id"}},
		{name: "imdb id tried before tvdb", q: Query{Kind: store.KindSeries, Hints: Hints{IMDb: "tt0000001", TVDB: 81189}}, wantID: 1396, wantByID: true, wantState: store.MatchMatched,
			wantAsked: []string{"/find/tt0000001?external_source=imdb_id", "/find/81189?external_source=tvdb_id"}},
		{name: "unknown tvdb id and no title", q: Query{Kind: store.KindSeries, Hints: Hints{TVDB: 99999}}, wantState: store.MatchUnmatched,
			wantAsked: []string{"/find/99999?external_source=tvdb_id"}},
		{name: "a tvdb id means nothing for a movie", q: Query{Kind: store.KindMovie, Hints: Hints{TVDB: 81189}}, wantState: store.MatchUnmatched, wantAsked: []string{}},
	} {
		f := newFakeTMDB(t)
		find(f)
		if tc.routes != nil {
			tc.routes(f)
		}
		res, err := Match(ctx, newTMDB(t, f), tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if res.ID != tc.wantID || res.ByID != tc.wantByID || res.State != tc.wantState {
			t.Errorf("%s: id %d byID %v state %s; want %d %v %s", tc.name, res.ID, res.ByID, res.State, tc.wantID, tc.wantByID, tc.wantState)
		}
		if res.ByID && (res.Score != 1 || res.NeedsReview) {
			t.Errorf("%s: an id gives full confidence, got score %v review %v", tc.name, res.Score, res.NeedsReview)
		}
		if got := f.asked(); !slices.Equal(got, tc.wantAsked) && !(len(got) == 0 && len(tc.wantAsked) == 0) {
			t.Errorf("%s: requests\n got %q\nwant %q", tc.name, got, tc.wantAsked)
		}
	}
}

func TestMatchNeverMakesTMDBTroubleAnUnmatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		transient bool
	}{
		{"server error", 500, true}, {"bad gateway", 502, true}, {"rate limited for long", 429, true}, {"bad key", 401, false},
	} {
		f := newFakeTMDB(t)
		headers := map[string]string{}
		if tc.status == 429 {
			headers["Retry-After"] = "9999"
		}
		f.fail("/search/movie", params("query", "The Matrix", "year", "1999"), tc.status, "", headers)
		res, err := Match(t.Context(), newTMDB(t, f), movieQ("The Matrix", 1999))
		if err == nil || IsTransient(err) != tc.transient {
			t.Errorf("%s: err = %v, transient %v; want an error, transient %v", tc.name, err, IsTransient(err), tc.transient)
		}
		if res.State == store.MatchUnmatched || res.ID != 0 {
			t.Errorf("%s: result %+v; a failure must not decide anything", tc.name, res)
		}
	}

	// Same for the lookup by id.
	f := newFakeTMDB(t)
	f.fail("/find/tt0133093", params("external_source", "imdb_id"), 503, "", nil)
	f.on("/search/movie", params("query", "The Matrix", "year", "1999"), "search_movie_matrix.json")
	q := movieQ("The Matrix", 1999)
	q.Hints.IMDb = "tt0133093"
	if res, err := Match(t.Context(), newTMDB(t, f), q); err == nil || !IsTransient(err) || res.ID != 0 {
		t.Errorf("a failing find fell back to the search: %+v, %v", res, err)
	}

	// The ids of a show: the TVDB and IMDb lookups failing is just as little a
	// reason to fall through to a search, let alone to unmatch.
	for _, tc := range []struct {
		name  string
		hints Hints
		path  string
		query string
	}{
		{"tvdb", Hints{TVDB: 81189}, "/find/81189", params("external_source", "tvdb_id")},
		{"imdb for a show", Hints{IMDb: "tt0903747"}, "/find/tt0903747", params("external_source", "imdb_id")},
	} {
		for _, status := range []int{503, 429} {
			f = newFakeTMDB(t)
			headers := map[string]string{}
			if status == 429 {
				headers["Retry-After"] = "9999"
			}
			f.fail(tc.path, tc.query, status, "", headers)
			f.on("/search/tv", params("query", "Breaking Bad"), "search_tv_breaking_bad.json")
			res, err := Match(t.Context(), newTMDB(t, f), Query{Kind: store.KindSeries, Title: "Breaking Bad", Hints: tc.hints})
			if err == nil || !IsTransient(err) || res.ID != 0 || res.State == store.MatchUnmatched || res.State == store.MatchMatched {
				t.Errorf("%s with %d: %+v, %v; want a transient error and no verdict", tc.name, status, res, err)
			}
			if f.count("/search/tv") != 0 {
				t.Errorf("%s with %d fell through to a search", tc.name, status)
			}
		}
	}

	// And a failure on a later search of the cascade.
	f = newFakeTMDB(t)
	f.fail("/search/movie", params("query", "The Matrix"), 500, "", nil)
	if _, err := Match(t.Context(), newTMDB(t, f), movieQ("The Matrix", 1990)); err == nil || !IsTransient(err) {
		t.Errorf("failure in the second search: %v", err)
	}
}

func TestTitleSimilarity(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want float64
	}{
		{"The Matrix", "The Matrix", 1},
		{"the matrix", "THE MATRIX", 1},
		{"Amélie", "Amelie", 1},
		{"Ame\u0301lie", "Am\u00e9lie", 1},
		{"2001 A Space Odyssey", "2001: A Space Odyssey", 1},
		{"Spider-Man: Homecoming", "Spider Man Homecoming", 1},
		{"Spiderman", "Spider-Man", 1},
		{"Tom & Jerry", "Tom and Jerry", 1},
		{"Don't Look Up", "Dont Look Up", 1},
		{"Don’t Look Up", "Don't Look Up", 1},
		{"Mission: Impossible - Fallout", "Mission Impossible Fallout", 1},
		{"千と千尋の神隠し", "千と千尋の神隠し", 1},
		{"Blade Runner", "Blade Runner 2049", 4.0 / 5},
		{"Blade Runner 2049", "Blade Runner", 4.0 / 5},
		{"The Thing", "The Thing from Another World", 4.0 / 7},
		{"Foo Bar Baz", "Foo", 0.5},
		{"Cidade de Deus", "City of God", 0},
		{"1917", "2019", 0},
		{"1917", "The Russian Revolution of 1917", 2.0 / 6},
		{"the the", "the", 2.0 / 3}, // a word counts once per appearance
		{"", "", 0},
		{"Matrix", "", 0},
		{"???", "!!!", 0},
		{"???", "Matrix", 0},
	} {
		if got := titleSimilarity(tc.a, tc.b); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("titleSimilarity(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if a, b := titleSimilarity(tc.a, tc.b), titleSimilarity(tc.b, tc.a); a != b {
			t.Errorf("titleSimilarity(%q, %q) is not symmetric: %v vs %v", tc.a, tc.b, a, b)
		}
	}
}

func TestYearScore(t *testing.T) {
	for _, tc := range []struct {
		query, cand int
		want        float64
	}{
		{1999, 1999, 1}, {1999, 2000, 0.8}, {1999, 1998, 0.8}, {1999, 2001, 0}, {1999, 1997, 0}, {1999, 2021, 0},
		{0, 1999, 0.5}, {0, 0, 0.5}, {1999, 0, 0}, {1900, 1899, 0.8},
	} {
		if got := yearScore(tc.query, tc.cand); got != tc.want {
			t.Errorf("yearScore(%d, %d) = %v, want %v", tc.query, tc.cand, got, tc.want)
		}
	}
}

func TestPopularityRank(t *testing.T) {
	pops := func(ps ...float64) []Candidate {
		out := make([]Candidate, len(ps))
		for i, p := range ps {
			out[i].Popularity = p
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		cands []Candidate
		want  []float64
	}{
		{"one", pops(5), []float64{1}},
		{"two", pops(5, 9), []float64{0, 1}},
		{"three, linear", pops(10, 30, 20), []float64{0, 1, 0.5}},
		{"five", pops(1, 2, 3, 4, 5), []float64{0, 0.25, 0.5, 0.75, 1}},
		{"ties share a rank", pops(7, 7, 3), []float64{1, 1, 0}},
		{"all alike", pops(0, 0, 0), []float64{1, 1, 1}},
		{"empty", nil, nil},
	} {
		var got []float64
		for i := range tc.cands {
			got = append(got, popularityRank(tc.cands, i))
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: ranks %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClassifyThresholds(t *testing.T) {
	for _, tc := range []struct {
		score  float64
		state  string
		review bool
	}{
		{1, store.MatchMatched, false},
		{0.9, store.MatchMatched, false},
		{0.8, store.MatchMatched, false},
		{0.799999, store.MatchMatched, true},
		{0.7, store.MatchMatched, true},
		{0.55, store.MatchMatched, true},
		{0.549999, store.MatchUnmatched, false},
		{0.4, store.MatchUnmatched, false},
		{0, store.MatchUnmatched, false},
	} {
		state, review := Classify(tc.score)
		if state != tc.state || review != tc.review {
			t.Errorf("Classify(%v) = %s, %v; want %s, %v", tc.score, state, review, tc.state, tc.review)
		}
	}
	if MatchThreshold != 0.80 || ReviewThreshold != 0.55 {
		t.Errorf("thresholds are %v and %v", MatchThreshold, ReviewThreshold)
	}
}

func TestScoreFormulaAndBoundaries(t *testing.T) {
	// 0.6*sim + 0.3*year + 0.1*popularity, each part visible on its own.
	c := func(title string, year int) Candidate { return Candidate{Title: title, Year: year} }
	for _, tc := range []struct {
		name string
		q    Query
		c    Candidate
		pop  float64
		want float64
	}{
		{"all perfect", movieQ("A B", 2000), c("A B", 2000), 1, 1},
		{"only the title", movieQ("A B", 2000), c("A B", 1900), 0, 0.6},
		{"only the year", movieQ("A B", 2000), c("X Y", 2000), 0, 0.3},
		{"only popularity", movieQ("A B", 2000), c("X Y", 1900), 1, 0.1},
		{"year off by one", movieQ("A B", 2000), c("X Y", 2001), 0, 0.24},
		{"year unknown", movieQ("A B", 0), c("X Y", 2001), 0, 0.15},
		{"half the title", movieQ("A B", 2000), c("A C", 1900), 0, 0.3},
		{"0.8 on paper is 0.8", movieQ("A B", 0), c("A B", 1999), 0.5, 0.8},
		{"0.55 on paper is 0.55", movieQ("A B", 0), c("A C", 1999), 1, 0.55},
		{"original title counts", movieQ("Cidade de Deus", 2002), Candidate{Title: "City of God", OriginalTitle: "Cidade de Deus", Year: 2002}, 0, 0.9},
		{"the better of the two titles", movieQ("City of God", 2002), Candidate{Title: "City of God", OriginalTitle: "Cidade de Deus", Year: 2002}, 0, 0.9},
	} {
		if got := score(tc.q, tc.c, tc.pop); got != tc.want {
			t.Errorf("%s: score = %v, want %v", tc.name, got, tc.want)
		}
	}
	// And the two that sit on a threshold are on the right side of it.
	if state, review := Classify(score(movieQ("A B", 0), c("A B", 1999), 0.5)); state != store.MatchMatched || review {
		t.Errorf("0.8 classified as %s review %v", state, review)
	}
	if state, review := Classify(score(movieQ("A B", 0), c("A C", 1999), 1)); state != store.MatchMatched || !review {
		t.Errorf("0.55 classified as %s review %v", state, review)
	}
}

func TestScoreCandidatesUsesOnlyTheTopTen(t *testing.T) {
	var cands []Candidate
	for i := range 12 {
		cands = append(cands, Candidate{ID: int64(i + 1), Title: fmt.Sprintf("Other %d", i), Year: 1900, Popularity: float64(i)})
	}
	cands[10] = Candidate{ID: 999, Title: "Perfect", Year: 2000, Popularity: 100} // the 11th
	got := ScoreCandidates(movieQ("Perfect", 2000), cands)
	if len(got) != 10 {
		t.Errorf("scored %d candidates, want 10", len(got))
	}
	for _, s := range got {
		if s.ID == 999 {
			t.Error("the 11th result was scored")
		}
	}
	// Positive control: in the top ten, it wins.
	cands[0], cands[10] = cands[10], cands[0]
	if got := ScoreCandidates(movieQ("Perfect", 2000), cands); got[0].ID != 999 {
		t.Errorf("the perfect candidate in the top ten lost: %+v", got[0])
	}
}

func TestScoreCandidatesKeepsTMDBOrderForTies(t *testing.T) {
	cands := []Candidate{
		{ID: 1, Title: "Same", Year: 2000, Popularity: 5},
		{ID: 2, Title: "Same", Year: 2000, Popularity: 5},
		{ID: 3, Title: "Same", Year: 2000, Popularity: 5},
	}
	got := ScoreCandidates(movieQ("Same", 2000), cands)
	if got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 {
		t.Errorf("ties reordered: %+v", got)
	}
}

func TestSimplifyQuery(t *testing.T) {
	for in, want := range map[string]string{
		"Amélie":                   "amelie",
		"Tom & Jerry":              "tom and jerry",
		"The Grand Budapest Hotel": "grand budapest hotel",
		"A Beautiful Mind":         "beautiful mind",
		"An Education":             "education",
		"Spider-Man: Homecoming":   "spider man homecoming",
		"  The   Matrix  ":         "matrix",
		"The":                      "the",
		"Thelma & Louise":          "thelma and louise",
		"Andromeda":                "andromeda",
		"It's Complicated":         "its complicated",
		"L’Auberge Espagnole":      "lauberge espagnole",
		"The A-Team":               "a team",
		"2001: A Space Odyssey":    "2001 a space odyssey",
		"":                         "",
	} {
		if got := SimplifyQuery(in); got != want {
			t.Errorf("SimplifyQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMovieQuery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
		year  int
		path  string
		want  Query
	}{
		{"file has everything", "Blade Runner", 1982, "/m/Movies/Blade Runner (1982).mkv", Query{Kind: store.KindMovie, Title: "Blade Runner", Year: 1982}},
		{"the folder gives the year", "br", 0, "/m/Movies/Blade Runner (1982)/br.mkv", Query{Kind: store.KindMovie, Title: "Blade Runner", Year: 1982}},
		{"dots in the folder", "br", 0, "/m/Movies/Blade.Runner.(1982)/br.mkv", Query{Kind: store.KindMovie, Title: "Blade Runner", Year: 1982}},
		{"square brackets", "br", 0, "/m/Movies/Blade Runner [1982]/br.mkv", Query{Kind: store.KindMovie, Title: "Blade Runner", Year: 1982}},
		{"number title in a folder", "x", 0, "/m/Movies/1917 (2019)/x.mkv", Query{Kind: store.KindMovie, Title: "1917", Year: 2019}},
		{"the file's year beats the folder's", "Blade Runner", 1982, "/m/Movies/Blade Runner (2049)/x.mkv", Query{Kind: store.KindMovie, Title: "Blade Runner", Year: 1982}},
		{"no year anywhere", "Blade Runner", 0, "/m/Movies/Sci-Fi/Blade Runner.mkv", Query{Kind: store.KindMovie, Title: "Blade Runner"}},
		{"a bare year in the folder is not trusted", "br", 0, "/m/Movies/Blade Runner 2049/br.mkv", Query{Kind: store.KindMovie, Title: "br"}},
		{"only a year in brackets", "x", 0, "/m/Movies/(1982)/x.mkv", Query{Kind: store.KindMovie, Title: "x"}},
		{"implausible year", "x", 0, "/m/Movies/Foo (1066)/x.mkv", Query{Kind: store.KindMovie, Title: "x"}},
		{"id tag in the title and file name", "Movie [tmdbid-603]", 1999, "/m/Movie (1999) [tmdbid-603].mkv", Query{Kind: store.KindMovie, Title: "Movie", Year: 1999, Hints: Hints{TMDB: 603}}},
		{"id in the folder", "m", 0, "/m/Movie (1999) [imdbid-tt0133093]/m.mkv", Query{Kind: store.KindMovie, Title: "Movie", Year: 1999, Hints: Hints{IMDb: "tt0133093"}}},
		{"id in the grandparent is too far", "m", 1999, "/m/Movie [tmdbid-603]/CD1/m.mkv", Query{Kind: store.KindMovie, Title: "m", Year: 1999}},
	} {
		if got := MovieQuery(tc.title, tc.year, tc.path); got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestShowQuery(t *testing.T) {
	got := ShowQuery("Breaking Bad [tvdbid-81189]", "/m/TV/Breaking Bad [tvdbid-81189]/Season 01/S01E01.mkv")
	want := Query{Kind: store.KindSeries, Title: "Breaking Bad", Hints: Hints{TVDB: 81189}}
	if got != want {
		t.Errorf("ShowQuery = %+v, want %+v", got, want)
	}
	got = ShowQuery("Show", "/m/TV/Show {tmdb-1396}/S01E01 {imdb-tt0903747}.mkv")
	if want := (Query{Kind: store.KindSeries, Title: "Show", Hints: Hints{TMDB: 1396, IMDb: "tt0903747"}}); got != want {
		t.Errorf("ShowQuery = %+v, want %+v", got, want)
	}
}
