package store

import (
	"context"
	"slices"
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
