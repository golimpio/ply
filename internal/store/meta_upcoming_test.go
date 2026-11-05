package store

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

// addAiring links a show of the library to a series title whose next episode
// airs on nextAir (empty: it has none).
func addAiring(t *testing.T, s *Store, show string, tmdb int64, nextAir string, next Episode) int64 {
	t.Helper()
	in := tmdbSeries(tmdb, show, 2020)
	in.NextAirDate = nextAir
	in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = next.Season, next.Number, next.Name
	return addShow(t, s, show, []int{1}, in)
}

func upcomingShows(t *testing.T, s *Store, today string, limit int) []string {
	t.Helper()
	got, err := s.Upcoming(context.Background(), today, limit)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range got {
		names = append(names, u.Show+" "+u.NextAirDate)
	}
	return names
}

func TestUpcoming(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	const today = "2026-11-02"

	zeta := addAiring(t, s, "Zeta", 1, "2026-11-05", Episode{Season: 2, Number: 5, Name: "Big One"})
	addAiring(t, s, "Beta", 2, "2026-11-03", Episode{Season: 1, Number: 1, Name: "Beta Opens"})
	addAiring(t, s, "Alpha", 3, "2026-11-03", Episode{})
	addAiring(t, s, "Today Show", 4, today, Episode{Season: 3, Number: 4, Name: "Tonight"})
	addAiring(t, s, "Yesterday Show", 5, "2026-11-01", Episode{Season: 1, Number: 2, Name: "Gone By"})
	addAiring(t, s, "Between Seasons", 6, "", Episode{})
	// A year on is later, not earlier, whatever its month and day.
	addAiring(t, s, "Next Year", 7, "2027-01-04", Episode{Season: 1, Number: 1, Name: "New Year"})

	// Titles that are not upcoming however soon they air: one no file points to, one
	// whose files are gone, and a movie.
	ghost := tmdbSeries(8, "Ghost", 2020)
	ghost.NextAirDate = "2026-11-02"
	ghostID, err := s.UpsertTitle(ctx, ghost)
	if err != nil {
		t.Fatal(err)
	}
	gone := addAiring(t, s, "Deleted Show", 9, "2026-11-02", Episode{Season: 1, Number: 1})
	items, err := s.ShowItems(ctx, "Deleted Show")
	if err != nil || len(items) != 1 {
		t.Fatalf("ShowItems = %+v, %v", items, err)
	}
	mov := tmdbMovie(10, "Airing Movie", 2020)
	mov.NextAirDate = "2026-11-02"
	addMovie(t, s, "/media/m.mkv", mov)

	// Positive controls: all of them are in the database with a date that would qualify.
	for name, id := range map[string]int64{"ghost": ghostID, "deleted": gone} {
		if row, err := s.GetTitle(ctx, id); err != nil || row.NextAirDate != "2026-11-02" {
			t.Fatalf("%s title = %+v, %v", name, row, err)
		}
	}
	if got := upcomingShows(t, s, today, 50); !slices.Contains(got, "Deleted Show 2026-11-02") {
		t.Fatalf("before its files are deleted the show is upcoming: %q", got)
	}
	var ids []int64
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if err := s.DeleteItems(ctx, ids); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"Today Show 2026-11-02", // today counts
		"Alpha 2026-11-03",      // same day: by name
		"Beta 2026-11-03",
		"Zeta 2026-11-05",
		"Next Year 2027-01-04",
	}
	if got := upcomingShows(t, s, today, 50); !slices.Equal(got, want) {
		t.Errorf("Upcoming(%s) = %q\nwant %q", today, got, want)
	}
	// Tomorrow the show that aired today is gone; the day after, tomorrow's two are.
	if got := upcomingShows(t, s, "2026-11-03", 50); !slices.Equal(got, want[1:]) {
		t.Errorf("Upcoming(2026-11-03) = %q, want %q", got, want[1:])
	}
	if got := upcomingShows(t, s, "2026-11-04", 50); !slices.Equal(got, want[3:]) {
		t.Errorf("Upcoming(2026-11-04) = %q, want %q", got, want[3:])
	}
	if got := upcomingShows(t, s, "2027-01-05", 50); len(got) != 0 {
		t.Errorf("Upcoming after every date = %q, want none", got)
	}
	// The limit keeps the soonest.
	if got := upcomingShows(t, s, today, 3); !slices.Equal(got, want[:3]) {
		t.Errorf("Upcoming(limit 3) = %q, want %q", got, want[:3])
	}

	got, err := s.Upcoming(ctx, today, 50)
	if err != nil {
		t.Fatal(err)
	}
	byShow := map[string]UpcomingShow{}
	for _, u := range got {
		byShow[u.Show] = u
	}
	z := byShow["Zeta"]
	if z.ID != zeta || z.Kind != KindSeries || z.Title != "Zeta" || z.Poster != "tmdb:/p1.jpg" || z.Backdrop != "tmdb:/b1.jpg" || z.ItemID != 0 {
		t.Errorf("Zeta's card = %+v", z.TitleCard)
	}
	if e := z.NextEpisode; e.Season != 2 || e.Number != 5 || e.Name != "Big One" || e.AirDate != "2026-11-05" {
		t.Errorf("Zeta's next episode = %+v", e)
	}
	if e := byShow["Alpha"].NextEpisode; e.Number != 0 || e.Name != "" {
		t.Errorf("a show with no episode named: %+v", e)
	}
	// Sidecar artwork of the show beats TMDB's, as on every card.
	if err := s.SetSeriesArt(ctx, "Zeta", Art{Poster: "/media/Zeta/poster.jpg"}); err != nil {
		t.Fatal(err)
	}
	sm, err := s.GetSeriesMatch(ctx, "Zeta")
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.Upcoming(ctx, today, 50)
	for _, u := range got {
		if u.Show == "Zeta" && u.Poster != LocalRef(ArtSeries, sm.ID, "poster") {
			t.Errorf("Zeta's poster = %q, want the sidecar", u.Poster)
		}
	}
}

func TestUpcomingOfAnEmptyLibrary(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Upcoming(context.Background(), "2026-11-02", 50)
	if err != nil || len(got) != 0 {
		t.Errorf("Upcoming = %+v, %v", got, err)
	}
}

// One title linked by two show names (two folders) is one entry.
func TestUpcomingListsATitleOnce(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id := addAiring(t, s, "Show", 1, "2026-11-05", Episode{Season: 1, Number: 3, Name: "Three"})
	if _, err := s.UpsertItem(ctx, episode("/media/Show (2020)/S01E01.mkv", "Show (2020)", 1, 1), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "Show (2020)", Match{TitleID: id, State: MatchMatched}); err != nil {
		t.Fatal(err)
	}
	if got := upcomingShows(t, s, "2026-11-02", 50); !slices.Equal(got, []string{"Show 2026-11-05"}) {
		t.Errorf("Upcoming = %q, want the title once, under its first show name", got)
	}
}

func TestNextEpisodeIsStoredAndCleared(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	in := tmdbSeries(1396, "Breaking Bad", 2008)
	in.NextAirDate = "2030-01-02"
	in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = 6, 7, "Ozymandias"
	id := addShow(t, s, "Breaking Bad", []int{1}, in)

	row, err := s.GetTitle(ctx, id)
	if err != nil || row.NextAirDate != "2030-01-02" || row.NextEpisodeSeason != 6 || row.NextEpisodeNumber != 7 || row.NextEpisodeName != "Ozymandias" {
		t.Fatalf("title = %+v, %v", row, err)
	}
	// The detail and the card carry the date too.
	d, err := s.GetTitleDetail(ctx, id)
	if err != nil || d.NextAirDate != "2030-01-02" || d.TitleCard.NextAirDate != "2030-01-02" {
		t.Errorf("detail next air date = %q / %q, %v", d.NextAirDate, d.TitleCard.NextAirDate, err)
	}
	cards, err := s.Titles(ctx, TitleFilter{})
	if err != nil || len(cards) != 1 || cards[0].NextAirDate != "2030-01-02" {
		t.Errorf("Titles = %+v, %v", cards, err)
	}

	// A refresh that finds nothing more to air clears all of it.
	again := tmdbSeries(1396, "Breaking Bad", 2008)
	if _, err := s.UpsertTitle(ctx, again); err != nil {
		t.Fatal(err)
	}
	row, _ = s.GetTitle(ctx, id)
	if row.NextAirDate != "" || row.NextEpisodeSeason != 0 || row.NextEpisodeNumber != 0 || row.NextEpisodeName != "" {
		t.Errorf("after a fetch with no next episode: %+v", row)
	}
	if got := upcomingShows(t, s, "2000-01-01", 50); len(got) != 0 {
		t.Errorf("Upcoming = %q, want none", got)
	}
}

func TestMigration004KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 3) // M6 as first shipped
	// Positive control: version 3 has the air date and not the episode.
	if cols := columns(t, old.rw, "titles"); !slices.Contains(cols, "next_air_date") || slices.Contains(cols, "next_episode_name") {
		t.Fatalf("version 3 titles columns: %v", cols)
	}
	now := int64(1_700_000_000)
	exec := func(q string, args ...any) sql.Result {
		t.Helper()
		res, err := old.rw.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res
	}
	exec(`INSERT INTO titles (kind, tmdb_id, title, sort_title, year, next_air_date, poster, source, locked, fetched_at, created_at, updated_at)
		VALUES ('series', 1396, 'Old Show', 'Old Show', 2008, '2026-11-08', 'tmdb:/old.jpg', 'tmdb', 1, ?, ?, ?)`, now, now, now)
	exec(`INSERT INTO titles (kind, tmdb_id, title, source, created_at, updated_at) VALUES ('movie', 603, 'Old Movie', 'tmdb', ?, ?)`, now, now)
	exec(`INSERT INTO items (kind, title, show, season, episode, path, root, size, mtime, added_at, updated_at)
		VALUES ('episode', 'Pilot', 'Old Show', 1, 1, '/media/Old Show/S01E01.mkv', '/media', 10, 5, 10, 20)`)
	exec(`INSERT INTO series_matches (show, title_id, match_state) VALUES ('Old Show', 1, 'matched')`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a version 3 database: %v", err)
	}
	defer s.Close()

	row, err := s.GetTitle(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "Old Show" || row.Year != 2008 || row.NextAirDate != "2026-11-08" || row.Poster != "tmdb:/old.jpg" ||
		!row.Locked || row.FetchedAt != now || row.CreatedAt != now {
		t.Errorf("title after upgrade: %+v", row)
	}
	if row.NextEpisodeSeason != 0 || row.NextEpisodeNumber != 0 || row.NextEpisodeName != "" {
		t.Errorf("the new columns of an old row: %+v; want unknown", row)
	}
	if movie, err := s.GetTitle(ctx, 2); err != nil || movie.Title != "Old Movie" {
		t.Errorf("movie after upgrade: %+v, %v", movie, err)
	}
	// The old row is upcoming with a date and no episode, until it is next fetched.
	got, err := s.Upcoming(ctx, "2026-11-02", 50)
	if err != nil || len(got) != 1 || got[0].Show != "Old Show" || got[0].NextAirDate != "2026-11-08" || got[0].NextEpisode.Number != 0 {
		t.Errorf("Upcoming after upgrade = %+v, %v", got, err)
	}
	// And a fetch fills the new columns.
	in := tmdbSeries(1396, "Old Show", 2008)
	in.NextAirDate, in.NextEpisodeSeason, in.NextEpisodeNumber, in.NextEpisodeName = "2026-11-08", 3, 4, "Please Hold"
	if _, err := s.UpsertTitle(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Upcoming(ctx, "2026-11-02", 50); len(got) != 1 || got[0].NextEpisode != (Episode{Season: 3, Number: 4, Name: "Please Hold", AirDate: "2026-11-08"}) {
		t.Errorf("Upcoming after a fetch = %+v", got)
	}

	var versions []int
	rows, err := s.rw.Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if want := migrationNumbers(t); !slices.Equal(versions, want) || !slices.Contains(versions, 8) {
		t.Errorf("schema_version = %v, want %v (and 8, the TVDB one)", versions, want)
	}
	var violations int
	if err := s.rw.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Errorf("foreign key violations after upgrade: %d, %v", violations, err)
	}
	for _, c := range []string{"next_episode_season", "next_episode_number", "next_episode_name"} {
		if !slices.Contains(columns(t, s.rw, "titles"), c) {
			t.Errorf("titles has no column %s", c)
		}
	}
}
