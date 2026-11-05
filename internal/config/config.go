// Package config parses command-line flags and resolves the data directory
// and persistent server identity.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ply/internal/library"
)

// RootFlag is a library folder given on the command line.
type RootFlag struct {
	Path string // absolute, cleaned
	Kind string // library.KindMovies (-movies), library.KindShows (-shows), or "" (-media: not known yet)
}

// Config is the fully resolved server configuration.
type Config struct {
	// Roots are the folders given with -media, -movies and -shows: absolute,
	// cleaned, de-duplicated, in the order given.
	Roots []RootFlag
	// Media is the path of every entry of Roots, whatever its type.
	Media        []string
	Addr         string
	Data         string // absolute data directory
	Token        string
	Name         string
	ScanInterval time.Duration
	FFprobe      string
	FFmpeg       string
	StreamIdle   time.Duration // a streaming session without a segment request or a heartbeat this long is stopped
	HWAccel      string        // auto, none, or a hardware encoder family (see hwaccel.ValidMode)

	// The limits on plays: -max-streams and -max-transcodes, or their environment
	// variables. 0 means not given, and then the settings table, then the default,
	// decide (internal/api: playLimits).
	MaxStreams, MaxTranscodes int

	Watch bool // -watch: pick up changes in the media folders within seconds

	// AllowHosts are extra Host names the server answers to (-allow-host, or
	// PLY_ALLOW_HOSTS comma-separated); see api.Options.AllowedHosts.
	AllowHosts []string

	// Metadata (M6). The TMDB key is kept as two values, because the settings
	// page says where the key in use came from: -tmdb-key, then PLY_TMDB_KEY.
	TMDBKey          string        // -tmdb-key
	TMDBKeyEnv       string        // PLY_TMDB_KEY
	Metadata         bool          // -metadata: false switches every online lookup off
	MetadataLanguage string        // -metadata-language, a TMDB language such as en-US
	MetadataRefresh  time.Duration // -metadata-refresh: how often titles are refreshed; 0 never
	TMDBURL          string        // hidden: -tmdb-url replaces TMDB's API address (tests)
	TMDBImageURL     string        // hidden: -tmdb-image-url replaces TMDB's image address (tests)

	// TheTVDB (M8): the same two places for the key as for TMDB's (-tvdb-key, then
	// PLY_TVDB_KEY), and for the subscriber PIN (-tvdb-pin, then PLY_TVDB_PIN); the settings
	// table and the build come after. Neither is ever printed.
	TVDBKey      string // -tvdb-key
	TVDBKeyEnv   string // PLY_TVDB_KEY
	TVDBPIN      string // -tvdb-pin
	TVDBPINEnv   string // PLY_TVDB_PIN
	TVDBURL      string // hidden: -tvdb-url replaces TVDB's API address (tests)
	TVDBImageURL string // hidden: -tvdb-image-url replaces the host of TVDB's images (tests)

	// Ratings (M10): the MDBList key has the same two places as the others (-mdblist-key, then
	// PLY_MDBLIST_KEY; the settings table comes after) and is never printed. The two addresses are
	// for tests: MDBList's API and IMDb's two datasets (ratings, and the episodes of shows).
	MDBListKey      string // -mdblist-key
	MDBListKeyEnv   string // PLY_MDBLIST_KEY
	MDBListURL      string // hidden: -mdblist-url
	IMDbRatingsURL  string // hidden: -imdb-ratings-url
	IMDbEpisodesURL string // hidden: -imdb-episodes-url
}

// Environment variables that stand in for flags (the flag wins).
const (
	TMDBKeyEnv           = "PLY_TMDB_KEY"
	MetadataEnv          = "PLY_METADATA"
	MetadataLanguageEnv  = "PLY_METADATA_LANGUAGE"
	MetadataRefreshEnv   = "PLY_METADATA_REFRESH"
	TMDBURLEnv           = "PLY_TMDB_URL"
	TMDBImageURLEnv      = "PLY_TMDB_IMAGE_URL"
	TVDBKeyEnv           = "PLY_TVDB_KEY"
	TVDBPINEnv           = "PLY_TVDB_PIN"
	TVDBURLEnv           = "PLY_TVDB_URL"
	TVDBImageURLEnv      = "PLY_TVDB_IMAGE_URL"
	MDBListKeyEnv        = "PLY_MDBLIST_KEY"
	MDBListURLEnv        = "PLY_MDBLIST_URL"
	IMDbRatingsURLEnv    = "PLY_IMDB_RATINGS_URL"
	IMDbEpisodesURLEnv   = "PLY_IMDB_EPISODES_URL"
	AllowHostsEnv        = "PLY_ALLOW_HOSTS"
	MaxStreamsEnv        = "PLY_MAX_STREAMS"
	MaxTranscodesEnv     = "PLY_MAX_TRANSCODES"
	DefaultMetadataLang  = "en-US"
	DefaultMetadataEvery = 24 * time.Hour
)

// hiddenFlags are accepted but not listed in the usage.
var hiddenFlags = map[string]bool{"tmdb-url": true, "tmdb-image-url": true, "tvdb-url": true, "tvdb-image-url": true,
	"mdblist-url": true, "imdb-ratings-url": true, "imdb-episodes-url": true}

var languageCode = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2})?$`)

// envBool reads a boolean from the environment; unset gives def. A value that
// is not a boolean is an error: a privacy switch must not be guessed.
func envBool(name string, def bool) (bool, error) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q: want true or false", name, v)
	}
	return b, nil
}

func envDuration(name string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %v", name, v, err)
	}
	return d, nil
}

// Bounds of the limits on plays, here and in the settings API.
const (
	MaxStreamsCeiling    = 32
	MaxTranscodesCeiling = 16
)

func envString(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// DefaultStreamIdle is how long a streaming session may go without a request
// before its ffmpeg is killed. A session whose player sends heartbeats (every
// 10 s, paused or not) is held to a shorter time, stream.DefaultBeatTimeout, since
// it says plainly when it has gone.
const DefaultStreamIdle = 60 * time.Second

// StreamIdleEnv, when set to a Go duration, replaces the default of
// -stream-idle. It exists so tests and scripts can shorten the timeout
// without touching the command line; the flag still wins.
const StreamIdleEnv = "PLY_STREAM_IDLE"

func defaultStreamIdle() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(StreamIdleEnv)); err == nil && d > 0 {
		return d
	}
	return DefaultStreamIdle
}

func validHWAccel(v string) bool {
	switch v {
	case "auto", "none", "videotoolbox", "vaapi", "qsv", "nvenc":
		return true
	}
	return false
}

// DBPath is the SQLite database file inside the data directory.
func (c Config) DBPath() string { return filepath.Join(c.Data, "ply.db") }

// rootList collects the values of -media, -movies or -shows into one list shared
// by the three, which keeps the order they were given in.
type rootList struct {
	kind string
	into *[]RootFlag
}

func (l rootList) String() string { return "" }

func (l rootList) Set(v string) error {
	if v == "" {
		return errors.New("empty path")
	}
	*l.into = append(*l.into, RootFlag{Path: v, Kind: l.kind})
	return nil
}

// resolveRoots makes the paths absolute and drops repeats. A folder named twice
// with the same type, or once with a type and once with -media, is one folder
// of that type; named with two types it is an error.
func resolveRoots(given []RootFlag) ([]RootFlag, error) {
	var out []RootFlag
	at := map[string]int{}
	for _, g := range given {
		abs, err := filepath.Abs(g.Path)
		if err != nil {
			return nil, fmt.Errorf("media root %q: %w", g.Path, err)
		}
		i, seen := at[abs]
		switch {
		case !seen:
			at[abs] = len(out)
			out = append(out, RootFlag{Path: abs, Kind: g.Kind})
		case g.Kind != "" && out[i].Kind != "" && g.Kind != out[i].Kind:
			return nil, fmt.Errorf("%s is given as both %s and %s folder", abs, out[i].Kind, g.Kind)
		case g.Kind != "":
			out[i].Kind = g.Kind
		}
	}
	return out, nil
}

// Parse parses args (without the program name). Usage and flag errors are
// written to out; flag.ErrHelp is returned for -h.
func Parse(args []string, out io.Writer) (Config, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "ply"
	}

	var (
		c     Config
		given []RootFlag
		data  string
	)
	fs := flag.NewFlagSet("ply", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Var(rootList{"", &given}, "media", "media folder of either type: movies or shows is guessed from its files, and can be changed in Settings (repeatable)")
	fs.Var(rootList{library.KindMovies, &given}, "movies", "folder of movies (repeatable); its type cannot be changed in Settings")
	fs.Var(rootList{library.KindShows, &given}, "shows", "folder of TV shows (repeatable); its type cannot be changed in Settings")
	fs.StringVar(&c.Addr, "addr", ":8420", "listen address (:0 picks a free port)")
	fs.StringVar(&data, "data", "", "data directory (default: per-user application data dir)")
	fs.StringVar(&c.Token, "token", "", "require this token for /api (Bearer header or ?token=)")
	fs.StringVar(&c.Name, "name", hostname, "server name shown to clients")
	fs.BoolVar(&c.Watch, "watch", true, "watch media folders and pick up changes within seconds")
	fs.Func("allow-host", "also answer to this host name, e.g. a reverse proxy's public name; \".example.com\" allows every name under it (repeatable; env "+AllowHostsEnv+")", func(v string) error {
		if v = strings.TrimSpace(v); v == "" {
			return errors.New("empty host name")
		}
		c.AllowHosts = append(c.AllowHosts, v)
		return nil
	})
	fs.DurationVar(&c.ScanInterval, "scan-interval", 15*time.Minute, "periodic rescan interval (0 disables; a scan always runs at startup)")
	fs.StringVar(&c.FFprobe, "ffprobe", "ffprobe", "ffprobe binary")
	fs.StringVar(&c.FFmpeg, "ffmpeg", "ffmpeg", "ffmpeg binary")
	fs.DurationVar(&c.StreamIdle, "stream-idle", defaultStreamIdle(), "stop a streaming session after this long without requests, for a player that does not send heartbeats; one that does is stopped 30 s after its last beat or request (env "+StreamIdleEnv+" sets the default)")
	fs.IntVar(&c.MaxStreams, "max-streams", 0, "most plays at once, direct plays included, 1 to 32 (default 4, or what Settings says; env "+MaxStreamsEnv+"). Given here, Settings cannot change it")
	fs.IntVar(&c.MaxTranscodes, "max-transcodes", 0, "most transcodes at once, 1 to 16 (default 1 with a software encoder, 3 with a hardware one, or what Settings says; env "+MaxTranscodesEnv+"). Given here, Settings cannot change it")
	fs.StringVar(&c.HWAccel, "hwaccel", "auto", "video encoder: auto (probe the GPU), none (libx264), videotoolbox, vaapi, qsv or nvenc")

	envMetadata, err := envBool(MetadataEnv, true)
	if err != nil {
		return Config{}, err
	}
	envRefresh, err := envDuration(MetadataRefreshEnv, DefaultMetadataEvery)
	if err != nil {
		return Config{}, err
	}
	fs.StringVar(&c.TMDBKey, "tmdb-key", "", "TMDB API key or read access token for posters, plots and cast (env "+TMDBKeyEnv+"; can also be set in the web UI)")
	fs.BoolVar(&c.Metadata, "metadata", envMetadata, "look up metadata online (TMDB); false keeps everything local (env "+MetadataEnv+")")
	fs.StringVar(&c.MetadataLanguage, "metadata-language", envString(MetadataLanguageEnv, DefaultMetadataLang), "TMDB language, such as en-US or pt-BR (env "+MetadataLanguageEnv+")")
	fs.DurationVar(&c.MetadataRefresh, "metadata-refresh", envRefresh, "how often titles are refreshed from TMDB (0 turns the refresh off; env "+MetadataRefreshEnv+")")
	fs.StringVar(&c.TMDBURL, "tmdb-url", envString(TMDBURLEnv, ""), "")
	fs.StringVar(&c.TMDBImageURL, "tmdb-image-url", envString(TMDBImageURLEnv, ""), "")
	fs.StringVar(&c.TVDBKey, "tvdb-key", "", "TheTVDB project API key for TV shows; shows then come from TVDB, and TMDB is the fallback (env "+TVDBKeyEnv+"; can also be set in the web UI)")
	fs.StringVar(&c.TVDBPIN, "tvdb-pin", "", "your TheTVDB subscriber PIN, for a project key that needs one (env "+TVDBPINEnv+"; can also be set in the web UI)")
	fs.StringVar(&c.TVDBURL, "tvdb-url", envString(TVDBURLEnv, ""), "")
	fs.StringVar(&c.TVDBImageURL, "tvdb-image-url", envString(TVDBImageURLEnv, ""), "")
	fs.StringVar(&c.MDBListKey, "mdblist-key", "", "MDBList API key for the Rotten Tomatoes, Metacritic, Letterboxd and Trakt ratings (env "+MDBListKeyEnv+"; can also be set in the web UI)")
	fs.StringVar(&c.MDBListURL, "mdblist-url", envString(MDBListURLEnv, ""), "")
	fs.StringVar(&c.IMDbRatingsURL, "imdb-ratings-url", envString(IMDbRatingsURLEnv, ""), "")
	fs.StringVar(&c.IMDbEpisodesURL, "imdb-episodes-url", envString(IMDbEpisodesURLEnv, ""), "")
	fs.Usage = func() { usage(fs, out) }
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	c.TMDBKeyEnv = os.Getenv(TMDBKeyEnv)
	c.TVDBKeyEnv = os.Getenv(TVDBKeyEnv)
	c.TVDBPINEnv = os.Getenv(TVDBPINEnv)
	c.MDBListKeyEnv = os.Getenv(MDBListKeyEnv)
	if err := c.parseLimits(fs); err != nil {
		return Config{}, err
	}
	if len(c.AllowHosts) == 0 {
		for _, h := range strings.Split(os.Getenv(AllowHostsEnv), ",") {
			if h = strings.TrimSpace(h); h != "" {
				c.AllowHosts = append(c.AllowHosts, h)
			}
		}
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.ScanInterval < 0 {
		return Config{}, errors.New("-scan-interval must not be negative")
	}
	if c.StreamIdle <= 0 {
		return Config{}, errors.New("-stream-idle must be positive")
	}
	if !validHWAccel(c.HWAccel) {
		return Config{}, fmt.Errorf("-hwaccel %q: want auto, none, videotoolbox, vaapi, qsv or nvenc", c.HWAccel)
	}
	if c.MetadataRefresh < 0 {
		return Config{}, errors.New("-metadata-refresh must not be negative")
	}
	if !languageCode.MatchString(c.MetadataLanguage) {
		return Config{}, fmt.Errorf("-metadata-language %q: want a language such as en-US or pt-BR", c.MetadataLanguage)
	}
	c.TMDBKey = strings.TrimSpace(c.TMDBKey)
	c.TMDBKeyEnv = strings.TrimSpace(c.TMDBKeyEnv)
	c.TVDBKey, c.TVDBKeyEnv = strings.TrimSpace(c.TVDBKey), strings.TrimSpace(c.TVDBKeyEnv)
	c.TVDBPIN, c.TVDBPINEnv = strings.TrimSpace(c.TVDBPIN), strings.TrimSpace(c.TVDBPINEnv)
	c.MDBListKey, c.MDBListKeyEnv = strings.TrimSpace(c.MDBListKey), strings.TrimSpace(c.MDBListKeyEnv)

	if c.Roots, err = resolveRoots(given); err != nil {
		return Config{}, err
	}
	for _, r := range c.Roots {
		c.Media = append(c.Media, r.Path)
	}

	if c.Data, err = DataDir(data); err != nil {
		return Config{}, err
	}
	return c, nil
}

// parseLimits settles -max-streams and -max-transcodes: the flag, else the
// environment, else 0 (not given). A value that was given must be in range: a
// limit must not turn into something else silently.
func (c *Config) parseLimits(fs *flag.FlagSet) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, l := range []struct {
		flag, env string
		into      *int
		ceiling   int
	}{
		{"max-streams", MaxStreamsEnv, &c.MaxStreams, MaxStreamsCeiling},
		{"max-transcodes", MaxTranscodesEnv, &c.MaxTranscodes, MaxTranscodesCeiling},
	} {
		v, label := *l.into, fmt.Sprintf("-%s %d", l.flag, *l.into)
		if !given[l.flag] {
			raw := strings.TrimSpace(os.Getenv(l.env))
			if raw == "" {
				*l.into = 0
				continue
			}
			n, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%s=%q: want a whole number from 1 to %d", l.env, raw, l.ceiling)
			}
			v, label = n, fmt.Sprintf("%s=%d", l.env, n)
		}
		if v < 1 || v > l.ceiling {
			return fmt.Errorf("%s: want a whole number from 1 to %d", label, l.ceiling)
		}
		*l.into = v
	}
	return nil
}

// usage prints the flags that are not hidden, as the flag package would.
func usage(fs *flag.FlagSet, out io.Writer) {
	fmt.Fprintf(out, "Usage of %s:\n", fs.Name())
	shown := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
	shown.SetOutput(out)
	fs.VisitAll(func(f *flag.Flag) {
		if !hiddenFlags[f.Name] {
			shown.Var(f.Value, f.Name, f.Usage)
			shown.Lookup(f.Name).DefValue = f.DefValue
		}
	})
	shown.PrintDefaults()
}

// DataDir returns the data directory: override if non-empty, else the
// platform default. The directory is not created.
func DataDir(override string) (string, error) {
	home, _ := os.UserHomeDir()
	return dataDir(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"), override)
}

func dataDir(goos, home, xdg, override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	if goos == "darwin" {
		if home == "" {
			return "", errors.New("cannot determine home directory; pass -data")
		}
		return filepath.Join(home, "Library", "Application Support", "ply"), nil
	}
	if filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "ply"), nil
	}
	if home == "" {
		return "", errors.New("cannot determine home directory; pass -data")
	}
	return filepath.Join(home, ".local", "share", "ply"), nil
}

// ServerID returns the persistent server id stored in dir/server-id (16 random
// bytes, hex), creating it on first use. A malformed file is replaced.
func ServerID(dir string) (string, error) {
	path := filepath.Join(dir, "server-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); validID(id) {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
