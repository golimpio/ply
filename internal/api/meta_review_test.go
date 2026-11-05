package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/config"
	"ply/internal/metadata"
	"ply/internal/store"
)

// The independent review of M8 (Q4 and the nits that are the API's).

func (m *menv) postTest(provider string) *httptest.ResponseRecorder {
	m.t.Helper()
	return m.do("POST", "/api/settings/test", `{"provider": "`+provider+`"}`)
}

// Q4: POST /api/settings/test checks the key in use with a request made now, and answers with
// ok or the reason; a key that does not work is an answer (200), not a failure of the request.
func TestSettingsTestChecksTheStoredKeyAndNeverEchoesIt(t *testing.T) {
	se := newTVDBSettingsEnv(t)

	// No keys yet: not ok, and the reason is the missing key.
	for _, p := range []string{"tvdb", "tmdb"} {
		rec := se.postTest(p)
		wantStatus(t, rec, 200)
		if got := decode[SettingsTest](t, rec); got.OK || !strings.Contains(got.Error, "key") {
			t.Errorf("%s with no key = %+v", p, got)
		}
	}
	if len(se.tvdb.Calls()) != 0 {
		t.Errorf("TVDB was asked: %q", se.tvdb.Calls())
	}

	// With the keys stored (checked when stored), the test is ok. A provider is named in any case.
	wantStatus(t, se.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`", "tvdbPin": "`+apiTVDBPIN+`", "tmdbKey": "goodkey123"}`), 200)
	built := len(se.built)
	for _, p := range []string{"tvdb", "tmdb", " TVDB ", "Tmdb"} {
		rec := se.postTest(p)
		wantStatus(t, rec, 200)
		got := decode[SettingsTest](t, rec)
		if !got.OK || got.Error != "" {
			t.Errorf("%q with a working key = %+v", p, got)
		}
		if strings.Contains(rec.Body.String(), apiTVDBKey) || strings.Contains(rec.Body.String(), apiTVDBPIN) || strings.Contains(rec.Body.String(), "goodkey123") {
			t.Errorf("the answer holds a key: %s", rec.Body)
		}
	}
	// The test asks the provider, now: a fresh one is built for each (a remembered token cannot answer).
	if len(se.built) <= built {
		t.Error("the test did not make a request of its own for TMDB")
	}

	// The key stops working (the stored one is changed behind the server's back, as if TVDB revoked it).
	if err := se.store.SetSetting(context.Background(), metadata.SettingTVDBKey, "revoked-key-4411"); err != nil {
		t.Fatal(err)
	}
	if err := se.store.SetSetting(context.Background(), metadata.SettingTVDBPIN, "revoked-pin-9922"); err != nil {
		t.Fatal(err)
	}
	rec := se.postTest("tvdb")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || !strings.Contains(got.Error, "TVDB") {
		t.Errorf("a revoked key = %+v", got)
	}
	for _, secret := range []string{"revoked-key-4411", "revoked-pin-9922", apiTVDBKey, apiTVDBPIN} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(se.logs.String(), secret) {
			t.Errorf("%q is in the answer or in the log", secret)
		}
	}
	// An outage is an answer too.
	if err := se.store.SetSetting(context.Background(), metadata.SettingTVDBKey, "downkey-5533"); err != nil {
		t.Fatal(err)
	}
	rec = se.postTest("tvdb")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || got.Error == "" || strings.Contains(rec.Body.String(), "downkey-5533") {
		t.Errorf("an outage = %+v / %s", got, rec.Body)
	}
	// The other provider is not touched by it.
	if got := decode[SettingsTest](t, se.postTest("tmdb")); !got.OK {
		t.Errorf("tmdb after TVDB's key went = %+v", got)
	}
}

// A key from a flag or the environment is the one tested, and with lookups off nothing is asked.
func TestSettingsTestUsesTheKeyFromTheFlagAndStaysOffWhenLookupsAre(t *testing.T) {
	se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBKeyFlag, o.TVDBPINFlag = apiTVDBKey, "flag-pin-wrong" })
	rec := se.postTest("tvdb")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || strings.Contains(rec.Body.String(), "flag-pin-wrong") || strings.Contains(rec.Body.String(), apiTVDBKey) {
		t.Errorf("a key with a wrong PIN, from the flag = %+v / %s", got, rec.Body)
	}
	good := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBKeyEnv, o.TVDBPINEnv = apiTVDBKey, apiTVDBPIN })
	if got := decode[SettingsTest](t, good.postTest("tvdb")); !got.OK {
		t.Errorf("a good key from the environment = %+v", got)
	}

	off := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBKeyFlag, o.Disabled = apiTVDBKey, true })
	rec = off.postTest("tvdb")
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || !strings.Contains(got.Error, "off") {
		t.Errorf("with lookups off = %+v", got)
	}
	if len(off.tvdb.Calls()) != 0 || len(off.built) != 0 {
		t.Errorf("a request was made with lookups off: %q %q", off.tvdb.Calls(), off.built)
	}
}

func TestSettingsTestRefusesWhatItDoesNotUnderstand(t *testing.T) {
	se := newTVDBSettingsEnv(t)
	for name, body := range map[string]string{
		"no body":     ``,
		"not json":    `provider=tvdb`,
		"no provider": `{}`,
		"empty":       `{"provider": ""}`,
		"another":     `{"provider": "imdb"}`,
		"wrong type":  `{"provider": 7}`,
		"an array":    `["tvdb"]`,
	} {
		rec := se.do("POST", "/api/settings/test", body)
		if rec.Code != 400 {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	// A key in the body is not something to test: only the key in use is, so it is not used.
	rec := se.do("POST", "/api/settings/test", `{"provider": "tvdb", "tvdbKey": "`+apiTVDBKey+`", "key": "`+apiTVDBKey+`"}`)
	wantStatus(t, rec, 200)
	if got := decode[SettingsTest](t, rec); got.OK || len(se.tvdb.Calls()) != 0 {
		t.Errorf("a key in the body was tested: %+v", got)
	}
	// Only POST.
	for _, method := range []string{"GET", "PUT", "PATCH", "DELETE"} {
		if rec := se.do(method, "/api/settings/test", `{"provider": "tvdb"}`); rec.Code != 405 && rec.Code != 404 {
			t.Errorf("%s /api/settings/test = %d", method, rec.Code)
		}
	}
	// Without a Settings there is nothing to test.
	bare := newMetaEnv(t, "", func(o *Options) { o.Settings = nil })
	wantJSONError(t, bare.do("POST", "/api/settings/test", `{"provider": "tvdb"}`), 503)
}

// It makes the server log in somewhere with a key the page cannot see: for ply's own page and native
// clients, never for a page of another site, as /api/folders is.
func TestSettingsTestIsPrivate(t *testing.T) {
	se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBKeyFlag, o.TVDBPINFlag = apiTVDBKey, apiTVDBPIN })
	call := func(hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/settings/test", strings.NewReader(`{"provider": "tvdb"}`))
		req.Header.Set("Authorization", "Bearer "+apiToken)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		se.srv.ServeHTTP(rec, req)
		return rec
	}
	// Positive control: our own page, and a client with no Origin, are served.
	for name, hdr := range map[string]map[string]string{
		"no origin": nil, "our page": {"Origin": "http://example.com"}, "same-origin": {"Sec-Fetch-Site": "same-origin"},
	} {
		if rec := call(hdr); rec.Code != 200 || !decode[SettingsTest](t, rec).OK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	before := len(se.tvdb.Calls())
	for name, hdr := range map[string]map[string]string{
		"another site": {"Origin": "http://evil.example"}, "cross-site": {"Sec-Fetch-Site": "cross-site"}, "null origin": {"Origin": "null"},
	} {
		rec := call(hdr)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS headers on a private endpoint", name)
		}
	}
	if got := len(se.tvdb.Calls()); got != before {
		t.Errorf("a refused request made %d calls", got-before)
	}
	// The preflight is not answered either.
	req := httptest.NewRequest("OPTIONS", "/api/settings/test", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	se.srv.ServeHTTP(rec, req)
	if rec.Code == 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight = %d with %v", rec.Code, rec.Header())
	}
}

// tvdbKeyChecked says a key was checked; a PIN alone is not a key.
func TestSettingsTVDBKeyCheckedIsNotSetByAPINAlone(t *testing.T) {
	se := newTVDBSettingsEnv(t)
	rec := se.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`"}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); v.TVDBKeyChecked == nil || !*v.TVDBKeyChecked {
		t.Fatalf("control: a key was stored and checked, tvdbKeyChecked = %v", v.TVDBKeyChecked)
	}
	rec = se.do("PATCH", "/api/settings", `{"tvdbPin": ""}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); v.TVDBKeyChecked != nil {
		t.Errorf("removing the PIN says the key was checked: %v", *v.TVDBKeyChecked)
	}
	se2 := newTVDBSettingsEnv(t)
	wantStatus(t, se2.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`", "tvdbPin": "`+apiTVDBPIN+`"}`), 200)
	rec = se2.do("PATCH", "/api/settings", `{"tvdbPin": "`+apiTVDBPIN+`"}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); !v.TVDBPINSet || v.TVDBKeyChecked != nil {
		t.Errorf("a PIN alone: %+v", v)
	}
}

// The order is a thing of a show from TVDB only; the answer to a change of it says how many of the
// files have no episode in the new order.
func TestEpisodeOrderIsExposedOnlyForTVDBAndPatchReportsUnmappedFiles(t *testing.T) {
	m, titleID, _ := newTVDBEnv(t)
	path := fmt.Sprintf("/api/titles/%d", titleID)

	// absolute: the three files have an episode each.
	rec := m.do("PATCH", path, `{"episodeOrder": "absolute"}`)
	wantStatus(t, rec, 200)
	raw := decode[map[string]any](t, rec)
	if n, ok := raw["unmapped"]; !ok || n != float64(0) {
		t.Errorf("unmapped after absolute = %v, %v; want 0, present", n, ok)
	}
	// aired: S01E03 has none.
	rec = m.do("PATCH", path, `{"episodeOrder": "aired"}`)
	wantStatus(t, rec, 200)
	if n, ok := decode[map[string]any](t, rec)["unmapped"]; !ok || n != float64(1) {
		t.Errorf("unmapped after aired = %v, %v; want 1", n, ok)
	}
	// A change that is not of the order says nothing of it.
	rec = m.do("PATCH", path, `{"overview": "Mine."}`)
	wantStatus(t, rec, 200)
	if _, ok := decode[map[string]any](t, rec)["unmapped"]; ok {
		t.Error("unmapped in the answer to a change of the text")
	}

	// A show from TMDB has no order to show, though the store keeps one.
	tmdb := decode[ShowDetail](t, m.get("/api/shows/Cool%20Show"))
	if tmdb.Meta == nil || tmdb.Meta.Source != "tmdb" || tmdb.Meta.EpisodeOrder != "" {
		t.Errorf("a show from TMDB: %+v", tmdb.Meta)
	}
	// And one that is taken from TVDB to TMDB by hand stops showing it.
	wantStatus(t, m.do("PATCH", path, `{"episodeOrder": "absolute"}`), 200)
	if d := decode[ShowDetail](t, m.get("/api/shows/Test%20Anime")); d.Meta.Source != "tvdb" || d.Meta.EpisodeOrder != "absolute" {
		t.Fatalf("control: %+v", d.Meta)
	}
	wantStatus(t, m.do("PUT", "/api/shows/Test%20Anime/match", `{"tmdbId": 77}`), 200)
	d := decode[ShowDetail](t, m.get("/api/shows/Test%20Anime"))
	if d.Meta == nil || d.Meta.Source != "tmdb" || d.Meta.EpisodeOrder != "" {
		t.Errorf("after the pick of TMDB's: %+v", d.Meta)
	}
}

// A merged person's old id answers 404 {"error": "merged", "into": newId}; an unknown id, and an id
// whose survivor is gone, the plain 404.
func TestPersonOldIDAnswersMergedWithTheNewID(t *testing.T) {
	m, titleID, files := newTVDBEnv(t)
	ctx := context.Background()
	d, err := m.store.GetTitleDetail(ctx, titleID)
	if err != nil || len(d.Cast) != 1 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	old := d.Cast[0].PersonID
	// A Bryan Cranston by TMDB's id 17419 is in a movie too.
	if _, err := m.store.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 1000, Title: "Cameo"},
		Credits: []store.Credit{{Person: store.Person{TMDBID: 17419, Name: "Bryan Cranston"}, Kind: store.CreditCast}}}); err != nil {
		t.Fatal(err)
	}
	path := func(id int64) string { return fmt.Sprintf("/api/people/%d", id) }

	// Before the page is opened the old id is a person.
	if _, err := m.store.GetPerson(ctx, old); err != nil {
		t.Fatalf("control: %v", err)
	}
	survivor := decode[Person](t, m.get(path(old))).ID // (opening the page merges the person)
	if survivor == old {
		t.Fatal("control: the person was not merged")
	}
	wantStatus(t, m.get(path(survivor)), 200)

	rec := m.get(path(old))
	wantStatus(t, rec, 404)
	if got, want := strings.TrimSpace(rec.Body.String()), fmt.Sprintf(`{"error":"merged","into":%d}`, survivor); got != want {
		t.Errorf("the old id answers %s, want %s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	// What the answer says is an id that answers.
	var body struct {
		Into int64 `json:"into"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	wantStatus(t, m.get(path(body.Into)), 200)

	// An id that never was a person: the plain 404.
	rec = m.get(path(99999))
	wantJSONError(t, rec, 404)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"person not found"}` {
		t.Errorf("an unknown id answers %s", got)
	}

	// The survivor goes (the library no longer has them): the alias goes with it, and the old id is a plain 404.
	if err := m.store.DeleteItems(ctx, []int64{files[1], files[2], files[3]}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.PruneMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.GetPerson(ctx, survivor); err == nil {
		t.Fatal("control: the survivor is still there")
	}
	for _, id := range []int64{old, survivor} {
		rec = m.get(path(id))
		wantJSONError(t, rec, 404)
		if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"person not found"}` {
			t.Errorf("%d after the survivor went answers %s", id, got)
		}
	}
}

// ---- tvdbPinSource ----

func TestSettingsTVDBPinSourceAndTheRemovalOfAPinAFlagGives(t *testing.T) {
	view := func(se *settingsEnv) SettingsView { return decode[SettingsView](t, se.get("/api/settings")) }

	// Where the PIN comes from, in the order the key's does; "" for none. It is a field of the answer
	// even when there is no PIN.
	for _, tc := range []struct {
		name              string
		flag, env, stored string
		want              string
	}{
		{"none", "", "", "", ""},
		{"settings", "", "", "stored-pin-1", "settings"},
		{"env beats settings", "", "env-pin-1", "stored-pin-1", "env"},
		{"flag beats the others", "flag-pin-1", "env-pin-1", "stored-pin-1", "flag"},
	} {
		se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBPINFlag, o.TVDBPINEnv = tc.flag, tc.env })
		if tc.stored != "" {
			se.store.SetSetting(context.Background(), metadata.SettingTVDBPIN, tc.stored)
		}
		rec := se.get("/api/settings")
		raw := decode[map[string]any](t, rec)
		if got, has := raw["tvdbPinSource"]; !has || got != tc.want {
			t.Errorf("%s: tvdbPinSource = %v (present %v), want %q", tc.name, got, has, tc.want)
		}
		if v := view(se); v.TVDBPINSet != (tc.want != "") {
			t.Errorf("%s: tvdbPinSet = %v", tc.name, v.TVDBPINSet)
		}
		for _, pin := range []string{tc.flag, tc.env, tc.stored} {
			if pin != "" && strings.Contains(rec.Body.String(), pin) {
				t.Errorf("%s: the PIN %q is in the answer", tc.name, pin)
			}
		}
	}

	// A PIN from a flag or the environment cannot be removed here: 409, nothing stored, whatever
	// else the request has. Storing one is allowed (the flag still beats it).
	for _, by := range []struct{ name, flag, env, msg string }{
		{"flag", "flag-pin-1", "", "-tvdb-pin"},
		{"env", "", "env-pin-1", config.TVDBPINEnv},
	} {
		se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBPINFlag, o.TVDBPINEnv = by.flag, by.env })
		ctx := context.Background()
		se.store.SetSetting(ctx, metadata.SettingTVDBPIN, "stored-pin-1")
		for _, method := range []string{"PATCH", "PUT"} {
			for _, body := range []string{`{"tvdbPin": ""}`, `{"tvdbPin": "   "}`, `{"language": "fr-FR", "tvdbPin": ""}`, `{"maxStreams": 3, "tvdbPin": ""}`} {
				rec := se.do(method, "/api/settings", body)
				wantJSONError(t, rec, 409)
				if msg := decode[map[string]string](t, rec)["error"]; !strings.Contains(msg, by.msg) || !strings.Contains(msg, "tvdbPin") ||
					strings.Contains(msg, "flag-pin-1") || strings.Contains(msg, "env-pin-1") {
					t.Errorf("%s %s %s: message %q", by.name, method, body, msg)
				}
			}
		}
		if v, ok, _ := se.store.GetSetting(ctx, metadata.SettingTVDBPIN); !ok || v != "stored-pin-1" {
			t.Errorf("%s: the stored PIN is %q, %v after refused removals", by.name, v, ok)
		}
		if v := view(se); v.Language != "en-US" || v.MaxStreams == 3 {
			t.Errorf("%s: a refused request changed other settings: %+v", by.name, v)
		}
		// Positive control: a PIN is stored (checked with the key, if there is one).
		wantStatus(t, se.do("PATCH", "/api/settings", `{"tvdbPin": "newer-pin-2"}`), 200)
		if v, _, _ := se.store.GetSetting(ctx, metadata.SettingTVDBPIN); v != "newer-pin-2" {
			t.Errorf("%s: a PIN given in the UI was not stored: %q", by.name, v)
		}
		if v := view(se); v.TVDBPINSource != by.name {
			t.Errorf("%s: source after storing one = %q; the flag or env still beats it", by.name, v.TVDBPINSource)
		}
	}

	// A PIN from the settings (or none) can be removed, as before.
	se := newTVDBSettingsEnv(t)
	se.store.SetSetting(context.Background(), metadata.SettingTVDBPIN, "stored-pin-1")
	wantStatus(t, se.do("PATCH", "/api/settings", `{"tvdbPin": ""}`), 200)
	if v := view(se); v.TVDBPINSet || v.TVDBPINSource != "" {
		t.Errorf("after removing a stored PIN: %+v", v)
	}
	wantStatus(t, se.do("PATCH", "/api/settings", `{"tvdbPin": ""}`), 200) // none: nothing to remove, nothing wrong
}

// A key that works, tested, puts the status line right at once (and the worker retries what was put off).
func TestSettingsTestOfTVDBPutsTheStatusLineRight(t *testing.T) {
	se := newTVDBSettingsEnv(t)
	ctx := context.Background()
	wantStatus(t, se.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`"}`), 200)
	line := func() string { return decode[metadata.Status](t, se.get("/api/metadata/status")).ShowsLine }
	p, _ := se.settings.Provider(ctx)
	if p == nil || p.TVDB == nil {
		t.Fatal("no TVDB provider")
	}

	// TVDB refuses the key (as it can long after it was stored): the line says so.
	se.tvdb.SetError(&metadata.TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"})
	p.TVDB.SearchSeries(ctx, "x", 0)
	if got := line(); got != "TVDB rejected the key or PIN" {
		t.Fatalf("control: status line = %q", got)
	}
	// ... and an outage is another line.
	se.tvdb.SetError(&metadata.TVDBError{Op: "GET /search", Transient: true, Message: "connection refused"})
	p.TVDB.SearchSeries(ctx, "x", 0)
	if got := line(); got != "TVDB unreachable" {
		t.Fatalf("control: status line = %q", got)
	}

	// The key is tested and works: the line is right at once, without waiting for a pass.
	se.tvdb.SetError(nil)
	if got := decode[SettingsTest](t, se.postTest("tvdb")); !got.OK {
		t.Fatalf("test = %+v", got)
	}
	if got := line(); got != "Shows: TVDB" {
		t.Errorf("status line after a test that works = %q", got)
	}

	// A test that fails does not put it right.
	se.tvdb.SetError(&metadata.TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"})
	p.TVDB.SearchSeries(ctx, "x", 0)
	se.store.SetSetting(ctx, metadata.SettingTVDBKey, "revoked-key-4411")
	if got := decode[SettingsTest](t, se.postTest("tvdb")); got.OK {
		t.Fatalf("test of a revoked key = %+v", got)
	}
	if got := line(); got != "TVDB rejected the key or PIN" {
		t.Errorf("status line after a test that fails = %q", got)
	}
}
