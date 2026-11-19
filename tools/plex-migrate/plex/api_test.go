package plex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	sectionsJSON = `{"MediaContainer":{"Directory":[
		{"key":"1","type":"movie","title":"Movies","Location":[{"path":"/media/Movies"}]},
		{"key":"2","type":"show","title":"TV","Location":[{"path":"/media/TV"}]},
		{"key":"3","type":"artist","title":"Music"}]}}`
	moviesJSON = `{"MediaContainer":{"Metadata":[
		{"ratingKey":"10","type":"movie","guid":"plex://movie/x","Guid":[{"id":"imdb://tt0107302"},{"id":"tmdb://603"}],
		 "title":"The Matrix","year":1999,"thumb":"/library/metadata/10/thumb/1","art":"/library/metadata/10/art/1",
		 "viewOffset":60000,"viewCount":1,"lastViewedAt":1700000000,"userRating":8,
		 "Media":[{"Part":[{"file":"/media/Movies/The Matrix (1999).mkv"}]}]}]}}`
	showsJSON = `{"MediaContainer":{"Metadata":[
		{"ratingKey":"20","type":"show","guid":"plex://show/y","Guid":[{"id":"tmdb://1396"},{"id":"tvdb://81189"}],
		 "title":"Breaking Bad","year":2008,"thumb":"/library/metadata/20/thumb/1"}]}}`
	seasonsJSON = `{"MediaContainer":{"Metadata":[
		{"ratingKey":"21","type":"season","parentTitle":"Breaking Bad","index":1,"title":"Season 1","thumb":"/library/metadata/21/thumb/1"}]}}`
	episodesJSON = `{"MediaContainer":{"Metadata":[
		{"ratingKey":"22","type":"episode","grandparentTitle":"Breaking Bad","parentIndex":1,"index":1,
		 "guid":"plex://episode/z","Guid":[{"id":"imdb://tt0959621"}],"title":"Pilot","thumb":"/library/metadata/22/thumb/1",
		 "viewCount":1,"Media":[{"Part":[{"file":"/media/TV/Breaking Bad/Season 01/Breaking Bad S01E01.mkv"}]}]}]}}`
)

func fakePlex(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/library/sections":
			io.WriteString(w, sectionsJSON)
		case r.URL.Path == "/library/sections/1/all":
			io.WriteString(w, moviesJSON)
		case r.URL.Path == "/library/sections/2/all":
			switch r.URL.Query().Get("type") {
			case "2":
				io.WriteString(w, showsJSON)
			case "3":
				io.WriteString(w, seasonsJSON)
			case "4":
				io.WriteString(w, episodesJSON)
			default:
				io.WriteString(w, `{"MediaContainer":{}}`)
			}
		case strings.HasPrefix(r.URL.Path, "/library/metadata/") &&
			(strings.HasSuffix(r.URL.Path, "/thumb") || strings.HasSuffix(r.URL.Path, "/art")):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 'a'})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAPILibraries(t *testing.T) {
	srv := fakePlex(t)
	s := NewAPI(srv.URL, "tok")
	libs, err := s.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 { // the artist library is skipped
		t.Fatalf("libraries = %+v", libs)
	}
	if libs[0].Kind != Movie || libs[0].Paths[0] != "/media/Movies" {
		t.Errorf("movie library = %+v", libs[0])
	}
	if libs[1].Kind != Show || libs[1].Paths[0] != "/media/TV" {
		t.Errorf("show library = %+v", libs[1])
	}
}

func TestAPIItems(t *testing.T) {
	srv := fakePlex(t)
	s := NewAPI(srv.URL, "tok")
	items, err := s.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	movie, ok := itemByKey(items, "10")
	if !ok || movie.Kind != Movie || movie.IDs.IMDb != "tt0107302" || movie.IDs.TMDB != 603 {
		t.Fatalf("movie = %+v (ok=%v)", movie, ok)
	}
	if len(movie.Files) != 1 || movie.Files[0] != "/media/Movies/The Matrix (1999).mkv" {
		t.Errorf("movie files = %v", movie.Files)
	}
	if movie.Watch.OffsetMS != 60000 || movie.Watch.ViewCount != 1 || movie.Watch.UserRating != 8 {
		t.Errorf("movie watch = %+v", movie.Watch)
	}
	show, ok := itemByKey(items, "20")
	if !ok || show.Kind != Show || show.IDs.TMDB != 1396 || show.IDs.TVDB != 81189 {
		t.Fatalf("show = %+v (ok=%v)", show, ok)
	}
	season, ok := itemByKey(items, "21")
	if !ok || season.Kind != Season || season.Show != "Breaking Bad" || season.Season != 1 {
		t.Fatalf("season = %+v (ok=%v)", season, ok)
	}
	ep, ok := itemByKey(items, "22")
	if !ok || ep.Kind != Episode || ep.Show != "Breaking Bad" || ep.Season != 1 || ep.Episode != 1 || ep.IDs.IMDb != "tt0959621" {
		t.Fatalf("episode = %+v (ok=%v)", ep, ok)
	}
}

func TestAPIArt(t *testing.T) {
	srv := fakePlex(t)
	s := NewAPI(srv.URL, "tok")
	items, _ := s.Items(context.Background())
	movie, _ := itemByKey(items, "10")
	b, err := s.Art(context.Background(), movie, "poster")
	if err != nil {
		t.Fatalf("art: %v", err)
	}
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		t.Errorf("art bytes = %v", b)
	}
	// An item with no thumb has no art (positive control: movie 10 did).
	noArt := Item{RatingKey: "99", Kind: Movie}
	if _, err := s.Art(context.Background(), noArt, "poster"); err != ErrNoArt {
		t.Errorf("no-thumb art = %v, want ErrNoArt", err)
	}
}

func TestAPITokenSent(t *testing.T) {
	srv := fakePlex(t)
	s := NewAPI(srv.URL, "wrong")
	if _, err := s.Libraries(context.Background()); err == nil {
		t.Error("a wrong token should be refused by the server")
	}
}
