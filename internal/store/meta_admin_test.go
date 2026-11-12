package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if v, ok, err := s.GetSetting(ctx, "tmdb_key"); err != nil || ok || v != "" {
		t.Fatalf("GetSetting(missing) = %q, %v, %v", v, ok, err)
	}
	if err := s.SetSetting(ctx, "tmdb_key", "abc"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetSetting(ctx, "tmdb_key"); err != nil || !ok || v != "abc" {
		t.Errorf("GetSetting = %q, %v, %v", v, ok, err)
	}
	s.SetSetting(ctx, "tmdb_key", "def")
	s.SetSetting(ctx, "empty", "")
	if v, _, _ := s.GetSetting(ctx, "tmdb_key"); v != "def" {
		t.Errorf("overwrite: %q", v)
	}
	// An empty value is a value, not a missing key.
	if v, ok, _ := s.GetSetting(ctx, "empty"); !ok || v != "" {
		t.Errorf("empty value = %q, %v", v, ok)
	}
	if err := s.DeleteSetting(ctx, "tmdb_key"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetSetting(ctx, "tmdb_key"); ok {
		t.Error("setting still there after DeleteSetting")
	}
	if err := s.DeleteSetting(ctx, "never-set"); err != nil {
		t.Errorf("DeleteSetting(missing) = %v", err)
	}
	if v, ok, _ := s.GetSetting(ctx, "empty"); !ok || v != "" {
		t.Errorf("another key was deleted: %q, %v", v, ok)
	}
}

// pruneFixture builds a library with a bit of everything and returns ids.
type pruneFixture struct {
	keepItem, goneItem               int64
	keepTitle, goneTitle, orphan     int64
	sharedPerson, onlyGone, onlyOrph int64
}

func buildPruneFixture(t *testing.T, s *Store) pruneFixture {
	t.Helper()
	ctx := context.Background()
	var f pruneFixture

	keep := tmdbMovie(1, "Keep", 2000)
	keep.Genres = []Genre{{ID: 28, Name: "Action"}}
	keep.Collection = &Collection{TMDBID: 100, Name: "Shared Collection"}
	keep.Credits = []Credit{castCredit(1, "Shared Person", "a", 0)}
	f.keepItem, f.keepTitle = addMovie(t, s, "/m/keep.mkv", keep)

	gone := tmdbMovie(2, "Gone", 2001)
	gone.Genres = []Genre{{ID: 28, Name: "Action"}, {ID: 35, Name: "Comedy"}}
	gone.Collection = &Collection{TMDBID: 100, Name: "Shared Collection"}
	gone.Credits = []Credit{castCredit(1, "Shared Person", "b", 0), castCredit(2, "Only In Gone", "c", 1)}
	gone.Seasons = []Season{{Number: 1, Episodes: []Episode{{Number: 1}}}}
	f.goneItem, f.goneTitle = addMovie(t, s, "/m/gone.mkv", gone)

	orphan := tmdbMovie(3, "Orphan", 2002)
	orphan.Genres = []Genre{{ID: 99, Name: "Documentary"}}
	orphan.Collection = &Collection{TMDBID: 200, Name: "Orphan Collection"}
	orphan.Credits = []Credit{castCredit(3, "Only In Orphan", "d", 0)}
	var err error
	if f.orphan, err = s.UpsertTitle(ctx, orphan); err != nil {
		t.Fatal(err)
	}

	// A show that has episodes, and a row for one that has none left.
	showTitle := addShow(t, s, "Real Show", []int{1}, tmdbSeries(4, "Real Show", 2010))
	_ = showTitle
	deadTitle, err := s.UpsertTitle(ctx, tmdbSeries(5, "Dead Show", 2010))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO series_matches (show, title_id, match_state) VALUES ('Dead Show', ?, 'matched')`, deadTitle); err != nil {
		t.Fatal(err)
	}

	person := func(tmdb int64) (id int64) {
		s.rw.QueryRow(`SELECT id FROM people WHERE tmdb_id = ?`, tmdb).Scan(&id)
		return
	}
	f.sharedPerson, f.onlyGone, f.onlyOrph = person(1), person(2), person(3)
	return f
}

func exists(t *testing.T, s *Store, table string, id int64) bool {
	t.Helper()
	var n int
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestPruneMetadata(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	f := buildPruneFixture(t, s)

	// Nothing is orphaned except what never had a file: the orphan title, its
	// person, its collection and its genre, and the show row without episodes
	// with its title. The file that is still there protects the rest.
	res, err := s.PruneMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (PruneResult{Series: 1, Titles: 2, People: 1, Collections: 1, Genres: 1}); res != want {
		t.Errorf("first prune = %+v, want %+v", res, want)
	}
	if exists(t, s, "titles", f.orphan) || exists(t, s, "people", f.onlyOrph) {
		t.Error("the orphan title or its person survived")
	}
	for _, id := range []int64{f.keepTitle, f.goneTitle} {
		if !exists(t, s, "titles", id) {
			t.Errorf("title %d was pruned while its file is still there", id)
		}
	}
	if !exists(t, s, "people", f.onlyGone) || !exists(t, s, "people", f.sharedPerson) {
		t.Error("a person of a title with a file was pruned")
	}
	if n := count(t, s, "series_matches"); n != 1 {
		t.Errorf("series_matches = %d, want just Real Show", n)
	}
	if n := count(t, s, "collections"); n != 1 {
		t.Errorf("collections = %d, want Shared Collection only", n)
	}

	// Delete a file: its title, the people only it had, its seasons, episodes
	// and genre links go; the shared person, collection and genre stay.
	if err := s.DeleteItems(ctx, []int64{f.goneItem}); err != nil {
		t.Fatal(err)
	}
	if !exists(t, s, "titles", f.goneTitle) {
		t.Fatal("positive control: the title exists until pruned")
	}
	e, _ := s.GetEntry(ctx, f.keepItem)
	if e.TitleID != f.keepTitle {
		t.Fatal("positive control: the kept item is linked")
	}
	res, err = s.PruneMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (PruneResult{Titles: 1, People: 1, Genres: 1}); res != want {
		t.Errorf("second prune = %+v, want %+v", res, want)
	}
	if exists(t, s, "titles", f.goneTitle) || exists(t, s, "people", f.onlyGone) {
		t.Error("the deleted file's title or its person survived")
	}
	if !exists(t, s, "people", f.sharedPerson) || !exists(t, s, "titles", f.keepTitle) {
		t.Error("something that is still used was pruned")
	}
	for table, want := range map[string]int{"collections": 1, "genres": 1, "seasons": 0, "episodes": 0, "title_genres": 1, "credits": 1} {
		if n := count(t, s, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}

	// The last file goes: the collection and the genre go too.
	if err := s.DeleteItems(ctx, []int64{f.keepItem}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"collections", "genres", "title_genres", "credits"} {
		if n := count(t, s, table); n != 0 {
			t.Errorf("%s = %d after the last movie went", table, n)
		}
	}
	if n := count(t, s, "titles"); n != 1 { // the show remains
		t.Errorf("titles = %d, want just the show", n)
	}
	if res, err = s.PruneMetadata(ctx); err != nil || res != (PruneResult{}) {
		t.Errorf("a prune with nothing to do = %+v, %v", res, err)
	}
}

func TestPruneMetadataIsOneTransaction(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	f := buildPruneFixture(t, s)

	// The last step fails: the earlier ones must not stay done.
	if _, err := s.rw.Exec(`CREATE TRIGGER no_genre_prune BEFORE DELETE ON genres
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneMetadata(ctx); err == nil {
		t.Fatal("PruneMetadata succeeded although a delete was refused")
	}
	if !exists(t, s, "titles", f.orphan) || !exists(t, s, "people", f.onlyOrph) || count(t, s, "series_matches") != 2 {
		t.Error("a failed prune left a partial result")
	}

	// Positive control: without the trigger the same data is pruned.
	if _, err := s.rw.Exec(`DROP TRIGGER no_genre_prune`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(t, s, "titles", f.orphan) || exists(t, s, "people", f.onlyOrph) || count(t, s, "series_matches") != 1 {
		t.Error("the prune did not run once allowed")
	}
}

func TestTitlesDueForRefresh(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }

	type spec struct {
		name       string
		kind       string
		status     string
		release    string
		nextAir    string
		fetchedAgo time.Duration
		fetchedAt  int64 // overrides fetchedAgo
		wantDue    bool
	}
	date := func(daysAgo int) string { return now.Add(-time.Duration(daysAgo) * day).Format("2006-01-02") }
	specs := []spec{
		{name: "running show, 8 days", kind: KindSeries, status: "Returning Series", fetchedAgo: 8 * day, wantDue: true},
		{name: "running show, 6 days", kind: KindSeries, status: "Returning Series", fetchedAgo: 6 * day},
		{name: "running show, just over 7 days", kind: KindSeries, status: "In Production", fetchedAgo: 7*day + time.Second, wantDue: true},
		{name: "running show, exactly 7 days", kind: KindSeries, status: "Returning Series", fetchedAgo: 7 * day},
		{name: "ended show, 8 days", kind: KindSeries, status: "Ended", fetchedAgo: 8 * day},
		{name: "ended show, 29 days", kind: KindSeries, status: "Ended", fetchedAgo: 29 * day},
		{name: "ended show, 31 days", kind: KindSeries, status: "Ended", fetchedAgo: 31 * day, wantDue: true},
		{name: "canceled show, 31 days", kind: KindSeries, status: "Canceled", fetchedAgo: 31 * day, wantDue: true},
		{name: "canceled show, 8 days", kind: KindSeries, status: "Canceled", fetchedAgo: 8 * day},
		{name: "new movie, 8 days", kind: KindMovie, release: date(30), fetchedAgo: 8 * day, wantDue: true},
		{name: "new movie, 6 days", kind: KindMovie, release: date(30), fetchedAgo: 6 * day},
		{name: "movie released 89 days ago, 8 days", kind: KindMovie, release: date(89), fetchedAgo: 8 * day, wantDue: true},
		{name: "movie released 91 days ago, 8 days", kind: KindMovie, release: date(91), fetchedAgo: 8 * day},
		{name: "old movie, 29 days", kind: KindMovie, release: date(400), fetchedAgo: 29 * day},
		{name: "old movie, 31 days", kind: KindMovie, release: date(400), fetchedAgo: 31 * day, wantDue: true},
		{name: "unreleased movie, 8 days", kind: KindMovie, release: now.Add(20 * day).Format("2006-01-02"), fetchedAgo: 8 * day, wantDue: true},
		{name: "movie without a release date, 31 days", kind: KindMovie, fetchedAgo: 31 * day, wantDue: true},
		{name: "movie without a release date, 8 days", kind: KindMovie, fetchedAgo: 8 * day},
		{name: "never fetched", kind: KindMovie, release: date(400), fetchedAt: 1, wantDue: true},

		// Airing: the refresh is due 6 h after the air date's midnight, once.
		{name: "aired, not fetched since", kind: KindSeries, status: "Returning Series", nextAir: "2026-11-01", fetchedAgo: 2 * day, wantDue: true},
		{name: "aired, fetched since", kind: KindSeries, status: "Returning Series", nextAir: "2026-11-01", fetchedAgo: day},
		{name: "airs today, 06:00 passed", kind: KindSeries, status: "Returning Series", nextAir: "2026-11-02", fetchedAgo: 3 * time.Hour},
		{name: "airs today, fetched before 06:00", kind: KindSeries, status: "Returning Series", nextAir: "2026-11-02", fetchedAgo: 12 * time.Hour, wantDue: true},
		{name: "airs tomorrow", kind: KindSeries, status: "Returning Series", nextAir: "2026-11-03", fetchedAgo: 2 * day},
		{name: "ended show with a stale air date", kind: KindSeries, status: "Ended", nextAir: "2026-11-01", fetchedAgo: 2 * day, wantDue: true},

		// Not candidates.
		{name: "no tmdb id", kind: KindMovie, release: date(400), fetchedAgo: 100 * day},
		{name: "not in the library", kind: KindSeries, status: "Returning Series", fetchedAgo: 100 * day},
	}
	byTitle := map[int64]string{}
	want := map[string]bool{}
	for i, sp := range specs {
		in := tmdbMovie(int64(1000+i), sp.name, 2000)
		in.Kind, in.Status, in.ReleaseDate, in.NextAirDate = sp.kind, sp.status, sp.release, sp.nextAir
		in.FetchedAt = ago(sp.fetchedAgo)
		if sp.fetchedAt != 0 {
			in.FetchedAt = sp.fetchedAt
		}
		if sp.name == "no tmdb id" {
			in.TMDBID, in.Source = 0, SourceNFO
		}
		inLibrary := sp.name != "not in the library"
		var id int64
		var err error
		switch {
		case !inLibrary:
			id, err = s.UpsertTitle(ctx, in)
		case sp.kind == KindMovie:
			_, id = addMovie(t, s, fmt.Sprintf("/m/%d.mkv", i), in)
		default:
			id = addShow(t, s, sp.name, []int{1}, in)
		}
		if err != nil {
			t.Fatalf("%s: %v", sp.name, err)
		}
		byTitle[id] = sp.name
		want[sp.name] = sp.wantDue
	}

	due, err := s.TitlesDueForRefresh(ctx, now, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range due {
		got[byTitle[d.ID]] = true
	}
	for _, sp := range specs {
		if got[sp.name] != sp.wantDue {
			t.Errorf("%s: due = %v, want %v", sp.name, got[sp.name], sp.wantDue)
		}
	}
	if len(due) == 0 {
		t.Fatal("nothing is due: the fixture is wrong")
	}
	if !slices.IsSortedFunc(due, func(a, b Title) int { return int(a.FetchedAt - b.FetchedAt) }) {
		t.Error("due titles are not oldest first")
	}
	if limited, _ := s.TitlesDueForRefresh(ctx, now, 3); len(limited) != 3 || limited[0].ID != due[0].ID {
		t.Errorf("limit 3 returned %d titles", len(limited))
	}

	// The airing boundary is the minute: 05:59 is early, 06:00 is on time.
	var airing int64
	for id, name := range byTitle {
		if name == "airs today, fetched before 06:00" {
			airing = id
		}
	}
	isDue := func(at time.Time) bool {
		t.Helper()
		due, err := s.TitlesDueForRefresh(ctx, at, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(due, func(d Title) bool { return d.ID == airing })
	}
	if isDue(time.Date(2026, 11, 2, 5, 59, 59, 0, time.UTC)) {
		t.Error("an episode that airs today is refreshed before 06:00")
	}
	if !isDue(time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)) {
		t.Error("an episode that aired is not refreshed at 06:00")
	}

	refs, err := s.LibraryTitleRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range refs {
		if n := byTitle[r.ID]; n == "no tmdb id" || n == "not in the library" || r.TMDBID == 0 {
			t.Errorf("LibraryTitleRefs includes %q: %+v", n, r)
		}
	}
	if len(refs) != len(specs)-2 {
		t.Errorf("LibraryTitleRefs = %d refs, want %d", len(refs), len(specs)-2)
	}
	if !reflect.DeepEqual(refs[0], TitleRef{ID: refs[0].ID, Kind: KindSeries, TMDBID: 1000, Source: SourceTMDB, Locked: false}) {
		t.Errorf("first ref = %+v", refs[0])
	}
}

// Readers see a title complete or not at all: UpsertTitle replaces genres and
// credits in one transaction and the detail is read from one snapshot. Run it
// with -race.
func TestConcurrentTitleWritesAndReads(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	in := func(gen int) *TitleInput {
		x := tmdbMovie(1, "A", 2000)
		x.Overview = fmt.Sprintf("gen %d", gen)
		for i := range 8 {
			x.Genres = append(x.Genres, Genre{ID: int64(100 + i), Name: fmt.Sprintf("G%d-%d", gen%2, i)})
			x.Credits = append(x.Credits, castCredit(int64(50+i), fmt.Sprintf("P%d-%d", gen%2, i), "x", i))
		}
		return x
	}
	_, id := addMovie(t, s, "/m/a.mkv", in(0))

	end := time.Now().Add(600 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	wg.Go(func() {
		for gen := 1; time.Now().Before(end); gen++ {
			if _, err := s.UpsertTitle(ctx, in(gen)); err != nil {
				errs <- err
				return
			}
		}
	})
	for range 3 {
		wg.Go(func() {
			for time.Now().Before(end) {
				d, err := s.GetTitleDetail(ctx, id)
				if err != nil {
					errs <- err
					return
				}
				if len(d.Cast) != 8 || len(d.Genres) != 8 {
					errs <- fmt.Errorf("partial title: %d cast, %d genres", len(d.Cast), len(d.Genres))
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
