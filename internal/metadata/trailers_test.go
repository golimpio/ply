package metadata

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ply/internal/store"
)

func TestParseVideoURL(t *testing.T) {
	const yt = "dQw4w9WgXcQ"
	for _, tc := range []struct {
		url       string
		site, key string
	}{
		{"https://www.youtube.com/watch?v=" + yt, store.SiteYouTube, yt},
		{"https://youtube.com/watch?v=" + yt + "&t=10s", store.SiteYouTube, yt},
		{"http://m.youtube.com/watch?v=" + yt, store.SiteYouTube, yt},
		{"https://youtu.be/" + yt, store.SiteYouTube, yt},
		{"https://www.youtube.com/embed/" + yt, store.SiteYouTube, yt},
		{"https://www.youtube-nocookie.com/embed/" + yt, store.SiteYouTube, yt},
		{"https://www.youtube.com/shorts/" + yt, store.SiteYouTube, yt},
		{" https://vimeo.com/76979871 ", store.SiteVimeo, "76979871"},
		{"https://player.vimeo.com/video/76979871", store.SiteVimeo, "76979871"},

		{"https://www.youtube.com/watch?v=short", "", ""},          // not a YouTube id
		{"https://www.youtube.com/watch?v=" + yt + "x", "", ""},    // one character too many
		{"https://www.youtube.com/watch?v=dQw4w9WgX%22Q", "", ""},  // a quote in it
		{"https://www.youtube.com/channel/" + yt, "", ""},          // not a video
		{"https://www.youtube.com/embed/" + yt + "/more", "", ""},  // a path that goes on
		{"https://youtube.com.evil.example/watch?v=" + yt, "", ""}, // another host
		{"https://evil.example/youtube.com/watch?v=" + yt, "", ""}, // another host, the name in the path
		{"javascript:alert(1)//youtube.com/watch?v=" + yt, "", ""}, // not http
		{"https://user@www.youtube.com/watch?v=" + yt, "", ""},     // credentials
		{"https://vimeo.com/channels/staffpicks/76979871", "", ""}, // not the plain address
		{"https://vimeo.com/abc", "", ""},                          // not a Vimeo id
		{"https://www.dailymotion.com/video/x7tgad0", "", ""},      // a site that is not played
		{"", "", ""},
	} {
		site, key, ok := ParseVideoURL(tc.url)
		if site != tc.site || key != tc.key || ok != (tc.site != "") {
			t.Errorf("ParseVideoURL(%q) = %q, %q, %v; want %q, %q", tc.url, site, key, ok, tc.site, tc.key)
		}
	}
}

// yt makes a valid YouTube key from a short name, so that the tests read.
func yt(name string) string { return (name + "___________")[:11] }

func keys(vs []store.Video) []string {
	var out []string
	for _, v := range vs {
		out = append(out, strings.TrimRight(v.Key, "_"))
	}
	return out
}

func TestRankVideos(t *testing.T) {
	v := func(name, lang, typ string, official bool, published string) store.Video {
		return store.Video{Site: store.SiteYouTube, Key: yt(name), Name: name, Type: typ, Language: lang, Official: official, PublishedAt: published}
	}
	for _, tc := range []struct {
		name  string
		langs []string
		in    []store.Video
		want  []string
	}{
		{"our language, then English, then none, then any other", []string{"de", "en"},
			[]store.Video{v("fr", "fr", "trailer", true, ""), v("none", "", "trailer", true, ""), v("en", "en", "trailer", true, ""), v("de", "de", "trailer", true, "")},
			[]string{"de", "en", "none", "fr"}},
		{"a language compared without regard to case", []string{"en"},
			[]store.Video{v("none", "", "trailer", true, ""), v("EN", "EN", "trailer", true, "")}, []string{"EN", "none"}},
		{"a trailer before a teaser", []string{"en"},
			[]store.Video{v("teaser", "en", "teaser", true, ""), v("trailer", "en", "trailer", true, "")}, []string{"trailer", "teaser"}},
		{"a type not given counts as a trailer", []string{"en"},
			[]store.Video{v("teaser", "en", "teaser", true, ""), v("tvdb", "en", "", true, "")}, []string{"tvdb", "teaser"}},
		{"an official one first", []string{"en"},
			[]store.Video{v("fan", "en", "trailer", false, "2024-01-01"), v("studio", "en", "trailer", true, "2020-01-01")}, []string{"studio", "fan"}},
		{"the newest first, an unknown date last", []string{"en"},
			[]store.Video{v("none", "en", "trailer", true, ""), v("old", "en", "trailer", true, "2019-05-01"), v("new", "en", "trailer", true, "2021-09-09")},
			[]string{"new", "old", "none"}},
		{"ties keep the provider's order", []string{"en"},
			[]store.Video{v("a", "en", "trailer", true, ""), v("b", "en", "trailer", true, ""), v("c", "en", "trailer", true, "")}, []string{"a", "b", "c"}},
		{"language beats type", []string{"de", "en"},
			[]store.Video{v("en", "en", "trailer", true, ""), v("de", "de", "teaser", false, "")}, []string{"de", "en"}},
		{"no language asked for: none before the others", nil,
			[]store.Video{v("fr", "fr", "trailer", true, ""), v("none", "", "trailer", true, "")}, []string{"none", "fr"}},
	} {
		if got := keys(rankVideos(tc.in, tc.langs...)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRankVideosDropsWhatCannotBePlayedAndKeepsTen(t *testing.T) {
	good := store.Video{Site: store.SiteYouTube, Key: yt("good"), Name: "  Good  "}
	in := []store.Video{
		good,
		{Site: store.SiteYouTube, Key: yt("good"), Name: "the same video again"},
		{Site: store.SiteYouTube, Key: "short"},
		{Site: "dailymotion", Key: yt("dm")},
		{Site: store.SiteVimeo, Key: "123"},
		{Site: store.SiteYouTube, Key: yt("long"), Name: strings.Repeat("é", maxVideoName+5)},
	}
	got := rankVideos(in)
	if want := []string{"good", "123", "long"}; !slices.Equal(keys(got), want) {
		t.Fatalf("kept %v, want %v", keys(got), want)
	}
	if got[0].Name != "Good" {
		t.Errorf("name %q, want it trimmed", got[0].Name)
	}
	if n := len([]rune(got[2].Name)); n != maxVideoName || !strings.HasPrefix(got[2].Name, "éé") {
		t.Errorf("a long name has %d runes, want %d", n, maxVideoName)
	}

	var many []store.Video
	for i := range maxVideos + 3 {
		many = append(many, store.Video{Site: store.SiteVimeo, Key: fmt.Sprint(1000 + i)})
	}
	if got := rankVideos(many); len(got) != maxVideos || got[0].Key != "1000" || got[maxVideos-1].Key != fmt.Sprint(1000+maxVideos-1) {
		t.Errorf("of %d videos kept %v", len(many), keys(got))
	}
}

const tmdbVideos = `{"id": 603, "results": [
	{"iso_639_1": "en", "name": "Behind the scenes", "key": "aaaaaaaaaaa", "site": "YouTube", "type": "Featurette", "official": true, "published_at": "2022-01-01T00:00:00.000Z"},
	{"iso_639_1": "en", "name": "Teaser", "key": "bbbbbbbbbbb", "site": "YouTube", "type": "Teaser", "official": true, "published_at": "1998-12-01T00:00:00.000Z"},
	{"iso_639_1": "en", "name": "Official Trailer", "key": "vKQi3bBA1y8", "site": "YouTube", "type": "Trailer", "official": true, "published_at": "1999-03-01T17:00:00.000Z"},
	{"iso_639_1": "en", "name": "On Vimeo", "key": "76979871", "site": "Vimeo", "type": "Trailer", "official": false, "published_at": ""},
	{"iso_639_1": "en", "name": "Elsewhere", "key": "x7tgad0", "site": "Dailymotion", "type": "Trailer", "official": true},
	{"iso_639_1": "en", "name": "A clip", "key": "ccccccccccc", "site": "YouTube", "type": "Clip", "official": true}
]}`

func TestTMDBTrailers(t *testing.T) {
	f := newFakeTMDB(t)
	f.set("/movie/603/videos", "", fakeRoute{status: 200, body: tmdbVideos})
	f.set("/tv/1396/videos", "", fakeRoute{status: 200, body: `{"id": 1396, "results": []}`})
	p := newTMDB(t, f, func(o *Options) { o.Language = "pt-BR" })

	got, err := p.Trailers(t.Context(), store.KindMovie, 603)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Video{
		{Site: store.SiteYouTube, Key: "vKQi3bBA1y8", Name: "Official Trailer", Type: store.VideoTrailer, Language: "en", Official: true, PublishedAt: "1999-03-01"},
		{Site: store.SiteVimeo, Key: "76979871", Name: "On Vimeo", Type: store.VideoTrailer, Language: "en"},
		{Site: store.SiteYouTube, Key: "bbbbbbbbbbb", Name: "Teaser", Type: store.VideoTeaser, Language: "en", Official: true, PublishedAt: "1998-12-01"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("movie trailers:\n got %+v\nwant %+v", got, want)
	}
	req := f.requests()[0]
	if q := req.Query; q.Get("include_video_language") != "pt,en,null" || q.Get("language") != "pt-BR" {
		t.Errorf("asked %s with %v", req.Path, q)
	}

	// A show is asked at /tv; none is no error.
	if got, err := p.Trailers(t.Context(), store.KindSeries, 1396); err != nil || len(got) != 0 {
		t.Errorf("show with none: %v, %v", got, err)
	}
	if n := f.count("/tv/1396/videos"); n != 1 {
		t.Errorf("/tv/1396/videos asked %d times", n)
	}

	f.fail("/movie/9/videos", "", 404, "error_404.json", nil)
	if _, err := p.Trailers(t.Context(), store.KindMovie, 9); !IsNotFound(err) {
		t.Errorf("unknown movie: %v", err)
	}
}

func TestTVDBTrailers(t *testing.T) {
	f := newTVDBFake(t)
	f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 200, body: `{"status": "success", "data": {"id": 81189, "name": "Breaking Bad",
		"trailers": [
			{"id": 1, "name": "Season 1 Trailer", "url": "https://www.youtube.com/watch?v=HhesaQXLuRY", "language": "eng", "runtime": 0},
			{"id": 2, "name": "Somewhere else", "url": "https://www.dailymotion.com/video/x7tgad0", "language": "eng"},
			{"id": 3, "name": "Bande-annonce", "url": "https://youtu.be/XZ8daibM3AE", "language": "fra"},
			{"id": 4, "name": "On Vimeo", "url": "https://vimeo.com/76979871", "language": ""}
		]}}`})
	p := testTVDBProvider(t, f)
	got, err := p.Trailers(t.Context(), store.KindSeries, 81189)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Video{
		{Site: store.SiteYouTube, Key: "HhesaQXLuRY", Name: "Season 1 Trailer", Type: store.VideoTrailer, Language: "eng"},
		{Site: store.SiteVimeo, Key: "76979871", Name: "On Vimeo", Type: store.VideoTrailer},
		{Site: store.SiteYouTube, Key: "XZ8daibM3AE", Name: "Bande-annonce", Type: store.VideoTrailer, Language: "fra"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("show trailers:\n got %+v\nwant %+v", got, want)
	}

	// TVDB has no movies: nothing is asked.
	before := len(f.requests())
	if got, err := p.Trailers(t.Context(), store.KindMovie, 81189); err != nil || got != nil || len(f.requests()) != before {
		t.Errorf("a movie: %v, %v, %d requests", got, err, len(f.requests())-before)
	}
}

// A provider without trailers.
type noTrailers struct{ Provider }

func TestHealthProviderPassesTrailersThrough(t *testing.T) {
	f := newTVDBFake(t)
	f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 503})
	var down, refused atomic.Bool
	h := trackHealth(testTVDBProvider(t, f), &down, &refused)
	l, ok := h.(trailerLister)
	if !ok {
		t.Fatal("the health wrapper hides Trailers")
	}
	if _, err := l.Trailers(t.Context(), store.KindSeries, 81189); err == nil || !down.Load() {
		t.Errorf("a 503: %v, down %v; want the failure noted", err, down.Load())
	}
	// Positive control of the other way: an answer clears it.
	f.set("/series/81189/extended", tvdbExt, fakeRoute{status: 200, body: `{"status": "success", "data": {"id": 81189, "name": "Breaking Bad"}}`})
	if _, err := l.Trailers(t.Context(), store.KindSeries, 81189); err != nil || down.Load() {
		t.Errorf("an answer: %v, down %v", err, down.Load())
	}

	none := trackHealth(noTrailers{testTVDBProvider(t, f)}, &down, &refused).(trailerLister)
	before := len(f.requests())
	if before == 0 {
		t.Fatal("control: the requests above were not counted")
	}
	if got, err := none.Trailers(t.Context(), store.KindSeries, 81189); got != nil || err != nil || len(f.requests()) != before {
		t.Errorf("a provider without trailers: %v, %v, %d requests", got, err, len(f.requests())-before)
	}
}
