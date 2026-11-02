package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// tmdbMovie is a movie title with the fields a TMDB fetch fills.
func tmdbMovie(tmdb int64, title string, year int) *TitleInput {
	return &TitleInput{Title: Title{
		Kind: KindMovie, TMDBID: tmdb, IMDbID: fmt.Sprintf("tt%07d", tmdb), Title: title,
		OriginalTitle: title, SortTitle: title, Year: year, Overview: "About " + title,
		Tagline: "Tag " + title, ReleaseDate: fmt.Sprintf("%d-03-31", year), Status: "Released",
		RuntimeMin: 136, Rating: 8.2, VoteCount: 1000, Certification: "R",
		Poster: fmt.Sprintf("tmdb:/p%d.jpg", tmdb), Backdrop: fmt.Sprintf("tmdb:/b%d.jpg", tmdb),
		Logo: fmt.Sprintf("tmdb:/l%d.png", tmdb), Source: SourceTMDB,
	}}
}

func tmdbSeries(tmdb int64, title string, year int) *TitleInput {
	in := tmdbMovie(tmdb, title, year)
	in.Kind = KindSeries
	in.Status = "Returning Series"
	return in
}

func castCredit(tmdb int64, name, character string, order int) Credit {
	return Credit{Person: Person{TMDBID: tmdb, Name: name, Profile: fmt.Sprintf("tmdb:/pr%d.jpg", tmdb)},
		Kind: CreditCast, Character: character, Order: order}
}

func crewCredit(tmdb int64, name, job string, order int) Credit {
	return Credit{Person: Person{TMDBID: tmdb, Name: name}, Kind: CreditCrew, Job: job, Order: order}
}

// fullMatrix is a movie with every kind of related row.
func fullMatrix() *TitleInput {
	in := tmdbMovie(603, "The Matrix", 1999)
	in.SortTitle = "Matrix, The"
	in.Genres = []Genre{{ID: 28, Name: "Action"}, {ID: 878, Name: "Science Fiction"}}
	in.Collection = &Collection{TMDBID: 2344, Name: "The Matrix Collection", Overview: "Wake up.",
		Poster: "tmdb:/cp.jpg", Backdrop: "tmdb:/cb.jpg"}
	in.Credits = []Credit{
		castCredit(6384, "Keanu Reeves", "Neo", 0),
		castCredit(2975, "Laurence Fishburne", "Morpheus", 1),
		crewCredit(9339, "Lana Wachowski", "Director", 0),
		crewCredit(9340, "Lilly Wachowski", "Director", 1),
		crewCredit(9339, "Lana Wachowski", "Writer", 2),
	}
	return in
}

// addMovie indexes a movie file and links it to a new title as matched.
func addMovie(t *testing.T, s *Store, path string, in *TitleInput) (itemID, titleID int64) {
	t.Helper()
	ctx := context.Background()
	itemID, err := s.UpsertItem(ctx, movie(path, in.Title.Title), nil)
	if err != nil {
		t.Fatal(err)
	}
	titleID, err = s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemMatch(ctx, itemID, Match{TitleID: titleID, State: MatchMatched, Score: 0.95}); err != nil {
		t.Fatal(err)
	}
	return itemID, titleID
}

// addShow indexes episodes of a show and links the show to a new title.
func addShow(t *testing.T, s *Store, show string, seasons []int, in *TitleInput) int64 {
	t.Helper()
	ctx := context.Background()
	for _, season := range seasons {
		if _, err := s.UpsertItem(ctx, episode(fmt.Sprintf("/media/%s/S%02dE01.mkv", show, season), show, season, 1), nil); err != nil {
			t.Fatal(err)
		}
	}
	titleID, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, show, Match{TitleID: titleID, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	return titleID
}

func TestUpsertTitleRoundTripAndDetail(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	titleID, err := s.UpsertTitle(ctx, fullMatrix())
	if err != nil {
		t.Fatal(err)
	}
	// Not in the library until a file is linked: no detail yet (positive control below).
	if _, err := s.GetTitleDetail(ctx, titleID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTitleDetail of an unlinked title = %v, want ErrNotFound", err)
	}
	row, err := s.GetTitle(ctx, titleID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "The Matrix" || row.TMDBID != 603 || row.IMDbID != "tt0000603" || row.CollectionID == 0 ||
		row.Source != SourceTMDB || row.Locked || row.FetchedAt == 0 || row.CreatedAt == 0 || row.NextAirDate != "" {
		t.Errorf("stored title: %+v", row)
	}

	itemID, err := s.UpsertItem(ctx, movie("/media/The Matrix (1999).mkv", "The Matrix"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemMatch(ctx, itemID, Match{TitleID: titleID, State: MatchMatched, Score: 0.93}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTitleDetail(ctx, titleID)
	if err != nil {
		t.Fatal(err)
	}
	card := TitleCard{ID: titleID, Kind: KindMovie, Title: "The Matrix", Year: 1999,
		Poster: "tmdb:/p603.jpg", Backdrop: "tmdb:/b603.jpg", Rating: 8.2, ItemID: itemID}
	if d.TitleCard != card {
		t.Errorf("card = %+v, want %+v", d.TitleCard, card)
	}
	if d.TMDBID != 603 || d.IMDbID != "tt0000603" || d.SortTitle != "Matrix, The" || d.Overview != "About The Matrix" ||
		d.Tagline != "Tag The Matrix" || d.ReleaseDate != "1999-03-31" || d.RuntimeMin != 136 ||
		d.VoteCount != 1000 || d.Certification != "R" || d.Logo != "tmdb:/l603.png" || d.Source != SourceTMDB ||
		d.MatchState != MatchMatched || d.NeedsReview || d.Locked {
		t.Errorf("detail: %+v", d)
	}
	if want := []Genre{{28, "Action"}, {878, "Science Fiction"}}; !reflect.DeepEqual(d.Genres, want) {
		t.Errorf("genres = %+v, want %+v", d.Genres, want)
	}
	if d.Collection == nil || d.Collection.TMDBID != 2344 || d.Collection.Name != "The Matrix Collection" {
		t.Errorf("collection = %+v", d.Collection)
	}
	if len(d.Cast) != 2 || d.Cast[0].Name != "Keanu Reeves" || d.Cast[0].Character != "Neo" ||
		d.Cast[0].Profile != "tmdb:/pr6384.jpg" || d.Cast[1].Name != "Laurence Fishburne" || d.Cast[0].PersonID == 0 {
		t.Errorf("cast = %+v", d.Cast)
	}
	if len(d.Crew) != 3 || d.Crew[0].Name != "Lana Wachowski" || d.Crew[0].Job != "Director" ||
		d.Crew[1].Name != "Lilly Wachowski" || d.Crew[2].Job != "Writer" || d.Crew[2].PersonID != d.Crew[0].PersonID {
		t.Errorf("crew = %+v", d.Crew)
	}
	if got := count(t, s, "people"); got != 4 {
		t.Errorf("people = %d, want 4 (Lana is one person with two jobs)", got)
	}

	// Review flag: any linked file needing review shows on the detail.
	if err := s.SetItemMatch(ctx, itemID, Match{TitleID: titleID, State: MatchMatched, NeedsReview: true, Score: 0.6}); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.GetTitleDetail(ctx, titleID); !d.NeedsReview {
		t.Error("NeedsReview not reported")
	}
	if _, err := s.GetTitleDetail(ctx, titleID+99); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown title = %v, want ErrNotFound", err)
	}
}

func TestUpsertTitleIsOneTransaction(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := fullMatrix()
	in.Seasons = []Season{{Number: 1, Name: "S1", Episodes: []Episode{{Number: 1, Name: "E1"}}}}
	in.Credits = append(in.Credits, Credit{Person: Person{Name: "Nobody"}, Kind: "bogus"}) // fails last

	if _, err := s.UpsertTitle(ctx, in); err == nil {
		t.Fatal("UpsertTitle accepted a credit of kind bogus")
	}
	for _, table := range []string{"titles", "collections", "genres", "title_genres", "people", "credits", "seasons", "episodes"} {
		if n := count(t, s, table); n != 0 {
			t.Errorf("%s has %d rows after a failed upsert, want none", table, n)
		}
	}

	// Positive control: the same input without the bad credit writes all of it.
	in.Credits = in.Credits[:len(in.Credits)-1]
	if _, err := s.UpsertTitle(ctx, in); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"titles": 1, "collections": 1, "genres": 2, "title_genres": 2,
		"people": 4, "credits": 5, "seasons": 1, "episodes": 1} {
		if n := count(t, s, table); n != want {
			t.Errorf("%s has %d rows, want %d", table, n, want)
		}
	}
	if _, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: "album"}}); err == nil {
		t.Error("UpsertTitle accepted kind album")
	}
}

func TestUpsertTitleUpdatesInPlace(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := addMovie(t, s, "/media/a.mkv", fullMatrix())

	again := fullMatrix()
	again.Overview = "New overview"
	again.Rating = 9.1
	again.Genres = []Genre{{ID: 28, Name: "Action"}, {ID: 12, Name: "Adventure"}}
	again.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Thomas Anderson", 0), castCredit(1, "Joe Pantoliano", "Cypher", 1)}
	again.Collection = nil
	id2, err := s.UpsertTitle(ctx, again)
	if err != nil || id2 != id {
		t.Fatalf("second upsert = %d, %v; want id %d", id2, err, id)
	}
	if n := count(t, s, "titles"); n != 1 {
		t.Errorf("titles = %d, want 1", n)
	}
	d, err := s.GetTitleDetail(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Overview != "New overview" || d.Rating != 9.1 {
		t.Errorf("fields not refreshed: %+v", d)
	}
	if d.Collection != nil {
		t.Errorf("collection = %+v; the title no longer belongs to one", d.Collection)
	}
	if !slices.Equal(genreNames(d.Genres), []string{"Action", "Adventure"}) {
		t.Errorf("genres = %v", d.Genres)
	}
	if len(d.Cast) != 2 || d.Cast[0].Character != "Thomas Anderson" || d.Cast[1].Name != "Joe Pantoliano" {
		t.Errorf("cast = %+v", d.Cast)
	}
	if len(d.Crew) != 0 {
		t.Errorf("crew = %+v, want it replaced by none", d.Crew)
	}
	if n := count(t, s, "credits"); n != 2 {
		t.Errorf("credits = %d, want 2", n)
	}
}

func genreNames(gs []Genre) []string {
	var names []string
	for _, g := range gs {
		names = append(names, g.Name)
	}
	return names
}

func TestLockedTitleKeepsEditedTextButRefreshesTheRest(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	lockedID, _ := addMovie(t, s, "/media/a.mkv", fullMatrix())
	plainID, _ := addMovie(t, s, "/media/b.mkv", tmdbMovie(604, "Reloaded", 2003))

	newTitle, newYear := "My Matrix", 2000
	if err := s.PatchTitle(ctx, lockedID, TitlePatch{Title: &newTitle, Year: &newYear}); err != nil {
		t.Fatal(err)
	}
	if err := s.PatchTitle(ctx, lockedID+99, TitlePatch{Title: &newTitle}); !errors.Is(err, ErrNotFound) {
		t.Errorf("PatchTitle(unknown) = %v, want ErrNotFound", err)
	}
	got, _ := s.GetTitle(ctx, lockedID)
	if got.Title != "My Matrix" || got.Year != 2000 || !got.Locked || got.Overview != "About The Matrix" {
		t.Fatalf("after the edit: %+v", got)
	}

	refresh := fullMatrix()
	refresh.Overview, refresh.Tagline, refresh.SortTitle = "TMDB overview", "TMDB tagline", "TMDB sort"
	refresh.Rating, refresh.OriginalTitle, refresh.Poster = 9.9, "Original", "tmdb:/new.jpg"
	refresh.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Neo", 0)}
	if _, err := s.UpsertTitle(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTitle(ctx, lockedID)
	if got.Title != "My Matrix" || got.Year != 2000 || got.Overview != "About The Matrix" ||
		got.Tagline != "Tag The Matrix" || got.SortTitle != "Matrix, The" || !got.Locked {
		t.Errorf("locked text was overwritten: %+v", got)
	}
	if got.Rating != 9.9 || got.OriginalTitle != "Original" || got.Poster != "tmdb:/new.jpg" {
		t.Errorf("locked title did not refresh its data: %+v", got)
	}
	d, _ := s.GetTitleDetail(ctx, lockedID)
	if len(d.Cast) != 1 || len(d.Crew) != 0 {
		t.Errorf("credits of a locked title were not refreshed: %+v %+v", d.Cast, d.Crew)
	}

	// Positive control: an unlocked title takes the same text.
	plain := tmdbMovie(604, "Reloaded", 2003)
	plain.Overview, plain.Title.Title, plain.Year = "TMDB overview", "The Matrix Reloaded", 2004
	if _, err := s.UpsertTitle(ctx, plain); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTitle(ctx, plainID)
	if got.Title != "The Matrix Reloaded" || got.Overview != "TMDB overview" || got.Year != 2004 {
		t.Errorf("unlocked title text not refreshed: %+v", got)
	}
}

func TestUpsertTitleNeverBlanksImages(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := addMovie(t, s, "/media/a.mkv", tmdbMovie(1, "A", 2000))
	bare := tmdbMovie(1, "A", 2000)
	bare.Poster, bare.Backdrop, bare.Logo = "", "", ""
	if _, err := s.UpsertTitle(ctx, bare); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTitle(ctx, id)
	if got.Poster != "tmdb:/p1.jpg" || got.Backdrop != "tmdb:/b1.jpg" || got.Logo != "tmdb:/l1.png" {
		t.Errorf("images after a fetch without images: %+v", got)
	}
	bare.Poster = "tmdb:/new.jpg"
	s.UpsertTitle(ctx, bare)
	if got, _ = s.GetTitle(ctx, id); got.Poster != "tmdb:/new.jpg" {
		t.Errorf("poster = %q, want the new one", got.Poster)
	}
}

func TestNFOOnlyTitles(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	nfo := &TitleInput{Title: Title{Kind: KindMovie, Title: "Home Movie", Year: 2011, Overview: "from nfo"},
		Genres:     []Genre{{Name: "Family"}, {Name: "Comedy"}},
		Collection: &Collection{Name: "Home Videos"},
		Credits:    []Credit{{Person: Person{Name: "Uncle Bob"}, Kind: CreditCast, Character: "Himself"}}}
	id, err := s.UpsertTitle(ctx, nfo)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetTitle(ctx, id)
	if row.TMDBID != 0 || row.Source != SourceNFO || row.CollectionID == 0 {
		t.Errorf("nfo title: %+v", row)
	}
	var ids []int64
	rows, _ := s.db.Query(`SELECT id FROM genres ORDER BY id`)
	for rows.Next() {
		var gid int64
		rows.Scan(&gid)
		ids = append(ids, gid)
	}
	rows.Close()
	if !slices.Equal(ids, []int64{-2, -1}) {
		t.Errorf("NFO genre ids = %v, want [-2 -1]", ids)
	}

	// Addressed by id, the same row is updated; the same genre name (any case)
	// and person and collection are reused.
	nfo.ID = id
	nfo.Overview = "edited nfo"
	nfo.Genres = []Genre{{Name: "family"}, {Name: "Drama"}}
	if id2, err := s.UpsertTitle(ctx, nfo); err != nil || id2 != id {
		t.Fatalf("update by id = %d, %v", id2, err)
	}
	if n := count(t, s, "titles"); n != 1 {
		t.Errorf("titles = %d, want 1", n)
	}
	if n := count(t, s, "genres"); n != 3 {
		t.Errorf("genres = %d, want Family, Comedy, Drama", n)
	}
	if n, p, c := count(t, s, "title_genres"), count(t, s, "people"), count(t, s, "collections"); n != 2 || p != 1 || c != 1 {
		t.Errorf("title_genres, people, collections = %d, %d, %d; want 2, 1, 1", n, p, c)
	}

	// Another NFO title (no id) is a new row; the same person is the same person.
	other := &TitleInput{Title: Title{Kind: KindMovie, Title: "Home Movie 2"},
		Credits: []Credit{{Person: Person{Name: "Uncle Bob"}, Kind: CreditCast}}}
	id3, err := s.UpsertTitle(ctx, other)
	if err != nil || id3 == id {
		t.Fatalf("second nfo title = %d, %v", id3, err)
	}
	if n := count(t, s, "people"); n != 1 {
		t.Errorf("people = %d, want Uncle Bob once", n)
	}

	// A TMDB match for the same row promotes it in place.
	promoted := tmdbMovie(777, "Home Movie", 2011)
	promoted.ID = id
	if id4, err := s.UpsertTitle(ctx, promoted); err != nil || id4 != id {
		t.Fatalf("promotion = %d, %v; want the same row", id4, err)
	}
	if row, _ = s.GetTitle(ctx, id); row.TMDBID != 777 || row.Source != SourceTMDB {
		t.Errorf("promoted: %+v", row)
	}

	// An id never takes over a row that already has a TMDB identity.
	hijack := tmdbMovie(888, "Something Else", 2012)
	hijack.ID = id
	if id5, err := s.UpsertTitle(ctx, hijack); err != nil || id5 == id {
		t.Fatalf("hijack = %d, %v; want a new row", id5, err)
	}
	if row, _ = s.GetTitle(ctx, id); row.TMDBID != 777 || row.Title != "Home Movie" {
		t.Errorf("row with a TMDB id was overwritten: %+v", row)
	}
}

func TestPeopleAndGenresAreSharedAcrossTitles(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := tmdbMovie(1, "A", 2000)
	a.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Neo", 0), castCredit(9, "Same Name", "x", 1)}
	a.Genres = []Genre{{ID: 28, Name: "Action"}}
	b := tmdbMovie(2, "B", 2001)
	b.Credits = []Credit{
		{Person: Person{TMDBID: 6384, Name: "Keanu Reeves"}, Kind: CreditCast, Character: "John Wick"}, // no photo this time
		castCredit(10, "Same Name", "y", 1), // another person, same name
		// Listed twice for one job: the first one counts.
		crewCredit(5, "Pat", "Producer", 3), crewCredit(5, "Pat", "Producer", 4),
	}
	b.Genres = []Genre{{ID: 28, Name: "Action"}, {ID: 28, Name: "Action"}}
	for _, in := range []*TitleInput{a, b} {
		if _, err := s.UpsertTitle(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, s, "people"); n != 4 {
		t.Errorf("people = %d, want Keanu, two Same Names, Pat", n)
	}
	var profile string
	s.db.QueryRow(`SELECT profile FROM people WHERE tmdb_id = 6384`).Scan(&profile)
	if profile != "tmdb:/pr6384.jpg" {
		t.Errorf("profile = %q; a credit without a photo must not erase it", profile)
	}
	if n := count(t, s, "genres"); n != 1 {
		t.Errorf("genres = %d, want 1", n)
	}
	var ord int
	s.db.QueryRow(`SELECT ord FROM credits WHERE job = 'Producer'`).Scan(&ord)
	if n := count(t, s, "credits"); n != 2+3 || ord != 3 {
		t.Errorf("credits = %d (ord of Pat %d), want 5 with the first Pat credit kept", n, ord)
	}

	// A TMDB genre renamed at TMDB is renamed here, and so for its titles.
	a.Genres = []Genre{{ID: 28, Name: "Action & Adventure"}}
	if _, err := s.UpsertTitle(ctx, a); err != nil {
		t.Fatal(err)
	}
	var name string
	s.db.QueryRow(`SELECT name FROM genres WHERE id = 28`).Scan(&name)
	if name != "Action & Adventure" || count(t, s, "genres") != 1 {
		t.Errorf("genre 28 = %q (%d genres)", name, count(t, s, "genres"))
	}
}

func TestSeasonsAndEpisodes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	show := tmdbSeries(1396, "Breaking Bad", 2008)
	show.NextAirDate = "2030-01-02"
	show.Seasons = []Season{
		{Number: 1, Name: "Season 1", Overview: "o1", Poster: "tmdb:/s1.jpg", AirDate: "2008-01-20", Episodes: []Episode{
			{Number: 1, Name: "Pilot", Overview: "p", AirDate: "2008-01-20", RuntimeMin: 58, Still: "tmdb:/e1.jpg", Rating: 8.1},
			{Number: 2, Name: "Cat's in the Bag...", AirDate: "2008-01-27"},
			{Number: 3, Name: "...And the Bag's in the River"},
		}},
	}
	id := addShow(t, s, "Breaking Bad", []int{1, 2}, show)

	seasons, err := s.Seasons(ctx, id)
	if err != nil || len(seasons) != 1 || seasons[0].Name != "Season 1" || seasons[0].EpisodeCount != 3 || seasons[0].Poster != "tmdb:/s1.jpg" {
		t.Fatalf("Seasons = %+v, %v", seasons, err)
	}
	ep, err := s.GetEpisode(ctx, id, 1, 1)
	want := Episode{Season: 1, Number: 1, Name: "Pilot", Overview: "p", AirDate: "2008-01-20", RuntimeMin: 58, Still: "tmdb:/e1.jpg", Rating: 8.1}
	if err != nil || ep != want {
		t.Errorf("GetEpisode = %+v, %v; want %+v", ep, err, want)
	}
	if _, err := s.GetEpisode(ctx, id, 1, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEpisode(1, 9) = %v, want ErrNotFound", err)
	}
	row, _ := s.GetTitle(ctx, id)
	if row.NextAirDate != "2030-01-02" {
		t.Errorf("NextAirDate = %q", row.NextAirDate)
	}

	// Season 2 has files and no episodes yet; season 1 has both.
	need, err := s.SeasonsNeedingFetch(ctx, id)
	if err != nil || !slices.Equal(need, []int{2}) {
		t.Errorf("SeasonsNeedingFetch = %v, %v; want [2]", need, err)
	}

	// Fetching season 2 adds it and leaves season 1 alone; re-fetching season 1
	// replaces its episodes, so a dropped episode goes.
	next := tmdbSeries(1396, "Breaking Bad", 2008)
	next.Seasons = []Season{{Number: 2, Name: "Season 2", Episodes: []Episode{{Number: 1, Name: "Seven Thirty-Seven"}}}}
	if _, err := s.UpsertTitle(ctx, next); err != nil {
		t.Fatal(err)
	}
	if row, _ = s.GetTitle(ctx, id); row.NextAirDate != "" {
		t.Errorf("NextAirDate = %q; the new fetch has none", row.NextAirDate)
	}
	if need, _ = s.SeasonsNeedingFetch(ctx, id); len(need) != 0 {
		t.Errorf("SeasonsNeedingFetch = %v, want none", need)
	}
	eps, _ := s.Episodes(ctx, id)
	if len(eps) != 4 {
		t.Fatalf("episodes = %+v, want 3 + 1", eps)
	}
	next.Seasons = []Season{{Number: 1, Name: "Season 1", Episodes: []Episode{{Number: 1, Name: "Pilot (renamed)"}, {Number: 2, Name: "Two"}}}}
	if _, err := s.UpsertTitle(ctx, next); err != nil {
		t.Fatal(err)
	}
	eps, _ = s.Episodes(ctx, id)
	var got []string
	for _, e := range eps {
		got = append(got, fmt.Sprintf("%d.%d %s", e.Season, e.Number, e.Name))
	}
	if !slices.Equal(got, []string{"1.1 Pilot (renamed)", "1.2 Two", "2.1 Seven Thirty-Seven"}) {
		t.Errorf("episodes = %q", got)
	}
	if seasons, _ = s.Seasons(ctx, id); len(seasons) != 2 || seasons[0].Poster != "tmdb:/s1.jpg" {
		t.Errorf("seasons = %+v; season 1 keeps its poster when the refetch has none", seasons)
	}
}

func TestCardsPreferSidecarArtwork(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	itemID, titleID := addMovie(t, s, "/media/a.mkv", tmdbMovie(1, "A", 2000))
	showTitle := addShow(t, s, "Show", []int{1}, tmdbSeries(2, "Show", 2010))

	cards, err := s.TitleCards(ctx, []int64{titleID, showTitle, 12345})
	if err != nil || len(cards) != 2 {
		t.Fatalf("TitleCards = %+v, %v", cards, err)
	}
	if c := cards[titleID]; c.Poster != "tmdb:/p1.jpg" || c.Backdrop != "tmdb:/b1.jpg" || c.ItemID != itemID {
		t.Errorf("movie card before sidecars: %+v", c)
	}
	if c := cards[showTitle]; c.Poster != "tmdb:/p2.jpg" || c.Show != "Show" || c.ItemID != 0 {
		t.Errorf("show card before sidecars: %+v", c)
	}

	if err := s.SetItemArt(ctx, itemID, Art{Poster: "/media/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesArt(ctx, "Show", Art{Backdrop: "/media/Show/fanart.jpg"}); err != nil {
		t.Fatal(err)
	}
	sm, err := s.GetSeriesMatch(ctx, "Show")
	if err != nil || sm.ID == 0 || sm.TitleID != showTitle || sm.State != MatchMatched {
		t.Fatalf("series match after art = %+v, %v (art must not disturb the match)", sm, err)
	}
	cards, _ = s.TitleCards(ctx, []int64{titleID, showTitle})
	if c := cards[titleID]; c.Poster != LocalRef(ArtItem, itemID, "poster") || c.Backdrop != "tmdb:/b1.jpg" {
		t.Errorf("movie card = %+v; local poster beats TMDB, backdrop stays", c)
	}
	if c := cards[showTitle]; c.Poster != "tmdb:/p2.jpg" || c.Backdrop != LocalRef(ArtSeries, sm.ID, "backdrop") {
		t.Errorf("show card = %+v", c)
	}
	d, _ := s.GetTitleDetail(ctx, titleID)
	if d.Poster != LocalRef(ArtItem, itemID, "poster") {
		t.Errorf("detail poster = %q", d.Poster)
	}
}

func TestListFiltersGenresCollectionsAndPeople(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	matrix := fullMatrix()
	_, matrixID := addMovie(t, s, "/media/matrix.mkv", matrix)

	reloaded := tmdbMovie(604, "The Matrix Reloaded", 2003)
	reloaded.SortTitle = "Matrix Reloaded, The"
	reloaded.Genres = []Genre{{ID: 28, Name: "Action"}}
	reloaded.Collection = &Collection{TMDBID: 2344, Name: "The Matrix Collection"}
	reloaded.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Neo", 0)}
	_, reloadedID := addMovie(t, s, "/media/reloaded.mkv", reloaded)

	// Released earlier than both but added last: collections list by release date.
	prequel := tmdbMovie(605, "Prequel", 1990)
	prequel.Collection = &Collection{TMDBID: 2344, Name: "The Matrix Collection"}
	_, prequelID := addMovie(t, s, "/media/prequel.mkv", prequel)

	western := tmdbMovie(700, "A Western", 1960)
	western.Genres = []Genre{{ID: 37, Name: "Western"}}
	western.Credits = []Credit{castCredit(55, "John Wayne", "Duke", 0)}
	_, westernID := addMovie(t, s, "/media/western.mkv", western)

	// A title no file links to is not in the library, whatever it shares.
	ghost := tmdbMovie(999, "Ghost", 2020)
	ghost.Genres = []Genre{{ID: 28, Name: "Action"}, {ID: 99, Name: "Documentary"}}
	ghost.Collection = &Collection{TMDBID: 2344, Name: "The Matrix Collection"}
	ghost.Credits = []Credit{castCredit(6384, "Keanu Reeves", "Neo", 0), castCredit(77, "Only In Ghost", "x", 1)}
	ghostID, err := s.UpsertTitle(ctx, ghost)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitle(ctx, ghostID); got.CollectionID == 0 {
		t.Fatal("positive control: the ghost title is in the collection")
	}

	ids := func(f TitleFilter) []int64 {
		t.Helper()
		cards, err := s.Titles(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, c := range cards {
			out = append(out, c.ID)
		}
		return out
	}
	var collectionID, actionID, keanuID int64
	s.db.QueryRow(`SELECT id FROM collections WHERE tmdb_id = 2344`).Scan(&collectionID)
	s.db.QueryRow(`SELECT id FROM genres WHERE name = 'Action'`).Scan(&actionID)
	s.db.QueryRow(`SELECT id FROM people WHERE tmdb_id = 6384`).Scan(&keanuID)

	// By sort title: "The Matrix" files under M, and "Matrix Reloaded, The" before "Matrix, The".
	if got, want := ids(TitleFilter{}), []int64{westernID, reloadedID, matrixID, prequelID}; !slices.Equal(got, want) {
		t.Errorf("all = %v, want %v", got, want)
	}
	if got, want := ids(TitleFilter{GenreID: actionID}), []int64{reloadedID, matrixID}; !slices.Equal(got, want) {
		t.Errorf("genre = %v, want %v (the ghost has the genre too)", got, want)
	}
	if got, want := ids(TitleFilter{CollectionID: collectionID}), []int64{reloadedID, matrixID, prequelID}; !slices.Equal(got, want) {
		t.Errorf("collection = %v, want %v", got, want)
	}
	if got, want := ids(TitleFilter{PersonID: keanuID}), []int64{reloadedID, matrixID}; !slices.Equal(got, want) {
		t.Errorf("person = %v, want %v", got, want)
	}
	if got, want := ids(TitleFilter{PersonID: keanuID, GenreID: actionID, CollectionID: collectionID}), []int64{reloadedID, matrixID}; !slices.Equal(got, want) {
		t.Errorf("combined = %v, want %v", got, want)
	}
	if got := ids(TitleFilter{GenreID: 99999}); len(got) != 0 {
		t.Errorf("unknown genre = %v", got)
	}

	// The collection page: release order, library titles only.
	c, err := s.GetCollection(ctx, collectionID)
	if err != nil {
		t.Fatal(err)
	}
	var order []int64
	for _, card := range c.Titles {
		order = append(order, card.ID)
	}
	if c.Name != "The Matrix Collection" || c.TMDBID != 2344 || !slices.Equal(order, []int64{prequelID, matrixID, reloadedID}) {
		t.Errorf("collection = %+v, titles %v", c.Collection, order)
	}
	if _, err := s.GetCollection(ctx, collectionID+99); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown collection = %v", err)
	}

	// Genres: counted over library titles only.
	genres, err := s.Genres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, g := range genres {
		got[g.Name] = g.Count
	}
	if !reflect.DeepEqual(got, map[string]int{"Action": 2, "Science Fiction": 1, "Western": 1}) {
		t.Errorf("genres = %v; Documentary only belongs to the ghost", got)
	}
	if !slices.IsSortedFunc(genres, func(a, b GenreCount) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }) {
		t.Errorf("genres not sorted by name: %v", genres)
	}

	// People: Keanu's credits in the library, newest first, without the ghost.
	p, err := s.GetPerson(ctx, keanuID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Keanu Reeves" || p.TMDBID != 6384 || p.FetchedAt != 0 {
		t.Errorf("person = %+v", p.Person)
	}
	var credited []int64
	for _, pc := range p.Credits {
		credited = append(credited, pc.Title.ID)
	}
	if !slices.Equal(credited, []int64{reloadedID, matrixID}) {
		t.Errorf("credits = %v, want [%d %d] newest first", credited, reloadedID, matrixID)
	}
	if pc := p.Credits[1]; pc.Kind != CreditCast || pc.Character != "Neo" || pc.Title.Title != "The Matrix" {
		t.Errorf("credit = %+v", pc)
	}
	var onlyGhost int64
	s.db.QueryRow(`SELECT id FROM people WHERE tmdb_id = 77`).Scan(&onlyGhost)
	if p, err = s.GetPerson(ctx, onlyGhost); err != nil || p.Name != "Only In Ghost" || len(p.Credits) != 0 {
		t.Errorf("a person with no library credits = %+v, %v; want the person and no credits", p, err)
	}
	if _, err := s.GetPerson(ctx, 123456); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown person = %v", err)
	}

	// Search: substring, prefix first, library people only.
	found := func(q string) []string {
		t.Helper()
		cards, err := s.SearchPeople(ctx, q, 20)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range cards {
			names = append(names, c.Name)
		}
		return names
	}
	if got := found("wach"); !slices.Equal(got, []string{"Lana Wachowski", "Lilly Wachowski"}) {
		t.Errorf("search wach = %v", got)
	}
	if got := found("only in"); len(got) != 0 {
		t.Errorf("search found %v, who has no title in the library", got)
	}
	if got := found("   "); got != nil {
		t.Errorf("blank search = %v", got)
	}
	if got := found("100%"); got != nil {
		t.Errorf("a %% in the query matched %v", got)
	}
	if got, _ := s.SearchPeople(ctx, "a", 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d", len(got))
	}
	// Prefix matches come before substring matches.
	if got := found("la"); len(got) < 2 || got[0] != "Lana Wachowski" {
		t.Errorf("search la = %v; names that start with it come first", got)
	}
}

func TestSetPersonDetails(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	addMovie(t, s, "/media/a.mkv", fullMatrix())
	var id int64
	s.db.QueryRow(`SELECT id FROM people WHERE tmdb_id = 6384`).Scan(&id)

	err := s.SetPersonDetails(ctx, id, Person{Biography: "bio", Birthday: "1964-09-02", PlaceOfBirth: "Beirut", KnownFor: "Acting", IMDbID: "nm0000206"}, 1234)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPerson(ctx, id)
	if p.Biography != "bio" || p.Birthday != "1964-09-02" || p.PlaceOfBirth != "Beirut" || p.KnownFor != "Acting" ||
		p.IMDbID != "nm0000206" || p.FetchedAt != 1234 || p.Profile != "tmdb:/pr6384.jpg" || p.Name != "Keanu Reeves" {
		t.Errorf("person = %+v", p.Person)
	}
	if err := s.SetPersonDetails(ctx, id+999, Person{}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown person = %v", err)
	}
}
