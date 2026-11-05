package config

import (
	"bytes"
	"strings"
	"testing"
)

// The TVDB key and PIN come from the flag and from the environment, kept apart so that
// the settings page can say which one is in use; the flag wins later, in metadata.Settings.
func TestTVDBFlagsAndEnvironment(t *testing.T) {
	names := []string{TVDBKeyEnv, TVDBPINEnv, TVDBURLEnv, TVDBImageURLEnv}
	for _, n := range names {
		t.Setenv(n, "")
	}
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TVDBKey != "" || c.TVDBKeyEnv != "" || c.TVDBPIN != "" || c.TVDBPINEnv != "" || c.TVDBURL != "" || c.TVDBImageURL != "" {
		t.Errorf("defaults = %+v", c)
	}

	// Flags only, spaces trimmed.
	c, err = Parse([]string{"-data", t.TempDir(), "-tvdb-key", "  flag-key ", "-tvdb-pin", " flag-pin  ",
		"-tvdb-url", "http://127.0.0.1:1/v4", "-tvdb-image-url", "http://127.0.0.1:1/img"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TVDBKey != "flag-key" || c.TVDBPIN != "flag-pin" || c.TVDBKeyEnv != "" || c.TVDBPINEnv != "" ||
		c.TVDBURL != "http://127.0.0.1:1/v4" || c.TVDBImageURL != "http://127.0.0.1:1/img" {
		t.Errorf("flags = %+v", c)
	}

	// The environment alone, then both: each is kept in its own field.
	t.Setenv(TVDBKeyEnv, " env-key\n")
	t.Setenv(TVDBPINEnv, "env-pin")
	t.Setenv(TVDBURLEnv, "http://tvdb.test/v4")
	t.Setenv(TVDBImageURLEnv, "http://img.test")
	c, err = Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TVDBKey != "" || c.TVDBKeyEnv != "env-key" || c.TVDBPIN != "" || c.TVDBPINEnv != "env-pin" ||
		c.TVDBURL != "http://tvdb.test/v4" || c.TVDBImageURL != "http://img.test" {
		t.Errorf("environment = %+v", c)
	}
	c, err = Parse([]string{"-data", t.TempDir(), "-tvdb-key", "flag-key", "-tvdb-pin", "flag-pin", "-tvdb-url", "http://flag/v4"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if c.TVDBKey != "flag-key" || c.TVDBKeyEnv != "env-key" || c.TVDBPIN != "flag-pin" || c.TVDBPINEnv != "env-pin" ||
		c.TVDBURL != "http://flag/v4" || c.TVDBImageURL != "http://img.test" {
		t.Errorf("both = %+v", c)
	}
}

// The usage names the key and the PIN flags and leaves the ones for tests out; a key given on
// the command line is not repeated in an error about something else.
func TestTVDBFlagsInTheUsageAndNotInErrors(t *testing.T) {
	var out bytes.Buffer
	if _, err := Parse([]string{"-h"}, &out); err == nil {
		t.Fatal("-h should stop the parse")
	}
	for _, want := range []string{"-tvdb-key", "-tvdb-pin", TVDBKeyEnv, TVDBPINEnv} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage does not mention %q:\n%s", want, out.String())
		}
	}
	for _, hidden := range []string{"-tvdb-url", "-tvdb-image-url"} {
		if strings.Contains(out.String(), hidden) {
			t.Errorf("usage shows %q", hidden)
		}
	}
	// Accepted all the same.
	if _, err := Parse([]string{"-data", t.TempDir(), "-tvdb-url", "http://x/v4", "-tvdb-image-url", "http://x/img"}, &bytes.Buffer{}); err != nil {
		t.Errorf("hidden flags: %v", err)
	}
	var errOut bytes.Buffer
	_, err := Parse([]string{"-data", t.TempDir(), "-tvdb-key", "secret-key-123", "-tvdb-pin", "secret-pin-456", "-scan-interval", "-5s"}, &errOut)
	if err == nil {
		t.Fatal("a negative scan interval was accepted")
	}
	if strings.Contains(err.Error()+errOut.String(), "secret-key-123") || strings.Contains(err.Error()+errOut.String(), "secret-pin-456") {
		t.Errorf("a key is in the error: %v / %s", err, errOut.String())
	}
}
