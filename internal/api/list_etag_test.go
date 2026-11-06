package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/store"
)

var listURLs = []string{"/api/items", "/api/items?kind=movie", "/api/shows", "/api/titles"}

// condGet asks for a URL with If-None-Match.
func (m *menv) condGet(target, inm string) *httptest.ResponseRecorder {
	m.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("If-None-Match", inm)
	for k, v := range m.auth() {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	return rec
}

func TestListsHaveAStrongETagAndAnswer304(t *testing.T) {
	m := newMetaEnv(t, "")
	for _, url := range listURLs {
		t.Run(url, func(t *testing.T) {
			first := m.get(url)
			wantStatus(t, first, 200)
			etag := first.Header().Get("ETag")
			if len(etag) < 4 || etag[0] != '"' || etag[len(etag)-1] != '"' {
				t.Fatalf("ETag = %q, want a strong tag (quoted, not W/)", etag)
			}
			if cc := first.Header().Get("Cache-Control"); cc != "private, no-cache" {
				t.Errorf("Cache-Control = %q", cc)
			}
			if first.Body.Len() < 100 {
				t.Fatalf("the control needs a list with something in it: %q", first.Body.String())
			}
			if again := m.get(url).Header().Get("ETag"); again != etag {
				t.Errorf("nothing was written and the ETag went from %s to %s", etag, again)
			}

			for _, inm := range []string{etag, "W/" + etag, `"zzz", ` + etag, "*"} {
				rec := m.condGet(url, inm)
				if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
					t.Errorf("If-None-Match %s: %d with %d bytes of body, want 304 and none", inm, rec.Code, rec.Body.Len())
				}
				if rec.Header().Get("ETag") != etag || rec.Header().Get("Cache-Control") != "private, no-cache" {
					t.Errorf("If-None-Match %s: the 304 has ETag %q and Cache-Control %q", inm, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
				}
			}
			// The control: another tag gets the list.
			other := m.condGet(url, `"another"`)
			if other.Code != 200 || other.Body.String() != first.Body.String() {
				t.Errorf("If-None-Match of another tag: %d, a body of %d bytes (want 200 and the list of %d)", other.Code, other.Body.Len(), first.Body.Len())
			}
		})
	}

	// Nothing is read for a 304: it is the answer to a request that is wrong too, so a bad
	// query is still a 400 without an ETag, whatever the client has.
	for _, url := range []string{"/api/items?kind=bogus", "/api/items?sort=name", "/api/shows?minRating=x", "/api/titles?genre=x"} {
		rec := m.condGet(url, "*")
		wantJSONError(t, rec, 400)
		if rec.Header().Get("ETag") != "" {
			t.Errorf("GET %s: a 400 with ETag %q", url, rec.Header().Get("ETag"))
		}
	}
}

// Nobody without the token learns anything about the library from a validator: with no token, a
// wrong one, or a tag copied from an authenticated answer (or the wildcard), it is 401, with no ETag.
func TestListsAnswer401BeforeAnyETag(t *testing.T) {
	m := newMetaEnv(t, apiToken)
	for _, url := range append(listURLs, "/api/items?kind=movie&fields=card") {
		t.Run(url, func(t *testing.T) {
			good := m.get(url)
			wantStatus(t, good, 200)
			etag := good.Header().Get("ETag")
			if etag == "" {
				t.Fatal("control: the authenticated answer has no ETag")
			}
			for _, who := range []struct {
				name   string
				header string // Authorization
				query  string
			}{{"no token", "", ""}, {"a wrong bearer", "Bearer nope", ""}, {"a wrong query token", "", "token=nope"}} {
				for _, inm := range []string{etag, "*", "W/" + etag} {
					target := url
					if who.query != "" {
						target += map[bool]string{true: "&", false: "?"}[strings.Contains(url, "?")] + who.query
					}
					req := httptest.NewRequest(http.MethodGet, target, nil)
					req.Header.Set("If-None-Match", inm)
					if who.header != "" {
						req.Header.Set("Authorization", who.header)
					}
					rec := httptest.NewRecorder()
					m.srv.ServeHTTP(rec, req)
					wantJSONError(t, rec, http.StatusUnauthorized)
					if rec.Header().Get("ETag") != "" {
						t.Errorf("%s, If-None-Match %s: a 401 with ETag %q", who.name, inm, rec.Header().Get("ETag"))
					}
				}
			}
			// The control: with the token, the same tag is 304.
			if rec := m.condGet(url, etag); rec.Code != http.StatusNotModified {
				t.Errorf("control: with the token, If-None-Match %s = %d, want 304", etag, rec.Code)
			}
		})
	}
}

// listState is what the lists said, to see that a change reached them.
func listState(m *menv) map[string]string {
	out := map[string]string{}
	for _, url := range listURLs {
		out[url] = m.get(url).Header().Get("ETag")
	}
	return out
}

func TestListETagChangesAfterEveryKindOfWrite(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name string
		do   func(m *menv)
		body func(m *menv) string // a piece of a list that shows the change, as a positive control
		url  string
	}{
		{"a scan: a new file", func(m *menv) {
			m.t.Helper()
			if _, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Alien", Year: 1979, Path: "/m/Alien (1979).mkv", Root: "/m"}, nil); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/items?kind=movie"},
		{"a scan: a new episode", func(m *menv) {
			if _, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Show: "Cool Show", Season: 1, Episode: 3, Path: "/t/Cool Show S01E03.mkv", Root: "/t"}, nil); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/shows"},
		{"a match of a film (unmatching it)", func(m *menv) {
			wantStatus(m.t, m.do("DELETE", fmt.Sprintf("/api/items/%d/match", m.mp4), ""), 200)
		}, nil, "/api/titles"},
		{"a match of a show", func(m *menv) {
			if err := m.store.SetSeriesMatch(ctx, "Cool Show", store.Match{State: store.MatchUnmatched}); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/shows"},
		{"an edit of a title", func(m *menv) {
			wantStatus(m.t, m.do("PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `{"sortTitle": "Zzz"}`), 200)
		}, func(m *menv) string { return m.get("/api/titles").Body.String() }, "/api/titles"},
		{"a progress save", func(m *menv) {
			wantStatus(m.t, m.putProgress(m.mkv, `{"positionSec": 100, "durationSec": 2400}`, m.auth()), 200)
		}, nil, "/api/items"},
		{"a watched change", func(m *menv) {
			if _, err := m.store.SetItemWatched(ctx, m.mp4, true); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/titles"},
		{"a rating", func(m *menv) {
			if err := m.store.ReplaceMDBListRatings(ctx, m.matrixTitle, []store.Rating{{Source: store.MDBListSources[0], Value: 88, Scale: 100}}, 1); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/titles"},
		{"a setting: the rating to show", func(m *menv) {
			if err := m.store.SetSetting(ctx, "ratings.primary", "tmdb"); err != nil {
				m.t.Fatal(err)
			}
		}, nil, ""}, // (the server of this test has no Settings, so no list changes with it: the ETag still must)
		{"files that go", func(m *menv) {
			if err := m.store.DeleteItems(ctx, []int64{m.multi}); err != nil {
				m.t.Fatal(err)
			}
		}, nil, "/api/items?kind=movie"},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			m := newMetaEnv(t, "")
			before := listState(m)
			var beforeBody string
			if w.url != "" {
				beforeBody = m.get(w.url).Body.String()
			}
			for url, etag := range before {
				if rec := m.condGet(url, etag); rec.Code != http.StatusNotModified { // the control: it holds until something is written
					t.Fatalf("GET %s with its own ETag = %d before any write", url, rec.Code)
				}
			}

			w.do(m)

			after := listState(m)
			// Every list is told (the ETag is of the library, not of the list): the rule is that none
			// can say 304 to a client that has the list from before.
			for url, etag := range before {
				if after[url] == etag {
					t.Errorf("GET %s: the ETag is still %s", url, etag)
				}
				rec := m.condGet(url, etag)
				if rec.Code != 200 {
					t.Errorf("GET %s with the ETag from before the write = %d, want 200", url, rec.Code)
				}
				if rec := m.condGet(url, after[url]); rec.Code != http.StatusNotModified { // and it settles
					t.Errorf("GET %s with the new ETag = %d, want 304", url, rec.Code)
				}
			}
			if w.url != "" && m.get(w.url).Body.String() == beforeBody {
				t.Errorf("control: GET %s is the same list after the write", w.url)
			}
		})
	}
}

// The background queue's rows are in no list: a busy queue must not make a browser read the
// library again.
func TestListETagIgnoresTheQueue(t *testing.T) {
	m := newMetaEnv(t, "")
	ctx := context.Background()
	before := listState(m)
	now := time.Now()
	id, err := m.store.InsertJob(ctx, jobs.Record{Kind: "work", Key: "k", State: jobs.Running, CreatedAt: now, RunAt: now, StartedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.UpdateJob(ctx, jobs.Record{ID: id, Kind: "work", Key: "k", State: jobs.Done, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	for url, etag := range before {
		if rec := m.condGet(url, etag); rec.Code != http.StatusNotModified {
			t.Errorf("GET %s after a job ran = %d, want 304", url, rec.Code)
		}
	}
	// The control: a write of the library, right after, does move them.
	if _, err := m.store.SetProgress(ctx, m.mkv, 5, 2400); err != nil {
		t.Fatal(err)
	}
	if rec := m.condGet("/api/items", before["/api/items"]); rec.Code != 200 {
		t.Errorf("control: GET /api/items after a progress save = %d, want 200", rec.Code)
	}
}

// The list carries the colour of each picture, which is not in the database: the ETag moves
// when a colour appears.
func TestListETagChangesWhenAnArtColourAppears(t *testing.T) {
	e := newEnv(t, "")
	e.withArt(fakeArtScript)
	before := e.get("/api/items").Header().Get("ETag")
	if rec := e.do("GET", "/api/items", map[string]string{"If-None-Match": before}, ""); rec.Code != http.StatusNotModified {
		t.Fatalf("control: GET /api/items with its own ETag = %d", rec.Code)
	}
	wantStatus(t, e.get(fmt.Sprintf("/api/items/%d/art/thumb", e.mp4)), 200) // makes the picture, and learns its colour

	rec := e.do("GET", "/api/items", map[string]string{"If-None-Match": before}, "")
	if rec.Code != 200 {
		t.Fatalf("GET /api/items with the ETag from before the picture = %d, want 200", rec.Code)
	}
	if rec.Header().Get("ETag") == before {
		t.Error("the ETag did not change")
	}
	var colour string
	for _, it := range decode[[]ItemSummary](t, rec) {
		if it.ID == e.mp4 && it.Art != nil {
			colour = it.Art.Color
		}
	}
	if colour == "" {
		t.Error("control: the list does not carry the colour")
	}
}
