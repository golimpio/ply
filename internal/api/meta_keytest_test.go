package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ply/internal/metadata"
	"ply/internal/store"
)

// The last test of each key (tmdbKeyTestedAt and tmdbKeyTestOk, and the same for tvdb and
// mdblist) in GET /api/settings: set by a test or by a save that checks the key, and gone when
// the key in use changes.

// wantNeverTested wants the view to say a key has no test (0, false).
func wantNeverTested(t *testing.T, what string, at int64, ok bool) {
	t.Helper()
	if at != 0 || ok {
		t.Errorf("%s: testedAt %d, ok %t; want 0, false", what, at, ok)
	}
}

// wantTested wants a test made since the given time (unix seconds), passed or not.
func wantTested(t *testing.T, what string, at int64, ok bool, since int64, passed bool) {
	t.Helper()
	if at < since || at > time.Now().Unix() || ok != passed {
		t.Errorf("%s: testedAt %d, ok %t; want a test made since %d, ok %t", what, at, ok, since, passed)
	}
}

// keyTestFingerprints are the fingerprints that the settings table keeps for the tests: they must be
// there, and must not leave the server.
func keyTestFingerprints(t *testing.T, st *store.Store, want int) []string {
	t.Helper()
	rows, err := st.SettingsWithPrefix(context.Background(), "keytest_")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for name, v := range rows {
		f := strings.Fields(v)
		if len(f) != 3 || len(f[0]) != 16 {
			t.Fatalf("row %s = %q; want a 16 digit fingerprint, a time and a result", name, v)
		}
		out = append(out, f[0])
	}
	if len(out) != want {
		t.Fatalf("control: %d rows of key tests, want %d", len(out), want)
	}
	return out
}

func TestSettingsShowTheLastTestOfTMDBAndTVDB(t *testing.T) {
	ctx := context.Background()
	se := newTVDBSettingsEnv(t)
	since := time.Now().Unix()
	var bodies strings.Builder
	keep := func(rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		bodies.WriteString(rec.Body.String())
		return rec
	}
	view := func() SettingsView { return decode[SettingsView](t, keep(se.get("/api/settings"))) }
	// setStored changes a stored value behind the server's back: no check, so no test.
	setStored := func(name, value string) {
		t.Helper()
		if err := se.store.SetSetting(ctx, name, value); err != nil {
			t.Fatal(err)
		}
	}
	// test asks for a test and checks the answer keeps its shape: 200, with ok and, if not, error.
	test := func(provider string, ok bool) {
		t.Helper()
		rec := keep(se.postTest(provider))
		wantStatus(t, rec, 200)
		raw := decode[map[string]any](t, rec)
		if raw["ok"] != ok || (ok && len(raw) != 1) || (!ok && (len(raw) != 2 || raw["error"] == "")) {
			t.Errorf("the answer to a test of %s = %v; want ok %t and, if not, an error, and nothing else", provider, raw, ok)
		}
	}

	// A key that was never tested: stored, but no test (the control for every zero below).
	setStored(metadata.SettingTMDBKey, "goodkey123")
	v := view()
	if !v.TMDBKeySet {
		t.Fatal("control: the key is not set")
	}
	wantNeverTested(t, "tmdb, not tested yet", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
	wantNeverTested(t, "tvdb, no key", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)

	// It passes.
	test("tmdb", true)
	v = view()
	wantTested(t, "tmdb after a test that passed", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, since, true)
	wantNeverTested(t, "tvdb, not tested", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)

	// A key that is out of reach fails, after a key that changed had cleared the test.
	setStored(metadata.SettingTMDBKey, "downkey123")
	v = view()
	wantNeverTested(t, "tmdb, the key changed", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
	test("tmdb", false)
	v = view()
	wantTested(t, "tmdb after a test in an outage", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, since, false)

	// A key that is refused.
	setStored(metadata.SettingTMDBKey, "badkey123")
	v = view()
	wantNeverTested(t, "tmdb, the key changed again", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
	test("tmdb", false)
	v = view()
	wantTested(t, "tmdb after a refused test", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, since, false)
	tmdbAt := v.TMDBKeyTestedAt

	// TVDB logs in with the key and the PIN, and its test is for the two.
	setStored(metadata.SettingTVDBKey, apiTVDBKey)
	setStored(metadata.SettingTVDBPIN, apiTVDBPIN)
	test("tvdb", true)
	v = view()
	wantTested(t, "tvdb after a login", v.TVDBKeyTestedAt, v.TVDBKeyTestOK, since, true)
	rec := keep(se.do("PATCH", "/api/settings", `{"tvdbPin": ""}`)) // removing the PIN needs no check
	wantStatus(t, rec, 200)
	v = decode[SettingsView](t, rec)
	if !v.TVDBKeySet || v.TVDBPINSet {
		t.Fatalf("control: the key should stay and the PIN go: %+v", v)
	}
	wantNeverTested(t, "tvdb, the PIN was removed (the answer to the PATCH)", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)
	v = view()
	wantNeverTested(t, "tvdb, the PIN was removed", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)
	if v.TMDBKeyTestedAt != tmdbAt || v.TMDBKeyTestOK {
		t.Errorf("tmdb's test moved with TVDB's PIN: %d %t, want %d false", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, tmdbAt)
	}
	test("tvdb", true) // the key without a PIN
	setStored(metadata.SettingTVDBPIN, "other-pin-77")
	v = view()
	wantNeverTested(t, "tvdb, the PIN changed", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)
	test("tvdb", false) // key and PIN do not go together
	v = view()
	wantTested(t, "tvdb after a login with a PIN that is wrong", v.TVDBKeyTestedAt, v.TVDBKeyTestOK, since, false)

	// A save that checks the key and the PIN is a test, and a passing one replaces the failed one.
	rec = keep(se.do("PATCH", "/api/settings", `{"tvdbPin": "`+apiTVDBPIN+`"}`))
	wantStatus(t, rec, 200)
	v = decode[SettingsView](t, rec)
	wantTested(t, "tvdb in the answer to a save that logged in", v.TVDBKeyTestedAt, v.TVDBKeyTestOK, since, true)
	v = view()
	wantTested(t, "tvdb after a save that logged in", v.TVDBKeyTestedAt, v.TVDBKeyTestOK, since, true)

	// The fingerprints are kept (two rows) and are in nothing that was said: no answer, no log.
	for _, fp := range keyTestFingerprints(t, se.store, 2) {
		if strings.Contains(bodies.String(), fp) || strings.Contains(se.logs.String(), fp) {
			t.Errorf("the fingerprint %s of a key is in an answer or in the log", fp)
		}
	}
	for _, secret := range []string{"goodkey123", "downkey123", "badkey123", apiTVDBKey, apiTVDBPIN, "other-pin-77"} {
		if strings.Contains(bodies.String(), secret) || strings.Contains(se.logs.String(), secret) {
			t.Errorf("%q is in an answer or in the log", secret)
		}
	}
}

func TestSettingsSaveThatChecksSetsTheTestAndARefusedOneLeavesItAlone(t *testing.T) {
	se := newTVDBSettingsEnv(t)
	since := time.Now().Unix()
	view := func() SettingsView { return decode[SettingsView](t, se.get("/api/settings")) }

	wantJSONError(t, se.do("PUT", "/api/settings", `{"tmdbKey": "badkey"}`), 400)
	v := view()
	wantNeverTested(t, "tmdb after a refused save of a key that is not in use", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)

	rec := se.do("PUT", "/api/settings", `{"tmdbKey": "goodkey123"}`)
	wantStatus(t, rec, 200)
	v = decode[SettingsView](t, rec)
	if v.TMDBKeyChecked == nil || !*v.TMDBKeyChecked {
		t.Fatalf("control: the save did not check the key: %+v", v)
	}
	wantTested(t, "tmdb in the answer to a save that checked", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, since, true)
	first := view()
	wantTested(t, "tmdb after a save that checked", first.TMDBKeyTestedAt, first.TMDBKeyTestOK, since, true)

	// Another key, refused: not stored, and not a word about the key in use.
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tmdbKey": "otherkey"}`), 400)
	if v := view(); v.TMDBKeyTestedAt != first.TMDBKeyTestedAt || !v.TMDBKeyTestOK {
		t.Errorf("tmdb after a refused save of another key: %d %t, want %d true", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, first.TMDBKeyTestedAt)
	}

	// Removing the key takes its test with it.
	rec = se.do("PUT", "/api/settings", `{"tmdbKey": ""}`)
	wantStatus(t, rec, 200)
	v = decode[SettingsView](t, rec)
	if v.TMDBKeySet {
		t.Fatal("control: the key is still set")
	}
	wantNeverTested(t, "tmdb after the key was removed", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
	v = view()
	wantNeverTested(t, "tmdb, later", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
}

func TestSettingsTestOfAKeyUnderAFlagSurvivesTheStoredKeyGoing(t *testing.T) {
	ctx := context.Background()
	since := time.Now().Unix()

	// The flag's key is the one in use; a stored key under it comes and goes without touching its test.
	se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.KeyFlag = "goodkey123" })
	view := func(se *settingsEnv) SettingsView { return decode[SettingsView](t, se.get("/api/settings")) }
	wantStatus(t, se.postTest("tmdb"), 200)
	first := view(se)
	wantTested(t, "the flag's key after a test", first.TMDBKeyTestedAt, first.TMDBKeyTestOK, since, true)
	if err := se.store.SetSetting(ctx, metadata.SettingTMDBKey, "shadowed-key-1"); err != nil {
		t.Fatal(err)
	}
	if v := view(se); v.TMDBKeySource != "flag" || v.TMDBKeyTestedAt != first.TMDBKeyTestedAt || !v.TMDBKeyTestOK {
		t.Errorf("a stored key under the flag's: %+v, want the same test, source flag", v)
	}
	wantStatus(t, se.do("PUT", "/api/settings", `{"tmdbKey": ""}`), 200)
	if _, ok, _ := se.store.GetSetting(ctx, metadata.SettingTMDBKey); ok {
		t.Fatal("control: the stored key is still there")
	}
	if v := view(se); v.TMDBKeySource != "flag" || v.TMDBKeyTestedAt != first.TMDBKeyTestedAt || !v.TMDBKeyTestOK {
		t.Errorf("the stored key removed while the flag's remains: %+v, want the same test, source flag", v)
	}

	// With no flag the stored key is the one in use, and removing it takes the test.
	plain := newTVDBSettingsEnv(t)
	wantStatus(t, plain.do("PUT", "/api/settings", `{"tmdbKey": "goodkey123"}`), 200)
	v := view(plain)
	wantTested(t, "the stored key after a save that checked", v.TMDBKeyTestedAt, v.TMDBKeyTestOK, since, true)
	wantStatus(t, plain.do("PUT", "/api/settings", `{"tmdbKey": ""}`), 200)
	v = view(plain)
	wantNeverTested(t, "the stored key, removed", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
}

func TestSettingsShowTheLastTestOfTheMDBListKey(t *testing.T) {
	ctx := context.Background()
	r := newRatingsAPI(t)
	since := time.Now().Unix()
	var bodies strings.Builder
	view := func() SettingsView {
		rec := r.get("/api/settings")
		bodies.WriteString(rec.Body.String())
		return decode[SettingsView](t, rec)
	}

	v := view()
	wantNeverTested(t, "mdblist, no key", v.MDBListKeyTestedAt, v.MDBListKeyTestOK)

	// A save that checks the key.
	rec := r.patch(`{"mdblistKey": "` + apiMDBKey + `"}`)
	wantStatus(t, rec, 200)
	bodies.WriteString(rec.Body.String())
	v = decode[SettingsView](t, rec)
	if v.MDBListKeyChecked == nil || !*v.MDBListKeyChecked {
		t.Fatalf("control: the save did not check the key: %+v", v)
	}
	wantTested(t, "mdblist in the answer to a save that checked", v.MDBListKeyTestedAt, v.MDBListKeyTestOK, since, true)
	v = view()
	wantTested(t, "mdblist after a save that checked", v.MDBListKeyTestedAt, v.MDBListKeyTestOK, since, true)
	// TMDB and TVDB have nothing to say.
	wantNeverTested(t, "tmdb", v.TMDBKeyTestedAt, v.TMDBKeyTestOK)
	wantNeverTested(t, "tvdb", v.TVDBKeyTestedAt, v.TVDBKeyTestOK)

	// The key stored is changed behind the server's back to one MDBList refuses: the test is gone, and a
	// new one says it fails. An outage, too.
	for _, tc := range []struct{ key, what string }{{"wrongkey", "refused"}, {"downkey", "unreachable"}} {
		if err := r.store.SetSetting(ctx, metadata.SettingMDBList, tc.key); err != nil {
			t.Fatal(err)
		}
		v = view()
		wantNeverTested(t, "mdblist, the key changed ("+tc.what+")", v.MDBListKeyTestedAt, v.MDBListKeyTestOK)
		rec = r.postTest("mdblist")
		wantStatus(t, rec, 200)
		if got := decode[SettingsTest](t, rec); got.OK || got.Error == "" {
			t.Errorf("a test of a key that is %s = %+v", tc.what, got)
		}
		bodies.WriteString(rec.Body.String())
		v = view()
		wantTested(t, "mdblist after a test of a key that is "+tc.what, v.MDBListKeyTestedAt, v.MDBListKeyTestOK, since, false)
	}

	// The key in use saved again and refused, or out of reach: the save fails, and that is a failed test
	// of the key (changed behind the server's back first, so that there is no test before it).
	for _, tc := range []struct {
		key    string
		status int
	}{{"wrongkey", 400}, {"downkey", 502}} {
		if err := r.store.SetSetting(ctx, metadata.SettingMDBList, tc.key); err != nil {
			t.Fatal(err)
		}
		v = view()
		wantNeverTested(t, "mdblist, the key changed before a save of it ("+tc.key+")", v.MDBListKeyTestedAt, v.MDBListKeyTestOK)
		rec = r.patch(`{"mdblistKey": "` + tc.key + `"}`)
		wantJSONError(t, rec, tc.status)
		bodies.WriteString(rec.Body.String())
		v = view()
		wantTested(t, "mdblist after a save of the key in use that failed ("+tc.key+")", v.MDBListKeyTestedAt, v.MDBListKeyTestOK, since, false)
	}

	// A good key saved again: a pass.
	wantStatus(t, r.patch(`{"mdblistKey": "`+apiMDBKey+`"}`), 200)
	v = view()
	wantTested(t, "mdblist after a good key was saved", v.MDBListKeyTestedAt, v.MDBListKeyTestOK, since, true)

	// Removed.
	wantStatus(t, r.patch(`{"mdblistKey": ""}`), 200)
	v = view()
	if v.MDBListKeySet {
		t.Fatal("control: the key is still set")
	}
	wantNeverTested(t, "mdblist, the key removed", v.MDBListKeyTestedAt, v.MDBListKeyTestOK)

	// Kept as a fingerprint (one row: the removal took the other away), which is not in anything said.
	if err := r.store.SetSetting(ctx, metadata.SettingMDBList, apiMDBKey); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, r.postTest("mdblist"), 200)
	for _, fp := range keyTestFingerprints(t, r.store, 1) {
		if strings.Contains(bodies.String(), fp) || strings.Contains(r.logs.String(), fp) {
			t.Errorf("the fingerprint %s of a key is in an answer or in the log", fp)
		}
	}
	if strings.Contains(bodies.String(), apiMDBKey) || strings.Contains(r.logs.String(), apiMDBKey) {
		t.Error("the MDBList key is in an answer or in the log")
	}
}

// A result is kept in the database only when each secret it covers is stored there: with the TVDB PIN
// from a flag, the page still gets the result, and the database gets nothing to guess the PIN from.
func TestSettingsTestOfTVDBWithAPINFromAFlagIsNotWrittenToTheDatabase(t *testing.T) {
	ctx := context.Background()
	since := time.Now().Unix()

	flagged := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBPINFlag = apiTVDBPIN })
	if err := flagged.store.SetSetting(ctx, metadata.SettingTVDBKey, apiTVDBKey); err != nil {
		t.Fatal(err)
	}
	if got := decode[SettingsTest](t, flagged.postTest("tvdb")); !got.OK {
		t.Fatalf("control: the login with the flag's PIN = %+v", got)
	}
	v := decode[SettingsView](t, flagged.get("/api/settings"))
	if v.TVDBPINSource != "flag" {
		t.Fatalf("control: the PIN comes from %q", v.TVDBPINSource)
	}
	wantTested(t, "tvdb with a PIN from a flag", v.TVDBKeyTestedAt, v.TVDBKeyTestOK, since, true)
	keyTestFingerprints(t, flagged.store, 0)

	// The same with the PIN stored: the result is kept.
	stored := newTVDBSettingsEnv(t)
	for name, value := range map[string]string{metadata.SettingTVDBKey: apiTVDBKey, metadata.SettingTVDBPIN: apiTVDBPIN} {
		if err := stored.store.SetSetting(ctx, name, value); err != nil {
			t.Fatal(err)
		}
	}
	if got := decode[SettingsTest](t, stored.postTest("tvdb")); !got.OK {
		t.Fatalf("control: the login with the stored PIN = %+v", got)
	}
	keyTestFingerprints(t, stored.store, 1)
}
