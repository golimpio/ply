package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"testing"
	"time"
)

// Tests that came out of the review of the read pool and single writer change. The
// first four are the reviewer's, adapted.

// The writer connection begins its transactions IMMEDIATE (the DSN's _txlock): a
// transaction holds the SQLite write lock from BEGIN, so a second connection (a
// second ply, the sqlite3 shell) cannot write between the transaction's first
// read and its first write.
func TestReviewTxlockImmediate(t *testing.T) {
	s, path := openTemp(t)
	ctx := testCtx(t)
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(100)"}
	other, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// control: with no store transaction open the other connection can write
	if _, err := other.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('control', '1')`); err != nil {
		t.Fatalf("control: %v", err)
	}
	err = s.write(ctx, prioHigh, func(tx *sql.Tx) error {
		if _, werr := other.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('ext', '1')`); werr == nil {
			t.Error("another connection wrote while a store write transaction was open: it did not BEGIN IMMEDIATE")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Each write is in its own class: the aggregate count of
// TestWritesTakeTheWriterInTheirClass does not notice two methods that swap classes
// (LinkItemTitle and LinkSeriesTitle share linkPrio).
func TestReviewLinkPriorityPerCall(t *testing.T) {
	title := func() *TitleInput { return &TitleInput{Title: Title{Kind: KindMovie, Title: "T"}} }
	for _, tc := range []struct {
		name     string
		call     func(ctx context.Context, s *Store)
		wantHigh int
	}{
		{"LinkItemTitle person", func(ctx context.Context, s *Store) {
			s.LinkItemTitle(ctx, 1, title(), Match{State: MatchManual}, "")
		}, 1},
		{"LinkItemTitle worker", func(ctx context.Context, s *Store) {
			s.LinkItemTitle(ctx, 1, title(), Match{State: MatchPending}, MatchPending)
		}, 0},
		{"LinkSeriesTitle person", func(ctx context.Context, s *Store) {
			s.LinkSeriesTitle(ctx, "x", title(), Match{State: MatchManual}, "")
		}, 1},
		{"LinkSeriesTitle worker", func(ctx context.Context, s *Store) {
			s.LinkSeriesTitle(ctx, "x", title(), Match{State: MatchPending}, MatchPending)
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			ctx := testCtx(t)
			release, err := s.acquire(ctx, prioLow)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); tc.call(ctx, s) }()
			waitFor(t, "the call to wait", func() bool { h, l := s.wl.waiting(); return h+l == 1 })
			if h, _ := s.wl.waiting(); h != tc.wantHigh {
				t.Errorf("%s waits as high=%d, want %d", tc.name, h, tc.wantHigh)
			}
			release()
			<-done
		})
	}
}

// Deterministic version of the half of TestWriterLockCancel that races: the context
// ends, and before the waiter takes l.mu to leave the queue, the lock is handed to
// it. Whatever the scheduler does, the writer must not be lost.
func TestReviewCancelAfterHandoff(t *testing.T) {
	var l writerLock
	if err := l.lock(t.Context(), prioLow); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan error, 1)
	go func() { got <- l.lock(ctx, prioLow) }()
	waitFor(t, "the caller to queue", func() bool { _, lo := l.waiting(); return lo == 1 })

	l.mu.Lock() // freeze the lock: the waiter cannot leave the queue yet
	cancel()
	// What unlock does, while the waiter, which has seen its context end, is still queued.
	w := l.queues[prioLow][0]
	l.queues[prioLow] = l.queues[prioLow][1:]
	close(w.ready)
	l.mu.Unlock()

	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("lock = %v, want context.Canceled", err)
	}
	tctx, tcancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer tcancel()
	if err := l.lock(tctx, prioLow); err != nil {
		t.Fatalf("the writer was lost: a waiter whose context ended as the lock was handed to it did not pass it on: %v", err)
	}
	l.unlock()
}

// The closing pass of SetParsed is for the shows of every batch, not only of the
// last: a show whose rows were all in the first batch, and that the matcher matched
// between the batches, ends the call cleared, as after one big transaction.
func TestReviewSetParsedFinalPassCoversEarlierBatches(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	ids := seedItems(t, s, "/lib", 2*chunkSpec)
	if _, err := s.rw.Exec(`UPDATE items SET kind = 'episode', season = 1, episode = id, match_state = 'matched',
		show = CASE WHEN id <= ? THEN 'A' ELSE 'B' END`, ids[chunkSpec-1]); err != nil {
		t.Fatal(err)
	}
	for _, show := range []string{"A", "B"} {
		if err := s.SetSeriesMatch(ctx, show, Match{State: MatchMatched, Score: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
	rows := make([]ParsedRow, len(ids))
	for i, id := range ids {
		show := "A"
		if i >= chunkSpec {
			show = "B"
		}
		rows[i] = ParsedRow{ID: id, Kind: KindEpisode, Title: "x", Show: show, Season: 2, Episode: i}
	}
	var grants int
	s.wl.granted = func(prio) {
		grants++
		if grants == 2 { // between the first batch and the second: the matcher matches A again
			if _, err := s.rw.Exec(`UPDATE series_matches SET match_state = 'matched' WHERE show = 'A'`); err != nil {
				t.Error(err)
			}
		}
	}
	if err := s.SetParsed(ctx, rows); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetSeriesMatch(ctx, "A")
	if err != nil || a.State != MatchPending {
		t.Errorf("show A after SetParsed = %+v, %v; want pending (cleared by the closing pass)", a.Match, err)
	}
}

// A series row is removed only by the closing pass of SetParsed: a show that has no
// episode after the first batch and gets some in the second keeps its manual match,
// as it does when the whole call is one transaction.
func TestSetParsedKeepsAManualMatchUntilTheLastBatch(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	ids := seedItems(t, s, "/lib", 2*chunkSpec)
	// "New" has one episode, in the first batch, which becomes a movie; the movies of
	// the second batch become its episodes.
	if _, err := s.rw.Exec(`UPDATE items SET kind = 'episode', show = 'New', season = 1, episode = 1 WHERE id = ?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "New", Match{State: MatchManual}); err != nil {
		t.Fatal(err)
	}
	rows := make([]ParsedRow, len(ids))
	for i, id := range ids {
		rows[i] = ParsedRow{ID: id, Kind: KindMovie, Title: "m"}
		if i >= chunkSpec {
			rows[i] = ParsedRow{ID: id, Kind: KindEpisode, Show: "New", Season: 1, Episode: i, Title: "e"}
		}
	}

	if err := s.SetParsed(ctx, rows); err != nil {
		t.Fatal(err)
	}
	sm, err := s.GetSeriesMatch(ctx, "New")
	if err != nil || sm.State != MatchManual {
		t.Errorf("the manual match of \"New\" after SetParsed = %+v, %v; want it kept", sm.Match, err)
	}
}

// A write is reported done only once it is committed: when the commit fails the
// caller is told that nothing was written, not that something was (the callers of
// the IfPending writes look at ok first).
func TestMatchWritesReportNothingWhenTheCommitFails(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	item, err := s.UpsertItem(ctx, movie("/media/m.mp4", "M"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertItem(ctx, episode("/media/Show/S01E01.mkv", "Show", 1, 1), nil); err != nil {
		t.Fatal(err)
	}

	// Every update of an item and every change to a series row adds a row that
	// breaks a deferred foreign key: the statements succeed, the COMMIT does not.
	for _, q := range []string{
		`CREATE TABLE zz_deferred (n INTEGER REFERENCES titles (id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER zz_items AFTER UPDATE ON items BEGIN INSERT INTO zz_deferred VALUES (999999); END`,
		`CREATE TRIGGER zz_series_insert AFTER INSERT ON series_matches BEGIN INSERT INTO zz_deferred VALUES (999999); END`,
		`CREATE TRIGGER zz_series_update AFTER UPDATE ON series_matches BEGIN INSERT INTO zz_deferred VALUES (999999); END`,
	} {
		if _, err := s.rw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Control: the trap does what is meant. The statement is accepted, the commit is not.
	stmtOK := false
	err = s.write(ctx, prioLow, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE items SET title = title WHERE id = ?`, item)
		stmtOK = err == nil
		return err
	})
	if !stmtOK || err == nil {
		t.Fatalf("control: statement accepted = %v, write = %v; want accepted and then refused at commit", stmtOK, err)
	}

	pending := Match{State: MatchMatched, Score: 1}
	if ok, err := s.SetItemMatchIfPending(ctx, item, pending); ok || err == nil {
		t.Errorf("SetItemMatchIfPending = %v, %v; want false and the error", ok, err)
	}
	if ok, err := s.SetSeriesMatchIfPending(ctx, "Show", pending); ok || err == nil {
		t.Errorf("SetSeriesMatchIfPending = %v, %v; want false and the error", ok, err)
	}
	title := &TitleInput{Title: Title{Kind: KindMovie, Title: "T"}}
	if id, ok, err := s.LinkItemTitle(ctx, item, title, pending, MatchPending); id != 0 || ok || err == nil {
		t.Errorf("LinkItemTitle = %d, %v, %v; want 0, false and the error", id, ok, err)
	}
	if id, ok, err := s.LinkSeriesTitle(ctx, "Show", title, pending, MatchPending); id != 0 || ok || err == nil {
		t.Errorf("LinkSeriesTitle = %d, %v, %v; want 0, false and the error", id, ok, err)
	}
	// Nothing was written.
	if m, err := s.GetItemMatch(ctx, item); err != nil || m.State != MatchPending {
		t.Errorf("the item after the failures = %+v, %v; want it still pending", m, err)
	}
	if _, err := s.GetSeriesMatch(ctx, "Show"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the show after the failures: %v; want it to have no row", err)
	}
	// And the writer is not left in a transaction: with the trap gone, the same call works.
	for _, q := range []string{`DROP TRIGGER zz_items`, `DROP TRIGGER zz_series_insert`, `DROP TRIGGER zz_series_update`} {
		if _, err := s.rw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.SetItemMatchIfPending(ctx, item, pending); !ok || err != nil {
		t.Errorf("SetItemMatchIfPending without the trap = %v, %v; want true", ok, err)
	}
}
