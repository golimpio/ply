package plex

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// fixtureDB builds a minimal Plex database (and one artwork bundle) under a temp
// data dir, shaped like the real schema, and returns the data dir.
func fixtureDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "Plug-in Support", "Databases")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "com.plexapp.plugins.library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE library_sections (id INTEGER PRIMARY KEY, name TEXT, agent TEXT)`,
		`CREATE TABLE section_locations (library_section_id INTEGER, root_path TEXT)`,
		`CREATE TABLE metadata_items (id INTEGER PRIMARY KEY, library_section_id INTEGER, parent_id INTEGER,
			"index" INTEGER, type INTEGER, guid TEXT, hash TEXT, title TEXT, year INTEGER,
			thumb_url TEXT, art_url TEXT, user_thumb_url TEXT, user_art_url TEXT, user_fields TEXT)`,
		`CREATE TABLE media_items (id INTEGER PRIMARY KEY, metadata_item_id INTEGER)`,
		`CREATE TABLE media_parts (id INTEGER PRIMARY KEY, media_item_id INTEGER, file TEXT)`,
		`CREATE TABLE guids (metadata_item_id INTEGER, guid TEXT)`,
		`CREATE TABLE metadata_item_views (account_id INTEGER, metadata_item_id INTEGER,
			view_offset INTEGER, view_count INTEGER, last_viewed_at INTEGER, rating REAL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO library_sections VALUES (1,'Movies','com.plexapp.agents.imdb'),(2,'TV','com.plexapp.agents.thetvdb')`)
	exec(`INSERT INTO section_locations VALUES (1,'/media/Movies'),(2,'/media/TV')`)
	// movie 10: legacy imdb guid, default poster, watched partway.
	exec(`INSERT INTO metadata_items (id,library_section_id,type,guid,hash,title,year,thumb_url)
		VALUES (10,1,1,'com.plexapp.agents.imdb://tt0107302?lang=en','ffffffffffffffffffffffffffffffffffffffff','Kalifornia',1993,'/library/metadata/10/thumb/1')`)
	// movie 11: a custom (uploaded) poster, with a bundle on disk.
	exec(`INSERT INTO metadata_items (id,library_section_id,type,guid,hash,title,year,user_thumb_url)
		VALUES (11,1,1,'com.plexapp.agents.themoviedb://603?lang=en','a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0','The Matrix',1999,'/library/metadata/11/thumb/9')`)
	// show 20 (new agent), season 21, episode 22.
	exec(`INSERT INTO metadata_items (id,library_section_id,type,guid,title,year) VALUES (20,2,2,'plex://show/abc','Breaking Bad',2008)`)
	exec(`INSERT INTO metadata_items (id,library_section_id,parent_id,"index",type,title) VALUES (21,2,20,1,3,'Season 1')`)
	exec(`INSERT INTO metadata_items (id,library_section_id,parent_id,"index",type,guid,title,thumb_url)
		VALUES (22,2,21,1,4,'plex://episode/def','Pilot','/library/metadata/22/thumb/1')`)
	exec(`INSERT INTO media_items VALUES (100,10),(101,11),(102,22)`)
	exec(`INSERT INTO media_parts VALUES (1000,100,'/media/Movies/Kalifornia (1993).mkv'),
		(1001,101,'/media/Movies/The Matrix (1999).mkv'),
		(1002,102,'/media/TV/Breaking Bad/Season 01/Breaking Bad S01E01.mkv')`)
	exec(`INSERT INTO guids VALUES (20,'tmdb://1396'),(20,'tvdb://81189'),(22,'imdb://tt0959621')`)
	exec(`INSERT INTO metadata_item_views VALUES (1,10,60000,1,1700000000,8.0),(1,22,0,1,1700000001,0)`)

	// The custom poster bundle for movie 11: Uploads/posters/<file>.
	bundle := filepath.Join(dir, "Metadata", "Movies", "a", "1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0.bundle", "Uploads", "posters")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "custom"), []byte{0xFF, 0xD8, 0xFF, 0xE0, 'p', 'o', 's', 't'}, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func itemByKey(items []Item, key string) (Item, bool) {
	for _, it := range items {
		if it.RatingKey == key {
			return it, true
		}
	}
	return Item{}, false
}

func TestDBItems(t *testing.T) {
	dir := fixtureDB(t)
	s, err := OpenDB(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, err := s.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	movie, ok := itemByKey(items, "10")
	if !ok {
		t.Fatal("movie 10 missing")
	}
	if movie.Kind != Movie || movie.Title != "Kalifornia" || movie.Year != 1993 {
		t.Errorf("movie = %+v", movie)
	}
	if movie.IDs.IMDb != "tt0107302" {
		t.Errorf("legacy imdb id = %q", movie.IDs.IMDb)
	}
	if len(movie.Files) != 1 || movie.Files[0] != "/media/Movies/Kalifornia (1993).mkv" {
		t.Errorf("movie files = %v", movie.Files)
	}
	if movie.Watch.OffsetMS != 60000 || movie.Watch.ViewCount != 1 || movie.Watch.LastViewed != 1700000000 || movie.Watch.UserRating != 8 {
		t.Errorf("movie watch = %+v", movie.Watch)
	}
	if movie.Custom {
		t.Error("movie 10 has no custom poster")
	}

	show, ok := itemByKey(items, "20")
	if !ok || show.Kind != Show || show.IDs.TMDB != 1396 || show.IDs.TVDB != 81189 {
		t.Fatalf("show = %+v (ok=%v)", show, ok)
	}
	season, ok := itemByKey(items, "21")
	if !ok || season.Kind != Season || season.Show != "Breaking Bad" || season.Season != 1 {
		t.Fatalf("season = %+v (ok=%v)", season, ok)
	}
	ep, ok := itemByKey(items, "22")
	if !ok || ep.Kind != Episode || ep.Show != "Breaking Bad" || ep.Season != 1 || ep.Episode != 1 || ep.IDs.IMDb != "tt0959621" {
		t.Fatalf("episode = %+v (ok=%v)", ep, ok)
	}
	if len(ep.Files) != 1 || ep.Files[0] != "/media/TV/Breaking Bad/Season 01/Breaking Bad S01E01.mkv" {
		t.Errorf("episode files = %v", ep.Files)
	}
}

func TestDBLibraries(t *testing.T) {
	dir := fixtureDB(t)
	s, err := OpenDB(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	libs, err := s.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 {
		t.Fatalf("libraries = %+v", libs)
	}
	byKey := map[string]Library{}
	for _, l := range libs {
		byKey[l.Key] = l
	}
	if l := byKey["1"]; l.Kind != Movie || len(l.Paths) != 1 || l.Paths[0] != "/media/Movies" {
		t.Errorf("movie library = %+v", l)
	}
	if l := byKey["2"]; l.Kind != Show || len(l.Paths) != 1 || l.Paths[0] != "/media/TV" {
		t.Errorf("show library = %+v", l)
	}
}

func TestDBArt(t *testing.T) {
	dir := fixtureDB(t)
	s, err := OpenDB(context.Background(), dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, _ := s.Items(context.Background())
	custom, _ := itemByKey(items, "11")
	if !custom.Custom {
		t.Fatal("movie 11 should be a custom poster")
	}
	b, err := s.Art(context.Background(), custom, "poster")
	if err != nil {
		t.Fatalf("custom art: %v", err)
	}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		t.Errorf("art bytes = %v, want the uploaded JPEG", b)
	}
	// A movie with no bundle has no art (positive control: movie 11 did).
	plain, _ := itemByKey(items, "10")
	if _, err := s.Art(context.Background(), plain, "poster"); err != ErrNoArt {
		t.Errorf("movie 10 art = %v, want ErrNoArt", err)
	}
}

func TestDBOpenMissing(t *testing.T) {
	if _, err := OpenDB(context.Background(), t.TempDir(), 1); err == nil {
		t.Error("OpenDB on a dir with no database should fail")
	}
}

// The real Plex database names the type column "metadata_type", not the API's
// "type". The reader must introspect it (this failed on a live server before).
func TestDBItemsMetadataTypeColumn(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "Plug-in Support", "Databases")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dbDir, "com.plexapp.plugins.library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE library_sections (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE metadata_items (id INTEGER PRIMARY KEY, library_section_id INTEGER, parent_id INTEGER,
			"index" INTEGER, metadata_type INTEGER, guid TEXT, hash TEXT, title TEXT, year INTEGER, thumb_url TEXT, user_thumb_url TEXT)`,
		`CREATE TABLE media_items (id INTEGER PRIMARY KEY, metadata_item_id INTEGER)`,
		`CREATE TABLE media_parts (id INTEGER PRIMARY KEY, media_item_id INTEGER, file TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO library_sections VALUES (1,'Movies')`,
		`INSERT INTO metadata_items (id,library_section_id,metadata_type,guid,title,year)
			VALUES (10,1,1,'com.plexapp.agents.imdb://tt0107302?lang=en','Kalifornia',1993)`,
		`INSERT INTO media_items VALUES (100,10)`,
		`INSERT INTO media_parts VALUES (1000,100,'/media/Movies/Kalifornia (1993).mkv')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenDB(context.Background(), dir, 1)
	if err != nil {
		t.Fatalf("OpenDB with a metadata_type column: %v", err)
	}
	defer s.Close()
	items, err := s.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := itemByKey(items, "10")
	if !ok || m.Kind != Movie || m.Title != "Kalifornia" || m.IDs.IMDb != "tt0107302" {
		t.Fatalf("movie = %+v (ok=%v)", m, ok)
	}
	if len(m.Files) != 1 || m.Files[0] != "/media/Movies/Kalifornia (1993).mkv" {
		t.Errorf("files = %v", m.Files)
	}
}
