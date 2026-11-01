package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestIsWatched(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pos, dur float64
		want     bool
	}{
		{"start", 0, 7200, false},
		{"half", 3600, 7200, false},
		{"just under 90%", 6479, 7200, false},
		{"exactly 90%", 6480, 7200, true},
		{"end", 7200, 7200, true},
		{"past the end", 7300, 7200, true},
		{"98%", 7100, 7200, true},
		{"short item: under 90% but within 120 s of the end", 500, 600, true},
		{"short item: more than 120 s from the end", 400, 600, false},
		{"short item: within 120 s of the end but under half played", 90, 200, false},
		{"tiny clip is not watched at its start", 5, 90, false},
		{"unknown duration", 5000, 0, false},
	} {
		if got := IsWatched(tc.pos, tc.dur); got != tc.want {
			t.Errorf("%s: IsWatched(%v, %v) = %v, want %v", tc.name, tc.pos, tc.dur, got, tc.want)
		}
	}
}

func TestSetProgress(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, err := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := func() *Progress {
		t.Helper()
		e, err := s.GetEntry(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return e.Progress
	}
	if stored() != nil {
		t.Fatal("a new item has progress")
	}

	p, err := s.SetProgress(ctx, id, 125.5, 7200)
	if err != nil || p == nil {
		t.Fatalf("SetProgress = %v, %v", p, err)
	}
	if got := stored(); got == nil || got.PositionSec != 125.5 || got.DurationSec != 7200 || got.Watched {
		t.Errorf("stored = %+v", got)
	}

	// A position before 30 s deletes the row (positive control above: it existed).
	if p, err := s.SetProgress(ctx, id, 12, 7200); err != nil || p != nil {
		t.Fatalf("SetProgress(12) = %v, %v; want nil, nil", p, err)
	}
	if got := stored(); got != nil {
		t.Errorf("progress after a restart = %+v, want none", got)
	}

	// Exactly 30 s counts as started.
	if p, _ := s.SetProgress(ctx, id, 30, 7200); p == nil || p.Watched {
		t.Errorf("SetProgress(30) = %+v, want an unwatched row", p)
	}

	// Watched at 90 %, and the row is kept.
	p, err = s.SetProgress(ctx, id, 6600, 7200)
	if err != nil || p == nil || !p.Watched {
		t.Fatalf("SetProgress(6600) = %+v, %v; want watched", p, err)
	}
	if got := stored(); got == nil || !got.Watched {
		t.Errorf("stored = %+v, want watched", got)
	}
}

func TestSetProgressWatchedShortPositionKeepsRow(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)
	// A 20 s clip played to its end is watched even though 20 s < 30 s.
	p, err := s.SetProgress(ctx, id, 19, 20)
	if err != nil || p == nil || !p.Watched {
		t.Fatalf("SetProgress(19, 20) = %+v, %v; want a watched row", p, err)
	}
}

func TestSetProgressUnknownItem(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.SetProgress(context.Background(), 999, 100, 200); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestContinueOrderLimitAndExclusions(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(path string) int64 {
		id, err := s.UpsertItem(ctx, movie(path, path), videoAudio)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c, watched := add("/media/a.mp4"), add("/media/b.mp4"), add("/media/c.mp4"), add("/media/w.mp4")
	add("/media/never-started.mp4")
	for _, id := range []int64{a, b, c} {
		if _, err := s.SetProgress(ctx, id, 100, 7200); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetProgress(ctx, watched, 7000, 7200); err != nil {
		t.Fatal(err)
	}
	// a was played most recently.
	if _, err := s.SetProgress(ctx, a, 200, 7200); err != nil {
		t.Fatal(err)
	}

	got, err := s.Continue(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, e := range got {
		ids = append(ids, e.ID)
		if e.Progress == nil || len(e.Streams) != len(videoAudio) {
			t.Errorf("entry %d: progress %v, %d streams", e.ID, e.Progress, len(e.Streams))
		}
	}
	if want := []int64{a, c, b}; !reflect.DeepEqual(ids, want) {
		t.Errorf("continue = %v, want %v (most recent first; watched and unstarted excluded)", ids, want)
	}

	if got, _ := s.Continue(ctx, 2); len(got) != 2 {
		t.Errorf("limit 2 returned %d items", len(got))
	}
	// Finishing an item takes it off the list.
	if _, err := s.SetProgress(ctx, a, 7100, 7200); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Continue(ctx, 20)
	if len(got) != 2 || got[0].ID != c {
		t.Errorf("after finishing a: %d items, first %v", len(got), got)
	}
}

func TestListItemsCarriesProgress(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a, _ := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), videoAudio)
	s.UpsertItem(ctx, movie("/media/b.mp4", "B"), videoAudio)
	if _, err := s.SetProgress(ctx, a, 300, 7200); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListItems(ctx, ListFilter{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("ListItems = %d entries, %v", len(entries), err)
	}
	if p := entries[0].Progress; p == nil || p.PositionSec != 300 {
		t.Errorf("A progress = %+v", p)
	}
	if entries[1].Progress != nil {
		t.Errorf("B progress = %+v, want none", entries[1].Progress)
	}
}

func TestExternalSubtitles(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)

	subs := []ExternalSubtitle{
		{Path: "/media/a.pt.srt", Language: "pt", Format: "srt"},
		{Path: "/media/a.en.forced.srt", Language: "en", Format: "srt", Forced: true},
	}
	if err := s.SetExternalSubtitles(ctx, id, subs); err != nil {
		t.Fatal(err)
	}
	e, _ := s.GetEntry(ctx, id)
	want := []ExternalSubtitle{subs[1], subs[0]} // ordered by path
	if !reflect.DeepEqual(e.External, want) {
		t.Errorf("External = %+v, want %+v", e.External, want)
	}
	states, _ := s.FileStates(ctx)
	if got := states["/media/a.mp4"].Subs; !reflect.DeepEqual(got, want) {
		t.Errorf("FileStates subs = %+v, want %+v", got, want)
	}

	// Replacing and clearing.
	if err := s.SetExternalSubtitles(ctx, id, subs[:1]); err != nil {
		t.Fatal(err)
	}
	if e, _ = s.GetEntry(ctx, id); len(e.External) != 1 {
		t.Errorf("after replace: %+v", e.External)
	}
	if err := s.SetExternalSubtitles(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if e, _ = s.GetEntry(ctx, id); len(e.External) != 0 {
		t.Errorf("after clear: %+v", e.External)
	}
}

func TestMigrationAddsForcedColumnToExistingDatabase(t *testing.T) {
	// A database created by M1-M4 has migration 1 only: opening it with this
	// version must add the column and keep the data.
	s, path := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil)
	if _, err := s.db.Exec(`ALTER TABLE subtitles_external DROP COLUMN forced`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_version WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO subtitles_external (item_id, path, language, format) VALUES (?, '/media/a.srt', '', 'srt')`, id); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen old database: %v", err)
	}
	defer s2.Close()
	e, err := s2.GetEntry(ctx, id)
	if err != nil || len(e.External) != 1 || e.External[0].Forced {
		t.Errorf("after upgrade: %+v, %v", e.External, err)
	}
}
