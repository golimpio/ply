package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// The MDBList key of these tests is made up.
const apiMDBKey = "goodmdblistkey-5c1e"

// fakeMDBList accepts one key. It records every key it was asked with, and what it answers to a
// wrong key quotes it, as a careless server might.
type fakeMDBList struct {
	*httptest.Server
	mu   sync.Mutex
	keys []string
}

func newFakeMDBList(t *testing.T) *fakeMDBList {
	t.Helper()
	f := &fakeMDBList{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		f.mu.Lock()
		f.keys = append(f.keys, key)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case key == apiMDBKey:
			fmt.Fprint(w, `{"api_requests": 1000, "api_requests_count": 5}`)
		case strings.HasPrefix(key, "down"):
			w.WriteHeader(502)
		default:
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error": "Invalid API key %s"}`, key)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// lastKey is the key of the latest request.
func (f *fakeMDBList) lastKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) == 0 {
		return ""
	}
	return f.keys[len(f.keys)-1]
}

func (f *fakeMDBList) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

// ratingsEnv is the settings API with an MDBList that takes apiMDBKey.
type ratingsAPI struct {
	*settingsEnv
	mdb *fakeMDBList
}

func newRatingsAPI(t *testing.T, so ...func(*metadata.SettingsOptions)) *ratingsAPI {
	t.Helper()
	mdb := newFakeMDBList(t)
	se := newSettingsEnv(t, append([]func(*metadata.SettingsOptions){func(o *metadata.SettingsOptions) { o.MDBListBaseURL = mdb.URL }}, so...)...)
	return &ratingsAPI{settingsEnv: se, mdb: mdb}
}

var fetchedAt = time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)

// seedRatings gives The Matrix the ratings of every source (but TMDB's, which is its own score).
func (r *ratingsAPI) seedRatings() {
	r.t.Helper()
	ctx := context.Background()
	if err := r.store.SetIMDbRatings(ctx, []store.IMDbTitleRating{{TitleID: r.matrixTitle, Value: 8.7, Votes: 2000000, URL: "https://www.imdb.com/title/tt0133093/"}},
		nil, fetchedAt.Unix(), nil); err != nil {
		r.t.Fatal(err)
	}
	if err := r.store.ReplaceMDBListRatings(ctx, r.matrixTitle, []store.Rating{
		{Source: store.RatingTrakt, Value: 86, Scale: 100, Votes: 70000, URL: "https://trakt.tv/movies/the-matrix-1999"},
		{Source: store.RatingLetterboxd, Value: 4.1, Scale: 5, URL: "https://letterboxd.com/film/the-matrix/"},
		{Source: store.RatingMetacritic, Value: 73, Scale: 100, Votes: 35, URL: "https://www.metacritic.com/movie/the-matrix"},
		{Source: store.RatingRTAudience, Value: 85, Scale: 100, Votes: 250000, State: store.StateUpright, URL: "https://www.rottentomatoes.com/m/matrix"},
		{Source: store.RatingRTCritics, Value: 83, Scale: 100, Votes: 150, State: store.StateFresh, URL: "https://www.rottentomatoes.com/m/matrix"},
	}, fetchedAt.Unix()); err != nil {
		r.t.Fatal(err)
	}
}

// addRated adds a movie with a file and, when imdb > 0, an IMDb rating of that value.
func (r *ratingsAPI) addRated(name string, imdb float64) (itemID, titleID int64) {
	r.t.Helper()
	ctx := context.Background()
	itemID, err := r.store.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: name, Year: 2000, Path: "/media/" + name + ".mp4", Root: "/media",
		Size: 1, MTime: 1, Container: "mp4"}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	titleID, err = r.store.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, Title: name, Year: 2000, Source: store.SourceNFO}})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.store.SetItemMatch(ctx, itemID, store.Match{TitleID: titleID, State: store.MatchMatched, Score: 1}); err != nil {
		r.t.Fatal(err)
	}
	if imdb > 0 {
		if err := r.store.SetIMDbRatings(ctx, []store.IMDbTitleRating{{TitleID: titleID, Value: imdb, Votes: 100, URL: "https://www.imdb.com/title/tt" + name + "/"}},
			nil, fetchedAt.Unix(), nil); err != nil {
			r.t.Fatal(err)
		}
	}
	return itemID, titleID
}

func (r *ratingsAPI) patch(body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.do("PATCH", "/api/settings", body)
}

// A title's ratings, as the API agreed them with the web UI: the entries, their fields, and the
// fixed order.
func TestTitleRatingsShapeAndOrder(t *testing.T) {
	r := newRatingsAPI(t)
	r.seedRatings()
	raw := decode[map[string]any](t, r.get(fmt.Sprintf("/api/items/%d", r.mp4)))
	meta := raw["meta"].(map[string]any)
	list := meta["ratings"].([]any)

	var order []string
	byName := map[string]map[string]any{}
	for _, e := range list {
		m := e.(map[string]any)
		order = append(order, m["source"].(string))
		byName[m["source"].(string)] = m
	}
	if want := []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt", "tmdb"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v (it does not depend on the order they were stored in)", order, want)
	}

	// The exact fields of each: votes and state only where there are any.
	for source, want := range map[string][]string{
		"imdb":        {"fetchedAt", "scale", "score100", "source", "url", "value", "votes"},
		"rt_critics":  {"fetchedAt", "scale", "score100", "source", "state", "url", "value", "votes"},
		"rt_audience": {"fetchedAt", "scale", "score100", "source", "state", "url", "value", "votes"},
		"metacritic":  {"fetchedAt", "scale", "score100", "source", "url", "value", "votes"},
		"letterboxd":  {"fetchedAt", "scale", "score100", "source", "url", "value"}, // no votes: the field is not there
		"trakt":       {"fetchedAt", "scale", "score100", "source", "url", "value", "votes"},
		"tmdb":        {"fetchedAt", "scale", "score100", "source", "url", "value", "votes"},
	} {
		if got := keysOf(byName[source]); !slices.Equal(got, want) {
			t.Errorf("%s has fields %v, want %v", source, got, want)
		}
	}
	type want struct {
		value, scale, score100, votes float64
		state, url                    string
	}
	for source, w := range map[string]want{
		"imdb":        {8.7, 10, 87, 2000000, "", "https://www.imdb.com/title/tt0133093/"},
		"rt_critics":  {83, 100, 83, 150, "fresh", "https://www.rottentomatoes.com/m/matrix"},
		"rt_audience": {85, 100, 85, 250000, "upright", "https://www.rottentomatoes.com/m/matrix"},
		"metacritic":  {73, 100, 73, 35, "", "https://www.metacritic.com/movie/the-matrix"},
		"letterboxd":  {4.1, 5, 82, 0, "", "https://letterboxd.com/film/the-matrix/"},
		"trakt":       {86, 100, 86, 70000, "", "https://trakt.tv/movies/the-matrix-1999"},
		"tmdb":        {8.2, 10, 82, 25000, "", "https://www.themoviedb.org/movie/603"},
	} {
		e := byName[source]
		got := want{e["value"].(float64), e["scale"].(float64), e["score100"].(float64), 0, "", e["url"].(string)}
		if v, ok := e["votes"].(float64); ok {
			got.votes = v
		}
		if s, ok := e["state"].(string); ok {
			got.state = s
		}
		if got != w {
			t.Errorf("%s = %+v, want %+v", source, got, w)
		}
		at, err := time.Parse(time.RFC3339, e["fetchedAt"].(string))
		if err != nil {
			t.Errorf("%s: fetchedAt %q is not RFC 3339: %v", source, e["fetchedAt"], err)
		} else if source != "tmdb" && !at.Equal(fetchedAt) {
			t.Errorf("%s: fetchedAt = %v, want %v", source, at, fetchedAt)
		}
	}
	// score100 is a whole number.
	for source, e := range byName {
		if s := e["score100"].(float64); s != float64(int(s)) {
			t.Errorf("%s: score100 = %v", source, s)
		}
	}

	// The same list is on the show page's title, and a title with none has an empty list, not null.
	show := decode[map[string]any](t, r.get("/api/shows/Cool%20Show"))
	if got := show["meta"].(map[string]any)["ratings"].([]any); len(got) != 1 || got[0].(map[string]any)["source"] != "tmdb" {
		t.Errorf("the show's ratings = %v, want only TMDB's", got)
	}
	_, titleID := r.addRated("Blank", 0)
	patchTitle := decode[map[string]any](t, r.do("PATCH", fmt.Sprintf("/api/titles/%d", titleID), `{"tagline": "x"}`))
	if got, ok := patchTitle["ratings"].([]any); !ok || len(got) != 0 {
		t.Errorf("a title with no ratings: %#v; want []", patchTitle["ratings"])
	}
	if v, there := patchTitle["rating"]; !there || v != nil {
		t.Errorf("a title with no ratings has rating %v (there: %v); want null", v, there)
	}
}

// A card shows the primary source's rating, or when the title has none from it, the first it has in
// the fixed order; the entry's source says which.
func TestCardRatingIsThePrimaryOrTheFirstAvailable(t *testing.T) {
	r := newRatingsAPI(t)
	r.seedRatings()
	_, onlyTMDB := r.matrixTitle, r.showTitle // the show has TMDB's score and nothing else

	source := func(path string, pick func(any) any) string {
		t.Helper()
		v := pick(decode[any](t, r.get(path)))
		if v == nil {
			return "<null>"
		}
		return v.(map[string]any)["source"].(string)
	}
	rows := func(v any) any { // the movie's row in /api/items
		for _, it := range v.([]any) {
			if it.(map[string]any)["id"] == float64(r.mp4) {
				return it.(map[string]any)["rating"]
			}
		}
		return nil
	}
	showRow := func(v any) any { return v.([]any)[0].(map[string]any)["rating"] }
	titleCard := func(v any) any {
		for _, c := range v.([]any) {
			if c.(map[string]any)["id"] == float64(r.matrixTitle) {
				return c.(map[string]any)["rating"]
			}
		}
		return nil
	}
	check := func(primary, matrix, show string) {
		t.Helper()
		// Every place a card is.
		if got := source("/api/items", rows); got != matrix {
			t.Errorf("primary %s: /api/items movie rating from %s, want %s", primary, got, matrix)
		}
		if got := source("/api/titles", titleCard); got != matrix {
			t.Errorf("primary %s: /api/titles rating from %s, want %s", primary, got, matrix)
		}
		if got := source(fmt.Sprintf("/api/items/%d", r.mp4), func(v any) any { return v.(map[string]any)["rating"] }); got != matrix {
			t.Errorf("primary %s: item detail rating from %s, want %s", primary, got, matrix)
		}
		if got := source(fmt.Sprintf("/api/items/%d", r.mp4), func(v any) any { return v.(map[string]any)["meta"].(map[string]any)["rating"] }); got != matrix {
			t.Errorf("primary %s: meta.rating from %s, want %s", primary, got, matrix)
		}
		if got := source(fmt.Sprintf("/api/collections/%d", r.collectionID), func(v any) any { return v.(map[string]any)["titles"].([]any)[0].(map[string]any)["rating"] }); got != matrix {
			t.Errorf("primary %s: collection rating from %s, want %s", primary, got, matrix)
		}
		if got := source(fmt.Sprintf("/api/people/%d", r.personID), func(v any) any {
			return v.(map[string]any)["credits"].([]any)[0].(map[string]any)["title"].(map[string]any)["rating"]
		}); got != matrix {
			t.Errorf("primary %s: a person's credit rating from %s, want %s", primary, got, matrix)
		}
		if got := source("/api/shows", showRow); got != show {
			t.Errorf("primary %s: /api/shows rating from %s, want %s", primary, got, show)
		}
		if got := source("/api/shows/Cool%20Show", func(v any) any { return v.(map[string]any)["rating"] }); got != show {
			t.Errorf("primary %s: show detail rating from %s, want %s", primary, got, show)
		}
	}
	check("(default)", "imdb", "tmdb") // IMDb for the movie; the show has no IMDb rating, so TMDB's, the first it has
	for _, tc := range []struct{ primary, matrix, show string }{
		{"rt_critics", "rt_critics", "tmdb"},
		{"metacritic", "metacritic", "tmdb"},
		{"letterboxd", "letterboxd", "tmdb"},
		{"trakt", "trakt", "tmdb"},
		{"tmdb", "tmdb", "tmdb"},
		{"imdb", "imdb", "tmdb"},
	} {
		wantStatus(t, r.patch(`{"ratingsPrimary": "`+tc.primary+`"}`), 200)
		check(tc.primary, tc.matrix, tc.show)
	}

	// The movie has no Letterboxd rating: the first it has in the fixed order is IMDb's.
	if err := r.store.ReplaceMDBListRatings(context.Background(), r.matrixTitle, []store.Rating{{Source: store.RatingTrakt, Value: 86, Scale: 100}}, 1); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, r.patch(`{"ratingsPrimary": "letterboxd"}`), 200)
	check("letterboxd (title has none)", "imdb", "tmdb")
	// And with IMDb's gone too, Trakt's comes before TMDB's.
	if _, err := r.store.PruneIMDbRatings(context.Background(), fetchedAt.Unix()+1, nil); err != nil {
		t.Fatal(err)
	}
	check("letterboxd (title has trakt and tmdb)", "trakt", "tmdb")
	_ = onlyTMDB

	// A title with no rating at all has none, and says null.
	raw := decode[[]map[string]any](t, r.get("/api/items"))
	for _, it := range raw {
		if it["id"] == float64(r.multi) {
			if v, there := it["rating"]; !there || v != nil {
				t.Errorf("an unmatched movie has rating %v (there: %v)", v, there)
			}
		}
	}
}

// sort=rating and minRating, on the lists the Movies and Shows pages use.
func TestListsSortAndFilterByRating(t *testing.T) {
	r := newRatingsAPI(t)
	r.seedRatings() // The Matrix: IMDb 8.7 -> 87
	_, alpha := r.addRated("Alpha", 9.0)
	_, bravo := r.addRated("Bravo", 6.0)
	_, charlie := r.addRated("Charlie", 0) // no rating at all
	_, delta := r.addRated("Delta", 8.7)   // ties with The Matrix
	_ = []int64{alpha, bravo, charlie, delta}

	titles := func(q string) []string {
		t.Helper()
		rec := r.get("/api/titles" + q)
		wantStatus(t, rec, 200)
		var out []string
		for _, c := range decode[[]TitleCard](t, rec) {
			out = append(out, c.Title)
		}
		return out
	}
	// Positive control: unsorted, every title is there, by name; Charlie is there with no rating.
	base := titles("")
	if !slices.Equal(base, []string{"Alpha", "Bravo", "Charlie", "Cool Show Official", "Delta", "The Matrix"}) {
		t.Fatalf("control: titles = %v", base)
	}
	if got := titles("?sort=rating"); !slices.Equal(got, []string{"Alpha", "Cool Show Official", "Delta", "The Matrix", "Bravo", "Charlie"}) {
		t.Errorf("sort=rating = %v; best first, equal scores in list order, no rating last", got)
	}
	for q, want := range map[string][]string{
		"?minRating=87":              {"Alpha", "Cool Show Official", "Delta", "The Matrix"},
		"?minRating=88":              {"Alpha", "Cool Show Official"},
		"?minRating=89":              {"Alpha", "Cool Show Official"},
		"?minRating=90":              {"Alpha"},
		"?minRating=60":              {"Alpha", "Bravo", "Cool Show Official", "Delta", "The Matrix"},
		"?minRating=0":               {"Alpha", "Bravo", "Cool Show Official", "Delta", "The Matrix"}, // Charlie has none: left out whatever the minimum
		"?minRating=87.5":            {"Alpha", "Cool Show Official"},
		"?minRating=60&sort=rating":  {"Alpha", "Cool Show Official", "Delta", "The Matrix", "Bravo"},
		"?sort=rating&minRating=100": {},
	} {
		got := titles(q)
		if got == nil {
			got = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", q, got, want)
		}
	}
	if rec := r.get("/api/titles?minRating=101"); rec.Code != 400 {
		t.Errorf("minRating=101: %d", rec.Code)
	}
	// An empty answer is [], not null.
	if rec := r.get("/api/titles?minRating=100"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("an empty list = %q", rec.Body.String())
	}

	// /api/items: the rows of movies (episodes take their show's rating, so they sort with the movies).
	items := func(q string) []ItemSummary {
		t.Helper()
		rec := r.get("/api/items" + q)
		wantStatus(t, rec, 200)
		return decode[[]ItemSummary](t, rec)
	}
	baseline := items("?kind=movie")
	score := func(it ItemSummary) int {
		if it.Rating == nil {
			return -1
		}
		return it.Rating.Score100
	}
	want := slices.Clone(baseline)
	sort.SliceStable(want, func(i, j int) bool { return score(want[i]) > score(want[j]) })
	got := items("?kind=movie&sort=rating")
	if len(got) != len(baseline) || len(baseline) != 6 {
		t.Fatalf("%d rows sorted, %d unsorted (want 6: The Matrix, the unmatched Multi and the four added)", len(got), len(baseline))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Errorf("row %d: item %d, want %d (best first, ties in list order, no rating last)", i, got[i].ID, want[i].ID)
		}
	}
	if score(got[0]) != 90 || score(got[len(got)-1]) != -1 || score(got[len(got)-2]) != -1 {
		t.Errorf("scores = %v", func() (s []int) {
			for _, it := range got {
				s = append(s, score(it))
			}
			return
		}())
	}
	min87 := items("?kind=movie&minRating=87")
	if len(min87) != 3 { // Alpha 90, The Matrix 87, Delta 87
		t.Errorf("minRating=87 gave %d rows, want 3", len(min87))
	}
	for _, it := range min87 {
		if it.Rating == nil || it.Rating.Score100 < 87 {
			t.Errorf("minRating=87 kept %+v", it)
		}
	}
	// Without the parameters the list is as it was (and the unrated rows are there).
	if got := items("?kind=movie"); len(got) != len(baseline) {
		t.Errorf("%d rows", len(got))
	}

	// /api/shows.
	shows := func(q string) []ShowSummary {
		t.Helper()
		rec := r.get("/api/shows" + q)
		wantStatus(t, rec, 200)
		return decode[[]ShowSummary](t, rec)
	}
	if got := shows("?minRating=89"); len(got) != 1 || got[0].Show != "Cool Show" {
		t.Errorf("shows minRating=89 = %+v", got)
	}
	if got := shows("?minRating=90"); got == nil || len(got) != 0 {
		t.Errorf("shows minRating=90 = %#v, want []", got)
	}
	if got := shows("?sort=rating"); len(got) != 1 {
		t.Errorf("shows sort=rating = %+v", got)
	}

	// Parameters that mean nothing are refused, on every list.
	for _, path := range []string{"/api/items", "/api/shows", "/api/titles"} {
		for _, q := range []string{"?sort=name", "?sort=Rating", "?sort=-rating", "?minRating=abc", "?minRating=-1", "?minRating=101", "?minRating=NaN", "?minRating=Inf", "?minRating=1e999"} {
			if rec := r.get(path + q); rec.Code != 400 {
				t.Errorf("%s%s = %d, want 400", path, q, rec.Code)
			}
		}
	}
}

// Equal ratings keep the order of the list however long it is (a short slice sorts stably by luck).
func TestApplyRatingQueryKeepsTiesInOrder(t *testing.T) {
	type row struct {
		id     int
		rating *RatingEntry
	}
	var rows []row
	for i := range 200 {
		switch i % 4 {
		case 0:
			rows = append(rows, row{i, &RatingEntry{Score100: 90}})
		case 1:
			rows = append(rows, row{i, &RatingEntry{Score100: 50}})
		case 2:
			rows = append(rows, row{i, nil})
		default:
			rows = append(rows, row{i, &RatingEntry{Score100: 90}})
		}
	}
	got := applyRatingQuery(rows, ratingQuery{byRating: true}, func(r row) *RatingEntry { return r.rating })
	if len(got) != len(rows) {
		t.Fatalf("%d rows, want %d", len(got), len(rows))
	}
	// 90s in list order, then the 50s in list order, then the unrated in list order.
	var want []int
	for _, class := range []func(row) bool{
		func(r row) bool { return r.rating != nil && r.rating.Score100 == 90 },
		func(r row) bool { return r.rating != nil && r.rating.Score100 == 50 },
		func(r row) bool { return r.rating == nil },
	} {
		for _, r := range rows {
			if class(r) {
				want = append(want, r.id)
			}
		}
	}
	for i := range got {
		if got[i].id != want[i] {
			t.Fatalf("row %d is %d, want %d: ties must keep the list's order", i, got[i].id, want[i])
		}
	}
	// The list given is not changed by sorting it.
	if rows[0].id != 0 || rows[1].id != 1 {
		t.Error("the input was sorted in place")
	}
	// With a minimum alone the order is the list's.
	min := applyRatingQuery(rows, ratingQuery{min: 60, hasMin: true}, func(r row) *RatingEntry { return r.rating })
	if len(min) != 100 || min[0].id != 0 || min[1].id != 3 || min[2].id != 4 {
		t.Errorf("minRating alone: %d rows, first %v", len(min), min[:3])
	}
}

// Each episode of the show's answer carries its IMDb rating (and [] when it has none); a movie's row
// has no such field.
func TestEpisodeRatings(t *testing.T) {
	r := newRatingsAPI(t)
	ctx := context.Background()
	// The episode has an IMDb id (a provider gave it: the id is stored as the show is written again).
	show := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 1396, Title: "Cool Show Official", Year: 2008,
		Overview: "A show.", Rating: 8.9, Certification: "TV-MA", Status: "Ended", Poster: "tmdb:/show.jpg", Backdrop: "tmdb:/showb.jpg",
		Source: store.SourceTMDB},
		Seasons: []store.Season{{Number: 1, Name: "The First Season", Overview: "It starts.", AirDate: "2008-01-20",
			Episodes: []store.Episode{{Number: 2, Name: "Pilot Episode", Overview: "The beginning.", AirDate: "2008-01-27",
				RuntimeMin: 58, Still: "tmdb:/still2.jpg", Rating: 8.3, IMDbID: "tt0959621"}}}}}
	if _, err := r.store.UpsertTitle(ctx, show); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetIMDbRatings(ctx, nil, []store.IMDbEpisodeRating{{IMDbID: "tt0959621", Value: 8.3, Votes: 31000}}, fetchedAt.Unix(), nil); err != nil {
		t.Fatal(err)
	}
	// A second file of the show, an episode with no metadata at all.
	if _, err := r.store.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Title: "Third", Show: "Cool Show", Season: 1, Episode: 3,
		Path: "/media/Cool Show/S01E03.mkv", Root: "/media", Size: 1, MTime: 1, Container: "mkv"}, nil); err != nil {
		t.Fatal(err)
	}

	rows := decode[[]map[string]any](t, r.get("/api/items?kind=episode&show=Cool+Show"))
	if len(rows) != 2 {
		t.Fatalf("%d episodes", len(rows))
	}
	for _, row := range rows {
		ratings, ok := row["ratings"].([]any)
		if !ok {
			t.Fatalf("episode %v has no ratings list: %v", row["episode"], row)
		}
		switch row["episode"] {
		case 2.0:
			if len(ratings) != 1 {
				t.Fatalf("episode 2 ratings = %v", ratings)
			}
			e := ratings[0].(map[string]any)
			if got := keysOf(e); !slices.Equal(got, []string{"fetchedAt", "scale", "score100", "source", "url", "value", "votes"}) {
				t.Errorf("episode entry fields = %v", got)
			}
			if e["source"] != "imdb" || e["value"] != 8.3 || e["scale"] != 10.0 || e["score100"] != 83.0 || e["votes"] != 31000.0 ||
				e["url"] != "https://www.imdb.com/title/tt0959621/" || e["fetchedAt"] != fetchedAt.Format(time.RFC3339) {
				t.Errorf("episode entry = %v", e)
			}
		case 3.0:
			if len(ratings) != 0 {
				t.Errorf("episode 3 ratings = %v, want []", ratings)
			}
		default:
			t.Errorf("episode %v", row["episode"])
		}
	}
	// The detail of the episode: the same, and in episodeInfo.
	d := decode[map[string]any](t, r.get(fmt.Sprintf("/api/items/%d", r.mkv)))
	info := d["episodeInfo"].(map[string]any)
	if got := info["ratings"].([]any); len(got) != 1 || got[0].(map[string]any)["value"] != 8.3 {
		t.Errorf("episodeInfo.ratings = %v", got)
	}
	if got := d["ratings"].([]any); len(got) != 1 {
		t.Errorf("ratings = %v", got)
	}
	if info["rating"] != 0.0 && info["rating"] != nil {
		// The episode's own TMDB score is what it was: a number, not an entry.
		if _, isNum := info["rating"].(float64); !isNum {
			t.Errorf("episodeInfo.rating = %v; it stays the TMDB score as a number", info["rating"])
		}
	}
	// Movies have no such field.
	for _, row := range decode[[]map[string]any](t, r.get("/api/items?kind=movie")) {
		if _, there := row["ratings"]; there {
			t.Errorf("a movie row has ratings: %v", row)
		}
	}
	if _, there := decode[map[string]any](t, r.get(fmt.Sprintf("/api/items/%d", r.mp4)))["ratings"]; there {
		t.Error("a movie's detail has top-level ratings (they are in meta.ratings)")
	}

	// Once the episode's rating is gone, its detail has an empty list, in episodeInfo and at the top: not
	// null, and not left out.
	if n, err := r.store.PruneIMDbRatings(ctx, fetchedAt.Unix()+10, nil); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want the episode's rating", n, err)
	}
	d = decode[map[string]any](t, r.get(fmt.Sprintf("/api/items/%d", r.mkv)))
	info = d["episodeInfo"].(map[string]any)
	if got, ok := info["ratings"].([]any); !ok || len(got) != 0 {
		t.Errorf("episodeInfo.ratings = %#v, want []", info["ratings"])
	}
	if got, ok := d["ratings"].([]any); !ok || len(got) != 0 {
		t.Errorf("ratings = %#v, want []", d["ratings"])
	}
}

// ---- settings ----

func TestRatingsSettingsFields(t *testing.T) {
	r := newRatingsAPI(t)
	ctx := context.Background()
	raw := func() map[string]any { return decode[map[string]any](t, r.get("/api/settings")) }

	v := raw()
	if v["mdblistKeySet"] != false || v["mdblistKeySource"] != "" || v["ratingsPrimary"] != "imdb" || v["imdbDatasetUpdatedAt"] != nil {
		t.Errorf("settings = %v", v)
	}
	if v["imdbAttribution"] != "Information courtesy of IMDb (https://www.imdb.com). Used with permission." || v["imdbAttributionUrl"] != "https://www.imdb.com" {
		t.Errorf("IMDb's attribution is %q (%v)", v["imdbAttribution"], v["imdbAttributionUrl"])
	}
	if !strings.Contains(v["mdblistAttribution"].(string), "MDBList") || v["mdblistAttributionUrl"] != "https://mdblist.com" {
		t.Errorf("MDBList's attribution is %q (%v)", v["mdblistAttribution"], v["mdblistAttributionUrl"])
	}
	if _, there := v["mdblistKeyChecked"]; there {
		t.Error("mdblistKeyChecked in a GET")
	}

	// A key MDBList refuses: 400; one it cannot be asked about: 502; one that is no key: 400. Nothing stored.
	wantJSONError(t, r.patch(`{"mdblistKey": "wrongkey"}`), 400)
	wantJSONError(t, r.patch(`{"mdblistKey": "downkey"}`), 502)
	wantJSONError(t, r.patch(`{"mdblistKey": "has a space"}`), 400)
	if _, ok, _ := r.store.GetSetting(ctx, metadata.SettingMDBList); ok {
		t.Error("an unchecked key was stored")
	}
	select {
	case <-r.ratings.Woken():
		t.Error("the ratings schedule was woken by keys that were refused")
	default:
	}
	// A good one: stored, reported without being shown, with the check; and the schedule is told to
	// look for work (there may be titles to rate now).
	rec := r.patch(`{"mdblistKey": " ` + apiMDBKey + ` "}`)
	wantStatus(t, rec, 200)
	select {
	case <-r.ratings.Woken():
	default:
		t.Error("a new MDBList key did not wake the ratings schedule")
	}
	got := decode[SettingsView](t, rec)
	if !got.MDBListKeySet || got.MDBListKeySource != "settings" || got.MDBListKeyChecked == nil || !*got.MDBListKeyChecked {
		t.Errorf("after the key = %+v", got)
	}
	if strings.Contains(rec.Body.String(), apiMDBKey) {
		t.Error("the key is in the response")
	}
	if strings.Contains(r.get("/api/settings").Body.String(), apiMDBKey) {
		t.Error("the key is in GET /api/settings")
	}
	if strings.Contains(r.logs.String(), apiMDBKey) {
		t.Errorf("the key is in the log: %s", r.logs.String())
	}
	if v := raw(); v["mdblistKeySet"] != true || v["mdblistKeySource"] != "settings" {
		t.Errorf("settings = %v", v)
	}
	if !slices.Equal(decode[SettingsView](t, r.get("/api/settings")).RatingsAvailable, []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt"}) {
		t.Errorf("available = %v", decode[SettingsView](t, r.get("/api/settings")).RatingsAvailable)
	}
	// Removing it.
	rec = r.patch(`{"mdblistKey": ""}`)
	wantStatus(t, rec, 200)
	if got := decode[SettingsView](t, rec); got.MDBListKeySet || got.MDBListKeyChecked != nil {
		t.Errorf("after removing it = %+v", got)
	}
	if !slices.Equal(decode[SettingsView](t, r.get("/api/settings")).RatingsAvailable, []string{"imdb"}) {
		t.Error("available after the removal")
	}

	// The choices: primary and shown (an ordered list).
	rec = r.patch(`{"ratingsPrimary": "rt_critics", "ratingsShown": ["trakt", "imdb"]}`)
	wantStatus(t, rec, 200)
	got = decode[SettingsView](t, rec)
	if got.RatingsPrimary != "rt_critics" || !slices.Equal(got.RatingsShown, []string{"trakt", "imdb"}) {
		t.Errorf("after the choices = %+v", got)
	}
	wantStatus(t, r.patch(`{"ratingsShown": []}`), 200)
	if v := raw(); !reflect.DeepEqual(v["ratingsShown"], []any{}) {
		t.Errorf("an empty list = %#v, want []", v["ratingsShown"])
	}
	for name, body := range map[string]string{
		"unknown primary":     `{"ratingsPrimary": "omdb"}`,
		"audience as primary": `{"ratingsPrimary": "rt_audience"}`,
		"empty primary":       `{"ratingsPrimary": ""}`,
		"unknown shown":       `{"ratingsShown": ["imdb", "omdb"]}`,
		"duplicate shown":     `{"ratingsShown": ["imdb", "imdb"]}`,
		"wrong type":          `{"ratingsShown": "imdb"}`,
		"numbers":             `{"ratingsShown": [1, 2]}`,
		"primary wrong type":  `{"ratingsPrimary": 7}`,
	} {
		if rec := r.patch(body); rec.Code != 400 {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if v := raw(); v["ratingsPrimary"] != "rt_critics" {
		t.Errorf("a refused update changed the primary to %v", v["ratingsPrimary"])
	}
	// PUT does the same as PATCH.
	wantStatus(t, r.do("PUT", "/api/settings", `{"ratingsPrimary": "tmdb"}`), 200)
	if v := raw(); v["ratingsPrimary"] != "tmdb" {
		t.Errorf("PUT: primary = %v", v["ratingsPrimary"])
	}
	// The read-only fields are not set from here.
	before := raw()
	wantJSONError(t, r.patch(`{"imdbAttribution": "mine", "ratingsAvailable": ["tmdb"]}`), 400) // nothing it knows
	after := raw()
	if !reflect.DeepEqual(before, after) {
		t.Error("a request with only read-only fields changed the settings")
	}
	// The date of the dataset appears once it has been downloaded (the service keeps it).
	if v := raw(); v["imdbDatasetUpdatedAt"] != nil {
		t.Errorf("imdbDatasetUpdatedAt = %v before a download", v["imdbDatasetUpdatedAt"])
	}
	if err := r.store.SetSetting(ctx, "imdb_ratings_updated", fmt.Sprint(fetchedAt.Unix())); err != nil {
		t.Fatal(err)
	}
	if v := raw(); v["imdbDatasetUpdatedAt"] != fetchedAt.Format(time.RFC3339) {
		t.Errorf("imdbDatasetUpdatedAt = %v, want %s", v["imdbDatasetUpdatedAt"], fetchedAt.Format(time.RFC3339))
	}
}

func TestRatingsAvailableFollowsKeysAndThePrivacySwitch(t *testing.T) {
	avail := func(r *ratingsAPI) []string { return decode[SettingsView](t, r.get("/api/settings")).RatingsAvailable }
	r := newRatingsAPI(t, func(o *metadata.SettingsOptions) { o.MDBListKeyFlag, o.KeyFlag = apiMDBKey, "tmdbkey123" })
	if got := avail(r); !slices.Equal(got, store.RatingSources) {
		t.Errorf("both keys: %v", got)
	}
	if v := decode[SettingsView](t, r.get("/api/settings")); v.MDBListKeySource != "flag" || !v.MDBListKeySet {
		t.Errorf("the key from a flag = %+v", v)
	}
	wantStatus(t, r.patch(`{"metadataEnabled": false}`), 200)
	if got := avail(r); got == nil || len(got) != 0 {
		t.Errorf("with lookups off: %#v, want []", got)
	}
	// Without a Settings at all the page still loads, with the defaults.
	bare := newMetaEnv(t, "", func(o *Options) { o.Settings = nil })
	v := decode[SettingsView](t, bare.get("/api/settings"))
	if v.RatingsPrimary != "imdb" || !slices.Equal(v.RatingsShown, store.RatingSources) || v.RatingsAvailable == nil || v.IMDbAttribution == "" {
		t.Errorf("no settings: %+v", v)
	}
}

func TestSettingsTestMDBList(t *testing.T) {
	r := newRatingsAPI(t)
	// No key: an answer, not a failure.
	rec := r.postTest("mdblist")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || !strings.Contains(got.Error, "MDBList") || r.mdb.calls() != 0 {
		t.Errorf("no key: %+v, %d calls", got, r.mdb.calls())
	}
	// A key that works.
	wantStatus(t, r.patch(`{"mdblistKey": "`+apiMDBKey+`"}`), 200)
	n := r.mdb.calls()
	rec = r.postTest("mdblist")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); !got.OK || got.Error != "" {
		t.Errorf("a good key: %+v", got)
	}
	if r.mdb.calls() != n+1 || r.mdb.lastKey() != apiMDBKey {
		t.Errorf("the key in use was not the one tested: %q", r.mdb.lastKey())
	}
	// A key in the body is not tested, only the one in use.
	rec = r.do("POST", "/api/settings/test", `{"provider": "mdblist", "mdblistKey": "other", "key": "other"}`)
	if got := decode[SettingsTest](t, rec); !got.OK || r.mdb.lastKey() != apiMDBKey {
		t.Errorf("a key in the body was used: %+v %q", got, r.mdb.lastKey())
	}
	// A key that went bad is an answer with the reason, and not the key.
	r2 := newRatingsAPI(t, func(o *metadata.SettingsOptions) { o.MDBListKeyFlag = "revokedkey-0000" })
	rec = r2.postTest("mdblist")
	wantStatus(t, rec, 200)
	got := decode[SettingsTest](t, rec)
	if got.OK || got.Error == "" || strings.Contains(rec.Body.String(), "revokedkey-0000") {
		t.Errorf("a refused key: %+v (%s)", got, rec.Body)
	}
	// Out of reach: an answer too.
	r3 := newRatingsAPI(t, func(o *metadata.SettingsOptions) { o.MDBListKeyFlag = "downkey-0000" })
	if got := decode[SettingsTest](t, r3.postTest("mdblist")); got.OK || got.Error == "" {
		t.Errorf("MDBList out of reach: %+v", got)
	}
	// Lookups off: nothing is sent.
	r4 := newRatingsAPI(t, func(o *metadata.SettingsOptions) { o.MDBListKeyFlag, o.Disabled = apiMDBKey, true })
	if got := decode[SettingsTest](t, r4.postTest("mdblist")); got.OK || got.Error == "" || r4.mdb.calls() != 0 {
		t.Errorf("lookups off: %+v, %d calls", got, r4.mdb.calls())
	}
	// The providers it knows are three.
	for _, p := range []string{"omdb", "", "MDBLIST "} {
		code := r.postTest(p).Code
		if p == "MDBLIST " { // case and spaces do not matter, as for the others
			if code != 200 {
				t.Errorf("provider %q: %d", p, code)
			}
			continue
		}
		if code != 400 {
			t.Errorf("provider %q: %d, want 400", p, code)
		}
	}
	wantJSONError(t, r.postTest("imdb"), 400)
}

// Ratings are a part of the answers only; nothing in them lets a client reach a URL of its own.
func TestRatingLinksAreWhatWasStored(t *testing.T) {
	r := newRatingsAPI(t)
	r.seedRatings()
	d := decode[ItemDetail](t, r.get(fmt.Sprintf("/api/items/%d", r.mp4)))
	for _, e := range d.Meta.Ratings {
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme != "https" || e.URL == "" {
			t.Errorf("%s: url %q", e.Source, e.URL)
		}
	}
}

func TestPrimaryRatingPrefersRottenTomatoesWhenAvailable(t *testing.T) {
	// 1. RT critics preferred when primary is rt_critics and present
	both := []store.Rating{
		{Source: store.RatingIMDb, Value: 8.5, Scale: 10, Votes: 1000},
		{Source: store.RatingRTCritics, Value: 92, Scale: 100, Votes: 200},
		{Source: store.RatingRTAudience, Value: 88, Scale: 100, Votes: 500},
	}
	got := primaryRating(both, store.RatingRTCritics)
	if got == nil || got.Source != store.RatingRTCritics {
		t.Fatalf("expected RT critics, got %+v", got)
	}

	// 2. Audience preferred when primary is rt_critics but critics absent
	audienceOnly := []store.Rating{
		{Source: store.RatingIMDb, Value: 8.5, Scale: 10, Votes: 1000},
		{Source: store.RatingRTAudience, Value: 88, Scale: 100, Votes: 500},
	}
	gotAudience := primaryRating(audienceOnly, store.RatingRTCritics)
	if gotAudience == nil || gotAudience.Source != store.RatingRTAudience {
		t.Fatalf("expected RT audience, got %+v", gotAudience)
	}

	// 3. Fallback to IMDb when primary is rt_critics and RT absent
	imdbOnly := []store.Rating{
		{Source: store.RatingIMDb, Value: 8.5, Scale: 10, Votes: 1000},
	}
	gotIMDb := primaryRating(imdbOnly, store.RatingRTCritics)
	if gotIMDb == nil || gotIMDb.Source != store.RatingIMDb {
		t.Fatalf("expected IMDb, got %+v", gotIMDb)
	}

	// 4. Positive control for absence: non-empty returns non-nil, empty returns nil
	if primaryRating(imdbOnly, "") == nil {
		t.Fatal("imdbOnly must return a rating (positive control)")
	}
	if primaryRating(nil, "") != nil {
		t.Fatalf("expected nil for empty ratings")
	}

	// 5. Explicit primary honored
	other := []store.Rating{
		{Source: store.RatingIMDb, Value: 8.5, Scale: 10, Votes: 1000},
		{Source: store.RatingRTCritics, Value: 92, Scale: 100, Votes: 200},
		{Source: "letterboxd", Value: 4.2, Scale: 5, Votes: 50},
	}
	gotOther := primaryRating(other, "letterboxd")
	if gotOther == nil || gotOther.Source != "letterboxd" {
		t.Fatalf("expected letterboxd, got %+v", gotOther)
	}
}
