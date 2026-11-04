package config

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLimitsAreNotGivenByDefault(t *testing.T) {
	t.Setenv(MaxStreamsEnv, "")
	t.Setenv(MaxTranscodesEnv, "")
	c, err := Parse([]string{"-data", t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	// 0 is "not given": the settings, then the encoder-dependent default, decide.
	if c.MaxStreams != 0 || c.MaxTranscodes != 0 {
		t.Errorf("limits = %d, %d, want 0 (not given)", c.MaxStreams, c.MaxTranscodes)
	}
	// -stream-idle stays what it was: a player that sends no heartbeats (hls.js before
	// the web change, AVPlayer) is not stopped any sooner.
	if DefaultStreamIdle != 60*time.Second || c.StreamIdle != 60*time.Second {
		t.Errorf("stream idle = %v (default %v), want 60s", c.StreamIdle, DefaultStreamIdle)
	}
}

func TestLimitsFromFlagsAndEnvironment(t *testing.T) {
	data := t.TempDir()
	t.Setenv(MaxStreamsEnv, "")
	t.Setenv(MaxTranscodesEnv, "")
	c, err := Parse([]string{"-data", data, "-max-streams", "6", "-max-transcodes", "2"}, &bytes.Buffer{})
	if err != nil || c.MaxStreams != 6 || c.MaxTranscodes != 2 {
		t.Fatalf("flags: %d, %d, %v", c.MaxStreams, c.MaxTranscodes, err)
	}

	t.Setenv(MaxStreamsEnv, "8")
	t.Setenv(MaxTranscodesEnv, "3")
	c, err = Parse([]string{"-data", data}, &bytes.Buffer{})
	if err != nil || c.MaxStreams != 8 || c.MaxTranscodes != 3 {
		t.Fatalf("environment: %d, %d, %v", c.MaxStreams, c.MaxTranscodes, err)
	}
	// The flag wins over the environment.
	c, err = Parse([]string{"-data", data, "-max-streams", "5"}, &bytes.Buffer{})
	if err != nil || c.MaxStreams != 5 || c.MaxTranscodes != 3 {
		t.Fatalf("flag over environment: %d, %d, %v", c.MaxStreams, c.MaxTranscodes, err)
	}
	// The ends.
	if c, err := Parse([]string{"-data", data, "-max-streams", "32", "-max-transcodes", "16"}, &bytes.Buffer{}); err != nil || c.MaxStreams != 32 || c.MaxTranscodes != 16 {
		t.Errorf("the highest limits: %d, %d, %v", c.MaxStreams, c.MaxTranscodes, err)
	}
	if c, err := Parse([]string{"-data", data, "-max-streams", "1", "-max-transcodes", "1"}, &bytes.Buffer{}); err != nil || c.MaxStreams != 1 || c.MaxTranscodes != 1 {
		t.Errorf("the lowest limits: %d, %d, %v", c.MaxStreams, c.MaxTranscodes, err)
	}
}

// A limit that was given and is not right is an error, not a quiet change into
// something else.
func TestBadLimitsAreRefused(t *testing.T) {
	t.Setenv(MaxStreamsEnv, "")
	t.Setenv(MaxTranscodesEnv, "")
	data := t.TempDir()
	for _, args := range [][]string{
		{"-max-streams", "0"}, {"-max-streams", "-1"}, {"-max-streams", "33"}, {"-max-streams", "many"},
		{"-max-transcodes", "0"}, {"-max-transcodes", "17"}, {"-max-transcodes", "-1"},
	} {
		_, err := Parse(append([]string{"-data", data}, args...), &bytes.Buffer{})
		if err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	_, err := Parse([]string{"-data", data, "-max-streams", "0"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-max-streams 0") || !strings.Contains(err.Error(), "1 to 32") {
		t.Errorf("error = %v, want it to name the flag, the value and the range", err)
	}
	for _, env := range []struct{ name, value string }{
		{MaxStreamsEnv, "0"}, {MaxStreamsEnv, "99"}, {MaxStreamsEnv, "x"}, {MaxTranscodesEnv, "17"}, {MaxTranscodesEnv, "-3"},
	} {
		t.Run(env.name+"="+env.value, func(t *testing.T) {
			t.Setenv(env.name, env.value)
			_, err := Parse([]string{"-data", data}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), env.name) {
				t.Errorf("%s=%s: error = %v, want one that names the variable", env.name, env.value, err)
			}
		})
	}
	// A bad environment value does not matter when the flag is given: the flag wins.
	t.Setenv(MaxStreamsEnv, "99")
	if c, err := Parse([]string{"-data", data, "-max-streams", "4"}, &bytes.Buffer{}); err != nil || c.MaxStreams != 4 {
		t.Errorf("flag over a bad environment value: %d, %v", c.MaxStreams, err)
	}
}

func TestUsageListsTheLimitFlags(t *testing.T) {
	var out bytes.Buffer
	_, _ = Parse([]string{"-h"}, &out)
	for _, f := range []string{"-max-streams", "-max-transcodes", MaxStreamsEnv, MaxTranscodesEnv} {
		if !strings.Contains(out.String(), f) {
			t.Errorf("usage does not mention %s:\n%s", f, out.String())
		}
	}
}
