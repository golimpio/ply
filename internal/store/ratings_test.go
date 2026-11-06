package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func sources(rs []Rating) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Source
	}
	return out
}

// setEpisodeID gives an episode an IMDb id and a source, as a mapping or a provider would have.
func setEpisodeID(t *testing.T, s *Store, titleID int64, season, number int, imdb, src string) {
	t.Helper()
	res, err := s.rw.Exec(`UPDATE episodes SET imdb_id = ?, imdb_src = ? WHERE title_id = ? AND season = ? AND episode = ?`,
		imdb, src, titleID, season, number)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("setEpisodeID: %d rows for S%dE%d of %d", n, season, number, titleID)
	}
}

// showWithEpisodes is a TMDB show of one season of three episodes, with a file for S01E01 only.
func showWithEpisodes(t *testing.T, s *Store, tmdb int64, name string) int64 {
	t.Helper()
	in := tmdbSeries(tmdb, name, 2008)
	in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "One"}, {Number: 2, Name: "Two"}, {Number: 3, Name: "Three"}}}}
	return addShow(t, s, name, []int{1}, in)
}

// A database from before M10 (version 9) is upgraded with its data in it: the new tables are
// there and empty, the new columns have their defaults, and nothing refers to a missing row.
func TestMigration010OnAV9Database(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 9)
	for _, q := range []string{
		`INSERT INTO titles (id, kind, tmdb_id, imdb_id, title, rating, vote_count, source, created_at, updated_at)
			VALUES (7, 'series', 1396, 'tt0903747', 'Breaking Bad', 8.9, 100, 'tmdb', 1, 1)`,
		`INSERT INTO seasons (title_id, season, name) VALUES (7, 1, 'Season 1')`,
		`INSERT INTO episodes (title_id, season, episode, name, rating) VALUES (7, 1, 1, 'Pilot', 8.3)`,
		`INSERT INTO items (kind, title, show, season, episode, path, root, size, mtime, added_at, updated_at)
			VALUES ('episode', 'Pilot', 'Breaking Bad', 1, 1, '/media/bb/S01E01.mkv', '/media', 10, 5, 10, 20)`,
		`INSERT INTO series_matches (show, title_id, match_state) VALUES ('Breaking Bad', 7, 'matched')`,
	} {
		if _, err := old.rw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Positive control: version 9 has none of what M10 adds.
	if cols := columns(t, old.rw, "episodes"); slices.Contains(cols, "imdb_id") || slices.Contains(cols, "imdb_src") {
		t.Fatalf("version 9 episodes already have the M10 columns: %v", cols)
	}
	var n int
	if err := old.rw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('title_ratings', 'episode_ratings')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("version 9 has the ratings tables: %d, %v", n, err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.rw.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != slices.Max(migrationNumbers(t)) {
		t.Fatalf("schema version = %d, %v; want the last migration", version, err)
	}
	for table, want := range map[string][]string{
		"title_ratings":   {"title_id", "source", "value", "scale", "votes", "state", "url", "fetched_at"},
		"episode_ratings": {"imdb_id", "source", "value", "scale", "votes", "fetched_at"},
	} {
		if got := columns(t, s.rw, table); !slices.Equal(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}
	for _, col := range []string{"imdb_id", "imdb_src"} {
		if !slices.Contains(columns(t, s.rw, "episodes"), col) {
			t.Errorf("episodes has no %s", col)
		}
	}
	if !slices.Contains(columns(t, s.rw, "titles"), "ratings_checked") {
		t.Error("titles has no ratings_checked")
	}

	// What was there is there, with the defaults in the new columns.
	eps, err := s.Episodes(ctx, 7)
	if err != nil || len(eps) != 1 || eps[0].Name != "Pilot" || eps[0].Rating != 8.3 || eps[0].IMDbID != "" || eps[0].IMDbSource != "" || eps[0].IMDbRating != nil {
		t.Fatalf("episodes after the upgrade = %+v, %v", eps, err)
	}
	d, err := s.GetTitleDetail(ctx, 7)
	if err != nil || d.Title != "Breaking Bad" || len(d.Ratings) != 1 || d.Ratings[0].Source != RatingTMDB {
		t.Fatalf("title after the upgrade = %+v, %v", d, err)
	}
	rows, err := s.rw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check finds a violation after the upgrade")
	}

	// The new tables work, and go with the title.
	if err := s.SetIMDbRatings(ctx, []IMDbTitleRating{{TitleID: 7, Value: 9.5, Votes: 2000, URL: "https://www.imdb.com/title/tt0903747/"}}, nil, 100, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`DELETE FROM titles WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "title_ratings"); got != 0 {
		t.Errorf("%d title_ratings rows left after the title went", got)
	}
}

// The library's IMDb ids are those of titles with a file (and of their episodes): a title no file
// refers to is not asked about.
func TestIMDbLibraryHoldsOnlyTheLibrary(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	_, movieID := addMovie(t, s, "/media/matrix.mkv", tmdbMovie(603, "The Matrix", 1999))
	showID := showWithEpisodes(t, s, 1396, "Show")
	ghost, err := s.UpsertTitle(ctx, tmdbMovie(9, "Ghost", 2020)) // a title, and no file
	if err != nil {
		t.Fatal(err)
	}
	setEpisodeID(t, s, showID, 1, 1, "tt7000001", IMDbFromDataset)
	// A show with no file has episodes with ids too: they are not the library's.
	ghostShow := tmdbSeries(77, "Ghost Show", 2010)
	ghostShow.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "Gone"}}}}
	ghostShowID, err := s.UpsertTitle(ctx, ghostShow)
	if err != nil {
		t.Fatal(err)
	}
	setEpisodeID(t, s, ghostShowID, 1, 1, "tt7000099", IMDbFromDataset)

	// Positive control: the ghost does have an IMDb id.
	var imdb string
	if err := s.rw.QueryRow(`SELECT imdb_id FROM titles WHERE id = ?`, ghost).Scan(&imdb); err != nil || imdb != "tt0000009" {
		t.Fatalf("control: the ghost's imdb id = %q, %v", imdb, err)
	}
	lib, err := s.IMDbLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := lib.Titles["tt0000603"]; !slices.Equal(got, []int64{movieID}) {
		t.Errorf("the movie = %v, want [%d]", got, movieID)
	}
	if _, there := lib.Titles["tt0000009"]; there {
		t.Error("a title without a file is in the library's ids")
	}
	if _, there := lib.Episodes["tt7000001"]; !there || len(lib.Episodes) != 1 {
		t.Errorf("episodes = %v, want only tt7000001 (not the id of an episode of a show with no file)", lib.Episodes)
	}
	if lib.Len() != len(lib.Titles)+1 {
		t.Errorf("Len = %d", lib.Len())
	}
}

// IMDb's ratings go to the titles that have the id and to the episodes, and are listed with the
// title's other ratings in the fixed order; what a later write does not reach is pruned.
func TestIMDbRatingsAreStoredReadAndPruned(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	_, movieID := addMovie(t, s, "/media/matrix.mkv", tmdbMovie(603, "The Matrix", 1999))
	showID := showWithEpisodes(t, s, 1396, "Show")
	setEpisodeID(t, s, showID, 1, 1, "tt7000001", IMDbFromDataset)

	if err := s.SetIMDbRatings(ctx,
		[]IMDbTitleRating{{TitleID: movieID, Value: 8.7, Votes: 2000000, URL: "https://www.imdb.com/title/tt0000603/"}},
		[]IMDbEpisodeRating{{IMDbID: "tt7000001", Value: 9.1, Votes: 30000}}, 100, nil); err != nil {
		t.Fatal(err)
	}
	// MDBList's come too: the card lists IMDb, then Rotten Tomatoes, then TMDB.
	if err := s.ReplaceMDBListRatings(ctx, movieID, []Rating{{Source: RatingRTCritics, Value: 93, Scale: 100, State: StateFresh, URL: "https://www.rottentomatoes.com/m/the_matrix"}}, 200); err != nil {
		t.Fatal(err)
	}

	cards, err := s.TitleCards(ctx, []int64{movieID})
	if err != nil {
		t.Fatal(err)
	}
	rs := cards[movieID].Ratings
	if !slices.Equal(sources(rs), []string{RatingIMDb, RatingRTCritics, RatingTMDB}) {
		t.Fatalf("ratings = %v, want imdb, rt_critics, tmdb in that order", sources(rs))
	}
	if r := rs[0]; r.Value != 8.7 || r.Scale != 10 || r.Votes != 2000000 || r.URL != "https://www.imdb.com/title/tt0000603/" || r.FetchedAt != 100 || r.Score100() != 87 {
		t.Errorf("imdb = %+v", r)
	}
	if r := rs[1]; r.State != StateFresh || r.Score100() != 93 || r.FetchedAt != 200 {
		t.Errorf("rt_critics = %+v", r)
	}
	if r := rs[2]; r.Value != 8.2 || r.Votes != 1000 || r.URL != "https://www.themoviedb.org/movie/603" {
		t.Errorf("tmdb = %+v (it is the title's own score, not stored again)", r)
	}
	if got := count(t, s, "title_ratings"); got != 2 {
		t.Errorf("title_ratings has %d rows; TMDB's is not stored a second time", got)
	}

	eps, err := s.Episodes(ctx, showID)
	if err != nil {
		t.Fatal(err)
	}
	if r := eps[0].IMDbRating; r == nil || r.Value != 9.1 || r.Votes != 30000 || r.URL != "https://www.imdb.com/title/tt7000001/" {
		t.Errorf("episode 1 rating = %+v", r)
	}
	if eps[1].IMDbRating != nil {
		t.Errorf("episode 2 has a rating: %+v", eps[1].IMDbRating)
	}
	ep, err := s.GetEpisode(ctx, showID, 1, 1)
	if err != nil || ep.IMDbRating == nil || ep.IMDbRating.Value != 9.1 {
		t.Errorf("GetEpisode = %+v, %v", ep, err)
	}

	// A write that does not reach the movie's row or the episode's: they are old, and pruned. The
	// show's id is new, and stays.
	if err := s.SetIMDbRatings(ctx, nil, []IMDbEpisodeRating{{IMDbID: "tt7000002", Value: 7, Votes: 5}}, 300, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneIMDbRatings(ctx, 300, nil)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want the movie's row and the first episode's", n, err)
	}
	if got := count(t, s, "title_ratings"); got != 1 { // the Rotten Tomatoes row is not IMDb's
		t.Errorf("title_ratings has %d rows after the prune, want the one of Rotten Tomatoes", got)
	}
	if got := count(t, s, "episode_ratings"); got != 1 {
		t.Errorf("episode_ratings has %d rows after the prune, want tt7000002's", got)
	}
}

// The ratings are written in chunks of at most 500 rows, one transaction each at background
// priority, and the pace (the queue's yield) is called between them.
func TestSetIMDbRatingsWritesInChunksAndYields(t *testing.T) {
	for _, tc := range []struct{ rows, chunks int }{{0, 0}, {1, 1}, {chunkSpec, 1}, {chunkSpec + 1, 2}, {2*chunkSpec + 7, 3}} {
		t.Run(fmt.Sprint(tc.rows), func(t *testing.T) {
			s, _ := openTemp(t)
			rows := make([]IMDbEpisodeRating, tc.rows)
			for i := range rows {
				rows[i] = IMDbEpisodeRating{IMDbID: fmt.Sprintf("tt%d", 1000+i), Value: 7, Votes: i}
			}
			g := record(s)
			var paced atomic.Int32
			if err := s.SetIMDbRatings(t.Context(), nil, rows, 50, func(context.Context) error { paced.Add(1); return nil }); err != nil {
				t.Fatal(err)
			}
			if got := g.count("low"); got != tc.chunks {
				t.Errorf("%d rows took the writer %d times, want %d", tc.rows, got, tc.chunks)
			}
			if got := g.count("high"); got != 0 {
				t.Errorf("%d writes were high priority; a background write must not jump ahead of a person's", got)
			}
			if got := int(paced.Load()); got != tc.chunks {
				t.Errorf("the pace was called %d times, want %d (after each chunk)", got, tc.chunks)
			}
			if got := count(t, s, "episode_ratings"); got != tc.rows {
				t.Errorf("%d rows stored, want %d", got, tc.rows)
			}
		})
	}
}

// A pace that fails (the job was cancelled) stops the write between chunks.
func TestSetIMDbRatingsStopsWhenThePaceFails(t *testing.T) {
	s, _ := openTemp(t)
	rows := make([]IMDbEpisodeRating, 3*chunkSpec)
	for i := range rows {
		rows[i] = IMDbEpisodeRating{IMDbID: fmt.Sprintf("tt%d", 1000+i), Value: 7}
	}
	stop := context.Canceled
	calls := 0
	err := s.SetIMDbRatings(t.Context(), nil, rows, 50, func(context.Context) error {
		calls++
		if calls == 2 {
			return stop
		}
		return nil
	})
	if err != stop {
		t.Fatalf("err = %v, want the pace's", err)
	}
	if got := count(t, s, "episode_ratings"); got != 2*chunkSpec {
		t.Errorf("%d rows stored, want the first two chunks (%d)", got, 2*chunkSpec)
	}
}

// Everything the last write did not reach is pruned, however much there is, a chunk at a time, and
// the pace is called between the chunks.
func TestPruneIMDbRatingsRemovesAllThatIsStaleInChunks(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	stale := make([]IMDbEpisodeRating, 2*chunkSpec+200)
	for i := range stale {
		stale[i] = IMDbEpisodeRating{IMDbID: fmt.Sprintf("tt%d", 1000+i), Value: 7}
	}
	if err := s.SetIMDbRatings(ctx, nil, stale, 10, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIMDbRatings(ctx, nil, []IMDbEpisodeRating{{IMDbID: "tt1", Value: 8}}, 20, nil); err != nil { // the current write
		t.Fatal(err)
	}
	if got := count(t, s, "episode_ratings"); got != len(stale)+1 {
		t.Fatalf("control: %d rows before the prune", got)
	}
	g := record(s)
	var paced int
	n, err := s.PruneIMDbRatings(ctx, 20, func(context.Context) error { paced++; return nil })
	if err != nil || n != len(stale) {
		t.Fatalf("pruned %d, %v; want %d", n, err, len(stale))
	}
	if got := count(t, s, "episode_ratings"); got != 1 {
		t.Errorf("%d rows left, want only the current write's", got)
	}
	if got := g.count("low"); got < 3 || g.count("high") != 0 {
		t.Errorf("the prune took the writer %d times (%d as high priority); chunks of %d, all background", got, g.count("high"), chunkSpec)
	}
	if paced != 2 {
		t.Errorf("paced %d times, want 2 (between the three chunks)", paced)
	}
}

// A title is due for MDBList when it was never asked about, when its last ask is over 14 days
// old, or over 7 for a title that came out in the last 90 days.
func TestDueRatings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	mk := func(tmdb int64, name, release string, checked time.Time) int64 {
		in := tmdbMovie(tmdb, name, 2000)
		in.ReleaseDate = release
		_, id := addMovie(t, s, "/media/"+name+".mkv", in)
		if !checked.IsZero() {
			if err := s.ReplaceMDBListRatings(ctx, id, nil, checked.Unix()); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	never := mk(1, "never", "1999-01-01", time.Time{})
	old := mk(2, "old", "1999-01-01", now.Add(-15*day))
	fresh := mk(3, "fresh", "1999-01-01", now.Add(-13*day))
	recentOld := mk(4, "recent-old", "2026-10-01", now.Add(-8*day))
	recentFresh := mk(5, "recent-fresh", "2026-10-01", now.Add(-6*day))
	edge := mk(6, "edge", "2026-08-10", now.Add(-8*day))            // 87 days ago: still recent
	notRecent := mk(7, "not-recent", "2026-07-01", now.Add(-8*day)) // over 90 days: 14-day rule
	_, noIDs := addMovie(t, s, "/media/noids.mkv", &TitleInput{Title: Title{Kind: KindMovie, Title: "No ids", Source: SourceNFO}})
	if _, err := s.UpsertTitle(ctx, tmdbMovie(8, "ghost", 2020)); err != nil { // no file
		t.Fatal(err)
	}

	due, err := s.DueRatings(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, d := range due {
		got = append(got, d.ID)
	}
	slices.Sort(got)
	want := []int64{never, old, recentOld, edge}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("due = %v, want never %d, old %d, recent-old %d, edge %d (not: fresh %d, recent-fresh %d, not-recent %d, no ids %d)",
			got, never, old, recentOld, edge, fresh, recentFresh, notRecent, noIDs)
	}
	if due[0].ID != never || due[0].Checked != 0 {
		t.Errorf("the title never asked about comes first, got %+v", due[0])
	}
	if one, _ := s.DueRatings(ctx, now, 1); len(one) != 1 {
		t.Errorf("limit 1 gave %d", len(one))
	}
}

// MDBList's answer replaces MDBList's ratings of the title (one it no longer gives goes), leaves
// IMDb's alone, and is recorded as asked also when it has nothing.
func TestReplaceMDBListRatings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	_, id := addMovie(t, s, "/media/m.mkv", tmdbMovie(603, "The Matrix", 1999))
	if err := s.SetIMDbRatings(ctx, []IMDbTitleRating{{TitleID: id, Value: 8.7, Votes: 1, URL: "u"}}, nil, 100, nil); err != nil {
		t.Fatal(err)
	}
	first := []Rating{
		{Source: RatingTrakt, Value: 85, Scale: 100, Votes: 9000, URL: "https://trakt.tv/movies/the-matrix-1999"},
		{Source: RatingMetacritic, Value: 73, Scale: 100},
		{Source: RatingLetterboxd, Value: 4.1, Scale: 5},
		{Source: RatingRTAudience, Value: 85, Scale: 100, State: StateUpright},
	}
	if err := s.ReplaceMDBListRatings(ctx, id, first, 1000); err != nil {
		t.Fatal(err)
	}
	cards, _ := s.TitleCards(ctx, []int64{id})
	if got := sources(cards[id].Ratings); !slices.Equal(got, []string{RatingIMDb, RatingRTAudience, RatingMetacritic, RatingLetterboxd, RatingTrakt, RatingTMDB}) {
		t.Fatalf("ratings = %v", got)
	}

	// The second answer has only Metacritic: the others are gone, IMDb's and TMDB's are not.
	if err := s.ReplaceMDBListRatings(ctx, id, []Rating{{Source: RatingMetacritic, Value: 74, Scale: 100}}, 2000); err != nil {
		t.Fatal(err)
	}
	cards, _ = s.TitleCards(ctx, []int64{id})
	if got := sources(cards[id].Ratings); !slices.Equal(got, []string{RatingIMDb, RatingMetacritic, RatingTMDB}) {
		t.Fatalf("after the second answer: %v", got)
	}
	if r := cards[id].Ratings[1]; r.Value != 74 || r.FetchedAt != 2000 {
		t.Errorf("metacritic = %+v", r)
	}
	var checked int64
	if err := s.rw.QueryRow(`SELECT ratings_checked FROM titles WHERE id = ?`, id).Scan(&checked); err != nil || checked != 2000 {
		t.Errorf("ratings_checked = %d, %v", checked, err)
	}

	// An empty answer is an answer.
	if err := s.ReplaceMDBListRatings(ctx, id, nil, 3000); err != nil {
		t.Fatal(err)
	}
	if err := s.rw.QueryRow(`SELECT ratings_checked FROM titles WHERE id = ?`, id).Scan(&checked); err != nil || checked != 3000 {
		t.Errorf("after an empty answer ratings_checked = %d, %v", checked, err)
	}
	cards, _ = s.TitleCards(ctx, []int64{id})
	if got := sources(cards[id].Ratings); !slices.Equal(got, []string{RatingIMDb, RatingTMDB}) {
		t.Errorf("after an empty answer: %v", got)
	}

	// What MDBList does not give is refused, and so is a title that is gone.
	for _, bad := range []Rating{{Source: RatingIMDb, Value: 8, Scale: 10}, {Source: RatingTMDB, Value: 8, Scale: 10}, {Source: "bogus", Value: 1, Scale: 1}, {Source: RatingTrakt, Value: 1}} {
		if err := s.ReplaceMDBListRatings(ctx, id, []Rating{bad}, 4000); err == nil {
			t.Errorf("a %s rating with scale %v was stored", bad.Source, bad.Scale)
		}
	}
	if err := s.ReplaceMDBListRatings(ctx, 424242, nil, 1); err != ErrNotFound {
		t.Errorf("a title that is gone: %v, want ErrNotFound", err)
	}
}

// A title MDBList would not rate is marked as asked about, and keeps the ratings it has.
func TestTouchRatingsChecked(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	_, id := addMovie(t, s, "/media/m.mkv", tmdbMovie(603, "The Matrix", 1999))
	if err := s.ReplaceMDBListRatings(ctx, id, []Rating{{Source: RatingMetacritic, Value: 73, Scale: 100}}, 1000); err != nil {
		t.Fatal(err)
	}
	checked := func() (n int64) {
		t.Helper()
		if err := s.rw.QueryRow(`SELECT ratings_checked FROM titles WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if checked() != 1000 {
		t.Fatalf("control: ratings_checked = %d", checked())
	}
	g := record(s)
	if err := s.TouchRatingsChecked(ctx, id, 5000); err != nil {
		t.Fatal(err)
	}
	if checked() != 5000 {
		t.Errorf("ratings_checked = %d, want 5000", checked())
	}
	if g.count("low") != 1 || g.count("high") != 0 {
		t.Errorf("%d background and %d high-priority writes, want one background", g.count("low"), g.count("high"))
	}
	cards, _ := s.TitleCards(ctx, []int64{id})
	if got := sources(cards[id].Ratings); !slices.Equal(got, []string{RatingMetacritic, RatingTMDB}) {
		t.Errorf("ratings after the touch = %v; they are kept", got)
	}
	if err := s.TouchRatingsChecked(ctx, 424242, 1); err != ErrNotFound {
		t.Errorf("a title that is gone: %v, want ErrNotFound", err)
	}
}

// TMDB's score is the title's own: a title from TMDB (or with an NFO over it) has it as a rating, an
// NFO-only title's rating is the NFO's and is not called TMDB's, and a show from TVDB (no score) has none.
func TestTMDBRatingComesFromTheTitle(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	_, tm := addMovie(t, s, "/media/a.mkv", tmdbMovie(1, "A", 2000))
	mixed := tmdbMovie(2, "B", 2000)
	mixed.Source = SourceMixed
	_, mx := addMovie(t, s, "/media/b.mkv", mixed)
	_, nfo := addMovie(t, s, "/media/c.mkv", &TitleInput{Title: Title{Kind: KindMovie, Title: "C", Rating: 7.5, VoteCount: 10, Source: SourceNFO}})
	unrated := tmdbMovie(4, "D", 2000)
	unrated.Rating = 0
	_, un := addMovie(t, s, "/media/d.mkv", unrated)
	tv := tmdbSeries(5, "E", 2000)
	tv.TMDBID, tv.TVDBID, tv.Source, tv.Rating = 0, 81189, SourceTVDB, 0
	tvID := addShow(t, s, "E", []int{1}, tv)
	// Whatever the source says, a rating with a TMDB id on a title that did not come from TMDB is not TMDB's.
	local := tmdbMovie(6, "F", 2000)
	local.Source, local.Rating = SourceNFO, 7.5
	_, nfoWithID := addMovie(t, s, "/media/f.mkv", local)
	tvRated := tmdbSeries(7, "G", 2000)
	tvRated.TVDBID, tvRated.Source, tvRated.Rating = 90210, SourceTVDB, 9.9
	tvRatedID := addShow(t, s, "G", []int{1}, tvRated)

	cards, err := s.TitleCards(ctx, []int64{tm, mx, nfo, un, tvID, nfoWithID, tvRatedID})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64][]string{tm: {RatingTMDB}, mx: {RatingTMDB}, nfo: {}, un: {}, tvID: {}, nfoWithID: {}, tvRatedID: {}} {
		if got := sources(cards[id].Ratings); !slices.Equal(got, want) {
			t.Errorf("title %d: ratings %v, want %v", id, got, want)
		}
	}
	if r := cards[tm].Ratings[0]; r.Scale != 10 || r.Votes != 1000 || r.URL != "https://www.themoviedb.org/movie/1" {
		t.Errorf("tmdb = %+v", r)
	}
}

// idsOf lists the IMDb ids of a show's episodes, as season/number=id@source (a blank id as "-").
func idsOf(t *testing.T, s *Store, titleID int64) []string {
	t.Helper()
	eps, err := s.Episodes(t.Context(), titleID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range eps {
		id := e.IMDbID
		if id == "" {
			id = "-"
		}
		out = append(out, fmt.Sprintf("%d/%d=%s@%s", e.Season, e.Number, id, e.IMDbSource))
	}
	return out
}

// A show's episodes keep their IMDb ids, with the source of each, when the season is written again
// with none (a refresh); an id the fetch does bring is the provider's and replaces the stored one.
func TestEpisodeIMDbIDsSurviveARefresh(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	seasonOf := func(names ...string) []Season {
		var eps []Episode
		for i, n := range names {
			eps = append(eps, Episode{Number: i + 1, Name: n})
		}
		return []Season{{Number: 1, Episodes: eps}}
	}
	in := tmdbSeries(1396, "Show", 2008)
	in.Seasons = seasonOf("One", "Two", "Three")
	showID := addShow(t, s, "Show", []int{1}, in)
	setEpisodeID(t, s, showID, 1, 1, "tt100", IMDbFromDataset)
	setEpisodeID(t, s, showID, 1, 2, "tt200", IMDbFromProvider)

	// The same season again: a TMDB refresh brings no ids.
	again := tmdbSeries(1396, "Show", 2008)
	again.Seasons = seasonOf("One (new)", "Two", "Three")
	if _, err := s.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	if eps, _ := s.Episodes(ctx, showID); eps[0].Name != "One (new)" { // positive control: the season was written again
		t.Fatalf("the refresh did not write the season: %+v", eps)
	}
	if got, want := idsOf(t, s, showID), []string{"1/1=tt100@dataset", "1/2=tt200@provider", "1/3=-@"}; !slices.Equal(got, want) {
		t.Errorf("after the refresh: %v, want %v", got, want)
	}

	// An id that a fetch does bring is the provider's, and the others are kept.
	fresh := tmdbSeries(1396, "Show", 2008)
	fresh.Seasons = seasonOf("One", "Two", "Three")
	fresh.Seasons[0].Episodes[0].IMDbID = "tt101"
	if _, err := s.UpsertTitle(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if got, want := idsOf(t, s, showID), []string{"1/1=tt101@provider", "1/2=tt200@provider", "1/3=-@"}; !slices.Equal(got, want) {
		t.Errorf("after a fetch with an id: %v, want %v", got, want)
	}
}

// The ids were mapped by season and number in the aired order, so they are kept only for a show that
// was in that order and still is: a show put in another order (the seasons are written again in its
// numbering), or one that left TVDB, starts without them.
func TestEpisodeIMDbIDsAreNotKeptAcrossAChangeOfOrder(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	tvdb := func(order string) *TitleInput {
		in := tmdbSeries(0, "TV", 2010)
		in.TMDBID, in.TVDBID, in.Source, in.IMDbID = 0, 555, SourceTVDB, "tt555"
		in.EpisodeOrder, in.ResetSeasons = order, true
		in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "A"}, {Number: 2, Name: "B"}}}}
		return in
	}
	tvID := addShow(t, s, "TV", []int{1}, tvdb(OrderAired))
	setEpisodeID(t, s, tvID, 1, 1, "tt901", IMDbFromDataset)

	// Positive control: the same order again keeps the id.
	if _, err := s.UpsertTitle(ctx, tvdb(OrderAired)); err != nil {
		t.Fatal(err)
	}
	if got := idsOf(t, s, tvID); got[0] != "1/1=tt901@dataset" {
		t.Fatalf("control: the aired order again: %v", got)
	}

	// Episode 1 of the DVD order is not episode 1 of the aired one.
	if _, err := s.UpsertTitle(ctx, tvdb(OrderDVD)); err != nil {
		t.Fatal(err)
	}
	if got, want := idsOf(t, s, tvID), []string{"1/1=-@", "1/2=-@"}; !slices.Equal(got, want) {
		t.Errorf("after the DVD order: %v, want none", got)
	}

	// Back to aired: the numbers of the DVD order are not the aired ones, so an id that the DVD rows
	// had (a provider's, say) does not follow the number.
	setEpisodeID(t, s, tvID, 1, 1, "tt0dvd", IMDbFromProvider)
	if _, err := s.UpsertTitle(ctx, tvdb(OrderAired)); err != nil {
		t.Fatal(err)
	}
	if got, want := idsOf(t, s, tvID), []string{"1/1=-@", "1/2=-@"}; !slices.Equal(got, want) {
		t.Errorf("after the DVD order back to aired: %v, want none", got)
	}
	setEpisodeID(t, s, tvID, 1, 2, "tt902", IMDbFromDataset)

	// The show leaves TVDB for TMDB: its seasons are wiped and written again.
	left := tmdbSeries(0, "TV", 2010)
	left.TMDBID, left.TVDBID, left.IMDbID = 777, 555, "tt555"
	left.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "A"}, {Number: 2, Name: "B"}}}}
	if _, err := s.UpsertTitle(ctx, left); err != nil {
		t.Fatal(err)
	}
	if got, want := idsOf(t, s, tvID), []string{"1/1=-@", "1/2=-@"}; !slices.Equal(got, want) {
		t.Errorf("after the show left TVDB: %v, want none", got)
	}
}

// The ratings of an episode follow its IMDb id, not its row: the row goes and comes back with a
// refresh, and the rating is still there.
func TestEpisodeRatingSurvivesTheSeasonBeingWrittenAgain(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	showID := showWithEpisodes(t, s, 1396, "Show")
	setEpisodeID(t, s, showID, 1, 1, "tt300", IMDbFromDataset)
	if err := s.SetIMDbRatings(ctx, nil, []IMDbEpisodeRating{{IMDbID: "tt300", Value: 8.8, Votes: 77}}, 10, nil); err != nil {
		t.Fatal(err)
	}
	again := tmdbSeries(1396, "Show", 2008)
	again.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "One"}, {Number: 2}, {Number: 3}}}}
	if _, err := s.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	ep, err := s.GetEpisode(ctx, showID, 1, 1)
	if err != nil || ep.IMDbRating == nil || ep.IMDbRating.Value != 8.8 {
		t.Errorf("the episode's rating after a refresh = %+v, %v", ep.IMDbRating, err)
	}
}

// ---- mapping the episodes' ids from IMDb's episode dataset ----

// dsEpisodes builds the dataset's rows of a show: season, then the numbers; tconst tt<season><number>
// with a prefix.
func dsEpisodes(prefix string, season int, numbers ...int) []DatasetEpisode {
	var out []DatasetEpisode
	for _, n := range numbers {
		out = append(out, DatasetEpisode{IMDbID: fmt.Sprintf("tt%s%d%02d", prefix, season, n), Season: season, Number: n})
	}
	return out
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// mapShow plans and applies the dataset for the show whose IMDb id is imdb.
func mapShow(t *testing.T, s *Store, imdb string, eps []DatasetEpisode) EpisodeIDPlan {
	t.Helper()
	plan, err := s.PlanEpisodeIMDb(t.Context(), map[string][]DatasetEpisode{imdb: eps})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEpisodeIMDb(t.Context(), plan, nil); err != nil {
		t.Fatal(err)
	}
	return plan
}

// seasonsShow is a TMDB show with the given number of episodes in each season, and a file for S01E01.
func seasonsShow(t *testing.T, s *Store, tmdb int64, name string, counts ...int) int64 {
	t.Helper()
	in := tmdbSeries(tmdb, name, 2008)
	for i, n := range counts {
		se := Season{Number: i + 1}
		for e := 1; e <= n; e++ {
			se.Episodes = append(se.Episodes, Episode{Number: e, Name: fmt.Sprint("E", e)})
		}
		in.Seasons = append(in.Seasons, se)
	}
	return addShow(t, s, name, []int{1}, in)
}

// The dataset's episodes are given to the stored ones by season and number: the plan has only what
// changes, and what is applied is the id with its source.
func TestMapEpisodeIMDbBySeasonAndNumber(t *testing.T) {
	s, _ := openTemp(t)
	showID := seasonsShow(t, s, 1396, "Show", 3, 2)
	ds := append(dsEpisodes("1", 1, 1, 2, 3), dsEpisodes("1", 2, 1, 2)...)
	if before := idsOf(t, s, showID); !slices.Equal(before, []string{"1/1=-@", "1/2=-@", "1/3=-@", "2/1=-@", "2/2=-@"}) {
		t.Fatalf("control: the episodes start with ids: %v", before)
	}
	plan := mapShow(t, s, "tt0001396", ds)
	want := []string{"1/1=tt1101@dataset", "1/2=tt1102@dataset", "1/3=tt1103@dataset", "2/1=tt1201@dataset", "2/2=tt1202@dataset"}
	if got := idsOf(t, s, showID); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
	if plan.Len() != 5 || plan.Stats.Mapped != 5 || plan.Stats.Cleared != 0 || plan.Stats.Seasons != 2 || plan.Stats.Skipped != 0 || plan.Stats.Shows != 1 {
		t.Errorf("plan = %d changes, %+v", plan.Len(), plan.Stats)
	}

	// Done once: the same dataset changes nothing, and writes nothing.
	g := record(s)
	again := mapShow(t, s, "tt0001396", ds)
	if again.Len() != 0 || g.count("low") != 0 {
		t.Errorf("the same dataset again: %d changes, %d writes", again.Len(), g.count("low"))
	}

	// An episode the dataset gives another id: it is the new one. One the dataset no longer has (it lists
	// S01E04 where it listed S01E03, the season having as many): cleared.
	ds[0].IMDbID = "tt9999"
	ds[2] = DatasetEpisode{IMDbID: "tt1104", Season: 1, Number: 4}
	mapShow(t, s, "tt0001396", ds)
	want = []string{"1/1=tt9999@dataset", "1/2=tt1102@dataset", "1/3=-@", "2/1=tt1201@dataset", "2/2=tt1202@dataset"}
	if got := idsOf(t, s, showID); !slices.Equal(got, want) {
		t.Errorf("after a change of the dataset: %v, want %v", got, want)
	}
}

// idAt is the id of one episode with its source ("id@source"; "" when it has none).
func idAt(t *testing.T, s *Store, titleID int64, season, number int) string {
	t.Helper()
	ep, err := s.GetEpisode(t.Context(), titleID, season, number)
	if err != nil {
		t.Fatalf("S%dE%d: %v", season, number, err)
	}
	if ep.IMDbID == "" {
		return ""
	}
	return ep.IMDbID + "@" + ep.IMDbSource
}

// A season is mapped only when the stored episodes and the dataset's are as many: a provider that
// merges a two-parter or splits an episode shifts every number after it. Only the show's latest season,
// which may be on air, may differ by two. A season that stops agreeing loses the ids an earlier mapping
// gave it, and nothing else.
func TestMapEpisodeIMDbNeedsTheCountsToAgree(t *testing.T) {
	s, _ := openTemp(t)
	showID := seasonsShow(t, s, 1396, "Show", 10, 10, 10, 10)
	var ds []DatasetEpisode
	ds = append(ds, dsEpisodes("1", 1, seq(1, 10)...)...) // 10: agrees
	ds = append(ds, dsEpisodes("1", 2, seq(1, 11)...)...) // 11: one more, in a season that is over
	ds = append(ds, dsEpisodes("1", 3, seq(1, 9)...)...)  // 9: one fewer, a two-parter merged, say
	ds = append(ds, dsEpisodes("1", 4, seq(1, 12)...)...) // 12: two more, in the latest season: on air

	// What an earlier mapping gave season 3, and a provider's id there.
	setEpisodeID(t, s, showID, 3, 1, "tt0old", IMDbFromDataset)
	setEpisodeID(t, s, showID, 3, 2, "tt0prov", IMDbFromProvider)
	plan := mapShow(t, s, "tt0001396", ds)

	for _, tc := range []struct {
		season, number int
		want           string
	}{
		{1, 10, "tt1110@dataset"},
		{2, 10, ""},                // 11 against 10, not the latest season: not mapped
		{3, 5, ""},                 // 9 against 10: not mapped
		{3, 1, ""},                 // and what was mapped before is taken away
		{3, 2, "tt0prov@provider"}, // but not a provider's
		{4, 10, "tt1410@dataset"},  // 12 against 10 in the latest season: mapped
	} {
		if got := idAt(t, s, showID, tc.season, tc.number); got != tc.want {
			t.Errorf("S%dE%d = %q, want %q", tc.season, tc.number, got, tc.want)
		}
	}
	if plan.Stats.Seasons != 2 || plan.Stats.Skipped != 2 || plan.Stats.Mapped != 20 || plan.Stats.Cleared != 1 {
		t.Errorf("stats = %+v, want 2 seasons mapped, 2 left, 20 episodes given an id, 1 cleared", plan.Stats)
	}

	// The latest season at three more is not the same list any more, and loses its ids; at two fewer it is
	// mapped again.
	base := ds[:30:30]
	mapShow(t, s, "tt0001396", append(base, dsEpisodes("1", 4, seq(1, 13)...)...))
	if got := idAt(t, s, showID, 4, 10); got != "" {
		t.Errorf("S4E10 with 13 against 10 = %q, want it taken away", got)
	}
	mapShow(t, s, "tt0001396", append(base, dsEpisodes("1", 4, seq(1, 8)...)...))
	if got := idAt(t, s, showID, 4, 8); got != "tt1408@dataset" {
		t.Errorf("S4E8 with 8 against 10 = %q, want tt1408@dataset", got)
	}
	if got := idAt(t, s, showID, 4, 9); got != "" {
		t.Errorf("S4E9 = %q; the dataset has no ninth episode yet", got)
	}
}

// The rule for the counts, season by season: the stored counts and the dataset's, and the seasons that
// come out mapped. A season of five episodes or fewer, on either side, has to agree exactly, the latest
// one too; so does a season the dataset is ahead of.
func TestPlanEpisodeIMDbCounts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		have    []int // stored episodes in seasons 1, 2, ...
		dataset []int // the dataset's
		mapped  []int // the seasons that come out mapped
	}{
		{"exact", []int{10}, []int{10}, []int{1}},
		{"a two-parter merged, season over", []int{9, 10}, []int{10, 10}, []int{2}},
		{"two merged, season over", []int{8, 10}, []int{10, 10}, []int{2}},
		{"an episode split, season over", []int{11, 10}, []int{10, 10}, []int{2}},
		{"the latest season with two more announced", []int{10, 12}, []int{10, 10}, []int{1, 2}},
		{"the latest season with two fewer announced", []int{10, 8}, []int{10, 10}, []int{1, 2}},
		{"the latest season with three apart", []int{10, 13}, []int{10, 10}, []int{1}},
		{"the latest season with three apart, the other way", []int{10, 7}, []int{10, 10}, []int{1}},
		{"a tiny season, one against three", []int{1}, []int{3}, nil},
		{"a small latest season, four against six", []int{4}, []int{6}, nil},
		{"five against six", []int{5}, []int{6}, nil},
		{"six against five", []int{6}, []int{5}, nil},
		{"five, exactly", []int{5}, []int{5}, []int{1}},
		{"six against seven, the latest season", []int{6}, []int{7}, []int{1}},
		{"a small season that is over, exactly", []int{3, 10}, []int{3, 10}, []int{1, 2}},
		{"a small season that is over, one off", []int{3, 10}, []int{4, 10}, []int{2}},
		{"the dataset is ahead: the stored latest season is over", []int{10, 10}, []int{10, 12, 3}, []int{1}},
		{"the dataset is ahead, exactly", []int{10, 12}, []int{10, 12, 3}, []int{1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stored []storedEpisode
			for i, n := range tc.have {
				stored = append(stored, ststored(i+1, n)...)
			}
			var ds []DatasetEpisode
			for i, n := range tc.dataset {
				ds = append(ds, dsEpisodes("1", i+1, seq(1, n)...)...)
			}
			var st EpisodeMapStats
			changes := planEpisodeMap(1, stored, ds, &st)
			got := map[int]bool{}
			for _, c := range changes {
				got[c.slot.season] = true
			}
			for season := 1; season <= len(tc.have); season++ {
				want := slices.Contains(tc.mapped, season)
				// A season with fewer stored than the dataset has is mapped in full; one with more, up to the dataset's.
				if got[season] != want {
					t.Errorf("season %d (%d stored against %d): mapped = %v, want %v", season, tc.have[season-1], tc.dataset[season-1], got[season], want)
				}
			}
			if st.Seasons != len(tc.mapped) || st.Skipped != len(tc.have)-len(tc.mapped) {
				t.Errorf("stats = %+v, want %d seasons mapped and %d left", st, len(tc.mapped), len(tc.have)-len(tc.mapped))
			}
		})
	}
}

// ststored is n stored episodes of a season, with no ids.
func ststored(season, n int) []storedEpisode {
	var out []storedEpisode
	for i := 1; i <= n; i++ {
		out = append(out, storedEpisode{slot: episodeSlot{season, i}})
	}
	return out
}

// Specials (season 0) are not mapped, whatever the dataset has: no provider numbers them as IMDb does.
func TestMapEpisodeIMDbSkipsSpecials(t *testing.T) {
	s, _ := openTemp(t)
	in := tmdbSeries(1396, "Show", 2008)
	in.Seasons = []Season{
		{Number: 0, Episodes: []Episode{{Number: 1, Name: "Special"}}},
		{Number: 1, Episodes: []Episode{{Number: 1, Name: "One"}}},
	}
	showID := addShow(t, s, "Show", []int{1}, in)
	ds := append(dsEpisodes("1", 0, 1), dsEpisodes("1", 1, 1)...)
	mapShow(t, s, "tt0001396", ds)
	if got := idAt(t, s, showID, 1, 1); got != "tt1101@dataset" { // control: the show was mapped
		t.Fatalf("S1E1 = %q", got)
	}
	if got := idAt(t, s, showID, 0, 1); got != "" {
		t.Errorf("the special got %q", got)
	}
}

// Only a show in the aired order is mapped: the dataset numbers episodes as they aired, and a show
// in the DVD or absolute order has other numbers.
func TestMapEpisodeIMDbOnlyForTheAiredOrder(t *testing.T) {
	s, _ := openTemp(t)
	mk := func(tvdb int64, name, order string) int64 {
		in := tmdbSeries(0, name, 2010)
		in.TMDBID, in.TVDBID, in.Source, in.IMDbID = 0, tvdb, SourceTVDB, "tt0000555"
		in.EpisodeOrder, in.ResetSeasons = order, true
		in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1, Name: "A"}, {Number: 2, Name: "B"}}}}
		return addShow(t, s, name, []int{1}, in)
	}
	ds := dsEpisodes("1", 1, 1, 2)

	dvd := mk(555, "DVD", OrderDVD)
	plan := mapShow(t, s, "tt0000555", ds)
	if plan.Len() != 0 || plan.Stats.Shows != 0 {
		t.Errorf("a show in the DVD order was planned for: %d changes, %+v", plan.Len(), plan.Stats)
	}
	if got := idAt(t, s, dvd, 1, 1); got != "" {
		t.Errorf("the DVD order got %q", got)
	}

	// Positive control: the same show, in the aired order, is mapped.
	aired := mk(556, "Aired", OrderAired)
	mapShow(t, s, "tt0000555", ds)
	if got := idAt(t, s, aired, 1, 1); got != "tt1101@dataset" {
		t.Errorf("control: the aired order got %q", got)
	}
	if got := idAt(t, s, dvd, 1, 1); got != "" {
		t.Errorf("the DVD order got %q once the aired one was mapped", got)
	}

	// A plan made while the show was in the aired order, applied after it was put in another: nothing
	// is written to it.
	plan, err := s.PlanEpisodeIMDb(t.Context(), map[string][]DatasetEpisode{"tt0000555": dsEpisodes("2", 1, 1, 2)})
	if err != nil || plan.Len() != 2 {
		t.Fatalf("plan = %d changes, %v", plan.Len(), err)
	}
	if _, err := s.rw.Exec(`UPDATE titles SET episode_order = 'absolute' WHERE id = ?`, aired); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEpisodeIMDb(t.Context(), plan, nil); err != nil {
		t.Fatal(err)
	}
	if got := idAt(t, s, aired, 1, 1); got != "tt1101@dataset" {
		t.Errorf("a show put in another order since the plan was written to: %q", got)
	}
}

// An id that a provider's data gave is never replaced or cleared by the mapping, in the plan or when
// the plan is applied (the id may have come after the plan was made).
func TestMapEpisodeIMDbNeverOverwritesAProvidersID(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	showID := seasonsShow(t, s, 1396, "Show", 3)
	setEpisodeID(t, s, showID, 1, 1, "tt0prov1", IMDbFromProvider)
	setEpisodeID(t, s, showID, 1, 3, "tt0prov3", IMDbFromProvider)

	ds := dsEpisodes("1", 1, 1, 2)                                          // the dataset has no S01E03, and other ids for 1 and 2
	ds = append(ds, DatasetEpisode{IMDbID: "tt1113", Season: 1, Number: 4}) // 3 stored, 3 in the dataset
	plan := mapShow(t, s, "tt0001396", ds)
	if plan.Len() != 1 {
		t.Errorf("the plan changes %d episodes, want only S01E02", plan.Len())
	}
	want := []string{"1/1=tt0prov1@provider", "1/2=tt1102@dataset", "1/3=tt0prov3@provider"}
	if got := idsOf(t, s, showID); !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}

	// A provider's id that arrives between the plan and its application.
	late, err := s.PlanEpisodeIMDb(ctx, map[string][]DatasetEpisode{"tt0001396": dsEpisodes("2", 1, 1, 2, 3)})
	if err != nil || late.Len() != 1 {
		t.Fatalf("plan = %d changes, %v; want S01E02 only", late.Len(), err)
	}
	setEpisodeID(t, s, showID, 1, 2, "tt0prov2", IMDbFromProvider)
	if err := s.ApplyEpisodeIMDb(ctx, late, nil); err != nil {
		t.Fatal(err)
	}
	if got := idAt(t, s, showID, 1, 2); got != "tt0prov2@provider" {
		t.Errorf("S01E02 = %q; the plan overwrote a provider's id", got)
	}
}

// Two episodes of the dataset for one season and number are not told apart: neither is given.
func TestMapEpisodeIMDbLeavesAnAmbiguousSlot(t *testing.T) {
	s, _ := openTemp(t)
	showID := seasonsShow(t, s, 1396, "Show", 3)
	ds := dsEpisodes("1", 1, 1, 2, 3)
	ds = append(ds, DatasetEpisode{IMDbID: "tt7777", Season: 1, Number: 2})
	ds = append(ds, ds[0]) // the same row twice is not a conflict
	mapShow(t, s, "tt0001396", ds)
	if got, want := idsOf(t, s, showID), []string{"1/1=tt1101@dataset", "1/2=-@", "1/3=tt1103@dataset"}; !slices.Equal(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

// A show the dataset has nothing for keeps the ids it has (a short file must not take them away); the
// shows it does have are mapped. Only the shows of the library, with an IMDb id and episodes, count.
func TestMapEpisodeIMDbTouchesOnlyWhatTheDatasetHas(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	one := seasonsShow(t, s, 1, "One", 2)
	two := seasonsShow(t, s, 2, "Two", 2)
	setEpisodeID(t, s, one, 1, 1, "tt0keep", IMDbFromDataset)
	mapShow(t, s, "tt0000002", dsEpisodes("2", 1, 1, 2)) // only show Two is in the dataset
	if got := idAt(t, s, one, 1, 1); got != "tt0keep@dataset" {
		t.Errorf("a show the dataset lacks lost its id: %q", got)
	}
	if got := idAt(t, s, two, 1, 1); got != "tt2101@dataset" {
		t.Errorf("control: the show in the dataset got %q", got)
	}

	// Out of the library: no file; no IMDb id; no episodes. Each would be mapped if it were listed.
	ghost := tmdbSeries(3, "Ghost", 2008)
	ghost.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1}}}}
	if _, err := s.UpsertTitle(ctx, ghost); err != nil {
		t.Fatal(err)
	}
	noID := tmdbSeries(4, "NoID", 2008)
	noID.IMDbID = ""
	noID.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1}}}}
	addShow(t, s, "NoID", []int{1}, noID)
	addShow(t, s, "Bare", []int{1}, tmdbSeries(5, "Bare", 2008))
	// A show whose only episodes are specials has nothing to map: the file is not fetched for it.
	specials := tmdbSeries(6, "Specials", 2008)
	specials.Seasons = []Season{{Number: 0, Episodes: []Episode{{Number: 1, Name: "A special"}}}}
	addShow(t, s, "Specials", []int{1}, specials)
	shows, err := s.IMDbShows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 2 || shows[0] != (IMDbShow{TitleID: one, IMDbID: "tt0000001", Episodes: 2}) || shows[1].TitleID != two {
		t.Errorf("shows = %+v, want One and Two only", shows)
	}
}

// Many changes are written in chunks of at most 500 at background priority, with the pace called
// between them; a plan with none takes the writer not at all.
func TestApplyEpisodeIMDbWritesInChunks(t *testing.T) {
	s, _ := openTemp(t)
	in := tmdbSeries(1396, "Show", 2008)
	se := Season{Number: 1}
	var ds []DatasetEpisode
	for n := 1; n <= 2*chunkSpec+7; n++ {
		se.Episodes = append(se.Episodes, Episode{Number: n})
		ds = append(ds, DatasetEpisode{IMDbID: fmt.Sprintf("tt%d", 100000+n), Season: 1, Number: n})
	}
	in.Seasons = []Season{se}
	addShow(t, s, "Show", []int{1}, in)
	plan, err := s.PlanEpisodeIMDb(t.Context(), map[string][]DatasetEpisode{"tt0001396": ds})
	if err != nil || plan.Len() != len(ds) {
		t.Fatalf("plan = %d changes, %v", plan.Len(), err)
	}
	g := record(s)
	paced := 0
	if err := s.ApplyEpisodeIMDb(t.Context(), plan, func(context.Context) error { paced++; return nil }); err != nil {
		t.Fatal(err)
	}
	if g.count("low") != 3 || g.count("high") != 0 || paced != 3 {
		t.Errorf("%d changes: %d background writes, %d high, paced %d; want 3, 0, 3", len(ds), g.count("low"), g.count("high"), paced)
	}
	var n int
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM episodes WHERE imdb_src = 'dataset'`).Scan(&n); err != nil || n != len(ds) {
		t.Errorf("%d episodes mapped, %v; want %d", n, err, len(ds))
	}

	// A pace that fails stops it between chunks.
	stop := context.Canceled
	if err := s.ApplyEpisodeIMDb(t.Context(), plan, func(context.Context) error { return stop }); err != stop {
		t.Errorf("err = %v, want the pace's", err)
	}
	g = record(s)
	if err := s.ApplyEpisodeIMDb(t.Context(), EpisodeIDPlan{}, nil); err != nil || g.count("low") != 0 {
		t.Errorf("an empty plan: %v, %d writes", err, g.count("low"))
	}
}

// The queries the ratings run at every look must not grow with the size of the library as a
// correlated subquery over every episode does: at 10,000 episodes with files (100 shows), each
// takes a few milliseconds.
func TestRatingsQueriesAtTheSizeOfALibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a library of 10,000 episodes")
	}
	s, _ := openTemp(t)
	ctx := t.Context()
	dataset := map[string][]DatasetEpisode{}
	var shows []*TitleInput

	// The files, in one transaction.
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	insertFile, err := tx.PrepareContext(ctx, `INSERT INTO items (kind, title, show, season, episode, path, root, size, mtime, added_at, updated_at)
		VALUES ('episode', 'Ep', ?, ?, ?, ?, '/media', 10, 5, 1, 1)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for sh := range 100 {
		name := fmt.Sprintf("Show %d", sh)
		in := tmdbSeries(int64(5000+sh), name, 2008)
		for se := 1; se <= 5; se++ {
			season := Season{Number: se}
			for n := 1; n <= 20; n++ {
				if _, err := insertFile.ExecContext(ctx, name, se, n, fmt.Sprintf("/media/%s/S%02dE%02d.mkv", name, se, n)); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				season.Episodes = append(season.Episodes, Episode{Number: n, Name: fmt.Sprint("E", n)})
				dataset[in.IMDbID] = append(dataset[in.IMDbID], DatasetEpisode{IMDbID: fmt.Sprintf("tt%d%d%02d", sh, se, n), Season: se, Number: n})
			}
			in.Seasons = append(in.Seasons, season)
		}
		shows = append(shows, in)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, in := range shows {
		id, err := s.UpsertTitle(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetSeriesMatch(ctx, in.Title.Title, Match{TitleID: id, State: MatchMatched, Score: 0.9}); err != nil {
			t.Fatal(err)
		}
	}

	limit := 200 * time.Millisecond
	if raceDetector {
		limit = 2 * time.Second // the race detector is several times slower; the quadratic form took 5.7 s without it
	}
	timed := func(name string, f func() error) {
		t.Helper()
		t0 := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(t0); d > limit {
			t.Errorf("%s took %v over 10,000 episodes, want under %v", name, d, limit)
		}
	}
	timed("IMDbShows", func() error {
		shows, err := s.IMDbShows(ctx)
		if err == nil && len(shows) != 100 {
			err = fmt.Errorf("%d shows, want 100", len(shows))
		}
		return err
	})
	var plan EpisodeIDPlan
	timed("PlanEpisodeIMDb", func() (err error) {
		plan, err = s.PlanEpisodeIMDb(ctx, dataset)
		if err == nil && plan.Len() != 10000 {
			err = fmt.Errorf("%d changes, want 10000", plan.Len())
		}
		return err
	})
	if err := s.ApplyEpisodeIMDb(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	timed("IMDbLibrary", func() error {
		lib, err := s.IMDbLibrary(ctx)
		if err == nil && len(lib.Episodes) != 10000 {
			err = fmt.Errorf("%d episodes, want 10000", len(lib.Episodes))
		}
		return err
	})
	timed("DueRatings", func() error { _, err := s.DueRatings(ctx, time.Now(), 20); return err })
}

func TestRatingScore100(t *testing.T) {
	for _, tc := range []struct {
		r    Rating
		want int
	}{
		{Rating{Value: 8.7, Scale: 10}, 87},
		{Rating{Value: 93, Scale: 100}, 93},
		{Rating{Value: 4.1, Scale: 5}, 82},
		{Rating{Value: 4.125, Scale: 5}, 83}, // rounded, not cut
		{Rating{Value: 7.45, Scale: 10}, 75},
		{Rating{Value: 5, Scale: 0}, 0},
	} {
		if got := tc.r.Score100(); got != tc.want {
			t.Errorf("%+v: score100 = %d, want %d", tc.r, got, tc.want)
		}
	}
}

func TestRatingSourcesAreInTheFixedOrder(t *testing.T) {
	want := []string{"imdb", "rt_critics", "rt_audience", "metacritic", "letterboxd", "trakt", "tmdb"}
	if !reflect.DeepEqual(RatingSources, want) {
		t.Errorf("RatingSources = %v", RatingSources)
	}
	for _, s := range want {
		if !ValidRatingSource(s) {
			t.Errorf("%s is not valid", s)
		}
	}
	if ValidRatingSource("omdb") || ValidRatingSource("") {
		t.Error("an unknown source is valid")
	}
}
