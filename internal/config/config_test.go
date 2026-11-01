package config

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
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
