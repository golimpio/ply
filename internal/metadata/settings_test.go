package metadata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"ply/internal/store"
)

// stubProvider answers Check as told and nothing else.
type stubProvider struct {
	Provider
	key, language string
	check         error
	checked       *[]string
}

func (s *stubProvider) Check(context.Context) error {
	if s.checked != nil {
		*s.checked = append(*s.checked, s.key+"/"+s.language)
	}
	return s.check
}

func (s *stubProvider) ImageURL(size, file string) string { return "http://img/" + size + "/" + file }

type settingsEnv struct {
	t       *testing.T
	st      *store.Store
	built   []string // key/language of each provider built
	checked []string
	check   error // what Check returns for a new key
}

func newSettings(t *testing.T, mod ...func(*SettingsOptions)) (*Settings, *settingsEnv) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "My Data", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	env := &settingsEnv{t: t, st: st}
	o := SettingsOptions{Store: st, NewProvider: func(key, lang string) Provider {
		env.built = append(env.built, key+"/"+lang)
		return &stubProvider{key: key, language: lang, check: env.check, checked: &env.checked}
	}}
	for _, m := range mod {
		m(&o)
	}
	return NewSettings(o), env
}

func TestKeyPrecedenceIsFlagEnvSettingsDefault(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                          string
		flag, env, stored, defaultKey string
		wantKey                       string
		wantSource                    KeySource
	}{
		{"all four", "FLAG", "ENV", "STORED", "DEFAULT", "FLAG", KeyFromFlag},
		{"no flag", "", "ENV", "STORED", "DEFAULT", "ENV", KeyFromEnv},
		{"no flag or env", "", "", "STORED", "DEFAULT", "STORED", KeyFromSettings},
		{"only the build default", "", "", "", "DEFAULT", "DEFAULT", KeyFromDefault},
		{"flag and default", "FLAG", "", "", "DEFAULT", "FLAG", KeyFromFlag},
		{"env beats default", "", "ENV", "", "DEFAULT", "ENV", KeyFromEnv},
		{"stored beats default", "", "", "STORED", "DEFAULT", "STORED", KeyFromSettings},
		{"flag beats settings", "FLAG", "", "STORED", "", "FLAG", KeyFromFlag},
		{"none", "", "", "", "", "", KeyNone},
	} {
		s, env := newSettings(t, func(o *SettingsOptions) { o.KeyFlag, o.KeyEnv, o.KeyDefault = tc.flag, tc.env, tc.defaultKey })
		if tc.stored != "" {
			if err := env.st.SetSetting(ctx, SettingTMDBKey, tc.stored); err != nil {
				t.Fatal(err)
			}
		}
		r := s.Resolve(ctx)
		if r.Key() != tc.wantKey || r.KeySource != tc.wantSource || r.KeySet() != (tc.wantKey != "") {
			t.Errorf("%s: key %q source %q set %v; want %q %q", tc.name, r.Key(), r.KeySource, r.KeySet(), tc.wantKey, tc.wantSource)
		}
		p, _ := s.Provider(ctx)
		if (p != nil) != (tc.wantKey != "") {
			t.Errorf("%s: provider = %v", tc.name, p)
		}
		if p != nil && p.TMDB.(*stubProvider).key != tc.wantKey {
			t.Errorf("%s: the provider was built with %q, want %q", tc.name, p.TMDB.(*stubProvider).key, tc.wantKey)
		}
	}
}

func TestPrivacySwitchAndLanguage(t *testing.T) {
	ctx := context.Background()
	s, env := newSettings(t, func(o *SettingsOptions) { o.KeyFlag = "K"; o.Language = "pt-BR" })
	r := s.Resolve(ctx)
	if !r.Enabled || r.ForcedOff || !r.Online() || r.Language != "pt-BR" {
		t.Errorf("default: %+v", r)
	}
	// The setting turns it off and on.
	env.st.SetSetting(ctx, SettingEnabled, "false")
	if r := s.Resolve(ctx); r.Enabled || r.ForcedOff || r.Online() {
		t.Errorf("switched off in settings: %+v", r)
	}
	if p, _ := s.Provider(ctx); p != nil {
		t.Error("a provider while switched off")
	}
	if _, ok := s.ImageURL(ctx, "w342", "a.jpg"); ok {
		t.Error("an image URL while switched off")
	}
	env.st.SetSetting(ctx, SettingEnabled, "true")
	if r := s.Resolve(ctx); !r.Enabled {
		t.Errorf("switched on again: %+v", r)
	}
	// Language: the setting beats the flag; a bad stored value is ignored.
	env.st.SetSetting(ctx, SettingLanguage, "de-DE")
	if r := s.Resolve(ctx); r.Language != "de-DE" {
		t.Errorf("language = %q", r.Language)
	}
	env.st.SetSetting(ctx, SettingLanguage, "not a language")
	if r := s.Resolve(ctx); r.Language != "pt-BR" {
		t.Errorf("a malformed stored language was used: %q", r.Language)
	}

	// -metadata=false wins over everything.
	forced, fenv := newSettings(t, func(o *SettingsOptions) { o.KeyFlag = "K"; o.Disabled = true })
	fenv.st.SetSetting(ctx, SettingEnabled, "true")
	if r := forced.Resolve(ctx); r.Enabled || !r.ForcedOff || r.Online() {
		t.Errorf("forced off: %+v", r)
	}
	if p, _ := forced.Provider(ctx); p != nil {
		t.Error("a provider although -metadata=false")
	}
	if _, ok := forced.ImageURL(ctx, "w342", "a.jpg"); ok {
		t.Error("an image URL although -metadata=false")
	}
	if got := NewSettings(SettingsOptions{Store: fenv.st}).Resolve(ctx).Language; got != DefaultLanguage {
		t.Errorf("default language = %q", got)
	}
}

func TestProviderIsBuiltOncePerKeyAndLanguage(t *testing.T) {
	ctx := context.Background()
	s, env := newSettings(t)
	if p, r := s.Provider(ctx); p != nil || r.KeySet() {
		t.Fatalf("no key yet: %v %v", p, r)
	}
	env.st.SetSetting(ctx, SettingTMDBKey, "AAA")
	p1, _ := s.Provider(ctx)
	p2, _ := s.Provider(ctx)
	if p1 == nil || p1.TMDB != p2.TMDB || len(env.built) != 1 {
		t.Fatalf("built %v; want one provider shared", env.built)
	}
	// A change of key is seen at once, with no restart.
	env.st.SetSetting(ctx, SettingTMDBKey, "BBB")
	p3, r := s.Provider(ctx)
	if p3.TMDB == p1.TMDB || p3.TMDB.(*stubProvider).key != "BBB" || r.Key() != "BBB" || len(env.built) != 2 {
		t.Errorf("after a key change: %v, built %v", p3, env.built)
	}
	// And of language.
	env.st.SetSetting(ctx, SettingLanguage, "fr-FR")
	if p4, _ := s.Provider(ctx); p4.TMDB.(*stubProvider).language != "fr-FR" || len(env.built) != 3 {
		t.Errorf("after a language change: built %v", env.built)
	}
	// The key removed: no provider.
	env.st.DeleteSetting(ctx, SettingTMDBKey)
	if p, _ := s.Provider(ctx); p != nil {
		t.Error("a provider without a key")
	}
	if url, ok := s.ImageURL(ctx, "w342", "x.jpg"); !ok || url != DefaultImageBaseURL+"/w342/x.jpg" {
		t.Errorf("image URL without a key = %q, %v (images need no key)", url, ok)
	}
}

func TestUpdateValidatesBeforeItStores(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	flag := func(b bool) *bool { return &b }

	s, env := newSettings(t)
	if err := s.Update(ctx, Update{Key: str("0123456789abcdef0123456789abcdef"), Enabled: flag(true), Language: str("de-DE")}); err != nil {
		t.Fatal(err)
	}
	if got := env.checked; len(got) != 1 || got[0] != "0123456789abcdef0123456789abcdef/de-DE" {
		t.Errorf("the new key was checked as %q; want against TMDB with the new language", got)
	}
	r := s.Resolve(ctx)
	if r.Key() != "0123456789abcdef0123456789abcdef" || r.KeySource != KeyFromSettings || r.Language != "de-DE" || !r.Enabled {
		t.Errorf("after Update: %v", r)
	}

	// Refused input stores nothing, not even the parts that were fine.
	before := s.Resolve(ctx)
	for name, u := range map[string]Update{
		"key with a space":    {Key: str("abc def"), Language: str("fr-FR")},
		"key with a quote":    {Key: str(`abc"def`)},
		"key with a newline":  {Key: str("abc\ndef")},
		"key with a slash":    {Key: str("abc/def")},
		"huge key":            {Key: str(strings.Repeat("a", maxKeyLength+1))},
		"bad language":        {Language: str("english"), Enabled: flag(false)},
		"bad language 2":      {Language: str("en_US")},
		"empty language":      {Language: str("")},
		"language with space": {Language: str("en US")},
	} {
		if err := s.Update(ctx, u); !errors.Is(err, ErrInvalidSetting) {
			t.Errorf("%s: err = %v, want ErrInvalidSetting", name, err)
		}
		if after := s.Resolve(ctx); after != before {
			t.Errorf("%s: a refused update changed the settings: %v", name, after)
		}
	}
	if len(env.checked) != 1 {
		t.Errorf("TMDB was asked about keys that never passed the format check: %q", env.checked)
	}

	// TMDB refuses the key: nothing stored.
	env.check = &Error{Op: "GET /configuration", Status: 401, Message: "Invalid API key: You must be granted a valid key."}
	if err := s.Update(ctx, Update{Key: str("newkey"), Enabled: flag(false)}); !errors.Is(err, ErrKeyRejected) {
		t.Errorf("rejected key: %v", err)
	}
	if after := s.Resolve(ctx); after != before {
		t.Errorf("a rejected key changed the settings: %v", after)
	}
	// TMDB out of reach: a different error, and nothing stored either.
	env.check = &Error{Op: "GET /configuration", Transient: true, Message: "connection refused"}
	err := s.Update(ctx, Update{Key: str("newkey")})
	if !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrKeyRejected) || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("unreachable: %v", err)
	}
	if after := s.Resolve(ctx); after != before {
		t.Errorf("an unchecked key changed the settings: %v", after)
	}

	// No key in the update: no check. Empty key: removed, no check.
	env.check = errors.New("must not be called")
	env.checked = nil
	if err := s.Update(ctx, Update{Enabled: flag(false)}); err != nil || s.Resolve(ctx).Enabled {
		t.Errorf("switching off: %v", err)
	}
	if err := s.Update(ctx, Update{Key: str("  ")}); err != nil {
		t.Errorf("clearing the key: %v", err)
	}
	if r := s.Resolve(ctx); r.KeySet() || r.KeySource != KeyNone {
		t.Errorf("after clearing: %v", r)
	}
	if len(env.checked) != 0 {
		t.Errorf("checks %q for updates without a new key", env.checked)
	}
}

// -metadata=false: the key is kept, and TMDB is not asked about it.
func TestUpdateWhileForcedOffStoresTheKeyWithoutAskingTMDB(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	s, env := newSettings(t, func(o *SettingsOptions) { o.Disabled = true })
	env.check = &Error{Op: "GET /configuration", Status: 401, Message: "a key TMDB would refuse"}

	if err := s.Update(ctx, Update{Key: str("0123456789abcdef0123456789abcdef"), Language: str("de-DE")}); err != nil {
		t.Fatalf("Update while forced off: %v", err)
	}
	if len(env.checked) != 0 || len(env.built) != 0 {
		t.Errorf("TMDB was reached while forced off: checked %q, providers built %q", env.checked, env.built)
	}
	r := s.Resolve(ctx)
	if r.Key() != "0123456789abcdef0123456789abcdef" || r.KeySource != KeyFromSettings || r.Language != "de-DE" || !r.ForcedOff || r.Online() {
		t.Errorf("after Update: %v", r)
	}
	// What is local is still checked.
	if err := s.Update(ctx, Update{Key: str("has space")}); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("malformed key while forced off: %v", err)
	}

	// Positive control: the same update, not forced off, does ask.
	s2, env2 := newSettings(t)
	env2.check = &Error{Op: "GET /configuration", Status: 401, Message: "refused"}
	if err := s2.Update(ctx, Update{Key: str("0123456789abcdef0123456789abcdef")}); !errors.Is(err, ErrKeyRejected) || len(env2.checked) != 1 {
		t.Errorf("not forced off: err %v, checked %q", err, env2.checked)
	}
}

func TestUpdateStoresAShadowedKeyButTheFlagStillWins(t *testing.T) {
	ctx := context.Background()
	key := "settingskey"
	s, _ := newSettings(t, func(o *SettingsOptions) { o.KeyFlag = "flagkey" })
	if err := s.Update(ctx, Update{Key: &key}); err != nil {
		t.Fatal(err)
	}
	if r := s.Resolve(ctx); r.Key() != "flagkey" || r.KeySource != KeyFromFlag {
		t.Errorf("the flag must win: %v", r)
	}
}

func TestResolvedNeverPrintsTheKey(t *testing.T) {
	const key = "super-secret-key-1234567890"
	s, _ := newSettings(t, func(o *SettingsOptions) { o.KeyFlag = key })
	r := s.Resolve(context.Background())
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("settings", "resolved", r)
	logger.Info("settings", slog.Any("r", &r))
	jbuf := &bytes.Buffer{}
	slog.New(slog.NewJSONHandler(jbuf, nil)).Info("settings", "resolved", r)
	for _, out := range []string{
		fmt.Sprint(r), fmt.Sprintf("%v", r), fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r), fmt.Sprintf("%s", r), fmt.Sprintf("%q", r),
		fmt.Sprintf("%v", &r), fmt.Sprintf("%+v", []Resolved{r}), fmt.Sprintf("%v", map[string]Resolved{"a": r}),
		buf.String(), jbuf.String(),
	} {
		if strings.Contains(out, key) {
			t.Errorf("the key is in %q", out)
		}
	}
	// Positive control: the key is there to be used, and the output says it is set.
	if r.Key() != key || !strings.Contains(r.String(), "keySet:true") {
		t.Errorf("Key() = %q, String() = %s", r.Key(), r)
	}
	if !strings.Contains(buf.String(), "keySet=true") {
		t.Errorf("log output = %s", buf.String())
	}
}

func TestStaticSource(t *testing.T) {
	ctx := context.Background()
	src := NewStaticSource(&stubProvider{})
	if p, r := src.Provider(ctx); p == nil || !r.Online() || !r.KeySet() {
		t.Errorf("default: %v %v", p, r)
	}
	if u, ok := src.ImageURL(ctx, "w92", "a.jpg"); !ok || u != "http://img/w92/a.jpg" {
		t.Errorf("ImageURL = %q, %v", u, ok)
	}
	src.Set(nil, true)
	if p, r := src.Provider(ctx); p != nil || r.KeySet() || !r.Enabled {
		t.Errorf("no key: %v %v", p, r)
	}
	if _, ok := src.ImageURL(ctx, "w92", "a.jpg"); ok {
		t.Error("image URL without a provider")
	}
	src.Set(&stubProvider{}, false)
	if p, r := src.Provider(ctx); p != nil || r.Enabled {
		t.Errorf("switched off: %v %v", p, r)
	}
}
