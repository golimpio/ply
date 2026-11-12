package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// extraReads and extraWrites are routed calls another test file adds (the
// background queue's, in jobs_test.go), so that each table of methods lives with
// its methods.
var extraReads, extraWrites []routed

// chunkSpec is the batch size the bulk writes are specified to have: at most 500
// rows per transaction. It is a number of its own, not writeChunk, so that changing
// writeChunk fails the tests rather than moving them along.
const chunkSpec = 500

// seedItems inserts n movies under root, each with one stream and a progress row,
// in one transaction (it goes round the writer: it is setup), and returns their
// ids, lowest first.
func seedItems(t *testing.T, s *Store, root string, n int) []int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO items (kind, title, path, root, size, mtime, added_at, updated_at)
		SELECT 'movie', 'Seed ' || i, ? || '/' || i || '.mp4', ?, 100 + i, 1, 1, 1 FROM seq`, n, root, root); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO streams (item_id, idx, type, codec) SELECT id, 0, 'video', 'h264' FROM items WHERE root = ?`,
		`INSERT INTO progress (item_id, position_sec, duration_sec, updated_at) SELECT id, 100, 7200, 5 FROM items WHERE root = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, root); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM items WHERE root = ? ORDER BY id`, root)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != n {
		t.Fatalf("seeded %d items, want %d", len(ids), n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// grants records, in order, what takes the writer: "high" and "low" for the
// priority of each write that does.
type grants struct {
	mu  sync.Mutex
	log []string
}

func (g *grants) add(v string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log = append(g.log, v)
}

func (g *grants) all() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.log)
}

func (g *grants) count(v string) int {
	n := 0
	for _, e := range g.all() {
		if e == v {
			n++
		}
	}
	return n
}

// record installs a grants log on the store's writer lock.
func record(s *Store) *grants {
	g := &grants{}
	s.wl.granted = func(p prio) { g.add(map[prio]string{prioHigh: "high", prioLow: "low"}[p]) }
	return g
}

// testCtx is t.Context() with a deadline, so that a test that would wait for ever
// (on a pool with too few connections, say) fails instead.
func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Every connection of both pools gets the pragmas, not only the first one: the
// read pool opens a new connection whenever its busy ones are all taken.
func TestEveryConnectionHasThePragmas(t *testing.T) {
	s, _ := openTemp(t)
	ctx := testCtx(t)

	if got := s.rw.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("the write pool has %d connections, want exactly 1", got)
	}
	if got, want := s.ro.Stats().MaxOpenConnections, readConns(); got != want || got < 2 {
		t.Errorf("the read pool has %d connections, want %d (at least 2)", got, want)
	}

	for _, pool := range []struct {
		name string
		db   *sql.DB
	}{{"rw", s.rw}, {"ro", s.ro}} {
		readOnly := int64(0)
		if pool.name == "ro" {
			readOnly = 1
		}
		n := pool.db.Stats().MaxOpenConnections
		var (
			wg      sync.WaitGroup // every goroutine holds its connection before any reads
			holding sync.WaitGroup
			mu      sync.Mutex
			bad     []string
		)
		holding.Add(n)
		for i := range n {
			wg.Go(func() {
				c, err := pool.db.Conn(ctx)
				holding.Done()
				if err != nil {
					mu.Lock()
					bad = append(bad, fmt.Sprintf("conn %d: %v", i, err))
					mu.Unlock()
					return
				}
				defer c.Close()
				holding.Wait() // n connections are open at once: n different ones
				for pragma, want := range map[string]any{
					"journal_mode": "wal", "busy_timeout": int64(5000), "foreign_keys": int64(1),
					"synchronous": int64(1), // NORMAL; the default is 2, FULL
					"query_only":  readOnly,
				} {
					var got any
					if err := c.QueryRowContext(ctx, `PRAGMA `+pragma).Scan(&got); err != nil || got != want {
						mu.Lock()
						bad = append(bad, fmt.Sprintf("conn %d: PRAGMA %s = %v (%v), want %v", i, pragma, got, err, want))
						mu.Unlock()
					}
				}
			})
		}
		wg.Wait()
		if len(bad) > 0 {
			t.Errorf("%s pool (%d connections at once):\n%s", pool.name, n, strings.Join(bad, "\n"))
		}
	}
}

// The read pool cannot write: SQLite refuses it (query_only), so a write routed
// to it fails loudly instead of racing the writer.
func TestReadPoolRefusesWrites(t *testing.T) {
	s, _ := openTemp(t)
	ctx := testCtx(t)

	// Positive control: the same statement works on the write pool, and what it
	// wrote is there for a read that comes after.
	if _, err := s.rw.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('control', '1')`); err != nil {
		t.Fatalf("control: the writer cannot write: %v", err)
	}
	if v, ok, err := s.GetSetting(ctx, "control"); err != nil || !ok || v != "1" {
		t.Fatalf("control: a read after the write sees %q, %v, %v", v, ok, err)
	}

	c, err := s.ro.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, q := range []string{
		`INSERT INTO settings (key, value) VALUES ('x', '1')`,
		`UPDATE settings SET value = '2'`,
		`DELETE FROM settings`,
		`CREATE TABLE nope (id INTEGER)`,
	} {
		if _, err := c.ExecContext(ctx, q); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
			t.Errorf("%s on a reader = %v, want a read-only error", q, err)
		}
	}
	// The same through a transaction, which is how the store reads in snapshots.
	tx, err := s.ro.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('x', '1')`); err == nil {
		t.Error("a snapshot wrote")
	}
	tx.Rollback() // the pool may have only two connections: one is c's
	if _, ok, _ := s.GetSetting(ctx, "x"); ok {
		t.Error("the refused write is visible")
	}
}

// A call of every Store method, with arguments that get it to its first SQL
// statement. prio is the class of a write.
type routed struct {
	name string
	call func(ctx context.Context, s *Store) error
	prio prio // writes only
}

func reads() []routed {
	nf := func(err error) error { // what the call finds is not the point
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return append([]routed{
		{name: "Folders", call: func(ctx context.Context, s *Store) error { _, err := s.Folders(ctx); return err }},
		{name: "GetFolder", call: func(ctx context.Context, s *Store) error { _, err := s.GetFolder(ctx, 1); return nf(err) }},
		{name: "CountItemsByRoot", call: func(ctx context.Context, s *Store) error { _, err := s.CountItemsByRoot(ctx); return err }},
		{name: "RootItems", call: func(ctx context.Context, s *Store) error { _, err := s.RootItems(ctx, "/nowhere"); return err }},
		{name: "PendingMatches", call: func(ctx context.Context, s *Store) error { _, err := s.PendingMatches(ctx); return err }},
		{name: "ArchivedProgress", call: func(ctx context.Context, s *Store) error { _, err := s.ArchivedProgress(ctx); return err }},
		{name: "GetEntry", call: func(ctx context.Context, s *Store) error { _, err := s.GetEntry(ctx, 1); return nf(err) }},
		{name: "ListItems", call: func(ctx context.Context, s *Store) error { _, err := s.ListItems(ctx, ListFilter{}); return err }},
		{name: "Shows", call: func(ctx context.Context, s *Store) error { _, err := s.Shows(ctx); return err }},
		{name: "FileStates", call: func(ctx context.Context, s *Store) error { _, err := s.FileStates(ctx); return err }},
		{name: "CountItems", call: func(ctx context.Context, s *Store) error { _, err := s.CountItems(ctx); return err }},
		{name: "Keyframes", call: func(ctx context.Context, s *Store) error { _, _, err := s.Keyframes(ctx, 1); return err }},
		{name: "GetTitle", call: func(ctx context.Context, s *Store) error { _, err := s.GetTitle(ctx, 1); return nf(err) }},
		{name: "GetSetting", call: func(ctx context.Context, s *Store) error { _, _, err := s.GetSetting(ctx, "k"); return err }},
		{name: "SettingsWithPrefix", call: func(ctx context.Context, s *Store) error { _, err := s.SettingsWithPrefix(ctx, "k"); return err }},
		{name: "ItemsBelow", call: func(ctx context.Context, s *Store) error { _, err := s.ItemsBelow(ctx, "/nowhere"); return err }},
		{name: "TypedSince", call: func(ctx context.Context, s *Store) error { _, err := s.TypedSince(ctx); return err }},
		{name: "TitlesDueForRefresh", call: func(ctx context.Context, s *Store) error {
			_, err := s.TitlesDueForRefresh(ctx, time.Now(), 10)
			return err
		}},
		{name: "AiringTitlesDue", call: func(ctx context.Context, s *Store) error {
			_, err := s.AiringTitlesDue(ctx, time.Now(), 10)
			return err
		}},
		{name: "LibraryTitleRefs", call: func(ctx context.Context, s *Store) error { _, err := s.LibraryTitleRefs(ctx); return err }},
		{name: "TitleBriefs", call: func(ctx context.Context, s *Store) error { _, err := s.TitleBriefs(ctx, []int64{1}); return err }},
		{name: "ShowItems", call: func(ctx context.Context, s *Store) error { _, err := s.ShowItems(ctx, "x"); return err }},
		{name: "ShowSeasons", call: func(ctx context.Context, s *Store) error { _, err := s.ShowSeasons(ctx, "x"); return err }},
		{name: "SeasonsWithFiles", call: func(ctx context.Context, s *Store) error { _, err := s.SeasonsWithFiles(ctx, 1); return err }},
		{name: "SeasonsNeedingFetchAll", call: func(ctx context.Context, s *Store) error { _, err := s.SeasonsNeedingFetchAll(ctx); return err }},
		{name: "GetItemMatch", call: func(ctx context.Context, s *Store) error { _, err := s.GetItemMatch(ctx, 1); return nf(err) }},
		{name: "DueItems", call: func(ctx context.Context, s *Store) error { _, err := s.DueItems(ctx, 1, 10); return err }},
		{name: "DueSeries", call: func(ctx context.Context, s *Store) error { _, err := s.DueSeries(ctx, 1, 10); return err }},
		{name: "GetSeriesMatch", call: func(ctx context.Context, s *Store) error { _, err := s.GetSeriesMatch(ctx, "x"); return nf(err) }},
		{name: "SeriesMatches", call: func(ctx context.Context, s *Store) error { _, err := s.SeriesMatches(ctx); return err }},
		{name: "CountMatches", call: func(ctx context.Context, s *Store) error { _, err := s.CountMatches(ctx); return err }},
		{name: "LocalArt", call: func(ctx context.Context, s *Store) error {
			_, err := s.LocalArt(ctx, ArtItem, 1, "poster")
			return nf(err)
		}},
		{name: "Titles", call: func(ctx context.Context, s *Store) error { _, err := s.Titles(ctx, TitleFilter{}); return err }},
		{name: "TitleCards", call: func(ctx context.Context, s *Store) error { _, err := s.TitleCards(ctx, []int64{1}); return err }},
		{name: "Upcoming", call: func(ctx context.Context, s *Store) error { _, err := s.Upcoming(ctx, "2020-01-01", 10); return err }},
		{name: "GetTitleDetail", call: func(ctx context.Context, s *Store) error { _, err := s.GetTitleDetail(ctx, 1); return nf(err) }},
		{name: "Genres", call: func(ctx context.Context, s *Store) error { _, err := s.Genres(ctx); return err }},
		{name: "GetCollection", call: func(ctx context.Context, s *Store) error { _, err := s.GetCollection(ctx, 1); return nf(err) }},
		{name: "MergedPerson", call: func(ctx context.Context, s *Store) error { _, err := s.MergedPerson(ctx, 1); return nf(err) }},
		{name: "GetPerson", call: func(ctx context.Context, s *Store) error { _, err := s.GetPerson(ctx, 1); return nf(err) }},
		{name: "GetTitleVideos", call: func(ctx context.Context, s *Store) error { _, err := s.GetTitleVideos(ctx, 1); return nf(err) }},
		{name: "Devices", call: func(ctx context.Context, s *Store) error { _, err := s.Devices(ctx); return err }},
		{name: "Schedules", call: func(ctx context.Context, s *Store) error { _, err := s.Schedules(ctx); return err }},
		{name: "SearchPeople", call: func(ctx context.Context, s *Store) error { _, err := s.SearchPeople(ctx, "x", 10); return err }},
		{name: "ReviewList", call: func(ctx context.Context, s *Store) error { _, err := s.ReviewList(ctx); return err }},
		{name: "Seasons", call: func(ctx context.Context, s *Store) error { _, err := s.Seasons(ctx, 1); return err }},
		{name: "Episodes", call: func(ctx context.Context, s *Store) error { _, err := s.Episodes(ctx, 1); return err }},
		{name: "GetEpisode", call: func(ctx context.Context, s *Store) error { _, err := s.GetEpisode(ctx, 1, 1, 1); return nf(err) }},
		{name: "SeasonsNeedingFetch", call: func(ctx context.Context, s *Store) error { _, err := s.SeasonsNeedingFetch(ctx, 1); return err }},
		{name: "PathsWithFingerprint", call: func(ctx context.Context, s *Store) error { _, err := s.PathsWithFingerprint(ctx, 1, "f"); return err }},
		{name: "WithoutFingerprint", call: func(ctx context.Context, s *Store) error { _, err := s.WithoutFingerprint(ctx, 0, 10); return err }},
		{name: "Continue", call: func(ctx context.Context, s *Store) error { _, err := s.Continue(ctx, 10); return err }},
		{name: "FilmWatches", call: func(ctx context.Context, s *Store) error { _, err := s.FilmWatches(ctx, []int64{1}); return err }},
		{name: "GetTitleByTVDB", call: func(ctx context.Context, s *Store) error { _, err := s.GetTitleByTVDB(ctx, 1); return nf(err) }},
		{name: "TMDBSeriesWithTVDBID", call: func(ctx context.Context, s *Store) error { _, err := s.TMDBSeriesWithTVDBID(ctx, 10); return err }},
		{name: "MaxTitleID", call: func(ctx context.Context, s *Store) error { _, err := s.MaxTitleID(ctx); return err }},
		{name: "UnmappedEpisodeFiles", call: func(ctx context.Context, s *Store) error { _, err := s.UnmappedEpisodeFiles(ctx, 1); return err }},
		// M10: ratings.
		{name: "IMDbLibrary", call: func(ctx context.Context, s *Store) error { _, err := s.IMDbLibrary(ctx); return err }},
		{name: "DueRatings", call: func(ctx context.Context, s *Store) error { _, err := s.DueRatings(ctx, time.Now(), 10); return err }},
		{name: "IMDbShows", call: func(ctx context.Context, s *Store) error { _, err := s.IMDbShows(ctx); return err }},
		{name: "PlanEpisodeIMDb", call: func(ctx context.Context, s *Store) error {
			_, err := s.PlanEpisodeIMDb(ctx, map[string][]DatasetEpisode{"tt1": {{IMDbID: "tt2", Season: 1, Number: 1}}})
			return err
		}},
	}, extraReads...)
}

func writes() []routed {
	title := func() *TitleInput { return &TitleInput{Title: Title{Kind: KindMovie, Title: "T"}} }
	match := Match{State: MatchPending}
	e := func(err error) error { // an unknown row is a fine answer to a call made only to reach the writer
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrFolderExists) {
			return nil
		}
		return err
	}
	return append([]routed{
		// What a person is waiting for.
		{name: "SetProgress (not started)", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetProgress(ctx, 1, 10, 100)
			return err
		}},
		{name: "SetProgress (a position)", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetProgress(ctx, 1, 600, 7200)
			return e(err)
		}},
		// Marking watched: a person's click, which covers many files in one write.
		{name: "SetItemWatched", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetItemWatched(ctx, 1, true)
			return e(err)
		}},
		{name: "SetTitleWatched", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetTitleWatched(ctx, 1, true)
			return e(err)
		}},
		{name: "SetShowWatched", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetShowWatched(ctx, "x", false)
			return e(err)
		}},
		{name: "SetSeasonWatched", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetSeasonWatched(ctx, "x", 1, false)
			return e(err)
		}},
		{name: "SetSetting", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return s.SetSetting(ctx, "k", "v") }},
		{name: "DeleteSetting", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return s.DeleteSetting(ctx, "k") }},
		{name: "AddFolder", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.AddFolder(ctx, "/x", FolderMovies, false)
			return e(err)
		}},
		{name: "SetFolderKind", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetFolderKind(ctx, 1, FolderShows)) }},
		{name: "DeleteFolder", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.DeleteFolder(ctx, 1)) }},
		{name: "UpsertFlagFolder", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, _, err := s.UpsertFlagFolder(ctx, "/y", FolderMovies, "")
			return err
		}},
		{name: "SetKeyframes", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetKeyframes(ctx, 1, []float64{0})) }},
		{name: "PatchTitle", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.PatchTitle(ctx, 1, TitlePatch{})) }},
		{name: "SetPersonDetails", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetPersonDetails(ctx, 1, Person{}, 1)) }},
		{name: "SetTitleVideos", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetTitleVideos(ctx, 1, "tmdb:1", nil, 1)) }},
		{name: "PutDevice", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return s.PutDevice(ctx, Device{ID: "device-01"}, 1) }},
		{name: "TouchDevice", prio: prioLow, call: func(ctx context.Context, s *Store) error { return s.TouchDevice(ctx, "device-01", 2) }},
		{name: "PutSchedule", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			return s.PutSchedule(ctx, ScheduleRecord{Task: "scan.full", Enabled: true, AtMinute: 60})
		}},
		{name: "MarkScheduleRun", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.MarkScheduleRun(ctx, "scan.full", 2)) }},
		{name: "SetPersonDetailsMerged", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetPersonDetailsMerged(ctx, 1, Person{}, 1)
			return e(err)
		}},
		{name: "SetEpisodeOrder", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetEpisodeOrder(ctx, 1, OrderDVD)) }},
		{name: "SetItemMatch", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetItemMatch(ctx, 1, match)) }},
		{name: "SetSeriesMatch", prio: prioHigh, call: func(ctx context.Context, s *Store) error { return e(s.SetSeriesMatch(ctx, "x", match)) }},
		{name: "LinkItemTitle (a person's)", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, _, err := s.LinkItemTitle(ctx, 1, title(), Match{State: MatchManual}, "")
			return e(err)
		}},
		{name: "LinkSeriesTitle (a person's)", prio: prioHigh, call: func(ctx context.Context, s *Store) error {
			_, _, err := s.LinkSeriesTitle(ctx, "x", title(), Match{State: MatchManual}, "")
			return e(err)
		}},
		// The scanner, the metadata worker and the chunks of bulk writes.
		{name: "UpsertItem", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, err := s.UpsertItem(ctx, movie("/p.mp4", "P"), nil)
			return err
		}},
		{name: "DeleteItems", prio: prioLow, call: func(ctx context.Context, s *Store) error { return s.DeleteItems(ctx, []int64{1}) }},
		{name: "SetItemRoot", prio: prioLow, call: func(ctx context.Context, s *Store) error { return s.SetItemRoot(ctx, 1, "/r") }},
		{name: "ArchiveAndDeleteRoot", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, err := s.ArchiveAndDeleteRoot(ctx, "/nowhere", false)
			return err
		}},
		{name: "RestoreArchived", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, _, _, err := s.RestoreArchived(ctx, 1, "/p.mp4", 1)
			return err
		}},
		{name: "ForgetArchivedMatch", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return s.ForgetArchivedMatch(ctx, "/p.mp4", KindMovie)
		}},
		{name: "SetParsed", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return s.SetParsed(ctx, []ParsedRow{{ID: 1, Kind: KindMovie}})
		}},
		{name: "UpsertTitle", prio: prioLow, call: func(ctx context.Context, s *Store) error { _, err := s.UpsertTitle(ctx, title()); return err }},
		{name: "PruneMetadata", prio: prioLow, call: func(ctx context.Context, s *Store) error { _, err := s.PruneMetadata(ctx); return err }},
		{name: "SetItemEmbeddedCover", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.SetItemEmbeddedCover(ctx, 1, 2)) }},
		{name: "ReleasePending", prio: prioLow, call: func(ctx context.Context, s *Store) error { return s.ReleasePending(ctx) }},
		{name: "RequeueUnmatched", prio: prioLow, call: func(ctx context.Context, s *Store) error { _, err := s.RequeueUnmatched(ctx); return err }},
		{name: "SetItemMatchIfPending", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetItemMatchIfPending(ctx, 1, match)
			return e(err)
		}},
		{name: "SetSeriesMatchIfPending", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, err := s.SetSeriesMatchIfPending(ctx, "x", match)
			return e(err)
		}},
		{name: "TouchTitle", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.TouchTitle(ctx, 1, 1)) }},
		{name: "LinkItemTitle (the worker's)", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, _, err := s.LinkItemTitle(ctx, 1, title(), match, MatchPending)
			return e(err)
		}},
		{name: "LinkSeriesTitle (the worker's)", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, _, err := s.LinkSeriesTitle(ctx, "x", title(), match, MatchPending)
			return e(err)
		}},
		{name: "DeferItemMatch", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.DeferItemMatch(ctx, 1, 5)) }},
		{name: "DeferSeriesMatch", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.DeferSeriesMatch(ctx, "x", 5)) }},
		{name: "SetItemArt", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.SetItemArt(ctx, 1, Art{Poster: "/a"})) }},
		{name: "SetSeriesArt", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.SetSeriesArt(ctx, "x", Art{Poster: "/a"})) }},
		{name: "MoveItem", prio: prioLow, call: func(ctx context.Context, s *Store) error { return e(s.MoveItem(ctx, 1, movie("/q.mp4", "Q"))) }},
		{name: "SetFingerprint", prio: prioLow, call: func(ctx context.Context, s *Store) error { return s.SetFingerprint(ctx, 1, 1, 1, "f") }},
		{name: "SetExternalSubtitles", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return s.SetExternalSubtitles(ctx, 1, []ExternalSubtitle{{Path: "/a.srt", Format: "srt"}})
		}},
		// M10: ratings, all background work.
		{name: "SetIMDbRatings", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return s.SetIMDbRatings(ctx, nil, []IMDbEpisodeRating{{IMDbID: "tt1", Value: 8, Votes: 1}}, 1, nil)
		}},
		{name: "PruneIMDbRatings", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			_, err := s.PruneIMDbRatings(ctx, 1, nil)
			return err
		}},
		{name: "ReplaceMDBListRatings", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return e(s.ReplaceMDBListRatings(ctx, 1, nil, 1))
		}},
		{name: "TouchRatingsChecked", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return e(s.TouchRatingsChecked(ctx, 1, 1))
		}},
		{name: "ApplyEpisodeIMDb", prio: prioLow, call: func(ctx context.Context, s *Store) error {
			return s.ApplyEpisodeIMDb(ctx, EpisodeIDPlan{changes: []epChange{{titleID: 1, slot: episodeSlot{1, 1}, imdb: "tt1", src: IMDbFromDataset}}}, nil)
		}},
	}, extraWrites...)
}

// Every method of Store is either a read or a write, and the test below knows
// which: a method added without a line in reads or writes fails here, so that
// nobody ships one that was never routed.
func TestEveryStoreMethodIsRouted(t *testing.T) {
	known := map[string]bool{"Close": true, "Warnings": true, "Notices": true, "Generation": true} // these touch no pool
	for _, r := range reads() {
		known[r.name] = true
	}
	for _, w := range writes() {
		known[strings.SplitN(w.name, " ", 2)[0]] = true
	}
	typ := reflect.TypeFor[*Store]()
	for method := range typ.Methods() {
		if name := method.Name; !known[name] {
			t.Errorf("Store.%s is neither in reads() nor in writes() of pool_test.go: decide which pool it uses, and add it", name)
		}
	}
}

// A read does not wait for the writer: while one write holds the writer lock and
// another holds the SQLite write lock on the one write connection, every read
// still answers. A read routed to the write pool would wait for it.
func TestReadsDoNotWaitForTheWriter(t *testing.T) {
	s, _ := openTemp(t)
	ctx := testCtx(t)

	release, err := s.acquire(ctx, prioLow)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('held', '1')`); err != nil {
		t.Fatal(err)
	}

	// Positive control: a write now waits (the lock is held), so the reads below
	// answering is not because nothing is held.
	blocked := make(chan error, 1)
	wctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	go func() { blocked <- s.SetSetting(wctx, "k", "v") }()
	if err := <-blocked; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("control: a write while the writer is held = %v, want to wait until its deadline", err)
	}

	var wg sync.WaitGroup
	for _, r := range reads() {
		wg.Go(func() {
			rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := r.call(rctx, s); err != nil {
				t.Errorf("%s while the writer is busy: %v", r.name, err)
			}
		})
	}
	wg.Wait()
	// The uncommitted write is not visible to them.
	if _, ok, _ := s.GetSetting(ctx, "held"); ok {
		t.Error("a reader sees an uncommitted write")
	}
}

// Every write takes the writer lock, in the class it should: with the lock held,
// each write waits for it, and the waiters add up to the high and low ones
// expected. A write that goes round the lock, or in the wrong class, shows here.
func TestWritesTakeTheWriterInTheirClass(t *testing.T) {
	s, _ := openTemp(t)
	ctx := testCtx(t)
	release, err := s.acquire(ctx, prioLow)
	if err != nil {
		t.Fatal(err)
	}

	var wantHigh, wantLow int
	var wg sync.WaitGroup
	for _, w := range writes() {
		if w.prio == prioHigh {
			wantHigh++
		} else {
			wantLow++
		}
		wg.Go(func() {
			if err := w.call(ctx, s); err != nil {
				t.Errorf("%s: %v", w.name, err)
			}
		})
	}
	waitFor(t, fmt.Sprintf("%d high and %d low writes to wait", wantHigh, wantLow), func() bool {
		h, l := s.wl.waiting()
		return h+l >= wantHigh+wantLow
	})
	time.Sleep(50 * time.Millisecond) // a write that was going to get through has by now
	if h, l := s.wl.waiting(); h != wantHigh || l != wantLow {
		t.Errorf("waiting: %d high and %d low, want %d and %d", h, l, wantHigh, wantLow)
	}
	release()
	wg.Wait()
}

// The writer goes to the high-priority callers first, each class in the order it
// asked. The control puts the same callers in one class: first come, first served.
func TestWriterLockOrder(t *testing.T) {
	run := func(highPrio prio) []string {
		var l writerLock
		ctx := t.Context()
		if err := l.lock(ctx, prioLow); err != nil { // held, as by a write in progress
			t.Fatal(err)
		}
		var (
			mu    sync.Mutex
			order []string
			wg    sync.WaitGroup
			want  int
		)
		ask := func(name string, p prio) {
			want++
			wg.Go(func() {
				if err := l.lock(ctx, p); err != nil {
					t.Errorf("%s: %v", name, err)
					return
				}
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				l.unlock()
			})
			waitFor(t, name+" to queue", func() bool { h, lo := l.waiting(); return h+lo == want })
		}
		ask("low1", prioLow)
		ask("low2", prioLow)
		ask("high1", highPrio)
		ask("low3", prioLow)
		ask("high2", highPrio)
		l.unlock()
		wg.Wait()
		return order
	}
	if got, want := run(prioHigh), []string{"high1", "high2", "low1", "low2", "low3"}; !slices.Equal(got, want) {
		t.Errorf("order with priority = %v, want %v", got, want)
	}
	// Positive control: the same callers in one class go in the order they asked.
	if got, want := run(prioLow), []string{"low1", "low2", "high1", "low3", "high2"}; !slices.Equal(got, want) {
		t.Errorf("control: order without priority = %v, want %v", got, want)
	}
}

// A caller whose context ends while it waits leaves the queue, and the writer is
// never lost, whether the context ends before the lock is handed over, after, or
// at the same moment.
func TestWriterLockCancel(t *testing.T) {
	var l writerLock
	if err := l.lock(t.Context(), prioLow); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.lock(ctx, prioHigh) }()
	waitFor(t, "the caller to queue", func() bool { h, _ := l.waiting(); return h == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("lock after cancel = %v, want context.Canceled", err)
	}
	if h, lo := l.waiting(); h != 0 || lo != 0 {
		t.Errorf("the cancelled caller is still queued: %d, %d", h, lo)
	}
	l.unlock()
	if err := l.lock(t.Context(), prioLow); err != nil { // free again, not handed to the one that left
		t.Fatalf("the writer was lost: %v", err)
	}
	l.unlock()

	// Handed over as the context ends: whoever wins, the next caller gets the lock.
	for i := range 300 {
		if err := l.lock(t.Context(), prioLow); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		got := make(chan error, 1)
		go func() { got <- l.lock(ctx, prioLow) }()
		waitFor(t, "the caller to queue", func() bool { _, lo := l.waiting(); return lo == 1 })
		go cancel()
		l.unlock()
		if err := <-got; err == nil {
			l.unlock() // it won the race and holds the lock
		}
		tctx, tcancel := context.WithTimeout(t.Context(), 5*time.Second)
		if err := l.lock(tctx, prioLow); err != nil {
			t.Fatalf("round %d: the writer was lost: %v", i, err)
		}
		l.unlock()
		tcancel()
	}
}

// A bulk delete is written in chunks of chunkSpec rows, one transaction each, and
// the writer is taken once per chunk.
func TestDeleteItemsIsChunked(t *testing.T) {
	for _, tc := range []struct{ n, chunks int }{{0, 0}, {1, 1}, {chunkSpec, 1}, {chunkSpec + 1, 2}, {2*chunkSpec + 250, 3}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			s, _ := openTemp(t)
			ids := seedItems(t, s, "/bulk", max(tc.n, 1))
			ids = ids[:tc.n]
			g := record(s)
			if err := s.DeleteItems(t.Context(), ids); err != nil {
				t.Fatal(err)
			}
			if got := g.count("low"); got != tc.chunks {
				t.Errorf("%d ids took the writer %d times, want %d", tc.n, got, tc.chunks)
			}
			if got := g.count("high"); got != 0 {
				t.Errorf("a bulk delete took the writer %d times as high priority", got)
			}
			want := max(tc.n, 1) - tc.n
			if n, _ := s.CountItems(t.Context()); n != want {
				t.Errorf("%d items left, want %d", n, want)
			}
			if got := count(t, s, "streams"); got != want {
				t.Errorf("%d streams left, want %d (cascade)", got, want)
			}
		})
	}
}

// Three thousand files are removed, by three writers at once, while a person saves
// their place. The save must not wait for the removal, only for the chunk in
// progress: before the chunking the single write lock was held for the whole
// removal. The control makes the same save a background write, like the chunks
// are: it queues behind the other background writers instead of ahead of them.
func TestSaveProgressDoesNotWaitForABulkDelete(t *testing.T) {
	const files, bulkWriters = 3000, 3

	run := func(t *testing.T, asBackground bool) (chunksAhead, chunksTotal int, latency time.Duration) {
		s, _ := openTemp(t)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		ids := seedItems(t, s, "/bulk", files)
		keeper, err := s.UpsertItem(ctx, movie("/keep/keeper.mp4", "Keeper"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := s.CountItems(ctx); n != files+1 { // positive control for the absence below
			t.Fatalf("control: %d items, want %d", n, files+1)
		}

		var (
			log    grants
			gate   = make(chan struct{})
			opened sync.Once
			first  atomic.Bool
		)
		open := func() { opened.Do(func() { close(gate) }) }
		defer open()
		s.wl.granted = func(p prio) {
			if p == prioHigh {
				log.add("high")
				return
			}
			log.add("low")
			if first.CompareAndSwap(false, true) {
				<-gate // the first chunk is in flight until the save is waiting
			}
		}

		bulk := make(chan error, bulkWriters)
		per := files / bulkWriters
		for w := range bulkWriters {
			go func() { bulk <- s.DeleteItems(ctx, ids[w*per:(w+1)*per]) }()
		}
		waitFor(t, "the other bulk writers to queue", func() bool { _, l := s.wl.waiting(); return l == bulkWriters-1 })

		saved := make(chan error, 1)
		start := time.Now()
		if asBackground {
			go func() {
				saved <- s.write(ctx, prioLow, func(tx *sql.Tx) error {
					log.add("save")
					_, err := tx.ExecContext(ctx, `INSERT INTO progress (item_id, position_sec, duration_sec, updated_at)
						VALUES (?, 100, 7200, 1)`, keeper)
					return err
				})
			}()
		} else {
			go func() { _, err := s.SetProgress(ctx, keeper, 100, 7200); saved <- err }()
		}
		waitFor(t, "the save to queue", func() bool { h, l := s.wl.waiting(); return h+l == bulkWriters })
		open()
		if err := <-saved; err != nil {
			t.Fatal(err)
		}
		latency = time.Since(start)
		entries := log.all()
		mark := "high"
		if asBackground {
			mark = "save"
		}
		at := slices.Index(entries, mark)
		if at < 0 {
			t.Fatalf("the save never took the writer: %v", entries)
		}
		for _, e := range entries[:at] {
			if e == "low" {
				chunksAhead++
			}
		}
		chunksAhead-- // the chunk that was in flight does not count: that one is waited for
		if asBackground {
			chunksAhead-- // and the save's own take of the writer, as a background write
		}

		for range bulkWriters {
			if err := <-bulk; err != nil {
				t.Fatal(err)
			}
		}
		chunksTotal = log.count("low")
		if n, _ := s.CountItems(ctx); n != 1 {
			t.Errorf("%d items left after the removal, want only the keeper", n)
		}
		if e, err := s.GetEntry(ctx, keeper); err != nil || e.Progress == nil || e.Progress.PositionSec != 100 {
			t.Errorf("the keeper's progress = %+v, %v; want 100", e.Progress, err)
		}
		return chunksAhead, chunksTotal, latency
	}

	ahead, total, took := run(t, false)
	t.Logf("with priority: the save waited behind %d of %d chunks (%v)", ahead, total, took)
	if ahead != 0 {
		t.Errorf("the save waited behind %d chunks of the removal, want only the one in flight", ahead)
	}
	if want := bulkWriters * (files / bulkWriters / chunkSpec); total < want {
		t.Errorf("the removal took the writer %d times, want at least %d (chunks of %d rows)", total, want, chunkSpec)
	}

	ctrlAhead, _, ctrlTook := run(t, true)
	t.Logf("control, as a background write: the save waited behind %d chunks (%v)", ctrlAhead, ctrlTook)
	if ctrlAhead < bulkWriters-1 {
		t.Errorf("control: without priority the save waited behind %d chunks, want at least %d", ctrlAhead, bulkWriters-1)
	}
}

// Close closes both pools, and the last connection to go checkpoints the WAL and
// removes its file.
func TestCloseClosesBothPools(t *testing.T) {
	s, path := openTemp(t)
	ctx := t.Context()
	if err := s.SetSetting(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetSetting(ctx, "k"); err != nil || !ok { // a reader connection is open
		t.Fatalf("control: GetSetting = %v, %v", ok, err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("control: no WAL file while the store is open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.ro.PingContext(ctx); err == nil {
		t.Error("the read pool is still open after Close")
	}
	if err := s.rw.PingContext(ctx); err == nil {
		t.Error("the write pool is still open after Close")
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the WAL file is still there after Close (%v): the last connection did not checkpoint and remove it", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
	}
	// What was written is in the file.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, ok, _ := s2.GetSetting(ctx, "k"); !ok || v != "v" {
		t.Errorf("after reopen: %q, %v", v, ok)
	}
}

// The read pool has a connection per CPU up to four (more do not pay, and each
// keeps a page cache), at least two, and keeps all of them: a connection that is
// closed when idle costs its pragmas and a cold cache again.
func TestReadPoolIsCappedAndKeepsItsConnections(t *testing.T) {
	s, _ := openTemp(t)
	ctx := testCtx(t)
	want := max(2, min(runtime.NumCPU(), 4))
	if got := s.ro.Stats().MaxOpenConnections; got != want {
		t.Errorf("the read pool has %d connections on %d CPUs, want %d", got, runtime.NumCPU(), want)
	}

	// Take all of them at once, so that the pool opens them, and give them back.
	conns := make([]*sql.Conn, 0, want)
	for range want {
		c, err := s.ro.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	if st := s.ro.Stats(); st.InUse != want {
		t.Fatalf("control: %d connections in use, want %d", st.InUse, want)
	}
	for _, c := range conns {
		c.Close()
	}
	if st := s.ro.Stats(); st.Idle != want || st.OpenConnections != want {
		t.Errorf("after use: %d idle and %d open connections, want %d of both", st.Idle, st.OpenConnections, want)
	}
}

// A write is visible to the very next read, whichever reader connection serves
// it: the readers are not a snapshot taken at Open, and a caller that has written
// something and then reads it back (a handler saving a setting, say) finds it.
func TestReadersSeeEveryCommittedWrite(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	for i := range 50 {
		key := fmt.Sprintf("k%d", i)
		if err := s.SetSetting(ctx, key, "v"); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, ok, err := s.GetSetting(ctx, key); err != nil || !ok {
				t.Fatalf("a read after SetSetting(%q) = %v, %v", key, ok, err)
			}
		}
	}
}
