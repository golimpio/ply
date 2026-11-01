package library

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"ply/internal/store"
)

// FuzzParseName feeds arbitrary paths to the name parser. Whatever the file is
// called, the result must be a well-formed movie or episode.
func FuzzParseName(f *testing.F) {
	for _, s := range []string{
		"/m/Blade Runner 2049 (2017).mkv",
		"/m/1917 (2019).mp4",
		"/m/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv",
		"/m/Movie.Title.2019.1920x1080.mkv",
		"/m/Movie 1850.mkv", // outside 1900-2099: part of the title
		"/m/Movie 2150 (2150).mkv",
		"/m/Movie 1900 1899.mkv",
		"/m/Ame\u0301lie (2001).mkv", // NFD input
		"/tv/Show.Name.S02E05.Episode.Title.720p.HDTV.x264-GRP.mkv",
		"/tv/Show.S01E102.mkv",
		"/tv/Show 99x999.mkv",
		"/tv/Show S99E999 Title.mkv",
		"/tv/Show S123E1234.mkv",
		"/tv/Café/Season 1/S01E01.mkv",
		"/tv/Cafe\u0301/Season 1/S01E01.mkv", // NFD directory
		"/S01E01.mkv",
		"/tv/Breaking Bad/Specials/1x03.mkv",
		"/m/\xff\xfe invalid utf8 (2020).mkv",
		"/m/.mkv", "", "/", ".", "S01E01", "-", "([", "/m/ - .mkv",
		"/m/\u1100\u1161 (1999).mkv", // Hangul jamo that compose
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		got := ParseName(path)
		switch got.Kind {
		case store.KindMovie:
			if got.Year != 0 && (got.Year < 1900 || got.Year > 2099) {
				t.Errorf("movie year %d out of range: %+v", got.Year, got)
			}
			if got.Show != "" || got.Season != 0 || got.Episode != 0 {
				t.Errorf("movie with episode fields: %+v", got)
			}
		case store.KindEpisode:
			// \d{1,2} and \d{1,3} in the patterns.
			if got.Season < 0 || got.Season > 99 || got.Episode < 0 || got.Episode > 999 {
				t.Errorf("season/episode out of range: %+v", got)
			}
			if got.Year != 0 {
				t.Errorf("episode with a year: %+v", got)
			}
		default:
			t.Fatalf("kind %q for %q", got.Kind, path)
		}

		if !utf8.ValidString(path) {
			return // titles of undecodable names are whatever bytes survive
		}
		for _, s := range []string{got.Title, got.Show} {
			if !utf8.ValidString(s) {
				t.Errorf("ParseName(%q) produced invalid UTF-8 %q", path, s)
			}
			if !norm.NFC.IsNormalString(s) {
				t.Errorf("ParseName(%q) produced a title that is not NFC: %q (NFC: %q)", path, s, norm.NFC.String(s))
			}
			if strings.ContainsAny(s, "._") || s != strings.Join(strings.Fields(s), " ") || strings.Trim(s, " -([") != s {
				t.Errorf("ParseName(%q) produced an uncleaned name %q", path, s)
			}
		}
	})
}

var fuzzLang = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,4})?$`)

// FuzzSidecars checks, for arbitrary video and file names, that sidecars never
// panics and that everything it returns really is "<base>[.<lang>][.forced].<ext>".
func FuzzSidecars(f *testing.F) {
	for _, s := range [][3]string{
		{"/media/Movie (2020).mkv", "Movie (2020).en.srt", "Movie (2020).srt"},
		{"/media/Movie (2020).mkv", "MOVIE (2020).EN.FORCED.SRT", "Movie (2020).pt-BR.ass"},
		{"/media/Movie (2020).mkv", "Movie (2020).en.fr.srt", "Movie (2020)2.srt"},
		{"/media/Movie (2020).mkv", "Movie (2020).srt", "Movie (2020).en.srt"}, // given out of order
		{"/media/x.mkv", "x.en.forced.sdh.cc.srt", "x.forced.sdh.cc.full.srt"}, // four parts
		{"/media/x.mkv", "x.en.sdh.cc.srt", "x.sdh.forced.srt"},                // three parts
		{"/media/Movie (2020).mkv", "Movie (2020).a.b.c.d.srt", "Movie.srt"},
		{"/media/a.v.mkv", "a.vtt", "a.v.vtt"},   // the video's base runs into the subtitle's extension
		{"/media/a.sr.mkv", "a.srt", "a.sr.srt"}, // ditto
		{"/media/a.srt.mkv", "a.srt", "a.srt.srt"},
		{"/media/x.mkv", ".srt", "x."},
		{"/media/x.mkv", "x..srt", "x.en..srt"},
		{"/media/x.mkv", "x.en_US.srt", "x.zh-Hant.vtt"},
		{"/media/x.mkv", "x.\xff.srt", "\xffx.srt"},
		{"/media/é.mkv", "É.en.srt", "e\u0301.en.srt"},
		{"", "", ""},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, video, a, b string) {
		names := []string{a, b}
		got := sidecars(video, names)

		dir := filepath.Dir(video)
		base := strings.TrimSuffix(filepath.Base(video), filepath.Ext(video))
		var paths []string
		for _, s := range got {
			paths = append(paths, s.Path)
			var name string
			for _, n := range names {
				if filepath.Join(dir, n) == s.Path {
					name = n
				}
			}
			if name == "" {
				t.Fatalf("sidecars(%q, %q) returned %q, which is not one of the names", video, names, s.Path)
			}
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			if want := map[string]string{".srt": "srt", ".vtt": "vtt", ".ass": "ass"}[strings.ToLower(ext)]; want == "" || s.Format != want {
				t.Errorf("%q: format %q for extension %q", name, s.Format, ext)
			}
			if len(stem) < len(base) || !strings.EqualFold(stem[:len(base)], base) {
				t.Errorf("%q is not a sidecar of base %q", name, base)
				continue
			}
			rest := stem[len(base):]
			if rest != "" && rest[0] != '.' {
				t.Errorf("%q is not a sidecar of base %q: %q follows the base", name, base, rest)
			}
			if s.Language != "" && !fuzzLang.MatchString(s.Language) {
				t.Errorf("%q: language %q", name, s.Language)
			}
			if rest != "" && len(strings.Split(rest[1:], ".")) > 3 {
				t.Errorf("%q has too many parts to be a sidecar", name)
			}
			if s.Forced != strings.Contains(strings.ToLower("."+rest+"."), ".forced.") {
				t.Errorf("%q: forced = %v", name, s.Forced)
			}
		}
		if !slices.IsSorted(paths) {
			t.Errorf("sidecars not ordered by path: %q", paths)
		}
	})
}
