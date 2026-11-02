//go:build unix

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// A new database is created owner-only in the first place, not chmod-ed after
// SQLite made it as readable as the umask allows: in a directory that is
// already open to others (the user's own, left as it is) there would be a window
// in which the file is readable. The chmod is switched off and the umask opened
// so that only the creation can be what makes the mode.
func TestOpenCreatesANewDatabaseOwnerOnly(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0))
	dir := filepath.Join(t.TempDir(), "ply")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ply.db")
	var chmods []string
	s, err := open(path, func(name string, perm os.FileMode) error {
		chmods = append(chmods, filepath.Base(name))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSetting(context.Background(), "tmdb_key", "secret"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		if m := mode(t, f); m != 0o600 {
			t.Errorf("%s mode = %v without a chmod, want 0600", filepath.Base(f), m)
		}
	}
	if got := strings.Join(chmods, " "); got != "ply.db ply.db-wal ply.db-shm" {
		t.Errorf("chmod was asked for %q", got)
	}
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("warnings = %q, want none", w)
	}
	if m := mode(t, dir); m != 0o755 {
		t.Errorf("existing dir mode = %v, want it left at 0755", m)
	}
}

// vfat, exfat and network filesystems refuse chmod, or ignore it: ply still
// starts, and says so. Any other failure is real.
func TestOpenCarriesOnWhenTheFilesystemRefusesChmod(t *testing.T) {
	ctx := context.Background()
	refuse := func(err error) func(string, os.FileMode) error {
		return func(name string, _ os.FileMode) error { return &os.PathError{Op: "chmod", Path: name, Err: err} }
	}
	for _, errno := range []syscall.Errno{syscall.EPERM, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EROFS} {
		path := filepath.Join(t.TempDir(), "data", "ply.db")
		s, err := open(path, refuse(errno))
		if err != nil {
			t.Errorf("%v: Open failed: %v", errno, err)
			continue
		}
		if err := s.SetSetting(ctx, "language", "fr-FR"); err != nil { // the store works
			t.Errorf("%v: %v", errno, err)
		}
		w := s.Warnings()
		if len(w) != 1 || !strings.Contains(w[0], errno.Error()) || !strings.Contains(w[0], filepath.Dir(path)) {
			t.Errorf("%v: warnings = %q, want one naming the error and the data dir", errno, w)
		}
		s.Close()
	}

	// Controls: no warning when chmod works, nor when the -wal and -shm files
	// are not there to be chmod-ed.
	s, err := open(filepath.Join(t.TempDir(), "ply.db"), func(name string, _ os.FileMode) error {
		if strings.HasSuffix(name, "ply.db") {
			return nil
		}
		return &os.PathError{Op: "chmod", Path: name, Err: os.ErrNotExist}
	})
	if err != nil {
		t.Fatalf("missing -wal and -shm: %v", err)
	}
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("missing -wal and -shm: warnings %q", w)
	}
	s.Close()

	// An error that is not about permissions on the filesystem still fails.
	path := filepath.Join(t.TempDir(), "ply.db")
	if s, err := open(path, refuse(syscall.EIO)); !errors.Is(err, syscall.EIO) {
		t.Errorf("EIO: Open = %v, want the error", err)
	} else if s != nil {
		t.Error("a failed Open returned a store")
	}
	// EACCES is not "the filesystem has no permissions": the file is ours to change.
	if _, err := open(filepath.Join(t.TempDir(), "ply.db"), refuse(syscall.EACCES)); !errors.Is(err, syscall.EACCES) {
		t.Errorf("EACCES: Open = %v, want the error", err)
	}
}

// EPERM from chmod is only a warning for a regular file of ours: a database
// file of another user, or something that is not a file, is refused.
func TestOpenRefusesADatabaseItCannotOwn(t *testing.T) {
	eperm := func(name string, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: name, Err: syscall.EPERM}
	}

	// Control: our own file on a filesystem that refuses chmod opens, with a warning.
	s, err := open(filepath.Join(t.TempDir(), "ply.db"), eperm)
	if err != nil {
		t.Fatalf("own file: %v", err)
	}
	s.Close()

	// Someone else's file.
	defer func(f func(os.FileInfo) (int, bool)) { fileOwner = f }(fileOwner)
	fileOwner = func(os.FileInfo) (int, bool) { return os.Geteuid() + 1, true }
	if s, err := open(filepath.Join(t.TempDir(), "ply.db"), eperm); err == nil || !strings.Contains(err.Error(), "another user") {
		if s != nil {
			s.Close()
		}
		t.Errorf("another user's file: err = %v, want a refusal", err)
	}
	fileOwner = ownerOf

	// A -wal that is a directory, not a file.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ply.db-wal-probe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ownFile(filepath.Join(dir, "ply.db-wal-probe")); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: err = %v, want not a regular file", err)
	}
	if err := ownFile(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("missing file: %v, want nil (nothing to protect)", err)
	}
}
