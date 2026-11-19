package plex

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// dbRelPath is the Plex library database within the data directory.
const dbRelPath = "Plug-in Support/Databases/com.plexapp.plugins.library.db"

// DBSource reads a read-only copy of Plex's library database, and its Metadata
// bundles for artwork. It never opens the live database read-write: OpenDB copies
// the database (and its WAL sidecars) to a scratch directory first.
type DBSource struct {
	db      *sql.DB
	dataDir string
	account int64
	scratch string
	typeCol string // metadata_items' type column: "metadata_type" (Plex DB) or "type"
}

// OpenDB copies the Plex database under dataDir to a scratch directory and opens
// the copy read-only. account is the Plex account id whose watch state to read
// (1 is usually the admin). The caller must Close it (which removes the scratch).
func OpenDB(ctx context.Context, dataDir string, account int64) (*DBSource, error) {
	src := filepath.Join(dataDir, filepath.FromSlash(dbRelPath))
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("plex database not found at %s: %w", src, err)
	}
	scratch, err := os.MkdirTemp("", "plex-migrate-")
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(scratch, "library.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(src+suffix, dst+suffix); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue // a checkpointed database has no WAL sidecars
			}
			os.RemoveAll(scratch)
			return nil, fmt.Errorf("copy plex database: %w", err)
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: dst, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		os.RemoveAll(scratch)
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		os.RemoveAll(scratch)
		return nil, fmt.Errorf("open plex database copy: %w", err)
	}
	typeCol, err := detectTypeCol(ctx, db)
	if err != nil {
		db.Close()
		os.RemoveAll(scratch)
		return nil, err
	}
	return &DBSource{db: db, dataDir: dataDir, account: account, scratch: scratch, typeCol: typeCol}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (s *DBSource) Close() error {
	err := s.db.Close()
	if s.scratch != "" {
		os.RemoveAll(s.scratch)
	}
	return err
}

// rowsAsMaps reads a query into one map per row (column name → value as text),
// so a column Plex added or renamed across versions does not break the scan.
func (s *DBSource) rowsAsMaps(ctx context.Context, query string, args ...any) ([]map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]string, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case nil:
				m[c] = ""
			case []byte:
				m[c] = string(v)
			default:
				m[c] = fmt.Sprint(v)
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *DBSource) hasTable(ctx context.Context, name string) bool {
	var got string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	return err == nil
}

// detectTypeCol finds metadata_items' type column. The Plex database calls it
// "metadata_type"; the HTTP API (and some fixtures) call it "type". Introspecting
// it keeps the reader working across Plex versions instead of hardcoding a name.
func detectTypeCol(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(metadata_items)`)
	if err != nil {
		return "", fmt.Errorf("metadata_items schema: %w", err)
	}
	defer rows.Close()
	var hasType, hasMetaType bool
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return "", err
		}
		switch name {
		case "metadata_type":
			hasMetaType = true
		case "type":
			hasType = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch {
	case hasMetaType:
		return "metadata_type", nil
	case hasType:
		return "type", nil
	}
	return "", fmt.Errorf("metadata_items has no type column (metadata_type or type)")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// Libraries returns the sections and their folders. The kind is taken from the
// metadata types present (1 movie, 2 show); a section with neither is skipped.
func (s *DBSource) Libraries(ctx context.Context) ([]Library, error) {
	secs, err := s.rowsAsMaps(ctx, `SELECT id, name FROM library_sections`)
	if err != nil {
		return nil, fmt.Errorf("library_sections: %w", err)
	}
	hasLoc := s.hasTable(ctx, "section_locations")
	var out []Library
	for _, sec := range secs {
		id := sec["id"]
		var typ string
		_ = s.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT %s FROM metadata_items WHERE library_section_id=? AND %s IN (1,2) LIMIT 1`, s.typeCol, s.typeCol), id).Scan(&typ)
		kind, ok := KindOfType(atoi(typ))
		if !ok {
			continue // music, photos, …: out of scope
		}
		lib := Library{Key: id, Title: sec["name"], Kind: kind}
		if hasLoc {
			locs, err := s.rowsAsMaps(ctx, `SELECT root_path FROM section_locations WHERE library_section_id=?`, id)
			if err == nil {
				for _, l := range locs {
					if l["root_path"] != "" {
						lib.Paths = append(lib.Paths, l["root_path"])
					}
				}
			}
		}
		out = append(out, lib)
	}
	return out, nil
}

// Items returns the movies, shows, seasons and episodes flattened, with their
// files, external ids, selected artwork references and watch state.
func (s *DBSource) Items(ctx context.Context) ([]Item, error) {
	mi, err := s.rowsAsMaps(ctx, fmt.Sprintf(`SELECT * FROM metadata_items WHERE %s IN (1,2,3,4)`, s.typeCol))
	if err != nil {
		return nil, fmt.Errorf("metadata_items: %w", err)
	}
	byID := make(map[string]map[string]string, len(mi))
	for _, m := range mi {
		byID[m["id"]] = m
	}

	files := map[string][]string{}
	fr, err := s.rowsAsMaps(ctx, fmt.Sprintf(`SELECT mi.id AS id, mp.file AS file
		FROM metadata_items mi
		JOIN media_items m ON m.metadata_item_id = mi.id
		JOIN media_parts mp ON mp.media_item_id = m.id
		WHERE mi.%s IN (1,4)`, s.typeCol))
	if err != nil {
		return nil, fmt.Errorf("media_parts: %w", err)
	}
	for _, f := range fr {
		if f["file"] != "" {
			files[f["id"]] = append(files[f["id"]], f["file"])
		}
	}

	guids := map[string][]string{}
	if s.hasTable(ctx, "guids") {
		gr, err := s.rowsAsMaps(ctx, `SELECT metadata_item_id, guid FROM guids`)
		if err == nil {
			for _, g := range gr {
				guids[g["metadata_item_id"]] = append(guids[g["metadata_item_id"]], g["guid"])
			}
		}
	}

	watch := map[string]Watch{}
	if s.hasTable(ctx, "metadata_item_views") {
		wr, err := s.rowsAsMaps(ctx, `SELECT * FROM metadata_item_views WHERE account_id=?`, s.account)
		if err == nil {
			for _, w := range wr {
				watch[w["metadata_item_id"]] = Watch{
					OffsetMS:   atoi64(w["view_offset"]),
					ViewCount:  atoi64(w["view_count"]),
					LastViewed: atoi64(w["last_viewed_at"]),
					UserRating: atof(w["rating"]),
				}
			}
		}
	}

	var out []Item
	for _, m := range mi {
		kind, ok := KindOfType(atoi(m[s.typeCol]))
		if !ok {
			continue
		}
		it := Item{
			RatingKey: m["id"],
			Kind:      kind,
			Title:     m["title"],
			Year:      atoi(m["year"]),
			GUID:      m["guid"],
			Hash:      m["hash"],
			IDs:       ParseIDs(m["guid"], guids[m["id"]]),
			Files:     files[m["id"]],
			PosterURL: firstNonEmpty(m["user_thumb_url"], m["thumb_url"]),
			ArtURL:    firstNonEmpty(m["user_art_url"], m["art_url"]),
			Custom:    m["user_thumb_url"] != "",
			Watch:     watch[m["id"]],
		}
		switch kind {
		case Season:
			it.Show = titleOf(byID, m["parent_id"])
			it.Season = atoi(m["index"])
		case Episode:
			season := byID[m["parent_id"]]
			it.Show = titleOf(byID, season["parent_id"])
			it.Season = atoi(season["index"])
			it.Episode = atoi(m["index"])
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

func titleOf(byID map[string]map[string]string, id string) string {
	if m, ok := byID[id]; ok {
		return m["title"]
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Art resolves the item's bundle and reads the selected image: an uploaded
// poster (the user's pick) is preferred, else a downloaded candidate. This is
// approximate when several candidates exist; the API source is exact.
func (s *DBSource) Art(ctx context.Context, it Item, slot string) ([]byte, error) {
	if it.Hash == "" || len(it.Hash) < 2 {
		return nil, ErrNoArt
	}
	sub := "TV Shows"
	if it.Kind == Movie {
		sub = "Movies"
	}
	bundle := filepath.Join(s.dataDir, "Metadata", sub, it.Hash[:1], it.Hash[1:]+".bundle")
	dir := "posters"
	if slot == "art" {
		dir = "art"
	}
	for _, d := range []string{
		filepath.Join(bundle, "Uploads", dir),
		filepath.Join(bundle, "Contents", "_combined", dir),
		filepath.Join(bundle, "Contents", "_stored", dir),
	} {
		if f := newestFile(d); f != "" {
			return os.ReadFile(f)
		}
	}
	return nil, ErrNoArt
}

// newestFile returns the most recently modified regular file in dir, "" if none.
func newestFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestTime int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if t := fi.ModTime().UnixNano(); best == "" || t > bestTime {
			best, bestTime = filepath.Join(dir, e.Name()), t
		}
	}
	return best
}
