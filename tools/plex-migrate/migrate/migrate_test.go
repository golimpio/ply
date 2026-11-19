package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ply/tools/plex-migrate/plex"
)

// fakeSource is an in-memory plex.Source.
type fakeSource struct {
	items []plex.Item
	art   map[string][]byte // ratingKey + "/" + slot → bytes
}

func (f *fakeSource) Libraries(context.Context) ([]plex.Library, error) { return nil, nil }
func (f *fakeSource) Items(context.Context) ([]plex.Item, error)        { return f.items, nil }
func (f *fakeSource) Art(_ context.Context, it plex.Item, slot string) ([]byte, error) {
	if b, ok := f.art[it.RatingKey+"/"+slot]; ok {
		return b, nil
	}
	return nil, plex.ErrNoArt
}
func (f *fakeSource) Close() error { return nil }

func fixturePlyDB(t *testing.T, items []PlyItem) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, kind TEXT, path TEXT, show TEXT,
		season INTEGER, episode INTEGER, title_id INTEGER, duration_sec REAL)`); err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		title := sql.NullInt64{Int64: it.TitleID, Valid: it.TitleID != 0}
		if _, err := db.Exec(`INSERT INTO items VALUES (?,?,?,?,?,?,?,?)`,
			it.ID, it.Kind, it.Path, it.Show, it.Season, it.Episode, title, it.DurationSec); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadPlyItems(t *testing.T) {
	want := []PlyItem{
		{ID: 1, Kind: "movie", Path: "/m/A.mkv", DurationSec: 7200},
		{ID: 2, Kind: "episode", Path: "/t/S/Season 01/S01E01.mkv", Show: "S", Season: 1, Episode: 1, TitleID: 9, DurationSec: 2400},
	}
	dir := fixturePlyDB(t, want)
	got, err := ReadPlyItems(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("items = %+v", got)
	}
	if got[0].ID != 1 || got[0].Path != "/m/A.mkv" || got[0].TitleID != 0 {
		t.Errorf("movie = %+v", got[0])
	}
	if got[1].Show != "S" || got[1].Season != 1 || got[1].Episode != 1 || got[1].TitleID != 9 {
		t.Errorf("episode = %+v", got[1])
	}
}

func TestPathMap(t *testing.T) {
	pm, err := ParsePathMap([]string{"/volume1=/mnt", "/volume1/Movies=/mnt/Movies"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/volume1/Movies/A.mkv": "/mnt/Movies/A.mkv", // longest prefix wins
		"/volume1/TV/B.mkv":     "/mnt/TV/B.mkv",
		"/other/C.mkv":          "/other/C.mkv",
	}
	for in, want := range cases {
		if got := pm.Map(in); got != want {
			t.Errorf("Map(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParsePathMap([]string{"bad"}); err == nil {
		t.Error("a spec without '=' should fail")
	}
}

// libraryFixture builds a Plex movie + show/season/episode and matching ply items
// under a real temp media root, so sidecar detection works on disk.
func libraryFixture(t *testing.T) (root string, src *fakeSource, plyItems []PlyItem) {
	t.Helper()
	root = t.TempDir()
	moviePath := filepath.Join(root, "Movies", "The Matrix (1999).mkv")
	epPath := filepath.Join(root, "TV", "Breaking Bad", "Season 01", "Breaking Bad S01E01.mkv")
	for _, p := range []string{moviePath, epPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src = &fakeSource{
		items: []plex.Item{
			{RatingKey: "10", Kind: plex.Movie, Title: "The Matrix", Year: 1999, IDs: plex.IDs{TMDB: 603},
				Files: []string{moviePath}, PosterURL: "/thumb", ArtURL: "/art", Watch: plex.Watch{OffsetMS: 60000, ViewCount: 1}},
			{RatingKey: "20", Kind: plex.Show, Title: "Breaking Bad", IDs: plex.IDs{TMDB: 1396}, PosterURL: "/thumb"},
			{RatingKey: "21", Kind: plex.Season, Show: "Breaking Bad", Season: 1, PosterURL: "/thumb"},
			{RatingKey: "22", Kind: plex.Episode, Show: "Breaking Bad", Season: 1, Episode: 1,
				Files: []string{epPath}, PosterURL: "/thumb", Watch: plex.Watch{ViewCount: 1}},
		},
		art: map[string][]byte{
			"10/poster": {0xFF, 0xD8, 'm'}, "10/art": {0xFF, 0xD8, 'b'},
			"20/poster": {0xFF, 0xD8, 's'}, "21/poster": {0xFF, 0xD8, 'n'}, "22/poster": {0xFF, 0xD8, 'e'},
		},
	}
	plyItems = []PlyItem{
		{ID: 1, Kind: "movie", Path: moviePath, DurationSec: 7200},
		{ID: 2, Kind: "episode", Path: epPath, Show: "Breaking Bad", Season: 1, Episode: 1, DurationSec: 2400},
	}
	return root, src, plyItems
}

func opsOfType(p *Plan, t OpType) []Op {
	var out []Op
	for _, op := range p.Ops {
		if op.Type == t {
			out = append(out, op)
		}
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	root, src, plyItems := libraryFixture(t)
	p, err := BuildPlan(context.Background(), src, plyItems, PathMap{}, false)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Stats
	if s.PlexMovies != 1 || s.JoinedMovies != 1 || s.PlexShows != 1 || s.PlexSeasons != 1 || s.PlexEpisodes != 1 || s.JoinedEpisodes != 1 {
		t.Errorf("stats = %+v", s)
	}
	if len(p.Unmatched) != 0 {
		t.Errorf("unmatched = %v", p.Unmatched)
	}
	if mm := opsOfType(p, OpMatchMovie); len(mm) != 1 || mm[0].PlyID != 1 || mm[0].TMDB != 603 {
		t.Errorf("match-movie = %+v", mm)
	}
	if ms := opsOfType(p, OpMatchShow); len(ms) != 1 || ms[0].PlyShow != "Breaking Bad" || ms[0].TMDB != 1396 {
		t.Errorf("match-show = %+v", ms)
	}
	showFolder := filepath.Join(root, "TV", "Breaking Bad")
	if po := opsOfType(p, OpPoster); len(po) != 2 { // movie + show
		t.Errorf("poster ops = %+v", po)
	} else {
		if po[0].Target != filepath.Join(root, "Movies", "The Matrix (1999)-poster.jpg") {
			t.Errorf("movie poster target = %q", po[0].Target)
		}
	}
	if sp := opsOfType(p, OpSeasonPoster); len(sp) != 1 || sp[0].Target != filepath.Join(showFolder, "season01-poster.jpg") {
		t.Errorf("season poster = %+v", sp)
	}
	if es := opsOfType(p, OpEpisodeStill); len(es) != 1 || es[0].Target != filepath.Join(root, "TV", "Breaking Bad", "Season 01", "Breaking Bad S01E01-thumb.jpg") {
		t.Errorf("episode still = %+v", es)
	}
	if pr := opsOfType(p, OpProgress); len(pr) != 1 || pr[0].PlyID != 1 || pr[0].PositionSec != 60 {
		t.Errorf("progress = %+v", pr)
	}
	if w := opsOfType(p, OpWatched); len(w) != 2 { // movie + episode
		t.Errorf("watched = %+v", w)
	}
	// The show poster lands in the show folder, not the season folder.
	for _, op := range opsOfType(p, OpPoster) {
		if op.Plex.RatingKey == "20" && op.Target != filepath.Join(showFolder, "poster.jpg") {
			t.Errorf("show poster target = %q", op.Target)
		}
	}
}

func TestBuildPlanSkipsExistingSidecar(t *testing.T) {
	root, src, plyItems := libraryFixture(t)
	// An existing movie poster: the plan must skip it (positive control: it exists).
	existing := filepath.Join(root, "Movies", "The Matrix (1999)-poster.jpg")
	if err := os.WriteFile(existing, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(context.Background(), src, plyItems, PathMap{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range opsOfType(p, OpPoster) {
		if op.Target == existing {
			if !op.Skip {
				t.Error("an existing poster should be skipped")
			}
			return
		}
	}
	t.Fatal("no poster op for the existing file")
}

func TestBuildPlanUnmatched(t *testing.T) {
	_, src, _ := libraryFixture(t)
	// No ply items at all: nothing joins.
	p, err := BuildPlan(context.Background(), src, nil, PathMap{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats.JoinedMovies != 0 || p.Stats.JoinedEpisodes != 0 {
		t.Errorf("nothing should join: %+v", p.Stats)
	}
	if len(p.Unmatched) == 0 {
		t.Error("unmatched files should be reported")
	}
}

type recordedCall struct {
	Method string
	Path   string
	Body   map[string]any
}

func fakePly(t *testing.T) (*httptest.Server, *[]recordedCall, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, recordedCall{r.Method, r.URL.Path, body})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &mu
}

func TestApply(t *testing.T) {
	root, src, plyItems := libraryFixture(t)
	srv, calls, mu := fakePly(t)
	p, err := BuildPlan(context.Background(), src, plyItems, PathMap{}, false)
	if err != nil {
		t.Fatal(err)
	}
	client := NewPlyClient(srv.URL, "tok")
	if err := Apply(context.Background(), p, src, client, false, nil); err != nil {
		t.Fatal(err)
	}
	// The movie poster was written with the Plex bytes.
	b, err := os.ReadFile(filepath.Join(root, "Movies", "The Matrix (1999)-poster.jpg"))
	if err != nil {
		t.Fatalf("poster not written: %v", err)
	}
	if len(b) < 3 || b[0] != 0xFF || b[2] != 'm' {
		t.Errorf("poster bytes = %v", b)
	}
	// The show and season posters landed in the show folder.
	if _, err := os.Stat(filepath.Join(root, "TV", "Breaking Bad", "poster.jpg")); err != nil {
		t.Errorf("show poster: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "TV", "Breaking Bad", "season01-poster.jpg")); err != nil {
		t.Errorf("season poster: %v", err)
	}
	// The API saw the match and watch calls.
	mu.Lock()
	defer mu.Unlock()
	paths := map[string]bool{}
	for _, c := range *calls {
		paths[c.Method+" "+c.Path] = true
	}
	for _, want := range []string{"PUT /api/items/1/match", "PUT /api/shows/Breaking Bad/match", "PUT /api/items/1/progress", "PUT /api/items/1/watched", "PUT /api/items/2/watched"} {
		if !paths[want] {
			t.Errorf("missing API call %q; got %v", want, paths)
		}
	}
}

func TestApplyIdempotentArt(t *testing.T) {
	root, src, plyItems := libraryFixture(t)
	srv, _, _ := fakePly(t)
	p, _ := BuildPlan(context.Background(), src, plyItems, PathMap{}, false)
	client := NewPlyClient(srv.URL, "tok")
	if err := Apply(context.Background(), p, src, client, false, nil); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "Movies", "The Matrix (1999)-poster.jpg")
	// Change the file, then re-apply without force: it must be left alone.
	if err := os.WriteFile(target, []byte("curated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), p, src, client, false, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "curated" {
		t.Errorf("re-apply overwrote a curated sidecar: %q", b)
	}
	// With force it is overwritten.
	if err := Apply(context.Background(), p, src, client, true, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) == "curated" {
		t.Error("force should overwrite the sidecar")
	}
}
