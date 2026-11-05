package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// Every key and PIN below is made up. The ones in the output of the tests are looked for by
// these exact strings.
const (
	secretKey = "k3y-0f-the-project-5ecret"
	secretPIN = "P1N-5ecret-9"
)

// tvdbStub answers Check as told and nothing else.
type tvdbStub struct {
	Provider
	key, pin, language string
	check              error
	checked            *[]string
}

func (s *tvdbStub) Check(context.Context) error {
	*s.checked = append(*s.checked, s.key+"|"+s.pin+"|"+s.language)
	return s.check
}

// newTVDBSettings is newSettings with a TVDB stub: env.built lists the TVDB providers built
// as key|pin|language, env.checked what was checked, and tvdbCheck what a check answers.
func newTVDBSettings(t *testing.T, tvdbCheck *error, mod ...func(*SettingsOptions)) (*Settings, *settingsEnv) {
	t.Helper()
	var env *settingsEnv
	s, env := newSettings(t, append([]func(*SettingsOptions){func(o *SettingsOptions) {
		o.NewTVDB = func(key, pin, lang string) Provider {
			env.built = append(env.built, "tvdb:"+key+"|"+pin+"|"+lang)
			return &tvdbStub{key: key, pin: pin, language: lang, check: *tvdbCheck, checked: &env.checked}
		}
	}}, mod...)...)
	return s, env
}

func TestTVDBKeyAndPINPrecedence(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                                   string
		keyFlag, keyEnv, keyStored, keyDefault string
		pinFlag, pinEnv, pinStored             string
		wantKey, wantPIN                       string
		wantKeySource                          KeySource
	}{
		{"all four keys, all three PINs", "FLAG", "ENV", "STORED", "DEFAULT", "pFLAG", "pENV", "pSTORED", "FLAG", "pFLAG", KeyFromFlag},
		{"no flag", "", "ENV", "STORED", "DEFAULT", "", "pENV", "pSTORED", "ENV", "pENV", KeyFromEnv},
		{"no flag or env", "", "", "STORED", "DEFAULT", "", "", "pSTORED", "STORED", "pSTORED", KeyFromSettings},
		{"only the build's key", "", "", "", "DEFAULT", "", "", "", "DEFAULT", "", KeyFromDefault},
		{"build key and the user's PIN", "", "", "", "DEFAULT", "", "", "pSTORED", "DEFAULT", "pSTORED", KeyFromDefault},
		{"stored beats the build's", "", "", "STORED", "DEFAULT", "", "", "", "STORED", "", KeyFromSettings},
		{"flag key, stored PIN", "FLAG", "", "", "", "", "", "pSTORED", "FLAG", "pSTORED", KeyFromFlag},
		{"PIN from the flag beats the stored one", "", "", "STORED", "", "pFLAG", "", "pSTORED", "STORED", "pFLAG", KeyFromSettings},
		{"nothing", "", "", "", "", "", "", "", "", "", KeyNone},
	} {
		var check error
		s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) {
			o.TVDBKeyFlag, o.TVDBKeyEnv, o.TVDBKeyDefault, o.TVDBPINFlag, o.TVDBPINEnv = tc.keyFlag, tc.keyEnv, tc.keyDefault, tc.pinFlag, tc.pinEnv
		})
		if tc.keyStored != "" {
			env.st.SetSetting(ctx, SettingTVDBKey, tc.keyStored)
		}
		if tc.pinStored != "" {
			env.st.SetSetting(ctx, SettingTVDBPIN, tc.pinStored)
		}
		r := s.Resolve(ctx)
		if r.TVDBKey() != tc.wantKey || r.TVDBPIN() != tc.wantPIN || r.TVDBKeySource != tc.wantKeySource ||
			r.TVDBKeySet() != (tc.wantKey != "") || r.TVDBPINSet() != (tc.wantPIN != "") {
			t.Errorf("%s: key %q pin %q source %q; want %q %q %q", tc.name, r.TVDBKey(), r.TVDBPIN(), r.TVDBKeySource, tc.wantKey, tc.wantPIN, tc.wantKeySource)
		}
		p, _ := s.Provider(ctx)
		if (p.HasTVDB()) != (tc.wantKey != "") {
			t.Errorf("%s: TVDB provider = %v", tc.name, p.HasTVDB())
		}
		if p.HasTVDB() {
			got := p.TVDB.(*healthProvider).Provider.(*tvdbStub)
			if got.key != tc.wantKey || got.pin != tc.wantPIN {
				t.Errorf("%s: the provider was built with %q / %q, want %q / %q", tc.name, got.key, got.pin, tc.wantKey, tc.wantPIN)
			}
		}
		// TMDB's key is not mixed up with TVDB's.
		if r.KeySet() {
			t.Errorf("%s: a TMDB key appeared: %v", tc.name, r)
		}
	}
}

// TVDB alone is enough to be online; a provider is built once per key, PIN and language.
func TestTVDBProviderIsBuiltOncePerKeyPINAndLanguage(t *testing.T) {
	ctx := context.Background()
	var check error
	s, env := newTVDBSettings(t, &check)
	if r := s.Resolve(ctx); r.Online() {
		t.Fatal("online with no key")
	}
	env.st.SetSetting(ctx, SettingTVDBKey, "AAA")
	p1, r := s.Provider(ctx)
	p2, _ := s.Provider(ctx)
	if !r.Online() || p1.TMDB != nil || p1.TVDB != p2.TVDB || len(env.built) != 1 {
		t.Fatalf("one TVDB key: %+v, %+v, built %v", p1, r, env.built)
	}
	env.st.SetSetting(ctx, SettingTVDBPIN, "PPP")
	s.Provider(ctx)
	env.st.SetSetting(ctx, SettingLanguage, "pt-BR")
	s.Provider(ctx)
	if want := []string{"tvdb:AAA||en-US", "tvdb:AAA|PPP|en-US", "tvdb:AAA|PPP|pt-BR"}; !equalStrings(env.built, want) {
		t.Errorf("built %q, want %q", env.built, want)
	}
	env.st.DeleteSetting(ctx, SettingTVDBKey)
	if p, _ := s.Provider(ctx); p != nil {
		t.Error("a provider without a key")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTVDBUpdateChecksTheKeyBeforeItStoresAnything(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	var check error
	s, env := newTVDBSettings(t, &check)
	stored := func(key string) (string, bool) {
		v, ok, _ := env.st.GetSetting(ctx, key)
		return v, ok
	}

	// A key that works is stored, after a check that used the key and the PIN that will go with it.
	if err := s.Update(ctx, Update{TVDBKey: str("  " + secretKey + " "), TVDBPIN: str(secretPIN)}); err != nil {
		t.Fatal(err)
	}
	if want := []string{secretKey + "|" + secretPIN + "|en-US"}; !equalStrings(env.checked, want) {
		t.Errorf("checked %q, want %q (once, with the PIN of the same request)", env.checked, want)
	}
	if v, ok := stored(SettingTVDBKey); !ok || v != secretKey {
		t.Errorf("stored key = %q, %v", v, ok)
	}
	if v, ok := stored(SettingTVDBPIN); !ok || v != secretPIN {
		t.Errorf("stored PIN = %q, %v", v, ok)
	}

	// A key TVDB refuses is not stored, and neither is anything else in the request.
	env.checked = nil
	check = &TVDBError{Op: "POST /login", Status: 401, Message: tvdbRejected}
	err := s.Update(ctx, Update{TVDBKey: str("badkey"), Language: str("de-DE"), Enabled: ptrBool(false)})
	if !errors.Is(err, ErrTVDBRejected) {
		t.Fatalf("err = %v, want ErrTVDBRejected", err)
	}
	if v, _ := stored(SettingTVDBKey); v != secretKey {
		t.Errorf("a refused key replaced the stored one: %q", v)
	}
	if _, ok := stored(SettingLanguage); ok {
		t.Error("the language was stored although the key was refused")
	}
	if _, ok := stored(SettingEnabled); ok {
		t.Error("the privacy switch was stored although the key was refused")
	}
	// Control: the same request with a key that works stores all of it.
	check = nil
	if err := s.Update(ctx, Update{TVDBKey: str("goodkey"), Language: str("de-DE")}); err != nil {
		t.Fatal(err)
	}
	if v, _ := stored(SettingTVDBKey); v != "goodkey" {
		t.Errorf("key = %q", v)
	}
	if v, _ := stored(SettingLanguage); v != "de-DE" {
		t.Errorf("language = %q", v)
	}

	// TVDB out of reach: not a refusal, and nothing stored.
	check = &TVDBError{Op: "POST /login", Transient: true, Message: "timed out"}
	err = s.Update(ctx, Update{TVDBKey: str("other")})
	if !errors.Is(err, ErrTVDBUnreachable) || errors.Is(err, ErrTVDBRejected) || errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want ErrTVDBUnreachable (and not TMDB's)", err)
	}
	if v, _ := stored(SettingTVDBKey); v != "goodkey" {
		t.Errorf("key = %q after an unreachable check", v)
	}

	// A bad TVDB key stops a good TMDB key in the same request: both or neither.
	check = &TVDBError{Op: "POST /login", Status: 401, Message: tvdbRejected}
	if err := s.Update(ctx, Update{Key: str("0123456789abcdef"), TVDBKey: str("badkey")}); !errors.Is(err, ErrTVDBRejected) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := stored(SettingTMDBKey); ok {
		t.Error("the TMDB key was stored although the TVDB key was refused")
	}
	check = nil

	// "" removes, and asks TVDB nothing.
	env.checked = nil
	if err := s.Update(ctx, Update{TVDBKey: str(""), TVDBPIN: str("")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored(SettingTVDBKey); ok {
		t.Error("the key was not removed")
	}
	if _, ok := stored(SettingTVDBPIN); ok {
		t.Error("the PIN was not removed")
	}
	if len(env.checked) != 0 {
		t.Errorf("removing checked %q", env.checked)
	}

	// A PIN alone is checked with the key it will go with; with no key there is nothing to check it against.
	if err := s.Update(ctx, Update{TVDBPIN: str("lonely")}); err != nil || len(env.checked) != 0 {
		t.Errorf("a PIN with no key: %v, checked %q", err, env.checked)
	}
	env.st.SetSetting(ctx, SettingTVDBKey, "keyonly")
	env.checked = nil
	if err := s.Update(ctx, Update{TVDBPIN: str("newpin")}); err != nil || !equalStrings(env.checked, []string{"keyonly|newpin|de-DE"}) {
		t.Errorf("a PIN with a key: %v, checked %q", err, env.checked)
	}
	// Taking the PIN away leaves the key; that is not checked.
	env.checked = nil
	if err := s.Update(ctx, Update{TVDBPIN: str("")}); err != nil || len(env.checked) != 0 {
		t.Errorf("removing the PIN: %v, checked %q", err, env.checked)
	}
}

func ptrBool(b bool) *bool { return &b }

func TestTVDBUpdateRefusesMalformedKeysAndPINs(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	var check error
	s, env := newTVDBSettings(t, &check)
	for _, u := range []Update{
		{TVDBKey: str("has space")}, {TVDBKey: str("semi;colon")}, {TVDBKey: str("quote\"")}, {TVDBKey: str(strings.Repeat("a", maxKeyLength+1))},
		{TVDBPIN: str("pin with space")}, {TVDBPIN: str("pin/slash")}, {TVDBPIN: str("pin\nline")},
	} {
		err := s.Update(ctx, u)
		if !errors.Is(err, ErrInvalidSetting) {
			t.Errorf("%+v: err = %v", redacted(u), err)
		}
		if err != nil && (strings.Contains(err.Error(), "space") && false) {
			t.Error("unreachable")
		}
	}
	if len(env.checked) != 0 || len(env.built) != 0 {
		t.Errorf("a malformed key reached the network: checked %q", env.checked)
	}
	if _, ok, _ := env.st.GetSetting(ctx, SettingTVDBKey); ok {
		t.Error("a malformed key was stored")
	}
}

func redacted(u Update) string {
	return fmt.Sprintf("key set %v, PIN set %v", u.TVDBKey != nil, u.TVDBPIN != nil)
}

// With -metadata=false nothing may leave the machine: a key is stored as given, unchecked.
func TestTVDBUpdateWhileForcedOffChecksNothing(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	check := errors.New("must not be called")
	s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.Disabled = true })
	if err := s.Update(ctx, Update{TVDBKey: str("anykey"), TVDBPIN: str("anypin")}); err != nil {
		t.Fatal(err)
	}
	if len(env.checked) != 0 || len(env.built) != 0 {
		t.Errorf("checked %q, built %q while forced off", env.checked, env.built)
	}
	if v, ok, _ := env.st.GetSetting(ctx, SettingTVDBKey); !ok || v != "anykey" {
		t.Errorf("key = %q, %v", v, ok)
	}
	// The format is still checked.
	if err := s.Update(ctx, Update{TVDBKey: str("not valid")}); !errors.Is(err, ErrInvalidSetting) {
		t.Errorf("err = %v", err)
	}
	if p, _ := s.Provider(ctx); p != nil {
		t.Error("a provider although -metadata=false")
	}
}

// A TVDB key where there was none, or taking the key away, makes the worker move the shows
// over again. The same key entered again, a PIN, and one key replaced by another do not: it is
// the same TVDB, and the shows a person picked at TMDB while it was on stay where they are.
func TestTVDBUpdateReArmsTheMoveOnlyFromNoKeyToAKey(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	var check error
	s, env := newTVDBSettings(t, &check)
	mark := func() {
		env.st.SetSetting(ctx, SettingTVDBSwitched, "1")
		env.st.SetSetting(ctx, SettingTVDBSwitchUpTo, "7")
	}
	marked := func() bool {
		_, a, _ := env.st.GetSetting(ctx, SettingTVDBSwitched)
		_, b, _ := env.st.GetSetting(ctx, SettingTVDBSwitchUpTo)
		return a || b
	}
	mark()
	if !marked() {
		t.Fatal("control: the notes were not written")
	}
	if err := s.Update(ctx, Update{TVDBKey: str("first")}); err != nil || marked() {
		t.Fatalf("a key where there was none did not re-arm the move: %v, marked %v", err, marked())
	}
	mark()
	if err := s.Update(ctx, Update{TVDBKey: str("first")}); err != nil || !marked() {
		t.Errorf("the same key again re-armed the move: %v, marked %v", err, marked())
	}
	if err := s.Update(ctx, Update{TVDBPIN: str("pin")}); err != nil || !marked() {
		t.Errorf("a PIN re-armed the move: %v, marked %v", err, marked())
	}
	if err := s.Update(ctx, Update{TVDBKey: str("second")}); err != nil || !marked() {
		t.Errorf("one key replaced by another re-armed the move: %v, marked %v", err, marked())
	}
	if err := s.Update(ctx, Update{TVDBKey: str("")}); err != nil || marked() {
		t.Errorf("removing the key did not re-arm the move: %v, marked %v", err, marked())
	}
	mark()
	if err := s.Update(ctx, Update{TVDBKey: str("third")}); err != nil || marked() {
		t.Errorf("a key after none did not re-arm the move: %v, marked %v", err, marked())
	}
}

// Neither the key nor the PIN is printed or logged by anything that holds them.
func TestTVDBSecretsAreNeverPrintedByTheSettings(t *testing.T) {
	ctx := context.Background()
	var check error
	s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) {
		o.TVDBKeyFlag, o.TVDBPINFlag, o.KeyFlag = secretKey, secretPIN, "t-m-d-b-secret-key"
	})
	r := s.Resolve(ctx)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("settings", "resolved", r, "settings", s)
	log.Info("settings again", slog.Any("r", r))
	text := fmt.Sprintf("%v|%+v|%#v|%s|%v|%+v|%#v|%s", r, r, r, r, s, s, s, buf.String())
	js, _ := json.Marshal(r)
	text += string(js)
	for _, secret := range []string{secretKey, secretPIN, "t-m-d-b-secret-key"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q is in what the settings print:\n%s", secret, text)
		}
	}
	// Positive control: the key is there to be found when it is asked for.
	if r.TVDBKey() != secretKey || r.TVDBPIN() != secretPIN {
		t.Fatal("the keys are not in the resolved settings")
	}
	if !strings.Contains(fmt.Sprint(r), "tvdbKeySet:true") || !strings.Contains(fmt.Sprint(r), "tvdbPINSet:true") {
		t.Errorf("Resolved prints %q: it should say that the key and the PIN are set", r)
	}
	_ = env
}

// The real thing against the fake TVDB: a key is logged in with before it is stored, the login
// carries the PIN only when there is one, and neither shows up in the error of a refusal.
func TestTVDBUpdateLogsInWithTheRealClient(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	f := newTVDBFake(t)
	s, env := newSettings(t, func(o *SettingsOptions) { o.TVDBBaseURL = f.URL(); o.NewProvider = nil })

	// The right key and PIN.
	if err := s.Update(ctx, Update{TVDBKey: str(f.key), TVDBPIN: str(f.pin)}); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := env.st.GetSetting(ctx, SettingTVDBKey); !ok || v != f.key {
		t.Errorf("stored key = %q, %v", v, ok)
	}
	if bodies := f.loginBodies(); len(bodies) != 1 || !strings.Contains(bodies[0], `"pin":"`+f.pin+`"`) {
		t.Errorf("login bodies = %q", bodies)
	}

	// Without a PIN the login has no "pin" at all.
	f2 := newTVDBFake(t)
	f2.setPIN("")
	s2, _ := newSettings(t, func(o *SettingsOptions) { o.TVDBBaseURL = f2.URL() })
	if err := s2.Update(ctx, Update{TVDBKey: str(f2.key)}); err != nil {
		t.Fatalf("a key that works without a PIN: %v", err)
	}
	if bodies := f2.loginBodies(); len(bodies) != 1 || strings.Contains(bodies[0], "pin") {
		t.Errorf("login bodies = %q; the PIN is left out when there is none", bodies)
	}

	// A wrong key: refused, and the key is not in the message.
	err := s.Update(ctx, Update{TVDBKey: str("a-wrong-key-123")})
	if !errors.Is(err, ErrTVDBRejected) || strings.Contains(err.Error(), "a-wrong-key-123") || strings.Contains(err.Error(), f.pin) {
		t.Errorf("err = %v", err)
	}
	if v, _, _ := env.st.GetSetting(ctx, SettingTVDBKey); v != f.key {
		t.Errorf("the refused key replaced the stored one: %q", v)
	}
	// A server that is not there.
	dead := newTVDBFake(t)
	url := dead.URL()
	dead.srv.Close()
	s3, _ := newSettings(t, func(o *SettingsOptions) { o.TVDBBaseURL = url })
	err = s3.Update(ctx, Update{TVDBKey: str("a-key-for-nobody")})
	if !errors.Is(err, ErrTVDBUnreachable) || strings.Contains(err.Error(), "a-key-for-nobody") {
		t.Errorf("err = %v", err)
	}
}

func TestSettingsTVDBImageURL(t *testing.T) {
	ctx := context.Background()
	var check error
	s, env := newTVDBSettings(t, &check)
	for size, want := range map[string]string{
		"w92": "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg", "w342": "https://artworks.thetvdb.com/banners/posters/1-1_t.jpg",
		"w500": "https://artworks.thetvdb.com/banners/posters/1-1.jpg", "original": "https://artworks.thetvdb.com/banners/posters/1-1.jpg",
	} {
		if got, ok := s.TVDBImageURL(ctx, size, "/banners/posters/1-1.jpg"); !ok || got != want {
			t.Errorf("TVDBImageURL(%s) = %q, %v; want %q", size, got, ok, want)
		}
	}
	// Never a host of the path's choosing, a bad size or a path that is not plain.
	for _, tc := range [][2]string{
		{"w342", "https://evil.example/banners/x.jpg"}, {"w342", "//evil.example/x.jpg"}, {"w342", "/banners/../../x.jpg"},
		{"w342", "/banners/x.jpg?host=evil"}, {"w999", "/banners/x.jpg"}, {"w342", "/banners/x.svg"}, {"w342", ""},
		{"w342", "/banners/a b.jpg"}, {"w342", "/banners/x.jpg#frag"}, {"w342", `/banners\x.jpg`},
	} {
		if got, ok := s.TVDBImageURL(ctx, tc[0], tc[1]); ok || got != "" {
			t.Errorf("TVDBImageURL(%q, %q) = %q, %v", tc[0], tc[1], got, ok)
		}
	}
	// The privacy switch: nothing leaves the machine.
	env.st.SetSetting(ctx, SettingEnabled, "false")
	if got, ok := s.TVDBImageURL(ctx, "w342", "/banners/posters/1-1.jpg"); ok || got != "" {
		t.Errorf("an address while switched off: %q, %v", got, ok)
	}
	env.st.SetSetting(ctx, SettingEnabled, "true")

	// The tests' base replaces the host and nothing else.
	local, _ := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBImageBaseURL = "http://127.0.0.1:9/img/" })
	if got, ok := local.TVDBImageURL(ctx, "w185", "/banners/posters/1-1.jpg"); !ok || got != "http://127.0.0.1:9/img/banners/posters/1-1_t.jpg" {
		t.Errorf("with a test base: %q, %v", got, ok)
	}
}

// Test checks the key that is in use for a provider with a request made now, wherever the key comes
// from, and says why when it cannot: no provider, no key, lookups off, a key refused, an outage.
func TestSettingsTestChecksTheKeyInUse(t *testing.T) {
	ctx := context.Background()
	var check error
	s, env := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBKeyFlag, o.TVDBPINFlag = secretKey, secretPIN })

	// The key in use is the flag's here, and the login carries the PIN with it.
	if err := s.Test(ctx, SourceTVDB); err != nil {
		t.Fatalf("Test(tvdb) with a working key = %v", err)
	}
	if want := []string{secretKey + "|" + secretPIN + "|" + DefaultLanguage}; !slices.Equal(env.checked, want) {
		t.Errorf("checked %q, want %q", env.checked, want)
	}
	// No TMDB key: nothing to check, and nothing asked.
	if err := s.Test(ctx, SourceTMDB); !errors.Is(err, ErrNoTMDB) {
		t.Errorf("Test(tmdb) with no key = %v, want ErrNoTMDB", err)
	}
	// The key of the settings is used when there is no flag or env (checked from the store, not from memory).
	s2, env2 := newTVDBSettings(t, &check)
	if err := s2.Test(ctx, SourceTVDB); !errors.Is(err, ErrNoTVDB) {
		t.Errorf("Test(tvdb) with no key = %v, want ErrNoTVDB", err)
	}
	env2.st.SetSetting(ctx, SettingTMDBKey, "tmdb-stored-key")
	env2.st.SetSetting(ctx, SettingTVDBKey, "tvdb-stored-key")
	if err := s2.Test(ctx, SourceTMDB); err != nil {
		t.Errorf("Test(tmdb) with the stored key = %v", err)
	}
	if err := s2.Test(ctx, SourceTVDB); err != nil {
		t.Errorf("Test(tvdb) with the stored key = %v", err)
	}
	if want := []string{"tmdb-stored-key/" + DefaultLanguage, "tvdb-stored-key||" + DefaultLanguage}; !slices.Equal(env2.checked, want) {
		t.Errorf("checked %q, want %q", env2.checked, want)
	}

	// What the provider says: refused, unreachable. Neither holds the key or the PIN.
	check = &TVDBError{Op: "POST /login", Status: 401, Message: "TVDB rejected the key or PIN"}
	s3, _ := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBKeyFlag, o.TVDBPINFlag = secretKey, secretPIN })
	if err := s3.Test(ctx, SourceTVDB); !errors.Is(err, ErrTVDBRejected) || strings.Contains(err.Error(), secretKey) || strings.Contains(err.Error(), secretPIN) {
		t.Errorf("Test(tvdb) with a refused key = %v, want ErrTVDBRejected and no secret", err)
	}
	check = &TVDBError{Op: "POST /login", Transient: true, Message: "connection refused"}
	s4, _ := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBKeyFlag = secretKey })
	if err := s4.Test(ctx, SourceTVDB); !errors.Is(err, ErrTVDBUnreachable) || strings.Contains(err.Error(), secretKey) {
		t.Errorf("Test(tvdb) in an outage = %v, want ErrTVDBUnreachable and no secret", err)
	}

	// Lookups off: nothing leaves the machine, not even a test. A provider that is none: invalid.
	check = nil
	off, offEnv := newTVDBSettings(t, &check, func(o *SettingsOptions) { o.TVDBKeyFlag, o.Disabled = secretKey, true })
	if err := off.Test(ctx, SourceTVDB); !errors.Is(err, ErrLookupsOff) || len(offEnv.checked) != 0 {
		t.Errorf("Test(tvdb) with lookups off = %v, checked %q; want ErrLookupsOff and no request", err, offEnv.checked)
	}
	for _, bad := range []string{"", "imdb", "TVDB", "nfo"} {
		if err := s.Test(ctx, bad); !errors.Is(err, ErrInvalidSetting) {
			t.Errorf("Test(%q) = %v, want ErrInvalidSetting", bad, err)
		}
	}
}
