package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The independent review of M8 (S3, S5, Q3 and the parts of the nits that are the store's).

// S5: the runner applies every migration whose version is not recorded, not only those above the
// highest one recorded. A database that has 8 and not 7 (the TVDB migration was numbered after
// the jobs one by another branch, and ran first) gets 7 too, and 8 is not run twice.
func TestOpenAppliesAMigrationBelowTheHighestOneRecorded(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 6)
	body, err := migrationFS.ReadFile("migrations/008_tvdb.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.applyMigration(ctx, 8, string(body)); err != nil {
		t.Fatal(err)
	}
	// Control: this is the database the test is about, 1 to 6 and 8, and its tables show it.
	versions := func(s *Store) []int {
		t.Helper()
		rows, err := s.rw.Query(`SELECT version FROM schema_version ORDER BY version`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []int
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			got = append(got, v)
		}
		return got
	}
	if got := versions(old); !slices.Equal(got, []int{1, 2, 3, 4, 5, 6, 8}) {
		t.Fatalf("control: versions = %v", got)
	}
	if got := queryInt(t, old, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'jobs'`); got != 0 {
		t.Fatal("control: the jobs table exists before migration 7")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a database with 8 and not 7: %v", err)
	}
	defer s.Close()
	if got, want := versions(s), migrationNumbers(t); !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v", got, want)
	}
	if got := queryInt(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'jobs'`); got != 1 {
		t.Error("migration 7 was not applied: no jobs table")
	}
	// And once more: nothing is left to apply, and nothing is applied twice.
	s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("Open again: %v", err)
	}
	defer again.Close()
	if got, want := versions(again), migrationNumbers(t); !slices.Equal(got, want) {
		t.Errorf("versions after reopening = %v, want %v", got, want)
	}
}

// S3: a fetch that began in an episode order that is not the title's any more (a person changed it
// while the show was being fetched) writes the title, and not the seasons it brings, not even when it
// asks for a reset; the order stays. One that was made in the stored order is written as usual.
func TestUpsertTitleDropsSeasonsFetchedInAnotherOrderThanTheStoredOne(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tvdbSeries(81189, "Breaking Bad")
	in.Seasons = []Season{
		{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "Pilot"}}},
		{Number: 2, Episodes: []Episode{{Season: 2, Number: 1, Name: "Seven Thirty-Seven"}}},
	}
	id, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetEpisodeOrder(ctx, id, OrderAbsolute); err != nil {
		t.Fatal(err)
	}

	stale := tvdbSeries(81189, "Breaking Bad")
	stale.Overview = "fetched meanwhile"
	stale.ExpectOrder, stale.ResetSeasons = OrderAired, true
	stale.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "stale 1"}, {Season: 1, Number: 2, Name: "stale 2"}}}}
	if _, err := s.UpsertTitle(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTitle(ctx, id)
	if got.EpisodeOrder != OrderAbsolute {
		t.Errorf("order = %q after a fetch made in aired order, want absolute", got.EpisodeOrder)
	}
	if got.Overview != "fetched meanwhile" {
		t.Errorf("overview = %q: the title itself is still written", got.Overview)
	}
	eps, _ := s.Episodes(ctx, id)
	if len(eps) != 2 || eps[0].Name != "Pilot" || eps[1].Name != "Seven Thirty-Seven" {
		t.Errorf("episodes = %+v; the stale seasons replaced the ones that were there", eps)
	}

	// Positive control: a fetch made in the order the title has replaces them.
	fresh := tvdbSeries(81189, "Breaking Bad")
	fresh.ExpectOrder, fresh.ResetSeasons = OrderAbsolute, true
	fresh.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "abs 1"}, {Season: 1, Number: 2, Name: "abs 2"}, {Season: 1, Number: 3, Name: "abs 3"}}}}
	if _, err := s.UpsertTitle(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if eps, _ = s.Episodes(ctx, id); len(eps) != 3 || eps[2].Name != "abs 3" {
		t.Errorf("episodes after a fetch in the stored order = %+v", eps)
	}
	if got, _ = s.GetTitle(ctx, id); got.EpisodeOrder != OrderAbsolute {
		t.Errorf("order = %q", got.EpisodeOrder)
	}
}

// A show that leaves TVDB (a person picks its TMDB title by hand) is TMDB's: its episodes were
// numbered in TVDB's order and are dropped, and the order is aired again; the order of a show
// from TMDB is not a thing. A show that stays at TMDB keeps the seasons the upsert does not bring.
func TestUpsertThatTakesATitleOutOfTVDBDropsItsEpisodesAndResetsTheOrder(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	var abs []Episode
	for i := 1; i <= 12; i++ {
		abs = append(abs, Episode{Season: 1, Number: i, Name: "abs"})
	}
	anime := tvdbSeries(900001, "Test Anime")
	anime.TMDBID = 500
	// (a season TMDB does not bring too: a plain upsert would leave it)
	anime.Seasons = []Season{{Number: 1, Episodes: abs}, {Number: 2, Episodes: []Episode{{Season: 2, Number: 1, Name: "abs season 2"}}}}
	id, err := s.UpsertTitle(ctx, anime)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetEpisodeOrder(ctx, id, OrderAbsolute); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitle(ctx, id); got.Source != SourceTVDB || got.EpisodeOrder != OrderAbsolute || len(mustEpisodes(t, s, id)) != 13 {
		t.Fatalf("control: %s %s, %d episodes", got.Source, got.EpisodeOrder, len(mustEpisodes(t, s, id)))
	}

	tm := tmdbSeries(500, "Test Anime", 2008)
	tm.TVDBID = 900001
	tm.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "t1"}, {Season: 1, Number: 2, Name: "t2"}}}}
	if again, err := s.UpsertTitle(ctx, tm); err != nil || again != id {
		t.Fatalf("UpsertTitle = %d, %v; want the same row %d", again, err, id)
	}
	got, _ := s.GetTitle(ctx, id)
	eps := mustEpisodes(t, s, id)
	if got.Source != SourceTMDB || got.EpisodeOrder != OrderAired {
		t.Errorf("after the pick: source %s, order %s; want tmdb and aired", got.Source, got.EpisodeOrder)
	}
	if len(eps) != 2 || eps[0].Name != "t1" || eps[1].Name != "t2" {
		t.Errorf("episodes = %+v, want TMDB's two", eps)
	}

	// A show of TMDB, upserted again with one season, keeps the other (what a plain upsert does).
	tm.Seasons = []Season{{Number: 2, Episodes: []Episode{{Season: 2, Number: 1, Name: "t3"}}}}
	if _, err := s.UpsertTitle(ctx, tm); err != nil {
		t.Fatal(err)
	}
	if got := mustEpisodes(t, s, id); len(got) != 3 {
		t.Errorf("episodes = %+v; a show at TMDB loses the seasons an upsert does not bring", got)
	}
}

func mustEpisodes(t *testing.T, s *Store, id int64) []Episode {
	t.Helper()
	eps, err := s.Episodes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return eps
}

// What a change of order leaves without a name: the files with no episode in the title's order.
func TestUnmappedEpisodeFilesCountsTheFilesWithNoEpisode(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for n := 1; n <= 3; n++ {
		addAt(t, s, episode("/lib/Anime/S01E0"+string(rune('0'+n))+".mkv", "Anime", 1, n))
	}
	addAt(t, s, episode("/lib/Other/S01E01.mkv", "Other", 1, 1)) // another show: not counted
	in := tvdbSeries(900001, "Anime")
	in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1}, {Season: 1, Number: 2}}}}
	id, err := s.UpsertTitle(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.UnmappedEpisodeFiles(ctx, id); err != nil || n != 0 {
		t.Fatalf("before the show is linked: %d, %v; nothing is linked, so nothing is unmapped", n, err)
	}
	if err := s.SetSeriesMatch(ctx, "Anime", Match{TitleID: id, State: MatchMatched, Score: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UnmappedEpisodeFiles(ctx, id); err != nil || n != 1 {
		t.Errorf("3 files and 2 episodes: %d, %v; want 1", n, err)
	}
	in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1}, {Season: 1, Number: 2}, {Season: 1, Number: 3}}}}
	if _, err := s.UpsertTitle(ctx, in); err != nil {
		t.Fatal(err)
	}
	if n, err := s.UnmappedEpisodeFiles(ctx, id); err != nil || n != 0 {
		t.Errorf("3 files and 3 episodes: %d, %v; want 0", n, err)
	}
}

// tvdbSlot reads what the archive holds for a show's match: the TMDB id, the TVDB id and the show.
func tvdbSlot(t *testing.T, s *Store, path string) (seriesID, seriesTVDB int64, show string) {
	t.Helper()
	if err := s.rw.QueryRow(`SELECT series_id, series_tvdb_id, series_show FROM item_archive WHERE path = ?`, path).
		Scan(&seriesID, &seriesTVDB, &show); err != nil {
		t.Fatalf("archive row of %s: %v", path, err)
	}
	return
}

// Q3: the archive of a removed folder keeps a manual match made through TVDB by the TVDB id, next to
// the TMDB one, and gives it back as such; a show picked at TMDB keeps its TMDB id as it did.
func TestArchiveKeepsAManualTVDBMatchByItsTVDBID(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	addAt(t, s, episode("/lib/TV/The Wire/S01E01.mkv", "The Wire", 1, 1))
	addAt(t, s, episode("/lib/TV/Fixed Show/S01E01.mkv", "Fixed Show", 1, 1))

	tv := tvdbSeries(79126, "The Wire")
	tv.TMDBID = 1438 // TVDB knows its TMDB id; the match was made through TVDB all the same
	tvTitle, err := s.UpsertTitle(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	tm := tmdbSeries(1396, "Breaking Bad", 2008)
	tm.TVDBID = 81189
	tmTitle, err := s.UpsertTitle(ctx, tm)
	if err != nil {
		t.Fatal(err)
	}
	for show, title := range map[string]int64{"The Wire": tvTitle, "Fixed Show": tmTitle} {
		if err := s.SetSeriesMatch(ctx, show, Match{TitleID: title, State: MatchManual, Score: 1}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.ArchiveAndDeleteRoot(ctx, "/lib", false); err != nil {
		t.Fatal(err)
	}
	if id, tvdbID, show := tvdbSlot(t, s, "/lib/TV/The Wire/S01E01.mkv"); id != 0 || tvdbID != 79126 || show != "The Wire" {
		t.Errorf("archive of the TVDB match = TMDB %d, TVDB %d, %q; want the TVDB id alone", id, tvdbID, show)
	}
	if id, tvdbID, show := tvdbSlot(t, s, "/lib/TV/Fixed Show/S01E01.mkv"); id != 1396 || tvdbID != 0 || show != "Fixed Show" {
		t.Errorf("archive of the TMDB match = TMDB %d, TVDB %d, %q; want the TMDB id alone", id, tvdbID, show)
	}

	// The files are back: the matches come with their kinds.
	readd := func(path, show string) int64 { // (the scanner's report of the file; the worker then applies the match)
		it := episode(path, show, 1, 1)
		it.Root, it.Size = "/lib", 100
		id, err := s.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	wid := readd("/lib/TV/The Wire/S01E01.mkv", "The Wire")
	fid := readd("/lib/TV/Fixed Show/S01E01.mkv", "Fixed Show")
	id, kind, ok, err := s.RestoreArchived(ctx, wid, "/lib/TV/The Wire/S01E01.mkv", 100)
	if err != nil || !ok || id != 79126 || kind != KindSeriesTVDB {
		t.Errorf("RestoreArchived of the TVDB match = %d, %q, %v, %v; want 79126, series-tvdb", id, kind, ok, err)
	}
	id, kind, ok, err = s.RestoreArchived(ctx, fid, "/lib/TV/Fixed Show/S01E01.mkv", 100)
	if err != nil || !ok || id != 1396 || kind != KindSeries {
		t.Errorf("RestoreArchived of the TMDB match = %d, %q, %v, %v; want 1396, series", id, kind, ok, err)
	}

	// Forgetting it, once applied, takes the row with it; only that kind's row.
	if err := s.ForgetArchivedMatch(ctx, "/lib/TV/The Wire/S01E01.mkv", KindSeriesTVDB); err != nil {
		t.Fatal(err)
	}
	left := archived(t, s)
	if _, ok := left["/lib/TV/The Wire/S01E01.mkv"]; ok {
		t.Error("the TVDB match is still archived after it was forgotten")
	}
	if _, ok := left["/lib/TV/Fixed Show/S01E01.mkv"]; !ok {
		t.Error("forgetting the TVDB match took the other one with it")
	}
}

// Q3: and the same for an episode that leaves its show (read as another by a new scan): the slot
// of the show's match holds the id of the provider it was made through, and the latest wins.
func TestSetParsedArchivesAManualTVDBMatchOfAShowByItsTVDBID(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	e1 := addAt(t, s, episode("/lib/TV/Wire/S01E01.mkv", "Wire", 1, 1))
	h1 := addAt(t, s, episode("/lib/TV/Heroes/S01E01.mkv", "Heroes", 1, 1))
	tv := tvdbSeries(79126, "The Wire")
	tv.TMDBID = 1438
	title, err := s.UpsertTitle(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Wire", Match{TitleID: title, State: MatchManual, Score: 1}); err != nil {
		t.Fatal(err)
	}
	// Control: a TMDB slot is there from before; the TVDB match replaces it (each slot holds the latest).
	if _, err := s.rw.Exec(`INSERT INTO item_archive (path, size, series_id, series_show, archived_at)
		VALUES ('/lib/TV/Wire/S01E01.mkv', 100, 1438, 'Wire', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: e1, Kind: KindEpisode, Show: "TV", Season: 1, Episode: 1},
		{ID: h1, Kind: KindEpisode, Show: "TV", Season: 1, Episode: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if id, tvdbID, show := tvdbSlot(t, s, "/lib/TV/Wire/S01E01.mkv"); id != 0 || tvdbID != 79126 || show != "Wire" {
		t.Errorf("archive = TMDB %d, TVDB %d, %q; want the TVDB id of the match, and the older TMDB slot gone", id, tvdbID, show)
	}
	// Back to its show: the match is due again, through TVDB.
	if err := s.SetParsed(ctx, []ParsedRow{
		{ID: e1, Kind: KindEpisode, Show: "Wire", Season: 1, Episode: 1},
		{ID: h1, Kind: KindEpisode, Show: "Heroes", Season: 1, Episode: 1},
	}); err != nil {
		t.Fatal(err)
	}
	want := []PendingMatch{{e1, "/lib/TV/Wire/S01E01.mkv", 79126, KindSeriesTVDB}}
	if pending, err := s.PendingMatches(ctx); err != nil || !slices.Equal(pending, want) {
		t.Errorf("PendingMatches = %+v, %v; want %+v", pending, err, want)
	}
}

// A fetch that is dropped because its order is not the stored one any more does not write the next
// episode either: it is the next episode of the order it was made in.
func TestUpsertDroppedByExpectOrderKeepsTheNextEpisode(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	next := func(in *TitleInput, season, number int, name string) *TitleInput {
		in.NextAirDate, in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = "2030-01-01", season, number, name
		return in
	}
	id, err := s.UpsertTitle(ctx, next(tvdbSeries(81189, "Breaking Bad"), 2, 1, "Aired next"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetEpisodeOrder(ctx, id, OrderAbsolute); err != nil {
		t.Fatal(err)
	}
	nextOf := func() string {
		t.Helper()
		got, err := s.GetTitle(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join([]string{got.NextAirDate, itoa(got.NextEpisodeSeason), itoa(got.NextEpisodeNumber), got.NextEpisodeName}, "|")
	}

	stale := next(tvdbSeries(81189, "Breaking Bad"), 9, 9, "Stale next")
	stale.ExpectOrder = OrderAired
	if _, err := s.UpsertTitle(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if got := nextOf(); got != "2030-01-01|2|1|Aired next" {
		t.Errorf("next episode after a fetch in another order = %q; it was written in the stale numbering", got)
	}

	// Positive control: a fetch in the stored order writes it.
	fresh := next(tvdbSeries(81189, "Breaking Bad"), 1, 5, "Absolute next")
	fresh.ExpectOrder = OrderAbsolute
	if _, err := s.UpsertTitle(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if got := nextOf(); got != "2030-01-01|1|5|Absolute next" {
		t.Errorf("next episode after a fetch in the stored order = %q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// The worker's own match of a show at TMDB (the fallback, for another folder or another name of a show
// that is on TVDB already) is linked to the TVDB row and does not turn it into TMDB's. A person's pick of
// the TMDB title does.
func TestLinkSeriesTitleOfTheWorkerLeavesAShowFromTVDBAlone(t *testing.T) {
	for _, source := range []string{SourceTMDB, SourceMixed} { // (mixed: an NFO laid over TMDB's data)
		t.Run(source, func(t *testing.T) {
			s, _ := openTemp(t)
			ctx := context.Background()
			addAt(t, s, episode("/lib/Breaking Bad/S01E01.mkv", "Breaking Bad", 1, 1))
			addAt(t, s, episode("/lib/Breaking Bad (2008)/S01E01.mkv", "Breaking Bad (2008)", 1, 1))
			tv := tvdbSeries(81189, "Breaking Bad")
			tv.TMDBID, tv.Overview = 1396, "From TVDB"
			tv.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "TVDB pilot"}}}}
			tvID, err := s.UpsertTitle(ctx, tv)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetSeriesMatch(ctx, "Breaking Bad", Match{TitleID: tvID, State: MatchMatched, Score: 1}); err != nil {
				t.Fatal(err)
			}
			tmdb := func() *TitleInput {
				in := tmdbSeries(1396, "Breaking Bad", 2008)
				in.TVDBID, in.Overview, in.Source = 81189, "From TMDB", source
				in.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "TMDB pilot"}}}}
				return in
			}

			id, ok, err := s.LinkSeriesTitle(ctx, "Breaking Bad (2008)", tmdb(), Match{State: MatchMatched, Score: 0.9}, MatchPending)
			if err != nil || !ok || id != tvID {
				t.Fatalf("LinkSeriesTitle = %d, %v, %v; want the TVDB row %d", id, ok, err, tvID)
			}
			got, _ := s.GetTitle(ctx, tvID)
			if got.Source != SourceTVDB || got.Overview != "From TVDB" || got.EpisodeOrder != OrderAired {
				t.Errorf("the row after the worker's match = %+v; it was turned into TMDB's", got)
			}
			if eps := mustEpisodes(t, s, tvID); len(eps) != 1 || eps[0].Name != "TVDB pilot" {
				t.Errorf("episodes = %+v; the show lost TVDB's", eps)
			}
			if m, err := s.GetSeriesMatch(ctx, "Breaking Bad (2008)"); err != nil || m.TitleID != tvID || m.State != MatchMatched {
				t.Errorf("the second name's match = %+v, %v; want it linked to the TVDB row", m, err)
			}
			if n := queryInt(t, s, `SELECT COUNT(*) FROM titles WHERE kind = 'series'`); n != 1 {
				t.Errorf("%d series titles, want the one", n)
			}

			// A person's pick is another thing: the same input, with no state to expect, turns it into TMDB's.
			id, ok, err = s.LinkSeriesTitle(ctx, "Breaking Bad (2008)", tmdb(), Match{State: MatchManual, Score: 1}, "")
			if err != nil || !ok || id != tvID {
				t.Fatalf("LinkSeriesTitle by a person = %d, %v, %v", id, ok, err)
			}
			got, _ = s.GetTitle(ctx, tvID)
			if got.Source == SourceTVDB || got.Overview != "From TMDB" || got.EpisodeOrder != OrderAired {
				t.Errorf("the row after a person's pick = %+v; it should be TMDB's", got)
			}
			if eps := mustEpisodes(t, s, tvID); len(eps) != 1 || eps[0].Name != "TMDB pilot" {
				t.Errorf("episodes after the pick = %+v", eps)
			}
		})
	}
}

// namesOf runs a query that gives one name per row and joins them.
func namesOf(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.rw.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return strings.Join(out, ",")
}

// The worker's own TMDB result for another name of a show that is stored from TVDB writes nothing: not
// TMDB's genres or cast on the TVDB row, nor new genres or people. A person's pick does write them.
func TestLinkSeriesTitleOfTheWorkerWritesNoGenresOrCreditsOnAShowFromTVDB(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	tv := tvdbSeries(81189, "Breaking Bad")
	tv.TMDBID = 1396
	tv.Genres = []Genre{{Name: "Drama"}}
	tv.Credits = []Credit{{Person: Person{TVDBID: 296140, Name: "Bryan Cranston"}, Kind: CreditCast, Character: "Walter"}}
	tvID, err := s.UpsertTitle(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	addAt(t, s, episode("/lib/Second Name/S01E01.mkv", "Second Name", 1, 1))
	tmdb := func() *TitleInput {
		in := tmdbSeries(1396, "Breaking Bad", 2008)
		in.Genres = []Genre{{ID: 80, Name: "Crime"}}
		in.Credits = []Credit{castCredit(17419, "Bryan Cranston", "W", 0)}
		return in
	}
	genres := func() string {
		return namesOf(t, s, `SELECT g.name FROM title_genres tg JOIN genres g ON g.id = tg.genre_id WHERE tg.title_id = ? ORDER BY g.name`, tvID)
	}
	people := func() string {
		return namesOf(t, s, `SELECT p.name || ':' || COALESCE(p.tmdb_id, 0) || '/' || COALESCE(p.tvdb_id, 0)
			FROM credits c JOIN people p ON p.id = c.person_id WHERE c.title_id = ? ORDER BY p.id`, tvID)
	}
	if genres() != "Drama" || people() != "Bryan Cranston:0/296140" {
		t.Fatalf("control: genres %q, credits %q", genres(), people())
	}
	allGenres, allPeople := count(t, s, "genres"), count(t, s, "people")

	id, ok, err := s.LinkSeriesTitle(ctx, "Second Name", tmdb(), Match{State: MatchMatched, Score: 0.9}, MatchPending)
	if err != nil || !ok || id != tvID {
		t.Fatalf("link = %d, %v, %v; want the TVDB row %d", id, ok, err, tvID)
	}
	if genres() != "Drama" || people() != "Bryan Cranston:0/296140" {
		t.Errorf("after the worker's link: genres %q, credits %q; TMDB's were written on the TVDB row", genres(), people())
	}
	if count(t, s, "genres") != allGenres || count(t, s, "people") != allPeople {
		t.Errorf("genres %d -> %d, people %d -> %d: rows were made for a fetch that was not stored",
			allGenres, count(t, s, "genres"), allPeople, count(t, s, "people"))
	}

	// A person's pick writes them (the show is TMDB's then).
	if _, _, err := s.LinkSeriesTitle(ctx, "Second Name", tmdb(), Match{State: MatchManual, Score: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if genres() != "Crime" || !strings.HasPrefix(people(), "Bryan Cranston:17419") {
		t.Errorf("after a person's pick: genres %q, credits %q", genres(), people())
	}
}

// ---- a merged person's old id ----

// mergeFor puts a person of TVDB (tvdb id) in a show and folds it, as a fetch of its record does, into the
// person with the TMDB id (made by a credit of a movie), and returns the two ids.
func mergeFor(t *testing.T, s *Store, tvdb, tmdb int64, movieTMDB int64) (old, survivor int64) {
	t.Helper()
	ctx := context.Background()
	// (the TMDB person first: the TVDB one then has the higher id)
	mv := tmdbMovie(movieTMDB, "Movie "+itoa(int(movieTMDB)), 2000)
	mv.Credits = []Credit{castCredit(tmdb, "Person "+itoa(int(tmdb)), "X", 0)}
	if _, err := s.UpsertTitle(ctx, mv); err != nil {
		t.Fatal(err)
	}
	show := tvdbSeries(tvdb*10, "Show of "+itoa(int(tvdb)))
	show.Credits = []Credit{{Person: Person{TVDBID: tvdb, Name: "Person " + itoa(int(tvdb))}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, show); err != nil {
		t.Fatal(err)
	}
	if err := s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = ?`, tvdb).Scan(&old); err != nil {
		t.Fatal(err)
	}
	var err error
	if survivor, err = s.SetPersonDetailsMerged(ctx, old, Person{TMDBID: tmdb, Biography: "bio"}, 5); err != nil || survivor == old {
		t.Fatalf("merge = %d, %v; want a survivor other than %d", survivor, err, old)
	}
	return old, survivor
}

func TestMergedPersonAnswersForTheOldIDUntilTheSurvivorGoes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	old, survivor := mergeFor(t, s, 296140, 17419, 1000)
	if _, err := s.GetPerson(ctx, old); !errors.Is(err, ErrNotFound) {
		t.Fatalf("control: the merged person is still there: %v", err)
	}
	if into, err := s.MergedPerson(ctx, old); err != nil || into != survivor {
		t.Errorf("MergedPerson(old) = %d, %v; want %d", into, err, survivor)
	}
	// Not for the survivor, for a person that was never merged, or for an id that never was.
	for _, id := range []int64{survivor, 99999, 0, -1} {
		if into, err := s.MergedPerson(ctx, id); !errors.Is(err, ErrNotFound) || into != 0 {
			t.Errorf("MergedPerson(%d) = %d, %v; want ErrNotFound", id, into, err)
		}
	}

	// The survivor is pruned (nothing in the library has them any more): the alias goes with it.
	if _, err := s.rw.Exec(`DELETE FROM titles`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPerson(ctx, survivor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("control: the survivor was not pruned: %v", err)
	}
	if into, err := s.MergedPerson(ctx, old); !errors.Is(err, ErrNotFound) || into != 0 {
		t.Errorf("MergedPerson(old) after the survivor went = %d, %v; want ErrNotFound", into, err)
	}
	if n := count(t, s, "people_alias"); n != 0 {
		t.Errorf("%d aliases left after the survivor went", n)
	}
}

// A chain of merges a, b, c gives c for every id on it: the merge moves the aliases of the person that goes.
func TestMergePeopleMovesTheAliasesOfThePersonThatGoes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	person := func(tmdb, tvdb any) int64 {
		res, err := s.rw.Exec(`INSERT INTO people (name, tmdb_id, tvdb_id) VALUES ('P', ?, ?)`, tmdb, tvdb)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	a, b, c := person(nil, 1), person(nil, 2), person(300, nil)
	merge := func(from, into int64) {
		t.Helper()
		err := s.write(ctx, prioHigh, func(tx *sql.Tx) error {
			_, _, err := mergePeople(ctx, tx, from, into, sql.NullInt64{})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	merge(a, b)
	if into, err := s.MergedPerson(ctx, a); err != nil || into != b {
		t.Fatalf("a -> %d, %v; want b %d", into, err, b)
	}
	merge(b, c)
	for name, id := range map[string]int64{"a": a, "b": b} {
		if into, err := s.MergedPerson(ctx, id); err != nil || into != c {
			t.Errorf("%s -> %d, %v; want c %d", name, into, err, c)
		}
	}
	if got := queryInt(t, s, `SELECT new_id FROM people_alias WHERE old_id = ?`, a); got != int(c) {
		t.Errorf("the alias of a is stored as %d, want c (%d): the chain is not moved up", got, c)
	}
	// If a chain is left as it is, it is followed (an alias of an alias).
	d, e, f := person(nil, 11), person(nil, 12), person(310, nil)
	if _, err := s.rw.Exec(`INSERT INTO people_alias (old_id, new_id) VALUES (?, ?)`, e+1000, f); err != nil { // (a stale one: the id is no person's)
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO people_alias (old_id, new_id) VALUES (?, ?), (?, ?)`, d, e, e, f); err != nil {
		t.Fatal(err)
	}
	if into, err := s.MergedPerson(ctx, d); err != nil || into != f {
		t.Errorf("a chain d -> e -> f as stored: %d, %v; want f", into, err)
	}
	// ... and ends where the chain does, a loop included.
	if _, err := s.rw.Exec(`INSERT OR REPLACE INTO people_alias (old_id, new_id) VALUES (?, ?)`, f, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergedPerson(ctx, d); err != nil && !errors.Is(err, ErrNotFound) {
		t.Errorf("a loop in the aliases: %v", err)
	}
}

// SQLite gives the highest id again after it is deleted: a person who takes the id of one that was
// merged away is not that one, and the id answers with the person.
func TestAPersonWhoTakesTheIDOfAMergedOneEndsItsAlias(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	old, survivor := mergeFor(t, s, 296140, 17419, 1000)
	if old < survivor {
		t.Skip("the merged person did not have the highest id: nothing is handed out again") // (not the case here)
	}
	if _, err := s.MergedPerson(ctx, old); err != nil {
		t.Fatalf("control: no alias for %d: %v", old, err)
	}
	res, err := s.rw.Exec(`INSERT INTO people (name) VALUES ('Someone new')`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := res.LastInsertId(); got != old {
		t.Skipf("the new person has id %d, not the %d that was merged away: this SQLite does not reuse it", got, old)
	}
	if _, err := s.MergedPerson(ctx, old); !errors.Is(err, ErrNotFound) {
		t.Errorf("the id of a new person is still an alias: %v", err)
	}
}

// A fetch that lost the race to a merge, with no TMDB id of its own, still finds the survivor: by the alias.
func TestSetPersonDetailsMergedFindsTheSurvivorOfAMergeByTheAlias(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	old, survivor := mergeFor(t, s, 296140, 17419, 1000)
	got, err := s.SetPersonDetailsMerged(ctx, old, Person{Biography: "from a loser"}, 9)
	if err != nil || got != survivor {
		t.Errorf("SetPersonDetailsMerged on a merged id with no TMDB id = %d, %v; want the survivor %d", got, err, survivor)
	}
	if d, _ := s.GetPerson(ctx, survivor); d.Biography != "bio" {
		t.Errorf("the loser overwrote the survivor's details: %+v", d.Person)
	}
	// An id that never was a person is still an error.
	if _, err := s.SetPersonDetailsMerged(ctx, 99999, Person{Biography: "x"}, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown person: %v", err)
	}
}
