package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// keepBackups is how many copies made before an upgrade are kept.
const keepBackups = 3

// pendingMigrations says which schema version an existing database is at (0 for a new
// or empty one, which has nothing to lose) and whether there are migrations it has not had.
func (s *Store) pendingMigrations(ctx context.Context) (from int, pending bool, err error) {
	var n int
	if err := s.rw.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`).Scan(&n); err != nil || n == 0 {
		return 0, false, err
	}
	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return 0, false, err
	}
	files, err := migrationFiles()
	if err != nil {
		return 0, false, err
	}
	for v := range applied {
		from = max(from, v)
	}
	for _, m := range files {
		pending = pending || !applied[m.version]
	}
	return from, pending, nil
}

// backupBeforeMigration copies the database, as it is at schema version from, to
// <db>.bak-v<from> (VACUUM INTO: a consistent copy with whatever is in the WAL) and keeps
// the keepBackups newest copies. The copy holds the metadata keys as the database does, so it
// is made owner-only the way the database is: created that way, chmod-ed, and the same
// checks (closeUp). It is written under another name and renamed, so a copy is never half
// there. It returns the line to log, and the soft failure of the chmod, if the filesystem
// has no permissions. An error means no backup was made, and the database must not be migrated.
func (s *Store) backupBeforeMigration(ctx context.Context, db string, from int, chmod func(string, os.FileMode) error) (notice string, soft error, err error) {
	target := db + ".bak-v" + strconv.Itoa(from)
	tmp := target + ".tmp"
	_ = os.Remove(tmp) // left by a start that was killed
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	f.Close()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp)
		}
	}()
	// SQLite writes into a file that is there and empty.
	if _, err := s.rw.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return "", nil, err
	}

	if soft, err = closeUp(tmp, chmod); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", nil, err
	}
	done = true
	pruneBackups(db)
	return fmt.Sprintf("backed up the database to %s before upgrading it from schema version %d", target, from), soft, nil
}

// pruneBackups removes all but the keepBackups newest copies of the database db that
// backupBeforeMigration made (by when they were written, then by name). It is a tidy-up: what it
// cannot remove stays.
func pruneBackups(db string) {
	dir, base := filepath.Dir(db), filepath.Base(db)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	name := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.bak-v\d+$`)
	type backup struct {
		path string
		mod  time.Time
	}
	var backups []backup
	for _, e := range entries {
		if !e.Type().IsRegular() || !name.MatchString(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			backups = append(backups, backup{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(backups, func(i, j int) bool {
		if !backups[i].mod.Equal(backups[j].mod) {
			return backups[i].mod.After(backups[j].mod)
		}
		return backups[i].path > backups[j].path
	})
	for _, b := range backups[min(keepBackups, len(backups)):] {
		_ = os.Remove(b.path)
	}
}
