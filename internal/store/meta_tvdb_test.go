package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// migrationNumbers are the numbers of the migration files, which is what
// schema_version holds once they have all run.
func migrationNumbers(t *testing.T) []int {
	t.Helper()
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, f := range files {
		num, _, _ := strings.Cut(f.Name(), "_")
		n, err := strconv.Atoi(num)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func tvdbSeries(tvdb int64, title string) *TitleInput {
	return &TitleInput{Title: Title{Kind: KindSeries, TVDBID: tvdb, Title: title, SortTitle: title, Year: 2008,
		Source: SourceTVDB, Poster: "tvdb:/banners/posters/" + strconv.FormatInt(tvdb, 10) + "-1.jpg"}}
}

func queryInt(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.rw.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The rebuild of titles must not run the ON DELETE actions of what refers to it:
// every episode, credit, season, genre link and item link of a v6 database is
// still there, and the table takes the new values.
func TestMigration008RebuildsTitlesKeepingEverythingThatReferToIt(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 6)
	for _, q := range []string{
		`INSERT INTO collections (id, tmdb_id, name) VALUES (1, 2344, 'The Matrix Collection')`,
		`INSERT INTO titles (id, kind, tmdb_id, imdb_id, tvdb_id, title, source, collection_id, locked, next_air_date,
			next_episode_season, next_episode_number, next_episode_name, fetched_at, created_at, updated_at)
		 VALUES (1, 'movie', 603, 'tt0133093', NULL, 'The Matrix', 'tmdb', 1, 1, NULL, 0, 0, '', 111, 10, 20)`,
		`INSERT INTO titles (id, kind, tmdb_id, tvdb_id, title, source, next_air_date, next_episode_season,
			next_episode_number, next_episode_name, fetched_at, created_at, updated_at)
		 VALUES (2, 'series', 1396, 81189, 'Breaking Bad', 'mixed', '2026-12-01', 6, 7, 'Next one', 222, 10, 20)`,
		// TMDB's external ids say two shows are the same TVDB series: the second loses the id.
		`INSERT INTO titles (id, kind, tmdb_id, tvdb_id, title, source, fetched_at, created_at, updated_at)
		 VALUES (3, 'series', 5555, 81189, 'Breaking Bad (duplicate)', 'tmdb', 0, 10, 20)`,
		`INSERT INTO seasons (title_id, season, name) VALUES (2, 1, 'Season 1')`,
		`INSERT INTO episodes (title_id, season, episode, name) VALUES (2, 1, 1, 'Pilot'), (2, 1, 2, 'Cat in the Bag')`,
		`INSERT INTO people (id, tmdb_id, name) VALUES (1, 6384, 'Keanu Reeves'), (2, 17419, 'Bryan Cranston')`,
		`INSERT INTO credits (title_id, person_id, kind, job, character, ord) VALUES (1, 1, 'cast', '', 'Neo', 0), (2, 2, 'cast', '', 'Walter', 0)`,
		`INSERT INTO genres (id, name) VALUES (28, 'Action')`,
		`INSERT INTO title_genres (title_id, genre_id) VALUES (1, 28), (2, 28)`,
		`INSERT INTO series_matches (show, title_id, match_state) VALUES ('Breaking Bad', 2, 'manual')`,
	} {
		if _, err := old.rw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	movieID := insertOldItem(t, old, "/media/The Matrix (1999).mkv", "The Matrix")
	if _, err := old.rw.Exec(`UPDATE items SET title_id = 1, match_state = 'matched' WHERE id = ?`, movieID); err != nil {
		t.Fatal(err)
	}
	// Positive control: the old table does not have what the migration adds.
	if cols := columns(t, old.rw, "titles"); slices.Contains(cols, "episode_order") {
		t.Fatalf("a v6 titles table has episode_order already: %v", cols)
	}
	if _, err := old.rw.Exec(`INSERT INTO titles (kind, title, source, created_at, updated_at) VALUES ('series', 'x', 'tvdb', 1, 1)`); err == nil {
		t.Fatal("a v6 database takes source 'tvdb': the control is wrong")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v6 database: %v", err)
	}
	defer s.Close()

	for table, want := range map[string]int{"titles": 3, "episodes": 2, "seasons": 1, "credits": 2, "title_genres": 2,
		"people": 2, "series_matches": 1, "collections": 1, "genres": 1} {
		if got := count(t, s, table); got != want {
			t.Errorf("%s has %d rows after the migration, want %d: the rebuild must not cascade", table, got, want)
		}
	}
	if got := queryInt(t, s, `SELECT title_id FROM items WHERE id = ?`, movieID); got != 1 {
		t.Errorf("the movie file's title link = %d, want 1", got)
	}
	if got := queryInt(t, s, `SELECT title_id FROM series_matches WHERE show = 'Breaking Bad'`); got != 2 {
		t.Errorf("the show's title link = %d, want 2", got)
	}
	if got := queryInt(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); got != 0 {
		t.Errorf("%d foreign key violations after the migration", got)
	}
	if got := queryInt(t, s, `PRAGMA foreign_keys`); got != 1 {
		t.Errorf("foreign_keys = %d after the migration, want 1: the runner must turn them back on", got)
	}

	m, err := s.GetTitle(ctx, 1)
	if err != nil || m.Title != "The Matrix" || m.TMDBID != 603 || m.IMDbID != "tt0133093" || !m.Locked || m.CollectionID != 1 ||
		m.FetchedAt != 111 || m.CreatedAt != 10 || m.UpdatedAt != 20 || m.Source != SourceTMDB || m.EpisodeOrder != OrderAired {
		t.Errorf("movie after the migration: %+v, %v", m, err)
	}
	bb, err := s.GetTitle(ctx, 2)
	if err != nil || bb.TVDBID != 81189 || bb.TMDBID != 1396 || bb.Source != SourceMixed || bb.NextAirDate != "2026-12-01" ||
		bb.NextEpisodeSeason != 6 || bb.NextEpisodeNumber != 7 || bb.NextEpisodeName != "Next one" || bb.EpisodeOrder != OrderAired {
		t.Errorf("show after the migration: %+v, %v", bb, err)
	}
	if dup, err := s.GetTitle(ctx, 3); err != nil || dup.TVDBID != 0 || dup.TMDBID != 5555 {
		t.Errorf("the second title with TVDB id 81189 = %+v, %v; it keeps its TMDB id and loses the TVDB one", dup, err)
	}

	// The new table takes what the old one could not, and still refuses the rest.
	for _, c := range []struct {
		name, q string
		ok      bool
	}{
		{"source tvdb", `INSERT INTO titles (kind, tvdb_id, title, source, created_at, updated_at) VALUES ('series', 1, 'a', 'tvdb', 1, 1)`, true},
		{"source nonsense", `INSERT INTO titles (kind, tvdb_id, title, source, created_at, updated_at) VALUES ('series', 2, 'a', 'imdb', 1, 1)`, false},
		{"same kind and tvdb id", `INSERT INTO titles (kind, tvdb_id, title, source, created_at, updated_at) VALUES ('series', 1, 'b', 'tvdb', 1, 1)`, false},
		{"another kind, same tvdb id", `INSERT INTO titles (kind, tvdb_id, title, source, created_at, updated_at) VALUES ('movie', 1, 'c', 'tvdb', 1, 1)`, true},
		{"same kind and tmdb id", `INSERT INTO titles (kind, tmdb_id, title, created_at, updated_at) VALUES ('movie', 603, 'd', 1, 1)`, false},
		{"order dvd", `INSERT INTO titles (kind, title, episode_order, created_at, updated_at) VALUES ('series', 'e', 'dvd', 1, 1)`, true},
		{"order nonsense", `INSERT INTO titles (kind, title, episode_order, created_at, updated_at) VALUES ('series', 'f', 'random', 1, 1)`, false},
		{"person with a tvdb id", `INSERT INTO people (name, tvdb_id) VALUES ('g', 296140)`, true},
		{"second person with it", `INSERT INTO people (name, tvdb_id) VALUES ('h', 296140)`, false},
		{"people without one", `INSERT INTO people (name) VALUES ('i'), ('j')`, true},
	} {
		if _, err := s.rw.Exec(c.q); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
	if got := queryInt(t, s, `SELECT COUNT(*) FROM pragma_index_info('titles_collection')`); got != 1 {
		t.Errorf("titles_collection has %d columns, want it back", got)
	}

	// Cascades still work on the rebuilt table (positive control for the checks above).
	if _, err := s.rw.Exec(`DELETE FROM titles WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "episodes"); got != 0 {
		t.Errorf("episodes = %d after deleting their title: ON DELETE CASCADE is lost", got)
	}
	if got := queryInt(t, s, `SELECT COUNT(*) FROM series_matches WHERE title_id IS NULL`); got != 1 {
		t.Errorf("series_matches not set null after deleting their title")
	}
}

// A migration that asks for foreign keys off turns them on again, whether it works
// or not, and a violation it leaves behind is an error, not a commit.
func TestMigrationWithForeignKeysOffChecksAndRestores(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	before := queryInt(t, s, `SELECT COALESCE(MAX(version), 0) FROM schema_version`)

	bad := foreignKeysOff + "\nINSERT INTO series_matches (show, title_id) VALUES ('Orphan', 424242);\n"
	if err := s.applyMigration(ctx, before+1, bad); err == nil || !strings.Contains(err.Error(), "series_matches") {
		t.Errorf("a migration that leaves a dangling reference: err = %v", err)
	}
	if got := queryInt(t, s, `SELECT COUNT(*) FROM series_matches`); got != 0 {
		t.Errorf("the dangling row was committed")
	}
	if got := queryInt(t, s, `PRAGMA foreign_keys`); got != 1 {
		t.Errorf("foreign_keys = %d after a failed migration, want 1", got)
	}
	if got := queryInt(t, s, `SELECT COALESCE(MAX(version), 0) FROM schema_version`); got != before {
		t.Errorf("schema_version = %d after a failed migration, want %d", got, before)
	}

	// Positive control: the same row, without the directive, is refused by the engine itself.
	if err := s.applyMigration(ctx, before+1, "INSERT INTO series_matches (show, title_id) VALUES ('Orphan', 424242);"); err == nil {
		t.Error("a dangling reference was accepted with foreign keys on")
	}

	fine := foreignKeysOff + "\nCREATE TABLE fine (id INTEGER);\n"
	if err := s.applyMigration(ctx, before+1, fine); err != nil {
		t.Fatal(err)
	}
	if got := queryInt(t, s, `PRAGMA foreign_keys`); got != 1 {
		t.Errorf("foreign_keys = %d after a migration, want 1", got)
	}
}

func TestUpsertTVDBTitleKeepsOneRowPerShow(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// A show from TMDB, with its TVDB id from external_ids.
	tm := tmdbSeries(1396, "Breaking Bad", 2008)
	tm.TVDBID = 81189
	tm.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "Pilot"}}}}
	tmdbRow, err := s.UpsertTitle(ctx, tm)
	if err != nil {
		t.Fatal(err)
	}

	// The same show from TVDB (its remote ids give the TMDB id): the row moves over.
	tv := tvdbSeries(81189, "Breaking Bad")
	tv.TMDBID = 1396
	tv.Overview = "From TVDB"
	tvdbRow, err := s.UpsertTitle(ctx, tv)
	if err != nil || tvdbRow != tmdbRow {
		t.Fatalf("the TVDB fetch of a show TMDB has made row %d (%v), want row %d", tvdbRow, err, tmdbRow)
	}
	got, _ := s.GetTitle(ctx, tmdbRow)
	if got.Source != SourceTVDB || got.TVDBID != 81189 || got.TMDBID != 1396 || got.Overview != "From TVDB" || got.EpisodeOrder != OrderAired {
		t.Errorf("after the move: %+v", got)
	}
	if n := count(t, s, "titles"); n != 1 {
		t.Errorf("%d titles, want 1", n)
	}
	if n := count(t, s, "episodes"); n != 1 {
		t.Errorf("the show's episodes were lost in the move: %d", n)
	}

	// TVDB gives no TMDB id this time: the one the row has stays.
	tv2 := tvdbSeries(81189, "Breaking Bad")
	if _, err := s.UpsertTitle(ctx, tv2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitle(ctx, tmdbRow); got.TMDBID != 1396 {
		t.Errorf("TMDB id = %d after a TVDB fetch without one, want 1396 kept", got.TMDBID)
	}

	// A show of TVDB that TMDB does not know is its own row, found again by its TVDB id.
	other, err := s.UpsertTitle(ctx, tvdbSeries(71663, "The Simpsons"))
	if err != nil || other == tmdbRow {
		t.Fatalf("another show = %d, %v", other, err)
	}
	if again, err := s.UpsertTitle(ctx, tvdbSeries(71663, "The Simpsons")); err != nil || again != other {
		t.Errorf("the same TVDB show again = %d, %v; want %d", again, err, other)
	}
	if got, _ := s.GetTitle(ctx, other); got.Source != SourceTVDB || got.TMDBID != 0 {
		t.Errorf("a TVDB-only show: %+v", got)
	}

	// An id that another row has is not written to this one: two rows for one show
	// can happen, and the fetch must not fail on the unique index.
	clash := tvdbSeries(71663, "The Simpsons")
	clash.TMDBID = 1396 // the Breaking Bad row has it
	if _, err := s.UpsertTitle(ctx, clash); err != nil {
		t.Fatalf("a TVDB show whose TMDB id is another row's: %v", err)
	}
	if got, _ := s.GetTitle(ctx, other); got.TMDBID != 0 {
		t.Errorf("the Simpsons took TMDB id %d from Breaking Bad", got.TMDBID)
	}
	if got, _ := s.GetTitle(ctx, tmdbRow); got.TMDBID != 1396 {
		t.Errorf("Breaking Bad lost its TMDB id")
	}
}

// A show is found by the other id it carries when its own is not on the row yet: a TMDB row
// with no TVDB id that TVDB names by its TMDB id, and a TVDB row that TMDB names by its TVDB id.
func TestUpsertTitleFindsAShowByTheOtherIdItCarries(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	tmdbOnly, err := s.UpsertTitle(ctx, tmdbSeries(1396, "Breaking Bad", 2008)) // external ids gave no TVDB id
	if err != nil {
		t.Fatal(err)
	}
	tv := tvdbSeries(81189, "Breaking Bad")
	tv.TMDBID = 1396
	if id, err := s.UpsertTitle(ctx, tv); err != nil || id != tmdbOnly {
		t.Fatalf("TVDB's fetch of a show TMDB has = row %d, %v; want %d", id, err, tmdbOnly)
	}
	if got, _ := s.GetTitle(ctx, tmdbOnly); got.Source != SourceTVDB || got.TVDBID != 81189 || got.TMDBID != 1396 {
		t.Errorf("after TVDB's fetch: %+v", got)
	}

	tvdbOnly, err := s.UpsertTitle(ctx, tvdbSeries(71663, "The Simpsons")) // TVDB gave no TMDB id
	if err != nil {
		t.Fatal(err)
	}
	back := tmdbSeries(456, "The Simpsons", 1989)
	back.TVDBID = 71663
	if id, err := s.UpsertTitle(ctx, back); err != nil || id != tvdbOnly {
		t.Fatalf("TMDB's fetch of a show TVDB has = row %d, %v; want %d", id, err, tvdbOnly)
	}
	if got, _ := s.GetTitle(ctx, tvdbOnly); got.Source != SourceTMDB || got.TMDBID != 456 || got.TVDBID != 71663 {
		t.Errorf("after TMDB's fetch: %+v", got)
	}
	if n := count(t, s, "titles"); n != 2 {
		t.Errorf("%d titles, want 2", n)
	}
}

func TestUpsertTitleAdoptsLocalTitleOnlyWhenItHasNoRemoteId(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	local, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindSeries, Title: "Local Show", Source: SourceNFO}})
	if err != nil {
		t.Fatal(err)
	}
	in := tvdbSeries(500, "Local Show")
	in.ID = local
	if id, err := s.UpsertTitle(ctx, in); err != nil || id != local {
		t.Fatalf("a TVDB fetch that adopts the local title = %d, %v; want %d", id, err, local)
	}
	// Now the row has a TVDB id: addressing it by id no longer reaches it (it belongs to that identity).
	in2 := tvdbSeries(501, "Other")
	in2.ID = local
	if id, err := s.UpsertTitle(ctx, in2); err != nil || id == local {
		t.Errorf("a different TVDB show addressed by the first one's id = %d, %v; it must be a new row", id, err)
	}
}

func TestEpisodeOrderIsKeptByAnUpsertThatDoesNotSayAndReplacesSeasonsOnReset(t *testing.T) {
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
	if got, _ := s.GetTitle(ctx, id); got.EpisodeOrder != OrderAired {
		t.Fatalf("a new title's order = %q, want aired", got.EpisodeOrder)
	}
	if err := s.SetEpisodeOrder(ctx, id, OrderAbsolute); err != nil {
		t.Fatal(err)
	}
	// A refresh that does not say the order keeps it.
	if _, err := s.UpsertTitle(ctx, tvdbSeries(81189, "Breaking Bad")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTitle(ctx, id); got.EpisodeOrder != OrderAbsolute {
		t.Errorf("order = %q after a refresh that did not say, want absolute", got.EpisodeOrder)
	}
	if n := count(t, s, "episodes"); n != 2 {
		t.Fatalf("episodes = %d before the reset, want 2 (a plain upsert leaves other seasons alone)", n)
	}

	// The reset keeps only what it brings.
	re := tvdbSeries(81189, "Breaking Bad")
	re.EpisodeOrder = OrderAbsolute
	re.ResetSeasons = true
	re.Seasons = []Season{{Number: 1, Episodes: []Episode{{Season: 1, Number: 1, Name: "Pilot"}, {Season: 1, Number: 2, Name: "Seven Thirty-Seven"}}}}
	if _, err := s.UpsertTitle(ctx, re); err != nil {
		t.Fatal(err)
	}
	eps, err := s.Episodes(ctx, id)
	if err != nil || len(eps) != 2 || eps[1].Number != 2 || eps[1].Name != "Seven Thirty-Seven" {
		t.Errorf("episodes after the reset = %+v, %v", eps, err)
	}
	if n := count(t, s, "seasons"); n != 1 {
		t.Errorf("seasons after the reset = %d, want 1", n)
	}

	if err := s.SetEpisodeOrder(ctx, id, "random"); err == nil {
		t.Error("SetEpisodeOrder accepted 'random'")
	}
	if err := s.SetEpisodeOrder(ctx, 99999, OrderDVD); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEpisodeOrder on an unknown title = %v, want ErrNotFound", err)
	}
	m, _ := s.UpsertTitle(ctx, tmdbMovie(603, "The Matrix", 1999))
	if err := s.SetEpisodeOrder(ctx, m, OrderDVD); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEpisodeOrder on a movie = %v, want ErrNotFound", err)
	}
	bad := tvdbSeries(81190, "Bad")
	bad.EpisodeOrder = "random"
	if _, err := s.UpsertTitle(ctx, bad); err == nil {
		t.Error("UpsertTitle accepted the order 'random'")
	}
}

func TestTVDBCreditsMakeTVDBPeopleAndMergeByTMDBId(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// A TMDB show with Bryan Cranston (TMDB 17419) in it.
	tmdbShow := tmdbSeries(1396, "Breaking Bad", 2008)
	tmdbShow.Credits = []Credit{castCredit(17419, "Bryan Cranston", "Walter White", 0)}
	tmdbTitle, err := s.UpsertTitle(ctx, tmdbShow)
	if err != nil {
		t.Fatal(err)
	}
	// A TVDB show with the same man, as TVDB knows him: by its own person id only.
	tvShow := tvdbSeries(71663, "Another Show")
	tvShow.Credits = []Credit{
		{Person: Person{TVDBID: 296140, Name: "Bryan Cranston", Profile: "tvdb:/banners/v4/actor/296140/photo/bryan.jpg"},
			Kind: CreditCast, Character: "Hal", Order: 0},
		{Person: Person{TVDBID: 296140, Name: "Bryan Cranston"}, Kind: CreditCrew, Job: "Director"},
	}
	tvTitle, err := s.UpsertTitle(ctx, tvShow)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "people"); n != 2 {
		t.Fatalf("%d people before the merge, want 2 (TVDB's person is new until TVDB says who it is)", n)
	}
	var tvdbPerson int64
	if err := s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = 296140`).Scan(&tvdbPerson); err != nil {
		t.Fatal(err)
	}

	// The person's record at TVDB gives a TMDB id: the same man. Positive control: both rows exist.
	var tmdbPerson int64
	if err := s.rw.QueryRow(`SELECT id FROM people WHERE tmdb_id = 17419`).Scan(&tmdbPerson); err != nil || tmdbPerson == tvdbPerson {
		t.Fatalf("TMDB person %d, TVDB person %d, %v", tmdbPerson, tvdbPerson, err)
	}
	got, err := s.SetPersonDetailsMerged(ctx, tvdbPerson, Person{TMDBID: 17419, Biography: "From TVDB", IMDbID: "nm0186505"}, 99)
	if err != nil || got != tmdbPerson {
		t.Fatalf("merge = %d, %v; want the TMDB person %d", got, err, tmdbPerson)
	}
	if n := count(t, s, "people"); n != 1 {
		t.Errorf("%d people after the merge, want 1", n)
	}
	d, err := s.GetPerson(ctx, tmdbPerson)
	if err != nil || d.TMDBID != 17419 || d.TVDBID != 296140 || d.Biography != "From TVDB" || d.IMDbID != "nm0186505" || d.FetchedAt != 99 {
		t.Errorf("merged person = %+v, %v", d.Person, err)
	}
	// Credits of both shows, and the two TVDB credits (cast and Director) that the merge moved
	// (GetPerson lists the titles of the library only, and these are in no one's).
	rows, err := s.rw.Query(`SELECT title_id FROM credits WHERE person_id = ? ORDER BY title_id, kind`, tmdbPerson)
	if err != nil {
		t.Fatal(err)
	}
	var titles []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		titles = append(titles, id)
	}
	rows.Close()
	if want := []int64{tmdbTitle, tvTitle, tvTitle}; !slices.Equal(titles, want) {
		t.Errorf("credits of the merged person are in titles %v, want %v", titles, want)
	}
	if _, err := s.GetPerson(ctx, tvdbPerson); !errors.Is(err, ErrNotFound) {
		t.Errorf("the TVDB person row is still there: %v", err)
	}
	// The person is found by either id from now on: a TVDB credit lands on the same row.
	again := tvdbSeries(71663, "Another Show")
	again.Credits = tvShow.Credits[:1]
	if _, err := s.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "people"); n != 1 {
		t.Errorf("%d people after TVDB's credit came again, want 1", n)
	}

	// Details already fetched from TMDB stay: TVDB only adds what is missing (a photo).
	cameo := tmdbSeries(40, "Cameo Show", 2020)
	cameo.Credits = []Credit{{Person: Person{TMDBID: 18000, Name: "Tess Voice"}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, cameo); err != nil {
		t.Fatal(err)
	}
	var fetched int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tmdb_id = 18000`).Scan(&fetched)
	if err := s.SetPersonDetails(ctx, fetched, Person{Biography: "From TMDB", Profile: ""}, 50); err != nil {
		t.Fatal(err)
	}
	other := tvdbSeries(500, "Third Show")
	other.Credits = []Credit{{Person: Person{TVDBID: 777, Name: "Tess V.", Profile: "tvdb:/banners/x.jpg"}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, other); err != nil {
		t.Fatal(err)
	}
	var second int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = 777`).Scan(&second)
	got, err = s.SetPersonDetailsMerged(ctx, second, Person{TMDBID: 18000, Biography: "TVDB again", Profile: "tvdb:/banners/x.jpg"}, 120)
	if err != nil || got != fetched {
		t.Fatalf("second merge = %d, %v; want %d", got, err, fetched)
	}
	d, _ = s.GetPerson(ctx, fetched)
	if d.Biography != "From TMDB" || d.FetchedAt != 50 || d.Name != "Tess Voice" || d.TVDBID != 777 || d.Profile != "tvdb:/banners/x.jpg" {
		t.Errorf("TVDB overwrote details fetched from TMDB, lost its own id, or did not add the photo that was missing: %+v", d.Person)
	}
	// ... and when its credit comes again, the name and the photo of the merged person stay.
	if _, err := s.UpsertTitle(ctx, other); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetPerson(ctx, fetched)
	if d.Name != "Tess Voice" || d.Profile != "tvdb:/banners/x.jpg" {
		t.Errorf("a TVDB credit renamed a merged person: %+v", d.Person)
	}
	if err := s.SetPersonDetails(ctx, fetched, Person{Biography: "From TMDB", Profile: "tmdb:/own.jpg"}, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTitle(ctx, other); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.GetPerson(ctx, fetched); d.Profile != "tmdb:/own.jpg" {
		t.Errorf("a TVDB credit replaced the photo of a merged person: %q", d.Profile)
	}

	// Two TVDB people that TVDB gives the same TMDB id are not merged into one row (it holds
	// one TVDB id): the second keeps its own.
	rival := tvdbSeries(501, "Fourth Rival")
	rival.Credits = []Credit{{Person: Person{TVDBID: 778, Name: "Tess Voice II"}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, rival); err != nil {
		t.Fatal(err)
	}
	var rivalID int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = 778`).Scan(&rivalID)
	if got, err := s.SetPersonDetailsMerged(ctx, rivalID, Person{TMDBID: 18000, Biography: "rival"}, 130); err != nil || got != rivalID {
		t.Errorf("a second TVDB person under one TMDB person = %d, %v; want it kept as %d", got, err, rivalID)
	}
	if p, _ := s.GetPerson(ctx, rivalID); p.TVDBID != 778 || p.TMDBID != 0 || p.Biography != "rival" {
		t.Errorf("rival = %+v", p.Person)
	}
	if p, _ := s.GetPerson(ctx, fetched); p.TVDBID != 777 {
		t.Errorf("the merged person lost its TVDB id: %+v", p.Person)
	}

	// A request that lost the race: the person it fetched for was merged meanwhile by another, and the
	// answer is the person that is left, with that other request's details.
	racer := tvdbSeries(502, "Fifth Show")
	racer.Credits = []Credit{{Person: Person{TVDBID: 779, Name: "Late Person"}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, racer); err != nil {
		t.Fatal(err)
	}
	var lateID int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = 779`).Scan(&lateID)
	winner, err := s.SetPersonDetailsMerged(ctx, lateID, Person{TMDBID: 19000, Biography: "winner"}, 140)
	if err != nil || winner != lateID {
		t.Fatalf("winner = %d, %v", winner, err)
	}
	// The row the loser asked about is gone (merged into another person by the winner's request), and the
	// survivor has the TMDB id. (Its id is far from lateID, which SQLite would otherwise reuse.)
	if _, err := s.rw.Exec(`DELETE FROM people WHERE id = ?`, lateID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO people (id, name, tmdb_id, biography, fetched_at) VALUES (?, 'Late Person', 19000, 'winner', 140)`, lateID+1000); err != nil {
		t.Fatal(err)
	}
	survivor := lateID + 1000
	if n := queryInt(t, s, `SELECT COUNT(*) FROM people WHERE id = ?`, lateID); n != 0 {
		t.Fatalf("the loser's row still exists")
	}
	if got, err := s.SetPersonDetailsMerged(ctx, lateID, Person{TMDBID: 19000, Biography: "loser"}, 150); err != nil || got != survivor {
		t.Errorf("the loser of the race = %d, %v; want the survivor %d", got, err, survivor)
	}
	if p, _ := s.GetPerson(ctx, survivor); p.Biography != "winner" || p.FetchedAt != 140 {
		t.Errorf("the loser overwrote the details the winner wrote: %+v", p.Person)
	}
	// With no TMDB id to find the survivor by, a person that is gone is gone.
	if _, err := s.SetPersonDetailsMerged(ctx, lateID, Person{Biography: "x"}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("a person that is gone, no TMDB id: %v", err)
	}

	// A TVDB person whose record gives no TMDB id is only filled in; one that gives an
	// id nobody has takes the id.
	solo := tvdbSeries(600, "Fourth Show")
	solo.Credits = []Credit{{Person: Person{TVDBID: 888, Name: "Nobody Known"}, Kind: CreditCast}}
	if _, err := s.UpsertTitle(ctx, solo); err != nil {
		t.Fatal(err)
	}
	var soloID int64
	s.rw.QueryRow(`SELECT id FROM people WHERE tvdb_id = 888`).Scan(&soloID)
	if got, err := s.SetPersonDetailsMerged(ctx, soloID, Person{TMDBID: 31337, Biography: "bio"}, 7); err != nil || got != soloID {
		t.Fatalf("a TVDB person TMDB id nobody has = %d, %v; want it kept as %d", got, err, soloID)
	}
	if p, _ := s.GetPerson(ctx, soloID); p.TMDBID != 31337 || p.TVDBID != 888 || p.Biography != "bio" {
		t.Errorf("person = %+v", p.Person)
	}
	// ... so a later TMDB credit for that id lands on it.
	late := tmdbSeries(9, "Late Show", 2020)
	late.Credits = []Credit{castCredit(31337, "Nobody Known", "X", 0)}
	if _, err := s.UpsertTitle(ctx, late); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, s, `SELECT COUNT(*) FROM people WHERE name = 'Nobody Known'`); n != 1 {
		t.Errorf("%d people called Nobody Known, want 1", n)
	}
	if _, err := s.SetPersonDetailsMerged(ctx, 424242, Person{}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown person: %v", err)
	}
}

func TestLibraryQueriesSeeTVDBTitles(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	// A TVDB-only show with an episode file, and a TMDB one that TMDB gave a TVDB id.
	tv := tvdbSeries(81189, "Breaking Bad")
	tv.Status = "Continuing"
	tvID, err := s.UpsertTitle(ctx, tv)
	if err != nil {
		t.Fatal(err)
	}
	tm := tmdbSeries(1399, "Game of Thrones", 2011)
	tm.TVDBID = 121361
	tmID, _ := s.UpsertTitle(ctx, tm)
	for _, x := range []struct {
		show  string
		title int64
	}{{"Breaking Bad", tvID}, {"Game of Thrones", tmID}} {
		if _, err := s.UpsertItem(ctx, &Item{Kind: KindEpisode, Show: x.show, Season: 1, Episode: 1, Path: "/media/" + x.show + "/S01E01.mkv",
			Root: "/media", Size: 1, MTime: 1}, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSeriesMatch(ctx, x.show, Match{TitleID: x.title, State: MatchMatched}); err != nil {
			t.Fatal(err)
		}
	}

	refs, err := s.LibraryTitleRefs(ctx)
	if err != nil || len(refs) != 2 {
		t.Fatalf("LibraryTitleRefs = %+v, %v", refs, err)
	}
	if refs[0].Source != SourceTVDB || refs[0].TVDBID != 81189 || refs[0].TMDBID != 0 ||
		refs[1].Source != SourceTMDB || refs[1].TVDBID != 121361 || refs[1].TMDBID != 1399 {
		t.Errorf("refs = %+v", refs)
	}
	// Both are old, so both are due by age.
	due, err := s.TitlesDueForRefresh(ctx, time.Now().Add(60*24*time.Hour), 10)
	if err != nil || len(due) != 2 {
		t.Errorf("TitlesDueForRefresh = %d titles, %v; want both", len(due), err)
	}
	// The TVDB show has no episodes stored, but a TVDB fetch brings them all: only the TMDB
	// show's season is asked for.
	need, err := s.SeasonsNeedingFetchAll(ctx)
	if err != nil || len(need) != 1 || len(need[tmID]) != 1 {
		t.Errorf("SeasonsNeedingFetchAll = %v, %v; want only the TMDB show's season", need, err)
	}
	// Candidates for the move to TVDB: the TMDB show, with the id TMDB gave.
	sw, err := s.TMDBSeriesWithTVDBID(ctx, tmID)
	if err != nil || len(sw) != 1 || sw[0].ID != tmID || sw[0].TVDBID != 121361 {
		t.Errorf("TMDBSeriesWithTVDBID = %+v, %v", sw, err)
	}
	// A show made after the cutoff is not one of them: it was matched with TVDB on.
	if sw, err := s.TMDBSeriesWithTVDBID(ctx, tmID-1); err != nil || len(sw) != 0 {
		t.Errorf("below the cutoff: %+v, %v", sw, err)
	}
	if max, err := s.MaxTitleID(ctx); err != nil || max != tmID {
		t.Errorf("MaxTitleID = %d, %v; want %d", max, err, tmID)
	}
}
