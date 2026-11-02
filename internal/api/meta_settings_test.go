package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

// keyedProvider is a fake that accepts one key: the settings test's TMDB.
type keyedProvider struct {
	*metadatatest.Fake
	key, language string
	good          string
}

func (k *keyedProvider) Check(ctx context.Context) error {
	switch {
	case k.key == k.good:
		return nil
	case strings.HasPrefix(k.key, "down"):
		return &metadata.Error{Op: "GET /configuration", Transient: true, Message: "connection refused"}
	}
	return &metadata.Error{Op: "GET /configuration", Status: 401, Message: "Invalid API key: You must be granted a valid key."}
}

// settingsEnv is the API with a real Settings over the store, whose TMDB is a
// fake that takes the key "goodkey123".
type settingsEnv struct {
	*menv
	settings *metadata.Settings
	built    []string
	mu       sync.Mutex
}

func newSettingsEnv(t *testing.T, so ...func(*metadata.SettingsOptions)) *settingsEnv {
	t.Helper()
	se := &settingsEnv{}
	newMetaEnvWith(t, apiToken, func(m *menv, o *Options) {
		se.menv = m
		opts := metadata.SettingsOptions{Store: o.Store, Language: "en-US", Logger: o.Logger,
			NewProvider: func(key, lang string) metadata.Provider {
				se.mu.Lock()
				se.built = append(se.built, key+"/"+lang)
				se.mu.Unlock()
				return &keyedProvider{Fake: se.fake, key: key, language: lang, good: "goodkey123"}
			}}
		for _, f := range so {
			f(&opts)
		}
		se.settings = metadata.NewSettings(opts)
		se.worker = metadata.NewWorker(metadata.WorkerOptions{Store: o.Store, Source: se.settings, Images: o.Images, Refresh: 0})
		o.Settings, o.Metadata = se.settings, se.worker
	})
	se.menv.worker = se.worker
	return se
}

func TestSettingsDefaultsAndShape(t *testing.T) {
	se := newSettingsEnv(t)
	rec := se.get("/api/settings")
	wantStatus(t, rec, 200)
	raw := decode[map[string]any](t, rec)
	want := map[string]any{"tmdbKeySet": false, "tmdbKeySource": "", "metadataEnabled": true, "metadataForcedOff": false, "language": "en-US"}
	for k, v := range want {
		if raw[k] != v {
			t.Errorf("settings[%s] = %v, want %v", k, raw[k], v)
		}
	}
	if len(raw) != len(want) {
		t.Errorf("settings = %v; exactly these fields, and never the key", raw)
	}
	// Without a Settings at all the page still loads.
	bare := newMetaEnv(t, "", func(o *Options) { o.Settings = nil })
	if v := decode[SettingsView](t, bare.get("/api/settings")); v.TMDBKeySet || v.Language != "en-US" || v.MetadataEnabled {
		t.Errorf("no settings: %+v", v)
	}
}

func TestSettingsKeyPrecedenceFlagEnvSettingsDefault(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		flag, env, stored, defaultKey string
		source                        string
	}{
		{"flag wins", "FLAGKEY", "ENVKEY", "goodkey123", "DEFAULTKEY", "flag"},
		{"env next", "", "ENVKEY", "goodkey123", "DEFAULTKEY", "env"},
		{"then settings", "", "", "goodkey123", "DEFAULTKEY", "settings"},
		{"then the build default", "", "", "", "DEFAULTKEY", "default"},
		{"nothing", "", "", "", "", ""},
	} {
		se := newSettingsEnv(t, func(o *metadata.SettingsOptions) { o.KeyFlag, o.KeyEnv, o.KeyDefault = tc.flag, tc.env, tc.defaultKey })
		if tc.stored != "" {
			if err := se.store.SetSetting(context.Background(), metadata.SettingTMDBKey, tc.stored); err != nil {
				t.Fatal(err)
			}
		}
		v := decode[SettingsView](t, se.get("/api/settings"))
		if v.TMDBKeySource != tc.source || v.TMDBKeySet != (tc.source != "") {
			t.Errorf("%s: %+v, want source %q", tc.name, v, tc.source)
		}
		// Saving a key in the UI does not take over from a stronger source.
		if tc.source == "flag" || tc.source == "env" {
			rec := se.do("PUT", "/api/settings", `{"tmdbKey": "goodkey123"}`)
			wantStatus(t, rec, 200)
			if got := decode[SettingsView](t, rec); got.TMDBKeySource != tc.source {
				t.Errorf("%s: after PUT source = %q", tc.name, got.TMDBKeySource)
			}
		}
	}
}

func TestSettingsPutValidatesAndTakesEffectWithoutARestart(t *testing.T) {
	se := newSettingsEnv(t)
	ctx := context.Background()
	se.fake.MovieSearch = nil
	// Before a key: the worker defers files and asks nothing.
	se.worker.RunOnce(ctx)
	if n := len(se.fake.Calls()); n != 0 {
		t.Fatalf("calls without a key: %q", se.fake.Calls())
	}
	st := decode[metadata.Status](t, se.get("/api/metadata/status"))
	if st.KeySet {
		t.Errorf("status = %+v", st)
	}

	// A refused key is a 400 with a message, and nothing is stored.
	rec := se.do("PUT", "/api/settings", `{"tmdbKey": "badkey0001"}`)
	wantJSONError(t, rec, 400)
	if !strings.Contains(rec.Body.String(), "rejected") {
		t.Errorf("message = %s", rec.Body.String())
	}
	if _, ok, _ := se.store.GetSetting(ctx, metadata.SettingTMDBKey); ok {
		t.Error("a refused key was stored")
	}
	// TMDB out of reach: a 502, not a verdict on the key.
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tmdbKey": "downkey001"}`), 502)
	if _, ok, _ := se.store.GetSetting(ctx, metadata.SettingTMDBKey); ok {
		t.Error("an unchecked key was stored")
	}

	// A good key: stored, reported, live.
	rec = se.do("PUT", "/api/settings", `{"tmdbKey": " goodkey123 "}`)
	wantStatus(t, rec, 200)
	v := decode[SettingsView](t, rec)
	if !v.TMDBKeySet || v.TMDBKeySource != "settings" || !v.MetadataEnabled {
		t.Errorf("after PUT = %+v", v)
	}
	if stored, _, _ := se.store.GetSetting(ctx, metadata.SettingTMDBKey); stored != "goodkey123" {
		t.Errorf("stored key = %q", stored)
	}
	if st := decode[metadata.Status](t, se.get("/api/metadata/status")); !st.KeySet {
		t.Errorf("status after the key = %+v", st)
	}
	// The worker, which was told, now asks TMDB for the waiting files.
	se.fake.MovieSearch = map[string][]metadata.Candidate{"multi": {{TMDBID: 7, Title: "Multi", Year: 2019, Popularity: 1}}}
	se.fake.Movies = map[int64]*store.TitleInput{7: {Title: store.Title{Kind: store.KindMovie, TMDBID: 7, Title: "Multi", Year: 2019}}}
	se.worker.RunOnce(ctx)
	if e, _ := se.store.GetEntry(ctx, se.multi); e.MatchState != store.MatchMatched {
		t.Errorf("after the key, with no restart: %+v (calls %q)", e.Item, se.fake.Calls())
	}

	// Removing the key.
	rec = se.do("PUT", "/api/settings", `{"tmdbKey": ""}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); v.TMDBKeySet {
		t.Errorf("after removing the key = %+v", v)
	}
	if _, ok, _ := se.store.GetSetting(ctx, metadata.SettingTMDBKey); ok {
		t.Error("the key is still stored")
	}
}

func TestSettingsSwitchAndLanguage(t *testing.T) {
	se := newSettingsEnv(t, func(o *metadata.SettingsOptions) { o.KeyFlag = "goodkey123" })
	v := decode[SettingsView](t, se.do("PUT", "/api/settings", `{"metadataEnabled": false, "language": "pt-BR"}`))
	if v.MetadataEnabled || v.Language != "pt-BR" || v.MetadataForcedOff {
		t.Errorf("after PUT = %+v", v)
	}
	if st := decode[metadata.Status](t, se.get("/api/metadata/status")); st.Enabled {
		t.Errorf("status = %+v", st)
	}
	wantJSONError(t, se.do("PUT", fmt.Sprintf("/api/items/%d/match", se.multi), `{"tmdbId": 5}`), 409) // switched off
	se.fake.MovieSearch = map[string][]metadata.Candidate{"the matrix": {{TMDBID: 603, Title: "The Matrix", Year: 1999}}}
	v = decode[SettingsView](t, se.do("PUT", "/api/settings", `{"metadataEnabled": true}`))
	if !v.MetadataEnabled || v.Language != "pt-BR" {
		t.Errorf("switched back on = %+v", v)
	}
	if got := decode[[]Candidate](t, se.get("/api/metadata/search?kind=movie&q=The+Matrix")); len(got) != 1 {
		t.Errorf("search with the switch on again: %+v", got)
	}
	built := se.built[len(se.built)-1]
	if built != "goodkey123/pt-BR" {
		t.Errorf("provider last built as %q; the language change should reach TMDB calls", built)
	}

	// -metadata=false: the switch cannot be turned on from the UI.
	forced := newSettingsEnv(t, func(o *metadata.SettingsOptions) { o.KeyFlag = "goodkey123"; o.Disabled = true })
	v = decode[SettingsView](t, forced.do("PUT", "/api/settings", `{"metadataEnabled": true}`))
	if v.MetadataEnabled || !v.MetadataForcedOff {
		t.Errorf("forced off = %+v", v)
	}
	wantJSONError(t, forced.get("/api/metadata/search?kind=movie&q=x"), 409)
}

func TestSettingsPutRejectsBadInput(t *testing.T) {
	se := newSettingsEnv(t)
	ctx := context.Background()
	for body, name := range map[string]string{
		``:                         "empty body",
		`{}`:                       "nothing to change",
		`nope`:                     "not JSON",
		`{"tmdbKey": 5}`:           "key as a number",
		`{"tmdbKey": "has space"}`: "key with a space",
		`{"tmdbKey": "quo\"te"}`:   "key with a quote",
		`{"tmdbKey": "new\nline"}`: "key with a newline",
		`{"tmdbKey": "a/b"}`:       "key with a slash",
		`{"tmdbKey": "` + strings.Repeat("a", 3000) + `"}`: "huge key",
		`{"metadataEnabled": "yes"}`:                       "enabled as a string",
		`{"language": "english"}`:                          "bad language",
		`{"language": ""}`:                                 "empty language",
		`{"language": 3}`:                                  "language as a number",
		`{"unknown": true}`:                                "unknown field only",
	} {
		rec := se.do("PUT", "/api/settings", body)
		if rec.Code != 400 {
			t.Errorf("%s: status %d %s, want 400", name, rec.Code, rec.Body.String())
			continue
		}
		wantJSONError(t, rec, 400)
	}
	for _, key := range []string{metadata.SettingTMDBKey, metadata.SettingEnabled, metadata.SettingLanguage} {
		if _, ok, _ := se.store.GetSetting(ctx, key); ok {
			t.Errorf("setting %s was written by a refused request", key)
		}
	}
	// Positive control: a valid change does write.
	wantStatus(t, se.do("PUT", "/api/settings", `{"language": "de-DE"}`), 200)
	if v, ok, _ := se.store.GetSetting(ctx, metadata.SettingLanguage); !ok || v != "de-DE" {
		t.Errorf("language setting = %q, %v", v, ok)
	}

	bare := newMetaEnv(t, "", func(o *Options) { o.Settings = nil })
	wantJSONError(t, bare.do("PUT", "/api/settings", `{"language": "de-DE"}`), 503)
}

// authCase is one request of the auth matrix.
type authCase struct{ method, path, body string }

func (m *menv) authCases() []authCase {
	return []authCase{
		{"GET", "/api/people?q=a", ""},
		{"GET", fmt.Sprintf("/api/people/%d", m.personID), ""},
		{"GET", "/api/genres", ""},
		{"GET", "/api/titles", ""},
		{"GET", "/api/upcoming", ""},
		{"PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `{"title": "Hacked"}`},
		{"GET", fmt.Sprintf("/api/collections/%d", m.collectionID), ""},
		{"GET", "/api/shows/Cool%20Show", ""},
		{"PUT", "/api/shows/Cool%20Show/match", `{"tmdbId": 1396}`},
		{"DELETE", "/api/shows/Cool%20Show/match", ""},
		{"POST", "/api/shows/Cool%20Show/metadata/refresh", ""},
		{"PUT", fmt.Sprintf("/api/items/%d/match", m.multi), `{"tmdbId": 603}`},
		{"DELETE", fmt.Sprintf("/api/items/%d/match", m.mp4), ""},
		{"POST", fmt.Sprintf("/api/items/%d/metadata/refresh", m.mp4), ""},
		{"POST", "/api/metadata/refresh", ""},
		{"GET", "/api/metadata/status", ""},
		{"GET", "/api/metadata/review", ""},
		{"GET", "/api/metadata/search?kind=movie&q=x", ""},
		{"GET", "/api/settings", ""},
		{"PUT", "/api/settings", `{"language": "fr-FR"}`},
		{"GET", "/api/images/tmdb/w342/p603.jpg", ""},
		{"GET", fmt.Sprintf("/api/images/local/item/%d/poster", m.mp4), ""},
	}
}

func TestEveryNewEndpointNeedsTheToken(t *testing.T) {
	for _, tc := range newSettingsEnv(t).authCases() {
		// A fresh library for each, so that one request cannot change another's outcome.
		se := newSettingsEnv(t)
		call := func(hdr map[string]string, query string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(tc.method, tc.path+query, strings.NewReader(tc.body))
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			se.srv.ServeHTTP(rec, req)
			return rec
		}
		for name, hdr := range map[string]map[string]string{
			"no token":        nil,
			"wrong token":     {"Authorization": "Bearer nope"},
			"basic auth":      {"Authorization": "Basic czNjcmV0"},
			"bare token":      {"Authorization": apiToken},
			"token as cookie": {"Cookie": "token=" + apiToken},
		} {
			rec := call(hdr, "")
			if rec.Code != 401 {
				t.Errorf("%s %s with %s = %d, want 401", tc.method, tc.path, name, rec.Code)
			}
		}
		if rec := call(nil, sep(tc.path)+"token=nope"); rec.Code != 401 {
			t.Errorf("%s %s with a wrong ?token = %d", tc.method, tc.path, rec.Code)
		}
		// Nothing was done by the refused requests.
		if len(se.fake.Calls()) != 0 {
			t.Errorf("%s %s: TMDB was asked by a refused request: %q", tc.method, tc.path, se.fake.Calls())
		}
		if e, _ := se.store.GetEntry(context.Background(), se.multi); e.MatchState != store.MatchPending || e.TitleID != 0 {
			t.Errorf("%s %s: an item changed: %+v", tc.method, tc.path, e.Item)
		}
		if title, _ := se.store.GetTitle(context.Background(), se.matrixTitle); title.Locked || title.Title != "The Matrix" {
			t.Errorf("%s %s: a title changed: %+v", tc.method, tc.path, title)
		}
		if _, ok, _ := se.store.GetSetting(context.Background(), metadata.SettingLanguage); ok {
			t.Errorf("%s %s: a setting was written", tc.method, tc.path)
		}

		// Positive control: with the token it is handled (not 401, not a 405 or a 5xx).
		for _, rec := range []*httptest.ResponseRecorder{call(map[string]string{"Authorization": "Bearer " + apiToken}, ""), call(nil, sep(tc.path)+"token="+apiToken)} {
			if rec.Code == 401 || rec.Code == 405 || rec.Code >= 500 && rec.Code != 502 && rec.Code != 503 {
				t.Errorf("%s %s with the token = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		}
	}
}

func sep(path string) string {
	if strings.Contains(path, "?") {
		return "&"
	}
	return "?"
}

// fakeTMDB is a TMDB that echoes the key in its error messages, the worst case
// for a server that must not leak it.
func fakeTMDB(t *testing.T, good string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("api_key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case key != good:
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"status_code":7,"status_message":"Invalid API key %s: you must be granted a valid key (api_key=%s)."}`, key, key)
		case strings.HasPrefix(r.URL.Path, "/3/configuration"):
			fmt.Fprint(w, `{"images":{"secure_base_url":"https://image.tmdb.org/t/p/"}}`)
		case strings.HasPrefix(r.URL.Path, "/3/search/movie"):
			fmt.Fprint(w, `{"results":[{"id":603,"title":"The Matrix","original_title":"The Matrix","release_date":"1999-03-30","popularity":80,"poster_path":"/p.jpg"}]}`)
		case strings.HasPrefix(r.URL.Path, "/3/person/"):
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"status_message":"server trouble for key %s"}`, key)
		default:
			w.WriteHeader(500)
			fmt.Fprintf(w, `{"status_message":"upstream failure, api_key=%s"}`, key)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheKeyNeverAppearsInAnyResponseOrLogLine(t *testing.T) {
	const secret = "SECRETKEY0123456789abcdef0123456789"
	tmdb := fakeTMDB(t, secret)
	se := newSettingsEnv(t, func(o *metadata.SettingsOptions) {
		o.NewProvider = nil // the real client, talking to the fake TMDB
		o.BaseURL = tmdb.URL + "/3"
		o.ImageBaseURL = tmdb.URL + "/img"
	})
	var bodies []string
	collect := func(rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		bodies = append(bodies, rec.Body.String())
		for k, vs := range rec.Header() {
			bodies = append(bodies, k+": "+strings.Join(vs, ", "))
		}
		return rec
	}

	// Setting the key: refused (the fake echoes the wrong key it was sent), then accepted.
	collect(se.do("PUT", "/api/settings", `{"tmdbKey": "WRONGKEY0000000000000000000000000"}`))
	collect(se.do("PUT", "/api/settings", fmt.Sprintf(`{"tmdbKey": %q}`, secret)))
	if v := decode[SettingsView](t, se.get("/api/settings")); !v.TMDBKeySet || v.TMDBKeySource != "settings" {
		t.Fatalf("the key was not accepted: %+v", v)
	}
	// Every read that could show it.
	for _, p := range []string{"/api/settings", "/api/metadata/status", "/api/metadata/review", "/api/items", "/api/shows", "/api/genres",
		fmt.Sprintf("/api/items/%d", se.mp4), "/api/metadata/search?kind=movie&q=The+Matrix&year=1999", "/api/metadata/search?kind=series&q=x"} {
		collect(se.get(p))
	}
	// Failures that carry TMDB's own words: a movie TMDB errors on, a person.
	rec := collect(se.do("PUT", fmt.Sprintf("/api/items/%d/match", se.multi), `{"tmdbId": 603}`))
	if rec.Code != 502 {
		t.Errorf("match with TMDB failing = %d %s", rec.Code, rec.Body.String())
	}
	collect(se.get(fmt.Sprintf("/api/people/%d", se.personID)))
	collect(se.do("PUT", "/api/shows/Cool%20Show/match", `{"imdbId": "tt0944947"}`))
	// The worker runs against the same TMDB and logs its failures.
	se.worker.RunOnce(context.Background())
	collect(se.get("/api/metadata/status"))

	everything := strings.Join(bodies, "\n") + "\n" + se.logs.String()
	for _, secretPart := range []string{secret, secret[:16], secret[16:], "WRONGKEY0000", url.QueryEscape(secret)} {
		if strings.Contains(everything, secretPart) {
			i := strings.Index(everything, secretPart)
			t.Fatalf("%q appears in a response or a log line: ...%s...", secretPart, everything[max(0, i-80):min(len(everything), i+80)])
		}
	}

	// Positive controls: the capture saw the traffic, TMDB's words reached the
	// responses with the key cut out, and the key is really in use.
	logs := se.logs.String()
	if !strings.Contains(logs, "tmdb request") || !strings.Contains(logs, "/configuration") {
		t.Errorf("the log capture saw no TMDB request lines:\n%s", logs)
	}
	if !strings.Contains(everything, "***") {
		t.Errorf("no redaction marker: TMDB's echoed key never reached the API")
	}
	if got := se.settings.Resolve(context.Background()).Key(); got != secret {
		t.Errorf("the key in use is %q", got)
	}
	if stored, _, _ := se.store.GetSetting(context.Background(), metadata.SettingTMDBKey); stored != secret {
		t.Errorf("stored key = %q", stored)
	}
}

func TestTheKeyFromTheFlagIsNeverEchoedEither(t *testing.T) {
	const secret = "FLAGKEYSECRET0123456789abcdef0123"
	se := newSettingsEnv(t, func(o *metadata.SettingsOptions) { o.KeyFlag = secret })
	var all []string
	for _, c := range se.authCases() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+apiToken)
		se.srv.ServeHTTP(rec, req)
		all = append(all, rec.Body.String())
	}
	if strings.Contains(strings.Join(all, "\n")+se.logs.String(), secret) {
		t.Error("the flag's key is in a response or a log line")
	}
	// A JSON dump of the settings view has no key field at all.
	b, _ := json.Marshal(se.settingsViewForTest())
	if bytes.Contains(b, []byte("tmdbKey\"")) || bytes.Contains(b, []byte(secret)) {
		t.Errorf("settings view = %s", b)
	}
	_ = slog.Default
}

func (se *settingsEnv) settingsViewForTest() SettingsView {
	return se.srv.settingsView(context.Background())
}
