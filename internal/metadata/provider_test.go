package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"ply/internal/store"
)

func newTMDB(t *testing.T, f *fakeTMDB, mod ...func(*Options)) *TMDB {
	t.Helper()
	return NewTMDB(testClient(t, f, mod...))
}

// creditSummary renders credits as "kind job name #order" lines.
func creditSummary(credits []store.Credit) []string {
	var out []string
	for _, c := range credits {
		out = append(out, fmt.Sprintf("%s %s %s #%d", c.Kind, c.Job, c.Person.Name, c.Order))
	}
	return out
}

func TestProviderMovie(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/movie/603", "", "movie_603.json")
	f.on("/collection/2344", "", "collection_2344.json")
	in, err := newTMDB(t, f).Movie(t.Context(), 603)
	if err != nil {
		t.Fatal(err)
	}

	want := store.Title{
		Kind: store.KindMovie, TMDBID: 603, IMDbID: "tt0133093", Title: "The Matrix", OriginalTitle: "The Matrix",
		SortTitle: "Matrix, The", Year: 1999, Overview: in.Overview, Tagline: "Welcome to the Real World.",
		ReleaseDate: "1999-03-30", Status: "Released", RuntimeMin: 136, Rating: 8.2, VoteCount: 25102,
		Certification: "R", Poster: "tmdb:/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg", Backdrop: "tmdb:/fNG7i7RqMErkcqhohV2a6cV1Ehy.jpg",
		Logo: "tmdb:/matrix_logo_en.png", Source: store.SourceTMDB,
	}
	if in.Title != want || in.Overview == "" {
		t.Errorf("title:\n got %+v\nwant %+v", in.Title, want)
	}
	if !reflect.DeepEqual(in.Genres, []store.Genre{{ID: 28, Name: "Action"}, {ID: 878, Name: "Science Fiction"}}) {
		t.Errorf("genres = %+v", in.Genres)
	}
	wantColl := &store.Collection{TMDBID: 2344, Name: "The Matrix Collection", Poster: "tmdb:/lh4aGpd3U9rm9B8Oqr6CUgQLtZL.jpg",
		Backdrop: "tmdb:/bRm2DEgUiYciDw3myHuYFInD7la.jpg", Overview: "The Matrix Collection is a science fiction action franchise created by the Wachowskis."}
	if !reflect.DeepEqual(in.Collection, wantColl) {
		t.Errorf("collection = %+v, want %+v", in.Collection, wantColl)
	}
	// Cast in billing order; crew only in the kept jobs, most important first,
	// in TMDB's order within a job.
	wantCredits := []string{
		"cast  Keanu Reeves #0", "cast  Laurence Fishburne #1", "cast  Carrie-Anne Moss #2",
		"cast  Hugo Weaving #3", "cast  Joe Pantoliano #4", "cast  Gloria Foster #5",
		"crew Director Lilly Wachowski #0", "crew Director Lana Wachowski #1",
		"crew Writer Lana Wachowski #2", "crew Writer Lilly Wachowski #3",
		"crew Original Music Composer Don Davis #4", "crew Director of Photography Bill Pope #5",
		"crew Producer Joel Silver #6",
	}
	if got := creditSummary(in.Credits); !slices.Equal(got, wantCredits) {
		t.Errorf("credits:\n got %q\nwant %q", got, wantCredits)
	}
	if c := in.Credits[0]; c.Character != "Thomas A. Anderson / Neo" || c.Person.TMDBID != 6384 || c.Person.Profile != "tmdb:/4D0PpNI0kmP58hgrwGC3wCjxhnm.jpg" {
		t.Errorf("first cast credit = %+v", c)
	}
	if c := in.Credits[5]; c.Person.Profile != "" {
		t.Errorf("a person with no photo has profile %q", c.Person.Profile)
	}
	if n := f.count("/collection/2344"); n != 1 {
		t.Errorf("the collection was fetched %d times, want once", n)
	}
}

func TestProviderMovieRegionsAndLogoLanguage(t *testing.T) {
	for _, tc := range []struct {
		lang, cert, logo string
	}{
		{"en-US", "R", "tmdb:/matrix_logo_en.png"},
		{"en-GB", "15", "tmdb:/matrix_logo_en.png"},
		{"de-DE", "16", "tmdb:/matrix_logo_null.png"}, // no German logo: the textless one
		{"fr-FR", "R", "tmdb:/matrix_logo_fr.png"},    // no French rating: the US one
		{"pt-BR", "R", "tmdb:/matrix_logo_null.png"},
	} {
		f := newFakeTMDB(t)
		f.on("/movie/603", "", "movie_603.json")
		f.on("/collection/2344", "", "collection_2344.json")
		in, err := newTMDB(t, f, func(o *Options) { o.Language = tc.lang }).Movie(t.Context(), 603)
		if err != nil {
			t.Fatal(err)
		}
		if in.Certification != tc.cert || in.Logo != tc.logo {
			t.Errorf("%s: certification %q logo %q; want %q %q", tc.lang, in.Certification, in.Logo, tc.cert, tc.logo)
		}
		req := f.requests()[0]
		if req.Query.Get("language") != tc.lang {
			t.Errorf("%s: language param %q", tc.lang, req.Query.Get("language"))
		}
		if got, want := req.Query.Get("include_image_language"), tc.lang[:2]+",null"; got != want {
			t.Errorf("%s: include_image_language %q, want %q", tc.lang, got, want)
		}
	}
}

func TestProviderMovieCollectionFailures(t *testing.T) {
	// A collection that is gone: the movie keeps what it says about it.
	f := newFakeTMDB(t)
	f.on("/movie/603", "", "movie_603.json")
	f.fail("/collection/2344", "", 404, "error_404.json", nil)
	in, err := newTMDB(t, f).Movie(t.Context(), 603)
	switch {
	case err != nil:
		t.Errorf("with a 404 collection: %v", err)
	case in.Collection == nil || in.Collection.TMDBID != 2344 || in.Collection.Name != "The Matrix Collection" || in.Collection.Overview != "":
		t.Errorf("with a 404 collection: %+v", in.Collection)
	}

	// One that may come back: the movie is not stored without it.
	f = newFakeTMDB(t)
	f.on("/movie/603", "", "movie_603.json")
	f.fail("/collection/2344", "", 503, "", nil)
	if in, err := newTMDB(t, f).Movie(t.Context(), 603); err == nil || !IsTransient(err) || in != nil {
		t.Errorf("with a 503 collection: %+v, %v; want a transient error", in, err)
	}

	// Not found movie.
	f = newFakeTMDB(t)
	f.fail("/movie/9", "", 404, "error_404.json", nil)
	if _, err := newTMDB(t, f).Movie(t.Context(), 9); !IsNotFound(err) {
		t.Errorf("unknown movie: %v", err)
	}
}

func TestMovieInputCapsAndOrdersCast(t *testing.T) {
	d := &MovieDetails{ID: 1, Title: "Big Cast", ReleaseDate: "2020-05-05"}
	for i := 0; i < 45; i++ { // listed in reverse billing order
		n := 44 - i
		d.Credits.Cast = append(d.Credits.Cast, CastCredit{ID: int64(1000 + n), Name: fmt.Sprintf("Actor %02d", n), Character: fmt.Sprintf("Role %d", n), Order: n})
	}
	for i := 0; i < 12; i++ {
		d.Credits.Crew = append(d.Credits.Crew, CrewCredit{ID: int64(2000 + i), Name: fmt.Sprintf("Producer %d", i), Job: "Producer"})
	}
	in := movieInput(d, nil, "en", "US")
	var cast []string
	crew := 0
	for _, c := range in.Credits {
		if c.Kind == store.CreditCast {
			cast = append(cast, c.Person.Name)
		} else {
			crew++
		}
	}
	if len(cast) != maxCast || cast[0] != "Actor 00" || cast[29] != "Actor 29" {
		t.Errorf("cast = %d people from %q to %q; want the top %d by order", len(cast), cast[0], cast[len(cast)-1], maxCast)
	}
	if crew != 12 {
		t.Errorf("crew = %d, want every producer", crew)
	}
	if in.Collection != nil || in.Year != 2020 {
		t.Errorf("collection %+v year %d", in.Collection, in.Year)
	}
}

func TestProviderSeries(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/tv/1396", "", "tv_1396.json")
	f.on("/tv/1396/season/1", "", "tv_1396_season_1.json")
	f.fail("/tv/1396/season/2", "", 404, "error_404.json", nil)
	in, err := newTMDB(t, f).Series(t.Context(), 1396, []int{1, 2, 1})
	if err != nil {
		t.Fatal(err)
	}

	got := in.Title
	if got.Kind != store.KindSeries || got.TMDBID != 1396 || got.TVDBID != 81189 || got.IMDbID != "tt0903747" ||
		got.Title != "Breaking Bad" || got.SortTitle != "Breaking Bad" || got.Year != 2008 || got.ReleaseDate != "2008-01-20" ||
		got.EndDate != "2013-09-29" || got.Status != "Ended" || got.RuntimeMin != 45 || got.Certification != "TV-MA" ||
		got.Logo != "tmdb:/bb_logo_en.png" || got.NextAirDate != "" || got.Tagline != "Change the equation." ||
		got.Poster != "tmdb:/ggFHVNu6YYI5L9pCfOacjizRGt.jpg" || got.Source != store.SourceTMDB {
		t.Errorf("title: %+v", got)
	}
	if !reflect.DeepEqual(in.Genres, []store.Genre{{ID: 18, Name: "Drama"}, {ID: 80, Name: "Crime"}}) {
		t.Errorf("genres = %+v", in.Genres)
	}
	wantCredits := []string{
		"cast  Bryan Cranston #0", "cast  Aaron Paul #1", "cast  Anna Gunn #2",
		"crew Creator Vince Gilligan #0", "crew Director Vince Gilligan #1", "crew Writer Vince Gilligan #2",
		"crew Original Music Composer Dave Porter #3", "crew Director of Photography Michael Slovis #4",
	}
	if g := creditSummary(in.Credits); !slices.Equal(g, wantCredits) {
		t.Errorf("credits:\n got %q\nwant %q", g, wantCredits)
	}
	if c := in.Credits[0]; c.Character != "Walter White" {
		t.Errorf("character = %q; the role played in most episodes", c.Character)
	}

	if len(in.Seasons) != 1 {
		t.Fatalf("seasons = %+v; season 2 is unknown to TMDB and is left out", in.Seasons)
	}
	se := in.Seasons[0]
	if se.Number != 1 || se.Name != "Season 1" || se.AirDate != "2008-01-20" || se.Poster != "tmdb:/1BP4xYv9ZG4ZVHkL7ocOziBbSYH.jpg" || len(se.Episodes) != 7 {
		t.Errorf("season: %+v", se)
	}
	wantEp := store.Episode{Season: 1, Number: 1, Name: "Pilot", Overview: "Diagnosed with terminal lung cancer, a high school chemistry teacher turns to a life of crime.",
		AirDate: "2008-01-20", RuntimeMin: 58, Still: "tmdb:/ydlY3iPfeOAvu8gVqrxPoMvzNCn.jpg", Rating: 8.3}
	if se.Episodes[0] != wantEp {
		t.Errorf("episode 1 = %+v, want %+v", se.Episodes[0], wantEp)
	}
	if se.Episodes[2].Still != "" {
		t.Errorf("episode 3 has still %q, TMDB has none", se.Episodes[2].Still)
	}
	if f.count("/tv/1396/season/1") != 1 || f.count("/tv/1396/season/2") != 1 {
		t.Errorf("season requests: %q (each season once, even when listed twice)", f.asked())
	}
}

func TestProviderSeriesAiringAndFailures(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/tv/100088", "", "tv_airing.json")
	in, err := newTMDB(t, f).Series(t.Context(), 100088, nil)
	if err != nil {
		t.Fatal(err)
	}
	if in.NextAirDate != "2026-11-08" || in.Status != "Returning Series" || in.EndDate != "2026-11-01" || len(in.Seasons) != 0 || in.Certification != "TV-MA" || in.TVDBID != 392256 {
		t.Errorf("airing show: %+v", in.Title)
	}

	// A season that fails for a reason that may pass: no half-fetched show.
	f = newFakeTMDB(t)
	f.on("/tv/1396", "", "tv_1396.json")
	f.on("/tv/1396/season/1", "", "tv_1396_season_1.json")
	f.fail("/tv/1396/season/2", "", 500, "", nil)
	if in, err := newTMDB(t, f).Series(t.Context(), 1396, []int{1, 2}); err == nil || !IsTransient(err) || in != nil {
		t.Errorf("Series with a failing season = %v, %v", in, err)
	}
}

func TestProviderPersonFindChangesCheck(t *testing.T) {
	ctx := t.Context()
	f := newFakeTMDB(t)
	f.on("/person/6384", "", "person_6384.json")
	f.on("/find/tt0133093", params("external_source", "imdb_id"), "find_imdb_tt0133093.json")
	f.on("/find/tt0000000", params("external_source", "imdb_id"), "find_empty.json")
	f.on("/configuration", "", "configuration.json")
	day := params("end_date", "2026-11-02", "start_date", "2026-11-01", "page", "1")
	f.on("/movie/changes", day, "changes_movie_page1.json")
	f.on("/movie/changes", params("end_date", "2026-11-02", "start_date", "2026-11-01", "page", "2"), "changes_movie_page2.json")
	f.on("/tv/changes", day, "changes_tv_page1.json")
	p := newTMDB(t, f)

	person, err := p.Person(ctx, 6384)
	want := &store.Person{TMDBID: 6384, Name: "Keanu Reeves", Profile: "tmdb:/4D0PpNI0kmP58hgrwGC3wCjxhnm.jpg",
		Biography: person.Biography, Birthday: "1964-09-02", PlaceOfBirth: "Beirut, Lebanon", KnownFor: "Acting", IMDbID: "nm0000206"}
	if err != nil || !reflect.DeepEqual(person, want) || person.Biography == "" {
		t.Errorf("Person = %+v, %v", person, err)
	}

	movies, series, err := p.Find(ctx, ExternalIMDb, "tt0133093")
	if err != nil || len(movies) != 1 || len(series) != 0 || movies[0].TMDBID != 603 || movies[0].Year != 1999 || movies[0].Title != "The Matrix" {
		t.Errorf("Find = %+v %+v %v", movies, series, err)
	}
	if movies, series, err = p.Find(ctx, ExternalIMDb, "tt0000000"); err != nil || len(movies)+len(series) != 0 {
		t.Errorf("Find(unknown) = %+v %+v %v", movies, series, err)
	}

	start, end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if ids, err := p.Changes(ctx, store.KindMovie, start, end); err != nil || !slices.Equal(ids, []int64{603, 604, 999001, 27205, 949}) {
		t.Errorf("Changes(movie) = %v, %v", ids, err)
	}
	if ids, err := p.Changes(ctx, store.KindSeries, start, end); err != nil || !slices.Equal(ids, []int64{1396, 100088}) {
		t.Errorf("Changes(series) = %v, %v", ids, err)
	}

	if err := p.Check(ctx); err != nil {
		t.Errorf("Check = %v", err)
	}
	bad := newFakeTMDB(t)
	bad.fail("/configuration", "", 401, "error_401.json", nil)
	if err := newTMDB(t, bad).Check(ctx); !IsUnauthorized(err) {
		t.Errorf("Check with a bad key = %v", err)
	}
	if got := p.ImageURL("w342", "/abc.jpg"); got != f.srv.URL+"/img/w342/abc.jpg" {
		t.Errorf("ImageURL = %q", got)
	}
}

func TestProviderSearchCandidates(t *testing.T) {
	f := newFakeTMDB(t)
	f.on("/search/movie", params("query", "Amelie"), "search_movie_amelie.json")
	f.on("/search/tv", params("query", "Breaking Bad", "first_air_date_year", "2008"), "search_tv_breaking_bad.json")
	p := newTMDB(t, f)

	cands, err := p.SearchMovies(t.Context(), "Amelie", 0)
	if err != nil || len(cands) != 2 {
		t.Fatalf("SearchMovies = %+v, %v", cands, err)
	}
	want := Candidate{TMDBID: 194, Title: "Amélie", OriginalTitle: "Le Fabuleux Destin d'Amélie Poulain", Year: 2001,
		Overview: cands[0].Overview, Poster: "tmdb:/nSxDa3M9aMvGVLoItzWTepQ5h5d.jpg", Popularity: 40.121}
	if cands[0] != want {
		t.Errorf("candidate = %+v, want %+v", cands[0], want)
	}
	shows, err := p.SearchSeries(t.Context(), "Breaking Bad", 2008)
	if err != nil || len(shows) != 2 || shows[0].TMDBID != 1396 || shows[0].Title != "Breaking Bad" || shows[0].Year != 2008 {
		t.Errorf("SearchSeries = %+v, %v", shows, err)
	}
}

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Matrix":     "Matrix, The",
		"A Bug's Life":   "Bug's Life, A",
		"An Education":   "Education, An",
		"the matrix":     "matrix, the",
		"The  Matrix":    "Matrix, The",
		"  The Matrix  ": "Matrix, The",
		"Theatre":        "Theatre",
		"The":            "The",
		"A":              "A",
		"An":             "An",
		"Anna":           "Anna",
		"1917":           "1917",
		"Amélie":         "Amélie",
		"The The":        "The, The",
		"":               "",
	} {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestYearOfAndImageRef(t *testing.T) {
	for in, want := range map[string]int{"1999-03-30": 1999, "2001": 2001, "": 0, "99": 0, "abcd-01-01": 0, "0000-00-00": 0, "1750-01-01": 0, "2999-01-01": 0} {
		if got := yearOf(in); got != want {
			t.Errorf("yearOf(%q) = %d, want %d", in, got, want)
		}
	}
	if ImageRef("/a.jpg") != "tmdb:/a.jpg" || ImageRef("") != "" {
		t.Error("ImageRef")
	}
}

func TestBestLogoAndCertification(t *testing.T) {
	str := func(s string) *string { return &s }
	logos := Images{Logos: []Image{
		{FilePath: "/fr.png", Language: str("fr"), VoteAverage: 9},
		{FilePath: "/null-low.png", VoteAverage: 4},
		{FilePath: "/null-high.png", VoteAverage: 6},
		{FilePath: "/en-low.png", Language: str("en"), VoteAverage: 3},
		{FilePath: "/en-high.png", Language: str("en"), VoteAverage: 5, VoteCount: 1},
		{FilePath: "/en-high-more-votes.png", Language: str("en"), VoteAverage: 5, VoteCount: 9},
		{FilePath: "", Language: str("en"), VoteAverage: 99},
		{FilePath: "/empty-lang.png", Language: str(""), VoteAverage: 7},
	}}
	for lang, want := range map[string]string{
		"en": "/en-high-more-votes.png", "EN": "/en-high-more-votes.png", "fr": "/fr.png", "ja": "/empty-lang.png",
	} {
		if got := bestLogo(logos, lang); got != want {
			t.Errorf("bestLogo(%q) = %q, want %q", lang, got, want)
		}
	}
	if got := bestLogo(Images{Logos: []Image{{FilePath: "/fr.png", Language: str("fr")}}}, "en"); got != "" {
		t.Errorf("only a foreign logo: %q", got)
	}
	if got := bestLogo(Images{}, "en"); got != "" {
		t.Errorf("no logos: %q", got)
	}

	var rd ReleaseDates
	raw := `{"results":[
		{"iso_3166_1":"US","release_dates":[{"certification":"","type":1},{"certification":"PG","type":2},{"certification":"R","type":3}]},
		{"iso_3166_1":"GB","release_dates":[{"certification":"","type":3},{"certification":"18","type":5}]},
		{"iso_3166_1":"FR","release_dates":[{"certification":"","type":3}]}]}`
	if err := json.Unmarshal([]byte(raw), &rd); err != nil {
		t.Fatal(err)
	}
	for region, want := range map[string]string{
		"US": "R",  // theatrical beats the earlier type 2
		"GB": "18", // no theatrical rating: the first one there is
		"FR": "R",  // nothing in France: the US one
		"JP": "R",
	} {
		if got := movieCertification(rd, region); got != want {
			t.Errorf("movieCertification(%s) = %q, want %q", region, got, want)
		}
	}
	if got := movieCertification(ReleaseDates{}, "US"); got != "" {
		t.Errorf("no release dates: %q", got)
	}
	var cr ContentRatings
	if err := json.Unmarshal([]byte(`{"results":[{"iso_3166_1":"DE","rating":"16"},{"iso_3166_1":"US","rating":"TV-14"},{"iso_3166_1":"GB","rating":""}]}`), &cr); err != nil {
		t.Fatal(err)
	}
	for region, want := range map[string]string{"DE": "16", "GB": "TV-14", "BR": "TV-14"} {
		if got := tvCertification(cr, region); got != want {
			t.Errorf("tvCertification(%s) = %q, want %q", region, got, want)
		}
	}
}

// The fixtures are valid JSON of the shapes the client reads (a typo in one
// would otherwise show up as a puzzling failure far away).
func TestFixturesAreValidJSON(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "tmdb", "*.json"))
	if err != nil || len(files) < 20 {
		t.Fatalf("fixtures: %d files, %v", len(files), err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(name) == "malformed.json" {
			if json.Valid(data) {
				t.Errorf("%s should be invalid JSON", name)
			}
			continue
		}
		if !json.Valid(data) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
}
