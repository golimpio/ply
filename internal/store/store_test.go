package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ply/internal/probe"
)

// openTemp opens a store in a directory whose name contains a space, like the
// macOS default data dir ("Application Support").
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Application Support", "ply", "ply.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func movie(path, title string) *Item {
	return &Item{Kind: KindMovie, Title: title, Year: 2020, Path: path, Root: "/media",
		Size: 100, MTime: 1_700_000_000_000_000_000, Container: "mp4", DurationSec: 7200.5, Bitrate: 5000}
}

func episode(path, show string, season, ep int) *Item {
	return &Item{Kind: KindEpisode, Title: "Ep", Show: show, Season: season, Episode: ep, Path: path,
		Root: "/media", Size: 10, MTime: 5, Container: "mkv"}
}

var videoAudio = []probe.Stream{
	{Index: 0, Type: "video", Codec: "h264", CodecTag: "avc1", Profile: "High", Width: 1920, Height: 1080,
		PixFmt: "yuv420p", ColorTransfer: "bt709", Default: true},
	{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Language: "eng", Title: "Stereo", Default: true},
	{Index: 2, Type: "subtitle", Codec: "subrip", Language: "por", Forced: true},
}

func TestOpenInDirWithSpace(t *testing.T) {
	s, path := openTemp(t)
	if !strings.Contains(path, " ") {
		t.Fatalf("test path %q should contain a space", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created at the unescaped path: %v", err)
	}
	if n, err := s.CountItems(context.Background()); err != nil || n != 0 {
		t.Errorf("CountItems = %d, %v; want 0, nil", n, err)
	}
}

func TestMigrationsAreIdempotentAndVersioned(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	if _, err := s.UpsertItem(ctx, movie("/media/a.mp4", "A"), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var versions int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	// One row per migration file, not one more per Open.
	files, err := migrationFS.ReadDir("migrations")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v (%d files)", err, len(files))
	}
	if versions != len(files) {
		t.Errorf("schema_version rows = %d after reopen, want %d (one per migration)", versions, len(files))
	}
	if n, _ := s2.CountItems(ctx); n != 1 {
		t.Errorf("items after reopen = %d, want 1 (data must persist)", n)
	}
	for _, table := range []string{"items", "streams", "subtitles_external", "progress", "keyframes"} {
		var name string
		err := s2.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestPragmasApplied(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v; want 1", fk, err)
	}
}

func TestUpsertAndGetEntry(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	it := movie("/media/Movie (2020).mp4", "Movie")
	id, err := s.UpsertItem(ctx, it, videoAudio)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 || it.ID != id {
		t.Fatalf("id = %d, it.ID = %d", id, it.ID)
	}

	e, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "Movie" || e.Year != 2020 || e.Container != "mp4" || e.DurationSec != 7200.5 ||
		e.Bitrate != 5000 || e.MTime != it.MTime || e.Path != it.Path || e.Root != "/media" {
		t.Errorf("item round-trip mismatch: %+v", e.Item)
	}
	if e.AddedAt == 0 || e.UpdatedAt == 0 {
		t.Errorf("timestamps not set: %+v", e.Item)
	}
	if !reflect.DeepEqual(e.Streams, videoAudio) {
		t.Errorf("streams round-trip mismatch:\n got %+v\nwant %+v", e.Streams, videoAudio)
	}
}

func TestUpsertSamePathUpdatesInPlace(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	id, err := s.UpsertItem(ctx, movie("/media/a.mp4", "Old"), videoAudio)
	if err != nil {
		t.Fatal(err)
	}
	// Pretend the row was added long ago so we can tell added_at is kept.
	if _, err := s.db.Exec(`UPDATE items SET added_at = 1, updated_at = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	changed := movie("/media/a.mp4", "New")
	changed.Size = 999
	newStreams := []probe.Stream{{Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160}}
	id2, err := s.UpsertItem(ctx, changed, newStreams)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("upsert of the same path changed the id: %d -> %d", id, id2)
	}
	if n, _ := s.CountItems(ctx); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}

	e, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "New" || e.Size != 999 {
		t.Errorf("item not updated: %+v", e.Item)
	}
	if e.AddedAt != 1 {
		t.Errorf("added_at = %d, want it preserved (1)", e.AddedAt)
	}
	if e.UpdatedAt <= 1 {
		t.Errorf("updated_at = %d, want it refreshed", e.UpdatedAt)
	}
	if !reflect.DeepEqual(e.Streams, newStreams) {
		t.Errorf("streams not replaced: %+v", e.Streams)
	}
}

func TestPathStoredRaw(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	nfd := "/media/Café (2019).mkv" // decomposed é, as macOS filenames often are
	nfc := "/media/Café (2019).mkv"
	if nfd == nfc {
		t.Fatal("test strings must differ")
	}
	id, err := s.UpsertItem(ctx, movie(nfd, "Café"), nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.GetEntry(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != nfd {
		t.Errorf("path was altered: got %q, want raw %q", e.Path, nfd)
	}
	states, err := s.FileStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states[nfd]; !ok {
		t.Error("FileStates is not keyed by the raw path")
	}
	if _, ok := states[nfc]; ok {
		t.Error("NFC form of the path must not alias the NFD row")
	}
}

func TestGetEntryNotFound(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.GetEntry(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteItemsCascades(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	keep, _ := s.UpsertItem(ctx, movie("/media/keep.mp4", "Keep"), videoAudio)
	gone, _ := s.UpsertItem(ctx, movie("/media/gone.mp4", "Gone"), videoAudio)
	for _, q := range []string{
		`INSERT INTO progress (item_id, updated_at) VALUES (?, 1)`,
		`INSERT INTO keyframes (item_id, times) VALUES (?, x'00')`,
		`INSERT INTO subtitles_external (item_id, path, format) VALUES (?, 'x.srt', 'srt')`,
	} {
		if _, err := s.db.Exec(q, gone); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	count := func(table string, id int64) int {
		t.Helper()
		col := "item_id"
		if table == "items" {
			col = "id"
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Positive controls: the dependent rows exist before the delete.
	for _, table := range []string{"items", "streams", "progress", "keyframes", "subtitles_external"} {
		if count(table, gone) == 0 {
			t.Fatalf("setup: no %s rows for the item to be deleted", table)
		}
	}

	if err := s.DeleteItems(ctx, []int64{gone}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"items", "streams", "progress", "keyframes", "subtitles_external"} {
		if n := count(table, gone); n != 0 {
			t.Errorf("%s rows for deleted item = %d, want 0", table, n)
		}
	}
	if count("items", keep) != 1 || count("streams", keep) != len(videoAudio) {
		t.Error("deleting one item affected another")
	}
	if err := s.DeleteItems(ctx, nil); err != nil {
		t.Errorf("DeleteItems(nil) = %v", err)
	}
}

func TestListItemsOrderingAndFilters(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	items := []*Item{
		movie("/media/b.mp4", "banana"),
		movie("/media/a.mp4", "Apple"),
		movie("/media/c.mp4", "Cherry"),
		episode("/media/z-s2e1.mkv", "Zeta", 2, 1),
		episode("/media/z-s1e10.mkv", "Zeta", 1, 10),
		episode("/media/z-s1e2.mkv", "Zeta", 1, 2),
		episode("/media/a-s1e1.mkv", "alpha", 1, 1),
	}
	for _, it := range items {
		if _, err := s.UpsertItem(ctx, it, videoAudio[:1]); err != nil {
			t.Fatal(err)
		}
	}

	paths := func(es []Entry) []string {
		var p []string
		for _, e := range es {
			p = append(p, filepath.Base(e.Path))
		}
		return p
	}

	all, err := s.ListItems(ctx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.mp4", "b.mp4", "c.mp4", "a-s1e1.mkv", "z-s1e2.mkv", "z-s1e10.mkv", "z-s2e1.mkv"}
	if got := paths(all); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	for _, e := range all {
		if len(e.Streams) != 1 || e.Streams[0].Codec != "h264" {
			t.Errorf("%s: streams not attached: %+v", e.Path, e.Streams)
		}
	}

	movies, _ := s.ListItems(ctx, ListFilter{Kind: KindMovie})
	if got := paths(movies); !reflect.DeepEqual(got, want[:3]) {
		t.Errorf("movies = %v", got)
	}
	eps, _ := s.ListItems(ctx, ListFilter{Kind: KindEpisode})
	if got := paths(eps); !reflect.DeepEqual(got, want[3:]) {
		t.Errorf("episodes = %v", got)
	}
	zeta, _ := s.ListItems(ctx, ListFilter{Kind: KindEpisode, Show: "ZETA"})
	if got := paths(zeta); !reflect.DeepEqual(got, want[4:]) {
		t.Errorf("show filter (case-insensitive) = %v", got)
	}
	none, err := s.ListItems(ctx, ListFilter{Show: "nope"})
	if err != nil || len(none) != 0 {
		t.Errorf("unknown show = %v, %v; want empty", none, err)
	}
}

func TestShows(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if shows, err := s.Shows(ctx); err != nil || len(shows) != 0 {
		t.Fatalf("empty library: %v, %v", shows, err)
	}
	for i, it := range []*Item{
		episode("/m/1.mkv", "Beta", 1, 1),
		episode("/m/2.mkv", "Beta", 1, 2),
		episode("/m/3.mkv", "Beta", 3, 1),
		episode("/m/4.mkv", "alpha", 0, 1), // specials
		movie("/m/5.mp4", "Not a show"),
	} {
		if _, err := s.UpsertItem(ctx, it, nil); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	got, err := s.Shows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Show{
		{Name: "alpha", Seasons: []int{0}, EpisodeCount: 1},
		{Name: "Beta", Seasons: []int{1, 3}, EpisodeCount: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shows = %+v, want %+v", got, want)
	}
}

func TestFileStates(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/media/a.mp4", "A")
	id, _ := s.UpsertItem(ctx, it, nil)

	states, err := s.FileStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := FileState{ID: id, Root: "/media", Size: 100, MTime: it.MTime}
	if got := states["/media/a.mp4"]; !reflect.DeepEqual(got, want) {
		t.Errorf("state = %+v, want %+v", got, want)
	}
}

func TestDSNEscapesPath(t *testing.T) {
	got := dsn("/Users/g/Library/Application Support/ply/ply.db")
	if !strings.HasPrefix(got, "file:///Users/g/Library/Application%20Support/ply/ply.db?") {
		t.Errorf("dsn = %q", got)
	}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(1)"} {
		if !strings.Contains(got, "_pragma="+p) {
			t.Errorf("dsn %q missing pragma %s", got, p)
		}
	}
}

func TestKeyframesRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, _ := s.UpsertItem(ctx, movie("/media/kf.mkv", "KF"), videoAudio)

	// Positive control for the miss: nothing cached yet.
	if _, ok, err := s.Keyframes(ctx, id); err != nil || ok {
		t.Fatalf("Keyframes before Set = ok %v, err %v; want a clean miss", ok, err)
	}

	want := []float64{0, 2.002, 4.004, 6.5, 1234.567891}
	if err := s.SetKeyframes(ctx, id, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Keyframes(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Keyframes = ok %v, err %v", ok, err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("times[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// Replace.
	if err := s.SetKeyframes(ctx, id, []float64{0, 9}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Keyframes(ctx, id); len(got) != 2 || got[1] != 9 {
		t.Errorf("after replace: %v", got)
	}

	if err := s.SetKeyframes(ctx, 9999, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetKeyframes for a missing item = %v, want ErrNotFound", err)
	}
}

func TestUpsertInvalidatesKeyframes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	it := movie("/media/kf2.mkv", "KF2")
	id, _ := s.UpsertItem(ctx, it, videoAudio)
	if err := s.SetKeyframes(ctx, id, []float64{0, 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Keyframes(ctx, id); !ok {
		t.Fatal("setup: keyframes not cached")
	}
	// The scanner upserts an item when its file changed.
	if _, err := s.UpsertItem(ctx, it, videoAudio); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Keyframes(ctx, id); ok {
		t.Error("keyframes survived an upsert of the item")
	}
}
