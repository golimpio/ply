package api

import (
	"context"
	"fmt"
	"maps"
	"net/http/httptest"
	"strings"
	"testing"
)

// With no token (the default) any web page the user opens can send requests to
// the server, and CORS is open for the media. What changes something must come
// from the server's own pages, or from a client that sends no Origin.
func TestCrossSiteWritesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		hdr    map[string]string
		want   int
	}{
		{"no origin (a native client)", "", nil, 200},
		{"the server's own page", "http://example.com", nil, 200}, // httptest requests are for example.com
		{"a page of another site", "http://evil.example", nil, 403},
		{"a subdomain of the host", "http://evil.example.com", nil, 403},
		{"a host that ends like ours", "http://notexample.com", nil, 403},
		{"another port of the same host", "http://example.com:8421", nil, 403},
		{"the same host in other case", "http://EXAMPLE.com", nil, 200},
		{"another scheme of the same host", "https://example.com", nil, 403},
		{"the same page behind a TLS proxy", "https://example.com", map[string]string{"X-Forwarded-Proto": "https"}, 200},
		{"another site, saying it is behind a proxy", "https://evil.example", map[string]string{"X-Forwarded-Proto": "https"}, 403},
		{"a sandboxed page", "null", nil, 403},
	} {
		m := newMetaEnv(t, "") // no token
		patch := func() *httptest.ResponseRecorder {
			hdr := map[string]string{"Origin": tc.origin}
			maps.Copy(hdr, tc.hdr)
			if tc.origin == "" {
				delete(hdr, "Origin")
			}
			return m.env.do("PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), hdr, `{"title":"Edited"}`)
		}
		rec := patch()
		got := rec.Code
		title, _ := m.store.GetTitle(context.Background(), m.matrixTitle)
		if got != tc.want {
			t.Errorf("%s: PATCH = %d, want %d", tc.name, got, tc.want)
		}
		if edited := title.Title == "Edited"; edited != (tc.want == 200) {
			t.Errorf("%s: the title was edited = %v with status %d", tc.name, edited, got)
		}
		if tc.want == 403 {
			want := fmt.Sprintf("Origin %s does not match Host example.com; a reverse proxy must pass the original Host header", tc.origin)
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("%s: the refusal should say %q: %s", tc.name, want, rec.Body)
			}
		}
	}
}

func TestCrossSiteRequestsOfEveryMethodAreRefused(t *testing.T) {
	m := newMetaEnv(t, "")
	foreign := map[string]string{"Origin": "http://evil.example"}
	ctx := context.Background()
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/scan", ""}, // a "simple" request: no preflight, sent as it is
		{"POST", "/api/metadata/refresh", ""},
		{"POST", fmt.Sprintf("/api/items/%d/metadata/refresh", m.mp4), ""},
		{"PUT", "/api/settings", `{"language":"fr-FR"}`},
		{"PUT", fmt.Sprintf("/api/items/%d/match", m.multi), `{"tmdbId":603}`},
		{"DELETE", fmt.Sprintf("/api/items/%d/match", m.mp4), ""},
		{"PUT", fmt.Sprintf("/api/items/%d/progress", m.mp4), `{"positionSec":60,"durationSec":120}`},
		{"PUT", fmt.Sprintf("/api/items/%d/watched", m.mp4), `{"watched":true}`},
		{"PUT", fmt.Sprintf("/api/titles/%d/watched", m.matrixTitle), `{"watched":true}`},
		{"PUT", "/api/shows/Cool%20Show/watched", `{"watched":true}`},
		{"PUT", "/api/shows/Cool%20Show/seasons/1/watched", `{"watched":true}`},
		{"PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `{"title":"Hacked"}`},
		{"DELETE", "/api/sessions/nope", ""},
	} {
		wantJSONError(t, m.env.do(tc.method, tc.path, foreign, tc.body), 403)
		// Positive control: without the Origin the request is handled (not refused).
		if rec := m.env.do(tc.method, tc.path, nil, tc.body); rec.Code == 403 {
			t.Errorf("%s %s without an Origin = 403", tc.method, tc.path)
		}
	}
	if m.scanner.triggered != 1 { // only the control requests
		t.Errorf("scan triggered %d times, want 1 (the control)", m.scanner.triggered)
	}
	if e, _ := m.store.GetEntry(ctx, m.mp4); e.MatchState != "matched" {
		t.Errorf("a refused request changed an item: %+v", e.Item)
	}
}

// What is read stays open to every origin: the media, the artwork, Cast.
func TestCrossSiteReadsAndPreflightsStayOpen(t *testing.T) {
	m := newMetaEnv(t, "")
	foreign := map[string]string{"Origin": "http://evil.example"}
	for _, path := range []string{"/api/items", fmt.Sprintf("/api/items/%d/file", m.mp4), "/api/images/tmdb/w342/p603.jpg", "/api/titles"} {
		rec := m.env.do("GET", path, foreign, "")
		if rec.Code == 403 {
			t.Errorf("GET %s from another origin = 403", path)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("GET %s: Access-Control-Allow-Origin = %q", path, got)
		}
	}
	rec := m.env.do("HEAD", fmt.Sprintf("/api/items/%d/file", m.mp4), foreign, "")
	if rec.Code == 403 {
		t.Error("HEAD from another origin = 403")
	}
	// The preflight is answered as before: what it announces is refused when it is sent.
	rec = m.env.do("OPTIONS", "/api/settings", map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "PUT"}, "")
	if rec.Code != 204 {
		t.Errorf("preflight = %d, want 204", rec.Code)
	}
}

// With a token, whoever has it may write from anywhere: a client of another
// origin (a web UI served elsewhere) carries it, and a page that does not know
// it is refused either way.
func TestCrossSiteWritesWithATokenNeedTheToken(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	path := fmt.Sprintf("/api/titles/%d", m.matrixTitle)
	origin := "http://ui.example"
	for name, tc := range map[string]struct {
		hdr  map[string]string
		path string
		want int
	}{
		"bearer":              {map[string]string{"Origin": origin, "Authorization": "Bearer " + apiToken}, path, 200},
		"query":               {map[string]string{"Origin": origin}, path + "?token=" + apiToken, 200},
		"no token":            {map[string]string{"Origin": origin}, path, 403},
		"wrong token":         {map[string]string{"Origin": origin, "Authorization": "Bearer nope"}, path, 403},
		"same origin":         {map[string]string{"Origin": "http://example.com", "Authorization": "Bearer " + apiToken}, path, 200},
		"no origin":           {map[string]string{"Authorization": "Bearer " + apiToken}, path, 200},
		"no origin, no token": {nil, path, 401},
	} {
		if rec := m.env.do("PATCH", tc.path, tc.hdr, `{"title":"Edited"}`); rec.Code != tc.want {
			t.Errorf("%s: PATCH = %d, want %d", name, rec.Code, tc.want)
		}
	}
}
