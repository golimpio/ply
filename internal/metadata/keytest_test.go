package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The last test of a key: made by Test or by a save that checks, kept with a fingerprint of the key
// it was made with, and good only while that is the key in use.

func keyTestOf(t *testing.T, s *Settings, provider string) KeyTest {
	t.Helper()
	ctx := context.Background()
	return s.KeyTest(ctx, s.Resolve(ctx), provider)
}

// wantKeyTest wants a test made since the given time (unix seconds), passed or not.
func wantKeyTest(t *testing.T, got KeyTest, ok bool, since int64, what string) {
	t.Helper()
	if got.At < since || got.At > time.Now().Unix() || got.OK != ok {
		t.Errorf("%s: test = %+v; want one made since %d, ok %t", what, got, since, ok)
	}
}

func wantNoKeyTest(t *testing.T, got KeyTest, what string) {
	t.Helper()
	if got != (KeyTest{}) {
		t.Errorf("%s: test = %+v; want none (0, false)", what, got)
	}
}

// keyTestRow is the row of the settings table that keeps a provider's test.
func keyTestRow(env *settingsEnv, provider string) (string, bool) {
	env.t.Helper()
	v, ok, err := env.st.GetSetting(context.Background(), settingKeyTestPrefix+provider)
	if err != nil {
		env.t.Fatal(err)
	}
	return v, ok
}

// otherSettings is another Settings over the same store: a restart with other flags, or another
// process, or (with Disabled) a save that checks nothing.
func otherSettings(env *settingsEnv, mod func(*SettingsOptions)) *Settings {
	o := SettingsOptions{Store: env.st}
	mod(&o)
	return NewSettings(o)
}

// restarted is a Settings that has just started over the same store, with no flags: what is in the
// table is all it knows.
func restarted(env *settingsEnv) *Settings { return otherSettings(env, func(*SettingsOptions) {}) }

func tmdbRefused() error {
	return &Error{Op: "GET /configuration", Status: 401, Message: "Invalid API key"}
}

func tmdbOutage() error {
	return &Error{Op: "GET /configuration", Transient: true, Message: "connection refused"}
}

func TestKeyTestIsSetByATest(t *testing.T) {
	ctx := context.Background()
	var tvdbCheck error
	var logs bytes.Buffer
	s, env := newTVDBSettings(t, &tvdbCheck, func(o *SettingsOptions) {
		o.KeyFlag, o.TVDBKeyFlag, o.TVDBPINFlag = "tmdb-flag-key", secretKey, secretPIN
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	since := time.Now().Unix()
	for _, p := range []string{SourceTMDB, SourceTVDB} {
		wantNoKeyTest(t, keyTestOf(t, s, p), p+" before any test")
		if _, there := keyTestRow(env, p); there {
			t.Errorf("%s: a row before any test", p)
		}
	}

	// A key that works.
	if err := s.Test(ctx, SourceTMDB); err != nil {
		t.Fatalf("Test(tmdb) = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTMDB), true, since, "tmdb after a passing test")
	wantNoKeyTest(t, keyTestOf(t, s, SourceTVDB), "tvdb, which was not tested")

	// A key that is refused is a failed test, and so is a provider out of reach; each replaces
	// the one before.
	tvdbCheck = &TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"}
	if err := s.Test(ctx, SourceTVDB); !errors.Is(err, ErrTVDBRejected) {
		t.Fatalf("Test(tvdb) with a refused key = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTVDB), false, since, "tvdb after a refused test")
	env.check = tmdbOutage()
	if err := s.Test(ctx, SourceTMDB); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Test(tmdb) in an outage = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTMDB), false, since, "tmdb after a test in an outage")
	env.check, tvdbCheck = nil, nil
	for _, p := range []string{SourceTMDB, SourceTVDB} {
		if err := s.Test(ctx, p); err != nil {
			t.Fatalf("Test(%s) = %v", p, err)
		}
		wantKeyTest(t, keyTestOf(t, s, p), true, since, p+" after a test that passes again")
	}

	// Nothing was tried, so nothing is kept: no key, lookups off, a provider that is none.
	none, noneEnv := newTVDBSettings(t, &tvdbCheck)
	off, offEnv := newTVDBSettings(t, &tvdbCheck, func(o *SettingsOptions) { o.KeyFlag, o.TVDBKeyFlag, o.Disabled = "tmdb-flag-key", secretKey, true })
	for _, tc := range []struct {
		name string
		s    *Settings
		env  *settingsEnv
		p    string
		want error
	}{
		{"no tmdb key", none, noneEnv, SourceTMDB, ErrNoTMDB},
		{"no tvdb key", none, noneEnv, SourceTVDB, ErrNoTVDB},
		{"lookups off (tmdb)", off, offEnv, SourceTMDB, ErrLookupsOff},
		{"lookups off (tvdb)", off, offEnv, SourceTVDB, ErrLookupsOff},
		{"another provider", s, env, "omdb", ErrInvalidSetting},
	} {
		if err := tc.s.Test(ctx, tc.p); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Test = %v, want %v", tc.name, err, tc.want)
		}
		if row, there := keyTestRow(tc.env, tc.p); there {
			t.Errorf("%s: a row was kept: %q", tc.name, row)
		}
	}

	// A caller that went away got no answer: nothing is kept, and nothing is complained of.
	var goneLogs bytes.Buffer
	gone, _ := newTVDBSettings(t, &tvdbCheck, func(o *SettingsOptions) {
		o.KeyFlag = "tmdb-flag-key"
		o.Logger = slog.New(slog.NewTextHandler(&goneLogs, nil))
	})
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	gone.Test(cancelled, SourceTMDB)
	if got := keyTestOf(t, gone, SourceTMDB); got != (KeyTest{}) || goneLogs.Len() != 0 {
		t.Errorf("a caller that went away: test %+v, log %q", got, goneLogs.String())
	}
	if err := gone.Test(ctx, SourceTMDB); err != nil { // control: the same call, with a caller that stays
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, gone, SourceTMDB), true, since, "the same test, by a caller that stays")
	if logs.Len() != 0 {
		t.Errorf("logged: %s", logs.String())
	}
}

func TestKeyTestIsSetByASaveThatChecks(t *testing.T) {
	ctx := context.Background()
	var tvdbCheck error
	s, env := newTVDBSettings(t, &tvdbCheck)
	since := time.Now().Unix()
	str := func(v string) *string { return &v }
	const good = "goodkey123"

	if err := s.Update(ctx, Update{Key: str(good)}); err != nil {
		t.Fatal(err)
	}
	first := keyTestOf(t, s, SourceTMDB)
	wantKeyTest(t, first, true, since, "tmdb after a save that checked it")

	// A save that is refused stores nothing and says nothing of the key in use.
	env.check = tmdbRefused()
	if err := s.Update(ctx, Update{Key: str("otherkey456")}); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("Update with a refused key = %v", err)
	}
	if got := keyTestOf(t, s, SourceTMDB); got != first {
		t.Errorf("a refused save of another key: test = %+v, want the one before, %+v", got, first)
	}
	// The key in use saved again and not reachable: a failed test of it. Then it works again.
	env.check = tmdbOutage()
	if err := s.Update(ctx, Update{Key: str(good)}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Update in an outage = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTMDB), false, since, "tmdb after a save in an outage")
	env.check = nil
	if err := s.Update(ctx, Update{Key: str(good)}); err != nil {
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTMDB), true, since, "tmdb after the save that works")

	// TVDB: the key and the PIN are checked, and tested, together.
	if err := s.Update(ctx, Update{TVDBKey: str(secretKey), TVDBPIN: str(secretPIN)}); err != nil {
		t.Fatal(err)
	}
	tvdbFirst := keyTestOf(t, s, SourceTVDB)
	wantKeyTest(t, tvdbFirst, true, since, "tvdb after a save that logged in")
	tvdbCheck = &TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"}
	if err := s.Update(ctx, Update{TVDBPIN: str("wrong-pin-1")}); !errors.Is(err, ErrTVDBRejected) {
		t.Fatalf("Update with a PIN that is refused = %v", err)
	}
	if got := keyTestOf(t, s, SourceTVDB); got != tvdbFirst {
		t.Errorf("a refused PIN: test = %+v, want the one before, %+v", got, tvdbFirst)
	}
	if got := keyTestOf(t, s, SourceTMDB); !got.OK {
		t.Errorf("TVDB's refusal changed TMDB's test: %+v", got)
	}
	// The key and PIN in use saved again, with TVDB out of reach: a failed test of them.
	tvdbCheck = &TVDBError{Op: "POST /login", Transient: true, Message: "connection refused"}
	if err := s.Update(ctx, Update{TVDBKey: str(secretKey), TVDBPIN: str(secretPIN)}); !errors.Is(err, ErrTVDBUnreachable) {
		t.Fatalf("Update in an outage of TVDB = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTVDB), false, since, "tvdb after a save of the key and PIN in use in an outage")
	// ... and the same, refused, after the save that works.
	tvdbCheck = nil
	if err := s.Update(ctx, Update{TVDBKey: str(secretKey), TVDBPIN: str(secretPIN)}); err != nil {
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTVDB), true, since, "tvdb after the save that works")
	tvdbCheck = &TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"}
	if err := s.Update(ctx, Update{TVDBKey: str(secretKey), TVDBPIN: str(secretPIN)}); !errors.Is(err, ErrTVDBRejected) {
		t.Fatalf("Update with the key and PIN in use, refused = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceTVDB), false, since, "tvdb after a save of the key and PIN in use that was refused")

	// With lookups off nothing is checked, so nothing is kept.
	off, offEnv := newTVDBSettings(t, &tvdbCheck, func(o *SettingsOptions) { o.Disabled = true })
	if err := off.Update(ctx, Update{Key: str(good), TVDBKey: str(secretKey)}); err != nil {
		t.Fatal(err)
	}
	if len(offEnv.checked) != 0 {
		t.Fatalf("checked with lookups off: %q", offEnv.checked)
	}
	for _, p := range []string{SourceTMDB, SourceTVDB} {
		if row, there := keyTestRow(offEnv, p); there {
			t.Errorf("%s: a row with lookups off: %q", p, row)
		}
	}

	// A key under a flag: the save is checked and stored, but the key in use is the flag's, so the
	// check says nothing of it and must not take the place of its test.
	sh, shEnv := newTVDBSettings(t, &tvdbCheck, func(o *SettingsOptions) { o.KeyFlag = "flag-tmdb-key" })
	shEnv.check = tmdbRefused()
	if err := sh.Test(ctx, SourceTMDB); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("Test of the flag's key = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, sh, SourceTMDB), false, since, "the flag's key after a refused test")
	shEnv.check = nil
	if err := sh.Update(ctx, Update{Key: str(good)}); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := shEnv.st.GetSetting(ctx, SettingTMDBKey); !ok || v != good || sh.Resolve(ctx).KeySource != KeyFromFlag {
		t.Fatalf("control: the key was not stored under the flag's (%q, %v, %q)", v, ok, sh.Resolve(ctx).KeySource)
	}
	wantKeyTest(t, keyTestOf(t, sh, SourceTMDB), false, since, "the flag's key after a save of a key it shadows")
}

func TestKeyTestIsResetByAChangeOfTheKeyInUse(t *testing.T) {
	ctx := context.Background()
	str := func(v string) *string { return &v }
	disabled := func(o *SettingsOptions) { o.Disabled = true }

	// tmdbTested is a Settings with the stored key A, tested and passed.
	tmdbTested := func(t *testing.T, mod ...func(*SettingsOptions)) (*Settings, *settingsEnv) {
		t.Helper()
		var check error
		s, env := newTVDBSettings(t, &check, mod...)
		if err := env.st.SetSetting(ctx, SettingTMDBKey, "stored-key-A"); err != nil {
			t.Fatal(err)
		}
		if err := s.Test(ctx, SourceTMDB); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, s, SourceTMDB); got.At == 0 || !got.OK {
			t.Fatalf("control: test = %+v, want a test that passed", got)
		}
		return s, env
	}

	t.Run("a stored key replaced, and put back", func(t *testing.T) {
		s, env := tmdbTested(t)
		off := otherSettings(env, disabled)
		if err := off.Update(ctx, Update{Key: str("stored-key-B")}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTMDB), "after another key was stored")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTMDB), "after another key was stored, from the table")
		if err := off.Update(ctx, Update{Key: str("stored-key-A")}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTMDB), "after the first key was put back: the old test does not return")
	})

	t.Run("a stored key removed", func(t *testing.T) {
		s, env := tmdbTested(t)
		if err := s.Update(ctx, Update{Key: str("")}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTMDB), "after the key was removed")
		if row, there := keyTestRow(env, SourceTMDB); there {
			t.Errorf("the row is still there: %q", row)
		}
		if err := otherSettings(env, disabled).Update(ctx, Update{Key: str("stored-key-A")}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTMDB), "after the key came back")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTMDB), "after the key came back, from the table")
	})

	t.Run("a flag shadows the key at start", func(t *testing.T) {
		_, env := tmdbTested(t)
		flagged := otherSettings(env, func(o *SettingsOptions) { o.KeyFlag = "flag-key-F" })
		wantNoKeyTest(t, keyTestOf(t, flagged, SourceTMDB), "under a flag that is new")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTMDB), "without the flag again: the old test is not back")
	})

	t.Run("the key in use stays when a stored one goes under it", func(t *testing.T) {
		var check error
		s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.KeyFlag = "flag-key-F" })
		if err := s.Test(ctx, SourceTMDB); err != nil {
			t.Fatal(err)
		}
		first := keyTestOf(t, s, SourceTMDB)
		if first.At == 0 || !first.OK {
			t.Fatalf("control: test = %+v", first)
		}
		if err := s.Update(ctx, Update{Key: str("shadowed-key-S")}); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, s, SourceTMDB); got != first {
			t.Errorf("a stored key under the flag's: test = %+v, want %+v", got, first)
		}
		if err := s.Update(ctx, Update{Key: str("")}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := env.st.GetSetting(ctx, SettingTMDBKey); ok {
			t.Fatal("control: the stored key is still there")
		}
		if got := keyTestOf(t, s, SourceTMDB); got != first {
			t.Errorf("the stored key removed while the flag's remains: test = %+v, want %+v", got, first)
		}
	})

	t.Run("a stored key removed that had a key from the build under it", func(t *testing.T) {
		s, _ := tmdbTested(t, func(o *SettingsOptions) { o.KeyDefault = "build-key-D" })
		if err := s.Update(ctx, Update{Key: str("")}); err != nil {
			t.Fatal(err)
		}
		if r := s.Resolve(ctx); r.Key() != "build-key-D" || r.KeySource != KeyFromDefault {
			t.Fatalf("control: the key in use comes from %q, want the build's", r.KeySource)
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTMDB), "the build's key, which was not tested")
		if err := s.Test(ctx, SourceTMDB); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, s, SourceTMDB); got.At == 0 || !got.OK {
			t.Errorf("control: the build's key, tested: %+v", got)
		}
	})

	// tvdbTested has a TVDB key and PIN, both stored, tested and passed.
	tvdbTested := func(t *testing.T) (*Settings, *settingsEnv) {
		t.Helper()
		var check error
		s, env := newTVDBSettings(t, &check)
		for name, v := range map[string]string{SettingTVDBKey: secretKey, SettingTVDBPIN: secretPIN} {
			if err := env.st.SetSetting(ctx, name, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Test(ctx, SourceTVDB); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, s, SourceTVDB); got.At == 0 || !got.OK {
			t.Fatalf("control: test = %+v", got)
		}
		if got := keyTestOf(t, restarted(env), SourceTVDB); got.At == 0 || !got.OK {
			t.Fatalf("control: after a restart, test = %+v", got)
		}
		return s, env
	}

	t.Run("the PIN changed, and put back", func(t *testing.T) {
		s, env := tvdbTested(t)
		off := otherSettings(env, disabled)
		if err := off.Update(ctx, Update{TVDBPIN: str("other-pin-1")}); err != nil {
			t.Fatal(err)
		}
		if r := s.Resolve(ctx); r.TVDBKey() != secretKey {
			t.Fatal("control: the key changed")
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTVDB), "after the PIN changed, the key being the same")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTVDB), "after the PIN changed, from the table")
		if err := off.Update(ctx, Update{TVDBPIN: str(secretPIN)}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTVDB), "after the PIN was put back: the old test does not return")
	})

	t.Run("the PIN removed", func(t *testing.T) {
		s, env := tvdbTested(t)
		if err := s.Update(ctx, Update{TVDBPIN: str("")}); err != nil {
			t.Fatal(err)
		}
		if r := s.Resolve(ctx); r.TVDBPINSet() || r.TVDBKey() != secretKey {
			t.Fatal("control: the PIN is still set, or the key went")
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTVDB), "after the PIN was removed")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTVDB), "after the PIN was removed, from the table")
	})

	t.Run("a PIN from a flag takes the place of the stored one", func(t *testing.T) {
		_, env := tvdbTested(t)
		flagged := otherSettings(env, func(o *SettingsOptions) { o.TVDBPINFlag = "flag-pin-F" })
		wantNoKeyTest(t, keyTestOf(t, flagged, SourceTVDB), "under a PIN from a flag")
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTVDB), "without the flag again: the old test is not back")
	})

	t.Run("the TVDB key replaced, and put back", func(t *testing.T) {
		s, env := tvdbTested(t)
		off := otherSettings(env, disabled)
		if err := off.Update(ctx, Update{TVDBKey: str("stored-tvdb-key")}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, s, SourceTVDB), "after the key changed, the PIN being the same")
		if err := off.Update(ctx, Update{TVDBKey: str(secretKey)}); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, restarted(env), SourceTVDB), "after the key was put back: the old test does not return")
	})

	// A key stored under a flag's is not the key in use: the test of the flag's stands (it is in memory:
	// a key from a flag is not kept in the table with a result).
	t.Run("a stored TVDB key under the flag's", func(t *testing.T) {
		var check error
		s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBKeyFlag = secretKey })
		if err := env.st.SetSetting(ctx, SettingTVDBPIN, secretPIN); err != nil {
			t.Fatal(err)
		}
		if err := s.Test(ctx, SourceTVDB); err != nil {
			t.Fatal(err)
		}
		first := keyTestOf(t, s, SourceTVDB)
		if first.At == 0 || !first.OK {
			t.Fatalf("control: test = %+v", first)
		}
		if err := s.Update(ctx, Update{TVDBKey: str("stored-tvdb-key")}); err != nil {
			t.Fatal(err)
		}
		if r := s.Resolve(ctx); r.TVDBKeySource != KeyFromFlag {
			t.Fatalf("control: the key in use is from %q", r.TVDBKeySource)
		}
		if got := keyTestOf(t, s, SourceTVDB); got != first {
			t.Errorf("a stored key under the flag's: test = %+v, want %+v", got, first)
		}
	})
}

func TestKeyTestOfMDBList(t *testing.T) {
	ctx := t.Context()
	e := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag = "" })
	s := e.settings
	since := time.Now().Unix()
	key := mdbTestKey
	empty := ""
	works := func(w http.ResponseWriter, r *http.Request, n int) { fmt.Fprint(w, `{}`) }

	wantNoKeyTest(t, keyTestOf(t, s, SourceMDBList), "no key")
	if err := s.Update(ctx, Update{MDBListKey: &key}); err != nil {
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), true, since, "after a save that checked")

	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(401) })
	if err := s.Test(ctx, SourceMDBList); !errors.Is(err, ErrMDBListRejected) {
		t.Fatalf("Test with a refused key = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), false, since, "after a refused test")
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(503) })
	if err := s.Test(ctx, SourceMDBList); !errors.Is(err, ErrMDBListUnreachable) {
		t.Fatalf("Test in an outage = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), false, since, "after a test in an outage")
	e.reply(works)
	if err := s.Test(ctx, SourceMDBList); err != nil {
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), true, since, "after a test that passes")
	passed := keyTestOf(t, s, SourceMDBList)

	// A save of another key that MDBList refuses stores nothing and says nothing of the key in use.
	other := "OTHER0123456789"
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(401) })
	if err := s.Update(ctx, Update{MDBListKey: &other}); !errors.Is(err, ErrMDBListRejected) {
		t.Fatalf("Update with a refused key = %v", err)
	}
	if got := keyTestOf(t, s, SourceMDBList); got != passed {
		t.Errorf("a refused save of another key: test = %+v, want the one before, %+v", got, passed)
	}
	// The key in use saved again and refused, or not reachable: a failed test of it.
	if err := s.Update(ctx, Update{MDBListKey: &key}); !errors.Is(err, ErrMDBListRejected) {
		t.Fatalf("Update with the key in use, refused = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), false, since, "after a save of the key in use that was refused")
	e.reply(works)
	if err := s.Update(ctx, Update{MDBListKey: &key}); err != nil {
		t.Fatal(err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), true, since, "after the save that works")
	e.reply(func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(503) })
	if err := s.Update(ctx, Update{MDBListKey: &key}); !errors.Is(err, ErrMDBListUnreachable) {
		t.Fatalf("Update with the key in use, in an outage = %v", err)
	}
	wantKeyTest(t, keyTestOf(t, s, SourceMDBList), false, since, "after a save of the key in use in an outage")
	e.reply(works)

	if err := s.Update(ctx, Update{MDBListKey: &empty}); err != nil {
		t.Fatal(err)
	}
	wantNoKeyTest(t, keyTestOf(t, s, SourceMDBList), "after the key was removed")
	if err := NewSettings(SettingsOptions{Store: e.st, Disabled: true}).Update(ctx, Update{MDBListKey: &key}); err != nil {
		t.Fatal(err)
	}
	wantNoKeyTest(t, keyTestOf(t, s, SourceMDBList), "after the key was put back")
}

// Neither the key nor the PIN is kept, in a row or anywhere else: only a fingerprint, which does not
// go to the log either. A key or PIN from a flag is not in the table at all, and nor is a result for it.
func TestKeyTestKeepsNeitherTheKeyNorThePIN(t *testing.T) {
	ctx := context.Background()
	tmdbKey := secretKey + "-tmdb"
	for _, tc := range []struct {
		name   string
		mod    func(*SettingsOptions)
		stored map[string]string
		rows   int // rows of tests that the table should have
	}{
		{"keys in the table", func(*SettingsOptions) {}, map[string]string{SettingTMDBKey: tmdbKey, SettingTVDBKey: secretKey, SettingTVDBPIN: secretPIN}, 2},
		{"keys from flags", func(o *SettingsOptions) { o.KeyFlag, o.TVDBKeyFlag, o.TVDBPINFlag = tmdbKey, secretKey, secretPIN }, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var check error
			var logs bytes.Buffer
			s, env := newTVDBSettings(t, &check, tc.mod, func(o *SettingsOptions) {
				o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			for name, v := range tc.stored {
				if err := env.st.SetSetting(ctx, name, v); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{SourceTMDB, SourceTVDB} {
				if err := s.Test(ctx, p); err != nil {
					t.Fatal(err)
				}
				if got := keyTestOf(t, s, p); got.At == 0 || !got.OK { // the result is there, in the table or not
					t.Fatalf("control: %s: test = %+v", p, got)
				}
			}
			rows, err := env.st.SettingsWithPrefix(ctx, settingKeyTestPrefix)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.rows {
				t.Fatalf("%d rows of tests, want %d: %v", len(rows), tc.rows, rows)
			}
			shape := regexp.MustCompile(`^[0-9a-f]{16} [0-9]+ ok$`)
			want := map[string]string{SourceTMDB: fingerprint(SourceTMDB, tmdbKey), SourceTVDB: fingerprint(SourceTVDB, secretKey, secretPIN)}
			for p, fp := range want {
				if row, there := rows[settingKeyTestPrefix+p]; tc.rows > 0 && (!there || !shape.MatchString(row) || !strings.HasPrefix(row, fp+" ")) {
					t.Errorf("%s: row = %q, want the fingerprint %q, a time and ok", p, row, fp)
				}
			}
			secrets := []string{secretKey, secretPIN, tmdbKey}
			for name, v := range rows {
				for _, secret := range secrets {
					if strings.Contains(v, secret) {
						t.Errorf("setting %s holds a key or a PIN: %q", name, v)
					}
				}
			}
			if tc.stored == nil { // everything came from flags: none of it may be in the table
				all, err := env.st.SettingsWithPrefix(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				for name, v := range all {
					for _, secret := range secrets {
						if strings.Contains(v, secret) {
							t.Errorf("setting %s holds a key or a PIN from a flag: %q", name, v)
						}
					}
				}
			}
			for _, fp := range want {
				if strings.Contains(logs.String(), fp) {
					t.Errorf("a fingerprint is in the log: %s", logs.String())
				}
			}
			for _, secret := range secrets {
				if strings.Contains(logs.String(), secret) {
					t.Errorf("a key or a PIN is in the log: %s", logs.String())
				}
			}
		})
	}
}

// A result goes into the settings table only when every secret its fingerprint covers is stored there
// too: with a PIN from a flag next to a stored key, say, the fingerprint would let whoever has the
// database try PINs offline. Otherwise it is kept in memory, and a restart loses it.
func TestKeyTestIsKeptInTheTableOnlyWhenEverySecretIsStoredThere(t *testing.T) {
	ctx := context.Background()
	storedTVDB := map[string]string{SettingTVDBKey: secretKey}
	storedTVDBAndPIN := map[string]string{SettingTVDBKey: secretKey, SettingTVDBPIN: secretPIN}
	storedPIN := map[string]string{SettingTVDBPIN: secretPIN}
	for _, tc := range []struct {
		name     string
		provider string
		mod      func(*SettingsOptions)
		stored   map[string]string
		persists bool
	}{
		{"tmdb, stored", SourceTMDB, func(*SettingsOptions) {}, map[string]string{SettingTMDBKey: "stored-key-A"}, true},
		{"tmdb, flag", SourceTMDB, func(o *SettingsOptions) { o.KeyFlag = "flag-key-F" }, nil, false},
		{"tmdb, environment", SourceTMDB, func(o *SettingsOptions) { o.KeyEnv = "env-key-E" }, nil, false},
		{"tmdb, built in", SourceTMDB, func(o *SettingsOptions) { o.KeyDefault = "build-key-D" }, nil, false},
		{"tvdb, stored key", SourceTVDB, func(*SettingsOptions) {}, storedTVDB, true},
		{"tvdb, stored key and PIN", SourceTVDB, func(*SettingsOptions) {}, storedTVDBAndPIN, true},
		{"tvdb, stored key and a PIN from a flag", SourceTVDB, func(o *SettingsOptions) { o.TVDBPINFlag = "1234" }, storedTVDB, false},
		{"tvdb, stored key and a PIN from the environment", SourceTVDB, func(o *SettingsOptions) { o.TVDBPINEnv = "1234" }, storedTVDB, false},
		{"tvdb, key from a flag, stored PIN", SourceTVDB, func(o *SettingsOptions) { o.TVDBKeyFlag = "flag-key-F" }, storedPIN, false},
		{"tvdb, key from the environment", SourceTVDB, func(o *SettingsOptions) { o.TVDBKeyEnv = "env-key-E" }, nil, false},
		{"tvdb, key built in, stored PIN", SourceTVDB, func(o *SettingsOptions) { o.TVDBKeyDefault = "build-key-D" }, storedPIN, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var check error
			s, env := newTVDBSettings(t, &check, tc.mod)
			for name, v := range tc.stored {
				if err := env.st.SetSetting(ctx, name, v); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Test(ctx, tc.provider); err != nil {
				t.Fatal(err)
			}
			if got := keyTestOf(t, s, tc.provider); got.At == 0 || !got.OK {
				t.Fatalf("control: the test is not there: %+v", got)
			}
			if rows, err := env.st.SettingsWithPrefix(ctx, settingKeyTestPrefix); err != nil || (len(rows) == 1) != tc.persists || len(rows) > 1 {
				t.Errorf("rows of tests = %v (%v); want one: %t", rows, err, tc.persists)
			}
			// A restart: a new Settings over the same table, with the same flags.
			if got := keyTestOf(t, otherSettings(env, tc.mod), tc.provider); (got.At != 0) != tc.persists || got.OK != tc.persists {
				t.Errorf("after a restart: test = %+v; want it there: %t", got, tc.persists)
			}
		})
	}
}

// What is kept in the table is the result as it was, a failure as much as a pass.
func TestKeyTestKeptInTheTableSurvivesARestartWhetherItPassedOrNot(t *testing.T) {
	ctx := context.Background()
	var check error
	s, env := newTVDBSettings(t, &check)
	since := time.Now().Unix()
	if err := env.st.SetSetting(ctx, SettingTMDBKey, "stored-key-A"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		check error
		pass  bool
	}{{"refused", tmdbRefused(), false}, {"out of reach", tmdbOutage(), false}, {"working", nil, true}, {"refused again", tmdbRefused(), false}} {
		env.check = tc.check
		s.Test(ctx, SourceTMDB)
		wantKeyTest(t, keyTestOf(t, restarted(env), SourceTMDB), tc.pass, since, "after a restart, a test of a key that is "+tc.name)
	}
}

func TestKeyTestOfMDBListIsKeptInTheTableOnlyWhenStored(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		flag, env string
		persists  bool
	}{{"stored", "", "", true}, {"flag", mdbTestKey, "", false}, {"environment", "", mdbTestKey, false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRatingsEnv(t, func(so *SettingsOptions, _ *RatingsOptions) { so.MDBListKeyFlag, so.MDBListKeyEnv = tc.flag, tc.env })
			if tc.persists {
				if err := e.st.SetSetting(ctx, SettingMDBList, mdbTestKey); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.settings.Test(ctx, SourceMDBList); err != nil {
				t.Fatal(err)
			}
			if got := keyTestOf(t, e.settings, SourceMDBList); got.At == 0 || !got.OK {
				t.Fatalf("control: the test is not there: %+v", got)
			}
			if rows, err := e.st.SettingsWithPrefix(ctx, settingKeyTestPrefix); err != nil || (len(rows) == 1) != tc.persists || len(rows) > 1 {
				t.Errorf("rows of tests = %v (%v); want one: %t", rows, err, tc.persists)
			}
			restarted := NewSettings(SettingsOptions{Store: e.st, MDBListKeyFlag: tc.flag, MDBListKeyEnv: tc.env})
			if got := keyTestOf(t, restarted, SourceMDBList); (got.At != 0) != tc.persists {
				t.Errorf("after a restart: test = %+v; want it there: %t", got, tc.persists)
			}
		})
	}
}

// A row of the table that could not be the result for the key in use, because the key is not wholly
// stored there, is from another time (an earlier build kept them for any key): it is removed, by a
// read and by a test, so that no fingerprint of a flag's PIN stays in the database.
func TestKeyTestRowForASecretNotStoredThereIsRemoved(t *testing.T) {
	ctx := context.Background()
	var check error

	t.Run("seen by a read", func(t *testing.T) {
		_, env := newTVDBSettings(t, &check)
		flagged := otherSettings(env, func(o *SettingsOptions) { o.TVDBPINFlag = secretPIN })
		if err := env.st.SetSetting(ctx, SettingTVDBKey, secretKey); err != nil {
			t.Fatal(err)
		}
		row := fingerprint(SourceTVDB, secretKey, secretPIN) + " 1700000000 ok"
		if err := env.st.SetSetting(ctx, settingKeyTestPrefix+SourceTVDB, row); err != nil {
			t.Fatal(err)
		}
		wantNoKeyTest(t, keyTestOf(t, flagged, SourceTVDB), "a row for a stored key and a PIN from a flag")
		if row, there := keyTestRow(env, SourceTVDB); there {
			t.Errorf("the row is still there: %q", row)
		}
	})

	t.Run("seen by a test", func(t *testing.T) {
		s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.KeyFlag = "flag-key-F" })
		if err := env.st.SetSetting(ctx, settingKeyTestPrefix+SourceTMDB, "0123456789abcdef 1700000000 failed"); err != nil {
			t.Fatal(err)
		}
		if err := s.Test(ctx, SourceTMDB); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, s, SourceTMDB); got.At == 0 || !got.OK {
			t.Fatalf("control: the test is not there: %+v", got)
		}
		if row, there := keyTestRow(env, SourceTMDB); there {
			t.Errorf("the row is still there: %q", row)
		}
	})

	// The control: a row for a key that is stored is read, and left alone.
	t.Run("a row for a stored key stays", func(t *testing.T) {
		_, env := newTVDBSettings(t, &check)
		for name, v := range map[string]string{SettingTVDBKey: secretKey, SettingTVDBPIN: secretPIN,
			settingKeyTestPrefix + SourceTVDB: fingerprint(SourceTVDB, secretKey, secretPIN) + " 1700000000 ok"} {
			if err := env.st.SetSetting(ctx, name, v); err != nil {
				t.Fatal(err)
			}
		}
		if got := keyTestOf(t, otherSettings(env, func(*SettingsOptions) {}), SourceTVDB); got != (KeyTest{At: 1700000000, OK: true}) {
			t.Errorf("test = %+v, want the row's", got)
		}
		if _, there := keyTestRow(env, SourceTVDB); !there {
			t.Error("a row for a stored key was removed by a read")
		}
	})
}

// A row that is not exactly "<fingerprint of the key in use> <time> <ok|failed>" says nothing.
func TestKeyTestIgnoresARowThatIsNotWellFormed(t *testing.T) {
	ctx := context.Background()
	var check error
	_, env := newTVDBSettings(t, &check)
	if err := env.st.SetSetting(ctx, SettingTMDBKey, "stored-key-A"); err != nil {
		t.Fatal(err)
	}
	fp := fingerprint(SourceTMDB, "stored-key-A")
	for _, tc := range []struct {
		name, row string
		want      KeyTest
	}{
		{"a test that passed", fp + " 1700000000 ok", KeyTest{At: 1700000000, OK: true}},
		{"a test that failed", fp + " 1700000000 failed", KeyTest{At: 1700000000}},
		{"a row with nothing in it", "", KeyTest{}},
		{"the fingerprint of another key", fingerprint(SourceTMDB, "other-key") + " 1700000000 ok", KeyTest{}},
		{"the fingerprint of the key for another provider", fingerprint(SourceMDBList, "stored-key-A") + " 1700000000 ok", KeyTest{}},
		{"two fields", fp + " 1700000000", KeyTest{}},
		{"four fields", fp + " 1700000000 ok extra", KeyTest{}},
		{"time zero", fp + " 0 ok", KeyTest{}},
		{"time zero, failed", fp + " 0 failed", KeyTest{}},
		{"time before zero", fp + " -5 ok", KeyTest{}},
		{"time that is no number", fp + " soon ok", KeyTest{}},
		{"a result that is neither", fp + " 1700000000 maybe", KeyTest{}},
		{"a result in capitals", fp + " 1700000000 OK", KeyTest{}},
	} {
		if err := env.st.SetSetting(ctx, settingKeyTestPrefix+SourceTMDB, tc.row); err != nil {
			t.Fatal(err)
		}
		if got := keyTestOf(t, otherSettings(env, func(*SettingsOptions) {}), SourceTMDB); got != tc.want {
			t.Errorf("%s: test = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// gatedProvider holds the check of one key until it is let go, to make a test slow.
type gatedProvider struct {
	Provider
	key     string
	slow    string
	gate    chan struct{}
	entered chan string
}

func (g *gatedProvider) Check(context.Context) error {
	if g.key == g.slow {
		g.entered <- g.key
		<-g.gate
	}
	return nil
}

// A test of the old key that ends after the key was changed, and the new one checked and passed,
// must not take the place of the new key's result.
func TestKeyTestOfASlowTestDoesNotTakeThePlaceOfALaterOne(t *testing.T) {
	ctx := context.Background()
	gate, entered := make(chan struct{}), make(chan string, 4)
	s, env := newSettings(t, func(o *SettingsOptions) {
		o.NewProvider = func(key, lang string) Provider {
			return &gatedProvider{key: key, slow: "keyAAAA", gate: gate, entered: entered}
		}
	})
	if err := env.st.SetSetting(ctx, SettingTMDBKey, "keyAAAA"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Test(ctx, SourceTMDB) }() // a test of key A, held in flight
	<-entered
	newKey := "keyBBBB"
	if err := s.Update(ctx, Update{Key: &newKey}); err != nil { // the key in use is B now, and B's check passed
		t.Fatal(err)
	}
	before := keyTestOf(t, s, SourceTMDB)
	if before.At == 0 || !before.OK {
		t.Fatalf("control: B's test after the save = %+v", before)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := keyTestOf(t, s, SourceTMDB); got != before {
		t.Errorf("after the slow test of A ended: %+v, want B's, %+v", got, before)
	}
	if got := keyTestOf(t, otherSettings(env, func(*SettingsOptions) {}), SourceTMDB); got != before {
		t.Errorf("after the slow test of A ended, from the table: %+v, want B's, %+v", got, before)
	}
}

// An Update whose caller went away before it settled the tests does not mistake the empty answer of
// reads that failed for a change of keys: it clears nothing.
func TestKeyTestsAreLeftAloneWhenTheCallerOfAnUpdateWentAway(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	s, env := newSettings(t, func(o *SettingsOptions) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	if err := env.st.SetSetting(ctx, SettingTMDBKey, "stored-key-A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Test(ctx, SourceTMDB); err != nil {
		t.Fatal(err)
	}
	before := s.Resolve(ctx)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	s.afterUpdate(cancelled, before, map[string]string{})
	s.testMu.Lock()
	_, inMemory := s.tests[SourceTMDB]
	s.testMu.Unlock()
	if _, there := keyTestRow(env, SourceTMDB); !there || !inMemory || logs.Len() != 0 {
		t.Errorf("the result was cleared (row %t, memory %t) or something was logged: %q", there, inMemory, logs.String())
	}
}

func TestFingerprintTellsKeysApart(t *testing.T) {
	base := fingerprint(SourceTVDB, "k1", "1234")
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(base) {
		t.Errorf("fingerprint = %q, want 16 hex digits", base)
	}
	if base != fingerprint(SourceTVDB, "k1", "1234") {
		t.Error("the same key and PIN have two fingerprints")
	}
	for name, other := range map[string]string{
		"another key":              fingerprint(SourceTVDB, "k2", "1234"),
		"another PIN":              fingerprint(SourceTVDB, "k1", "1235"),
		"no PIN":                   fingerprint(SourceTVDB, "k1"),
		"another provider":         fingerprint(SourceTMDB, "k1", "1234"),
		"key and PIN run together": fingerprint(SourceTVDB, "k11234"),
	} {
		if other == base {
			t.Errorf("%s: the fingerprint is the same", name)
		}
	}
	if fingerprint(SourceTMDB, "") != "" || fingerprint(SourceTVDB, "", "1234") != "" {
		t.Error("no key has a fingerprint")
	}
	if got := (Resolved{key: "k1"}).keyFingerprint("omdb"); got != "" {
		t.Errorf("a provider that is none has a fingerprint: %q", got)
	}
}
