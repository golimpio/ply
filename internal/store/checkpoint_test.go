package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// inMainFile reports whether the query finds a row in the database file alone, as a
// power cut leaves it when the WAL's frames were never synced: a copy of the file
// without its -wal. A table the file does not have yet counts as no row.
func inMainFile(t *testing.T, path, query string, args ...any) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

const (
	hasSetting = `SELECT COUNT(*) FROM settings WHERE key = ?`
	hasItem    = `SELECT COUNT(*) FROM items WHERE path = ?`
)

// A write a person made is in the database file, not only in the WAL, a moment
// later: a power cut after that does not take it back. A background write is not
// pushed that way (the control), until the writer has been idle for a while.
func TestCheckpointFollowsAnInteractiveWrite(t *testing.T) {
	s, path := openTemp(t)
	ctx := t.Context()
	s.ck.setDelays(50*time.Millisecond, time.Hour)

	if _, err := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.CountItems(ctx); got != 1 || inMainFile(t, path, hasItem, "/media/a.mp4") {
		t.Fatalf("control: the item is committed (%d) and already in the database file alone (%v)", got, inMainFile(t, path, hasItem, "/media/a.mp4"))
	}
	if n := s.ck.runs.Load(); n != 0 {
		t.Fatalf("control: %d checkpoints after a background write", n)
	}

	if err := s.SetSetting(ctx, "tmdb.key", "secret"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a checkpoint after the interactive write", func() bool { return s.ck.runs.Load() >= 1 })
	if !inMainFile(t, path, hasSetting, "tmdb.key") {
		t.Error("the setting is not in the database file after the checkpoint")
	}
	if !inMainFile(t, path, hasItem, "/media/a.mp4") {
		t.Error("the checkpoint left the earlier write in the WAL")
	}
}

// Writes made while the writer stays busy do not each ask for a checkpoint: the
// first interactive one does, and the checkpoint serves the rest.
func TestCheckpointsAreCoalesced(t *testing.T) {
	s, _ := openTemp(t)
	ctx := t.Context()
	s.ck.setDelays(300*time.Millisecond, time.Hour)
	for i := range 30 {
		if err := s.SetSetting(ctx, "k"+string(rune('a'+i%26)), "v"); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "a checkpoint", func() bool { return s.ck.runs.Load() >= 1 })
	time.Sleep(400 * time.Millisecond)
	if n := s.ck.runs.Load(); n > 3 {
		t.Errorf("30 writes made %d checkpoints, want one (or a few if the machine was slow)", n)
	}
}

// When the writer goes quiet, a checkpoint follows whatever was written last, even
// if nobody was waiting for it.
func TestCheckpointFollowsAnIdleWriter(t *testing.T) {
	s, path := openTemp(t)
	ctx := t.Context()
	s.ck.setDelays(time.Hour, 80*time.Millisecond)
	if _, err := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a checkpoint once the writer is idle", func() bool { return s.ck.runs.Load() >= 1 })
	if !inMainFile(t, path, hasItem, "/media/a.mp4") {
		t.Error("the item is not in the database file after the idle checkpoint")
	}
}

// A closed store does not checkpoint: what was scheduled is cancelled.
func TestCloseCancelsTheCheckpoint(t *testing.T) {
	s, _ := openTemp(t)
	s.ck.setDelays(100*time.Millisecond, 100*time.Millisecond)
	if err := s.SetSetting(t.Context(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := s.ck.runs.Load(); n != 0 {
		t.Errorf("%d checkpoints after Close", n)
	}
}
