package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/unicode"
)

var regexpIMDb = regexp.MustCompile(`^tt\d{7,8}$`)

// bytesOf joins strings and single bytes.
func bytesOf(parts ...any) []byte {
	var b []byte
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			b = append(b, v...)
		case int:
			b = append(b, byte(v))
		}
	}
	return b
}

const matrixNFO = `<?xml version="1.0" encoding="utf-8" standalone="yes"?>
<movie>
  <title>The Matrix</title>
  <originaltitle>The Matrix</originaltitle>
  <sorttitle>Matrix, The</sorttitle>
  <year>1999</year>
  <premiered>1999-03-31</premiered>
  <outline>A hacker learns the truth.</outline>
  <plot>A computer hacker learns from mysterious rebels
about the true nature of his reality.</plot>
  <tagline>Welcome to the Real World.</tagline>
  <runtime>136</runtime>
  <mpaa>Rated R</mpaa>
  <ratings>
    <rating name="imdb" max="10" default="false"><value>8.7</value><votes>2,000,000</votes></rating>
    <rating name="themoviedb" max="10" default="true"><value>8.2</value><votes>25000</votes></rating>
  </ratings>
  <uniqueid type="imdb" default="false">tt0133093</uniqueid>
  <uniqueid type="tmdb" default="true">603</uniqueid>
  <genre>Action</genre>
  <genre>Science Fiction</genre>
  <studio>Warner Bros.</studio>
  <studio>Village Roadshow</studio>
  <director>Lana Wachowski</director>
  <director>Lilly Wachowski</director>
  <credits>Lana Wachowski</credits>
  <credits>Lilly Wachowski</credits>
  <set>
    <name>The Matrix Collection</name>
    <overview>Wake up.</overview>
  </set>
  <actor>
    <name>Keanu Reeves</name>
    <role>Neo</role>
    <order>0</order>
    <thumb>https://image.tmdb.org/t/p/original/keanu.jpg</thumb>
  </actor>
  <actor>
    <name>Laurence Fishburne</name>
    <role>Morpheus</role>
    <order>1</order>
  </actor>
  <fileinfo><streamdetails><video><codec>h264</codec></video></streamdetails></fileinfo>
</movie>`

func TestParseNFOFullMovie(t *testing.T) {
	got := ParseNFO([]byte(matrixNFO))
	want := NFO{
		Root: "movie", Title: "The Matrix", OriginalTitle: "The Matrix", SortTitle: "Matrix, The",
		Year: 1999, Premiered: "1999-03-31", Outline: "A hacker learns the truth.",
		Plot:    "A computer hacker learns from mysterious rebels\nabout the true nature of his reality.",
		Tagline: "Welcome to the Real World.", RuntimeMin: 136, MPAA: "Rated R",
		Rating: 8.2, Votes: 25000, // the one marked default
		Genres:    []string{"Action", "Science Fiction"},
		Studios:   []string{"Warner Bros.", "Village Roadshow"},
		Directors: []string{"Lana Wachowski", "Lilly Wachowski"},
		Writers:   []string{"Lana Wachowski", "Lilly Wachowski"},
		Set:       "The Matrix Collection",
		Actors: []NFOActor{
			{Name: "Keanu Reeves", Role: "Neo", Order: 0, Thumb: "https://image.tmdb.org/t/p/original/keanu.jpg"},
			{Name: "Laurence Fishburne", Role: "Morpheus", Order: 1},
		},
		IDs: Hints{TMDB: 603, IMDb: "tt0133093"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseNFO:\n got %+v\nwant %+v", got, want)
	}
	if !got.HasData() {
		t.Error("HasData = false")
	}
}

func TestParseNFOTable(t *testing.T) {
	utf16 := func(s string) []byte {
		out, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		in   []byte
		want NFO
	}{
		{"minimal", []byte(`<movie><title>Foo</title></movie>`), NFO{Root: "movie", Title: "Foo"}},
		{"uppercase tags", []byte(`<MOVIE><TITLE>Foo</TITLE><Year>2001</Year></MOVIE>`), NFO{Root: "movie", Title: "Foo", Year: 2001}},
		{"empty", []byte(``), NFO{}},
		{"whitespace", []byte("  \n\t "), NFO{}},
		{"not an nfo at all", []byte(`hello world, no ids here`), NFO{}},
		{"title only, trimmed", []byte("<movie>\n<title>\n  Foo Bar \n</title>\n</movie>"), NFO{Root: "movie", Title: "Foo Bar"}},

		// Ids.
		{"uniqueid tmdb", []byte(`<movie><uniqueid type="tmdb">603</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{TMDB: 603}}},
		{"uniqueid imdb", []byte(`<movie><uniqueid type="imdb">tt0133093</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{IMDb: "tt0133093"}}},
		{"uniqueid tvdb", []byte(`<tvshow><uniqueid type="tvdb">81189</uniqueid></tvshow>`), NFO{Root: "tvshow", IDs: Hints{TVDB: 81189}}},
		{"uniqueid type in capitals", []byte(`<movie><uniqueid type="TMDB">603</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{TMDB: 603}}},
		{"uniqueid themoviedb", []byte(`<movie><uniqueid type="themoviedb">603</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{TMDB: 603}}},
		{"uniqueid without a type, imdb shaped", []byte(`<movie><uniqueid>tt0133093</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{IMDb: "tt0133093"}}},
		{"uniqueid without a type, a number", []byte(`<movie><uniqueid>603</uniqueid></movie>`), NFO{Root: "movie"}},
		{"uniqueid of an unknown type", []byte(`<movie><uniqueid type="anidb">603</uniqueid></movie>`), NFO{Root: "movie"}},
		{"uniqueid not a number", []byte(`<movie><uniqueid type="tmdb">six</uniqueid></movie>`), NFO{Root: "movie"}},
		{"uniqueid negative", []byte(`<movie><uniqueid type="tmdb">-5</uniqueid></movie>`), NFO{Root: "movie"}},
		{"tmdb type with an imdb value", []byte(`<movie><uniqueid type="tmdb">tt0133093</uniqueid></movie>`), NFO{Root: "movie"}},
		{"tmdbid element", []byte(`<movie><tmdbid>603</tmdbid></movie>`), NFO{Root: "movie", IDs: Hints{TMDB: 603}}},
		{"imdbid element", []byte(`<movie><imdbid>tt0133093</imdbid></movie>`), NFO{Root: "movie", IDs: Hints{IMDb: "tt0133093"}}},
		{"imdbid in capitals", []byte(`<movie><imdbid> TT0133093 </imdbid></movie>`), NFO{Root: "movie", IDs: Hints{IMDb: "tt0133093"}}},
		{"tvdbid element", []byte(`<tvshow><tvdbid>81189</tvdbid></tvshow>`), NFO{Root: "tvshow", IDs: Hints{TVDB: 81189}}},
		{"id element, imdb", []byte(`<movie><id>tt0133093</id></movie>`), NFO{Root: "movie", IDs: Hints{IMDb: "tt0133093"}}},
		{"id element in a show is tvdb", []byte(`<tvshow><id>81189</id></tvshow>`), NFO{Root: "tvshow", IDs: Hints{TVDB: 81189}}},
		{"id element, a number in a movie, is ignored", []byte(`<movie><id>603</id></movie>`), NFO{Root: "movie"}},
		{"first uniqueid wins", []byte(`<movie><uniqueid type="tmdb">603</uniqueid><uniqueid type="tmdb">604</uniqueid></movie>`), NFO{Root: "movie", IDs: Hints{TMDB: 603}}},
		{"all three", []byte(`<tvshow><uniqueid type="tvdb">81189</uniqueid><uniqueid type="imdb">tt0903747</uniqueid><uniqueid type="tmdb">1396</uniqueid></tvshow>`),
			NFO{Root: "tvshow", IDs: Hints{TMDB: 1396, IMDb: "tt0903747", TVDB: 81189}}},
		{"id with 6 digits is not imdb", []byte(`<movie><id>tt123456</id></movie>`), NFO{Root: "movie"}},
		{"id in nested elements is not the movie's", []byte(`<movie><actor><name>A</name><uniqueid type="tmdb">5</uniqueid></actor></movie>`),
			NFO{Root: "movie", Actors: []NFOActor{{Name: "A"}}}},

		// URL-only and non-XML.
		{"tmdb url", []byte("https://www.themoviedb.org/movie/603\n"), NFO{IDs: Hints{TMDB: 603}}},
		{"tmdb url with a slug", []byte("https://www.themoviedb.org/movie/603-the-matrix?language=en-US"), NFO{IDs: Hints{TMDB: 603}}},
		{"tmdb tv url", []byte("https://www.themoviedb.org/tv/1396-breaking-bad"), NFO{IDs: Hints{TMDB: 1396}}},
		{"imdb url", []byte("http://www.imdb.com/title/tt0133093/\r\n"), NFO{IDs: Hints{IMDb: "tt0133093"}}},
		{"imdb url without www", []byte("https://imdb.com/title/tt0133093"), NFO{IDs: Hints{IMDb: "tt0133093"}}},
		{"imdb mobile url", []byte("https://m.imdb.com/title/tt0133093/?ref_=x"), NFO{IDs: Hints{IMDb: "tt0133093"}}},
		{"thetvdb url", []byte("https://thetvdb.com/?tab=series&id=81189"), NFO{IDs: Hints{TVDB: 81189}}},
		{"thetvdb url with seriesid", []byte("https://www.thetvdb.com/index.php?tab=series&seriesid=81189&lid=7"), NFO{IDs: Hints{TVDB: 81189}}},
		{"bare imdb id", []byte("tt0133093"), NFO{IDs: Hints{IMDb: "tt0133093"}}},
		{"two urls", []byte("https://www.imdb.com/title/tt0133093/\nhttps://www.themoviedb.org/movie/603\n"), NFO{IDs: Hints{TMDB: 603, IMDb: "tt0133093"}}},
		{"url inside text", []byte("see https://www.themoviedb.org/movie/603 for details"), NFO{IDs: Hints{TMDB: 603}}},
		{"url with a byte order mark", append([]byte{0xEF, 0xBB, 0xBF}, "https://www.themoviedb.org/movie/603"...), NFO{IDs: Hints{TMDB: 603}}},
		{"url in utf-16", utf16("https://www.themoviedb.org/movie/603"), NFO{IDs: Hints{TMDB: 603}}},
		{"a themoviedb person url is not a title", []byte("https://www.themoviedb.org/person/6384"), NFO{}},
		{"xml then a url", []byte("<movie><title>Foo</title></movie>\nhttps://www.themoviedb.org/movie/603\n"),
			NFO{Root: "movie", Title: "Foo", IDs: Hints{TMDB: 603}}},
		{"url then xml", []byte("https://www.imdb.com/title/tt0133093/\n<movie><title>Foo</title></movie>"),
			NFO{Root: "movie", Title: "Foo", IDs: Hints{IMDb: "tt0133093"}}},
		{"elements beat urls", []byte("<movie><uniqueid type=\"tmdb\">1</uniqueid></movie>\nhttps://www.themoviedb.org/movie/603\n"),
			NFO{Root: "movie", IDs: Hints{TMDB: 1}}},
		// A plot, a thumb or any other element that mentions a title is not that title's id.
		{"a url in the plot is not the id", []byte("<movie><plot>Like https://www.themoviedb.org/movie/603 and https://www.imdb.com/title/tt0133093</plot></movie>"),
			NFO{Root: "movie", Plot: "Like https://www.themoviedb.org/movie/603 and https://www.imdb.com/title/tt0133093"}},
		{"a url in a thumb is not the id", []byte("<movie><title>Foo</title><thumb>https://www.themoviedb.org/movie/603</thumb></movie>"),
			NFO{Root: "movie", Title: "Foo"}},
		{"a url in an unterminated plot is not the id", []byte("<movie><title>Foo</title><plot>see themoviedb.org/movie/603"), NFO{Root: "movie", Title: "Foo"}},
		{"the url after the xml is, not the plot's", []byte("<movie><plot>https://www.themoviedb.org/movie/1</plot></movie>\nhttps://www.themoviedb.org/movie/2\n"),
			NFO{Root: "movie", Plot: "https://www.themoviedb.org/movie/1", IDs: Hints{TMDB: 2}}},
		{"the url before the xml is, not the plot's", []byte("https://www.imdb.com/title/tt0133093/\n<movie><plot>https://www.imdb.com/title/tt0111161/</plot></movie>"),
			NFO{Root: "movie", Plot: "https://www.imdb.com/title/tt0111161/", IDs: Hints{IMDb: "tt0133093"}}},
		{"a url in a latin1 plot is not the id", bytesOf(`<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Am`, 0xE9, `lie</title><plot>`, 0xE9, 0xE9, 0xE9, ` https://www.themoviedb.org/movie/603</plot></movie>`),
			NFO{Root: "movie", Title: "Amélie", Plot: "ééé https://www.themoviedb.org/movie/603"}},
		{"a latin1 file with a url after it", bytesOf(`<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Am`, 0xE9, `lie</title></movie>`, "\nhttps://www.themoviedb.org/movie/603\n"),
			NFO{Root: "movie", Title: "Amélie", IDs: Hints{TMDB: 603}}},
		{"a bare imdb id inside xml text is not trusted", []byte("<movie><plot>like tt0133093</plot></movie>"), NFO{Root: "movie", Plot: "like tt0133093"}},

		// Tolerance.
		{"unknown elements", []byte(`<movie><foo><bar>1</bar></foo><title>Foo</title><thumb aspect="poster">x</thumb></movie>`), NFO{Root: "movie", Title: "Foo"}},
		{"truncated", []byte(`<movie><title>Foo</title><plot>unterminated`), NFO{Root: "movie", Title: "Foo"}},
		{"truncated mid-tag", []byte(`<movie><title>Foo</title><pl`), NFO{Root: "movie", Title: "Foo"}},
		{"missing end tag nests what follows", []byte(`<movie><title>Foo<year>2001</year></movie>`), NFO{Root: "movie", Title: "Foo"}},
		{"raw ampersand", []byte(`<movie><title>Tom & Jerry</title></movie>`), NFO{Root: "movie", Title: "Tom & Jerry"}},
		{"escaped ampersand", []byte(`<movie><title>Tom &amp; Jerry</title></movie>`), NFO{Root: "movie", Title: "Tom & Jerry"}},
		{"html entity", []byte(`<movie><title>Caf&eacute;</title></movie>`), NFO{Root: "movie", Title: "Café"}},
		{"cdata plot", []byte(`<movie><plot><![CDATA[<b>Bold</b> & text]]></plot></movie>`), NFO{Root: "movie", Plot: "<b>Bold</b> & text"}},
		{"comment", []byte(`<movie><!-- x --><title>Foo</title></movie>`), NFO{Root: "movie", Title: "Foo"}},
		{"doctype", []byte(`<!DOCTYPE movie><movie><title>Foo</title></movie>`), NFO{Root: "movie", Title: "Foo"}},
		{"garbage after the root", []byte(`<movie><title>Foo</title></movie>}}}<<<`), NFO{Root: "movie", Title: "Foo"}},
		{"second root is ignored", []byte(`<episodedetails><title>One</title></episodedetails><episodedetails><title>Two</title></episodedetails>`),
			NFO{Root: "episodedetails", Title: "One"}},
		{"first title wins", []byte(`<movie><title>One</title><title>Two</title></movie>`), NFO{Root: "movie", Title: "One"}},
		{"bom", append([]byte{0xEF, 0xBB, 0xBF}, `<movie><title>Foo</title></movie>`...), NFO{Root: "movie", Title: "Foo"}},
		{"latin1 declared", bytesOf(`<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Am`, 0xE9, `lie</title></movie>`),
			NFO{Root: "movie", Title: "Amélie"}},
		{"windows-1252 declared", bytesOf(`<?xml version="1.0" encoding="windows-1252"?><movie><title>`, 0x93, "Q", 0x94, `</title></movie>`),
			NFO{Root: "movie", Title: "\u201cQ\u201d"}},
		{"utf-16 with bom", utf16(`<?xml version="1.0" encoding="UTF-16"?><movie><title>Amélie</title></movie>`), NFO{Root: "movie", Title: "Amélie"}},
		{"invalid utf-8 stops the parse and keeps what came before", bytesOf(`<movie><title>Foo</title><plot>a`, 0xff, 0xfe, `</plot>`),
			NFO{Root: "movie", Title: "Foo"}},
		{"non-ascii", []byte(`<movie><title>千と千尋の神隠し</title><year>2001</year></movie>`), NFO{Root: "movie", Title: "千と千尋の神隠し", Year: 2001}},

		// Values.
		{"year from premiered", []byte(`<movie><premiered>1999-03-31</premiered></movie>`), NFO{Root: "movie", Year: 1999, Premiered: "1999-03-31"}},
		{"year from releasedate", []byte(`<movie><releasedate>2001-05-02</releasedate></movie>`), NFO{Root: "movie", Year: 2001, Premiered: "2001-05-02"}},
		{"year element beats premiered", []byte(`<movie><year>2000</year><premiered>1999-03-31</premiered></movie>`), NFO{Root: "movie", Year: 2000, Premiered: "1999-03-31"}},
		{"year with text", []byte(`<movie><year>(1999)</year></movie>`), NFO{Root: "movie", Year: 1999}},
		{"year implausible", []byte(`<movie><year>1700</year></movie>`), NFO{Root: "movie"}},
		{"year not a number", []byte(`<movie><year>unknown</year></movie>`), NFO{Root: "movie"}},
		{"premiered not a date", []byte(`<movie><premiered>March 1999</premiered></movie>`), NFO{Root: "movie", Year: 0}},
		{"runtime with unit", []byte(`<movie><runtime>136 min</runtime></movie>`), NFO{Root: "movie", RuntimeMin: 136}},
		{"runtime absurd", []byte(`<movie><runtime>99999999</runtime></movie>`), NFO{Root: "movie"}},
		{"rating plain", []byte(`<movie><rating>8.7</rating><votes>1,234</votes></movie>`), NFO{Root: "movie", Rating: 8.7, Votes: 1234}},
		{"rating out of range", []byte(`<movie><rating>87</rating></movie>`), NFO{Root: "movie"}},
		{"rating text", []byte(`<movie><rating>great</rating></movie>`), NFO{Root: "movie"}},
		{"ratings first when none is default", []byte(`<movie><ratings><rating name="a" max="10"><value>7.5</value><votes>10</votes></rating><rating name="b" max="10"><value>9</value></rating></ratings></movie>`),
			NFO{Root: "movie", Rating: 7.5, Votes: 10}},
		{"ratings scaled by max", []byte(`<movie><ratings><rating name="a" max="5" default="true"><value>4</value></rating></ratings></movie>`), NFO{Root: "movie", Rating: 8}},
		{"genre split and deduped", []byte(`<movie><genre>Action / Adventure</genre><genre>action</genre><genre> </genre><genre>Drama</genre></movie>`),
			NFO{Root: "movie", Genres: []string{"Action", "Adventure", "Drama"}}},
		{"writer element", []byte(`<movie><writer>A</writer><credits>B</credits><credits>a</credits></movie>`), NFO{Root: "movie", Writers: []string{"A", "B"}}},
		{"set as text", []byte(`<movie><set>Old Style Collection</set></movie>`), NFO{Root: "movie", Set: "Old Style Collection"}},
		{"set with name", []byte(`<movie><set><name>New Style</name></set></movie>`), NFO{Root: "movie", Set: "New Style"}},
		{"actors", []byte(`<movie><actor><name>A</name><role>R</role><order>3</order><thumb>t.jpg</thumb></actor><actor><role>nameless</role></actor><actor><name>B</name><order>x</order></actor></movie>`),
			NFO{Root: "movie", Actors: []NFOActor{{Name: "A", Role: "R", Order: 3, Thumb: "t.jpg"}, {Name: "B"}}}},

		// Shows and episodes.
		{"tvshow", []byte(`<tvshow><title>Breaking Bad</title><premiered>2008-01-20</premiered><studio>AMC</studio><status>Ended</status><uniqueid type="tvdb" default="true">81189</uniqueid></tvshow>`),
			NFO{Root: "tvshow", Title: "Breaking Bad", Premiered: "2008-01-20", Year: 2008, Studios: []string{"AMC"}, IDs: Hints{TVDB: 81189}}},
		{"episode", []byte(`<episodedetails><title>Pilot</title><season>1</season><episode>1</episode><aired>2008-01-20</aired><plot>p</plot><runtime>58</runtime></episodedetails>`),
			NFO{Root: "episodedetails", Title: "Pilot", Season: 1, Episode: 1, Premiered: "2008-01-20", Year: 2008, Plot: "p", RuntimeMin: 58}},
		{"negative season", []byte(`<episodedetails><season>-1</season><episode>-4</episode></episodedetails>`), NFO{Root: "episodedetails"}},
	} {
		got := ParseNFO(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
		checkNFO(t, got)
	}
}

func TestParseNFOHasData(t *testing.T) {
	if (NFO{Root: "movie"}).HasData() {
		t.Error("an NFO with nothing in it has data")
	}
	for _, in := range []string{"<movie><title>x</title></movie>", "https://imdb.com/title/tt0133093", "<movie><year>2000</year></movie>",
		"<movie><genre>x</genre></movie>", "<movie><plot>x</plot></movie>", "<movie><actor><name>x</name></actor></movie>"} {
		if !ParseNFO([]byte(in)).HasData() {
			t.Errorf("HasData(%q) = false", in)
		}
	}
}

func TestParseNFOLimits(t *testing.T) {
	// Lists are capped and nothing blows up on deep nesting or a huge file.
	var b strings.Builder
	b.WriteString("<movie>")
	for i := 0; i < 3*maxList; i++ {
		b.WriteString("<genre>g" + strings.Repeat("x", i%5) + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)) + "</genre>")
		b.WriteString("<actor><name>actor " + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)) + "</name></actor>")
	}
	b.WriteString("</movie>")
	n := ParseNFO([]byte(b.String()))
	if len(n.Genres) != maxList || len(n.Actors) != maxList {
		t.Errorf("genres %d, actors %d; want both capped at %d", len(n.Genres), len(n.Actors), maxList)
	}

	deep := strings.Repeat("<a>", 5000) + "<title>x</title>" + strings.Repeat("</a>", 5000)
	if n := ParseNFO([]byte("<movie><title>Foo</title>" + deep + "</movie>")); n.Title != "Foo" {
		t.Errorf("deeply nested file: %+v", n)
	}

	huge := "<movie><title>Foo</title><plot>" + strings.Repeat("x", 3*MaxNFOBytes) + "</plot></movie>\nhttps://www.themoviedb.org/movie/603"
	n = ParseNFO([]byte(huge))
	if n.Title != "Foo" || len(n.Plot) > maxText || n.IDs.TMDB != 0 {
		t.Errorf("huge file: title %q, plot %d bytes, ids %+v (the tail past the cap is not read)", n.Title, len(n.Plot), n.IDs)
	}
}

func TestParseNFOKeepsAtMostMaxTextOfAnElement(t *testing.T) {
	// The decoder hands over a closed 300 KiB plot in one piece.
	plot := strings.Repeat("x", 300<<10)
	n := ParseNFO([]byte("<movie><title>Foo</title><plot>" + plot + "</plot><year>1999</year></movie>"))
	if len(n.Plot) != maxText {
		t.Errorf("plot of %d bytes, want it cut at %d", len(n.Plot), maxText)
	}
	if n.Title != "Foo" || n.Year != 1999 {
		t.Errorf("what is around the plot: %+v", n)
	}
	// Many pieces (entities and CDATA break the text up) are capped too.
	pieces := strings.Repeat("a&amp;<![CDATA[b]]>", 40<<10)
	if n := ParseNFO([]byte("<movie><plot>" + pieces + "</plot></movie>")); len(n.Plot) > maxText+8 || len(n.Plot) < maxText-8 {
		t.Errorf("plot in pieces = %d bytes, want about %d", len(n.Plot), maxText)
	}
}

func TestParseNFODepthIsCapped(t *testing.T) {
	nest := func(depth int) string {
		return "<movie><title>Before</title>" + strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth) + "<year>1999</year></movie>"
	}
	// Deep enough to be a problem: parsing stops there and keeps what it had.
	if n := ParseNFO([]byte(nest(maxDepth + 10))); n.Title != "Before" || n.Year != 0 {
		t.Errorf("nested past the cap: %+v; want the title and no year", n)
	}
	// Just inside the cap: all of it.
	if n := ParseNFO([]byte(nest(maxDepth - 2))); n.Title != "Before" || n.Year != 1999 {
		t.Errorf("nested within the cap: %+v", n)
	}
}

func TestReadNFO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Movies")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Movie (1999).nfo")
	if err := os.WriteFile(path, []byte(matrixNFO), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := ReadNFO(path)
	if err != nil || n.Title != "The Matrix" || n.IDs.TMDB != 603 {
		t.Errorf("ReadNFO = %+v, %v", n, err)
	}
	if _, err := ReadNFO(filepath.Join(dir, "missing.nfo")); err == nil {
		t.Error("ReadNFO of a missing file succeeded")
	}
	if _, err := ReadNFO(dir); err == nil {
		t.Error("ReadNFO of a directory succeeded")
	}
}

// checkNFO is the invariant every parse result must satisfy; the fuzz test and
// the table share it.
func checkNFO(t testing.TB, n NFO) {
	t.Helper()
	strs := []string{n.Root, n.Title, n.OriginalTitle, n.SortTitle, n.Premiered, n.Plot, n.Outline, n.Tagline,
		n.MPAA, n.Set, n.IDs.IMDb}
	strs = append(strs, n.Genres...)
	strs = append(strs, n.Studios...)
	strs = append(strs, n.Directors...)
	strs = append(strs, n.Writers...)
	for _, a := range n.Actors {
		strs = append(strs, a.Name, a.Role, a.Thumb)
		if a.Name == "" || a.Order < 0 {
			t.Fatalf("bad actor %+v", a)
		}
	}
	for _, s := range strs {
		if !utf8.ValidString(s) {
			t.Fatalf("invalid UTF-8 in %q", s)
		}
	}
	if n.IDs.IMDb != "" && !regexpIMDb.MatchString(n.IDs.IMDb) {
		t.Fatalf("bad imdb id %q", n.IDs.IMDb)
	}
	if n.IDs.TMDB < 0 || n.IDs.TVDB < 0 {
		t.Fatalf("negative id %+v", n.IDs)
	}
	if n.Year != 0 && (n.Year < 1800 || n.Year > 2200) {
		t.Fatalf("year %d", n.Year)
	}
	if n.Rating < 0 || n.Rating > 10 || n.RuntimeMin < 0 || n.RuntimeMin >= 10000 || n.Votes < 0 || n.Season < 0 || n.Episode < 0 {
		t.Fatalf("number out of range: %+v", n)
	}
	if len(n.Genres) > maxList || len(n.Actors) > maxList || len(n.Studios) > maxList ||
		len(n.Directors) > maxList || len(n.Writers) > maxList {
		t.Fatalf("list over the cap")
	}
	if len(n.Plot) > maxText+4 {
		t.Fatalf("plot of %d bytes", len(n.Plot))
	}
}
