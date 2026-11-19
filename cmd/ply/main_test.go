package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunVersion runs the shortest command line there is: the logo on a terminal,
// then the version on stdout, and no server (nor a directory created).
func TestRunVersion(t *testing.T) {
	// -version reads none of these, but Parse does, and a regression that falls
	// through to the server must not reach the real home either.
	home := t.TempDir()
	data, logDir := filepath.Join(home, "data"), filepath.Join(home, "log")
	for k, v := range map[string]string{
		"HOME": home, "XDG_DATA_HOME": filepath.Join(home, "xdg-data"),
		"XDG_STATE_HOME": filepath.Join(home, "xdg-state"), "PLY_LOG_DIR": logDir,
		"PLY_METADATA": "", "PLY_METADATA_REFRESH": "", "PLY_METADATA_LANGUAGE": "",
		"PLY_MAX_STREAMS": "", "PLY_MAX_TRANSCODES": "", "PLY_LOG_STDERR": "",
	} {
		t.Setenv(k, v)
	}
	// run writes to os.Stdout and os.Stderr directly, and main reads the real
	// terminal: this test takes both over, and says itself whether stderr is one.
	oldOut, oldErr, oldArgs := os.Stdout, os.Stderr, os.Args
	t.Cleanup(func() { os.Stdout, os.Stderr, os.Args = oldOut, oldErr, oldArgs })

	runVersion := func(terminal bool) (stdout, stderr string) {
		t.Helper()
		dir := t.TempDir()
		outPath, errPath := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
		of, err := os.Create(outPath)
		if err != nil {
			t.Fatal(err)
		}
		ef, err := os.Create(errPath)
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout, os.Stderr = of, ef
		os.Args = []string{"ply", "-version", "-data", data, "-log-dir", logDir, "-addr", "127.0.0.1:0"}
		runErr := run(terminal)
		_ = of.Close()
		_ = ef.Close()
		if runErr != nil {
			t.Fatalf("run(terminal=%v): %v", terminal, runErr)
		}
		out, outErr := os.ReadFile(outPath)
		errOut, errOutErr := os.ReadFile(errPath)
		if outErr != nil || errOutErr != nil {
			t.Fatalf("read the run's output: %v, %v", outErr, errOutErr)
		}
		return string(out), string(errOut)
	}
	want := "ply v" + version + "\n"

	// A terminal: the logo, with its blank line, opens the run.
	stdout, stderr := runVersion(true)
	if wantLogo := "\n█▀█ █   █ █\n█▀▀ █▄▄ ▀█▀\n\n"; stderr != wantLogo {
		t.Errorf("logo on a terminal = %q, want %q", stderr, wantLogo)
	}
	if stdout != want {
		t.Errorf("stdout on a terminal = %q, want %q", stdout, want)
	}

	// A pipe, or a unit file's log: the version alone, so that a script can read it.
	stdout, stderr = runVersion(false)
	if stderr != "" {
		t.Errorf("a pipe was given the logo: %q", stderr)
	}
	if stdout != want {
		t.Errorf("stdout in a pipe = %q, want %q", stdout, want)
	}

	// -version starts no server and makes nothing: it was given both directories.
	for _, dir := range []string{data, logDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("-version made %s: %v", dir, err)
		}
	}
}
