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
	"runtime"
	"strings"
	"time"
)

// Config is the fully resolved server configuration.
type Config struct {
	Media        []string // absolute, cleaned, de-duplicated media roots
	Addr         string
	Data         string // absolute data directory
	Token        string
	Name         string
	ScanInterval time.Duration
	FFprobe      string
	FFmpeg       string
}

// DBPath is the SQLite database file inside the data directory.
func (c Config) DBPath() string { return filepath.Join(c.Data, "ply.db") }

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	if v == "" {
		return errors.New("empty path")
	}
	*l = append(*l, v)
	return nil
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
		media stringList
		data  string
	)
	fs := flag.NewFlagSet("ply", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Var(&media, "media", "media root directory (repeatable)")
	fs.StringVar(&c.Addr, "addr", ":8420", "listen address (:0 picks a free port)")
	fs.StringVar(&data, "data", "", "data directory (default: per-user application data dir)")
	fs.StringVar(&c.Token, "token", "", "require this token for /api (Bearer header or ?token=)")
	fs.StringVar(&c.Name, "name", hostname, "server name shown to clients")
	fs.DurationVar(&c.ScanInterval, "scan-interval", 15*time.Minute, "periodic rescan interval (0 disables; a scan always runs at startup)")
	fs.StringVar(&c.FFprobe, "ffprobe", "ffprobe", "ffprobe binary")
	fs.StringVar(&c.FFmpeg, "ffmpeg", "ffmpeg", "ffmpeg binary")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.ScanInterval < 0 {
		return Config{}, errors.New("-scan-interval must not be negative")
	}

	seen := map[string]bool{}
	for _, m := range media {
		abs, err := filepath.Abs(m)
		if err != nil {
			return Config{}, fmt.Errorf("media root %q: %w", m, err)
		}
		if !seen[abs] {
			seen[abs] = true
			c.Media = append(c.Media, abs)
		}
	}

	var err error
	if c.Data, err = DataDir(data); err != nil {
		return Config{}, err
	}
	return c, nil
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
