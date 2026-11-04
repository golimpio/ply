package library

import (
	"fmt"
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

func TestParseNameInMovies(t *testing.T) {
	root := Root{Path: "/m", Kind: KindMovies}
	tests := []struct {
		path  string
		title string
		year  int
	}{
		// The file name wins when it says something.
		{"/m/The Matrix (1999).mkv", "The Matrix", 1999},
		{"/m/Sci-Fi/Alien (1979).mkv", "Alien", 1979},
		{"/m/The Matrix (1999)/The Matrix (1999).mkv", "The Matrix", 1999},
		{"/m/Wrong Folder (1950)/Right.Movie.2001.1080p.mkv", "Right Movie", 2001},

		// A file name that is no title: the nearest folder with a name of its own.
		{"/m/The Matrix (1999)/movie.mkv", "The Matrix", 1999},
		{"/m/The.Matrix.1999.1080p.BluRay/video.mkv", "The Matrix", 1999},
		{"/m/Blade Runner 2049 (2017)/VIDEO_TS/video_ts.mkv", "Blade Runner 2049", 2017},
		{"/m/Dune (2021)/CD1/movie.mkv", "Dune", 2021},
		{"/m/Dune (2021)/Disc 2/film.mkv", "Dune", 2021},
		{"/m/Mr. Nobody (2009)/Video.mkv", "Mr Nobody", 2009},
		{"/m/Amélie (2001)/movie.mkv", "Amélie", 2001},
		{"/m/Amélie (2001)/movie.mkv", "Amélie", 2001}, // NFD folder, NFC title
		{"/m/Heat (1995)/BDMV/STREAM/00001.m2ts", "Heat", 1995},
		{"/m/2012 (2009)/main.mkv", "2012", 2009},
		{"/m/Film Noir/Out of the Past (1947)/title1.mkv", "Out of the Past", 1947},
		{"/m/1080p.mkv", "1080p", 0}, // nothing better anywhere: the file name stays

		// Nothing to fall back to: the file name, as it is.
		{"/m/movie.mkv", "movie", 0},
		{"/m/Movie/movie.mkv", "movie", 0}, // the folder says nothing either

		// Never an episode, whatever the name looks like.
		{"/m/Show.Name.S01E02.mkv", "Show Name S01E02", 0},
		{"/m/Show Name 1x05.mkv", "Show Name 1x05", 0},
		{"/m/Show/Season 1/Show.S01E02.2019.mkv", "Show S01E02", 2019},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ParseNameIn(root, tt.path)
			want := Parsed{Kind: store.KindMovie, Title: tt.title, Year: tt.year}
			if got != want {
				t.Errorf("ParseNameIn(movies, %q)\n got %+v\nwant %+v", tt.path, got, want)
			}
		})
	}
	// The same file is an episode to ParseName: the folder type is what decides.
	if got := ParseName("/m/Show.Name.S01E02.mkv"); got.Kind != store.KindEpisode {
		t.Fatalf("control: ParseName = %+v, want an episode", got)
	}
}

func TestParseNameInShows(t *testing.T) {
	root := Root{Path: "/tv", Kind: KindShows}
	ep := func(show, title string, season, episode int) Parsed {
		return Parsed{Kind: store.KindEpisode, Show: show, Title: title, Season: season, Episode: episode}
	}
	review := func(show, title string, season int) Parsed {
		return Parsed{Kind: store.KindEpisode, Show: show, Title: title, Season: season, NeedsReview: true}
	}
	tests := []struct {
		path string
		want Parsed
	}{
		// SxxEyy and 1x02 in the name: the show is still the first folder.
		{"/tv/Breaking Bad/Season 1/Breaking Bad S01E02 Cat's in the Bag.mkv", ep("Breaking Bad", "Cat's in the Bag", 1, 2)},
		{"/tv/Breaking Bad/Breaking.Bad.S02E05.Title.720p.HDTV.x264-GRP.mkv", ep("Breaking Bad", "Title", 2, 5)},
		{"/tv/Show/Show - 1x05 - Title.mkv", ep("Show", "Title", 1, 5)},
		{"/tv/Show/1x02 Title.mkv", ep("Show", "Title", 1, 2)},
		{"/tv/Show/Show S01 E02.mkv", ep("Show", "", 1, 2)},
		{"/tv/The Show (2019)/S04E08 Title [1080p].mkv", ep("The Show (2019)", "Title", 4, 8)},

		// The season in the name beats the Season folder.
		{"/tv/Show/Season 2/S01E05.mkv", ep("Show", "", 1, 5)},
		{"/tv/Show/Specials/S01E03.mkv", ep("Show", "", 1, 3)},

		// E02, Episode 2, Ep2: the season comes from the Season folder.
		{"/tv/Lost/S3/E04.mkv", ep("Lost", "", 3, 4)},
		{"/tv/Lost/Season 3/Lost E04 The Brig.mkv", ep("Lost", "The Brig", 3, 4)},
		{"/tv/Lost/Season 3/Episode 7 - The Brig.mkv", ep("Lost", "The Brig", 3, 7)},
		{"/tv/Lost/Season 3/Lost - Ep12.mkv", ep("Lost", "", 3, 12)},
		{"/tv/Lost/Season 3/episode.12.mkv", ep("Lost", "", 3, 12)},

		// A leading number.
		{"/tv/Show/Season 2/02 - Title.mkv", ep("Show", "Title", 2, 2)},
		{"/tv/Show/Season 2/2. Title.mkv", ep("Show", "Title", 2, 2)},
		{"/tv/Show/Season 2/12.mkv", ep("Show", "", 2, 12)},
		{"/tv/Show/07 Pilot.mkv", ep("Show", "Pilot", 1, 7)}, // no Season folder: season 1

		// Specials is season 0.
		{"/tv/The Wire/Specials/01 - Making Of.mkv", ep("The Wire", "Making Of", 0, 1)},
		{"/tv/The Wire/specials/E02.mkv", ep("The Wire", "", 0, 2)},
		{"/tv/The Wire/Specials/S00E03.mkv", ep("The Wire", "", 0, 3)},
		{"/tv/The Wire/Season 0/E04.mkv", ep("The Wire", "", 0, 4)},

		// No episode number: episode 0, for review. The season still comes from the folder.
		{"/tv/Breaking Bad/Season 2/Pilot.mkv", review("Breaking Bad", "Pilot", 2)},
		{"/tv/The Wire/Specials/Behind the Scenes.mkv", review("The Wire", "Behind the Scenes", 0)},
		// Extras are not episodes with a number to find: still episode 0, but nobody has to look.
		{"/tv/Show/Season 1/Extras/Featurette.mkv", ep("Show", "Featurette", 1, 0)}, // the nearest Season folder up
		{"/tv/Show/Featurettes/Making Of.mkv", ep("Show", "Making Of", 1, 0)},
		{"/tv/Show/Season 2/Behind The Scenes/Interview.mkv", ep("Show", "Interview", 2, 0)},
		{"/tv/Show/extra/Bloopers.mkv", ep("Show", "Bloopers", 1, 0)},
		{"/tv/Show/Extras and More/Bloopers.mkv", review("Show", "Bloopers", 1)},   // only the folders by that name
		{"/tv/Show/The Matrix (1999).mkv", review("Show", "The Matrix (1999)", 1)}, // a movie in a shows folder
		{"/tv/Show/2012 (2009).mkv", review("Show", "2012 (2009)", 1)},             // a year is not an episode
		{"/tv/Show/1080p.mkv", review("Show", "", 1)},                              // nor a resolution
		{"/tv/Show/3rd Rock.mkv", review("Show", "3rd Rock", 1)},

		// S00E00 is a number.
		{"/tv/Show/S00E00.mkv", ep("Show", "", 0, 0)},

		// A file straight in the root: the show comes from the name, if it has one.
		{"/tv/Show Name S01E02.mkv", ep("Show Name", "", 1, 2)},
		{"/tv/Show.Name.S01E02.Title.mkv", ep("Show Name", "Title", 1, 2)},
		{"/tv/S01E02.mkv", ep("tv", "", 1, 2)}, // ...and if it does not, the root's own name does
		{"/tv/Pilot.mkv", review("tv", "Pilot", 1)},
		// The root pointed at a Season folder: it is not the show.
		{"/tv/Season 1/Show S01E01.mkv", ep("Show", "", 1, 1)},
		// ...and nothing else names the show: the root's own name does.
		{"/tv/Season 1/E01.mkv", ep("tv", "", 1, 1)},
		{"/tv/Specials/02 - Making Of.mkv", ep("tv", "Making Of", 0, 2)},

		// The folder is the show, even when the file name names another.
		{"/tv/Other Dir/Real Show S01E01.mkv", ep("Other Dir", "", 1, 1)},

		// Names are cleaned like ParseName's.
		{"/tv/Mr.Robot/Season.1/Mr.Robot.S01E01.mkv", ep("Mr Robot", "", 1, 1)},
		{"/tv/Café/Season 1/S01E01.mkv", ep("Café", "", 1, 1)},
		{"/tv/Café/Season 1/S01E01.mkv", ep("Café", "", 1, 1)}, // NFD folder, NFC show
		{"/tv/Show/UPPER.CASE.S01E02.MKV", ep("Show", "", 1, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := ParseNameIn(root, tt.path); got != tt.want {
				t.Errorf("ParseNameIn(shows, %q)\n got %+v\nwant %+v", tt.path, got, tt.want)
			}
		})
	}
}

// A path outside its root (a symlink, a stale root) still parses as an episode.
func TestParseNameInShowsOutsideTheRoot(t *testing.T) {
	root := Root{Path: "/elsewhere", Kind: KindShows}
	got := ParseNameIn(root, "/tv/Breaking Bad/Season 1/02 - Cat.mkv")
	want := Parsed{Kind: store.KindEpisode, Show: "Breaking Bad", Title: "Cat", Season: 1, Episode: 2}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	root = Root{Path: "/elsewhere", Kind: KindMovies}
	if got := ParseNameIn(root, "/m/Heat (1995)/movie.mkv"); got.Title != "Heat" || got.Year != 1995 {
		t.Errorf("movie outside the root = %+v", got)
	}
	// A root that is a prefix of a sibling's name does not contain it.
	if dirs, ok := dirsBelow("/tv", "/tv2/Show/x.mkv"); ok {
		t.Errorf("dirsBelow(/tv, /tv2/Show/x.mkv) = %v, true; /tv2 is not under /tv", dirs)
	}
}

// A root that is the show's own folder (Season folders, or the files, straight
// below it) names the show, unless the file name does; a root that has no name to
// give does not.
func TestParseNameInShowsRootIsTheShowFolder(t *testing.T) {
	for _, tt := range []struct {
		root, path string
		want       string
	}{
		{"/shows/Breaking.Bad", "/shows/Breaking.Bad/Season 2/E03.mkv", "Breaking Bad"},
		{"/shows/Breaking Bad/", "/shows/Breaking Bad/Season 2/E03.mkv", "Breaking Bad"},
		{"/shows/Breaking Bad", "/shows/Breaking Bad/Season 2/Breaking Bad E03.mkv", "Breaking Bad"},
		{"/shows/Breaking Bad", "/shows/Breaking Bad/Season 2/Better Call Saul E03.mkv", "Better Call Saul"}, // the name wins
		{"/shows/Breaking Bad", "/shows/Breaking Bad/Season 2/Extras/E03.mkv", "Breaking Bad"},
		{"/", "/Season 1/E01.mkv", ""}, // no name to give
		{"/", "/E01.mkv", ""},          // nor here
		{"/shows/Breaking Bad", "/shows/Breaking Bad/E03.mkv", "Breaking Bad"}, // a file straight in the root: the root's name
	} {
		got := ParseNameIn(Root{Path: tt.root, Kind: KindShows}, tt.path)
		if got.Show != tt.want || got.Kind != store.KindEpisode {
			t.Errorf("ParseNameIn(%s, %s) = %+v, want show %q", tt.root, tt.path, got, tt.want)
		}
	}
}

// A root with no type (a -media folder not guessed yet) parses exactly as
// ParseName always has; so does a type nobody knows.
func TestParseNameInWithoutAKindIsParseName(t *testing.T) {
	paths := []string{
		"/m/The Matrix (1999).mkv", "/m/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", "/m/1917.mp4",
		"/m/Heat (1995)/movie.mkv", "/m/movie.mkv", "/m/Amélie (2001).mkv",
		"/tv/Show Name - S01E02 - Pilot.mkv", "/tv/Breaking Bad/Season 1/S01E02 Cat's in the Bag.mkv",
		"/tv/The Wire/Specials/S00E01 Making Of.mkv", "/tv/Show/Season 2/02 - Title.mkv", "/tv/Show/Pilot.mkv",
		"/tv/Other Dir/Real Show S01E01.mkv", "/S01E01.mkv", "/tv/Show 2x05.mkv", "/m/Movie.2019.1920x1080.mkv",
	}
	for _, kind := range []string{"", "mixed"} {
		root := Root{Path: "/", Kind: kind}
		for _, p := range paths {
			if got, want := ParseNameIn(root, p), ParseName(p); got != want {
				t.Errorf("ParseNameIn(kind %q, %q)\n got %+v\nwant %+v", kind, p, got, want)
			}
		}
	}
	// Control: with a type the same paths do change.
	if ParseNameIn(Root{Path: "/tv", Kind: KindShows}, "/tv/Show/Pilot.mkv") == ParseName("/tv/Show/Pilot.mkv") {
		t.Error("a shows root parsed like no root at all")
	}
}

func TestGuessKind(t *testing.T) {
	episodes := func(n int) []string {
		var p []string
		for i := 0; i < n; i++ {
			p = append(p, fmt.Sprintf("/x/Show S01E%02d.mkv", i+1))
		}
		return p
	}
	movies := func(n int) []string {
		var p []string
		for i := 0; i < n; i++ {
			p = append(p, fmt.Sprintf("/x/Movie %d (%d).mkv", i, 2000+i))
		}
		return p
	}
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{"no files", nil, KindMovies},
		{"all episodes", episodes(5), KindShows},
		{"all movies", movies(5), KindMovies},
		{"one episode", episodes(1), KindShows},
		{"more than half", append(episodes(3), movies(2)...), KindShows},
		{"exactly half", append(episodes(2), movies(2)...), KindMovies},
		{"less than half", append(episodes(2), movies(3)...), KindMovies},
		{"one of one movie", movies(1), KindMovies},
		{"NxNN counts", []string{"/x/Show 1x01.mkv", "/x/Show 1x02.mkv", "/x/Film (1999).mkv"}, KindShows},
		{"resolutions are not episodes", []string{"/x/A.1920x1080.mkv", "/x/B.1280x720.mkv"}, KindMovies},
		{"episodes by folder only do not count", []string{"/x/Show/Season 1/Pilot.mkv", "/x/Show/Season 1/Second.mkv"}, KindMovies},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GuessKind(tt.paths); got != tt.want {
				t.Errorf("GuessKind = %q, want %q", got, tt.want)
			}
		})
	}
}
