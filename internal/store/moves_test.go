package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"ply/internal/probe"
)

func TestMoveItemKeepsIdAndWhatHangsOffIt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	streams := []probe.Stream{{Index: 0, Type: "video", Codec: "h264"}}
	id, err := s.UpsertItem(ctx, &Item{Kind: "movie", Title: "movie", Path: "/m/movie.mkv", Root: "/m",
		Size: 10, MTime: 20, Container: "mkv", DurationSec: 600}, streams)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertItem(ctx, &Item{Kind: "movie", Title: "other", Path: "/m/other.mkv", Root: "/m",
		Size: 11, MTime: 21}, streams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProgress(ctx, id, 30, 600); err != nil {
		t.Fatal(err)
	}

	to := &Item{Kind: "episode", Title: "Pilot", Year: 0, Show: "Show", Season: 1, Episode: 2,
		Path: "/n/Show/S01E02.mkv", Root: "/n", MTime: 25}
	if err := s.MoveItem(ctx, id, to); err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != to.Path || e.Root != to.Root || e.Kind != "episode" || e.Show != "Show" || e.Season != 1 ||
		e.Episode != 2 || e.Title != "Pilot" {
		t.Errorf("moved item = %+v, want the new path and its parsed fields", e.Item)
	}
	if e.MTime != 25 {
		t.Errorf("mtime = %d, want the moved file's (25)", e.MTime)
	}
	if e.Size != 10 || e.Container != "mkv" || e.DurationSec != 600 {
		t.Errorf("moved item = %+v, want size and probe data kept", e.Item)
	}
	if len(e.Streams) != 1 || e.Progress == nil || e.Progress.PositionSec != 30 {
		t.Errorf("streams %v, progress %+v: want both kept", e.Streams, e.Progress)
	}
	if o, err := s.GetEntry(ctx, other); err != nil || o.Path != "/m/other.mkv" {
		t.Errorf("another item changed: %+v, %v", o.Item, err)
	}

	if err := s.MoveItem(ctx, id+100, to); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	if err := s.MoveItem(ctx, other, to); err == nil {
		t.Error("moving onto a path that is taken succeeded")
	}
}

func TestFingerprints(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []int64
	for i, fp := range []string{"", "abc", "", ""} {
		id, err := s.UpsertItem(ctx, &Item{Kind: "movie", Path: filepath.Join("/m", string(rune('a'+i))+".mkv"),
			Root: "/m", Size: 10, MTime: 20, Fingerprint: fp}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	if got, err := s.PathsWithFingerprint(ctx, 10, "abc"); err != nil || len(got) != 1 || got[0] != "/m/b.mkv" {
		t.Errorf("PathsWithFingerprint(10, abc) = %v, %v", got, err)
	}
	if got, _ := s.PathsWithFingerprint(ctx, 11, "abc"); len(got) != 0 {
		t.Errorf("another size matched: %v", got)
	}

	// The cursor pages through the items without one.
	page, err := s.WithoutFingerprint(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].ID != ids[0] || page[1].ID != ids[2] {
		t.Fatalf("first page = %+v, %v; want items 1 and 3", page, err)
	}
	if page, _ := s.WithoutFingerprint(ctx, page[1].ID, 2); len(page) != 1 || page[0].ID != ids[3] {
		t.Errorf("second page = %+v, want item 4", page)
	}

	// A fingerprint read from a file that has changed since is not recorded.
	if err := s.SetFingerprint(ctx, ids[0], 10, 21, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFingerprint(ctx, ids[0], 11, 20, "stale"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PathsWithFingerprint(ctx, 10, "stale"); len(got) != 0 {
		t.Errorf("a stale fingerprint was recorded: %v", got)
	}
	if err := s.SetFingerprint(ctx, ids[0], 10, 20, "fresh"); err != nil { // control
		t.Fatal(err)
	}
	if got, _ := s.PathsWithFingerprint(ctx, 10, "fresh"); len(got) != 1 {
		t.Errorf("control: the fingerprint was not recorded: %v", got)
	}
}
