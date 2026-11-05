package metadata

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ply/internal/store"
)

// The shows 900001 ("Test Anime") and 900002 are made up for these tests, with the shape
// of what TVDB answers (testdata/tvdb): 900001 has two seasons of two episodes aired and
// the same four numbered through as an absolute order; the key and every id in them are
// invented.

// fakeTestAnime teaches the fake TVDB the show 900001 in its aired and absolute orders and
// an empty DVD order.
func fakeTestAnime(f *tvdbFake) {
	f.on("/series/900001/extended", tvdbExt, "series_900001_extended.json")
	f.onTotal("/series/900001/episodes/official/eng", params("page", "0"), "episodes_900001_official_eng.json", 4)
	f.onTotal("/series/900001/episodes/absolute/eng", params("page", "0"), "episodes_900001_absolute_eng.json", 4)
	f.onTotal("/series/900001/episodes/dvd/eng", params("page", "0"), "episodes_900001_dvd_eng.json", 0)
}

func testTVDBProvider(t *testing.T, f *tvdbFake) *TVDBProvider {
	t.Helper()
	return NewTVDBProvider(testTVDBClient(t, f))
}

func TestTVDBProviderMapsAShow(t *testing.T) {
	f := newTVDBFake(t)
	fakeTestAnime(f)
	in, err := testTVDBProvider(t, f).Series(t.Context(), 900001, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/login", "/series/900001/extended?meta=translations&short=false", "/series/900001/episodes/official/eng?page=0"}; !slices.Equal(f.asked(), want) {
		t.Errorf("requests:\n got %q\nwant %q", f.asked(), want)
	}
	tt := in.Title
	if tt.Kind != store.KindSeries || tt.Source != store.SourceTVDB || tt.TVDBID != 900001 || tt.TMDBID != 900101 || tt.IMDbID != "tt9000001" ||
		tt.Title != "Test Anime" || tt.OriginalTitle != "Test Anime" || tt.SortTitle != "Test Anime" || tt.Year != 2020 || tt.ReleaseDate != "2020-04-01" ||
		tt.EndDate != "2026-12-01" || tt.Status != "Continuing" || tt.RuntimeMin != 24 || tt.Certification != "TV-14" || tt.EpisodeOrder != store.OrderAired ||
		tt.Rating != 0 || !strings.HasPrefix(tt.Overview, "A fictional series for ply's tests.") {
		t.Errorf("title = %+v", tt)
	}
	// English artwork beats the Japanese poster that scores higher; the background has no language.
	if tt.Poster != "tvdb:/banners/posters/900001-1.jpg" || tt.Backdrop != "tvdb:/banners/fanart/original/900001-1.jpg" || tt.Logo != "tvdb:/banners/clearlogo/900001.png" {
		t.Errorf("artwork = %q %q %q", tt.Poster, tt.Backdrop, tt.Logo)
	}
	// TVDB's genre ids are its own: none may reach the store, where they would rename TMDB's.
	if len(in.Genres) != 2 || in.Genres[0] != (store.Genre{Name: "Drama"}) || in.Genres[1] != (store.Genre{Name: "Anime"}) {
		t.Errorf("genres = %+v", in.Genres)
	}
	if tt.NextAirDate != "2026-12-01" || tt.NextEpisodeSeason != 2 || tt.NextEpisodeNumber != 2 || tt.NextEpisodeName != "Reckoning" {
		t.Errorf("next episode = %q S%dE%d %q", tt.NextAirDate, tt.NextEpisodeSeason, tt.NextEpisodeNumber, tt.NextEpisodeName)
	}

	// Actors in TVDB's order, then the crew by importance; a kind of crew ply does not keep is left out.
	type c struct {
		kind, job, name, char string
		tvdb                  int64
	}
	var got []c
	for _, cr := range in.Credits {
		got = append(got, c{cr.Kind, cr.Job, cr.Person.Name, cr.Character, cr.Person.TVDBID})
		if cr.Person.TMDBID != 0 {
			t.Errorf("credit of %s has a TMDB id: only a person's own record can say it", cr.Person.Name)
		}
	}
	want := []c{{store.CreditCast, "", "Tess Voice", "Sidekick", 777001}, {store.CreditCast, "", "Bryan Cranston", "Hero", 296140},
		{store.CreditCrew, "Creator", "Casey Creator", "", 777003}, {store.CreditCrew, "Director", "Dana Director", "", 777002}}
	if !slices.Equal(got, want) {
		t.Errorf("credits:\n got %+v\nwant %+v", got, want)
	}
	if in.Credits[1].Person.Profile != "tvdb:/banners/v4/actor/296140/photo/bryan.jpg" || in.Credits[0].Person.Profile != "" {
		t.Errorf("photos = %q, %q", in.Credits[1].Person.Profile, in.Credits[0].Person.Profile)
	}

	// Every season comes at once, with its poster from the aired seasons, and replaces what was stored.
	if !in.ResetSeasons || len(in.Seasons) != 2 {
		t.Fatalf("seasons = %+v, reset %v", in.Seasons, in.ResetSeasons)
	}
	s1, s2 := in.Seasons[0], in.Seasons[1]
	if s1.Number != 1 || s1.Poster != "tvdb:/banners/seasons/900001-1.jpg" || s1.AirDate != "2020-04-01" || len(s1.Episodes) != 2 ||
		s2.Number != 2 || s2.Poster != "tvdb:/banners/seasons/900001-2.jpg" || len(s2.Episodes) != 2 {
		t.Errorf("seasons = %+v", in.Seasons)
	}
	if e := s2.Episodes[0]; e != (store.Episode{Season: 2, Number: 1, Name: "Return", Overview: "Return (overview).", AirDate: "2021-04-01", RuntimeMin: 24,
		Still: "tvdb:/banners/episodes/900001/9000003.jpg"}) {
		t.Errorf("S02E01 = %+v", e)
	}
}

// A show is mapped in the order asked for: aired is S01E01..S02E02, absolute is one season
// numbered through, and a show without the order asked for is an error and not an empty show.
func TestTVDBProviderEpisodeOrders(t *testing.T) {
	f := newTVDBFake(t)
	fakeTestAnime(f)
	p := testTVDBProvider(t, f)
	ctx := t.Context()

	numbers := func(in *store.TitleInput) []string {
		var out []string
		for _, se := range in.Seasons {
			for _, e := range se.Episodes {
				out = append(out, strings.Join([]string{strconv.Itoa(e.Season), strconv.Itoa(e.Number), e.Name}, "/"))
			}
		}
		return out
	}
	aired, err := p.Series(ctx, 900001, store.OrderAired, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1/1/Awakening", "1/2/The Road", "2/1/Return", "2/2/Reckoning"}; !slices.Equal(numbers(aired), want) || aired.EpisodeOrder != "aired" {
		t.Errorf("aired = %q (%s)", numbers(aired), aired.EpisodeOrder)
	}
	abs, err := p.Series(ctx, 900001, store.OrderAbsolute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1/1/Awakening", "1/2/The Road", "1/3/Return", "1/4/Reckoning"}; !slices.Equal(numbers(abs), want) || abs.EpisodeOrder != "absolute" || len(abs.Seasons) != 1 {
		t.Errorf("absolute = %q (%s, %d seasons)", numbers(abs), abs.EpisodeOrder, len(abs.Seasons))
	}
	if abs.Seasons[0].Poster != "tvdb:/banners/seasons/900001-abs.jpg" {
		t.Errorf("absolute season poster = %q: the poster is the absolute season's", abs.Seasons[0].Poster)
	}
	if !slices.Contains(f.asked(), "/series/900001/episodes/absolute/eng?page=0") {
		t.Errorf("the absolute order was not asked for: %q", f.asked())
	}
	// The next episode is in the numbers of the order.
	if aired.NextEpisodeSeason != 2 || aired.NextEpisodeNumber != 2 || abs.NextEpisodeSeason != 1 || abs.NextEpisodeNumber != 4 {
		t.Errorf("next episode: aired S%dE%d, absolute S%dE%d", aired.NextEpisodeSeason, aired.NextEpisodeNumber, abs.NextEpisodeSeason, abs.NextEpisodeNumber)
	}

	// The absolute order puts a season-0 special where it was; absolute numbers win over the
	// numbers of the list when TVDB gives both.
	special := &TVDBSeries{ID: 1, Name: "S"}
	in := tvdbInput(special, []TVDBEpisode{
		{SeasonNumber: 0, Number: 1, Name: "Special"},
		{SeasonNumber: 3, Number: 2, AbsoluteNumber: 27, Name: "Late"},
		{SeasonNumber: 3, Number: 0, AbsoluteNumber: 0, Name: "No number"},
	}, store.OrderAbsolute, "absolute", "en-US")
	if got := numbers(in); !slices.Equal(got, []string{"0/1/Special", "1/27/Late"}) {
		t.Errorf("absolute mapping of specials and numbers = %q", got)
	}

	// DVD order: TVDB has none for this show.
	if got, err := p.Series(ctx, 900001, store.OrderDVD, nil); !errors.Is(err, ErrNoSuchOrder) || got != nil {
		t.Errorf("a show without a DVD order = %v, %v; want ErrNoSuchOrder", got, err)
	}
	if _, err := p.Series(ctx, 900001, "random", nil); err == nil {
		t.Error("an unknown order was accepted")
	}
}

func TestTVDBProviderSearchFindAndPerson(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/search", params("query", "Breaking Bad", "type", "series"), "search_breaking_bad.json")
	f.on("/search", params("query", "breaking bad", "type", "series"), "search_breaking_bad.json")
	f.on("/search", params("query", "breaking", "type", "series"), "search_breaking_bad.json")
	f.on("/search/remoteid/tt0903747", "", "remoteid_tt0903747.json")
	f.on("/people/296140/extended", "", "person_296140.json")
	f.on("/series/81189/extended", tvdbExt, "series_81189_extended.json")
	p := testTVDBProvider(t, f)
	ctx := t.Context()

	cands, err := p.SearchSeries(ctx, "Breaking Bad", 0)
	if err != nil || len(cands) == 0 {
		t.Fatalf("SearchSeries = %+v, %v", cands, err)
	}
	c := cands[0]
	if c.ID != 81189 || c.Source != SourceTVDB || c.Title != "Breaking Bad" || c.Year != 2008 || c.Poster != "tvdb:/banners/posters/81189-10.jpg" ||
		!strings.HasPrefix(c.Overview, "When Walter White") || c.Popularity <= 0 {
		t.Errorf("first candidate = %+v", c)
	}
	if len(cands) > 1 && cands[0].Popularity <= cands[1].Popularity {
		t.Errorf("relevance order is not kept in Popularity: %v then %v", cands[0].Popularity, cands[1].Popularity)
	}
	if m, err := p.SearchMovies(ctx, "anything", 0); m != nil || err != nil {
		t.Errorf("TVDB has no movies: %v, %v", m, err)
	}

	movies, series, err := p.Find(ctx, ExternalIMDb, "tt0903747")
	if err != nil || len(movies) != 0 || len(series) == 0 || series[0].ID != 81189 || series[0].Source != SourceTVDB {
		t.Errorf("Find by IMDb id = %+v %+v, %v", movies, series, err)
	}
	before := len(f.requests())
	if _, series, err := p.Find(ctx, ExternalTVDB, "81189"); err != nil || len(series) != 1 || series[0].ID != 81189 {
		t.Errorf("Find by TVDB id = %+v, %v", series, err)
	}
	if len(f.requests()) != before {
		t.Error("a TVDB id was looked up: it names the show by itself")
	}

	// The slug of an address is found by an exact slug among the search results.
	id, err := p.SeriesBySlug(ctx, "breaking-bad")
	if err != nil || id != 81189 {
		t.Errorf("SeriesBySlug(breaking-bad) = %d, %v", id, err)
	}
	if id, err := p.SeriesBySlug(ctx, "breaking"); !errors.Is(err, ErrNoSuchTitle) || id != 0 {
		t.Errorf("a slug that is a prefix of one = %d, %v; it must be exact", id, err)
	}

	br, err := p.Brief(ctx, 81189)
	if err != nil || br.ID != 81189 || br.Title != "Breaking Bad" || br.Year != 2008 {
		t.Errorf("Brief = %+v, %v", br, err)
	}

	per, err := p.Person(ctx, 296140)
	if err != nil {
		t.Fatal(err)
	}
	if per.TVDBID != 296140 || per.TMDBID != 17419 || per.IMDbID != "nm0186505" || per.Name != "Bryan Cranston" || per.Birthday != "1956-03-07" ||
		per.PlaceOfBirth != "Hollywood, California, USA" || per.Biography != "Bryan Lee Cranston is an American actor." ||
		per.Profile != "tvdb:/banners/v4/actor/296140/photo/bryan.jpg" {
		t.Errorf("person = %+v", per)
	}
	if _, err := p.Movie(ctx, 1); !IsNotFound(err) {
		t.Errorf("Movie at TVDB = %v, want not found", err)
	}
	if got := p.Name(); got != SourceTVDB {
		t.Errorf("Name = %q", got)
	}
}

func TestTVDBProviderChangesAreTheUpdatesFeed(t *testing.T) {
	f := newTVDBFake(t)
	for page, file := range []string{"updates_p0.json", "updates_p1.json"} {
		f.on("/updates", params("since", "1793792133", "type", "series", "action", "update", "page", strconv.Itoa(page)), file)
	}
	p := testTVDBProvider(t, f)
	since := time.Unix(1793792133, 0)
	ids, err := p.Changes(t.Context(), store.KindSeries, since, time.Unix(1793792999, 0))
	if err != nil || !slices.Equal(ids, []int64{255390, 274590, 484780, 484789, 484779, 465020}) {
		t.Errorf("Changes(series) = %v, %v", ids, err)
	}
	if ids, err := p.Changes(t.Context(), store.KindMovie, since, since); ids != nil || err != nil {
		t.Errorf("Changes(movie) = %v, %v: TVDB has no movies", ids, err)
	}
}

func TestTVDBImageRefsAndFiles(t *testing.T) {
	for _, tc := range []struct {
		in, ref string
	}{
		{"https://artworks.thetvdb.com/banners/posters/81189-10.jpg", "tvdb:/banners/posters/81189-10.jpg"},
		{"/banners/episodes/81189/3859781.jpg", "tvdb:/banners/episodes/81189/3859781.jpg"},
		{"banners/posters/1-1.png", "tvdb:/banners/posters/1-1.png"},
		{"https://evil.example/banners/x.jpg", ""},
		{"https://artworks.thetvdb.com.evil.example/x.jpg", ""},
		{"/banners/../etc/passwd", ""},
		{"", ""},
	} {
		if got := TVDBImageRef(tc.in); got != tc.ref {
			t.Errorf("TVDBImageRef(%q) = %q, want %q", tc.in, got, tc.ref)
		}
	}
	for _, tc := range []struct {
		ref  string
		file string
		ok   bool
	}{
		{"tvdb:/banners/posters/81189-10.jpg", "/banners/posters/81189-10.jpg", true},
		{"tvdb:/banners/v4/actor/296140/photo/61138b8f549b0.JPG", "/banners/v4/actor/296140/photo/61138b8f549b0.JPG", true},
		{"tmdb:/abc.jpg", "", false},
		{"tvdb:banners/x.jpg", "", false},
		{"tvdb:/banners/x.svg", "", false}, // not a picture TVDB serves
		{"tvdb:/banners/../x.jpg", "", false},
		{"tvdb:/banners/x.jpg?a=b", "", false},
		{"tvdb://evil.example/x.jpg", "", false},
	} {
		if file, ok := TVDBFile(tc.ref); file != tc.file || ok != tc.ok {
			t.Errorf("TVDBFile(%q) = %q, %v; want %q, %v", tc.ref, file, ok, tc.file, tc.ok)
		}
	}
	for _, tc := range []struct {
		size, file string
		ok         bool
	}{
		{"w342", "/banners/posters/1-1.jpg", true},
		{"w342", "banners/posters/1-1.jpg", true},
		{"original", "/banners/posters/1-1.png", true},
		{"w999", "/banners/posters/1-1.jpg", false},
		{"w342", "/banners/posters/1-1", false},
		{"w342", "/banners/%2e%2e/x.jpg", false},
		{"w342", "/banners/a b.jpg", false},
		{"w342", "", false},
	} {
		if got := ValidTVDBImage(tc.size, tc.file); got != tc.ok {
			t.Errorf("ValidTVDBImage(%q, %q) = %v, want %v", tc.size, tc.file, got, tc.ok)
		}
	}
	// The address of a size: the thumbnail up to w342, the original above.
	p := &TVDBProvider{}
	for size, want := range map[string]string{
		"w92": "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg", "w342": "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg",
		"w500": "https://artworks.thetvdb.com/banners/posters/1-1.jpg", "original": "https://artworks.thetvdb.com/banners/posters/1-1.jpg",
		"w1": "",
	} {
		if got := p.ImageURL(size, "/banners/posters/1-1.jpg"); got != want {
			t.Errorf("ImageURL(%s) = %q, want %q", size, got, want)
		}
	}
}

// A show with little in its record: no artworks, no characters, no seasons list, no TMDB id,
// nothing to air. The poster is the record's own image; nothing is invented.
func TestTVDBProviderMapsAShowWithLittleInIt(t *testing.T) {
	f := newTVDBFake(t)
	f.on("/series/900002/extended", tvdbExt, "series_900002_extended.json")
	f.onTotal("/series/900002/episodes/official/eng", params("page", "0"), "episodes_900002_official_eng.json", 2)
	in, err := testTVDBProvider(t, f).Series(t.Context(), 900002, store.OrderAired, nil)
	if err != nil {
		t.Fatal(err)
	}
	tt := in.Title
	if tt.TVDBID != 900002 || tt.TMDBID != 0 || tt.IMDbID != "tt9000002" || tt.Status != "Ended" || tt.Year != 2018 || tt.Poster != "tvdb:/banners/posters/900002-1.jpg" ||
		tt.Backdrop != "" || tt.Logo != "" || tt.Certification != "" || tt.NextAirDate != "" || tt.NextEpisodeNumber != 0 {
		t.Errorf("title = %+v", tt)
	}
	if len(in.Credits) != 0 || len(in.Genres) != 0 || len(in.Seasons) != 1 || len(in.Seasons[0].Episodes) != 2 || in.Seasons[0].Poster != "" {
		t.Errorf("credits %d, genres %d, seasons %+v", len(in.Credits), len(in.Genres), in.Seasons)
	}
}
