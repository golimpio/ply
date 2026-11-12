package store

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// A rescan, playback sessions and progress reports use the store at the same
// time. Writers replace an item's streams, sidecar subtitles and keyframes
// while readers look at it: a reader must always see the item completely
// before or completely after any one write (each is one transaction), nothing
// may deadlock on the single connection, and nothing may be lost. Run it with
// -race.
func TestConcurrentUseKeepsEveryWriteAtomic(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	subsA := []ExternalSubtitle{{Path: "/media/a.en.srt", Language: "en", Format: "srt"}, {Path: "/media/a.fr.srt", Language: "fr", Format: "srt"}}
	subsB := []ExternalSubtitle{{Path: "/media/b.de.srt", Language: "de", Format: "srt"}, {Path: "/media/b.es.vtt", Language: "es", Format: "vtt"}}
	paths := func(subs []ExternalSubtitle) []string {
		var p []string
		for _, s := range subs {
			p = append(p, s.Path)
		}
		return p
	}
	wantA, wantB := paths(subsA), paths(subsB)

	const nItems = 4
	item := func(n int) *Item { return movie(fmt.Sprintf("/media/m%d.mp4", n), fmt.Sprintf("M%d", n)) }
	ids := make([]int64, nItems)
	for n := range ids {
		var err error
		if ids[n], err = s.UpsertItem(ctx, item(n), videoAudio); err != nil {
			t.Fatal(err)
		}
		if err = s.SetExternalSubtitles(ctx, ids[n], subsA); err != nil {
			t.Fatal(err)
		}
	}

	var writers, readers sync.WaitGroup
	stop := make(chan struct{})
	// Fixed time, not a fixed count: the race detector makes every query many
	// times slower, and what matters is that reads and writes overlap for a while.
	const nWriters = 4
	end := time.Now().Add(700 * time.Millisecond)

	for w := range nWriters {
		writers.Go(func() {
			for i := 0; time.Now().Before(end); i++ {
				n := (w + i) % nItems
				subs := subsA
				if i%2 == 1 {
					subs = subsB
				}
				// Same path every time: concurrent upserts of one file must
				// stay one row with exactly its streams.
				if id, err := s.UpsertItem(ctx, item(n), videoAudio); err != nil || id != ids[n] {
					t.Errorf("UpsertItem(%d) = %d, %v; want %d", n, id, err, ids[n])
				}
				if err := s.SetExternalSubtitles(ctx, ids[n], subs); err != nil {
					t.Errorf("SetExternalSubtitles: %v", err)
				}
				if _, err := s.SetProgress(ctx, ids[n], 100+float64(i), 7200); err != nil {
					t.Errorf("SetProgress: %v", err)
				}
				if err := s.SetKeyframes(ctx, ids[n], []float64{0, 2, 4}); err != nil {
					t.Errorf("SetKeyframes: %v", err)
				}
				// An item that comes and goes, with everything hanging off it.
				if i%2 == 0 {
					scratch := fmt.Sprintf("/media/scratch-%d-%d.mp4", w, i)
					id, err := s.UpsertItem(ctx, movie(scratch, "Scratch"), videoAudio)
					if err == nil {
						_, err = s.SetProgress(ctx, id, 100, 7200)
					}
					if err == nil {
						err = s.DeleteItems(ctx, []int64{id})
					}
					if err != nil {
						t.Errorf("scratch item: %v", err)
					}
				}
			}
		})
	}

	for r := range 6 {
		readers.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				n := (r + i) % nItems
				e, err := s.GetEntry(ctx, ids[n])
				if err != nil {
					t.Errorf("GetEntry: %v", err)
					return
				}
				if len(e.Streams) != len(videoAudio) {
					t.Errorf("item %d seen with %d streams, want %d (a write was visible half-done)", n, len(e.Streams), len(videoAudio))
				}
				if got := paths(e.External); !slices.Equal(got, wantA) && !slices.Equal(got, wantB) {
					t.Errorf("item %d seen with sidecars %v, want %v or %v", n, got, wantA, wantB)
				}
				if kf, ok, err := s.Keyframes(ctx, ids[n]); err != nil || (ok && len(kf) != 3) {
					t.Errorf("Keyframes = %v, %v, %v", kf, ok, err)
				}
				list, err := s.ListItems(ctx, ListFilter{})
				if err != nil {
					t.Errorf("ListItems: %v", err)
				}
				for _, li := range list {
					if len(li.Streams) != len(videoAudio) {
						t.Errorf("ListItems returned %s with %d streams, want %d", li.Path, len(li.Streams), len(videoAudio))
					}
				}
				cont, err := s.Continue(ctx, 20)
				if err != nil {
					t.Errorf("Continue: %v", err)
				}
				for _, c := range cont {
					if c.Progress == nil || c.Progress.Watched() || len(c.Streams) != len(videoAudio) {
						t.Errorf("Continue returned %+v", c)
					}
				}
				if _, err := s.FileStates(ctx); err != nil {
					t.Errorf("FileStates: %v", err)
				}
				if _, err := s.Shows(ctx); err != nil {
					t.Errorf("Shows: %v", err)
				}
			}
		})
	}

	done := make(chan struct{})
	go func() { writers.Wait(); close(stop); readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the store deadlocked or stalled under concurrent use")
	}

	// Nothing lost or duplicated: the scratch items are gone, and so is
	// everything that hung off them.
	if n, err := s.CountItems(ctx); err != nil || n != nItems {
		t.Fatalf("CountItems = %d, %v; want %d", n, err, nItems)
	}
	for _, table := range []string{"streams", "progress"} {
		var rows int
		if err := s.rw.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		want := nItems * len(videoAudio)
		if table == "progress" {
			want = nItems
		}
		if rows != want {
			t.Errorf("%s rows = %d, want %d", table, rows, want)
		}
	}
}

// A migration is all or nothing: one that fails halfway leaves no table, no
// version row and no transaction holding the only connection.
func TestFailedMigrationLeavesNothingBehind(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	tableExists := func(name string) bool {
		t.Helper()
		var n int
		if err := s.rw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatalf("a failed migration left the store unusable: %v", err)
		}
		return n > 0
	}
	maxVersion := func() int {
		t.Helper()
		var v int
		if err := s.rw.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
			t.Fatalf("a failed migration left the store unusable: %v", err)
		}
		return v
	}
	before := maxVersion()

	// Positive control: a migration that works is applied and recorded.
	if err := s.applyMigration(ctx, before+1, `CREATE TABLE fine (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if !tableExists("fine") || maxVersion() != before+1 {
		t.Fatalf("setup: a good migration left table=%v, version %d", tableExists("fine"), maxVersion())
	}

	err := s.applyMigration(ctx, before+2, `CREATE TABLE half_done (id INTEGER); INSERT INTO no_such_table VALUES (1);`)
	if err == nil {
		t.Fatal("a migration with a bad statement succeeded")
	}
	if tableExists("half_done") {
		t.Error("the table of the failed migration is still there")
	}
	if v := maxVersion(); v != before+1 {
		t.Errorf("schema version = %d after the failed migration, want %d", v, before+1)
	}
	if _, err := s.CountItems(ctx); err != nil {
		t.Errorf("the store is unusable after a failed migration: %v", err)
	}
}
