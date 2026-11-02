package api

import (
	"testing"

	"ply/internal/metadata"
)

// -metadata=false: a key is stored without a word to TMDB, and the answer says
// it was not checked. Not forced off, it is checked and says so.
func TestSettingsPutKeyWhileForcedOffIsStoredUncheckedAndAsksTMDBNothing(t *testing.T) {
	forced := newSettingsEnv(t, func(o *metadata.SettingsOptions) { o.Disabled = true })
	rec := forced.do("PUT", "/api/settings", `{"tmdbKey": "a-key-tmdb-would-refuse"}`)
	wantStatus(t, rec, 200)
	raw := decode[map[string]any](t, rec)
	if raw["tmdbKeyChecked"] != false || raw["tmdbKeySet"] != true || raw["tmdbKeySource"] != "settings" || raw["metadataForcedOff"] != true {
		t.Errorf("answer = %v; want the key stored, not checked, forced off", raw)
	}
	if _, there := raw["tmdbKey"]; there {
		t.Error("the key is in the answer")
	}
	forced.mu.Lock()
	built := len(forced.built)
	forced.mu.Unlock()
	if built != 0 || len(forced.fake.Calls()) != 0 {
		t.Errorf("TMDB was reached: %d providers built, calls %q", built, forced.fake.Calls())
	}
	// A change that carries no key says nothing about checking.
	if raw := decode[map[string]any](t, forced.do("PUT", "/api/settings", `{"language": "de-DE"}`)); raw["tmdbKeyChecked"] != nil {
		t.Errorf("tmdbKeyChecked = %v with no key in the request", raw["tmdbKeyChecked"])
	}
	// Removing the key is not storing one.
	if raw := decode[map[string]any](t, forced.do("PUT", "/api/settings", `{"tmdbKey": ""}`)); raw["tmdbKeyChecked"] != nil || raw["tmdbKeySet"] != false {
		t.Errorf("after removing the key: %v", raw)
	}

	// Positive control: not forced off the key goes to TMDB and is checked.
	open := newSettingsEnv(t)
	raw = decode[map[string]any](t, open.do("PUT", "/api/settings", `{"tmdbKey": "goodkey123"}`))
	if raw["tmdbKeyChecked"] != true {
		t.Errorf("answer = %v; want tmdbKeyChecked true", raw)
	}
	open.mu.Lock()
	built = len(open.built)
	open.mu.Unlock()
	if built == 0 {
		t.Error("control: the key was not checked")
	}
	wantJSONError(t, open.do("PUT", "/api/settings", `{"tmdbKey": "a-key-tmdb-would-refuse"}`), 400)
}
