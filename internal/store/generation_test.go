package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/probe"
)

// Generation is what the API's ETags are made of: it must change after every kind of write
// that a list can show, stay put for reads and for the queue's own table, and not repeat
// itself from one run to the next.

func genStore(t *testing.T) (s *Store, item, title int64) {
	t.Helper()
	s, _ = openTemp(t)
	ctx := context.Background()
	var err error
	title, err = s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, TMDBID: 603, Title: "The Matrix", SortTitle: "Matrix, The", Year: 1999,
		Source: SourceTMDB}})
	if err != nil {
		t.Fatal(err)
	}
	item, err = s.UpsertItem(ctx, &Item{Kind: KindMovie, Title: "The Matrix", Year: 1999, Path: "/m/The Matrix.mkv", Root: "/m", DurationSec: 7200},
		[]probe.Stream{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertItem(ctx, &Item{Kind: KindEpisode, Show: "Some Show", Season: 1, Episode: 1, Path: "/t/Some Show S01E01.mkv", Root: "/t"}, nil); err != nil {
		t.Fatal(err)
	}
	return s, item, title
}

func TestGenerationFollowsEveryKindOfWrite(t *testing.T) {
	s, item, title := genStore(t)
	ctx := context.Background()
	writes := []struct {
		name string
		do   func() error
	}{
		{"a scan adds a file", func() error {
			_, err := s.UpsertItem(ctx, &Item{Kind: KindMovie, Title: "Alien", Path: "/m/Alien.mkv", Root: "/m"}, nil)
			return err
		}},
		{"a match (one statement)", func() error {
			return s.SetItemMatch(ctx, item, Match{TitleID: title, State: MatchMatched, Score: 0.9})
		}},
		{"a show's match", func() error { return s.SetSeriesMatch(ctx, "Some Show", Match{State: MatchPending}) }},
		{"a title (a transaction)", func() error {
			_, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, TMDBID: 604, Title: "Reloaded", Source: SourceTMDB}})
			return err
		}},
		{"a progress save", func() error { _, err := s.SetProgress(ctx, item, 30, 7200); return err }},
		{"a watched change", func() error { _, err := s.SetItemWatched(ctx, item, true); return err }},
		{"a rating", func() error {
			return s.ReplaceMDBListRatings(ctx, title, []Rating{{Source: MDBListSources[0], Value: 80, Scale: 100}}, 1)
		}},
		{"a setting (the primary rating, say)", func() error { return s.SetSetting(ctx, "ratings.primary", "tmdb") }},
		{"files that go", func() error { return s.DeleteItems(ctx, []int64{item}) }},
	}
	for _, w := range writes {
		before := s.Generation()
		if got := s.Generation(); got != before {
			t.Fatalf("%s: Generation moved on its own (%d -> %d)", w.name, before, got) // the control: no write, no change
		}
		if err := w.do(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if after := s.Generation(); after <= before {
			t.Errorf("%s: Generation %d -> %d, want it to go up", w.name, before, after)
		}
	}
}

func TestGenerationIgnoresReadsAndTheQueue(t *testing.T) {
	s, item, _ := genStore(t)
	ctx := context.Background()
	before := s.Generation()

	if _, err := s.ListItems(ctx, ListFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntry(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Titles(ctx, TitleFilter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Shows(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetSetting(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if got := s.Generation(); got != before {
		t.Errorf("reads moved Generation: %d -> %d", before, got)
	}

	// The background queue writes a lot and shows in no list.
	rec := jobs.Record{Kind: "art.grab", Key: "k", State: jobs.Queued, CreatedAt: time.Now(), RunAt: time.Now()}
	id, err := s.InsertJob(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	rec.ID, rec.State = id, jobs.Done
	rec.FinishedAt = time.Now()
	if err := s.UpdateJob(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneJobs(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := s.Generation(); got != before {
		t.Errorf("the queue's writes moved Generation: %d -> %d", before, got)
	}

	// Positive control: the same store does move for a write.
	if _, err := s.SetProgress(ctx, item, 10, 7200); err != nil {
		t.Fatal(err)
	}
	if got := s.Generation(); got <= before {
		t.Errorf("control: a progress save left Generation at %d (was %d)", got, before)
	}
}

// What no list shows is not a change to the lists: the keyframe cache, an item's fingerprint, when
// MDBList was last asked. The control is TouchTitle, which looks as small and is not: fetched_at is
// the time of TMDB's rating, which the lists show.
func TestGenerationIgnoresWritesNoListShows(t *testing.T) {
	s, item, title := genStore(t)
	ctx := context.Background()
	for _, w := range []struct {
		name string
		do   func() error
	}{
		{"keyframes", func() error { return s.SetKeyframes(ctx, item, []float64{0, 2.5, 5}) }},
		{"a fingerprint", func() error { return s.SetFingerprint(ctx, item, 0, 0, "fp") }},
		{"a ratings check", func() error { return s.TouchRatingsChecked(ctx, title, 123) }},
	} {
		before := s.Generation()
		if err := w.do(); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		if after := s.Generation(); after != before {
			t.Errorf("%s moved Generation: %d -> %d", w.name, before, after)
		}
	}
	// The writes did happen.
	if times, ok, err := s.Keyframes(ctx, item); err != nil || !ok || len(times) != 3 {
		t.Errorf("keyframes after SetKeyframes = %v, %v, %v", times, ok, err)
	}
	var got string
	if err := s.ro.QueryRowContext(ctx, `SELECT fingerprint FROM items WHERE id = ?`, item).Scan(&got); err != nil || got != "fp" {
		t.Errorf("fingerprint = %q, %v", got, err)
	}

	before := s.Generation()
	if err := s.TouchTitle(ctx, title, 456); err != nil {
		t.Fatal(err)
	}
	if after := s.Generation(); after <= before {
		t.Errorf("TouchTitle left Generation at %d (was %d): the lists show fetched_at", after, before)
	}
}

// A write that rolls back changed nothing, so the lists are still the lists.
func TestGenerationIgnoresARolledBackWrite(t *testing.T) {
	s, _, _ := genStore(t)
	ctx := context.Background()
	before := s.Generation()
	if err := s.SetItemEmbeddedCover(ctx, 9999, 1); !errors.Is(err, ErrNotFound) { // fails inside the transaction
		t.Fatalf("SetItemEmbeddedCover of an unknown item = %v, want ErrNotFound", err)
	}
	if got := s.Generation(); got != before {
		t.Errorf("a rolled-back write moved Generation: %d -> %d", before, got)
	}
}

// A client that kept an ETag from an earlier run must not find it equal to one of this run, whatever
// number of writes each had.
func TestGenerationStartsNewEveryRun(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	for range 20 {
		if err := s.SetSetting(ctx, "k", "v"); err != nil {
			t.Fatal(err)
		}
	}
	first := s.Generation()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if second := s2.Generation(); second <= first {
		t.Errorf("Generation after a restart = %d, want more than the %d the run before ended at", second, first)
	}
}
