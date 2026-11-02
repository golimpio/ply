package metadata

import (
	"reflect"
	"testing"
)

func FuzzNFO(f *testing.F) {
	for _, seed := range []string{
		matrixNFO,
		"",
		"https://www.themoviedb.org/movie/603",
		"http://www.imdb.com/title/tt0133093/",
		"tt0133093",
		`<movie><title>Tom & Jerry</title><year>1999</year><uniqueid type="tmdb">603</uniqueid></movie>`,
		`<tvshow><id>81189</id><actor><name>A</name><order>2</order></actor></tvshow>`,
		`<episodedetails><season>1</season><episode>2</episode><aired>2008-01-20</aired></episodedetails>`,
		`<movie><ratings><rating max="5" default="true"><value>4</value><votes>1,000</votes></rating></ratings></movie>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><movie><title>Am` + "\xe9" + `lie</title></movie>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><movie><plot>` + "\xe9\xe9\xe9" + `</plot></movie>` + "\nhttps://www.themoviedb.org/movie/603",
		`<movie><title>a</title></movie><x><y>https://www.imdb.com/title/tt0133093</y></x>https://www.themoviedb.org/movie/1`,
		"\xef\xbb\xbf<movie><title>bom</title></movie>",
		"\xff\xfe<\x00m\x00o\x00v\x00i\x00e\x00>\x00",
		`<movie><title>a</title></movie><movie><title>b</title></movie>`,
		`<movie><![CDATA[x]]><genre>a / b / c</genre>`,
		`<a><b><c><d>` + "\x00\x01",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n := ParseNFO(data)
		checkNFO(t, n)
		if again := ParseNFO(data); !reflect.DeepEqual(n, again) {
			t.Fatalf("parsing twice gave different results:\n%+v\n%+v", n, again)
		}
	})
}

func FuzzParseIDs(f *testing.F) {
	for _, seed := range []string{"The Matrix [tmdbid-603]", "{imdb-tt0133093}", "tt0133093", "[tvdbid=81189]", "", "tt", "[tmdbid-99999999999999999999]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		h := ParseIDs(name)
		if h.TMDB < 0 || h.TVDB < 0 || (h.IMDb != "" && !regexpIMDb.MatchString(h.IMDb)) {
			t.Fatalf("ParseIDs(%q) = %+v", name, h)
		}
		StripIDs(name)
	})
}
