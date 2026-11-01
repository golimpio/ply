package library

import (
	"testing"

	"ply/internal/store"
)

func TestParseNameMovies(t *testing.T) {
	tests := []struct {
		path  string
		title string
		year  int
	}{
		// The three year cases from the spec: the LAST plausible year wins.
		{"/m/Blade Runner 2049 (2017).mkv", "Blade Runner 2049", 2017},
		{"/m/1917 (2019).mp4", "1917", 2019},
		{"/m/2001 A Space Odyssey (1968).mkv", "2001 A Space Odyssey", 1968},

		{"/m/The Matrix (1999).mp4", "The Matrix", 1999},
		{"/m/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", "The Matrix", 1999},
		{"/m/Blade.Runner.2049.2017.2160p.UHD.BluRay.x265.HDR.mkv", "Blade Runner 2049", 2017},
		{"/m/2001.A.Space.Odyssey.1968.REMASTERED.1080p.mkv", "2001 A Space Odyssey", 1968},
		{"/m/The_Great_Escape_[1963].avi", "The Great Escape", 1963},
		{"/m/Movie Name - (2020) - 1080p.mkv", "Movie Name", 2020},
		{"/m/Movie [2021] [1080p].mkv", "Movie", 2021},
		{"/m/Movie.Title.2019.1920x1080.mkv", "Movie Title", 2019}, // 1920 is a resolution, not a year
		{"/m/Mr. Nobody (2009).mkv", "Mr Nobody", 2009},
		{"/m/Amélie (2001).mkv", "Amélie", 2001},
		{"/m/Amélie (2001).mkv", "Amélie", 2001}, // NFD input, NFC title

		// A lone number is a title; year bounds are 1900-2099.
		{"/m/1917.mp4", "1917", 0},
		{"/m/2012.mkv", "2012", 0},
		{"/m/2012 (2009).mkv", "2012", 2009},
		{"/m/Movie 1899.mkv", "Movie 1899", 0},
		{"/m/Movie 2100.mkv", "Movie 2100", 0},
		{"/m/Movie 1900.mkv", "Movie", 1900},
		{"/m/Movie 2099.mkv", "Movie", 2099},

		// No year: junk is trimmed, otherwise the name is kept.
		{"/m/Some.Movie.1080p.BluRay.x264.mkv", "Some Movie", 0},
		{"/m/Just A Title.mp4", "Just A Title", 0},
		{"/m/4K Beauty.mp4", "4K Beauty", 0},
		{"/m/Dotted.Name.Without.Year.mkv", "Dotted Name Without Year", 0},
		{"/m/Trailing Dash -.mp4", "Trailing Dash", 0},
		{"/m/UPPER.CASE.EXT.MKV", "UPPER CASE EXT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ParseName(tt.path)
			if got.Kind != store.KindMovie || got.Title != tt.title || got.Year != tt.year {
				t.Errorf("ParseName(%q) = %+v; want movie %q (%d)", tt.path, got, tt.title, tt.year)
			}
			if got.Show != "" || got.Season != 0 || got.Episode != 0 {
				t.Errorf("movie has episode fields: %+v", got)
			}
		})
	}
}

func TestParseNameEpisodes(t *testing.T) {
	tests := []struct {
		path          string
		show, title   string
		season, epNum int
	}{
		// SxxExx
		{"/tv/Show Name S01E02.mkv", "Show Name", "", 1, 2},
		{"/tv/Show Name - S01E02 - Pilot.mkv", "Show Name", "Pilot", 1, 2},
		{"/tv/Show.Name.S02E05.Episode.Title.720p.HDTV.x264-GRP.mkv", "Show Name", "Episode Title", 2, 5},
		{"/tv/show_name_s3e10_the_one.mp4", "show name", "the one", 3, 10},
		{"/tv/Show S1E5.mkv", "Show", "", 1, 5},
		{"/tv/Show S01 E02.mkv", "Show", "", 1, 2},
		{"/tv/Show.S01E102.mkv", "Show", "", 1, 102},
		{"/tv/The Show (2019) S04E08 Title [1080p].mkv", "The Show (2019)", "Title", 4, 8},
		{"/tv/Show S00E03 Special.mkv", "Show", "Special", 0, 3},
		{"/tv/Show.S01E02.1080p.WEB-DL.mkv", "Show", "", 1, 2},

		// NNxNN
		{"/tv/Show Name - 1x05 - Title.mkv", "Show Name", "Title", 1, 5},
		{"/tv/Show.Name.12x103.avi", "Show Name", "", 12, 103},
		{"/tv/Show 2x05.mkv", "Show", "", 2, 5},

		// Empty show part: parent dir, skipping Season / Specials dirs.
		{"/tv/Breaking Bad/S01E02.mkv", "Breaking Bad", "", 1, 2},
		{"/tv/Breaking Bad/Season 1/S01E02 Cat's in the Bag.mkv", "Breaking Bad", "Cat's in the Bag", 1, 2},
		{"/tv/Breaking Bad/Season 01/1x03.mkv", "Breaking Bad", "", 1, 3},
		{"/tv/Breaking.Bad/Season.2/S02E01.mkv", "Breaking Bad", "", 2, 1},
		{"/tv/The Wire/Specials/S00E01 Making Of.mkv", "The Wire", "Making Of", 0, 1},
		{"/tv/The Wire/specials/S00E02.mkv", "The Wire", "", 0, 2},
		{"/tv/Lost/S3/s03e04.mkv", "Lost", "", 3, 4},
		{"/tv/Odd Show/Season 1/Season 1/S01E01.mkv", "Odd Show", "", 1, 1},
		{"/tv/Café/Season 1/S01E01.mkv", "Café", "", 1, 1},

		// A show in the filename beats the directory.
		{"/tv/Other Dir/Real Show S01E01.mkv", "Real Show", "", 1, 1},
		// Nothing to infer the show from.
		{"/S01E01.mkv", "", "", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ParseName(tt.path)
			want := Parsed{Kind: store.KindEpisode, Title: tt.title, Show: tt.show, Season: tt.season, Episode: tt.epNum}
			if got != want {
				t.Errorf("ParseName(%q)\n got %+v\nwant %+v", tt.path, got, want)
			}
		})
	}
}

func TestParseNameNotAnEpisode(t *testing.T) {
	// Resolutions and codecs must not be mistaken for episode markers.
	for _, path := range []string{
		"/m/Movie.2019.1920x1080.mkv",
		"/m/Movie.720x480.avi",
		"/m/Movie.x264.mkv",
		"/m/Thesis01 Notes.mp4",
	} {
		if got := ParseName(path); got.Kind != store.KindMovie {
			t.Errorf("ParseName(%q) = %+v; want a movie", path, got)
		}
	}
}

func TestIsVideoFile(t *testing.T) {
	for name, want := range map[string]bool{
		"a.mp4": true, "a.M4V": true, "a.mov": true, "a.mkv": true, "a.webm": true,
		"a.avi": true, "a.ts": true, "a.m2ts": true, "A.MKV": true,
		"a.txt": false, "a.srt": false, "a": false, "mkv": false, "a.mp4.part": false, "a.jpg": false,
	} {
		if got := IsVideoFile(name); got != want {
			t.Errorf("IsVideoFile(%q) = %v, want %v", name, got, want)
		}
	}
}
