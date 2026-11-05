package metadata_test

import (
	"errors"
	"testing"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

// showFakes are a TVDB and a TMDB that know no show yet.
func showFakes() (tvdb, tmdb *metadatatest.Fake) {
	return &metadatatest.Fake{Source: metadata.SourceTVDB}, &metadatatest.Fake{}
}

func cand(id int64, title string, year int, pop float64) []metadata.Candidate {
	return []metadata.Candidate{{ID: id, Title: title, OriginalTitle: title, Year: year, Popularity: pop}}
}

var bbQuery = metadata.Query{Kind: store.KindSeries, Title: "Breaking Bad"}

func TestMatchShowAsksTVDBFirstAndTMDBOnlyWhenTVDBIsNotSure(t *testing.T) {
	ctx := t.Context()

	t.Run("a sure match at TVDB asks TMDB nothing", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(81189, "Breaking Bad", 2008, 9)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if err != nil || res.ID != 81189 || res.Source != metadata.SourceTVDB || res.State != store.MatchMatched || res.NeedsReview {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if n := len(tmdb.Calls()); n != 0 {
			t.Errorf("TMDB was asked %d times: %q", n, tmdb.Calls())
		}
	})

	t.Run("TVDB finds nothing: TMDB is tried", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(1396, "Breaking Bad", 2008, 9)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if err != nil || res.ID != 1396 || res.Source != metadata.SourceTMDB || res.State != store.MatchMatched {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if tvdb.Count("SearchSeries") == 0 {
			t.Error("TVDB was not asked first")
		}
	})

	t.Run("a doubtful match at TVDB loses to a sure one at TMDB", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(5, "Breaking Bed", 0, 1)}
		tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(1396, "Breaking Bad", 2008, 9)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if err != nil || res.Source != metadata.SourceTMDB || res.ID != 1396 {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})

	t.Run("a doubtful match at TVDB stands when TMDB has nothing better", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(5, "Breaking Bed", 0, 1)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if err != nil || res.Source != metadata.SourceTVDB || res.ID != 5 || res.State != store.MatchMatched || !res.NeedsReview {
			t.Fatalf("result = %+v, %v; want TVDB's, for review", res, err)
		}
		if tmdb.Count("SearchSeries") == 0 {
			t.Error("TMDB was not tried for a match that is not sure")
		}
	})

	t.Run("a tie goes to TVDB", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(5, "Breaking Bed", 0, 1)}
		tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(6, "Breaking Bed", 0, 1)}
		res, _ := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if res.Source != metadata.SourceTVDB {
			t.Errorf("result = %+v", res)
		}
	})

	t.Run("TVDB out of reach is a backoff, not a fallback", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(1396, "Breaking Bad", 2008, 9)}
		tvdb.Offline()
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery)
		if err == nil || !metadata.IsTransient(err) || res.ID != 0 {
			t.Fatalf("result = %+v, %v; want the transient error", res, err)
		}
		if n := len(tmdb.Calls()); n != 0 {
			t.Errorf("TMDB was asked while TVDB was down: %q", tmdb.Calls())
		}
		// Positive control: back up, TVDB answers (nothing there) and TMDB is tried.
		tvdb.Online()
		if res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery); err != nil || res.Source != metadata.SourceTMDB {
			t.Errorf("with TVDB up: %+v, %v", res, err)
		}
	})

	t.Run("a doubtful match at TVDB is not settled for while TMDB is out of reach", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(5, "Breaking Bed", 0, 1)}
		tmdb.Offline()
		if _, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, bbQuery); err == nil || !metadata.IsTransient(err) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("one provider alone", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(81189, "Breaking Bad", 2008, 9)}
		tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(1396, "Breaking Bad", 2008, 9)}
		if res, err := metadata.NewProviders(tmdb, nil).MatchShow(ctx, bbQuery); err != nil || res.Source != metadata.SourceTMDB {
			t.Errorf("TMDB alone: %+v, %v", res, err)
		}
		if res, err := metadata.NewProviders(nil, tvdb).MatchShow(ctx, bbQuery); err != nil || res.Source != metadata.SourceTVDB {
			t.Errorf("TVDB alone: %+v, %v", res, err)
		}
		if _, err := (*metadata.Providers)(nil).MatchShow(ctx, bbQuery); !errors.Is(err, metadata.ErrLookupsOff) {
			t.Errorf("no providers: %v", err)
		}
		if metadata.NewProviders(nil, nil) != nil {
			t.Error("NewProviders(nil, nil) is not nil")
		}
	})
}

func TestMatchShowIdHints(t *testing.T) {
	ctx := t.Context()
	q := func(h metadata.Hints) metadata.Query {
		return metadata.Query{Kind: store.KindSeries, Title: "Whatever", Hints: h}
	}

	t.Run("a TVDB id is exact: no search", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, q(metadata.Hints{TVDB: 81189}))
		if err != nil || res.ID != 81189 || res.Source != metadata.SourceTVDB || !res.ByID || res.Score != 1 {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if len(tvdb.Calls()) != 0 || len(tmdb.Calls()) != 0 {
			t.Errorf("calls: TVDB %q, TMDB %q", tvdb.Calls(), tmdb.Calls())
		}
	})

	t.Run("a TMDB id alone goes straight to TMDB", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, q(metadata.Hints{TMDB: 1396}))
		if err != nil || res.ID != 1396 || res.Source != metadata.SourceTMDB || !res.ByID {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if len(tvdb.Calls()) != 0 {
			t.Errorf("TVDB was asked: %q", tvdb.Calls())
		}
	})

	t.Run("an IMDb id is looked up at TVDB first", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tvdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0903747": cand(81189, "Breaking Bad", 2008, 0)}
		tmdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0903747": cand(1396, "Breaking Bad", 2008, 0)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, q(metadata.Hints{IMDb: "tt0903747"}))
		if err != nil || res.ID != 81189 || res.Source != metadata.SourceTVDB || !res.ByID {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if len(tmdb.Calls()) != 0 {
			t.Errorf("TMDB was asked: %q", tmdb.Calls())
		}
	})

	t.Run("an IMDb id TVDB does not know is found at TMDB", func(t *testing.T) {
		tvdb, tmdb := showFakes()
		tmdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0903747": cand(1396, "Breaking Bad", 2008, 0)}
		res, err := metadata.NewProviders(tmdb, tvdb).MatchShow(ctx, q(metadata.Hints{IMDb: "tt0903747"}))
		if err != nil || res.ID != 1396 || res.Source != metadata.SourceTMDB || !res.ByID {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})
}

func TestSearchShowsListsBothAndToleratesOne(t *testing.T) {
	ctx := t.Context()
	tvdb, tmdb := showFakes()
	tvdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(81189, "Breaking Bad", 2008, 9)}
	tmdb.SeriesSearch = map[string][]metadata.Candidate{"breaking bad": cand(1396, "Breaking Bad", 2008, 9)}
	p := metadata.NewProviders(tmdb, tvdb)

	res, err := p.SearchShows(ctx, bbQuery)
	if err != nil || len(res) != 2 || res[0].Source != metadata.SourceTVDB || res[0].ID != 81189 || res[1].Source != metadata.SourceTMDB || res[1].ID != 1396 {
		t.Fatalf("results = %+v, %v; want TVDB's first, then TMDB's, each saying where from", res, err)
	}
	// One out of reach is not "nothing found".
	tvdb.Offline()
	if res, err := p.SearchShows(ctx, bbQuery); err != nil || len(res) != 1 || res[0].Source != metadata.SourceTMDB {
		t.Errorf("with TVDB down: %+v, %v", res, err)
	}
	tmdb.Offline()
	if res, err := p.SearchShows(ctx, bbQuery); err == nil || res != nil {
		t.Errorf("with both down: %+v, %v", res, err)
	}
}

func TestResolveShow(t *testing.T) {
	ctx := t.Context()
	tvdb, tmdb := showFakes()
	tvdb.Slugs = map[string]int64{"breaking-bad": 81189}
	tvdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0903747": cand(81189, "Breaking Bad", 2008, 0)}
	tmdb.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0944947": cand(1399, "Game of Thrones", 2011, 0)}
	both, onlyTMDB, onlyTVDB := metadata.NewProviders(tmdb, tvdb), metadata.NewProviders(tmdb, nil), metadata.NewProviders(nil, tvdb)

	for _, tc := range []struct {
		name    string
		p       *metadata.Providers
		ref     metadata.Ref
		want    metadata.ShowRef
		wantErr error
	}{
		{"a TMDB id", both, metadata.Ref{TMDBID: 1396}, metadata.ShowRef{Source: "tmdb", ID: 1396}, nil},
		{"a TVDB id", both, metadata.Ref{TVDBID: 81189}, metadata.ShowRef{Source: "tvdb", ID: 81189}, nil},
		{"a slug", both, metadata.Ref{TVDBSlug: "breaking-bad"}, metadata.ShowRef{Source: "tvdb", ID: 81189}, nil},
		{"an unknown slug", both, metadata.Ref{TVDBSlug: "nope"}, metadata.ShowRef{}, metadata.ErrNoSuchTitle},
		{"an IMDb id that TVDB knows", both, metadata.Ref{IMDbID: "tt0903747"}, metadata.ShowRef{Source: "tvdb", ID: 81189}, nil},
		{"an IMDb id only TMDB knows", both, metadata.Ref{IMDbID: "tt0944947"}, metadata.ShowRef{Source: "tmdb", ID: 1399}, nil},
		{"an IMDb id nobody knows", both, metadata.Ref{IMDbID: "tt0000001"}, metadata.ShowRef{}, metadata.ErrNoSuchTitle},
		{"a TVDB id with no TVDB key", onlyTMDB, metadata.Ref{TVDBID: 81189}, metadata.ShowRef{}, metadata.ErrNoTVDB},
		{"a slug with no TVDB key", onlyTMDB, metadata.Ref{TVDBSlug: "breaking-bad"}, metadata.ShowRef{}, metadata.ErrNoTVDB},
		{"a TMDB id with no TMDB key", onlyTVDB, metadata.Ref{TMDBID: 1396}, metadata.ShowRef{}, metadata.ErrNoTMDB},
		{"an IMDb id at TMDB alone", onlyTMDB, metadata.Ref{IMDbID: "tt0944947"}, metadata.ShowRef{Source: "tmdb", ID: 1399}, nil},
		{"nothing set up", nil, metadata.Ref{TMDBID: 1}, metadata.ShowRef{}, metadata.ErrLookupsOff},
	} {
		got, err := tc.p.ResolveShow(ctx, tc.ref)
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("%s: %+v, %v; want %+v, %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestLookupNamesOneShowFromWhatWasTyped(t *testing.T) {
	ctx := t.Context()
	tvdb, tmdb := showFakes()
	tvdb.Slugs = map[string]int64{"breaking-bad": 81189}
	tvdb.Shows = map[int64]*store.TitleInput{81189: {Title: store.Title{Kind: store.KindSeries, TVDBID: 81189, Title: "Breaking Bad", Year: 2008,
		Overview: "Chemistry.", Poster: "tvdb:/banners/posters/81189-10.jpg"}}}
	p := metadata.NewProviders(tmdb, tvdb)

	for _, text := range []string{"tvdb:81189", "TVDB: 81189", "https://thetvdb.com/series/breaking-bad", "thetvdb.com/series/breaking-bad/allseasons/official",
		"https://www.thetvdb.com/?tab=series&id=81189"} {
		c, named, err := p.Lookup(ctx, text)
		if !named || err != nil || c.ID != 81189 || c.Source != metadata.SourceTVDB || c.Score != 1 || c.Title != "Breaking Bad" || c.Year != 2008 ||
			c.Poster != "tvdb:/banners/posters/81189-10.jpg" {
			t.Errorf("Lookup(%q) = %+v, %v, %v", text, c, named, err)
		}
	}
	// Words to search for are not a lookup.
	for _, text := range []string{"Breaking Bad", "tvdb", "tvdb:abc", "https://example.com/series/breaking-bad", "https://thetvdb.com.evil.example/series/x"} {
		if _, named, _ := p.Lookup(ctx, text); named {
			t.Errorf("Lookup(%q) took it for an address", text)
		}
	}
	// Named, but not found or not possible.
	if _, named, err := p.Lookup(ctx, "tvdb:4242"); !named || !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("an id TVDB does not have: %v, %v", named, err)
	}
	if _, named, err := p.Lookup(ctx, "thetvdb.com/series/no-such"); !named || !errors.Is(err, metadata.ErrNoSuchTitle) {
		t.Errorf("a slug TVDB does not have: %v, %v", named, err)
	}
	if _, named, err := metadata.NewProviders(tmdb, nil).Lookup(ctx, "tvdb:81189"); !named || !errors.Is(err, metadata.ErrNoTVDB) {
		t.Errorf("with no TVDB key: %v, %v", named, err)
	}
	// A look-up fetches one record, not the show's episodes.
	if n := tvdb.Count("Series("); n != 0 {
		t.Errorf("Lookup fetched the show %d times", n)
	}
}

func TestParseTVDBRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		id   int64
		slug string
		ok   bool
	}{
		{"tvdb:81189", 81189, "", true},
		{"  TVDB : 81189 ", 81189, "", true},
		{"tvdb:0", 0, "", false},
		{"tvdb:-5", 0, "", false},
		{"tvdb:12345678901", 0, "", false},
		{"https://thetvdb.com/series/breaking-bad", 0, "breaking-bad", true},
		{"http://www.thetvdb.com/series/Breaking-Bad/", 0, "breaking-bad", true},
		{"thetvdb.com/series/breaking-bad", 0, "breaking-bad", true},
		{"https://thetvdb.com/series/breaking-bad/allseasons/official", 0, "breaking-bad", true},
		{"https://thetvdb.com/series/breaking-bad?tab=seasons#top", 0, "breaking-bad", true},
		{"https://thetvdb.com/series/81189", 81189, "", true},
		{"https://thetvdb.com/dereferrer/series/81189", 81189, "", true},
		{"https://thetvdb.com/?tab=series&id=81189", 81189, "", true},
		{"https://thetvdb.com/index.php?tab=series&id=81189&lid=7", 81189, "", true},
		{"https://thetvdb.com/series/", 0, "", false},
		{"https://thetvdb.com/movies/the-matrix", 0, "", false},
		{"https://thetvdb.com/series/bad slug", 0, "", false},
		{"https://thetvdb.com/series/../x", 0, "", false},
		{"https://thetvdb.com:8443/series/breaking-bad", 0, "", false},
		{"https://user@thetvdb.com/series/breaking-bad", 0, "", false},
		{"https://evil.example/series/breaking-bad", 0, "", false},
		{"https://thetvdb.com.evil.example/series/breaking-bad", 0, "", false},
		{"https://notthetvdb.com/series/breaking-bad", 0, "", false},
		{"ftp://thetvdb.com/series/breaking-bad", 0, "", false},
		{"breaking bad", 0, "", false},
		{"", 0, "", false},
	} {
		id, slug, ok := metadata.ParseTVDBRef(tc.in)
		if id != tc.id || slug != tc.slug || ok != tc.ok {
			t.Errorf("ParseTVDBRef(%q) = %d, %q, %v; want %d, %q, %v", tc.in, id, slug, ok, tc.id, tc.slug, tc.ok)
		}
	}
}

func FuzzParseTVDBRef(f *testing.F) {
	for _, s := range []string{"tvdb:1", "thetvdb.com/series/x", "https://thetvdb.com/?tab=series&id=2", "%", "http://[::1", "tvdb:\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		id, slug, ok := metadata.ParseTVDBRef(s)
		if !ok && (id != 0 || slug != "") {
			t.Fatalf("not ok but %d, %q", id, slug)
		}
		if ok && (id < 0 || (id == 0) == (slug == "")) {
			t.Fatalf("ok but id %d, slug %q: exactly one names the show", id, slug)
		}
	})
}
