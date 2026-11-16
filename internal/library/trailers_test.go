package library

import (
	"path/filepath"
	"testing"

	"ply/internal/store"
)

func TestTrailerOf(t *testing.T) {
	m, tv := Root{Path: "/m", Kind: KindMovies}, Root{Path: "/tv", Kind: KindShows}
	vivre := "/m/Vivre sa vie (1962)"
	film := func(dir, title string, year int) Parsed {
		return Parsed{Kind: store.KindTrailer, Title: title, Year: year, OwnerDir: dir}
	}
	show := func(name string) Parsed {
		return Parsed{Kind: store.KindTrailer, Show: name, OwnerDir: "/tv/" + name}
	}
	for _, c := range []struct {
		root Root
		path string
		want Parsed
		ok   bool
	}{
		{m, vivre + "/Vivre sa vie (1962)-trailer.mp4", film(vivre, "Vivre sa vie", 1962), true},
		{m, vivre + "/Trailers/Bande-annonce.mkv", film(vivre, "Vivre sa vie", 1962), true},
		{m, vivre + "/trailer/x.mp4", film(vivre, "Vivre sa vie", 1962), true},
		{m, "/m/Heat (1995)-trailer.mp4", film("/m", "Heat", 1995), true},
		{m, "/m/Heat (1995).trailer.mkv", film("/m", "Heat", 1995), true},
		{m, "/m/Heat (1995) - trailer.mkv", film("/m", "Heat", 1995), true},
		{m, "/m/Trailers/x.mp4", Parsed{Kind: store.KindTrailer}, true}, // nobody's
		{tv, "/tv/The Wire/The Wire-trailer.mp4", show("The Wire"), true},
		{tv, "/tv/The Wire/Trailers/s1.mp4", show("The Wire"), true},
		{tv, "/tv/The Wire/Season 1/Trailers/t.mp4", show("The Wire"), true},
		{tv, "/tv/Trailer Park Boys/Season 1/Trailer Park Boys S01E01.mkv", Parsed{}, false},
		{m, "/m/The Trailer (2020).mkv", Parsed{}, false},
		{m, "/m/Trailer (2020)/Trailer (2020).mkv", Parsed{}, false},
	} {
		got, ok := trailerOf(c.root, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("trailerOf(%s) = %+v, %v; want %+v, %v", c.path, got, ok, c.want, c.ok)
		}
		if p := ParseNameIn(c.root, c.path); c.ok && p != c.want {
			t.Errorf("ParseNameIn(%s) = %+v; want %+v", c.path, p, c.want)
		}
	}
}

func TestIsPlexExtra(t *testing.T) {
	m, tv := Root{Path: "/m", Kind: KindMovies}, Root{Path: "/tv", Kind: KindShows}
	for _, p := range []string{"Featurettes/a.mkv", "Behind The Scenes/a.mkv", "Deleted Scenes/a.mkv", "Interviews/a.mkv",
		"Scenes/a.mkv", "Shorts/a.mkv", "Other/a.mkv", "x-featurette.mkv", "x-behindthescenes.mkv", "x-deleted.mkv",
		"x-interview.mkv", "x-scene.mkv", "x-short.mkv", "x-other.mkv"} {
		if !isPlexExtra(m, "/m/Film (2000)/"+p) {
			t.Errorf("%s is not an extra", p)
		}
	}
	for _, p := range []string{"/m/Film (2000)/Film (2000).mkv", "/m/Film (2000)/Film (2000)-trailer.mp4",
		"/m/Film (2000)/Trailers/t.mp4", "/m/Short Cuts (1993).mkv", "/m/Other People (2016)/Other People (2016).mkv"} {
		if isPlexExtra(m, p) {
			t.Errorf("%s is an extra", p)
		}
	}
	if isPlexExtra(tv, "/tv/Show/Featurettes/a.mkv") {
		t.Error("a shows folder's extras are its episodes 0")
	}
}

func TestShowExtrasStayEpisodeZero(t *testing.T) {
	p := ParseNameIn(Root{Path: "/tv", Kind: KindShows}, "/tv/Show/Deleted Scenes/a.mkv")
	if p.Kind != store.KindEpisode || p.Episode != 0 || p.NeedsReview || p.Show != "Show" {
		t.Errorf("a deleted scene: %+v", p)
	}
}

func TestShowDir(t *testing.T) {
	for path, want := range map[string]string{
		"/tv/The Wire/Season 1/x.mkv": "/tv/The Wire",
		"/tv/The Wire/x.mkv":          "/tv/The Wire",
		"/tv/Season 1/x.mkv":          "/tv", // the root is the show's folder
		"/tv/x S01E01.mkv":            "/tv",
	} {
		if got := ShowDir("/tv", path); got != want {
			t.Errorf("ShowDir(%s) = %s, want %s", path, got, want)
		}
	}
}

func kindOf(e *env, path string) (kind, owner string) {
	e.t.Helper()
	if err := e.raw().QueryRow(`SELECT kind, owner_dir FROM items WHERE path = ?`, path).Scan(&kind, &owner); err != nil {
		e.t.Fatalf("%s: %v", path, err)
	}
	return kind, owner
}

// A film's folder with two versions, its trailer and a featurette: two films, one trailer.
func TestScanIndexesTrailersAndSkipsExtras(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "Movies")
	dir := filepath.Join(root, "Film (2000)")
	for _, f := range []string{"Film (2000) - 1080p.mkv", "Film (2000) - 4K.mkv", "Film (2000)-trailer.mp4", "Featurettes/f.mkv"} {
		write(t, filepath.Join(dir, f), f)
	}
	e.setRoots(movies(root))
	if n := e.countRaw(`SELECT COUNT(*) FROM items WHERE kind = 'movie'`); n != 2 {
		t.Errorf("films: %d, want 2", n)
	}
	if kind, owner := kindOf(e, filepath.Join(dir, "Film (2000)-trailer.mp4")); kind != store.KindTrailer || owner != dir {
		t.Errorf("the trailer: %s, %s", kind, owner)
	}
	if n := e.countRaw(`SELECT COUNT(*) FROM items WHERE path = ?`, filepath.Join(dir, "Featurettes/f.mkv")); n != 0 {
		t.Errorf("the featurette is indexed")
	}
}

// A trailer an earlier version indexed as a film is a trailer once the folder is read again.
func TestRereadTurnsOldRowsIntoTrailers(t *testing.T) {
	e := newEnv(t)
	root := filepath.Join(t.TempDir(), "Movies")
	path := filepath.Join(root, "Film (2000)", "Film (2000)-trailer.mp4")
	write(t, filepath.Join(root, "Film (2000)", "Film (2000).mkv"), "f")
	write(t, path, "t")
	e.setRoots(movies(root))
	e.execRaw(`UPDATE items SET kind = 'movie', owner_dir = '', title = 'Film (2000)-trailer' WHERE path = ?`, path)
	var rerr error
	e.s.Reread([]string{root}, func(_ string, err error) { rerr = err })
	e.s.Wait()
	if kind, owner := kindOf(e, path); rerr != nil || kind != store.KindTrailer || owner != filepath.Dir(path) {
		t.Errorf("after the reread: %s, %s, %v", kind, owner, rerr)
	}
}
