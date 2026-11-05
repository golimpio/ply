package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ply/internal/metadata"
	"ply/internal/metadata/metadatatest"
	"ply/internal/store"
)

// The keys and PINs of these tests are made up.
const (
	apiTVDBKey = "goodtvdbkey-9f3a"
	apiTVDBPIN = "goodpin-77"
)

// keyedTVDB is a fake TVDB that logs in with one key, and one PIN when it has one.
type keyedTVDB struct {
	*metadatatest.Fake
	key, pin string
}

func (k *keyedTVDB) Check(ctx context.Context) error {
	switch {
	case strings.HasPrefix(k.key, "down"):
		return &metadata.TVDBError{Op: "POST /login", Transient: true, Message: "connection refused"}
	case k.key == apiTVDBKey && (k.pin == "" || k.pin == apiTVDBPIN):
		return nil
	}
	return &metadata.TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"}
}

// newTVDBSettingsEnv is the settings API with a TVDB that takes apiTVDBKey (and apiTVDBPIN).
func newTVDBSettingsEnv(t *testing.T, so ...func(*metadata.SettingsOptions)) *settingsEnv {
	t.Helper()
	var se *settingsEnv
	se = newSettingsEnv(t, append([]func(*metadata.SettingsOptions){func(o *metadata.SettingsOptions) {
		o.NewTVDB = func(key, pin, lang string) metadata.Provider {
			return &keyedTVDB{Fake: se.tvdb, key: key, pin: pin}
		}
	}}, so...)...)
	return se
}

func TestSettingsTVDBFieldsAndKeyChecks(t *testing.T) {
	se := newTVDBSettingsEnv(t)
	ctx := context.Background()
	raw := func() map[string]any { return decode[map[string]any](t, se.get("/api/settings")) }

	// The shape without a key.
	r := raw()
	if r["tvdbKeySet"] != false || r["tvdbPinSet"] != false || r["tvdbKeySource"] != "" || r["tvdbAttribution"] != tvdbAttribution ||
		r["tvdbAttributionUrl"] != "https://thetvdb.com" {
		t.Errorf("settings = %v", r)
	}
	if _, has := r["tvdbKeyChecked"]; has {
		t.Error("tvdbKeyChecked in a GET: it is in the answer to a PUT that stored a key")
	}
	if !strings.Contains(r["tvdbAttribution"].(string), "TheTVDB") {
		t.Errorf("attribution = %q", r["tvdbAttribution"])
	}

	// A key TVDB refuses: 400, nothing stored. One it cannot be asked about: 502.
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tvdbKey": "wrongkey"}`), 400)
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tvdbKey": "downkey"}`), 502)
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tvdbKey": "has a space"}`), 400)
	wantJSONError(t, se.do("PUT", "/api/settings", `{"tvdbPin": "pin;drop"}`), 400)
	if _, ok, _ := se.store.GetSetting(ctx, metadata.SettingTVDBKey); ok {
		t.Error("an unchecked key was stored")
	}

	// A good key: stored, reported with where it came from, checked.
	rec := se.do("PUT", "/api/settings", `{"tvdbKey": " `+apiTVDBKey+` "}`)
	wantStatus(t, rec, 200)
	v := decode[SettingsView](t, rec)
	if !v.TVDBKeySet || v.TVDBPINSet || v.TVDBKeySource != "settings" || v.TVDBKeyChecked == nil || !*v.TVDBKeyChecked {
		t.Errorf("after the key = %+v", v)
	}
	if stored, _, _ := se.store.GetSetting(ctx, metadata.SettingTVDBKey); stored != apiTVDBKey {
		t.Errorf("stored key = %q", stored)
	}
	if st := decode[metadata.Status](t, se.get("/api/metadata/status")); !st.TVDBKeySet || st.Shows != "tvdb" {
		t.Errorf("status = %+v", st)
	}
	// TMDB's card is untouched.
	if v.TMDBKeySet {
		t.Error("the TVDB key made the TMDB key look set")
	}

	// The PIN: checked with the key; a wrong one is refused, a right one stored.
	se2 := newTVDBSettingsEnv(t)
	wantStatus(t, se2.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`", "tvdbPin": "`+apiTVDBPIN+`"}`), 200)
	if v := decode[SettingsView](t, se2.get("/api/settings")); !v.TVDBKeySet || !v.TVDBPINSet {
		t.Errorf("key and PIN = %+v", v)
	}
	wantJSONError(t, se2.do("PATCH", "/api/settings", `{"tvdbPin": "wrongpin"}`), 400)
	if v := decode[SettingsView](t, se2.get("/api/settings")); !v.TVDBPINSet {
		t.Errorf("a refused PIN removed the stored one: %+v", v)
	}
	// "" removes each, and PATCH is PUT.
	wantStatus(t, se2.do("PATCH", "/api/settings", `{"tvdbPin": ""}`), 200)
	if v := decode[SettingsView](t, se2.get("/api/settings")); v.TVDBPINSet || !v.TVDBKeySet {
		t.Errorf("after removing the PIN = %+v", v)
	}
	rec = se2.do("PUT", "/api/settings", `{"tvdbKey": ""}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); v.TVDBKeySet || v.TVDBKeySource != "" || v.TVDBKeyChecked != nil {
		t.Errorf("after removing the key = %+v", v)
	}
	if _, ok, _ := se2.store.GetSetting(ctx, metadata.SettingTVDBKey); ok {
		t.Error("the key is still stored")
	}

	// The empty request names the new fields.
	rec = se.do("PUT", "/api/settings", `{}`)
	wantStatus(t, rec, 400)
	if !strings.Contains(rec.Body.String(), "tvdbKey") || !strings.Contains(rec.Body.String(), "tvdbPin") {
		t.Errorf("the message does not name tvdbKey and tvdbPin: %s", rec.Body)
	}
}

func TestSettingsTVDBKeyIsNeverReturnedOrLogged(t *testing.T) {
	se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.TVDBPINFlag = "flagged-pin-123" })
	var seen []string
	check := func(what string, rec interface{ String() string }) {
		seen = append(seen, rec.String())
	}
	for _, body := range []string{`{"tvdbKey": "wrongkey-zzz"}`, `{"tvdbKey": "downkey-yyy"}`, `{"tvdbKey": "` + apiTVDBKey + `", "tvdbPin": "` + apiTVDBPIN + `"}`} {
		check(body, se.do("PUT", "/api/settings", body).Body)
	}
	check("settings", se.get("/api/settings").Body)
	check("status", se.get("/api/metadata/status").Body)
	se.worker.RunOnce(context.Background())
	seen = append(seen, se.logs.String())
	all := strings.Join(seen, "\n")
	for _, secret := range []string{apiTVDBKey, apiTVDBPIN, "flagged-pin-123", "wrongkey-zzz", "downkey-yyy"} {
		if strings.Contains(all, secret) {
			t.Errorf("%q is in a response or in the log", secret)
		}
	}
	// Positive control: the capture saw the calls (a PIN flag is reported as set, not as itself).
	if !strings.Contains(all, `"tvdbPinSet":true`) && !strings.Contains(all, `"tvdbKeySet":true`) {
		t.Errorf("the captured responses do not show a key set: %.300s", all)
	}
}

func TestSettingsTVDBKeySourcesAndForcedOff(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		flag, env, stored, defaultKey, wantSource string
	}{
		{"flag", "FLAGKEY", "ENVKEY", apiTVDBKey, "DEFAULT", "flag"},
		{"env", "", "ENVKEY", apiTVDBKey, "DEFAULT", "env"},
		{"settings", "", "", apiTVDBKey, "DEFAULT", "settings"},
		{"default", "", "", "", "DEFAULT", "default"},
		{"none", "", "", "", "", ""},
	} {
		se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) {
			o.TVDBKeyFlag, o.TVDBKeyEnv, o.TVDBKeyDefault = tc.flag, tc.env, tc.defaultKey
		})
		if tc.stored != "" {
			se.store.SetSetting(context.Background(), metadata.SettingTVDBKey, tc.stored)
		}
		v := decode[SettingsView](t, se.get("/api/settings"))
		if v.TVDBKeySource != tc.wantSource || v.TVDBKeySet != (tc.wantSource != "") {
			t.Errorf("%s: %+v", tc.name, v)
		}
		// Saving a key in the UI does not take over from a flag or the environment.
		if tc.wantSource == "flag" || tc.wantSource == "env" {
			rec := se.do("PUT", "/api/settings", `{"tvdbKey": "`+apiTVDBKey+`"}`)
			wantStatus(t, rec, 200)
			if got := decode[SettingsView](t, rec); got.TVDBKeySource != tc.wantSource {
				t.Errorf("%s: after PUT the source is %q", tc.name, got.TVDBKeySource)
			}
		}
	}

	// -metadata=false: the key is stored as given, unchecked, and the answer says so.
	se := newTVDBSettingsEnv(t, func(o *metadata.SettingsOptions) { o.Disabled = true })
	rec := se.do("PUT", "/api/settings", `{"tvdbKey": "wrongkey"}`)
	wantStatus(t, rec, 200)
	if v := decode[SettingsView](t, rec); !v.TVDBKeySet || v.TVDBKeyChecked == nil || *v.TVDBKeyChecked || !v.MetadataForcedOff {
		t.Errorf("forced off: %+v", v)
	}
}

// newTVDBEnv is the API with TVDB on and the show "Test Anime" matched there, with files for
// S01E01, S01E02 and S01E03 (which is only an episode in the absolute order).
func newTVDBEnv(t *testing.T) (m *menv, titleID int64, files map[int]int64) {
	t.Helper()
	m = newMetaEnv(t, apiToken)
	m.src.SetTVDB(m.tvdb)
	ctx := context.Background()
	ep := func(s, n int, name string) store.Episode {
		return store.Episode{Season: s, Number: n, Name: name, Overview: name + " overview."}
	}
	anime := func(order string) *store.TitleInput {
		in := &store.TitleInput{Title: store.Title{Kind: store.KindSeries, TVDBID: 900001, TMDBID: 900101, Title: "Test Anime", Year: 2020,
			Source: store.SourceTVDB, EpisodeOrder: order, Poster: "tvdb:/banners/posters/900001-1.jpg", Backdrop: "tvdb:/banners/fanart/original/900001-1.jpg",
			Logo: "tvdb:/banners/clearlogo/900001.png"},
			Credits: []store.Credit{{Person: store.Person{TVDBID: 296140, Name: "Bryan Cranston", Profile: "tvdb:/banners/v4/actor/296140/photo/bryan.jpg"},
				Kind: store.CreditCast, Character: "Hero"}}}
		if order == store.OrderAbsolute {
			in.Seasons = []store.Season{{Number: 1, Poster: "tvdb:/banners/seasons/900001-abs.jpg", Episodes: []store.Episode{ep(1, 1, "Awakening"), ep(1, 2, "The Road"), ep(1, 3, "Return")}}}
		} else {
			in.Seasons = []store.Season{{Number: 1, Episodes: []store.Episode{ep(1, 1, "Awakening"), ep(1, 2, "The Road")}}, {Number: 2, Episodes: []store.Episode{ep(2, 1, "Return")}}}
		}
		return in
	}
	m.tvdb.Shows = map[int64]*store.TitleInput{900001: anime("aired")}
	m.tvdb.ShowOrders = map[metadatatest.ShowOrder]*store.TitleInput{{ID: 900001, Order: "absolute"}: anime("absolute")}
	m.tvdb.SeriesSearch = map[string][]metadata.Candidate{"test anime": {{ID: 900001, Title: "Test Anime", OriginalTitle: "Test Anime", Year: 2020, Popularity: 9,
		Poster: "tvdb:/banners/posters/900001-1.jpg"}}}
	m.tvdb.Slugs = map[string]int64{"test-anime": 900001}
	m.tvdb.People = map[int64]*store.Person{296140: {TVDBID: 296140, TMDBID: 17419, Name: "Bryan Cranston", Biography: "From TVDB."}}
	m.fake.SeriesSearch = map[string][]metadata.Candidate{"test anime": {{ID: 77, Title: "Test Anime", OriginalTitle: "Test Anime", Year: 2020, Popularity: 9}}}
	m.fake.Shows = map[int64]*store.TitleInput{77: {Title: store.Title{Kind: store.KindSeries, TMDBID: 77, Title: "Test Anime", Source: store.SourceTMDB, Overview: "From TMDB"}}}

	var err error
	titleID, err = m.store.UpsertTitle(ctx, anime("aired"))
	if err != nil {
		t.Fatal(err)
	}
	files = map[int]int64{}
	for n, se := range map[int][2]int{1: {1, 1}, 2: {1, 2}, 3: {1, 3}} {
		id, err := m.store.UpsertItem(ctx, &store.Item{Kind: store.KindEpisode, Show: "Test Anime", Season: se[0], Episode: se[1],
			Path: fmt.Sprintf("/media/Test Anime/Test Anime - %02d.mkv", n), Root: "/media", Size: 1, MTime: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = id
	}
	if err := m.store.SetSeriesMatch(ctx, "Test Anime", store.Match{TitleID: titleID, State: store.MatchMatched, Score: 1}); err != nil {
		t.Fatal(err)
	}
	return m, titleID, files
}

func TestShowFromTVDBHasSourceOrderAndTVDBImageURLs(t *testing.T) {
	m, titleID, files := newTVDBEnv(t)
	d := decode[ShowDetail](t, m.get("/api/shows/Test%20Anime"))
	if d.Meta == nil || d.Meta.Source != "tvdb" || d.Meta.EpisodeOrder != "aired" || d.Meta.TVDBID != 900001 || d.Meta.TMDBID != 900101 || d.Meta.ID != titleID {
		t.Fatalf("meta = %+v", d.Meta)
	}
	if want := withTok("/api/images/tvdb/w342/banners/posters/900001-1.jpg"); d.Poster != want {
		t.Errorf("poster = %q, want %q", d.Poster, want)
	}
	if want := withTok("/api/images/tvdb/w1280/banners/fanart/original/900001-1.jpg"); d.Backdrop != want {
		t.Errorf("backdrop = %q, want %q", d.Backdrop, want)
	}
	if want := withTok("/api/images/tvdb/w500/banners/clearlogo/900001.png"); d.Meta.Logo != want {
		t.Errorf("logo = %q, want %q", d.Meta.Logo, want)
	}
	if len(d.Meta.Cast) != 1 || d.Meta.Cast[0].Profile != withTok("/api/images/tvdb/w185/banners/v4/actor/296140/photo/bryan.jpg") {
		t.Errorf("cast = %+v", d.Meta.Cast)
	}
	// A movie has a source and no episode order.
	mv := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", m.mp4)))
	if mv.Meta == nil || mv.Meta.Source != "tmdb" || mv.Meta.EpisodeOrder != "" {
		t.Errorf("movie meta = %+v", mv.Meta)
	}
	// An episode finds its episode by season and number.
	it := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", files[2])))
	if it.EpisodeInfo == nil || it.EpisodeInfo.Name != "The Road" {
		t.Errorf("episode info = %+v", it.EpisodeInfo)
	}
	if it3 := decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", files[3]))); it3.EpisodeInfo != nil {
		t.Errorf("S01E03 has episode info in the aired order: %+v", it3.EpisodeInfo)
	}
}

func TestPatchTitleEpisodeOrderRemapsTheFiles(t *testing.T) {
	m, titleID, files := newTVDBEnv(t)
	path := fmt.Sprintf("/api/titles/%d", titleID)
	info := func(n int) *EpisodeInfo {
		return decode[ItemDetail](t, m.get(fmt.Sprintf("/api/items/%d", files[n]))).EpisodeInfo
	}

	rec := m.do("PATCH", path, `{"episodeOrder": "absolute"}`)
	wantStatus(t, rec, 200)
	d := decode[TitleDetail](t, rec)
	if d.EpisodeOrder != "absolute" || d.Source != "tvdb" || d.Locked {
		t.Errorf("detail = %+v; the order changes, the title is not locked", d)
	}
	if e := info(3); e == nil || e.Name != "Return" {
		t.Errorf("S01E03 after the change = %+v", e)
	}
	if got := m.tvdb.Calls(); got[len(got)-1] != "Series(900001, absolute)" {
		t.Errorf("TVDB calls = %q", got)
	}
	sd := decode[ShowDetail](t, m.get("/api/shows/Test%20Anime"))
	if sd.Meta.EpisodeOrder != "absolute" || sd.Seasons[0].Poster != withTok("/api/images/tvdb/w342/banners/seasons/900001-abs.jpg") {
		t.Errorf("show after = %+v / %+v", sd.Meta, sd.Seasons)
	}

	// Back to aired, together with an edit of the text: the edit locks, the order alone does not.
	rec = m.do("PATCH", path, `{"episodeOrder": "aired", "overview": "Mine."}`)
	wantStatus(t, rec, 200)
	if d := decode[TitleDetail](t, rec); d.EpisodeOrder != "aired" || !d.Locked || d.Overview != "Mine." {
		t.Errorf("detail = %+v", d)
	}
	if e := info(3); e != nil {
		t.Errorf("S01E03 is an episode again in the aired order: %+v", e)
	}

	// What it refuses.
	wantJSONError(t, m.do("PATCH", path, `{"episodeOrder": "random"}`), 400)
	wantJSONError(t, m.do("PATCH", path, `{"episodeOrder": ""}`), 400)
	wantJSONError(t, m.do("PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `{"episodeOrder": "dvd"}`), 400) // a movie
	wantJSONError(t, m.do("PATCH", fmt.Sprintf("/api/titles/%d", m.showTitle), `{"episodeOrder": "dvd"}`), 400)   // a show from TMDB
	wantJSONError(t, m.do("PATCH", "/api/titles/99999", `{"episodeOrder": "dvd"}`), 404)
	m.tvdb.Hook = func(_ context.Context, call string) error {
		if strings.Contains(call, "dvd") {
			return metadata.ErrNoSuchOrder
		}
		return nil
	}
	rec = m.do("PATCH", path, `{"episodeOrder": "dvd"}`)
	wantJSONError(t, rec, 409)
	if d := decode[TitleDetail](t, m.do("PATCH", path, `{"episodeOrder": "aired"}`)); d.EpisodeOrder != "aired" {
		t.Errorf("a refused order changed the show: %+v", d)
	}
	m.tvdb.Hook = nil
	m.tvdb.Offline()
	wantJSONError(t, m.do("PATCH", path, `{"episodeOrder": "absolute"}`), 502)
	m.tvdb.Online()
	m.src.SetTVDB(nil)
	wantJSONError(t, m.do("PATCH", path, `{"episodeOrder": "absolute"}`), 409)
	if got := m.do("PATCH", path, `{}`); got.Code != 400 || !strings.Contains(got.Body.String(), "episodeOrder") {
		t.Errorf("an empty patch: %d %s", got.Code, got.Body)
	}
}

func TestMetadataSearchCarriesTheSourceAndTakesTVDBAddresses(t *testing.T) {
	m, _, _ := newTVDBEnv(t)
	search := func(q string) *httptest.ResponseRecorder {
		return m.get("/api/metadata/search?kind=series&q=" + url.QueryEscape(q))
	}

	got := decode[[]Candidate](t, search("Test Anime"))
	if len(got) != 2 || got[0].Source != "tvdb" || got[0].ID != 900001 || got[0].TVDBID != 900001 || got[0].TMDBID != 0 ||
		got[0].Poster != withTok("/api/images/tvdb/w185/banners/posters/900001-1.jpg") ||
		got[1].Source != "tmdb" || got[1].ID != 77 || got[1].TMDBID != 77 || got[1].TVDBID != 0 {
		t.Errorf("results = %+v; want TVDB's, then TMDB's, each with its own id field", got)
	}
	// The raw JSON names the fields.
	var raw []map[string]any
	if err := json.Unmarshal(search("Test Anime").Body.Bytes(), &raw); err != nil || raw[0]["source"] != "tvdb" || raw[1]["source"] != "tmdb" {
		t.Errorf("raw = %v, %v", raw, err)
	}

	for _, q := range []string{"tvdb:900001", "TVDB: 900001", "https://thetvdb.com/series/test-anime", "thetvdb.com/series/test-anime/allseasons/official"} {
		c := decode[[]Candidate](t, search(q))
		if len(c) != 1 || c[0].Source != "tvdb" || c[0].TVDBID != 900001 || c[0].Title != "Test Anime" || c[0].Score != 1 || c[0].Year != 2020 {
			t.Errorf("search %q = %+v", q, c)
		}
	}
	wantJSONError(t, search("tvdb:424242"), 400)
	wantJSONError(t, search("https://thetvdb.com/series/no-such-show"), 400)
	// An address elsewhere is only words to search for.
	if c := decode[[]Candidate](t, search("https://example.com/series/test-anime")); len(c) != 0 {
		t.Errorf("an address at another site found %+v", c)
	}
	// No TVDB key: the address cannot be followed, and the search is TMDB's alone.
	m.src.SetTVDB(nil)
	wantJSONError(t, search("tvdb:900001"), 409)
	if c := decode[[]Candidate](t, search("Test Anime")); len(c) != 1 || c[0].Source != "tmdb" {
		t.Errorf("without TVDB: %+v", c)
	}
}

func TestFixMatchWithATVDBShow(t *testing.T) {
	m, titleID, _ := newTVDBEnv(t)
	other := func() int64 { // a show with no match
		id, err := m.store.UpsertItem(context.Background(), &store.Item{Kind: store.KindEpisode, Show: "Other Name", Season: 1, Episode: 1,
			Path: "/media/Other Name/S01E01.mkv", Root: "/media", Size: 1, MTime: 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	other()
	put := func(body string) *httptest.ResponseRecorder {
		return m.do("PUT", "/api/shows/Other%20Name/match", body)
	}

	for _, body := range []string{`{"tvdbId": 900001}`, `{"tvdb": "tvdb:900001"}`, `{"tvdb": "https://thetvdb.com/series/test-anime"}`, `{"tvdb": "thetvdb.com/series/900001"}`} {
		rec := put(body)
		wantStatus(t, rec, 200)
		d := decode[ShowDetail](t, rec)
		if d.Meta == nil || d.Meta.Source != "tvdb" || d.Meta.ID != titleID || d.MatchState != "manual" {
			t.Errorf("%s: %+v / %+v", body, d.Meta, d.MatchState)
		}
		wantStatus(t, m.do("DELETE", "/api/shows/Other%20Name/match", ""), 200)
	}
	// TMDB's pick, by hand, while TVDB is on: TMDB's.
	rec := put(`{"tmdbId": 77}`)
	wantStatus(t, rec, 200)
	if d := decode[ShowDetail](t, rec); d.Meta == nil || d.Meta.Source != "tmdb" || d.MatchState != "manual" {
		t.Errorf("tmdb pick: %+v", d.Meta)
	}

	for _, body := range []string{`{"tvdbId": 0}`, `{"tvdbId": -3}`, `{"tvdb": "banana"}`, `{"tvdb": "https://evil.example/series/test-anime"}`,
		`{"tvdbId": 900001, "tmdbId": 77}`, `{"tvdbId": 900001, "tvdb": "tvdb:900001"}`, `{"imdbId": "tt0133093", "tvdbId": 1}`, `{}`} {
		wantJSONError(t, put(body), 400)
	}
	wantJSONError(t, put(`{"tvdbId": 424242}`), 400)                  // TVDB has no such show
	wantJSONError(t, put(`{"tvdb": "thetvdb.com/series/nope"}`), 400) // no such slug

	// A movie cannot take a TVDB title.
	wantJSONError(t, m.do("PUT", fmt.Sprintf("/api/items/%d/match", m.mp4), `{"tvdbId": 900001}`), 400)
	wantJSONError(t, m.do("PUT", fmt.Sprintf("/api/items/%d/match", m.mp4), `{"tvdb": "tvdb:900001"}`), 400)

	m.src.SetTVDB(nil)
	wantJSONError(t, put(`{"tvdbId": 900001}`), 409)
}

func TestTVDBImageRouteServesOnlyPlainTVDBPaths(t *testing.T) {
	m, _, _ := newTVDBEnv(t)
	hits := m.imageHits()

	rec := m.get("/api/images/tvdb/w342/banners/posters/900001-1.jpg")
	wantStatus(t, rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Body.Len() == 0 || m.imageHits() != hits+1 {
		t.Errorf("body %d bytes, %d image requests", rec.Body.Len(), m.imageHits()-hits)
	}
	// Cached: once is enough, for this size; another size is another file.
	m.get("/api/images/tvdb/w342/banners/posters/900001-1.jpg")
	if m.imageHits() != hits+1 {
		t.Errorf("a cached image was fetched again (%d requests)", m.imageHits()-hits)
	}
	wantStatus(t, m.get("/api/images/tvdb/original/banners/posters/900001-1.jpg"), 200)

	// A TMDB path is no TVDB path, and nothing that is not plain gets as far as the cache.
	hits = m.imageHits()
	for _, tc := range []struct {
		path string
		want []int
	}{
		{"/api/images/tvdb/w999/banners/posters/1.jpg", []int{400}},
		{"/api/images/tvdb/w342/banners/posters/1.svg", []int{400}},
		{"/api/images/tvdb/w342/banners/posters/1", []int{400}},
		{"/api/images/tvdb/w342/banners/posters/a%20b.jpg", []int{400}},
		{"/api/images/tvdb/w342/banners/%2e%2e/%2e%2e/x.jpg", []int{400, 404}},
		{"/api/images/tvdb/w342/banners/..%2fx.jpg", []int{400, 404}},
		{"/api/images/tvdb/w342/%2e%2e/x.jpg", []int{400, 404}},
		{"/api/images/tvdb/w342/banners/posters/..%5cx.jpg", []int{400, 404}},
		{"/api/images/tvdb/w342/banners//posters/1.jpg", []int{301, 307, 400, 404}}, // the mux tidies a double slash
		{"/api/images/tvdb/w342/banners/../x.jpg", []int{301, 307, 400, 404}},
		{"/api/images/tvdb/w342/", []int{400, 404}},
		{"/api/images/tvdb/w342/https://evil.example/x.jpg", []int{301, 307, 400, 404}},
		{"/api/images/tvdb/w342/banners/posters/1.jpg%00.png", []int{400, 404}},
	} {
		rec := m.get(tc.path)
		ok := false
		for _, w := range tc.want {
			ok = ok || rec.Code == w
		}
		if !ok {
			t.Errorf("GET %s = %d, want one of %v", tc.path, rec.Code, tc.want)
		}
		if rec.Code == 200 || strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
			t.Errorf("GET %s served an image", tc.path)
		}
	}
	if m.imageHits() != hits {
		t.Errorf("%d requests reached the image server for paths that are not allowed", m.imageHits()-hits)
	}

	// The token is required like for everything else under /api.
	rr := httptest.NewRecorder()
	m.srv.ServeHTTP(rr, httptest.NewRequest("GET", "/api/images/tvdb/w342/banners/posters/900001-1.jpg", nil))
	if rr.Code != 401 {
		t.Errorf("without the token: %d", rr.Code)
	}

	// With the lookups off the cached image is still there, a new one is a 404.
	m.src.Set(m.fake, false)
	wantStatus(t, m.get("/api/images/tvdb/w342/banners/posters/900001-1.jpg"), 200)
	wantStatus(t, m.get("/api/images/tvdb/w342/banners/posters/900001-2.jpg"), 404)
}

func TestPersonOfTVDBIsMergedWhenTVDBGivesTheTMDBId(t *testing.T) {
	m, titleID, _ := newTVDBEnv(t)
	d, err := m.store.GetTitleDetail(context.Background(), titleID)
	if err != nil || len(d.Cast) != 1 {
		t.Fatalf("detail = %+v, %v", d, err)
	}
	tvdbPerson := d.Cast[0].PersonID
	// Keanu from the movie is another man; a Bryan Cranston by TMDB's id 17419 is added as a credit.
	m.store.UpsertTitle(context.Background(), &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 1000, Title: "Cameo"},
		Credits: []store.Credit{{Person: store.Person{TMDBID: 17419, Name: "Bryan Cranston"}, Kind: store.CreditCast}}})

	p := decode[Person](t, m.get(fmt.Sprintf("/api/people/%d", tvdbPerson)))
	if p.ID == tvdbPerson || p.TMDBID != 17419 || p.TVDBID != 296140 || p.Biography != "From TVDB." || p.Name != "Bryan Cranston" {
		t.Errorf("person = %+v; the page of the TVDB person is the merged person's", p)
	}
	// The old id is gone, and says where the person went.
	rec := m.get(fmt.Sprintf("/api/people/%d", tvdbPerson))
	wantStatus(t, rec, 404)
	if got, want := strings.TrimSpace(rec.Body.String()), fmt.Sprintf(`{"error":"merged","into":%d}`, p.ID); got != want {
		t.Errorf("the old id answers %s, want %s", got, want)
	}
}
