package metadata

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// tree creates empty files (and their directories) under a root whose name has
// a space and returns the root.
func tree(t *testing.T, files ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "My Library")
	for _, f := range files {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fileNames lists the files (not directories) of dir, like the scanner's listing.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestMovieLocal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		movie string // relative to the root
		want  Local  // relative to the root
	}{
		{"nothing", []string{"M/Movie (2020).mkv"}, "M/Movie (2020).mkv", Local{}},
		{"base names", []string{"M/Movie (2020).mkv", "M/Movie (2020)-poster.jpg", "M/Movie (2020)-fanart.png", "M/Movie (2020).nfo"}, "M/Movie (2020).mkv",
			Local{NFO: "M/Movie (2020).nfo", Poster: "M/Movie (2020)-poster.jpg", Backdrop: "M/Movie (2020)-fanart.png"}},
		{"folder names in a one-movie folder", []string{"M/a.mkv", "M/poster.jpg", "M/fanart.jpg", "M/movie.nfo"}, "M/a.mkv",
			Local{NFO: "M/movie.nfo", Poster: "M/poster.jpg", Backdrop: "M/fanart.jpg"}},
		{"base names beat folder names", []string{"M/a.mkv", "M/a-poster.png", "M/poster.jpg", "M/a-fanart.png", "M/fanart.jpg", "M/a.nfo", "M/movie.nfo"}, "M/a.mkv",
			Local{NFO: "M/a.nfo", Poster: "M/a-poster.png", Backdrop: "M/a-fanart.png"}},
		{"poster order: poster, folder, cover", []string{"M/a.mkv", "M/cover.jpg", "M/folder.jpg"}, "M/a.mkv", Local{Poster: "M/folder.jpg"}},
		{"cover alone", []string{"M/a.mkv", "M/cover.webp"}, "M/a.mkv", Local{Poster: "M/cover.webp"}},
		{"backdrop order: fanart, backdrop, background", []string{"M/a.mkv", "M/background.jpg", "M/backdrop.jpg"}, "M/a.mkv", Local{Backdrop: "M/backdrop.jpg"}},
		{"background alone", []string{"M/a.mkv", "M/background.png"}, "M/a.mkv", Local{Backdrop: "M/background.png"}},
		{"extension order jpg, jpeg, png, webp", []string{"M/a.mkv", "M/poster.webp", "M/poster.png", "M/poster.jpeg"}, "M/a.mkv", Local{Poster: "M/poster.jpeg"}},
		{"jpg first", []string{"M/a.mkv", "M/poster.png", "M/poster.jpg"}, "M/a.mkv", Local{Poster: "M/poster.jpg"}},
		{"case insensitive", []string{"M/a.mkv", "M/POSTER.JPG", "M/FanArt.PNG", "M/MOVIE.NFO"}, "M/a.mkv",
			Local{NFO: "M/MOVIE.NFO", Poster: "M/POSTER.JPG", Backdrop: "M/FanArt.PNG"}},
		{"base compared without case", []string{"M/The Movie.mkv", "M/the movie-POSTER.jpg", "M/THE MOVIE.nfo"}, "M/The Movie.mkv",
			Local{NFO: "M/THE MOVIE.nfo", Poster: "M/the movie-POSTER.jpg"}},
		{"video extension in capitals", []string{"M/a.MKV", "M/poster.jpg"}, "M/a.MKV", Local{Poster: "M/poster.jpg"}},
		{"not images", []string{"M/a.mkv", "M/poster.txt", "M/poster.jpg.bak", "M/poster", "M/fanart.gif", "M/folder.jpg.jpg"}, "M/a.mkv", Local{}},
		{"subtitles and nfo do not make a second movie", []string{"M/a.mkv", "M/a.srt", "M/a.en.srt", "M/a.nfo", "M/poster.jpg"}, "M/a.mkv",
			Local{NFO: "M/a.nfo", Poster: "M/poster.jpg"}},
		{"nested folders are not looked at", []string{"M/a.mkv", "M/Extras/poster.jpg", "M/Extras/trailer.mkv"}, "M/a.mkv", Local{}},

		// Several movies in one folder.
		{"two movies: own names only (first)", []string{"M/a.mkv", "M/b.mkv", "M/poster.jpg", "M/fanart.jpg", "M/movie.nfo", "M/a-poster.jpg", "M/a.nfo"}, "M/a.mkv",
			Local{NFO: "M/a.nfo", Poster: "M/a-poster.jpg"}},
		{"two movies: own names only (second)", []string{"M/a.mkv", "M/b.mkv", "M/poster.jpg", "M/fanart.jpg", "M/movie.nfo", "M/a-poster.jpg", "M/a.nfo"}, "M/b.mkv", Local{}},
		{"two movies of different containers", []string{"M/a.mkv", "M/b.MP4", "M/poster.jpg"}, "M/a.mkv", Local{}},
		{"three movies, one with its own art", []string{"M/a.mkv", "M/b.avi", "M/c.m2ts", "M/folder.jpg", "M/b-fanart.jpg"}, "M/b.avi", Local{Backdrop: "M/b-fanart.jpg"}},
	} {
		root := tree(t, tc.files...)
		video := filepath.Join(root, tc.movie)
		got := MovieLocal(video, fileNames(t, filepath.Dir(video)))
		if want := absLocal(root, tc.want); got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, want)
		}
	}
}

func absLocal(root string, l Local) Local {
	join := func(p string) string {
		if p == "" {
			return ""
		}
		return filepath.Join(root, p)
	}
	return Local{NFO: join(l.NFO), Poster: join(l.Poster), Backdrop: join(l.Backdrop), Thumb: join(l.Thumb)}
}

func TestMovieLocalOneMovieRuleHasAPositiveControl(t *testing.T) {
	root := tree(t, "M/a.mkv", "M/b.mkv", "M/poster.jpg", "M/movie.nfo")
	dir := filepath.Join(root, "M")
	if got := MovieLocal(filepath.Join(dir, "a.mkv"), fileNames(t, dir)); got != (Local{}) {
		t.Fatalf("two movies: %+v", got)
	}
	// The same folder after one movie leaves: the folder-level files apply.
	if err := os.Remove(filepath.Join(dir, "b.mkv")); err != nil {
		t.Fatal(err)
	}
	want := Local{NFO: filepath.Join(dir, "movie.nfo"), Poster: filepath.Join(dir, "poster.jpg")}
	if got := MovieLocal(filepath.Join(dir, "a.mkv"), fileNames(t, dir)); got != want {
		t.Errorf("one movie: %+v, want %+v", got, want)
	}
}

func TestMovieLocalNormalisation(t *testing.T) {
	// macOS lists names decomposed (NFD) while a video name may come composed.
	composed, decomposed := "Am\u00e9lie", norm.NFD.String("Am\u00e9lie")
	if composed == decomposed {
		t.Fatal("test strings are the same")
	}
	root := tree(t, "M/"+composed+".mkv", "M/"+decomposed+"-poster.jpg")
	dir := filepath.Join(root, "M")
	got := MovieLocal(filepath.Join(dir, composed+".mkv"), fileNames(t, dir))
	if got.Poster == "" {
		t.Errorf("an NFD poster was not found for an NFC video: %+v", got)
	}
	if filepath.Base(got.Poster) != decomposed+"-poster.jpg" {
		t.Errorf("poster path %q must be the name as listed on disk", got.Poster)
	}
}

func TestMovieLocalIgnoresListingOrder(t *testing.T) {
	names := []string{"a.mkv", "poster.jpg", "POSTER.JPG", "Poster.jpg"}
	want := MovieLocal("/m/a.mkv", names)
	for range 5 {
		slices.Reverse(names)
		if got := MovieLocal("/m/a.mkv", names); got != want {
			t.Errorf("order changed the answer: %+v vs %+v", got, want)
		}
	}
	if want.Poster != "/m/POSTER.JPG" { // uppercase sorts first
		t.Errorf("poster = %q", want.Poster)
	}
}

func TestEpisodeLocal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  Local
	}{
		{"nothing", []string{"S/Show S01E01.mkv"}, Local{}},
		{"nfo and thumb", []string{"S/Show S01E01.mkv", "S/Show S01E01.nfo", "S/Show S01E01-thumb.jpg"}, Local{NFO: "S/Show S01E01.nfo", Thumb: "S/Show S01E01-thumb.jpg"}},
		{"thumb named like the video", []string{"S/Show S01E01.mkv", "S/Show S01E01.JPG"}, Local{Thumb: "S/Show S01E01.JPG"}},
		{"-thumb beats the plain name", []string{"S/Show S01E01.mkv", "S/Show S01E01.jpg", "S/Show S01E01-thumb.png"}, Local{Thumb: "S/Show S01E01-thumb.png"}},
		{"other episodes' files are not ours", []string{"S/Show S01E01.mkv", "S/Show S01E02.nfo", "S/Show S01E02-thumb.jpg", "S/Show S01E02.mkv"}, Local{}},
		{"folder-level names are not episode art", []string{"S/Show S01E01.mkv", "S/poster.jpg", "S/folder.jpg", "S/tvshow.nfo", "S/movie.nfo"}, Local{}},
		{"a longer name is another episode", []string{"S/Show S01E01.mkv", "S/Show S01E01E02.nfo", "S/Show S01E010.jpg"}, Local{}},
	} {
		root := tree(t, tc.files...)
		video := filepath.Join(root, "S", "Show S01E01.mkv")
		got := EpisodeLocal(video, fileNames(t, filepath.Dir(video)))
		if want := absLocal(root, tc.want); got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, want)
		}
	}
}

func TestShowAndSeasonLocal(t *testing.T) {
	root := tree(t,
		"Show/tvshow.nfo", "Show/POSTER.png", "Show/folder.jpg", "Show/fanart.webp", "Show/backdrop.jpg",
		"Show/season01-poster.jpg", "Show/season02-poster.png", "Show/season-specials-poster.jpg", "Show/season10-poster.jpg",
		"Show/Season 01/poster.jpg", "Show/Season 01/S01E01.mkv", "Show/Season 02/folder.png", "Show/Season 03/S03E01.mkv",
	)
	show := filepath.Join(root, "Show")
	names := fileNames(t, show)

	if got, want := ShowLocal(show, names), (Local{NFO: filepath.Join(show, "tvshow.nfo"), Poster: filepath.Join(show, "POSTER.png"), Backdrop: filepath.Join(show, "fanart.webp")}); got != want {
		t.Errorf("ShowLocal = %+v, want %+v", got, want)
	}
	if got := ShowLocal(show, []string{"folder.jpg", "backdrop.jpg"}); got.Poster != filepath.Join(show, "folder.jpg") || got.Backdrop != filepath.Join(show, "backdrop.jpg") || got.NFO != "" {
		t.Errorf("ShowLocal with the second names = %+v", got)
	}
	if got := ShowLocal(show, []string{"movie.nfo", "poster.txt"}); got != (Local{}) {
		t.Errorf("ShowLocal found %+v among things that are not a show's files", got)
	}

	for season, want := range map[int]string{
		0: "season-specials-poster.jpg", 1: "season01-poster.jpg", 2: "season02-poster.png", 10: "season10-poster.jpg", 3: "", 11: "",
	} {
		got := SeasonPoster(show, names, season)
		if want != "" {
			want = filepath.Join(show, want)
		}
		if got != want {
			t.Errorf("SeasonPoster(%d) = %q, want %q", season, got, want)
		}
	}
	if got := SeasonPoster(show, []string{"season1-poster.jpg"}, 1); got != filepath.Join(show, "season1-poster.jpg") {
		t.Errorf("SeasonPoster without the leading zero = %q", got)
	}

	s1, s2, s3 := filepath.Join(show, "Season 01"), filepath.Join(show, "Season 02"), filepath.Join(show, "Season 03")
	if got := FolderPoster(s1, fileNames(t, s1)); got != filepath.Join(s1, "poster.jpg") {
		t.Errorf("FolderPoster(Season 01) = %q", got)
	}
	if got := FolderPoster(s2, fileNames(t, s2)); got != filepath.Join(s2, "folder.png") {
		t.Errorf("FolderPoster(Season 02) = %q", got)
	}
	if got := FolderPoster(s3, fileNames(t, s3)); got != "" {
		t.Errorf("FolderPoster(Season 03) = %q, want none", got)
	}
}
