package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"ply/internal/store"
)

// Names of the rows in the settings table that this package owns.
const (
	SettingTMDBKey   = "tmdb_key"
	SettingEnabled   = "metadata_enabled" // "true" | "false"
	SettingLanguage  = "metadata_language"
	DefaultLanguage  = "en-US"
	keyCheckTimeout  = 15 * time.Second
	maxKeyLength     = 2048
	keyValidationMsg = "the key may only have letters, digits and . _ - characters"
)

// KeySource says where the TMDB key in use came from, strongest first.
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
	// ErrUnreachable means the key could not be checked: TMDB was out of reach.
	ErrUnreachable = errors.New("could not reach TMDB")
	// ErrLookupsOff means a call needs TMDB and online lookups are off: the
	// privacy switch is off, or there is no key.
	ErrLookupsOff = errors.New("online metadata lookups are off")
)

var (
	keyChars     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	languageCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2})?$`)
)

// Resolved is the effective configuration at one moment. It never prints the
// key: use Key when it must be sent to TMDB, and nowhere else.
type Resolved struct {
	key       string
	KeySource KeySource
	// Enabled is the privacy switch: false when -metadata=false was given or the
	// setting turned it off. Nothing leaves the machine then.
	Enabled bool
	// ForcedOff is set when the flag, not the setting, is what turned it off.
	ForcedOff bool
	Language  string
}

// KeySet reports whether there is a key at all.
func (r Resolved) KeySet() bool { return r.key != "" }

// Key is the key to send to TMDB.
func (r Resolved) Key() string { return r.key }

// Online reports whether lookups can be made: switched on, and a key.
func (r Resolved) Online() bool { return r.Enabled && r.key != "" }

func (r Resolved) String() string {
	return fmt.Sprintf("{keySet:%t source:%q enabled:%t language:%q}", r.KeySet(), r.KeySource, r.Enabled, r.Language)
}

// GoString keeps %#v from printing the key too.
func (r Resolved) GoString() string { return "metadata.Resolved" + r.String() }

// LogValue keeps slog from printing the key.
func (r Resolved) LogValue() slog.Value {
	return slog.GroupValue(slog.Bool("keySet", r.KeySet()), slog.String("source", string(r.KeySource)),
		slog.Bool("enabled", r.Enabled), slog.String("language", r.Language))
}

// Source gives the worker and the API the provider to use right now. It is
// asked again for every pass and every request, so that a change of key or of
// the privacy switch takes effect at once.
type Source interface {
	// Provider returns the provider, or nil when online lookups are off.
	Provider(ctx context.Context) (Provider, Resolved)
	// ImageURL is where a TMDB image can be downloaded, or false when the
	// privacy switch is off.
	ImageURL(ctx context.Context, size, file string) (string, bool)
}

// SettingsOptions configures NewSettings.
type SettingsOptions struct {
	Store *store.Store
	// The key, from the command line, the environment and the build; the
	// settings table is the fourth place. In that order they win.
	KeyFlag, KeyEnv, KeyDefault string
	// Disabled is -metadata=false: the privacy switch is off whatever the
	// settings say.
	Disabled bool
	// Language is the default (-metadata-language); a stored setting overrides it.
	Language string
	// BaseURL and ImageBaseURL replace TMDB's addresses (tests).
	BaseURL, ImageBaseURL string
	HTTPClient            *http.Client
	Logger                *slog.Logger
	// NewProvider builds a provider for a key and language. The default is a
	// TMDB client; tests replace it.
	NewProvider func(key, language string) Provider
}

// Settings resolves the TMDB key, the privacy switch and the language from the
// flags, the environment and the settings table, and keeps the provider built
// from them.
type Settings struct {
	o SettingsOptions

	mu       sync.Mutex
	cached   Provider
	cachedBy [2]string // key, language
}

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

// Resolve reads the settings table and combines it with the fixed sources.
func (s *Settings) Resolve(ctx context.Context) Resolved {
	r := Resolved{Enabled: !s.o.Disabled, ForcedOff: s.o.Disabled, Language: s.o.Language}
	switch {
	case s.o.KeyFlag != "":
		r.key, r.KeySource = s.o.KeyFlag, KeyFromFlag
	case s.o.KeyEnv != "":
		r.key, r.KeySource = s.o.KeyEnv, KeyFromEnv
	default:
		if v, ok, err := s.o.Store.GetSetting(ctx, SettingTMDBKey); err == nil && ok && v != "" {
			r.key, r.KeySource = v, KeyFromSettings
		} else if s.o.KeyDefault != "" {
			r.key, r.KeySource = s.o.KeyDefault, KeyFromDefault
		}
	}
	if v, ok, err := s.o.Store.GetSetting(ctx, SettingEnabled); err == nil && ok && v == "false" {
		r.Enabled = false
	}
	if v, ok, err := s.o.Store.GetSetting(ctx, SettingLanguage); err == nil && ok && languageCode.MatchString(v) {
		r.Language = v
	}
	return r
}

// Provider implements Source. The provider is built once per key and language.
func (s *Settings) Provider(ctx context.Context) (Provider, Resolved) {
	r := s.Resolve(ctx)
	if !r.Online() {
		return nil, r
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if by := [2]string{r.key, r.Language}; s.cached == nil || s.cachedBy != by {
		s.cached, s.cachedBy = s.newProvider(r.key, r.Language), by
	}
	return s.cached, r
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

// Update is a change of settings; nil fields stay as they are.
type Update struct {
	Key      *string // "" removes the stored key
	Enabled  *bool
	Language *string
}

// Update validates and stores a change. A new key is checked against TMDB
// first (ErrKeyRejected, ErrUnreachable); a malformed key or language is
// ErrInvalidSetting. Nothing is stored unless everything is valid.
func (s *Settings) Update(ctx context.Context, u Update) error {
	var key string
	if u.Key != nil {
		key = strings.TrimSpace(*u.Key)
		if key != "" {
			if len(key) > maxKeyLength || !keyChars.MatchString(key) {
				return fmt.Errorf("%w: %s", ErrInvalidSetting, keyValidationMsg)
			}
		}
	}
	if u.Language != nil && !languageCode.MatchString(*u.Language) {
		return fmt.Errorf("%w: language must look like en-US or pt", ErrInvalidSetting)
	}
	if u.Key != nil && key != "" {
		lang := s.Resolve(ctx).Language
		if u.Language != nil {
			lang = *u.Language
		}
		if err := s.checkKey(ctx, key, lang); err != nil {
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
	if u.Key != nil {
		if key == "" {
			return s.o.Store.DeleteSetting(ctx, SettingTMDBKey)
		}
		return s.o.Store.SetSetting(ctx, SettingTMDBKey, key)
	}
	return nil
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

// StaticSource is a Source with a fixed provider, for tests and for programs
// that do not use the settings table. A nil provider means no key.
type StaticSource struct {
	mu       sync.Mutex
	p        Provider
	disabled bool
	imageURL string
}

// NewStaticSource returns a source that always answers p.
func NewStaticSource(p Provider) *StaticSource { return &StaticSource{p: p} }

// Set changes the provider (nil: no key) and the privacy switch.
func (s *StaticSource) Set(p Provider, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.p, s.disabled = p, !enabled
}

// Provider implements Source.
func (s *StaticSource) Provider(context.Context) (Provider, Resolved) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Resolved{Enabled: !s.disabled, Language: DefaultLanguage}
	if s.p != nil {
		r.key, r.KeySource = "static", KeyFromSettings
	}
	if s.disabled || s.p == nil {
		return nil, r
	}
	return s.p, r
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
