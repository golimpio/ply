package store

import (
	"context"
	"testing"
)

// Version 19 on a database of version 18: items keep their rows, ids and what refers to
// them, and the kind 'trailer' is allowed.
func TestMigration019KeepsItemsAndAllowsTrailer(t *testing.T) {
	old, path := openAtVersion(t, 18)
	for _, q := range []string{
		`INSERT INTO items (id, kind, title, path, root, size, mtime, added_at, updated_at) VALUES (7, 'movie', 'Heat', '/m/Heat.mkv', '/m', 1, 1, 1, 1)`,
		`INSERT INTO progress (item_id, position_sec, duration_sec, updated_at) VALUES (7, 60, 120, 1)`,
		`INSERT INTO streams (item_id, idx, type, codec) VALUES (7, 0, 'video', 'h264')`,
	} {
		if _, err := old.rw.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if _, err := old.rw.Exec(`INSERT INTO items (kind, path, root, size, mtime, added_at, updated_at) VALUES ('trailer', '/m/x-trailer.mp4', '/m', 1, 1, 1, 1)`); err == nil {
		t.Fatal("control: version 18 takes kind trailer")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, err := s.GetEntry(ctx, 7)
	if err != nil || e.Item.Title != "Heat" || e.Item.OwnerDir != "" || len(e.Streams) != 1 || e.Progress == nil {
		t.Fatalf("item 7 after the migration: %+v, %v", e, err)
	}
	for _, idx := range []string{"items_root", "items_browse", "items_title", "items_match", "items_fingerprint"} {
		var n int
		if err := s.ro.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d, %v", idx, n, err)
		}
	}
	if _, err := s.rw.Exec(`INSERT INTO items (kind, path, root, size, mtime, added_at, updated_at) VALUES ('trailer', '/m/x-trailer.mp4', '/m', 1, 1, 1, 1)`); err != nil {
		t.Errorf("a trailer: %v", err)
	}
	if _, err := s.rw.Exec(`INSERT INTO items (kind, path, root, size, mtime, added_at, updated_at) VALUES ('clip', '/m/c.mp4', '/m', 1, 1, 1, 1)`); err == nil {
		t.Error("a clip was taken")
	}
}

func trailerAt(path, ownerDir, title string, year int, show string) *Item {
	return &Item{Kind: KindTrailer, Title: title, Year: year, Show: show, OwnerDir: ownerDir, Path: path,
		Root: "/media", Size: 10, MTime: 5, Container: "mp4", DurationSec: 120}
}

func addTrailer(t *testing.T, s *Store, it *Item) int64 {
	t.Helper()
	id, err := s.UpsertItem(context.Background(), it, nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// linkedMovie indexes a film file linked to the title of that TMDB id (made if new).
func linkedMovie(t *testing.T, s *Store, path, title string, year int, tmdb int64) (itemID, titleID int64) {
	t.Helper()
	ctx := context.Background()
	m := movie(path, title)
	m.Year = year
	itemID, err := s.UpsertItem(ctx, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	titleID, err = s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindMovie, TMDBID: tmdb, Title: title, Year: year}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetItemMatch(ctx, itemID, Match{TitleID: titleID, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	return itemID, titleID
}

func trailerIDs(t *testing.T, s *Store, titleID int64) []int64 {
	t.Helper()
	list, err := s.TitleTrailers(context.Background(), titleID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, it := range list {
		if it.Kind != KindTrailer {
			t.Errorf("TitleTrailers gave a %s", it.Kind)
		}
		ids = append(ids, it.ID)
	}
	return ids
}

// Several versions of a film in its own folder share the trailer of that folder.
func TestTitleTrailersOwnFolder(t *testing.T) {
	s, _ := openTemp(t)
	dir := "/media/Vivre sa vie (1962)"
	_, title := linkedMovie(t, s, dir+"/Vivre sa vie (1962) - 1080p.mkv", "Vivre sa vie", 1962, 25600)
	linkedMovie(t, s, dir+"/Vivre sa vie (1962) - 4K.mkv", "Vivre sa vie", 1962, 25600)
	tr := addTrailer(t, s, trailerAt(dir+"/Vivre sa vie (1962)-trailer.mp4", dir, "Vivre sa vie", 1962, ""))
	if got := trailerIDs(t, s, title); len(got) != 1 || got[0] != tr {
		t.Errorf("trailers = %v, want [%d]", got, tr)
	}
}

// Loose films in one folder: the trailer is the one whose name says it.
func sharedFolder(t *testing.T, dir string) {
	s, _ := openTemp(t)
	_, alien := linkedMovie(t, s, dir+"/Alien (1979).mkv", "Alien", 1979, 348)
	_, heat := linkedMovie(t, s, dir+"/Heat (1995).mkv", "Heat", 1995, 949)
	tr := addTrailer(t, s, trailerAt(dir+"/Heat (1995)-trailer.mp4", dir, "Heat", 1995, ""))
	if got := trailerIDs(t, s, heat); len(got) != 1 || got[0] != tr {
		t.Errorf("Heat's trailers = %v, want [%d]", got, tr)
	}
	if got := trailerIDs(t, s, alien); len(got) != 0 {
		t.Errorf("Alien's trailers = %v, want none", got)
	}
}

func TestTitleTrailersSharedRoot(t *testing.T)      { sharedFolder(t, "/media") }
func TestTitleTrailersSharedSubfolder(t *testing.T) { sharedFolder(t, "/media/Collection") }

// A show's trailer is found through the show's match.
func TestTitleTrailersShow(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	addEpisode(t, s, "The Wire", 1, 1)
	title, err := s.UpsertTitle(ctx, &TitleInput{Title: Title{Kind: KindSeries, TMDBID: 1438, Title: "The Wire"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesMatch(ctx, "The Wire", Match{TitleID: title, State: MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	tr := addTrailer(t, s, trailerAt("/media/The Wire/The Wire-trailer.mp4", "/media/The Wire", "", 0, "The Wire"))
	if got := trailerIDs(t, s, title); len(got) != 1 || got[0] != tr {
		t.Errorf("trailers = %v, want [%d]", got, tr)
	}
}

// A trailer is not a film: no list, count or progress has it.
func TestTrailersStayOutOfLists(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	linkedMovie(t, s, "/media/Heat (1995)/Heat (1995).mkv", "Heat", 1995, 949)
	tr := addTrailer(t, s, trailerAt("/media/Heat (1995)/Heat (1995)-trailer.mp4", "/media/Heat (1995)", "Heat", 1995, ""))
	if e, err := s.GetEntry(ctx, tr); err != nil || e.Item.Kind != KindTrailer || e.Item.OwnerDir != "/media/Heat (1995)" {
		t.Fatalf("control: the trailer is there: %+v, %v", e.Item, err)
	}
	if list, err := s.ListItems(ctx, ListFilter{}); err != nil || len(list) != 1 || list[0].Item.Kind != KindMovie {
		t.Errorf("ListItems = %d items, %v; want the film alone", len(list), err)
	}
	if n, err := s.CountItems(ctx); err != nil || n != 1 {
		t.Errorf("CountItems = %d, %v; want 1", n, err)
	}
	if m, err := s.CountItemsByRoot(ctx); err != nil || m["/media"] != 1 {
		t.Errorf("CountItemsByRoot = %v, %v; want 1", m, err)
	}
	if p, err := s.SetProgress(ctx, tr, 60, 120); err != nil || p != nil {
		t.Errorf("SetProgress on a trailer = %+v, %v; want nothing stored", p, err)
	}
	if list, err := s.Continue(ctx, 10); err != nil || len(list) != 0 {
		t.Errorf("Continue = %d, %v; want none", len(list), err)
	}
}

// A film file read again as a trailer loses its match and its progress.
func TestSetParsedMakesATrailer(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := linkedMovie(t, s, "/media/Heat (1995)/Heat (1995)-trailer.mp4", "Heat (1995)-trailer", 1995, 949)
	if _, err := s.SetProgress(ctx, id, 60, 120); err != nil {
		t.Fatal(err)
	}
	if err := s.SetParsed(ctx, []ParsedRow{{ID: id, Kind: KindTrailer, Title: "Heat", Year: 1995, OwnerDir: "/media/Heat (1995)"}}); err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEntry(ctx, id)
	if err != nil || e.Item.Kind != KindTrailer || e.Item.TitleID != 0 || e.Item.MatchState != MatchDisabled ||
		e.Item.OwnerDir != "/media/Heat (1995)" || e.Progress != nil {
		t.Errorf("after SetParsed: %+v (progress %+v), %v", e.Item, e.Progress, err)
	}
}
