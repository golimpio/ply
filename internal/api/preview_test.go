package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ply/internal/store"
)

// conformist is a film the library does not have, as the fake TMDB knows it.
func conformist() *store.TitleInput {
	in := &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 8416, IMDbID: "tt0065571", Title: "The Conformist",
		OriginalTitle: "Il conformista", Year: 1970, Overview: "A weak-willed man.", Tagline: "A dazzling movie.", RuntimeMin: 113,
		Poster: "tmdb:/conf.jpg", Backdrop: "tmdb:/confb.jpg", Source: store.SourceTMDB},
		Genres: []store.Genre{{ID: 18, Name: "Drama"}},
		Credits: []store.Credit{
			{Person: store.Person{TMDBID: 1, Name: "Bernardo Bertolucci"}, Kind: store.CreditCrew, Job: "Director"},
			{Person: store.Person{TMDBID: 1, Name: "Bernardo Bertolucci"}, Kind: store.CreditCrew, Job: "Screenplay"},
			{Person: store.Person{TMDBID: 2, Name: "Vittorio Storaro"}, Kind: store.CreditCrew, Job: "Director of Photography"},
		}}
	for i := range previewCast + 3 {
		in.Credits = append(in.Credits, store.Credit{Person: store.Person{TMDBID: int64(100 + i), Name: "Actor " + string(rune('A'+i)),
			Profile: "tmdb:/a.jpg"}, Kind: store.CreditCast, Character: "Part", Order: i})
	}
	return in
}

func TestTitlePreview(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Movies = map[int64]*store.TitleInput{8416: conformist()}

	rec := m.get("/api/metadata/preview/movie/8416")
	wantStatus(t, rec, http.StatusOK)
	p := decode[TitlePreview](t, rec)
	if p.Title != "The Conformist" || p.OriginalTitle != "Il conformista" || p.Year != 1970 || p.Tagline != "A dazzling movie." ||
		p.RuntimeMin != 113 || p.IMDbID != "tt0065571" || p.Kind != store.KindMovie || p.TMDBID != 8416 {
		t.Errorf("preview = %+v", p)
	}
	if len(p.Genres) != 1 || p.Genres[0] != "Drama" {
		t.Errorf("genres = %v", p.Genres)
	}
	if !strings.Contains(p.Poster, "/api/images/tmdb/") || !strings.Contains(p.Backdrop, "/api/images/tmdb/") {
		t.Errorf("poster %q, backdrop %q", p.Poster, p.Backdrop)
	}
	// The cast is cut at previewCast, best billed first; the crew is that of the credit lines only.
	if len(p.Cast) != previewCast || p.Cast[0].Name != "Actor A" || p.Cast[0].Role != "Part" || p.Cast[0].Profile == "" {
		t.Errorf("cast = %+v", p.Cast)
	}
	if len(p.Crew) != 2 || p.Crew[0].Role != "Director" || p.Crew[1].Role != "Screenplay" {
		t.Errorf("crew = %+v", p.Crew)
	}

	// Nothing of it is stored: the people it names are not the library's. The search does find
	// a person who is stored (the positive control).
	if people, _ := m.store.SearchPeople(context.Background(), "Keanu", 5); len(people) != 1 {
		t.Fatalf("the stored person is not found: %+v", people)
	}
	if people, _ := m.store.SearchPeople(context.Background(), "Bertolucci", 5); len(people) != 0 {
		t.Errorf("a preview stored people: %+v", people)
	}
}

func TestTitlePreviewSeries(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Shows = map[int64]*store.TitleInput{77: {Title: store.Title{Kind: store.KindSeries, TMDBID: 77, Title: "A Show", Year: 2001},
		Credits: []store.Credit{{Person: store.Person{TMDBID: 3, Name: "Ann"}, Kind: store.CreditCrew, Job: "Creator"}}}}
	rec := m.get("/api/metadata/preview/series/77")
	wantStatus(t, rec, http.StatusOK)
	if p := decode[TitlePreview](t, rec); p.Title != "A Show" || len(p.Crew) != 1 || p.Crew[0].Role != "Creator" {
		t.Errorf("preview = %+v", p)
	}
	// A show is asked for as a show, with no seasons.
	if got := m.fake.Count("Series(77, []"); got != 1 {
		t.Errorf("calls = %v", m.fake.Calls())
	}
}

func TestTitlePreviewErrors(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Movies = map[int64]*store.TitleInput{8416: conformist()}
	wantStatus(t, m.get("/api/metadata/preview/movie/8416"), http.StatusOK) // the positive control
	wantJSONError(t, m.get("/api/metadata/preview/movie/9999"), http.StatusNotFound)
	wantJSONError(t, m.get("/api/metadata/preview/episode/8416"), http.StatusBadRequest)
	wantJSONError(t, m.get("/api/metadata/preview/movie/x"), http.StatusNotFound)

	m.fake.Offline()
	wantJSONError(t, m.get("/api/metadata/preview/movie/8416"), http.StatusBadGateway)
	m.fake.Online()

	// With online lookups off nothing is asked, and the same with no TMDB key (TVDB alone has no films).
	calls := len(m.fake.Calls())
	m.src.Set(m.fake, false)
	wantJSONError(t, m.get("/api/metadata/preview/movie/8416"), http.StatusConflict)
	m.src.Set(nil, true)
	m.src.SetTVDB(m.tvdb)
	wantJSONError(t, m.get("/api/metadata/preview/movie/8416"), http.StatusConflict)
	if got := len(m.fake.Calls()); got != calls {
		t.Errorf("TMDB was asked with lookups off or no key: %v", m.fake.Calls()[calls:])
	}
}

// Closing the dialog cancels its request: TMDB is not waited for.
func TestTitlePreviewStopsWhenTheClientGoes(t *testing.T) {
	m := newMetaEnv(t, "")
	m.fake.Movies = map[int64]*store.TitleInput{8416: conformist()}
	asked := make(chan struct{})
	m.fake.Hook = func(ctx context.Context, call string) error {
		close(asked)
		<-ctx.Done() // TMDB never answers
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/metadata/preview/movie/8416", nil)
	done := make(chan struct{})
	go func() {
		m.srv.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-asked
	cancel()
	select {
	case <-done:
	case <-time.After(previewWait):
		t.Fatal("the preview went on after the client went away")
	}
}

// previewWait is far below metadata's own timeout of a preview (10 s).
const previewWait = 2 * time.Second
