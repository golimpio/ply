package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// openAtVersion returns a store (and its path, in a directory with a space)
// whose database has only the first n migrations applied: what an older ply
// left behind. Nothing else touches it, so Open on the path upgrades it.
func openAtVersion(t *testing.T, n int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Application Support", "ply", "ply.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, e := range entries { // ReadDir sorts by name
		num, _, _ := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(num)
		if err != nil {
			t.Fatal(err)
		}
		if version > n {
			break
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.applyMigration(ctx, version, string(body)); err != nil {
			t.Fatalf("migration %s: %v", e.Name(), err)
		}
		applied++
	}
	if applied != n {
		t.Fatalf("applied %d migrations, wanted %d", applied, n)
	}
	return s, path
}

// insertOldItem inserts an item with the columns migrations 1 and 2 know.
func insertOldItem(t *testing.T, s *Store, path, title string) int64 {
	t.Helper()
	res, err := s.db.Exec(`
		INSERT INTO items (kind, title, year, path, root, size, mtime, container, duration_sec, bitrate, added_at, updated_at)
		VALUES ('movie', ?, 2020, ?, '/media', 100, 1700000000000000000, 'mp4', 7200.5, 5000, 10, 20)`, title, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func columns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return cols
}

func TestMigration003KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	old, path := openAtVersion(t, 2) // M1-M5 schema
	movieID := insertOldItem(t, old, "/media/Old Movie (2020).mp4", "Old Movie")
	res, err := old.db.Exec(`
		INSERT INTO items (kind, title, year, show, season, episode, path, root, size, mtime, added_at, updated_at)
		VALUES ('episode', 'Pilot', 0, 'Old Show', 1, 1, '/media/Old Show/S01E01.mkv', '/media', 10, 5, 10, 20)`)
	if err != nil {
		t.Fatal(err)
	}
	epID, _ := res.LastInsertId()
	for _, q := range []string{
		`INSERT INTO streams (item_id, idx, type, codec) VALUES (` + strconv.FormatInt(movieID, 10) + `, 0, 'video', 'h264')`,
		`INSERT INTO subtitles_external (item_id, path, language, format, forced) VALUES (` + strconv.FormatInt(movieID, 10) + `, '/media/a.srt', 'en', 'srt', 1)`,
		`INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at) VALUES (` + strconv.FormatInt(movieID, 10) + `, 600, 7200, 0, 99)`,
		`INSERT INTO keyframes (item_id, times) VALUES (` + strconv.FormatInt(movieID, 10) + `, x'0000000000000000')`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Positive control: the old schema has none of the new columns.
	if cols := columns(t, old.db, "items"); slices.Contains(cols, "match_state") || slices.Contains(cols, "fingerprint") {
		t.Fatalf("version 2 items columns already have the M6 ones: %v", cols)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on an M5 database: %v", err)
	}
	defer s.Close()

	m, err := s.GetEntry(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Old Movie" || m.Year != 2020 || m.Path != "/media/Old Movie (2020).mp4" || m.Container != "mp4" ||
		m.DurationSec != 7200.5 || m.Bitrate != 5000 || m.AddedAt != 10 || m.UpdatedAt != 20 {
		t.Errorf("movie after upgrade: %+v", m.Item)
	}
	if len(m.Streams) != 1 || m.Streams[0].Codec != "h264" {
		t.Errorf("streams after upgrade: %+v", m.Streams)
	}
	if len(m.External) != 1 || !m.External[0].Forced || m.External[0].Language != "en" {
		t.Errorf("sidecar subtitles after upgrade: %+v", m.External)
	}
	if m.Progress == nil || m.Progress.PositionSec != 600 {
		t.Errorf("progress after upgrade: %+v", m.Progress)
	}
	if kf, ok, err := s.Keyframes(ctx, movieID); err != nil || !ok || len(kf) != 1 {
		t.Errorf("keyframes after upgrade: %v %v %v", kf, ok, err)
	}
	ep, err := s.GetEntry(ctx, epID)
	if err != nil || ep.Show != "Old Show" || ep.Season != 1 || ep.Episode != 1 {
		t.Errorf("episode after upgrade: %+v, %v", ep.Item, err)
	}

	// Existing rows enter the matching queue and carry no metadata.
	for _, e := range []Entry{m, ep} {
		if e.MatchState != MatchPending || e.TitleID != 0 || e.NeedsReview || e.Fingerprint != "" ||
			e.LocalPoster != "" || e.LocalBackdrop != "" || e.LocalThumb != "" {
			t.Errorf("item %d metadata defaults: %+v", e.ID, e.Item)
		}
	}
	due, err := s.DueItems(ctx, 1, 10)
	if err != nil || len(due) != 1 || due[0].ID != movieID {
		t.Errorf("DueItems after upgrade = %+v, %v; want the old movie", due, err)
	}
	dueShows, err := s.DueSeries(ctx, 1, 10)
	if err != nil || len(dueShows) != 1 || dueShows[0].Show != "Old Show" {
		t.Errorf("DueSeries after upgrade = %+v, %v; want the old show", dueShows, err)
	}

	var versions []int
	rows, err := s.db.Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if !slices.Equal(versions, []int{1, 2, 3, 4}) {
		t.Errorf("schema_version = %v, want [1 2 3 4]", versions)
	}
	var violations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Errorf("foreign key violations after upgrade: %d, %v", violations, err)
	}
}

func TestMigration003Schema(t *testing.T) {
	s, _ := openTemp(t)
	for table, want := range map[string][]string{
		"titles": {"id", "kind", "tmdb_id", "imdb_id", "tvdb_id", "title", "original_title", "sort_title", "year",
			"overview", "tagline", "release_date", "end_date", "status", "runtime_min", "rating", "vote_count",
			"certification", "poster", "backdrop", "logo", "collection_id", "source", "locked", "next_air_date",
			"fetched_at", "created_at", "updated_at"},
		"items": {"title_id", "match_state", "needs_review", "match_score", "match_attempts", "next_attempt_at",
			"local_poster", "local_backdrop", "local_thumb", "fingerprint"},
		"series_matches": {"show", "title_id", "match_state", "needs_review", "match_score", "match_attempts",
			"next_attempt_at", "local_poster", "local_backdrop"},
		"seasons":  {"title_id", "season", "name", "overview", "poster", "air_date"},
		"episodes": {"title_id", "season", "episode", "name", "overview", "air_date", "runtime_min", "still", "rating"},
		"people": {"id", "tmdb_id", "name", "profile", "biography", "birthday", "deathday", "place_of_birth",
			"known_for", "imdb_id", "fetched_at"},
		"credits":      {"title_id", "person_id", "kind", "job", "character", "ord"},
		"genres":       {"id", "name"},
		"title_genres": {"title_id", "genre_id"},
		"collections":  {"id", "tmdb_id", "name", "overview", "poster", "backdrop"},
		"settings":     {"key", "value"},
	} {
		got := columns(t, s.db, table)
		for _, c := range want {
			if !slices.Contains(got, c) {
				t.Errorf("%s has no column %s (columns: %v)", table, c, got)
			}
		}
	}

	// The indexes the queries rely on, including the ones the spec lists.
	for index, want := range map[string]string{
		"items_title": "title_id", "items_match": "match_state", "items_fingerprint": "size",
		"credits_person": "person_id", "title_genres_genre": "genre_id", "titles_collection": "collection_id",
		"people_name": "name",
	} {
		cols := indexColumns(t, s.db, index)
		if len(cols) == 0 || cols[0] != want {
			t.Errorf("index %s columns = %v, want it to start with %s", index, cols, want)
		}
	}
	if cols := indexColumns(t, s.db, "items_match"); !slices.Equal(cols, []string{"match_state", "next_attempt_at"}) {
		t.Errorf("items_match = %v", cols)
	}
	if cols := indexColumns(t, s.db, "items_fingerprint"); !slices.Equal(cols, []string{"size", "fingerprint"}) {
		t.Errorf("items_fingerprint = %v", cols)
	}
}

func indexColumns(t *testing.T, db *sql.DB, index string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols = append(cols, c)
	}
	return cols
}

func TestNewItemsArePendingAndFingerprintRoundTrips(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/media/a.mp4", "A")
	it.Fingerprint = "abc123"
	id, err := s.UpsertItem(ctx, it, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.MatchState != MatchPending || e.NeedsReview || e.TitleID != 0 {
		t.Errorf("new item metadata: %+v", e.Item)
	}
	if e.Fingerprint != "abc123" {
		t.Errorf("Fingerprint = %q, want abc123", e.Fingerprint)
	}
	// A changed file gets its new fingerprint, keeping its id.
	it.Fingerprint = "def456"
	if id2, err := s.UpsertItem(ctx, it, nil); err != nil || id2 != id {
		t.Fatalf("re-upsert = %d, %v", id2, err)
	}
	entries, err := s.ListItems(ctx, ListFilter{})
	if err != nil || len(entries) != 1 || entries[0].Fingerprint != "def456" || entries[0].MatchState != MatchPending {
		t.Errorf("ListItems after re-upsert: %+v, %v", entries, err)
	}
}
