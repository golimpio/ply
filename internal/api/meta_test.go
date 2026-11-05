package api

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"ply/internal/metadata"
	"ply/internal/store"
)

func TestItemListsCarryMetadata(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	items := decode[[]ItemSummary](t, m.get("/api/items"))
	byID := map[int64]ItemSummary{}
	for _, it := range items {
		byID[it.ID] = it
	}

	movie := byID[m.mp4]
	if movie.TitleID == nil || *movie.TitleID != m.matrixTitle || movie.MatchState != "matched" || movie.NeedsReview {
		t.Errorf("movie = %+v", movie)
	}
	for field, got := range map[string]string{
		"poster":   movie.Poster,
		"backdrop": movie.Backdrop,
		"logo":     movie.Logo,
	} {
		want := map[string]string{
			"poster":   "/api/images/tmdb/w342/p603.jpg",
			"backdrop": "/api/images/tmdb/w1280/b603.jpg",
			"logo":     "/api/images/tmdb/w500/l603.png",
		}[field]
		if got != withTok(want) {
			t.Errorf("%s = %q, want %q (relative, with the token)", field, got, withTok(want))
		}
	}
	if movie.Rating == nil || *movie.Rating != 8.2 || movie.Certification != "R" || !slices.Equal(movie.Genres, []string{"Action", "Science Fiction"}) {
		t.Errorf("movie rating/certification/genres: %+v", movie)
	}

	// A movie nothing was found for says so and has no metadata.
	plain := byID[m.multi]
	if plain.TitleID != nil || plain.Poster != "" || plain.Backdrop != "" || plain.MatchState != "pending" || plain.Genres != nil || plain.Rating != nil {
		t.Errorf("unmatched movie = %+v", plain)
	}

	// An episode takes the show's title and its own episode metadata.
	ep := byID[m.mkv]
	if ep.TitleID == nil || *ep.TitleID != m.showTitle || ep.MatchState != "matched" || ep.EpisodeTitle != "Pilot Episode" ||
		ep.Overview != "The beginning." || ep.Still != withTok("/api/images/tmdb/w500/still2.jpg") ||
		ep.Poster != withTok("/api/images/tmdb/w342/show.jpg") || !slices.Equal(ep.Genres, []string{"Drama"}) {
		t.Errorf("episode = %+v", ep)
	}
	if ep.Title != "Pilot" { // the file's own title is still the title
		t.Errorf("episode title = %q", ep.Title)
	}

	// Raw JSON: absent things are absent, the new keys are named as in the spec.
	raw := decode[[]map[string]any](t, m.get("/api/items"))
	for _, r := range raw {
		if int64(r["id"].(float64)) == m.multi {
			for _, k := range []string{"titleId", "poster", "backdrop", "logo", "rating", "certification", "genres", "overview", "still"} {
				if _, there := r[k]; there {
					t.Errorf("an unmatched movie has %q", k)
				}
			}
			for _, k := range []string{"matchState", "needsReview"} {
				if _, there := r[k]; !there {
					t.Errorf("an unmatched movie lacks %q", k)
				}
			}
		}
	}

	// Without a token configured the URLs have none.
	open := newMetaEnv(t, "")
	for _, it := range decode[[]ItemSummary](t, open.get("/api/items")) {
		if it.ID == open.mp4 && it.Poster != "/api/images/tmdb/w342/p603.jpg" {
			t.Errorf("poster without a token = %q", it.Poster)
		}
	}
	// /api/continue is enriched too.
	if _, err := m.store.SetProgress(context.Background(), m.mp4, 100, 7200); err != nil {
		t.Fatal(err)
	}
	cont := decode[[]ItemSummary](t, m.get("/api/continue"))
	if len(cont) != 1 || cont[0].Poster == "" || cont[0].MatchState != "matched" {
		t.Errorf("continue = %+v", cont)
	}
}

func TestItemListsStillWorkWithoutMetadata(t *testing.T) {
	e := newEnv(t, "")
	var found bool
	for _, it := range decode[[]ItemSummary](t, e.get("/api/items")) {
		if it.TitleID != nil || it.Poster != "" || it.MatchState != "pending" {
			t.Errorf("item %d = %+v; a library with no metadata has none to show", it.ID, it)
		}
		found = true
	}
	if !found {
		t.Fatal("no items")
	}
}

func TestSidecarArtBeatsTMDBAndEmbeddedCoverComesLast(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	if err := m.store.SetItemArt(ctx, m.mp4, store.Art{Poster: "/x/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetItemEmbeddedCover(ctx, m.multi, 3); err != nil {
		t.Fatal(err)
	}
	byID := map[int64]ItemSummary{}
	read := func() {
		t.Helper()
		clear(byID)
		for _, it := range decode[[]ItemSummary](t, m.get("/api/items")) {
			byID[it.ID] = it
		}
	}
	read()
	if got, want := byID[m.mp4].Poster, fmt.Sprintf("/api/images/local/item/%d/poster", m.mp4); got != want {
		t.Errorf("poster with a sidecar = %q, want %q", got, want)
	}
	if got := byID[m.mp4].Backdrop; got != "/api/images/tmdb/w1280/b603.jpg" {
		t.Errorf("backdrop = %q; only the poster has a sidecar", got)
	}
	if got, want := byID[m.multi].Poster, fmt.Sprintf("/api/images/local/item/%d/poster", m.multi); got != want {
		t.Errorf("embedded cover = %q, want %q (nothing else to show)", got, want)
	}
	// With a TMDB poster as well, TMDB's wins over the embedded cover. (The movie
	// has the TMDB poster of The Matrix; its cover is set after the list was read once.)
	other := newMetaEnv(t, "")
	read = func() {
		t.Helper()
		clear(byID)
		for _, it := range decode[[]ItemSummary](t, other.get("/api/items")) {
			byID[it.ID] = it
		}
	}
	read()
	if got := byID[other.mp4].Poster; got != "/api/images/tmdb/w342/p603.jpg" {
		t.Fatalf("movie poster before it has a cover = %q", got)
	}
	if err := other.store.SetItemEmbeddedCover(ctx, other.mp4, 3); err != nil {
		t.Fatal(err)
	}
	read() // the cover is only in what is read after it was set
	if e, err := other.store.GetEntry(ctx, other.mp4); err != nil || !strings.HasPrefix(e.LocalPoster, store.EmbeddedPrefix) {
		t.Fatalf("movie local poster = %q, %v; want an embedded cover", e.LocalPoster, err)
	}
	if got := byID[other.mp4].Poster; got != "/api/images/tmdb/w342/p603.jpg" {
		t.Errorf("movie poster with an embedded cover = %q; TMDB's ranks above it", got)
	}
}

// Two files of one movie (versions): each shows its own sidecar poster, the
// other shows the title's.
func TestEachVersionOfAMovieShowsItsOwnSidecarPoster(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	// "Multi" becomes a second file of The Matrix.
	if err := m.store.SetItemMatch(ctx, m.multi, store.Match{TitleID: m.matrixTitle, State: store.MatchMatched}); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetItemArt(ctx, m.multi, store.Art{Poster: "/x/second.jpg", Backdrop: "/x/second-fanart.jpg"}); err != nil {
		t.Fatal(err)
	}
	byID := map[int64]ItemSummary{}
	for _, it := range decode[[]ItemSummary](t, m.get("/api/items")) {
		byID[it.ID] = it
	}
	if got, want := byID[m.multi].Poster, fmt.Sprintf("/api/images/local/item/%d/poster", m.multi); got != want {
		t.Errorf("second file poster = %q, want its own sidecar %q", got, want)
	}
	if got, want := byID[m.multi].Backdrop, fmt.Sprintf("/api/images/local/item/%d/backdrop", m.multi); got != want {
		t.Errorf("second file backdrop = %q, want %q", got, want)
	}
	if got := byID[m.mp4].Poster; got != "/api/images/tmdb/w342/p603.jpg" {
		t.Errorf("first file poster = %q; it has no sidecar, so TMDB's", got)
	}
}

func TestItemDetailCarriesTheTitleAndEpisodeMetadata(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	d := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", m.mp4)))
	if d.Meta == nil || d.Meta.ID != m.matrixTitle || d.EpisodeInfo != nil {
		t.Fatalf("movie detail meta = %+v episode info %+v", d.Meta, d.EpisodeInfo)
	}
	meta := d.Meta
	if meta.Kind != "movie" || meta.Title != "The Matrix" || meta.Year != 1999 || meta.TMDBID != 603 || meta.IMDbID != "tt0133093" ||
		meta.SortTitle != "Matrix, The" || meta.Overview != "A hacker learns." || meta.Tagline != "Wake up." ||
		meta.ReleaseDate != "1999-03-30" || meta.Status != "Released" || meta.Certification != "R" || meta.Source != "tmdb" ||
		meta.MatchState != "matched" || meta.Locked || meta.NeedsReview || meta.RuntimeMin == nil || *meta.RuntimeMin != 136 ||
		meta.VoteCount == nil || *meta.VoteCount != 25000 || meta.Rating == nil || *meta.Rating != 8.2 {
		t.Errorf("meta = %+v", meta)
	}
	if meta.ItemID == nil || *meta.ItemID != m.mp4 || meta.Logo != withTok("/api/images/tmdb/w500/l603.png") {
		t.Errorf("meta item id / logo: %+v", meta)
	}
	if len(meta.Genres) != 2 || meta.Genres[0] != (Genre{ID: 28, Name: "Action"}) {
		t.Errorf("genres = %+v", meta.Genres)
	}
	if meta.Collection == nil || meta.Collection.ID != m.collectionID || meta.Collection.Name != "The Matrix Collection" {
		t.Errorf("collection = %+v", meta.Collection)
	}
	if len(meta.Cast) != 1 || meta.Cast[0].PersonID != m.personID || meta.Cast[0].Name != "Keanu Reeves" || meta.Cast[0].Character != "Neo" ||
		meta.Cast[0].Profile != withTok("/api/images/tmdb/w185/keanu.jpg") {
		t.Errorf("cast = %+v", meta.Cast)
	}
	if len(meta.Crew) != 1 || meta.Crew[0].Job != "Director" || meta.Crew[0].Name != "Lana Wachowski" || meta.Crew[0].Profile != "" {
		t.Errorf("crew = %+v", meta.Crew)
	}

	// The episode: the show's meta, and its own.
	ed := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", m.mkv)))
	if ed.Meta == nil || ed.Meta.ID != m.showTitle || ed.Meta.Kind != "series" || ed.Meta.Show != "Cool Show" {
		t.Errorf("episode meta = %+v", ed.Meta)
	}
	if ed.EpisodeInfo == nil || ed.EpisodeInfo.Name != "Pilot Episode" || ed.EpisodeInfo.Overview != "The beginning." ||
		ed.EpisodeInfo.AirDate != "2008-01-27" || ed.EpisodeInfo.Still != withTok("/api/images/tmdb/w500/still2.jpg") ||
		ed.EpisodeInfo.Rating == nil || *ed.EpisodeInfo.Rating != 8.3 {
		t.Errorf("episode info = %+v", ed.EpisodeInfo)
	}
	if ed.Season == nil || *ed.Season != 1 || ed.Episode == nil || *ed.Episode != 2 {
		t.Errorf("the episode number is still the item's: season %v episode %v", ed.Season, ed.Episode)
	}

	// An unmatched item has neither.
	raw := decode[map[string]any](t, m.get(fmt.Sprintf("/api/items/%d", m.multi)))
	for _, k := range []string{"meta", "episodeInfo"} {
		if _, there := raw[k]; there {
			t.Errorf("an unmatched item has %q", k)
		}
	}
}

func TestShowsListAndDetail(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	list := decode[[]ShowSummary](t, m.get("/api/shows"))
	if len(list) != 1 {
		t.Fatalf("shows = %+v", list)
	}
	sh := list[0]
	if sh.Show != "Cool Show" || sh.EpisodeCount != 1 || !slices.Equal(sh.Seasons, []int{1}) {
		t.Errorf("show = %+v", sh)
	}
	if sh.TitleID == nil || *sh.TitleID != m.showTitle || sh.Poster != withTok("/api/images/tmdb/w342/show.jpg") ||
		sh.Backdrop != withTok("/api/images/tmdb/w1280/showb.jpg") || sh.Year == nil || *sh.Year != 2008 || sh.Overview != "A show." ||
		!slices.Equal(sh.Genres, []string{"Drama"}) || sh.Rating == nil || *sh.Rating != 8.9 || sh.MatchState != "matched" || sh.NeedsReview {
		t.Errorf("show metadata = %+v", sh)
	}

	d := decode[ShowDetail](t, m.get("/api/shows/Cool%20Show"))
	if d.Show != "Cool Show" || d.EpisodeCount != 1 || d.Meta == nil || d.Meta.ID != m.showTitle || d.Poster == "" || d.MatchState != "matched" {
		t.Errorf("detail = %+v", d)
	}
	if len(d.Seasons) != 1 {
		t.Fatalf("seasons = %+v", d.Seasons)
	}
	if se := d.Seasons[0]; se.Season != 1 || se.Name != "The First Season" || se.Overview != "It starts." || se.AirDate != "2008-01-20" ||
		se.Poster != withTok("/api/images/tmdb/w342/s1.jpg") || se.EpisodeCount != 1 {
		t.Errorf("season = %+v", se)
	}
	// The name is found without regard to case, and 404s are JSON.
	if d := decode[ShowDetail](t, m.get("/api/shows/cool%20show")); d.Show != "Cool Show" {
		t.Errorf("case-insensitive lookup: %+v", d)
	}
	wantJSONError(t, m.get("/api/shows/Nope"), 404)

	// A show with no metadata: its seasons have default names.
	if err := m.store.SetSeriesMatch(context.Background(), "Cool Show", store.Match{State: store.MatchManual}); err != nil {
		t.Fatal(err)
	}
	d = decode[ShowDetail](t, m.get("/api/shows/Cool%20Show"))
	if d.Meta != nil || d.TitleID != nil || d.MatchState != "manual" || d.Seasons[0].Name != "Season 1" || d.Seasons[0].Poster != "" {
		t.Errorf("unmatched detail = %+v", d)
	}
}

func TestShowNamesWithSlashesAndSpacesAreOneSegment(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	for _, name := range []string{"AC/DC: The Show", "100% Wolf", "Ünïcode & Co"} {
		it := &store.Item{Kind: store.KindEpisode, Title: "x", Show: name, Season: 1, Episode: 1,
			Path: "/media/" + strings.ReplaceAll(name, "/", "_") + ".mkv", Root: "/media", Size: 1, MTime: 1, Container: "mkv"}
		if _, err := m.store.UpsertItem(ctx, it, nil); err != nil {
			t.Fatal(err)
		}
		d := decode[ShowDetail](t, m.get("/api/shows/"+url.PathEscape(name)))
		if d.Show != name {
			t.Errorf("GET /api/shows/%s = %q", url.PathEscape(name), d.Show)
		}
	}
}

func TestPeople(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.People = map[int64]*store.Person{6384: {TMDBID: 6384, Name: "Keanu Reeves", Profile: "tmdb:/keanu.jpg", Biography: "Canadian actor.",
		Birthday: "1964-09-02", PlaceOfBirth: "Beirut, Lebanon", KnownFor: "Acting", IMDbID: "nm0000206"}}

	p := decode[Person](t, m.get(fmt.Sprintf("/api/people/%d", m.personID)))
	if p.ID != m.personID || p.Name != "Keanu Reeves" || p.Biography != "Canadian actor." || p.Birthday != "1964-09-02" ||
		p.PlaceOfBirth != "Beirut, Lebanon" || p.KnownFor != "Acting" || p.IMDbID != "nm0000206" ||
		p.Profile != withTok("/api/images/tmdb/w185/keanu.jpg") {
		t.Errorf("person = %+v", p)
	}
	if len(p.Credits) != 1 || p.Credits[0].TitleID != m.matrixTitle || p.Credits[0].Kind != "cast" || p.Credits[0].Character != "Neo" ||
		p.Credits[0].Title.Title != "The Matrix" || p.Credits[0].Title.Poster == "" {
		t.Errorf("credits = %+v", p.Credits)
	}
	// Fetched on the first visit, not on the next.
	if n := m.fake.Count("Person("); n != 1 {
		t.Errorf("%d person fetches after one visit", n)
	}
	m.get(fmt.Sprintf("/api/people/%d", m.personID))
	if n := m.fake.Count("Person("); n != 1 {
		t.Errorf("%d person fetches after two visits, want 1 (cached)", n)
	}

	// Only titles of the library are listed: once the file goes, so does the credit.
	if err := m.store.DeleteItems(context.Background(), []int64{m.mp4}); err != nil {
		t.Fatal(err)
	}
	if p := decode[Person](t, m.get(fmt.Sprintf("/api/people/%d", m.personID))); len(p.Credits) != 0 || p.Name == "" {
		t.Errorf("credits after the file went: %+v", p.Credits)
	}

	wantJSONError(t, m.get("/api/people/99999"), 404)
	wantJSONError(t, m.get("/api/people/abc"), 404)
	wantJSONError(t, m.get("/api/people/-3"), 404)

	// Search.
	m2 := newMetaEnv(t, "")
	for q, want := range map[string]int{"kea": 1, "KEANU": 1, "wach": 1, "zzz": 0, "": 0, "  ": 0, "%": 0} {
		got := decode[[]PersonCard](t, m2.get("/api/people?q="+url.QueryEscape(q)))
		if len(got) != want {
			t.Errorf("q=%q: %d people, want %d (%+v)", q, len(got), want, got)
		}
	}
	got := decode[[]PersonCard](t, m2.get("/api/people?q=kea"))
	if got[0].Name != "Keanu Reeves" || got[0].Profile != "/api/images/tmdb/w185/keanu.jpg" {
		t.Errorf("card = %+v", got[0])
	}
	wantJSONError(t, m2.get("/api/people?q="+strings.Repeat("a", maxSearchQuery+1)), 400)
}

func TestPersonPageSurvivesTMDBTrouble(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Offline()
	p := decode[Person](t, m.get(fmt.Sprintf("/api/people/%d", m.personID)))
	if p.Name != "Keanu Reeves" || p.Biography != "" || len(p.Credits) != 1 {
		t.Errorf("person with TMDB down = %+v", p)
	}
	if !strings.Contains(m.logs.String(), "person details") {
		t.Errorf("the failure was not logged: %q", m.logs.String())
	}
	// And a server without a metadata worker serves what it has.
	bare := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	if p := decode[Person](t, bare.get(fmt.Sprintf("/api/people/%d", bare.personID))); p.Name != "Keanu Reeves" {
		t.Errorf("no worker: %+v", p)
	}
}

func TestGenresTitlesAndCollections(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	genres := decode[[]GenreCount](t, m.get("/api/genres"))
	got := map[string]int{}
	for _, g := range genres {
		got[g.Name] = g.Count
	}
	if got["Action"] != 1 || got["Science Fiction"] != 1 || got["Drama"] != 1 || len(got) != 3 {
		t.Errorf("genres = %+v", genres)
	}

	titles := func(q string) []TitleCard {
		t.Helper()
		return decode[[]TitleCard](t, m.get("/api/titles"+q))
	}
	if cards := titles(fmt.Sprintf("?genre=%d", m.genreID)); len(cards) != 1 || cards[0].ID != m.matrixTitle || cards[0].Kind != "movie" ||
		cards[0].ItemID == nil || *cards[0].ItemID != m.mp4 || cards[0].Poster != withTok("/api/images/tmdb/w342/p603.jpg") {
		t.Errorf("by genre = %+v", cards)
	}
	if cards := titles(fmt.Sprintf("?person=%d", m.personID)); len(cards) != 1 || cards[0].ID != m.matrixTitle {
		t.Errorf("by person = %+v", cards)
	}
	if cards := titles(fmt.Sprintf("?collection=%d&genre=%d", m.collectionID, m.genreID)); len(cards) != 1 {
		t.Errorf("by collection and genre = %+v", cards)
	}
	if cards := titles("?genre=999999"); len(cards) != 0 {
		t.Errorf("unknown genre = %+v", cards)
	}
	if cards := titles(""); len(cards) != 2 {
		t.Errorf("all titles = %+v", cards)
	}
	if cards := titles(""); cards[0].Kind == "series" && cards[0].Show != "Cool Show" {
		t.Errorf("show card = %+v", cards[0])
	}
	for _, q := range []string{"?genre=abc", "?collection=-1", "?person=1.5", "?genre=0x10", "?person=%20x"} {
		wantJSONError(t, m.get("/api/titles"+q), 400)
	}

	c := decode[CollectionDetail](t, m.get(fmt.Sprintf("/api/collections/%d", m.collectionID)))
	if c.ID != m.collectionID || c.Name != "The Matrix Collection" || c.Overview != "Wake up." || c.Poster != withTok("/api/images/tmdb/w342/cp.jpg") ||
		len(c.Titles) != 1 || c.Titles[0].ID != m.matrixTitle {
		t.Errorf("collection = %+v", c)
	}
	wantJSONError(t, m.get("/api/collections/99999"), 404)
	wantJSONError(t, m.get("/api/collections/x"), 404)
	wantJSONError(t, m.get("/api/collections/0"), 404)

	// JSON shape: empty lists are lists.
	var raw map[string]any
	raw = decode[map[string]any](t, m.get(fmt.Sprintf("/api/items/%d", m.mp4)))["meta"].(map[string]any)
	for _, k := range []string{"genres", "cast", "crew"} {
		if _, ok := raw[k].([]any); !ok {
			t.Errorf("meta.%s = %v, want a list", k, raw[k])
		}
	}
}

func TestMatchingAFilePutAndDelete(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.Movies = map[int64]*store.TitleInput{
		603: {Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix", Year: 1999, Poster: "tmdb:/p603.jpg"}},
		27205: {Title: store.Title{Kind: store.KindMovie, TMDBID: 27205, Title: "Inception", Year: 2010, Poster: "tmdb:/inc.jpg"},
			Genres: []store.Genre{{ID: 28, Name: "Action"}}},
	}
	m.fake.FindMovies = map[string][]metadata.Candidate{"imdb_id:tt1375666": {{ID: 27205, Title: "Inception"}}}
	path := fmt.Sprintf("/api/items/%d/match", m.multi)

	// By TMDB id.
	rec := m.do("PUT", path, `{"tmdbId": 27205}`)
	wantStatus(t, rec, 200)
	d := decode[ItemDetail](t, rec)
	if d.ID != m.multi || d.Meta == nil || d.Meta.Title != "Inception" || d.MatchState != "manual" || d.NeedsReview || d.Poster == "" {
		t.Errorf("after PUT = %+v / %+v", d.ItemSummary, d.Meta)
	}
	// Re-match by IMDb id to another title.
	rec = m.do("PUT", path, `{"imdbId": "TT0133093 "}`)
	if rec.Code != 400 {
		t.Errorf("a tt0133093 with trailing space and upper case: %d %s (tt0133093 is unknown to the fake; want a clean 400)", rec.Code, rec.Body.String())
	}
	m.fake.FindMovies["imdb_id:tt0133093"] = []metadata.Candidate{{ID: 603, Title: "The Matrix"}}
	rec = m.do("PUT", path, `{"imdbId": "TT0133093 "}`)
	wantStatus(t, rec, 200)
	if d := decode[ItemDetail](t, rec); d.Meta == nil || d.Meta.TMDBID != 603 {
		t.Errorf("by imdb id = %+v", d.Meta)
	}

	// Un-match: back to the file name, and not matched again on its own.
	rec = m.do("DELETE", path, "")
	wantStatus(t, rec, 200)
	d = decode[ItemDetail](t, rec)
	if d.Meta != nil || d.TitleID != nil || d.MatchState != "manual" || d.Poster != "" {
		t.Errorf("after DELETE = %+v / %+v", d.ItemSummary, d.Meta)
	}
	m.worker.RunOnce(context.Background())
	if e, _ := m.store.GetEntry(context.Background(), m.multi); e.MatchState != store.MatchManual {
		t.Errorf("the worker matched a file the user unmatched: %+v", e.Item)
	}
}

func TestMatchRequestsAreValidated(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Movies = map[int64]*store.TitleInput{603: {Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix"}}}
	path := fmt.Sprintf("/api/items/%d/match", m.multi)
	calls := len(m.fake.Calls())

	for body, name := range map[string]string{
		`{"tmdbId": 603, "imdbId": "tt0133093"}`: "both",
		`{}`:                                     "neither",
		``:                                       "empty body",
		`not json`:                               "not JSON",
		`{"tmdbId": 0}`:                          "tmdbId 0",
		`{"tmdbId": -4}`:                         "negative tmdbId",
		`{"tmdbId": "603"}`:                      "tmdbId as a string",
		`{"tmdbId": 6.5}`:                        "fractional tmdbId",
		`{"imdbId": "133093"}`:                   "imdbId without tt",
		`{"imdbId": "tt12"}`:                     "short imdbId",
		`{"imdbId": ""}`:                         "empty imdbId",
		`{"imdbId": 133093}`:                     "imdbId as a number",
		`{"imdbId": "tt0133093x"}`:               "imdbId with junk",
	} {
		if rec := m.do("PUT", path, body); rec.Code != 400 {
			t.Errorf("%s: status %d %s, want 400", name, rec.Code, rec.Body.String())
		} else {
			wantJSONError(t, rec, 400)
		}
	}
	if len(m.fake.Calls()) != calls {
		t.Errorf("TMDB was asked about invalid requests: %q", m.fake.Calls()[calls:])
	}
	wantJSONError(t, m.do("PUT", "/api/items/99999/match", `{"tmdbId": 603}`), 404)
	wantJSONError(t, m.do("PUT", "/api/items/abc/match", `{"tmdbId": 603}`), 404)
	wantJSONError(t, m.do("PUT", fmt.Sprintf("/api/items/%d/match", m.mkv), `{"tmdbId": 603}`), 400) // an episode
	wantJSONError(t, m.do("DELETE", fmt.Sprintf("/api/items/%d/match", m.mkv), ""), 400)
	wantJSONError(t, m.do("DELETE", "/api/items/99999/match", ""), 404)
	wantJSONError(t, m.do("PUT", path, `{"tmdbId": 424242}`), 400) // TMDB has nothing there

	// Too big a body.
	wantJSONError(t, m.do("PUT", path, `{"imdbId": "`+strings.Repeat("t", maxEditBody)+`"}`), 413)

	// TMDB trouble is a 502; no key (or switched off) a 409; no worker at all a 503.
	m.fake.Offline()
	wantJSONError(t, m.do("PUT", path, `{"tmdbId": 603}`), 502)
	m.fake.Online()
	m.src.Set(nil, true)
	wantJSONError(t, m.do("PUT", path, `{"tmdbId": 603}`), 409)
	m.src.Set(m.fake, true)
	bare := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	wantJSONError(t, bare.do("PUT", path, `{"tmdbId": 603}`), 503)
	wantJSONError(t, bare.do("DELETE", path, ""), 503)
}

func TestMatchingAShow(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.Shows = map[int64]*store.TitleInput{
		1399: {Title: store.Title{Kind: store.KindSeries, TMDBID: 1399, Title: "Other Show", Year: 2011, Poster: "tmdb:/os.jpg"},
			Seasons: []store.Season{{Number: 1, Name: "Book One"}}},
	}
	m.fake.FindSeries = map[string][]metadata.Candidate{"imdb_id:tt0944947": {{ID: 1399, Title: "Other Show"}}}

	rec := m.do("PUT", "/api/shows/Cool%20Show/match", `{"tmdbId": 1399}`)
	wantStatus(t, rec, 200)
	d := decode[ShowDetail](t, rec)
	if d.Show != "Cool Show" || d.MatchState != "manual" || d.Meta == nil || d.Meta.Title != "Other Show" || d.Seasons[0].Name != "Book One" {
		t.Errorf("after PUT = %+v", d)
	}
	rec = m.do("PUT", "/api/shows/Cool%20Show/match", `{"imdbId": "tt0944947"}`)
	wantStatus(t, rec, 200)
	if got := m.fake.Count("Find("); got != 1 {
		t.Errorf("%d finds", got)
	}
	wantJSONError(t, m.do("PUT", "/api/shows/Cool%20Show/match", `{}`), 400)
	wantJSONError(t, m.do("PUT", "/api/shows/Cool%20Show/match", `{"tmdbId": 5}`), 400)
	wantJSONError(t, m.do("PUT", "/api/shows/Nope/match", `{"tmdbId": 1399}`), 404)
	wantJSONError(t, m.do("DELETE", "/api/shows/Nope/match", ""), 404)

	rec = m.do("DELETE", "/api/shows/Cool%20Show/match", "")
	wantStatus(t, rec, 200)
	if d := decode[ShowDetail](t, rec); d.Meta != nil || d.MatchState != "manual" || d.TitleID != nil {
		t.Errorf("after DELETE = %+v", d)
	}
}

func TestPatchTitle(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	path := fmt.Sprintf("/api/titles/%d", m.matrixTitle)

	rec := m.do("PATCH", path, `{"title": "  My Matrix ", "sortTitle": "Matrix, My", "year": 2000, "overview": "Mine.", "tagline": ""}`)
	wantStatus(t, rec, 200)
	d := decode[TitleDetail](t, rec)
	if d.Title != "My Matrix" || d.SortTitle != "Matrix, My" || d.Year != 2000 || d.Overview != "Mine." || d.Tagline != "" || !d.Locked {
		t.Errorf("patched = %+v", d)
	}
	// One field only leaves the others, and the title stays locked.
	d = decode[TitleDetail](t, m.do("PATCH", path, `{"overview": "Again."}`))
	if d.Title != "My Matrix" || d.Overview != "Again." || !d.Locked {
		t.Errorf("second patch = %+v", d)
	}
	if got := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", m.mp4))); got.Meta.Title != "My Matrix" || !got.Meta.Locked {
		t.Errorf("item after the patch: %+v", got.Meta)
	}

	for body, name := range map[string]string{
		`{}`:               "no fields",
		``:                 "empty body",
		`nope`:             "not JSON",
		`{"title": "   "}`: "blank title",
		`{"year": -1}`:     "negative year",
		`{"year": 3000}`:   "year too big",
		`{"year": "1999"}`: "year as a string",
		`{"title": 5}`:     "title as a number",
		`{"overview": "` + strings.Repeat("x", maxTextLen+1) + `"}`:   "overview too long",
		`{"title": "` + strings.Repeat("x", maxTitleLen+1) + `"}`:     "title too long",
		`{"tagline": "` + strings.Repeat("x", maxTaglineLen+1) + `"}`: "tagline too long",
	} {
		if rec := m.do("PATCH", path, body); rec.Code != 400 {
			t.Errorf("%s: status %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	wantJSONError(t, m.do("PATCH", "/api/titles/99999", `{"title": "x"}`), 404)
	wantJSONError(t, m.do("PATCH", "/api/titles/abc", `{"title": "x"}`), 404)
	// A title no file refers to is not in the library: not editable either.
	orphan, err := m.store.UpsertTitle(context.Background(), &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 5, Title: "Orphan"}})
	if err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, m.do("PATCH", fmt.Sprintf("/api/titles/%d", orphan), `{"title": "x"}`), 404)
	if got, _ := m.store.GetTitle(context.Background(), orphan); got.Title != "Orphan" || got.Locked {
		t.Errorf("an orphan title was edited: %+v", got)
	}
}

func TestRefreshEndpoints(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.Movies = map[int64]*store.TitleInput{603: {Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix", Overview: "Fresh."}}}
	m.fake.Shows = map[int64]*store.TitleInput{1396: {Title: store.Title{Kind: store.KindSeries, TMDBID: 1396, Title: "Cool Show Official", Overview: "Fresh show."}}}

	rec := m.do("POST", fmt.Sprintf("/api/items/%d/metadata/refresh", m.mp4), "")
	wantStatus(t, rec, 202)
	if got := decode[map[string]bool](t, rec); !got["started"] {
		t.Errorf("body = %v", got)
	}
	wantStatus(t, m.do("POST", "/api/shows/Cool%20Show/metadata/refresh", ""), 202)
	wantStatus(t, m.do("POST", fmt.Sprintf("/api/items/%d/metadata/refresh", m.mkv), ""), 202) // an episode refreshes its show
	wantStatus(t, m.do("POST", "/api/metadata/refresh", ""), 202)
	wantJSONError(t, m.do("POST", "/api/items/99999/metadata/refresh", ""), 404)
	wantJSONError(t, m.do("POST", "/api/shows/Nope/metadata/refresh", ""), 404)

	// The asked-for refreshes happen in the worker.
	m.worker.RunOnce(context.Background())
	if m.fake.Count("Movie(603)") == 0 || m.fake.Count("Series(1396") == 0 {
		t.Errorf("the worker did not refresh: %q", m.fake.Calls())
	}
	if got, _ := m.store.GetTitle(context.Background(), m.matrixTitle); got.Overview != "Fresh." {
		t.Errorf("overview = %q", got.Overview)
	}

	bare := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	wantJSONError(t, bare.do("POST", "/api/metadata/refresh", ""), 503)
	wantJSONError(t, bare.do("POST", fmt.Sprintf("/api/items/%d/metadata/refresh", bare.mp4), ""), 503)
}

func TestStatusAndReview(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	ctx := context.Background()
	st := decode[metadata.Status](t, m.get("/api/metadata/status"))
	if st.Enabled != true || !st.KeySet || st.Running || st.Pending != 1 {
		t.Errorf("status = %+v", st)
	}
	raw := decode[map[string]any](t, m.get("/api/metadata/status"))
	for _, k := range []string{"enabled", "keySet", "running", "pending", "unmatched", "needsReview", "lastRefresh", "lastError"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("status lacks %q", k)
		}
	}
	bare := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	if st := decode[metadata.Status](t, bare.get("/api/metadata/status")); st.Enabled {
		t.Errorf("no worker: %+v", st)
	}

	// A doubtful match and an unmatched file are for review.
	if err := m.store.SetItemMatch(ctx, m.mp4, store.Match{TitleID: m.matrixTitle, State: store.MatchMatched, NeedsReview: true, Score: 0.7}); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetItemMatch(ctx, m.multi, store.Match{State: store.MatchUnmatched, Score: 0.2}); err != nil {
		t.Fatal(err)
	}
	review := decode[[]ReviewEntry](t, m.get("/api/metadata/review"))
	if len(review) != 2 {
		t.Fatalf("review = %+v", review)
	}
	if r := review[0]; r.Kind != "movie" || r.ItemID == nil || *r.ItemID != m.multi || r.Title != "Multi" || r.Year != 2019 || r.MatchState != "unmatched" || r.Candidate != nil {
		t.Errorf("first = %+v", r)
	}
	if r := review[1]; r.ItemID == nil || *r.ItemID != m.mp4 || !r.NeedsReview || r.Score != 0.7 || r.Candidate == nil || r.Candidate.ID != m.matrixTitle ||
		r.Candidate.Poster == "" {
		t.Errorf("second = %+v", r)
	}
	if st := decode[metadata.Status](t, m.get("/api/metadata/status")); st.Unmatched != 1 || st.NeedsReview != 1 {
		t.Errorf("status = %+v", st)
	}
	if got := decode[[]ReviewEntry](t, newMetaEnv(t, "").get("/api/metadata/review")); got == nil || len(got) != 0 {
		t.Errorf("empty review = %#v, want []", got)
	}
}

func TestMetadataSearch(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.MovieSearch = map[string][]metadata.Candidate{"the matrix": {
		{ID: 603, Title: "The Matrix", OriginalTitle: "The Matrix", Year: 1999, Overview: "A hacker.", Poster: "tmdb:/p603.jpg", Popularity: 80},
		{ID: 604, Title: "The Matrix Reloaded", Year: 2003, Popularity: 40},
	}}
	m.fake.SeriesSearch = map[string][]metadata.Candidate{"cool show": {{ID: 1396, Title: "Cool Show", Year: 2008, Popularity: 9}}}

	got := decode[[]Candidate](t, m.get("/api/metadata/search?kind=movie&q=The+Matrix&year=1999"))
	if len(got) != 2 || got[0].TMDBID != 603 || got[0].Title != "The Matrix" || got[0].Year != 1999 || got[0].Overview != "A hacker." ||
		got[0].Poster != withTok("/api/images/tmdb/w185/p603.jpg") || got[0].Score != 1 || got[1].TMDBID != 604 || got[1].Score >= got[0].Score {
		t.Errorf("movie search = %+v", got)
	}
	if got := decode[[]Candidate](t, m.get("/api/metadata/search?kind=series&q=Cool+Show")); len(got) != 1 || got[0].TMDBID != 1396 {
		t.Errorf("series search = %+v", got)
	}
	if got := decode[[]Candidate](t, m.get("/api/metadata/search?kind=movie&q=nothing+like+it")); got == nil || len(got) != 0 {
		t.Errorf("no results = %#v, want []", got)
	}

	for _, q := range []string{"", "?q=x", "?kind=episode&q=x", "?kind=movie", "?kind=movie&q=%20", "?kind=movie&q=x&year=abc",
		"?kind=movie&q=x&year=-5", "?kind=movie&q=x&year=99999", "?kind=Movie&q=x", "?kind=movie&q=" + strings.Repeat("x", maxSearchQuery+1)} {
		wantJSONError(t, m.get("/api/metadata/search"+q), 400)
	}
	m.fake.Offline()
	wantJSONError(t, m.get("/api/metadata/search?kind=movie&q=x"), 502)
	m.fake.Online()
	m.src.Set(nil, true)
	wantJSONError(t, m.get("/api/metadata/search?kind=movie&q=x"), 409)
	bare := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	wantJSONError(t, bare.get("/api/metadata/search?kind=movie&q=x"), 503)
}

func TestScanStatusReportsWatching(t *testing.T) {
	e := newEnv(t, "")
	raw := decode[map[string]any](t, e.get("/api/scan"))
	if w, ok := raw["watching"].([]any); !ok || len(w) != 0 {
		t.Errorf("watching without a watcher = %#v, want []", raw["watching"])
	}
	srv := New(Options{Store: e.store, Scanner: e.scanner, Watcher: stubWatcher{}})
	rec := httptestGet(srv, "/api/scan")
	raw = decode[map[string]any](t, rec)
	w, ok := raw["watching"].([]any)
	if !ok || len(w) != 2 {
		t.Fatalf("watching = %#v", raw["watching"])
	}
	first := w[0].(map[string]any)
	if first["root"] != "/media/a" || first["watching"] != true || first["dirs"] != float64(12) {
		t.Errorf("first root = %v", first)
	}
	if second := w[1].(map[string]any); second["watching"] != false || second["error"] != "root not found" {
		t.Errorf("second root = %v", second)
	}
	// The scanner's own fields are still there.
	for _, k := range []string{"running", "lastStarted", "lastFinished", "lastError", "itemCount"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("scan status lacks %q", k)
		}
	}
}
