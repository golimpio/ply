package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
	"ply/internal/watch"
)

const apiToken = "s3cret"

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("image-bytes")...)

const pngBytes = "\x89PNG\r\n\x1a\nimage-bytes"

// menv is the API over a library with metadata: the env of api_test.go plus a
// fake TMDB, a worker, an image cache and a captured log.
type menv struct {
	*env
	srv     *Server
	fake    *metadatatest.Fake
	src     *metadata.StaticSource
	worker  *metadata.Worker
	images  *metadata.ImageCache
	imgSrv  *httptest.Server
	imgHits *int32
	logs    *syncBuffer
	// ids
	matrixTitle, showTitle, personID, genreID, collectionID int64
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newMetaEnv builds the library of newEnv with metadata for the movie
// "Movie (2020)" (title 603) and the show "Cool Show".
func newMetaEnv(t *testing.T, token string, mod ...func(*Options)) *menv {
	t.Helper()
	return newMetaEnvWith(t, token, func(_ *menv, o *Options) {
		for _, f := range mod {
			f(o)
		}
	})
}

// newMetaEnvWith is newMetaEnv with a hook that sees the environment (fake,
// images, store) and the options before the server is built.
func newMetaEnvWith(t *testing.T, token string, hook func(*menv, *Options)) *menv {
	t.Helper()
	e := newEnv(t, token)
	m := &menv{env: e, fake: &metadatatest.Fake{}, logs: &syncBuffer{}}
	ctx := context.Background()

	var hits int32
	var hmu sync.Mutex
	m.imgHits = &hits
	m.imgSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hmu.Lock()
		hits++
		hmu.Unlock()
		w.Write(jpegBytes)
	}))
	t.Cleanup(m.imgSrv.Close)
	m.fake.ImageBase = m.imgSrv.URL
	m.src = metadata.NewStaticSource(m.fake)

	var err error
	m.images, err = metadata.NewImageCache(metadata.ImageOptions{Dir: filepath.Join(e.dir, "..", "images"), URL: m.src.ImageURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.images.Close)
	m.worker = metadata.NewWorker(metadata.WorkerOptions{Store: e.store, Source: m.src, Images: m.images, Refresh: 0})

	movie := &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 603, IMDbID: "tt0133093", Title: "The Matrix",
		OriginalTitle: "The Matrix", SortTitle: "Matrix, The", Year: 1999, Overview: "A hacker learns.", Tagline: "Wake up.",
		ReleaseDate: "1999-03-30", RuntimeMin: 136, Rating: 8.2, VoteCount: 25000, Certification: "R", Status: "Released",
		Poster: "tmdb:/p603.jpg", Backdrop: "tmdb:/b603.jpg", Logo: "tmdb:/l603.png", Source: store.SourceTMDB},
		Genres:     []store.Genre{{ID: 28, Name: "Action"}, {ID: 878, Name: "Science Fiction"}},
		Collection: &store.Collection{TMDBID: 2344, Name: "The Matrix Collection", Overview: "Wake up.", Poster: "tmdb:/cp.jpg"},
		Credits: []store.Credit{
			{Person: store.Person{TMDBID: 6384, Name: "Keanu Reeves", Profile: "tmdb:/keanu.jpg"}, Kind: store.CreditCast, Character: "Neo"},
			{Person: store.Person{TMDBID: 9339, Name: "Lana Wachowski"}, Kind: store.CreditCrew, Job: "Director"},
		}}
	m.matrixTitle, err = e.store.UpsertTitle(ctx, movie)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetItemMatch(ctx, e.mp4, store.Match{TitleID: m.matrixTitle, State: store.MatchMatched, Score: 0.95}); err != nil {
		t.Fatal(err)
	}

	show := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TMDBID: 1396, Title: "Cool Show Official", Year: 2008,
		Overview: "A show.", Rating: 8.9, Certification: "TV-MA", Status: "Ended", Poster: "tmdb:/show.jpg", Backdrop: "tmdb:/showb.jpg",
		Source: store.SourceTMDB},
		Genres: []store.Genre{{ID: 18, Name: "Drama"}},
		Seasons: []store.Season{{Number: 1, Name: "The First Season", Overview: "It starts.", Poster: "tmdb:/s1.jpg", AirDate: "2008-01-20",
			Episodes: []store.Episode{{Number: 2, Name: "Pilot Episode", Overview: "The beginning.", AirDate: "2008-01-27",
				RuntimeMin: 58, Still: "tmdb:/still2.jpg", Rating: 8.3}}}}}
	m.showTitle, err = e.store.UpsertTitle(ctx, show)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSeriesMatch(ctx, "Cool Show", store.Match{TitleID: m.showTitle, State: store.MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	people, _ := e.store.SearchPeople(ctx, "Keanu", 5)
	if len(people) != 1 {
		t.Fatalf("people = %+v", people)
	}
	m.personID = people[0].ID
	genres, _ := e.store.Genres(ctx)
	for _, g := range genres {
		if g.Name == "Action" {
			m.genreID = g.ID
		}
	}
	cards, _ := e.store.TitleCards(ctx, []int64{m.matrixTitle})
	d, _ := e.store.GetTitleDetail(ctx, m.matrixTitle)
	if d.Collection == nil {
		t.Fatal("no collection")
	}
	m.collectionID = d.Collection.ID
	_ = cards

	logger := slog.New(slog.NewTextHandler(m.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := Options{Store: e.store, Scanner: e.scanner, UI: testUI, ID: "abc123", Name: "Test Server", Version: "9.9.9", Token: token,
		Logger: logger, Metadata: m.worker, Images: m.images}
	hook(m, &o)
	m.srv = New(o)
	return m
}

func (m *menv) auth() map[string]string {
	if m.srv.Token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + m.srv.Token}
}

func (m *menv) do(method, target string, body string) *httptest.ResponseRecorder {
	m.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range m.auth() {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	return rec
}

func (m *menv) get(target string) *httptest.ResponseRecorder { return m.do(http.MethodGet, target, "") }

func (m *menv) imageHits() int {
	return int(*m.imgHits)
}

func (m *menv) writeFile(rel, content string) string {
	m.t.Helper()
	p := filepath.Join(m.env.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
	return p
}

// withToken is how the server writes URLs when a token is configured.
func withTok(u string) string {
	if strings.Contains(u, "?") {
		return u + "&token=" + apiToken
	}
	return u + "?token=" + apiToken
}

var _ = fmt.Sprint

type stubWatcher struct{}

func (stubWatcher) Status() []watch.RootStatus {
	return []watch.RootStatus{{Root: "/media/a", Watching: true, Dirs: 12}, {Root: "/media/b", Error: "root not found"}}
}

func httptestGet(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}
