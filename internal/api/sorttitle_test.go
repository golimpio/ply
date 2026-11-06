package api

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ply/internal/metadata"
	"ply/internal/store"
)

// asciiLower is SQLite's LOWER(), which orders GET /api/titles: ASCII letters only.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

var sortNames = []string{"The Matrix", "A Quiet Place", "An Education", "Amélie", "Anna", "Theatre", "the godfather", "Zodiac", "Alien",
	"12 Angry Men", "1917", "Se7en", "The  Double  Space", "A", "An", "The"}

// sortTitle is on the rows of the lists, and it is the key the server itself orders GET /api/titles
// by: sorting the movies of /api/items by it, with a title for each, gives the order of /api/titles.
func TestSortTitleOnListRowsIsTheKeyOfTheServersOrder(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	idOf := map[string]int64{} // name -> item
	for i, name := range sortNames {
		tid, err := m.store.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: int64(9000 + i), Title: name,
			SortTitle: metadata.SortTitle(name), Year: 2000, Source: store.SourceTMDB}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: name, Year: 2000, Path: fmt.Sprintf("/m/%d.mkv", i), Root: "/m"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.store.SetItemMatch(ctx, id, store.Match{TitleID: tid, State: store.MatchMatched, Score: 1}); err != nil {
			t.Fatal(err)
		}
		idOf[name] = id
	}

	// What the server gives: /api/titles in its own order, with the key of each card.
	cards := decode[[]TitleCard](t, m.get("/api/titles"))
	var movies []TitleCard
	for _, c := range cards {
		if c.Kind == store.KindMovie && c.ID != m.matrixTitle { // the fixture's own Matrix has the year 1999 and the item "Movie"
			movies = append(movies, c)
		}
	}
	if len(movies) != len(sortNames) {
		t.Fatalf("%d movie cards, want %d", len(movies), len(sortNames))
	}
	want := map[string]string{
		"The Matrix": "Matrix, The", "A Quiet Place": "Quiet Place, A", "An Education": "Education, An", "Amélie": "Amélie", "Anna": "Anna",
		"Theatre": "Theatre", "the godfather": "godfather, the", "Zodiac": "Zodiac", "Alien": "Alien", "12 Angry Men": "12 Angry Men",
		"1917": "1917", "Se7en": "Se7en", "The  Double  Space": "Double  Space, The", "A": "A", "An": "An", "The": "The",
	}
	for _, c := range movies {
		if c.SortTitle != want[c.Title] {
			t.Errorf("card %q: sortTitle %q, want %q", c.Title, c.SortTitle, want[c.Title])
		}
	}
	key := func(sortTitle string, year int, id int64) string {
		return fmt.Sprintf("%s\x00%04d\x00%020d", asciiLower(sortTitle), year, id)
	}
	if !slices.IsSortedFunc(cards, func(a, b TitleCard) int {
		return cmp.Compare(key(a.SortTitle, a.Year, a.ID), key(b.SortTitle, b.Year, b.ID))
	}) {
		t.Error("GET /api/titles is not in the order of sortTitle, year, id")
	}

	// The rows of /api/items carry the same key, so that a client that sorts them by it
	// puts the movies in the order the server put the cards.
	rows := decode[[]ItemSummary](t, m.get("/api/items?kind=movie"))
	byName := map[string]ItemSummary{}
	for _, r := range rows {
		byName[r.Title] = r
	}
	for _, c := range movies {
		r, ok := byName[c.Title]
		if !ok {
			t.Fatalf("no row for %q", c.Title)
		}
		if r.SortTitle != c.SortTitle {
			t.Errorf("row %q: sortTitle %q, but its card says %q", r.Title, r.SortTitle, c.SortTitle)
		}
	}
	rowKey := func(c TitleCard) string {
		r := byName[c.Title]
		year := 0
		if r.Year != nil {
			year = *r.Year
		}
		return key(r.SortTitle, year, r.ID)
	}
	sorted := slices.Clone(movies)
	slices.SortStableFunc(sorted, func(a, b TitleCard) int { return cmp.Compare(rowKey(a), rowKey(b)) })
	for i := range movies {
		if movies[i].ID != sorted[i].ID {
			t.Fatalf("sorting the rows of /api/items by sortTitle puts %q at %d, where the server put %q", sorted[i].Title, i, movies[i].Title)
		}
	}
	// The control: the plain titles would not be in that order (the articles are what is tested).
	byPlain := slices.Clone(movies)
	slices.SortStableFunc(byPlain, func(a, b TitleCard) int { return strings.Compare(asciiLower(a.Title), asciiLower(b.Title)) })
	if slices.EqualFunc(movies, byPlain, func(a, b TitleCard) bool { return a.ID == b.ID }) {
		t.Error("control: the order of the titles is the order of the plain names")
	}
}

// A movie that no title is matched to, and a show likewise, are sorted as the server sorts a
// title: with the article at the end. The function is the metadata package's.
func TestSortTitleOfRowsWithoutATitleIsMadeTheWayTitlesAre(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	for i, name := range sortNames {
		if _, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: name, Path: fmt.Sprintf("/u/%d.mkv", i), Root: "/u"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	shows := []string{"The Wire", "A Very Long Show", "an Anthology", "Cool Show"}
	for i, name := range shows {
		if _, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Show: name, Season: 1, Episode: 1, Path: fmt.Sprintf("/u/s%d.mkv", i), Root: "/u"}, nil); err != nil {
			t.Fatal(err)
		}
	}

	items := decode[[]ItemSummary](t, m.get("/api/items"))
	seenMovies := 0
	for _, it := range items {
		switch {
		case it.Kind == store.KindEpisode:
			if it.SortTitle != "" {
				t.Errorf("episode %q (%s) has a sortTitle %q: it is sorted by show, season and number", it.EpisodeTitle, it.Show, it.SortTitle)
			}
		case it.TitleID != nil:
			if it.Title == "Movie" && it.SortTitle != "Matrix, The" {
				t.Errorf("the fixture's film, matched to The Matrix: sortTitle %q", it.SortTitle)
			}
		default:
			seenMovies++
			if want := metadata.SortTitle(it.Title); it.SortTitle != want {
				t.Errorf("unmatched movie %q: sortTitle %q, want %q", it.Title, it.SortTitle, want)
			}
		}
	}
	if seenMovies != len(sortNames)+1 { // and the fixture's "Multi"
		t.Fatalf("saw %d unmatched movies, want %d", seenMovies, len(sortNames)+1)
	}
	byName := map[string]string{}
	for _, it := range items {
		byName[it.Title] = it.SortTitle
	}
	for name, want := range map[string]string{"The Matrix": "Matrix, The", "A Quiet Place": "Quiet Place, A", "An Education": "Education, An", "Theatre": "Theatre",
		"the godfather": "godfather, the", "Amélie": "Amélie", "The": "The"} {
		if byName[name] != want {
			t.Errorf("%q: sortTitle %q, want %q", name, byName[name], want)
		}
	}

	rows := decode[[]ShowSummary](t, m.get("/api/shows"))
	got := map[string]string{}
	for _, sh := range rows {
		got[sh.Show] = sh.SortTitle
	}
	for name, want := range map[string]string{"The Wire": "Wire, The", "A Very Long Show": "Very Long Show, A", "an Anthology": "Anthology, an",
		"Cool Show": "Cool Show Official"} { // the last is matched: the title's own
		if got[name] != want {
			t.Errorf("show %q: sortTitle %q, want %q", name, got[name], want)
		}
	}
	if d := decode[ShowDetail](t, m.get("/api/shows/The%20Wire")); d.SortTitle != "Wire, The" {
		t.Errorf("GET /api/shows/The Wire: sortTitle %q", d.SortTitle)
	}
}

// What a person wrote as the sort title of a title is the key of its rows.
func TestSortTitleFollowsAnEditOfTheTitle(t *testing.T) {
	m := newMetaEnv(t, "")
	wantStatus(t, m.do("PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `{"sortTitle": "Neo 1"}`), 200)
	for _, it := range decode[[]ItemSummary](t, m.get("/api/items?kind=movie")) {
		if it.ID == m.mp4 && it.SortTitle != "Neo 1" {
			t.Errorf("row: sortTitle %q after the edit, want Neo 1", it.SortTitle)
		}
	}
	for _, c := range decode[[]TitleCard](t, m.get("/api/titles")) {
		if c.ID == m.matrixTitle && c.SortTitle != "Neo 1" {
			t.Errorf("card: sortTitle %q after the edit, want Neo 1", c.SortTitle)
		}
	}
}
