package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ply/internal/metadata"
	"ply/internal/store"
)

// Edits read a small JSON body: one that is bigger is refused before it is
// read, not acted on because the part that counts is valid.
func TestOversizedEditBodiesAreRefused(t *testing.T) {
	huge := func(core string) string { // valid, and with an ignored field of 2 MB
		return `{` + core + `,"padding":"` + strings.Repeat("x", 2<<20) + `"}`
	}
	small := func(core string) string { return `{` + core + `,"padding":"x"}` }

	se := newSettingsEnv(t) // a Settings, and no TMDB key
	m := newMetaEnv(t, apiToken)
	m.fake.Movies = map[int64]*store.TitleInput{
		603: {Title: store.Title{Kind: store.KindMovie, TMDBID: 603, Title: "The Matrix", Year: 1999}},
	}
	m.fake.Shows = map[int64]*store.TitleInput{
		1399: {Title: store.Title{Kind: store.KindSeries, TMDBID: 1399, Title: "Other Show", Year: 2011}},
	}
	for _, tc := range []struct {
		name, method, path, core string
		env                      *menv
		changed                  func() bool // true when the request was acted on
	}{
		{"patch a title", "PATCH", fmt.Sprintf("/api/titles/%d", m.matrixTitle), `"title":"Edited"`, m, func() bool {
			title, _ := m.store.GetTitle(context.Background(), m.matrixTitle)
			return title.Locked || title.Title != "The Matrix"
		}},
		{"change the settings", "PUT", "/api/settings", `"language":"fr-FR"`, se.menv, func() bool {
			_, ok, _ := se.store.GetSetting(context.Background(), metadata.SettingLanguage)
			return ok
		}},
		{"match a movie", "PUT", fmt.Sprintf("/api/items/%d/match", m.multi), `"tmdbId":603`, m, func() bool {
			e, _ := m.store.GetEntry(context.Background(), m.multi)
			return e.MatchState != store.MatchPending
		}},
		{"match a show", "PUT", "/api/shows/Cool%20Show/match", `"tmdbId":1399`, m, func() bool {
			sm, _ := m.store.GetSeriesMatch(context.Background(), "Cool Show")
			return sm.State == store.MatchManual
		}},
	} {
		rec := tc.env.do(tc.method, tc.path, huge(tc.core))
		wantJSONError(t, rec, 413)
		if tc.changed() {
			t.Errorf("%s: a request refused as too big was acted on", tc.name)
		}
		// Positive control: the same request with a body that fits is accepted.
		if rec := tc.env.do(tc.method, tc.path, small(tc.core)); rec.Code != 200 {
			t.Errorf("%s with a small body = %d %s", tc.name, rec.Code, rec.Body.String())
		}
		if !tc.changed() {
			t.Errorf("%s: the control request changed nothing", tc.name)
		}
	}
	// A body that is not JSON stays a 400.
	wantJSONError(t, se.do("PUT", "/api/settings", `{"language":`), 400)
}
