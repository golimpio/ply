//go:build integration && unix

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stop ends the server with SIGTERM and checks that it exits cleanly.
func (p *ply) stop() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		p.t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		p.t.Fatalf("ply still running 10 s after SIGTERM\n%s", p.log.String())
	}
	if p.waitErr != nil || p.cmd.ProcessState.ExitCode() != 0 {
		p.t.Errorf("ply exited with %v, want a clean exit\n%s", p.waitErr, p.log.String())
	}
	if log := p.log.String(); strings.Contains(log, "level=ERROR") || strings.Contains(log, "panic") {
		p.t.Errorf("server log is not that of a clean run:\n%s", log)
	}
}

type folderJSON struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Guessed    bool   `json:"guessed"`
	Source     string `json:"source"`
	KindLocked bool   `json:"kindLocked"`
	State      string `json:"state"`
}

func (c *client) folders() map[string]folderJSON {
	c.t.Helper()
	resp, body := c.do("GET", "/api/folders", "", map[string]string{"Authorization": "Bearer " + e2eToken})
	if resp.StatusCode != 200 {
		c.t.Fatalf("GET /api/folders = %d %s", resp.StatusCode, body)
	}
	var list []folderJSON
	mustJSON(c.t, body, &list)
	out := map[string]folderJSON{}
	for _, f := range list {
		out[f.Path] = f
	}
	if len(out) != len(list) {
		c.t.Fatalf("a folder is listed twice: %s", body)
	}
	return out
}

// TestEndToEndFolders runs the real binary through the life of the library
// folders: no folders at all, one added from the web page, the command-line
// folders next to it, a type changed by a flag, and a restart that keeps the
// folders added from the page.
func TestEndToEndFolders(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	tmp := filepath.Join(base, "tmp")
	films, tv, mixed, added := filepath.Join(base, "films"), filepath.Join(base, "tv"), filepath.Join(base, "mixed"), filepath.Join(base, "added")
	for _, d := range []string{tmp, films, tv, mixed, added} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := buildPly(t, base)
	bearer := map[string]string{"Authorization": "Bearer " + e2eToken}
	start := func(folders ...string) (*ply, *client) {
		args := append([]string{"-addr", "127.0.0.1:0", "-data", data, "-token", e2eToken, "-scan-interval", "0"}, folders...)
		p := startPly(t, base, bin, tmp, args...)
		return p, &client{t: t, base: p.waitForURL(), http: &http.Client{Timeout: 30 * time.Second}}
	}

	// 1. No folders: it starts, says what to do, and takes a folder from the page.
	p, c := start()
	if got := c.folders(); len(got) != 0 {
		t.Errorf("a fresh server lists %v", got)
	}
	if !strings.Contains(p.log.String(), "add one in Settings") {
		t.Errorf("the log does not say to add a folder:\n%s", p.log.String())
	}
	resp, body := c.do("GET", "/api/fs", "", bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"places":[`) || strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Allow-Origin")), "*") {
		t.Errorf("GET /api/fs = %d %s (ACAO %q)", resp.StatusCode, body, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	resp, body = c.do("POST", "/api/folders", `{"path":"`+added+`","kind":"movies"}`, bearer)
	if resp.StatusCode != 201 {
		t.Fatalf("POST /api/folders = %d %s", resp.StatusCode, body)
	}
	if f := c.folders()[added]; f.Source != "ui" || f.Kind != "movies" || f.KindLocked || f.State != "ok" {
		t.Errorf("added = %+v", f)
	}
	// Another site cannot do it; and the answer carries no CORS header.
	cross := map[string]string{"Authorization": "Bearer " + e2eToken, "Origin": "http://evil.example"}
	resp, _ = c.do("POST", "/api/folders", `{"path":"`+tv+`","kind":"shows"}`, cross)
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("cross-site POST = %d, ACAO %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if _, ok := c.folders()[tv]; ok {
		t.Error("the cross-site POST added a folder")
	}
	p.stop()

	// 2. Command-line folders next to the one from the page.
	p, c = start("-movies", films, "-shows", tv, "-media", mixed)
	got := c.folders()
	for path, want := range map[string]folderJSON{
		films: {Source: "flag", Kind: "movies", KindLocked: true},
		tv:    {Source: "flag", Kind: "shows", KindLocked: true},
		mixed: {Source: "flag", Kind: "", KindLocked: false}, // not guessed before a scan finds files
		added: {Source: "ui", Kind: "movies", KindLocked: false},
	} {
		f, ok := got[path]
		if !ok || f.Source != want.Source || f.Kind != want.Kind || f.KindLocked != want.KindLocked || f.State != "ok" {
			t.Errorf("%s = %+v, want %+v", filepath.Base(path), f, want)
		}
	}
	if len(got) != 4 {
		t.Errorf("folders = %v, want 4", got)
	}
	del := func(f folderJSON) int {
		resp, _ := c.do("DELETE", "/api/folders/"+strconv.FormatInt(f.ID, 10), "", bearer)
		return resp.StatusCode
	}
	patch := func(f folderJSON, kind string) int {
		resp, _ := c.do("PATCH", "/api/folders/"+strconv.FormatInt(f.ID, 10), `{"kind":"`+kind+`"}`, bearer)
		return resp.StatusCode
	}
	if code := del(got[films]); code != 409 {
		t.Errorf("DELETE of a command-line folder = %d, want 409", code)
	}
	if code := patch(got[films], "shows"); code != 409 {
		t.Errorf("PATCH of a locked folder = %d, want 409", code)
	}
	if code := patch(got[added], "shows"); code != 200 {
		t.Errorf("PATCH of the page's folder = %d, want 200", code)
	}
	if c.folders()[added].Kind != "shows" || c.folders()[films].Kind != "movies" {
		t.Errorf("after the PATCHes: %+v", c.folders())
	}
	p.stop()

	// 3. A flag may change a type; the page's folder kept its own, and a folder
	// that left the command line is now one of the page's.
	p, c = start("-shows", films)
	got = c.folders()
	if f := got[films]; f.Kind != "shows" || f.Source != "flag" || !f.KindLocked {
		t.Errorf("films after -shows = %+v", f)
	}
	if f := got[tv]; f.Source != "ui" || f.Kind != "shows" || f.KindLocked {
		t.Errorf("tv, no longer a flag = %+v", f)
	}
	if f := got[added]; f.Kind != "shows" || f.Source != "ui" {
		t.Errorf("added = %+v", f)
	}
	if code := del(got[tv]); code != 204 {
		t.Errorf("DELETE of a folder that left the command line = %d, want 204", code)
	}
	if _, ok := c.folders()[tv]; ok {
		t.Error("tv is still listed")
	}
	p.stop()

	// 4. And the removal is kept.
	p, c = start()
	if _, ok := c.folders()[tv]; ok {
		t.Error("a removed folder came back at the next start")
	}
	if len(c.folders()) != 2 { // films and added
		t.Errorf("folders = %v, want films and added", c.folders())
	}
	p.stop()
}
