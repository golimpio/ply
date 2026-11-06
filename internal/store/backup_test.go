//go:build unix

package store

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// v10Database makes a database as the version before the watch state left it, with a watched file in it,
// closed. It returns its path.
func v10Database(t *testing.T) string {
	t.Helper()
	old, path := openAtVersion(t, 10)
	id := insertOldItem(t, old, "/media/seen.mp4", "Seen")
	if _, err := old.rw.Exec(`INSERT INTO progress (item_id, position_sec, duration_sec, watched, updated_at) VALUES (?, 7100, 7200, 1, 1700000100000000000)`, id); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// readOnly opens a database file to look at it, with nothing of the store's around it.
func readOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func versionOf(t *testing.T, path string) (version int, progressColumns []string) {
	t.Helper()
	db := readOnly(t, path)
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('progress')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		progressColumns = append(progressColumns, c)
	}
	return version, progressColumns
}

// backupsIn lists the copies made before an upgrade, by name.
func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.bak-v*"))
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		names[i] = filepath.Base(n)
	}
	slices.Sort(names)
	return names
}

func TestNewDatabaseMakesNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ply", "ply.db")
	var chmods []string
	s, err := open(path, func(name string, _ os.FileMode) error {
		chmods = append(chmods, filepath.Base(name))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := backupsIn(t, filepath.Dir(path)); len(got) != 0 {
		t.Errorf("a new database made backups: %v", got)
	}
	if n := s.Notices(); len(n) != 0 {
		t.Errorf("notices = %q, want none", n)
	}
	if got := strings.Join(chmods, " "); got != "ply.db ply.db-wal ply.db-shm" {
		t.Errorf("chmod was asked for %q", got)
	}
}

// An existing database that has migrations waiting is copied first, as it is, and the copy is private.
func TestUpgradeBacksTheDatabaseUpFirst(t *testing.T) {
	path := v10Database(t)
	dir := filepath.Dir(path)
	if got := backupsIn(t, dir); len(got) != 0 { // control
		t.Fatalf("control: backups before the upgrade: %v", got)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backup := path + ".bak-v10"
	if got := backupsIn(t, dir); !slices.Equal(got, []string{"ply.db.bak-v10"}) {
		t.Fatalf("backups = %v, want only ply.db.bak-v10", got)
	}
	if m := mode(t, backup); m != 0o600 {
		t.Errorf("the backup is %v, want 0600: it holds the metadata keys", m)
	}
	// It is the database as it was: version 10, a progress table with the old flag, the watched file.
	version, cols := versionOf(t, backup)
	if version != 10 || !slices.Contains(cols, "watched") || slices.Contains(cols, "watched_at") {
		t.Errorf("the backup is at version %d with progress %v, want the database before the upgrade", version, cols)
	}
	var watched int
	if err := readOnly(t, backup).QueryRow(`SELECT watched FROM progress`).Scan(&watched); err != nil || watched != 1 {
		t.Errorf("the watched file in the backup = %d, %v", watched, err)
	}
	// The upgraded database is not the backup: it moved on.
	if v := queryInt(t, s, `SELECT MAX(version) FROM schema_version`); v != slices.Max(migrationNumbers(t)) {
		t.Errorf("schema version = %d", v)
	}
	if cols := columns(t, s.rw, "progress"); !slices.Contains(cols, "watched_at") {
		t.Errorf("the upgraded progress = %v", cols)
	}
	if n := s.Notices(); len(n) != 1 || !strings.Contains(n[0], backup) || !strings.Contains(n[0], "10") {
		t.Errorf("notices = %q, want one line with the backup's path and the version", n)
	}
	if w := s.Warnings(); len(w) != 0 {
		t.Errorf("warnings = %q", w)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Opened again, there is nothing to migrate and nothing is copied (control for the above).
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := backupsIn(t, dir); !slices.Equal(got, []string{"ply.db.bak-v10"}) {
		t.Errorf("backups after a start with nothing to migrate = %v", got)
	}
	if n := again.Notices(); len(n) != 0 {
		t.Errorf("notices = %q, want none", n)
	}
}

// Only the three newest copies are kept; what is not a copy is left alone.
func TestOnlyTheNewestThreeBackupsAreKept(t *testing.T) {
	path := v10Database(t)
	dir := filepath.Dir(path)
	now := time.Now()
	write := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("older "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	// Older copies; v9 is the newest of them, and the version numbers do not say which is the newest.
	write("ply.db.bak-v6", 50*time.Hour)
	write("ply.db.bak-v9", 10*time.Hour)
	write("ply.db.bak-v7", 40*time.Hour)
	write("ply.db.bak-v12", 30*time.Hour)
	// Not copies of this database.
	write("ply.db.bak-vx", 90*time.Hour)
	write("ply.db.bak-v9.old", 90*time.Hour)
	write("other.db.bak-v3", 90*time.Hour)
	write("notes.txt", 90*time.Hour)

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := []string{"ply.db.bak-v10", "ply.db.bak-v12", "ply.db.bak-v9", "ply.db.bak-v9.old", "ply.db.bak-vx", "other.db.bak-v3"}
	slices.Sort(want)
	if got := backupsIn(t, dir); !slices.Equal(got, want) {
		t.Errorf("after the upgrade: %v\nwant the new one and the two newest before it (v9, v12), and what is not a copy: %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("notes.txt: %v", err)
	}
}

// The copy is private from the moment it is made (not chmod-ed after SQLite made it as readable as the umask
// says), through the same chmod and the same check of the owner as the database's own files; a copy a start
// that was killed left half made is replaced.
func TestBackupIsPrivateWhateverTheUmask(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0))
	path := v10Database(t)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := path + ".bak-v10.tmp"
	if err := os.WriteFile(stale, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stale, 0o644); err != nil {
		t.Fatal(err)
	}

	var chmods []string
	s, err := open(path, func(name string, _ os.FileMode) error { // the chmod is off: only the creation can make the mode
		chmods = append(chmods, filepath.Base(name))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m := mode(t, path+".bak-v10"); m != 0o600 {
		t.Errorf("the backup is %v without a chmod, want 0600", m)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the half-made copy is still there: %v", err)
	}
	if !slices.Contains(chmods, "ply.db.bak-v10.tmp") {
		t.Errorf("chmod was asked for %v, want the copy among them", chmods)
	}
	if version, _ := versionOf(t, path+".bak-v10"); version != 10 {
		t.Errorf("the backup is at version %d", version)
	}
}

// On a filesystem that has no permissions the copy is made and the start says so; a copy that is not the
// user's, or a failure of the chmod that is not about permissions, stops the upgrade with nothing changed.
func TestBackupOnFilesystemsWithAndWithoutPermissions(t *testing.T) {
	refuse := func(err error) func(string, os.FileMode) error {
		return func(name string, _ os.FileMode) error { return &os.PathError{Op: "chmod", Path: name, Err: err} }
	}

	// No permissions: carries on, with a warning.
	path := v10Database(t)
	s, err := open(path, refuse(syscall.EPERM))
	if err != nil {
		t.Fatalf("EPERM: %v", err)
	}
	if w := s.Warnings(); len(w) != 1 || !strings.Contains(w[0], filepath.Dir(path)) {
		t.Errorf("warnings = %q, want one naming the data directory", w)
	}
	if got := backupsIn(t, filepath.Dir(path)); !slices.Equal(got, []string{"ply.db.bak-v10"}) {
		t.Errorf("backups = %v", got)
	}
	s.Close()

	// A failure that is not about permissions: no copy, no upgrade.
	path = v10Database(t)
	if s, err := open(path, refuse(syscall.EIO)); !errors.Is(err, syscall.EIO) || s != nil {
		t.Fatalf("EIO: open = %v, %v", s, err)
	}
	assertNotUpgraded(t, path)

	// Someone else's file where the copy goes.
	path = v10Database(t)
	defer func(f func(os.FileInfo) (int, bool)) { fileOwner = f }(fileOwner)
	fileOwner = func(os.FileInfo) (int, bool) { return os.Geteuid() + 1, true }
	if s, err := open(path, refuse(syscall.EPERM)); err == nil || !strings.Contains(err.Error(), "another user") || s != nil {
		t.Fatalf("another user's file: open = %v, %v", s, err)
	}
	assertNotUpgraded(t, path)
}

// assertNotUpgraded checks that the database at path is still at version 10 and that no copy, whole or
// half, is left.
func assertNotUpgraded(t *testing.T, path string) {
	t.Helper()
	if version, cols := versionOf(t, path); version != 10 || !slices.Contains(cols, "watched") {
		t.Errorf("the database is at version %d with progress %v after a start that failed", version, cols)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-v") {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}

// A copy that cannot be made stops the upgrade: the database is not changed without one.
func TestNoUpgradeWithoutABackup(t *testing.T) {
	path := v10Database(t)
	blocked := path + ".bak-v10" // a directory with something in it is not replaced by a file
	if err := os.MkdirAll(filepath.Join(blocked, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "back up") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open = %v, want a failure to back up", err)
	}
	if version, cols := versionOf(t, path); version != 10 || !slices.Contains(cols, "watched") {
		t.Errorf("the database was upgraded without a backup: version %d, progress %v", version, cols)
	}
	if _, err := os.Stat(filepath.Join(blocked, "keep")); err != nil {
		t.Errorf("what was in the way was removed: %v", err)
	}
	if _, err := os.Stat(blocked + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the half-made copy was left: %v", err)
	}
}
