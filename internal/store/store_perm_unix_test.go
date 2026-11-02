//go:build unix

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// The database holds the TMDB key: nobody but its owner reads it, nor the
// write-ahead log and the shared-memory file next to it (they hold the same
// pages), nor lists the directory made for it.
func TestDatabaseFilesAreOwnerOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", "ply", "ply.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSetting(ctx, "tmdb_key", "secret"); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, filepath.Dir(path)); m != 0o700 {
		t.Errorf("data dir mode = %v, want 0700", m)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if m := mode(t, f); m != 0o600 { // the stat is also the control that the file exists
			t.Errorf("%s mode = %v, want 0600", filepath.Base(f), m)
		}
	}
}

// A database an earlier version left readable is closed up on the next start; a
// directory that already exists is the user's own and is left as it is.
func TestOpenTightensAnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ply")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ply.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(context.Background(), "tmdb_key", "secret"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(f, 0o644); err != nil {
			t.Fatalf("chmod %s: %v (the WAL files exist while the database is open)", f, err)
		}
	}
	// A second connection to the open database: Open does not need the first one closed.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	defer s.Close()
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if m := mode(t, f); m != 0o600 {
			t.Errorf("%s mode = %v after Open, want 0600", filepath.Base(f), m)
		}
	}
	if m := mode(t, dir); m != 0o755 {
		t.Errorf("existing dir mode = %v, want it left at 0755", m)
	}
}
