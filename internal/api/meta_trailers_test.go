package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/store"
)

func TestTitleTrailers(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	m.fake.TrailerLists = map[int64][]store.Video{603: {
		{Site: store.SiteYouTube, Key: "vKQi3bBA1y8", Name: "Official Trailer", Type: store.VideoTrailer, Language: "en", Official: true, PublishedAt: "1999-03-01"},
		{Site: store.SiteVimeo, Key: "76979871", Name: "Teaser", Type: store.VideoTeaser},
	}}
	path := fmt.Sprintf("/api/titles/%d/trailers", m.matrixTitle)

	rec := m.get(path)
	want := `[{"site":"youtube","key":"vKQi3bBA1y8","name":"Official Trailer","type":"trailer","language":"en","official":true,"publishedAt":"1999-03-01"},` +
		`{"site":"vimeo","key":"76979871","name":"Teaser","type":"teaser","official":false}]`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("GET %s = %d %s\nwant %s", path, rec.Code, rec.Body, want)
	}
	// Kept: a second visit asks TMDB nothing.
	if rec := m.get(path); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || m.fake.Count("Trailers(") != 1 {
		t.Errorf("second visit: %d %s, TMDB asked %d times", rec.Code, rec.Body, m.fake.Count("Trailers("))
	}

	// A title TMDB has none for (here a 404) is an empty list, not an error.
	if rec := m.get(fmt.Sprintf("/api/titles/%d/trailers", m.showTitle)); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("a show TMDB has none for: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"/api/titles/999999/trailers", "/api/titles/abc/trailers", "/api/titles/0/trailers"} {
		if rec := m.get(bad); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", bad, rec.Code, rec.Body)
		}
	}

	// The token is needed like everywhere else.
	req := httptest.NewRequest(http.MethodGet, path, nil)
	anon := httptest.NewRecorder()
	m.srv.ServeHTTP(anon, req)
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", anon.Code)
	}

	// With online lookups off none is listed, not even the stored ones: playing one would reach YouTube.
	m.src.Set(m.fake, false)
	if rec := m.get(path); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("lookups off: %d %s, want []", rec.Code, rec.Body)
	}
	if n := m.fake.Count("Trailers("); n != 2 {
		t.Errorf("TMDB asked %d times in all, want 2", n)
	}
}

func TestTitleTrailersWithoutMetadata(t *testing.T) {
	m := newMetaEnv(t, "", func(o *Options) { o.Metadata = nil })
	if rec := m.get(fmt.Sprintf("/api/titles/%d/trailers", m.matrixTitle)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no metadata subsystem: %d %s, want 503", rec.Code, rec.Body)
	}
}
