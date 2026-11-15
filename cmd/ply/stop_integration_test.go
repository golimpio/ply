//go:build integration && unix

package main

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ply -stop and ply -stop -force stop the ply that runs with the same data directory, token or
// not, and wait for it to exit; with none running they say so.
func TestEndToEndStop(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := buildPly(t, base)
	stop := func(args ...string) (string, error) {
		cmd := exec.Command(bin, append([]string{"-data", data}, args...)...)
		cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := stop("-stop"); err == nil || !strings.Contains(out, "no ply is running") {
		t.Fatalf("-stop with no server: %v %q, want an error saying none runs", err, out)
	}

	for _, args := range [][]string{{"-stop"}, {"-stop", "-force"}} {
		p := startPly(t, base, bin, tmp, "-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0")
		u := p.waitForURL()
		// Positive control: it is up, and has left its control file.
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(u + "/api/info"); err != nil {
			t.Fatalf("%v: the server is not up: %v", args, err)
		} else {
			resp.Body.Close()
		}
		if _, err := os.Stat(controlPath(data)); err != nil {
			t.Fatalf("%v: no control file while it runs: %v", args, err)
		}

		out, err := stop(args...)
		if err != nil || !strings.Contains(out, "Stopped.") {
			t.Fatalf("%v: %v %q, want Stopped.\n%s", args, err, out, p.log.String())
		}
		// ply -stop returned once the server was done: it has exited, or is exiting, at once.
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			t.Fatalf("%v: ply still running after ply -stop said it stopped\n%s", args, p.log.String())
		}
		if p.cmd.ProcessState.ExitCode() != 0 {
			t.Errorf("%v: ply exited with %v, want a clean exit\n%s", args, p.waitErr, p.log.String())
		}
		why := `msg="shutting down: asked by ply -stop"`
		if len(args) == 2 {
			why = `msg="shutting down: asked by ply -stop -force" within=5s`
		}
		if log := p.log.String(); !strings.Contains(log, why) || strings.Contains(log, "level=ERROR") {
			t.Errorf("%v: the log does not say %s, or has an error:\n%s", args, why, log)
		}
		if _, err := os.Stat(controlPath(data)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: the control file outlived the server: %v", args, err)
		}
	}
}
