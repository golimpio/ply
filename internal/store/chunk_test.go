package store

import (
	"context"
	"fmt"
	"testing"
)

// episodesOf turns the seeded movies under root into episodes of show, and gives
// the show a worker's match, so that a re-parse has a series row to clean.
func episodesOf(t *testing.T, s *Store, root, show string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.rw.Exec(`UPDATE items SET kind = 'episode', show = ?, season = 1, episode = id,
		match_state = 'matched' WHERE root = ?`, show, root); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, show, Match{State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
}

func parsedAsMovies(ids []int64) []ParsedRow {
	rows := make([]ParsedRow, len(ids))
	for i, id := range ids {
		rows[i] = ParsedRow{ID: id, Kind: KindMovie, Title: fmt.Sprintf("Movie %d", id), Year: 2001}
	}
	return rows
}

// SetParsed writes chunkSpec rows per transaction, and the series cleaning that
// the end of a re-parse needs happens once all the rows are in: a show whose
// episodes leave in different batches loses its row only with the last one.
func TestSetParsedIsChunked(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	const files = 2*chunkSpec + 200
	ids := seedItems(t, s, "/lib", files)
	episodesOf(t, s, "/lib", "Old Show")
	if _, err := s.GetSeriesMatch(ctx, "Old Show"); err != nil { // control: the row is there
		t.Fatal(err)
	}

	g := record(s)
	if err := s.SetParsed(ctx, parsedAsMovies(ids)); err != nil {
		t.Fatal(err)
	}
	// Three batches, and one transaction for the shows after them.
	if got := g.count("low"); got != 4 {
		t.Errorf("SetParsed of %d rows took the writer %d times, want 4 (3 batches and the shows)", files, got)
	}
	var movies, pending int
	s.rw.QueryRow(`SELECT COUNT(*), SUM(match_state = 'pending') FROM items WHERE kind = 'movie' AND show = ''`).Scan(&movies, &pending)
	if movies != files || pending != files {
		t.Errorf("%d movies and %d pending after SetParsed, want %d of both", movies, pending, files)
	}
	if _, err := s.GetSeriesMatch(ctx, "Old Show"); err == nil {
		t.Error("the series row of a show with no episode left is still there")
	}
}

// A re-parse that stops in the middle leaves the batches before it done, with
// their shows cleaned, and the batch that failed untouched; calling again with
// the same rows finishes it. Nothing is checked in the middle: a bad kind in the
// last row refuses the call before the first row is written.
func TestSetParsedStopsAndResumesBetweenBatches(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	const files = 2*chunkSpec + 200
	ids := seedItems(t, s, "/lib", files)
	episodesOf(t, s, "/lib", "Old Show")
	rows := parsedAsMovies(ids)

	// Refused up front: nothing is written.
	bad := append(append([]ParsedRow(nil), rows...), ParsedRow{ID: ids[0], Kind: "clip"})
	if err := s.SetParsed(ctx, bad); err == nil {
		t.Fatal("a row with kind \"clip\" was accepted")
	}
	if n := count(t, s, "items"); n != files {
		t.Fatalf("control: %d items", n)
	}
	var episodes int
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = 'episode'`).Scan(&episodes)
	if episodes != files {
		t.Fatalf("a refused call changed %d of %d items", files-episodes, files)
	}

	// The second batch fails on one row.
	failing := ids[chunkSpec+chunkSpec/2]
	if _, err := s.rw.Exec(fmt.Sprintf(`CREATE TRIGGER fail_in_batch_two BEFORE UPDATE ON items WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'refused'); END`, failing)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParsed(ctx, rows); err == nil {
		t.Fatal("control: the update was meant to fail")
	}
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = 'episode'`).Scan(&episodes)
	if episodes != files-chunkSpec {
		t.Errorf("%d episodes left after the failure, want %d: the first batch done, the rest not", episodes, files-chunkSpec)
	}
	var firstDone, failedDone int
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = 'movie' AND id IN (SELECT value FROM json_each(?))`,
		jsonIDs(ids[:chunkSpec])).Scan(&firstDone)
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = 'movie' AND id IN (SELECT value FROM json_each(?))`,
		jsonIDs(ids[chunkSpec:2*chunkSpec])).Scan(&failedDone)
	if firstDone != chunkSpec || failedDone != 0 {
		t.Errorf("first batch: %d of %d re-parsed; failed batch: %d re-parsed (want all and none)", firstDone, chunkSpec, failedDone)
	}
	// The first batch's own cleaning ran: the show still has episodes, so its row
	// stays, but the match the worker made for the old reading is cleared.
	sm, err := s.GetSeriesMatch(ctx, "Old Show")
	if err != nil || sm.State != MatchPending {
		t.Errorf("the show's row after the failure = %+v, %v; want it cleared to pending", sm, err)
	}

	// Calling again, after the cause is gone, finishes the job.
	if _, err := s.rw.Exec(`DROP TRIGGER fail_in_batch_two`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParsed(ctx, rows); err != nil {
		t.Fatal(err)
	}
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE kind = 'episode'`).Scan(&episodes)
	if episodes != 0 {
		t.Errorf("%d episodes after the second call, want none", episodes)
	}
	if _, err := s.GetSeriesMatch(ctx, "Old Show"); err == nil {
		t.Error("the row of the show with no episode left survived the second call")
	}
	// And once more: nothing to do, nothing breaks.
	if err := s.SetParsed(ctx, rows); err != nil {
		t.Errorf("a third call: %v", err)
	}
}

func jsonIDs(ids []int64) string {
	b := []byte("[")
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%d", id)
	}
	return string(append(b, ']'))
}

// A prune that has more than chunkSpec rows to remove from a table does it in
// rounds of chunkSpec, each one transaction.
func TestPruneMetadataIsChunked(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	keep := tmdbMovie(1, "Keep", 2000)
	keep.Genres = []Genre{{ID: 28, Name: "Action"}}
	keep.Credits = []Credit{castCredit(1, "Kept Person", "a", 0)}
	addMovie(t, s, "/m/keep.mkv", keep)

	const orphans = 2*chunkSpec + 200
	for _, q := range []string{
		`INSERT INTO genres (id, name) SELECT 10000 + i, 'Genre ' || i FROM seq`,
		`INSERT INTO people (name) SELECT 'Person ' || i FROM seq`,
		`INSERT INTO series_matches (show, match_state) SELECT 'Ghost ' || i, 'unmatched' FROM seq`,
		`INSERT INTO collections (name) SELECT 'Collection ' || i FROM seq`,
	} {
		if _, err := s.rw.Exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?) `+q, orphans); err != nil {
			t.Fatal(err)
		}
	}
	for table, want := range map[string]int{"genres": orphans + 1, "people": orphans + 1, "series_matches": orphans, "collections": orphans} {
		if n := count(t, s, table); n != want {
			t.Fatalf("control: %d rows in %s, want %d", n, table, want)
		}
	}

	g := record(s)
	r, err := s.PruneMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Genres != orphans || r.People != orphans || r.Series != orphans || r.Collections != orphans || r.Titles != 0 {
		t.Errorf("PruneMetadata = %+v, want %d genres, people, series and collections", r, orphans)
	}
	if got := g.count("low"); got != 3 {
		t.Errorf("a prune of %d rows per table took the writer %d times, want 3 rounds", orphans, got)
	}
	for table, want := range map[string]int{"genres": 1, "people": 1, "series_matches": 0, "collections": 0, "titles": 1} {
		if n := count(t, s, table); n != want {
			t.Errorf("%d rows left in %s, want %d", n, table, want)
		}
	}
	// Nothing to do: one round, no change.
	g2 := record(s)
	if r, err := s.PruneMetadata(ctx); err != nil || r != (PruneResult{}) || g2.count("low") != 1 {
		t.Errorf("second prune = %+v, %v in %d rounds; want nothing, nil, 1", r, err, g2.count("low"))
	}
}

// titleSpec is the most titles a prune round removes, as specified: a title takes
// its credits, seasons and episodes with it, so there are fewer per transaction.
const titleSpec = 50

func TestPruneMetadataTitlesGoInSmallChunks(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	const orphans = 2*titleSpec + 20
	if _, err := s.rw.Exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO titles (kind, title, created_at, updated_at) SELECT 'movie', 'Orphan ' || i, 1, 1 FROM seq`, orphans); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "titles"); n != orphans {
		t.Fatalf("control: %d titles, want %d", n, orphans)
	}
	g := record(s)
	r, err := s.PruneMetadata(ctx)
	if err != nil || r.Titles != orphans {
		t.Fatalf("PruneMetadata = %+v, %v; want %d titles", r, err, orphans)
	}
	if got := g.count("low"); got != 3 {
		t.Errorf("%d titles took the writer %d times, want 3 rounds of at most %d", orphans, got, titleSpec)
	}
	if n := count(t, s, "titles"); n != 0 {
		t.Errorf("%d titles left", n)
	}
}

// What an error finds already pruned is counted in the result that comes with it.
func TestPruneMetadataReturnsWhatItRemovedBeforeAnError(t *testing.T) {
	s, _ := openTemp(t)
	const orphans = 2*chunkSpec + 200
	if _, err := s.rw.Exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
		INSERT INTO genres (id, name) SELECT 10000 + i, 'Genre ' || i FROM seq`, orphans); err != nil {
		t.Fatal(err)
	}
	// The last genre is in the third round, whose transaction is undone whole.
	if _, err := s.rw.Exec(fmt.Sprintf(`CREATE TRIGGER refuse_last BEFORE DELETE ON genres WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'refused'); END`, 10000+orphans)); err != nil {
		t.Fatal(err)
	}
	r, err := s.PruneMetadata(t.Context())
	if err == nil {
		t.Fatal("control: the delete was meant to fail")
	}
	if r.Genres != 2*chunkSpec {
		t.Errorf("result with the error = %+v, want the %d genres of the two rounds that committed", r, 2*chunkSpec)
	}
	if n := count(t, s, "genres"); n != 200 {
		t.Errorf("%d genres left, want 200 (the failed round undone)", n)
	}
}

// ReleasePending and RequeueUnmatched touch every pending or unmatched row, but
// chunkSpec of them per transaction.
func TestBulkMatchUpdatesAreChunked(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	const n = 2*chunkSpec + 100
	ids := seedItems(t, s, "/lib", n)
	if _, err := s.rw.Exec(`UPDATE items SET match_state = 'pending', next_attempt_at = 99, match_attempts = 3`); err != nil {
		t.Fatal(err)
	}
	// One pending show, due later, and one unmatched, and an item that is neither.
	if _, err := s.rw.Exec(`INSERT INTO series_matches (show, match_state, next_attempt_at) VALUES ('Later', 'pending', 99)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`UPDATE items SET match_state = 'matched', next_attempt_at = 77 WHERE id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	waiting := func() (items, shows int) {
		s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE match_state = 'pending' AND next_attempt_at > 0`).Scan(&items)
		s.rw.QueryRow(`SELECT COUNT(*) FROM series_matches WHERE match_state = 'pending' AND next_attempt_at > 0`).Scan(&shows)
		return
	}
	if i, sh := waiting(); i != n-1 || sh != 1 {
		t.Fatalf("control: %d items and %d shows waiting", i, sh)
	}

	g := record(s)
	if err := s.ReleasePending(ctx); err != nil {
		t.Fatal(err)
	}
	if i, sh := waiting(); i != 0 || sh != 0 {
		t.Errorf("after ReleasePending: %d items and %d shows still waiting", i, sh)
	}
	if got := g.count("low"); got != 3 {
		t.Errorf("ReleasePending of %d rows took the writer %d times, want 3", n-1, got)
	}
	var attempts, matchedNext int
	s.rw.QueryRow(`SELECT MIN(match_attempts) FROM items WHERE match_state = 'pending'`).Scan(&attempts)
	s.rw.QueryRow(`SELECT next_attempt_at FROM items WHERE id = ?`, ids[0]).Scan(&matchedNext)
	if attempts != 3 || matchedNext != 77 {
		t.Errorf("ReleasePending changed attempts to %d or a matched item's next_attempt_at to %d", attempts, matchedNext)
	}

	// RequeueUnmatched: the same size, its count is the number of rows it changed.
	if _, err := s.rw.Exec(`UPDATE items SET match_state = 'unmatched', match_attempts = 5, next_attempt_at = 50, needs_review = 1
		WHERE id <> ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rw.Exec(`INSERT INTO series_matches (show, match_state, match_attempts) VALUES ('Lost', 'unmatched', 4)`); err != nil {
		t.Fatal(err)
	}
	g = record(s)
	got, err := s.RequeueUnmatched(ctx)
	if err != nil || got != n-1+1 {
		t.Errorf("RequeueUnmatched = %d, %v; want %d", got, err, n)
	}
	if c := g.count("low"); c != 3 {
		t.Errorf("RequeueUnmatched took the writer %d times, want 3", c)
	}
	var unmatched, due int
	s.rw.QueryRow(`SELECT (SELECT COUNT(*) FROM items WHERE match_state = 'unmatched') +
		(SELECT COUNT(*) FROM series_matches WHERE match_state = 'unmatched')`).Scan(&unmatched)
	s.rw.QueryRow(`SELECT COUNT(*) FROM items WHERE match_state = 'pending' AND next_attempt_at = 0
		AND match_attempts = 0 AND needs_review = 0`).Scan(&due)
	if unmatched != 0 || due != n-1 {
		t.Errorf("after RequeueUnmatched: %d unmatched left, %d items due and reset; want 0 and %d", unmatched, due, n-1)
	}
}
