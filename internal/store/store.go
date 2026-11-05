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
	"runtime"
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
// concurrent use.
//
// It holds two pools on one database file. The file is in WAL mode, so readers
// and the writer do not block each other: rw has the one connection that writes,
// ro has several that only read. Reads (ListItems, GetEntry, Continue, ...) run
// on ro and never wait for a write. Writes run on rw through s.write, one at a
// time, the ones a person is waiting for ahead of the background ones (see
// writerLock), and the big ones in chunks (see writeChunk). A checkpoint follows
// the interactive ones (see checkpointer).
type Store struct {
	rw       *sql.DB
	ro       *sql.DB
	wl       writerLock
	ck       checkpointer
	bg       context.Context // ends at Close: what the store does on its own stops with it
	stopBG   context.CancelFunc
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
	rw, err := sql.Open("sqlite", dsn(abs))
	if err != nil {
		return nil, err
	}
	// One writer connection: SQLite has one writer anyway, and Store.write
	// queues the callers in front of it by priority.
	rw.SetMaxOpenConns(1)
	rw.SetMaxIdleConns(1)

	s := &Store{rw: rw}
	ctx := context.Background()
	if err := rw.PingContext(ctx); err != nil {
		rw.Close()
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	if err := s.migrate(ctx); err != nil {
		rw.Close()
		return nil, fmt.Errorf("migrate %s: %w", abs, err)
	}
	if err := s.stampSince(ctx); err != nil {
		rw.Close()
		return nil, fmt.Errorf("open %s: %w", abs, err)
	}
	// The metadata keys (TMDB's, TVDB's and its PIN) are in here. A database an earlier version left readable is
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
				rw.Close()
				return nil, err
			}
			if unsupported == nil {
				unsupported = err
			}
		default:
			rw.Close()
			return nil, err
		}
	}
	if unsupported != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("cannot make the database files owner-only (%v): the filesystem "+
			"may not support permissions; keep the data directory %s private, it holds the metadata keys", unsupported, filepath.Dir(abs)))
	}

	// The readers come last: the database exists, is migrated, and its files are
	// closed up. They only read what the writer has made.
	ro, err := sql.Open("sqlite", roDSN(abs))
	if err != nil {
		rw.Close()
		return nil, err
	}
	s.bg, s.stopBG = context.WithCancel(context.Background())
	n := readConns()
	ro.SetMaxOpenConns(n)
	ro.SetMaxIdleConns(n) // keep them: a new connection runs its pragmas again
	if err := ro.PingContext(ctx); err != nil {
		ro.Close()
		rw.Close()
		s.stopBG()
		return nil, fmt.Errorf("open %s read-only: %w", abs, err)
	}
	s.ro = ro
	s.ck.start(s.checkpoint)
	return s, nil
}

// readConns is the size of the read pool: a connection per CPU up to four, and at
// least two so that a long read does not make every other one wait. Four is
// where more readers stop paying (BenchmarkListItemsReaders), and each connection
// keeps a page cache of its own.
func readConns() int { return max(2, min(runtime.NumCPU(), 4)) }

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
		return fmt.Errorf("%s belongs to another user (uid %d); ply keeps the metadata keys in it", path, uid)
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

// snapshot runs a multi-query read in one read transaction on a reader
// connection, so that it sees one committed state, whatever the writer does
// meanwhile. Without it a row deleted between two of the queries comes back with
// its streams, sidecars or progress missing. fn must use q, never s.ro: the
// pool is finite, and a read that waits for a second connection while holding
// the first can deadlock with its peers.
func (s *Store) snapshot(ctx context.Context, fn func(q queryer) error) error {
	tx, err := s.ro.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// Close closes both pools; the checkpointer stops first. Whichever connection
// closes last checkpoints the WAL and removes its file (the readers are not
// read-only files, only query_only), so the order of the two is only tidy.
func (s *Store) Close() error {
	s.ck.stop()
	if s.stopBG != nil {
		s.stopBG()
	}
	var roErr error
	if s.ro != nil {
		roErr = s.ro.Close()
	}
	return errors.Join(roErr, s.rw.Close())
}

// pragmas every connection of both pools gets, through the DSN: a pool that opens
// a new connection (the readers do, up to readConns) runs them again. busy_timeout
// first, so that the others wait for a lock rather than fail. synchronous=NORMAL
// does not corrupt the file in WAL mode, but a commit then does not wait for the
// disk: a crash of ply loses nothing, a power cut can take back what was
// committed since the last checkpoint (see checkpointer, which bounds that).
const pragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

// dsn builds the SQLite URI of the writer connection for an absolute path. The
// path is URL-escaped (the macOS default data dir contains a space). Its
// transactions begin immediate: they take the write lock at once, so that a
// transaction that reads and then writes never fails at the upgrade.
func dsn(abs string) string { return dsnWith(abs, "_txlock=immediate") }

// roDSN is dsn for the readers: query_only makes SQLite refuse any write on the
// connection, so a write routed to a reader fails instead of racing the writer.
//
// It is not mode=ro. That is stricter (no pragma turns it off) but, measured with
// this driver on a WAL database, it makes reads about twice as slow and keeps
// readers from scaling: ListItems on 3,000 items takes 30 ms with query_only and
// 55 ms with mode=ro, and with 4 readers at once 38 ms against 101 ms
// (BenchmarkListItemsReaders).
func roDSN(abs string) string { return dsnWith(abs, "_pragma=query_only(1)") }

func dsnWith(abs, extra string) string {
	u := url.URL{Scheme: "file", Path: abs, RawQuery: pragmas + "&" + extra}
	return u.String()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.rw.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	// Every version that is not recorded is applied, in order: a database that has 8 and
	// not 7 (a branch that numbered its migration later than the other's) gets 7 too.
	applied := map[int]bool{}
	rows, err := s.rw.QueryContext(ctx, `SELECT version FROM schema_version`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
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
		if applied[version] {
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

// foreignKeysOff is the line a migration starts with when it rebuilds a table that
// others refer to: SQLite cannot alter a CHECK, so the table is created anew, filled,
// swapped in, and with foreign keys on the DROP would run the ON DELETE actions of
// every row that refers to it. The pragma has no effect inside a transaction, so the
// runner turns the keys off before it begins one, checks them (PRAGMA
// foreign_key_check) before it commits, and turns them on again.
const foreignKeysOff = "-- ply:foreign-keys-off"

// applyMigration runs on the writer connection without the writer lock: Open
// calls it before anything else can reach the store.
func (s *Store) applyMigration(ctx context.Context, version int, body string) (err error) {
	// One connection for all of it: the pragma belongs to the connection.
	conn, err := s.rw.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	keysOff := strings.HasPrefix(strings.TrimSpace(body), foreignKeysOff)
	if keysOff {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer func() {
			if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); rerr != nil && err == nil {
				err = rerr
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if keysOff {
		if err := checkForeignKeys(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// checkForeignKeys fails when a row refers to one that is not there.
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fk int
		if err := rows.Scan(&table, &rowid, &parent, &fk); err != nil {
			return err
		}
		return fmt.Errorf("row %d of %s refers to a missing row of %s", rowid.Int64, table, parent)
	}
	return rows.Err()
}
