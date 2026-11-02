package config

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8420" || c.ScanInterval != 15*time.Minute || c.FFprobe != "ffprobe" || c.FFmpeg != "ffmpeg" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.Name == "" {
		t.Error("name should default to the hostname")
	}
	if c.StreamIdle != DefaultStreamIdle || c.HWAccel != "auto" {
		t.Errorf("stream-idle = %v, hwaccel = %q", c.StreamIdle, c.HWAccel)
	}
	if len(c.Media) != 0 {
		t.Errorf("media = %v, want none", c.Media)
	}
}

func TestParseMediaRepeatableDedupedAbsolute(t *testing.T) {
	dir := t.TempDir()
	c, err := Parse([]string{"-data", dir, "-media", "a", "-media", dir + "/b/", "-media", "a", "-media", dir + "/b"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	want := []string{filepath.Join(wd, "a"), filepath.Join(dir, "b")}
	if len(c.Media) != len(want) || c.Media[0] != want[0] || c.Media[1] != want[1] {
		t.Errorf("media = %v, want %v", c.Media, want)
	}
}

func TestParseErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"negative interval": {"-scan-interval", "-1s"},
		"stray argument":    {"oops"},
		"unknown flag":      {"-nope"},
		"empty media":       {"-media", ""},
		"zero idle":         {"-stream-idle", "0s"},
		"bad idle":          {"-stream-idle", "soon"},
		"bad hwaccel":       {"-hwaccel", "gpu"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(args, &bytes.Buffer{}); err == nil {
				t.Error("expected an error")
			}
		})
	}
	if _, err := Parse([]string{"-h"}, &bytes.Buffer{}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h error = %v, want flag.ErrHelp", err)
	}
}

func TestParseZeroScanInterval(t *testing.T) {
	c, err := Parse([]string{"-data", t.TempDir(), "-scan-interval", "0"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.ScanInterval != 0 {
		t.Errorf("ScanInterval = %v, want 0", c.ScanInterval)
	}
}

func TestDataDir(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name                      string
		goos, home, xdg, override string
		want                      string
		wantErr                   bool
	}{
		{"darwin default", "darwin", "/Users/g", "", "", "/Users/g/Library/Application Support/ply", false},
		{"darwin ignores xdg", "darwin", "/Users/g", "/x", "", "/Users/g/Library/Application Support/ply", false},
		{"linux xdg", "linux", "/home/g", "/data", "", "/data/ply", false},
		{"linux relative xdg ignored", "linux", "/home/g", "rel", "", "/home/g/.local/share/ply", false},
		{"linux default", "linux", "/home/g", "", "", "/home/g/.local/share/ply", false},
		{"override wins", "linux", "/home/g", "/data", tmp, tmp, false},
		{"darwin no home", "darwin", "", "", "", "", true},
		{"linux no home", "linux", "", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dataDir(tt.goos, tt.home, tt.xdg, tt.override)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServerID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new dir")
	id, err := ServerID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !validID(id) {
		t.Fatalf("id %q is not 16 hex bytes", id)
	}
	again, err := ServerID(dir)
	if err != nil || again != id {
		t.Errorf("second call = %q, %v; want persisted %q", again, err, id)
	}

	// A corrupt file is replaced with a fresh valid id.
	if err := os.WriteFile(filepath.Join(dir, "server-id"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed, err := ServerID(dir)
	if err != nil || !validID(fixed) || fixed == "garbage" {
		t.Errorf("after corruption = %q, %v", fixed, err)
	}
}

func TestParseStreamIdleAndHWAccel(t *testing.T) {
	dir := t.TempDir()
	c, err := Parse([]string{"-data", dir, "-stream-idle", "5m", "-hwaccel", "none"}, &bytes.Buffer{})
	if err != nil || c.StreamIdle != 5*time.Minute || c.HWAccel != "none" {
		t.Fatalf("%+v, %v", c, err)
	}
	for _, mode := range []string{"auto", "videotoolbox", "vaapi", "qsv", "nvenc"} {
		if _, err := Parse([]string{"-data", dir, "-hwaccel", mode}, &bytes.Buffer{}); err != nil {
			t.Errorf("-hwaccel %s: %v", mode, err)
		}
	}

	// The environment variable only changes the default; the flag wins.
	t.Setenv(StreamIdleEnv, "3s")
	if c, err := Parse([]string{"-data", dir}, &bytes.Buffer{}); err != nil || c.StreamIdle != 3*time.Second {
		t.Errorf("env default: %v, %v", c.StreamIdle, err)
	}
	if c, err := Parse([]string{"-data", dir, "-stream-idle", "9s"}, &bytes.Buffer{}); err != nil || c.StreamIdle != 9*time.Second {
		t.Errorf("flag over env: %v, %v", c.StreamIdle, err)
	}
	t.Setenv(StreamIdleEnv, "garbage")
	if c, _ := Parse([]string{"-data", dir}, &bytes.Buffer{}); c.StreamIdle != DefaultStreamIdle {
		t.Errorf("invalid env should be ignored: %v", c.StreamIdle)
	}
}

func TestMetadataDefaults(t *testing.T) {
	for _, name := range []string{TMDBKeyEnv, MetadataEnv, MetadataLanguageEnv, MetadataRefreshEnv, TMDBURLEnv, TMDBImageURLEnv} {
		t.Setenv(name, "")
	}
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TMDBKey != "" || c.TMDBKeyEnv != "" || !c.Metadata || c.MetadataLanguage != "en-US" || c.MetadataRefresh != 24*time.Hour ||
		c.TMDBURL != "" || c.TMDBImageURL != "" {
		t.Errorf("defaults: %+v", c)
	}
}

func TestMetadataFlags(t *testing.T) {
	for _, name := range []string{TMDBKeyEnv, MetadataEnv, MetadataLanguageEnv, MetadataRefreshEnv, TMDBURLEnv, TMDBImageURLEnv} {
		t.Setenv(name, "")
	}
	c, err := Parse([]string{"-data", t.TempDir(), "-tmdb-key", " abc123 ", "-metadata=false", "-metadata-language", "pt-BR",
		"-metadata-refresh", "6h", "-tmdb-url", "http://127.0.0.1:1/3", "-tmdb-image-url", "http://127.0.0.1:1/img"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TMDBKey != "abc123" || c.Metadata || c.MetadataLanguage != "pt-BR" || c.MetadataRefresh != 6*time.Hour ||
		c.TMDBURL != "http://127.0.0.1:1/3" || c.TMDBImageURL != "http://127.0.0.1:1/img" {
		t.Errorf("flags: %+v", c)
	}
	c, err = Parse([]string{"-data", t.TempDir(), "-metadata-refresh", "0"}, &bytes.Buffer{})
	if err != nil || c.MetadataRefresh != 0 {
		t.Errorf("refresh 0: %+v, %v", c, err)
	}
}

func TestMetadataEnvironment(t *testing.T) {
	t.Setenv(TMDBKeyEnv, "envkey")
	t.Setenv(MetadataEnv, "false")
	t.Setenv(MetadataLanguageEnv, "de-DE")
	t.Setenv(MetadataRefreshEnv, "12h")
	t.Setenv(TMDBURLEnv, "http://tmdb.test/3")
	t.Setenv(TMDBImageURLEnv, "http://img.test")
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TMDBKey != "" || c.TMDBKeyEnv != "envkey" || c.Metadata || c.MetadataLanguage != "de-DE" || c.MetadataRefresh != 12*time.Hour ||
		c.TMDBURL != "http://tmdb.test/3" || c.TMDBImageURL != "http://img.test" {
		t.Errorf("env: %+v", c)
	}

	// The flag wins over the environment; the key stays in two places so that
	// the settings page can say which one is in use.
	c, err = Parse([]string{"-data", t.TempDir(), "-tmdb-key", "flagkey", "-metadata=true", "-metadata-language", "fr-FR",
		"-metadata-refresh", "1h", "-tmdb-url", "http://flag/3"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TMDBKey != "flagkey" || c.TMDBKeyEnv != "envkey" || !c.Metadata || c.MetadataLanguage != "fr-FR" ||
		c.MetadataRefresh != time.Hour || c.TMDBURL != "http://flag/3" || c.TMDBImageURL != "http://img.test" {
		t.Errorf("flags over env: %+v", c)
	}
}

func TestMetadataErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"negative refresh":      {args: []string{"-metadata-refresh", "-1h"}},
		"bad refresh":           {args: []string{"-metadata-refresh", "daily"}},
		"bad language":          {args: []string{"-metadata-language", "english"}},
		"empty language":        {args: []string{"-metadata-language", ""}},
		"bad bool in env":       {env: map[string]string{MetadataEnv: "maybe"}},
		"bad duration in env":   {env: map[string]string{MetadataRefreshEnv: "soon"}},
		"negative duration env": {env: map[string]string{MetadataRefreshEnv: "-5m"}},
		"bad language in env":   {env: map[string]string{MetadataLanguageEnv: "en_US"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, e := range []string{TMDBKeyEnv, MetadataEnv, MetadataLanguageEnv, MetadataRefreshEnv} {
				t.Setenv(e, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Parse(append([]string{"-data", t.TempDir()}, tc.args...), &bytes.Buffer{}); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestUsageHidesTheTestFlags(t *testing.T) {
	var out bytes.Buffer
	if _, err := Parse([]string{"-h"}, &out); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h: %v", err)
	}
	usage := out.String()
	for _, want := range []string{"-media", "-tmdb-key", "-metadata", "-metadata-language", "-metadata-refresh", "-hwaccel", "-token"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage lacks %s:\n%s", want, usage)
		}
	}
	for _, hidden := range []string{"-tmdb-url", "-tmdb-image-url"} {
		if strings.Contains(usage, hidden) {
			t.Errorf("usage shows the hidden flag %s", hidden)
		}
	}
	// Hidden is not unknown.
	if _, err := Parse([]string{"-data", t.TempDir(), "-tmdb-url", "http://x/3"}, &bytes.Buffer{}); err != nil {
		t.Errorf("hidden flag rejected: %v", err)
	}
}

func TestWatchFlag(t *testing.T) {
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil || !c.Watch {
		t.Fatalf("default: %+v, %v; watching is on by default", c.Watch, err)
	}
	c, err = Parse([]string{"-data", t.TempDir(), "-watch=false"}, &bytes.Buffer{})
	if err != nil || c.Watch {
		t.Errorf("-watch=false: %+v, %v", c.Watch, err)
	}
}
