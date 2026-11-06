//go:build unix

package subs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A program that writes progress blocks on its stdout, as ffmpeg does for -progress pipe:1,
// is heard through the Generator's own exec: the times come out, the one that is not known
// does not, the last block counts, and the program is asked for it.
func TestFillProgressHearsWhatTheProgramWritesOnStdout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	body := `#!/bin/sh
echo "$@" > "$0.args"
printf 'out_time_us=N/A\nprogress=continue\nout_time_us=2000000\nprogress=continue\nout_time_us=4500000\nprogress=end\n'
for a in "$@"; do case "$a" in *.tmp) printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n\n' > "$a";; esac; done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Dir: filepath.Join(dir, "subs"), FFmpeg: script})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	src := track(existingFile(t, "a.mkv"), 3)

	var told []time.Duration
	if err := g.FillProgress(t.Context(), []Source{src}, nil, nil, func(at time.Duration) { told = append(told, at) }); err != nil {
		t.Fatal(err)
	}
	if !g.Cached(src) { // positive control: the program ran
		t.Fatal("the track was not written")
	}
	if want := []time.Duration{2 * time.Second, 4500 * time.Millisecond}; !slices.Equal(told, want) {
		t.Errorf("told %v, want %v", told, want)
	}
	args, _ := os.ReadFile(script + ".args")
	if !strings.Contains(string(args), "-progress pipe:1") {
		t.Errorf("the program was run with %q", args)
	}
}
