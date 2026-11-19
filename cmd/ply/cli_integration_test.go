//go:build integration && unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCommandLine runs the binary the way a person does before a server starts:
// -version, -h, and flags that are wrong. It is the flag wiring end to end, so
// "ply --version" cannot regress into "flag provided but not defined" again.
func TestCommandLine(t *testing.T) {
	bin := buildPly(t, t.TempDir())
	// Its own home and TMPDIR: neither the flags nor the version read the user's.
	env := []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}

	// ply runs the binary and says what it wrote and how it exited.
	ply := func(args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		var out, errOut strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errOut
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("%v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		return out.String(), errOut.String(), code
	}

	// -version and --version are one flag, and both print the version alone: the
	// logo and the colors go to a terminal, and a test's pipes are not one.
	for _, flag := range []string{"-version", "--version"} {
		stdout, stderr, code := ply(flag)
		if code != 0 {
			t.Fatalf("%s exited %d (stderr: %s)", flag, code, stderr)
		}
		if want := "ply v" + version + "\n"; stdout != want {
			t.Errorf("%s stdout = %q, want %q", flag, stdout, want)
		}
		if stderr != "" {
			t.Errorf("%s wrote to stderr: %q", flag, stderr)
		}
	}

	// -h: the usage, with -version among the flags.
	_, stderr, code := ply("-h")
	if code != 0 {
		t.Fatalf("-h exited %d (stderr: %s)", code, stderr)
	}
	for _, want := range []string{"Usage of ply:", "-version", "-media", "-addr"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("-h output lacks %q:\n%s", want, stderr)
		}
	}

	// An unknown flag: the flag package writes it once, with the usage, and main adds
	// nothing (it used to say it again, as "ply: flag provided...").
	_, stderr, code = ply("-nope")
	if code != 2 {
		t.Errorf("-nope exited %d, want 2", code)
	}
	if n := strings.Count(stderr, "flag provided but not defined: -nope"); n != 1 {
		t.Errorf("the error is written %d times, want once:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "ply: flag provided") {
		t.Errorf("main repeated the flag package's error:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Usage of ply:") {
		t.Errorf("no usage after the error:\n%s", stderr)
	}

	// A value the flag package rejects is the same kind of error.
	if _, stderr, code = ply("-max-streams", "abc"); code != 2 {
		t.Errorf("-max-streams abc exited %d, want 2 (stderr: %s)", code, stderr)
	}

	// A value ply itself rejects is not: one "ply:" line, no usage, exit 1.
	_, stderr, code = ply("-hwaccel", "bogus")
	if code != 1 {
		t.Errorf("-hwaccel bogus exited %d, want 1", code)
	}
	if n := strings.Count(stderr, "ply: "); n != 1 {
		t.Errorf("-hwaccel bogus: %d \"ply:\" lines, want 1:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "Usage of ply:") {
		t.Errorf("-hwaccel bogus should not print the usage:\n%s", stderr)
	}
}
