package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ply/internal/store"
)

// Names of the rows in the settings table that this package owns.
const (
	SettingTMDBKey   = "tmdb_key"
	SettingTVDBKey   = "tvdb_key"
	SettingTVDBPIN   = "tvdb_pin"
	SettingMDBList   = "mdblist_key"
	SettingEnabled   = "metadata_enabled" // "true" | "false"
	SettingLanguage  = "metadata_language"
	DefaultLanguage  = "en-US"
	keyCheckTimeout  = 15 * time.Second
	maxKeyLength     = 2048
	keyValidationMsg = "the key may only have letters, digits and . _ - characters"
	pinValidationMsg = "the PIN may only have letters, digits and . _ - characters"
)

// DefaultTVDBKey is the TVDB project key built into the binary, set with
// -ldflags "-X ply/internal/metadata.DefaultTVDBKey=...". It is empty in the repo:
// a key never belongs in code, tests or fixtures.
var DefaultTVDBKey string

// KeySource says where the key in use came from, strongest first.
type KeySource string

const (
	KeyFromFlag     KeySource = "flag"
	KeyFromEnv      KeySource = "env"
	KeyFromSettings KeySource = "settings"
	KeyFromDefault  KeySource = "default"
	KeyNone         KeySource = ""
)

var (
	// ErrInvalidSetting is wrapped by the errors Update returns for input it
	// refuses (a malformed key or language).
	ErrInvalidSetting = errors.New("invalid setting")
	// ErrKeyRejected means TMDB answered that the key is not valid.
	ErrKeyRejected = errors.New("TMDB rejected the key")
	// ErrTVDBRejected means TVDB refused the key or PIN at login.
	ErrTVDBRejected = errors.New("TVDB rejected the key or PIN")
	// ErrMDBListRejected means MDBList answered that the key is not valid.
	ErrMDBListRejected = errors.New("MDBList rejected the key")
	// ErrMDBListUnreachable is ErrUnreachable for MDBList.
	ErrMDBListUnreachable = errors.New("could not reach MDBList")
	// ErrUnreachable means the key could not be checked: TMDB was out of reach.
	ErrUnreachable = errors.New("could not reach TMDB")
	// ErrTVDBUnreachable is ErrUnreachable for TVDB.
	ErrTVDBUnreachable = errors.New("could not reach TVDB")
	// ErrLookupsOff means a call needs a provider and online lookups are off: the
	// privacy switch is off, or there is no key.
	ErrLookupsOff = errors.New("online metadata lookups are off")
)

var (
	keyChars     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	languageCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2})?$`)
)

// Resolved is the effective configuration at one moment. It never prints a
// key or the PIN: use Key, TVDBKey and TVDBPIN when they must be sent to the
// provider, and nowhere else.
type Resolved struct {
	key, tvdbKey, tvdbPIN string
	mdblistKey            string
	KeySource             KeySource // of the TMDB key
	TVDBKeySource         KeySource
	TVDBPINSource         KeySource // flag, env, settings, or none
	MDBListKeySource      KeySource // flag, env, settings, or none
	// Enabled is the privacy switch: false when -metadata=false was given or the
	// setting turned it off. Nothing leaves the machine then.
	Enabled bool
	// ForcedOff is set when the flag, not the setting, is what turned it off.
	ForcedOff bool
	Language  string
	// TVDBDown is set while the last thing asked of TVDB failed for the network's
	// sake; TVDBRefused while TVDB refused the key or the PIN. The status line says
	// which. A key that is refused is not an outage: it does not get better by itself.
	TVDBDown, TVDBRefused bool
}

// KeySet reports whether there is a TMDB key at all.
func (r Resolved) KeySet() bool { return r.key != "" }

// Key is the key to send to TMDB.
func (r Resolved) Key() string { return r.key }

// TVDBKeySet and TVDBPINSet report whether there is a TVDB key, and a PIN.
func (r Resolved) TVDBKeySet() bool { return r.tvdbKey != "" }
func (r Resolved) TVDBPINSet() bool { return r.tvdbPIN != "" }

// TVDBKey and TVDBPIN are what to send to TVDB.
func (r Resolved) TVDBKey() string { return r.tvdbKey }
func (r Resolved) TVDBPIN() string { return r.tvdbPIN }

// MDBListKeySet reports whether there is an MDBList key; MDBListKey is what to send to MDBList.
func (r Resolved) MDBListKeySet() bool { return r.mdblistKey != "" }
func (r Resolved) MDBListKey() string  { return r.mdblistKey }

// Online reports whether lookups can be made: switched on, and a key of either provider.
func (r Resolved) Online() bool { return r.Enabled && (r.key != "" || r.tvdbKey != "") }

func (r Resolved) String() string {
	return fmt.Sprintf("{keySet:%t source:%q tvdbKeySet:%t tvdbSource:%q tvdbPINSet:%t enabled:%t language:%q}",
		r.KeySet(), r.KeySource, r.TVDBKeySet(), r.TVDBKeySource, r.TVDBPINSet(), r.Enabled, r.Language)
}

// GoString keeps %#v from printing the keys too.
func (r Resolved) GoString() string { return "metadata.Resolved" + r.String() }

// LogValue keeps slog from printing the keys.
func (r Resolved) LogValue() slog.Value {
	return slog.GroupValue(slog.Bool("keySet", r.KeySet()), slog.String("source", string(r.KeySource)),
		slog.Bool("tvdbKeySet", r.TVDBKeySet()), slog.String("tvdbSource", string(r.TVDBKeySource)), slog.Bool("tvdbPINSet", r.TVDBPINSet()),
		slog.Bool("enabled", r.Enabled), slog.String("language", r.Language))
}

// Source gives the worker and the API the providers to use right now. It is
// asked again for every pass and every request, so that a change of key or of
// the privacy switch takes effect at once.
type Source interface {
	// Provider returns the providers, or nil when online lookups are off.
	Provider(ctx context.Context) (*Providers, Resolved)
	// ImageURL is where a TMDB image can be downloaded, or false when the
	// privacy switch is off.
	ImageURL(ctx context.Context, size, file string) (string, bool)
	// TVDBImageURL is ImageURL for a TVDB image: file is its "/banners/..." path.
	TVDBImageURL(ctx context.Context, size, file string) (string, bool)
}

// SettingsOptions configures NewSettings.
type SettingsOptions struct {
	Store *store.Store
	// The TMDB key, from the command line, the environment and the build; the
	// settings table is the fourth place. In that order they win.
	KeyFlag, KeyEnv, KeyDefault string
	// The same for TVDB: the key (flag, environment, settings, the build) and the PIN
	// (flag, environment, settings).
	TVDBKeyFlag, TVDBKeyEnv, TVDBKeyDefault string
	TVDBPINFlag, TVDBPINEnv                 string
	// The MDBList key (flag, environment, settings).
	MDBListKeyFlag, MDBListKeyEnv string
	// Disabled is -metadata=false: the privacy switch is off whatever the
	// settings say.
	Disabled bool
	// Language is the default (-metadata-language); a stored setting overrides it.
	Language string
	// BaseURL and ImageBaseURL replace TMDB's addresses (tests); TVDBBaseURL and
	// TVDBImageBaseURL, TVDB's.
	BaseURL, ImageBaseURL         string
	TVDBBaseURL, TVDBImageBaseURL string
	MDBListBaseURL                string
	HTTPClient                    *http.Client
	Logger                        *slog.Logger
	// NewProvider builds a TMDB provider for a key and language, NewTVDB a TVDB
	// one for a key, PIN and language. The defaults are the real clients; tests
	// replace them.
	NewProvider func(key, language string) Provider
	NewTVDB     func(key, pin, language string) Provider
	// NewMDBList builds an MDBList client for a key; the default is the real client.
	NewMDBList func(key string) *MDBListClient
}

// Settings resolves the keys, the privacy switch and the language from the
// flags, the environment and the settings table, and keeps the providers built
// from them.
type Settings struct {
	o SettingsOptions

	tvdbDown    atomic.Bool // the last thing asked of TVDB failed for the network's sake
	tvdbRefused atomic.Bool // the last thing asked of TVDB was refused: the key or the PIN

	// mdblistRefused is set when MDBList refused the key; the ratings job leaves MDBList alone until
	// the key is changed or tested again (a refused key does not get better by itself).
	mdblistRefused atomic.Bool

	mu           sync.Mutex
	cached       Provider
	cachedBy     [2]string // key, language
	cachedTVDB   Provider
	tvdbBy       [3]string // key, PIN, language
	cachedMDB    *MDBListClient
	cachedMDBKey string

	// testMu guards tests, and puts the keeping of a test (which looks at the key in use first) in
	// order with the settling of an Update. tests is the last test of each provider's key (keytest.go).
	testMu sync.Mutex
	tests  map[string]keyTestRec
}

// String, GoString and LogValue keep the keys and the PIN, which the options hold,
// out of anything that prints or logs the settings.
func (s *Settings) String() string { return "metadata.Settings{}" }

func (s *Settings) GoString() string { return s.String() }

func (s *Settings) LogValue() slog.Value { return slog.StringValue(s.String()) }

// NewSettings returns the settings resolver.
func NewSettings(o SettingsOptions) *Settings {
	if o.Language == "" {
		o.Language = DefaultLanguage
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Settings{o: o}
}

func (s *Settings) newProvider(key, language string) Provider {
	if s.o.NewProvider != nil {
		return s.o.NewProvider(key, language)
	}
	return NewTMDB(NewClient(Options{Key: key, Language: language, BaseURL: s.o.BaseURL, ImageBaseURL: s.o.ImageBaseURL,
		HTTPClient: s.o.HTTPClient, Logger: s.o.Logger}))
}

func (s *Settings) newMDBList(key string) *MDBListClient {
	if s.o.NewMDBList != nil {
		return s.o.NewMDBList(key)
	}
	return NewMDBListClient(MDBListOptions{Key: key, BaseURL: s.o.MDBListBaseURL, HTTPClient: s.o.HTTPClient, Logger: s.o.Logger})
}

func (s *Settings) newTVDB(key, pin, language string) Provider {
	if s.o.NewTVDB != nil {
		return s.o.NewTVDB(key, pin, language)
	}
	return NewTVDBProvider(NewTVDBClient(TVDBOptions{Key: key, PIN: pin, Language: language, BaseURL: s.o.TVDBBaseURL,
		HTTPClient: s.o.HTTPClient, Logger: s.o.Logger}))
}

// setting reads a stored value; "" when there is none.
func (s *Settings) setting(ctx context.Context, key string) string {
	if v, ok, err := s.o.Store.GetSetting(ctx, key); err == nil && ok {
		return v
	}
	return ""
}

// Resolve reads the settings table and combines it with the fixed sources.
func (s *Settings) Resolve(ctx context.Context) Resolved {
	r := Resolved{Enabled: !s.o.Disabled, ForcedOff: s.o.Disabled, Language: s.o.Language, TVDBDown: s.tvdbDown.Load(), TVDBRefused: s.tvdbRefused.Load()}
	r.key, r.KeySource = pick(
		cand{s.o.KeyFlag, KeyFromFlag}, cand{s.o.KeyEnv, KeyFromEnv},
		cand{s.setting(ctx, SettingTMDBKey), KeyFromSettings}, cand{s.o.KeyDefault, KeyFromDefault})
	r.tvdbKey, r.TVDBKeySource = pick(
		cand{s.o.TVDBKeyFlag, KeyFromFlag}, cand{s.o.TVDBKeyEnv, KeyFromEnv},
		cand{s.setting(ctx, SettingTVDBKey), KeyFromSettings}, cand{s.o.TVDBKeyDefault, KeyFromDefault})
	r.tvdbPIN, r.TVDBPINSource = pick(cand{s.o.TVDBPINFlag, KeyFromFlag}, cand{s.o.TVDBPINEnv, KeyFromEnv},
		cand{s.setting(ctx, SettingTVDBPIN), KeyFromSettings})
	r.mdblistKey, r.MDBListKeySource = pick(cand{s.o.MDBListKeyFlag, KeyFromFlag}, cand{s.o.MDBListKeyEnv, KeyFromEnv},
		cand{s.setting(ctx, SettingMDBList), KeyFromSettings})
	if v, ok, err := s.o.Store.GetSetting(ctx, SettingEnabled); err == nil && ok && v == "false" {
		r.Enabled = false
	}
	if v, ok, err := s.o.Store.GetSetting(ctx, SettingLanguage); err == nil && ok && languageCode.MatchString(v) {
		r.Language = v
	}
	return r
}

type cand struct {
	value  string
	source KeySource
}

// pick is the first candidate that is not empty.
func pick(cands ...cand) (string, KeySource) {
	for _, c := range cands {
		if c.value != "" {
			return c.value, c.source
		}
	}
	return "", KeyNone
}

// Provider implements Source. Each provider is built once per key and language.
func (s *Settings) Provider(ctx context.Context) (*Providers, Resolved) {
	r := s.Resolve(ctx)
	if !r.Online() {
		return nil, r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var tmdb, tvdb Provider
	if r.key != "" {
		if by := [2]string{r.key, r.Language}; s.cached == nil || s.cachedBy != by {
			s.cached, s.cachedBy = s.newProvider(r.key, r.Language), by
		}
		tmdb = s.cached
	}
	if r.tvdbKey != "" {
		if by := [3]string{r.tvdbKey, r.tvdbPIN, r.Language}; s.cachedTVDB == nil || s.tvdbBy != by {
			s.cachedTVDB, s.tvdbBy = trackHealth(s.newTVDB(r.tvdbKey, r.tvdbPIN, r.Language), &s.tvdbDown, &s.tvdbRefused), by
		}
		tvdb = s.cachedTVDB
	}
	return NewProviders(tmdb, tvdb), r
}

// ImageURL implements Source. It needs no key: images are public.
func (s *Settings) ImageURL(ctx context.Context, size, file string) (string, bool) {
	if !ValidSize(size) || !ValidImagePath(file) || !s.Resolve(ctx).Enabled {
		return "", false
	}
	base := strings.TrimRight(s.o.ImageBaseURL, "/")
	if base == "" {
		base = DefaultImageBaseURL
	}
	return base + "/" + size + "/" + file, true
}

// TVDBImageURL implements Source. It needs no key either. The address is on
// artworks.thetvdb.com whatever the path says (ValidTVDBImage), unless the tests
// have pointed TVDBImageBaseURL elsewhere.
func (s *Settings) TVDBImageURL(ctx context.Context, size, file string) (string, bool) {
	if !ValidTVDBImage(size, file) || !s.Resolve(ctx).Enabled {
		return "", false
	}
	return tvdbImageURLAt(s.o.TVDBImageBaseURL, size, file), true
}

// tvdbImageURLAt is the address of a TVDB image: the thumbnail for the small
// sizes. base replaces the artworks host; "" is the real one.
func tvdbImageURLAt(base, size, file string) string {
	if !strings.HasPrefix(file, "/") {
		file = "/" + file
	}
	if base == "" {
		return TVDBImageURL(file, tvdbSmall(size))
	}
	if tvdbSmall(size) {
		file = tvdbThumbnail(file)
	}
	return strings.TrimRight(base, "/") + file
}

// Update is a change of settings; nil fields stay as they are.
type Update struct {
	Key      *string // "" removes the stored key
	Enabled  *bool
	Language *string
	// TVDBKey and TVDBPIN are the TVDB key and the subscriber PIN ("" removes
	// the stored one).
	TVDBKey *string
	TVDBPIN *string
	// MDBListKey is the MDBList key ("" removes the stored one).
	MDBListKey *string
	// RatingsPrimary is the source whose rating cards show (one of RatingPrimaries); RatingsShown
	// the sources to show, in order, each once (any of store.RatingSources; may be empty).
	RatingsPrimary *string
	RatingsShown   *[]string
}

// Update validates and stores a change. A new key is checked against its provider
// first (ErrKeyRejected, ErrTVDBRejected, ErrUnreachable, ErrTVDBUnreachable); a
// malformed key, PIN or language is ErrInvalidSetting. Nothing is stored unless
// everything is valid. With -metadata=false nothing may leave the machine, so a key
// is stored as it is given, unchecked (its format is still checked).
//
// A TVDB key in use where there was none makes the next pass of the worker move the shows that
// came from TMDB over to TVDB (see Worker): the note that it has done so is cleared here, as
// it is when the key in use is gone. The key in use is the flag's, the environment's, the stored
// one or the build's, in that order: a key that replaces another, or a stored one that comes or
// goes under one of the others, does not clear it.
//
// The last test of a key (KeyTest) follows the key in use: a check that passes here is kept as the
// test of the key it checked, when that is the key in use once the change is stored, and a change of
// the key in use clears the old test. A check that fails is kept only for the key already in use.
// Where a test is kept (memory, and the settings table when every secret is stored there) is in keytest.go.
func (s *Settings) Update(ctx context.Context, u Update) error {
	key, err := cleanSecret(u.Key, keyValidationMsg)
	if err != nil {
		return err
	}
	tvdbKey, err := cleanSecret(u.TVDBKey, keyValidationMsg)
	if err != nil {
		return err
	}
	tvdbPIN, err := cleanSecret(u.TVDBPIN, pinValidationMsg)
	if err != nil {
		return err
	}
	mdbKey, err := cleanSecret(u.MDBListKey, keyValidationMsg)
	if err != nil {
		return err
	}
	if u.Language != nil && !languageCode.MatchString(*u.Language) {
		return fmt.Errorf("%w: language must look like en-US or pt", ErrInvalidSetting)
	}
	if u.RatingsPrimary != nil && !slices.Contains(RatingPrimaries, *u.RatingsPrimary) {
		return fmt.Errorf("%w: ratingsPrimary must be one of %s", ErrInvalidSetting, strings.Join(RatingPrimaries, ", "))
	}
	if u.RatingsShown != nil {
		if err := validRatingsShown(*u.RatingsShown); err != nil {
			return err
		}
	}
	cur := s.Resolve(ctx)
	lang := cur.Language
	if u.Language != nil {
		lang = *u.Language
	}
	passed := map[string]string{} // provider: fingerprint of the key whose check passed
	if u.Key != nil && key != "" && !s.o.Disabled {
		fp := fingerprint(SourceTMDB, key)
		if err := s.checkKey(ctx, key, lang); err != nil {
			return s.failedCheck(ctx, SourceTMDB, fp, err)
		}
		passed[SourceTMDB] = fp
	}
	// A key is tried with the PIN that will go with it; a PIN with the key it will go with.
	newKey, newPIN := cur.tvdbKey, cur.tvdbPIN
	if u.TVDBKey != nil {
		newKey = tvdbKey
	}
	if u.TVDBPIN != nil {
		newPIN = tvdbPIN
	}
	if ((u.TVDBKey != nil && tvdbKey != "") || (u.TVDBPIN != nil && tvdbPIN != "")) && newKey != "" && !s.o.Disabled {
		fp := fingerprint(SourceTVDB, newKey, newPIN)
		if err := s.checkTVDB(ctx, newKey, newPIN, lang); err != nil {
			return s.failedCheck(ctx, SourceTVDB, fp, err)
		}
		passed[SourceTVDB] = fp
		// It logged in: the refusal, or the outage, that the status line may be telling of is over.
		s.tvdbDown.Store(false)
		s.tvdbRefused.Store(false)
	}

	if u.MDBListKey != nil && mdbKey != "" && !s.o.Disabled {
		fp := fingerprint(SourceMDBList, mdbKey)
		if err := s.checkMDBList(ctx, mdbKey); err != nil {
			return s.failedCheck(ctx, SourceMDBList, fp, err)
		}
		passed[SourceMDBList] = fp
		s.mdblistRefused.Store(false)
	}
	if u.RatingsPrimary != nil {
		if err := s.o.Store.SetSetting(ctx, SettingRatingsPrimary, *u.RatingsPrimary); err != nil {
			return err
		}
	}
	if u.RatingsShown != nil {
		list := *u.RatingsShown
		if list == nil {
			list = []string{} // a list with nothing in it, not null
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return err
		}
		if err := s.o.Store.SetSetting(ctx, SettingRatingsShown, string(raw)); err != nil {
			return err
		}
	}
	if u.Language != nil {
		if err := s.o.Store.SetSetting(ctx, SettingLanguage, *u.Language); err != nil {
			return err
		}
	}
	if u.Enabled != nil {
		if err := s.o.Store.SetSetting(ctx, SettingEnabled, fmt.Sprint(*u.Enabled)); err != nil {
			return err
		}
	}
	if u.TVDBKey != nil {
		// The key in use after this change: the stored one may be shadowed by a flag, the environment
		// or the build's, and then changing it changes nothing about TVDB.
		after, _ := pick(cand{s.o.TVDBKeyFlag, KeyFromFlag}, cand{s.o.TVDBKeyEnv, KeyFromEnv},
			cand{tvdbKey, KeyFromSettings}, cand{s.o.TVDBKeyDefault, KeyFromDefault})
		if (cur.tvdbKey == "") != (after == "") {
			// No key in use, then one (or one, then none): the shows are moved to TVDB again by the next
			// pass. One key replaced by another is the same TVDB: it moves nothing again, and the
			// shows that a person picked at TMDB while TVDB was on stay there.
			for _, key := range []string{SettingTVDBSwitched, SettingTVDBSwitchUpTo} {
				if err := s.o.Store.DeleteSetting(ctx, key); err != nil {
					return err
				}
			}
		}
		if err := s.storeSecret(ctx, SettingTVDBKey, tvdbKey); err != nil {
			return err
		}
	}
	if u.TVDBPIN != nil {
		if err := s.storeSecret(ctx, SettingTVDBPIN, tvdbPIN); err != nil {
			return err
		}
	}
	if u.MDBListKey != nil {
		if err := s.storeSecret(ctx, SettingMDBList, mdbKey); err != nil {
			return err
		}
		s.mdblistRefused.Store(false) // a new key (or none) is not the one that was refused
	}
	if u.Key != nil {
		if err := s.storeSecret(ctx, SettingTMDBKey, key); err != nil {
			return err
		}
	}
	s.afterUpdate(ctx, cur, passed)
	return nil
}

// cleanSecret trims and checks a key or PIN that was given ("" is allowed: it
// removes it). A nil pointer is "not given".
func cleanSecret(v *string, msg string) (string, error) {
	if v == nil {
		return "", nil
	}
	s := strings.TrimSpace(*v)
	if s != "" && (len(s) > maxKeyLength || !keyChars.MatchString(s)) {
		return "", fmt.Errorf("%w: %s", ErrInvalidSetting, msg)
	}
	return s, nil
}

func (s *Settings) storeSecret(ctx context.Context, name, value string) error {
	if value == "" {
		return s.o.Store.DeleteSetting(ctx, name)
	}
	return s.o.Store.SetSetting(ctx, name, value)
}

func (s *Settings) checkKey(ctx context.Context, key, language string) error {
	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()
	err := s.newProvider(key, language).Check(ctx)
	switch {
	case err == nil:
		return nil
	case IsUnauthorized(err):
		return fmt.Errorf("%w: use a v3 API key or a v4 read access token from your TMDB account settings", ErrKeyRejected)
	default:
		// The error is TMDB's or the network's: it never holds the key.
		return fmt.Errorf("%w to check the key: %v", ErrUnreachable, err)
	}
}

// checkTVDB logs in with the key and PIN; the PIN is left out of the login when
// there is none. A fresh client is used, so that a token or a refusal it remembers
// from before cannot answer for the new key.
func (s *Settings) checkTVDB(ctx context.Context, key, pin, language string) error {
	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()
	err := s.newTVDB(key, pin, language).Check(ctx)
	switch {
	case err == nil:
		return nil
	case IsUnauthorized(err):
		return fmt.Errorf("%w: check the project key and, if your key needs one, the subscriber PIN", ErrTVDBRejected)
	default:
		// The error is TVDB's or the network's: it never holds the key or the PIN.
		return fmt.Errorf("%w to check the key: %v", ErrTVDBUnreachable, err)
	}
}

// checkMDBList asks MDBList for the key's limits: the cheapest call that shows the key works.
func (s *Settings) checkMDBList(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, keyCheckTimeout)
	defer cancel()
	err := s.newMDBList(key).Check(ctx)
	switch {
	case err == nil:
		return nil
	case IsUnauthorized(err):
		return fmt.Errorf("%w: use the API key from your MDBList preferences page", ErrMDBListRejected)
	default:
		// The error is MDBList's or the network's: it never holds the key.
		return fmt.Errorf("%w to check the key: %v", ErrMDBListUnreachable, err)
	}
}

// MDBList returns the client for the key in use, or nil when nothing may leave the machine or there
// is no key. It is built once per key.
func (s *Settings) MDBList(ctx context.Context) (*MDBListClient, Resolved) {
	r := s.Resolve(ctx)
	if !r.Enabled || r.mdblistKey == "" {
		return nil, r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cachedMDB == nil || s.cachedMDBKey != r.mdblistKey {
		s.cachedMDB, s.cachedMDBKey = s.newMDBList(r.mdblistKey), r.mdblistKey
	}
	return s.cachedMDB, r
}

// MDBListRefused reports whether MDBList refused the key it was last asked with.
func (s *Settings) MDBListRefused() bool { return s.mdblistRefused.Load() }

// NoteMDBList records what an MDBList call showed of the key: refused (a 401 or 403) or not.
func (s *Settings) NoteMDBList(err error) {
	if err != nil {
		if IsUnauthorized(err) {
			s.mdblistRefused.Store(true)
		}
		return
	}
	s.mdblistRefused.Store(false)
}

// Test checks the key in use for a provider (SourceTMDB or SourceTVDB), wherever it comes
// from (a flag, the environment, the settings or the build), with a request made now: a
// login for TVDB (with the PIN, if there is one), TMDB's configuration for TMDB. It is
// the Test button of the settings page: the stored key cannot be read back, so the server
// does it. nil means the key works. The errors are ErrInvalidSetting for another provider,
// ErrLookupsOff when nothing may leave the machine, ErrNoTMDB or ErrNoTVDB for no key, and
// what Update says of a key that is refused or cannot be checked. None holds a key or a PIN.
// A TVDB key that logs in clears what the status line says of TVDB (refused, unreachable).
// What the check answers is kept as the last test of that key (KeyTest): a key that works, one that
// is refused and a provider out of reach alike. An error before any check was made is not a test.
func (s *Settings) Test(ctx context.Context, provider string) error {
	if provider != SourceTMDB && provider != SourceTVDB && provider != SourceMDBList {
		return fmt.Errorf("%w: provider must be tmdb, tvdb or mdblist", ErrInvalidSetting)
	}
	r := s.Resolve(ctx)
	switch {
	case !r.Enabled:
		return ErrLookupsOff
	case provider == SourceMDBList && !r.MDBListKeySet():
		return ErrNoMDBList
	case provider == SourceMDBList:
		err := s.checkMDBList(ctx, r.mdblistKey)
		if err == nil {
			s.mdblistRefused.Store(false)
		}
		s.recordKeyTest(ctx, provider, r.keyFingerprint(provider), err == nil)
		return err
	case provider == SourceTMDB && !r.KeySet():
		return ErrNoTMDB
	case provider == SourceTVDB && !r.TVDBKeySet():
		return ErrNoTVDB
	case provider == SourceTMDB:
		err := s.checkKey(ctx, r.key, r.Language)
		s.recordKeyTest(ctx, provider, r.keyFingerprint(provider), err == nil)
		return err
	}
	err := s.checkTVDB(ctx, r.tvdbKey, r.tvdbPIN, r.Language)
	if err == nil {
		// It logged in: the refusal, or the outage, that the status line tells of is over.
		s.tvdbDown.Store(false)
		s.tvdbRefused.Store(false)
	}
	s.recordKeyTest(ctx, provider, r.keyFingerprint(provider), err == nil)
	return err
}

// health is what trackHealth keeps: whether the last thing asked of TVDB failed
// for the network's sake (down) or was refused for the key's (refused).
type healthProvider struct {
	Provider
	down, refused *atomic.Bool
}

// trackHealth makes p report to down and refused: down is set by a transient failure,
// refused by a refusal of the key or the PIN, and each is cleared by any other answer
// (a 404 is an answer). At most one is set.
func trackHealth(p Provider, down, refused *atomic.Bool) Provider {
	return &healthProvider{Provider: p, down: down, refused: refused}
}

func (h *healthProvider) note(err error) error {
	h.refused.Store(err != nil && IsUnauthorized(err))
	h.down.Store(err != nil && !IsUnauthorized(err) && IsTransient(err))
	return err
}

func (h *healthProvider) SearchSeries(ctx context.Context, q string, y int) ([]Candidate, error) {
	c, err := h.Provider.SearchSeries(ctx, q, y)
	return c, h.note(err)
}

func (h *healthProvider) Find(ctx context.Context, source, id string) (m, s []Candidate, err error) {
	m, s, err = h.Provider.Find(ctx, source, id)
	return m, s, h.note(err)
}

func (h *healthProvider) Series(ctx context.Context, id int64, order string, seasons []int) (*store.TitleInput, error) {
	in, err := h.Provider.Series(ctx, id, order, seasons)
	return in, h.note(err)
}

func (h *healthProvider) Person(ctx context.Context, id int64) (*store.Person, error) {
	p, err := h.Provider.Person(ctx, id)
	return p, h.note(err)
}

func (h *healthProvider) Changes(ctx context.Context, kind string, start, end time.Time) ([]int64, error) {
	ids, err := h.Provider.Changes(ctx, kind, start, end)
	return ids, h.note(err)
}

func (h *healthProvider) Check(ctx context.Context) error { return h.note(h.Provider.Check(ctx)) }

// SeriesBySlug and Brief pass through to a provider that has them, and note the result.
func (h *healthProvider) SeriesBySlug(ctx context.Context, slug string) (int64, error) {
	sr, ok := h.Provider.(slugResolver)
	if !ok {
		return 0, ErrNoSuchTitle
	}
	id, err := sr.SeriesBySlug(ctx, slug)
	return id, h.note(err)
}

func (h *healthProvider) Brief(ctx context.Context, id int64) (Candidate, error) {
	b, ok := h.Provider.(briefer)
	if !ok {
		in, err := h.Provider.Series(ctx, id, store.OrderAired, nil)
		if err != nil {
			return Candidate{}, h.note(err)
		}
		return Candidate{ID: id, Source: SourceTVDB, Title: in.Title.Title, OriginalTitle: in.OriginalTitle, Year: in.Year,
			Overview: in.Overview, Poster: in.Poster}, nil
	}
	c, err := b.Brief(ctx, id)
	return c, h.note(err)
}

// StaticSource is a Source with fixed providers, for tests and for programs
// that do not use the settings table. A nil provider means no key.
type StaticSource struct {
	mu       sync.Mutex
	p        Provider
	tvdb     Provider
	disabled bool
	down     atomic.Bool
	refused  atomic.Bool
}

// NewStaticSource returns a source that always answers p (TMDB).
func NewStaticSource(p Provider) *StaticSource { return &StaticSource{p: p} }

// Set changes the TMDB provider (nil: no key) and the privacy switch.
func (s *StaticSource) Set(p Provider, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p, s.disabled = p, !enabled
}

// SetTVDB changes the TVDB provider (nil: no TVDB key).
func (s *StaticSource) SetTVDB(p Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != nil {
		p = trackHealth(p, &s.down, &s.refused)
	}
	s.tvdb = p
}

// Provider implements Source.
func (s *StaticSource) Provider(context.Context) (*Providers, Resolved) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Resolved{Enabled: !s.disabled, Language: DefaultLanguage, TVDBDown: s.down.Load(), TVDBRefused: s.refused.Load()}
	if s.p != nil {
		r.key, r.KeySource = "static", KeyFromSettings
	}
	if s.tvdb != nil {
		r.tvdbKey, r.TVDBKeySource = "static", KeyFromSettings
	}
	if s.disabled {
		return nil, r
	}
	return NewProviders(s.p, s.tvdb), r
}

// ImageURL implements Source.
func (s *StaticSource) ImageURL(_ context.Context, size, file string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled || s.p == nil {
		return "", false
	}
	u := s.p.ImageURL(size, file)
	return u, u != ""
}

// TVDBImageURL implements Source.
func (s *StaticSource) TVDBImageURL(_ context.Context, size, file string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled || s.tvdb == nil {
		return "", false
	}
	u := s.tvdb.ImageURL(size, file)
	return u, u != ""
}
