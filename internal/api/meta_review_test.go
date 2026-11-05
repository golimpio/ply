package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ply/internal/metadata"
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
