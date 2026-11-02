// Package store persists the library in SQLite (modernc.org/sqlite, no cgo).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is a SQLite-backed library store. All methods are safe for
// concurrent use; the underlying pool has a single connection.
type Store struct {
	db       *sql.DB
	warnings []string
}

// Warnings returns what Open could live with but the caller should log: the
// store has no logger of its own.
func (s *Store) Warnings() []string { return s.warnings }

// Open opens (creating if needed) the database at path and applies pending
// migrations. The parent directory is created (readable by its owner only) if
// missing, a new database file is created readable by its owner only, and the
// files of an existing one are made so. On a filesystem without permissions
// (vfat, exfat, some network mounts) the last step is a warning, not an error.
func Open(path string) (*Store, error) { return open(path, os.Chmod) }

// open is Open with the chmod to use, so that a test can make it fail.
func open(path string, chmod func(string, os.FileMode) error) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	// SQLite would create the file with the mode the umask leaves (0644) and it
	// would stay readable until the chmod below. The -wal and -shm files it makes
	// next take the mode of the database.
	if f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", abs, err)
	}
	db, err := sql.Open("sqlite", dsn(abs))
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serialises writers anyway, and it keeps
	// per-connection pragmas (foreign_keys) trivially consistent.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", abs, err)
	}
	// The TMDB key is in here. A database an earlier version left readable is
	// closed up now.
	var unsupported error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := chmod(abs+suffix, 0o600)
		switch {
		case err == nil || errors.Is(err, os.ErrNotExist):
		case noPermissions(err):
			// Only a file of ours on a filesystem that ignores modes is let
			// through: EPERM also means "someone else's file", and that is no
			// place for the key.
			if err := ownFile(abs + suffix); err != nil {
				db.Close()
				return nil, err
			}
			if unsupported == nil {
				unsupported = err
			}
		default:
			db.Close()
			return nil, err
		}
	}
	if unsupported != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("cannot make the database files owner-only (%v): the filesystem "+
			"may not support permissions; keep the data directory %s private, it holds the TMDB key", unsupported, filepath.Dir(abs)))
	}
	return s, nil
}

// ownFile returns an error unless path is a regular file owned by this user
// (or is not there: nothing to protect).
func ownFile(path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if uid, ok := fileOwner(fi); ok && uid != os.Geteuid() {
		return fmt.Errorf("%s belongs to another user (uid %d); ply keeps the TMDB key in it", path, uid)
	}
	return nil
}

// fileOwner is the owning uid of fi, where the platform has one; a variable so
// that a test can play another user.
var fileOwner = ownerOf

// noPermissions reports whether err says the filesystem has no say over modes
// (vfat, exfat and many network mounts), or does not let this user change them.
func noPermissions(err error) bool {
	for _, e := range []error{syscall.EPERM, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EROFS} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// queryer is the read side that *sql.DB and *sql.Tx share.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// snapshot runs a multi-query read in one transaction, so that it sees one
// committed state. Without it a row deleted between two of the queries comes
// back with its streams, sidecars or progress missing. The pool has a single
// connection: fn must use q, never s.db.
func (s *Store) snapshot(ctx context.Context, fn func(q queryer) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// dsn builds the SQLite URI for an absolute path. The path is URL-escaped
// (the macOS default data dir contains a space).
func dsn(abs string) string {
	u := url.URL{
		Scheme:   "file",
		Path:     abs,
		RawQuery: "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
	}
	return u.String()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("migration %q: name must start with a number", e.Name())
		}
		if version <= current {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if err := s.applyMigration(ctx, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version int, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}
