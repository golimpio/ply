package metadata

import "testing"

func TestParseIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Hints
	}{
		{"jellyfin tmdb", "The Matrix (1999) [tmdbid-603]", Hints{TMDB: 603}},
		{"jellyfin tmdb equals", "The Matrix [tmdbid=603]", Hints{TMDB: 603}},
		{"plex tmdb", "The Matrix (1999) {tmdb-603}", Hints{TMDB: 603}},
		{"upper case tag", "The Matrix [TMDBID-603]", Hints{TMDB: 603}},
		{"jellyfin imdb", "The Matrix (1999) [imdbid-tt0133093]", Hints{IMDb: "tt0133093"}},
		{"plex imdb", "The Matrix (1999) {imdb-tt0133093}", Hints{IMDb: "tt0133093"}},
		{"bare imdb in a file name", "The.Matrix.1999.tt0133093.1080p.mkv", Hints{IMDb: "tt0133093"}},
		{"bare imdb with underscores", "The_Matrix_tt0133093_1080p.mkv", Hints{IMDb: "tt0133093"}},
		{"imdb, 8 digits", "Movie tt12345678", Hints{IMDb: "tt12345678"}},
		{"imdb upper case", "Movie TT0133093", Hints{IMDb: "tt0133093"}},
		{"tvdb", "Breaking Bad [tvdbid-81189]", Hints{TVDB: 81189}},
		{"tvdb plex", "Breaking Bad {tvdb-81189}", Hints{TVDB: 81189}},
		{"several", "Show (2008) [tvdbid-81189] [imdbid-tt0903747] {tmdb-1396}", Hints{TMDB: 1396, IMDb: "tt0903747", TVDB: 81189}},
		{"id with spaces in the tag", "Movie [ tmdbid - 603 ]", Hints{TMDB: 603}},

		// Not ids.
		{"plain name", "The Matrix (1999)", Hints{}},
		{"imdb with 6 digits", "Movie tt123456", Hints{}},
		{"imdb with 9 digits", "Movie tt123456789", Hints{}},
		{"imdb inside a word", "Scotty0133093 tt0133093x", Hints{}},
		{"tmdb without a number", "Movie [tmdbid-]", Hints{}},
		{"tmdb with letters", "Movie [tmdbid-60a3]", Hints{}},
		{"bare number", "1917 (2019)", Hints{}},
		{"tmdb word without brackets", "tmdbid-603", Hints{}},
		{"empty", "", Hints{}},
	} {
		if got := ParseIDs(tc.in); got != tc.want {
			t.Errorf("%s: ParseIDs(%q) = %+v, want %+v", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestStripIDs(t *testing.T) {
	for in, want := range map[string]string{
		"The Matrix (1999) [tmdbid-603]":                   "The Matrix (1999)",
		"The Matrix (1999) {imdb-tt0133093} 1080p":         "The Matrix (1999) 1080p",
		"The.Matrix.tt0133093.mkv":                         "The.Matrix..mkv",
		"[tmdbid-1] A [tvdbid-2] B {tmdb-3}":               "A B",
		"Plain Name":                                       "Plain Name",
		"Doc  (2000)   [tmdbid-5]":                         "Doc (2000)",
		"tt0133093 and tt0111161 both":                     "and both",
		"1917 (2019)":                                      "1917 (2019)",
		"Show [imdbid-tt0903747] [tvdbid-81189] Season 01": "Show Season 01",
	} {
		if got := StripIDs(in); got != want {
			t.Errorf("StripIDs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathHints(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		parents int
		want    Hints
	}{
		{"in the file name", "/m/Movies/The Matrix (1999) [tmdbid-603].mkv", 1, Hints{TMDB: 603}},
		{"in the folder name", "/m/Movies/The Matrix (1999) [tmdbid-603]/matrix.mkv", 1, Hints{TMDB: 603}},
		{"the file beats the folder", "/m/Movies/Matrix [tmdbid-1]/Matrix [tmdbid-2].mkv", 1, Hints{TMDB: 2}},
		{"file and folder fill each other", "/m/Matrix {imdb-tt0133093}/Matrix [tmdbid-603].mkv", 1, Hints{TMDB: 603, IMDb: "tt0133093"}},
		{"nearest folder first", "/m/Show [tvdbid-1]/Season [tvdbid-2]/ep.mkv", 2, Hints{TVDB: 2}},
		{"show folder two levels up", "/m/Show [tvdbid-81189]/Season 01/S01E01.mkv", 2, Hints{TVDB: 81189}},
		{"too far up", "/m/Show [tvdbid-81189]/Season 01/S01E01.mkv", 1, Hints{}},
		{"no parents", "/m/Matrix [tmdbid-603]/matrix.mkv", 0, Hints{}},
		{"the root is not a name", "/x [tmdbid-9].mkv", 5, Hints{TMDB: 9}},
		{"bare imdb in the file", "/m/Movies/Matrix.tt0133093.mkv", 1, Hints{IMDb: "tt0133093"}},
		{"nothing", "/m/Movies/Matrix (1999).mkv", 3, Hints{}},
	} {
		if got := PathHints(tc.path, tc.parents); got != tc.want {
			t.Errorf("%s: PathHints(%q, %d) = %+v, want %+v", tc.name, tc.path, tc.parents, got, tc.want)
		}
	}
}
