//go:build integration

package library_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"ply/internal/api"
	"ply/internal/library"
	"ply/internal/playback"
	"ply/internal/probe"
	"ply/internal/store"
)

const (
	movieRel   = "Movies/Test Movie (2020).mp4"
	episodeRel = "Shows/Test Show/Season 1/Test Show S01E02 Pilot.mkv"
)

// generateFixtures creates two 2 second clips with ffmpeg's lavfi sources:
// mp4/h264/aac and mkv/hevc/eac3. It returns the directory holding them.
func generateFixtures(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	dir := t.TempDir()
	for _, f := range []struct {
		rel  string
		args []string
	}{
		{movieRel, []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}},
		{episodeRel, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error",
			"-c:a", "eac3", "-ac", "2", "-b:a", "128k"}},
	} {
		out := filepath.Join(dir, f.rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"-nostdin", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=25",
			"-f", "lavfi", "-i", "sine=frequency=440:duration=2"}, f.args...)
		args = append(args, "-shortest", out)

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, output)
		}
	}
	return dir
}

type lib struct {
	root    string
	store   *store.Store
	scanner *library.Scanner
	handler http.Handler
}

// newLib copies the fixtures into a fresh root (its name contains a space)
// and wires a real ffprobe-backed scanner to a store and the API.
func newLib(t *testing.T, fixtures string) *lib {
	t.Helper()
	root := filepath.Join(t.TempDir(), "media root")
	for _, rel := range []string{movieRel, episodeRel} {
		data, err := os.ReadFile(filepath.Join(fixtures, rel))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sc := library.NewScanner(t.Context(), st, probe.FFprobe{Niceness: 10}, []string{root}, nil)
	return &lib{
		root:    root,
		store:   st,
		scanner: sc,
		handler: api.New(api.Options{Store: st, Scanner: sc, ID: "it", Name: "it", Version: "test"}),
	}
}

func (l *lib) request(t *testing.T, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	l.handler.ServeHTTP(rec, req)
	return rec
}

func (l *lib) items(t *testing.T) []api.ItemSummary {
	t.Helper()
	rec := l.request(t, "GET", "/api/items", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /api/items = %d: %s", rec.Code, rec.Body)
	}
	var items []api.ItemSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func (l *lib) detail(t *testing.T, id int64) api.ItemDetail {
	t.Helper()
	rec := l.request(t, "GET", fmt.Sprintf("/api/items/%d", id), nil)
	if rec.Code != 200 {
		t.Fatalf("GET item %d = %d: %s", id, rec.Code, rec.Body)
	}
	var d api.ItemDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (l *lib) scan(t *testing.T) error {
	t.Helper()
	return l.scanner.Scan(t.Context())
}

func find(items []api.ItemSummary, kind string) *api.ItemSummary {
	for i := range items {
		if items[i].Kind == kind {
			return &items[i]
		}
	}
	return nil
}

func TestIntegration(t *testing.T) {
	fixtures := generateFixtures(t)

	t.Run("scan indexes both files with probed data", func(t *testing.T) {
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		items := l.items(t)
		if len(items) != 2 {
			t.Fatalf("items = %d, want 2: %+v", len(items), items)
		}

		m := find(items, "movie")
		if m == nil || m.Title != "Test Movie" || m.Year == nil || *m.Year != 2020 || m.Container != "mp4" ||
			m.VideoCodec != "h264" || m.AudioCodec != "aac" || m.Width != 320 || m.Height != 240 {
			t.Fatalf("movie = %+v", m)
		}
		if m.DurationSec < 1.5 || m.DurationSec > 3 {
			t.Errorf("movie duration = %v, want about 2s", m.DurationSec)
		}

		e := find(items, "episode")
		if e == nil || e.Show != "Test Show" || e.Season == nil || *e.Season != 1 || e.Episode == nil || *e.Episode != 2 ||
			e.EpisodeTitle != "Pilot" || e.Container != "mkv" || e.VideoCodec != "hevc" || e.AudioCodec != "eac3" {
			t.Fatalf("episode = %+v", e)
		}
		if e.DurationSec < 1.5 || e.DurationSec > 3 {
			t.Errorf("episode duration = %v, want about 2s", e.DurationSec)
		}

		shows := l.request(t, "GET", "/api/shows", nil)
		var got []api.ShowSummary
		if err := json.Unmarshal(shows.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Show != "Test Show" ||
			got[0].EpisodeCount != 1 {
			t.Errorf("shows = %s (%v)", shows.Body, err)
		}

		// A rescan with nothing changed keeps ids stable.
		before := items
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		after := l.items(t)
		if len(after) != 2 || after[0].ID != before[0].ID || after[1].ID != before[1].ID {
			t.Errorf("ids changed on an unchanged rescan: %+v -> %+v", before, after)
		}
	})

	t.Run("decisions for avplayer", func(t *testing.T) {
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		items := l.items(t)
		mp4, mkv := find(items, "movie"), find(items, "episode")

		d := l.detail(t, mp4.ID)
		if d.Playback.Method != "direct" || d.Playback.VideoAction != "copy" || d.Playback.AudioAction != "copy" ||
			d.Playback.URL != fmt.Sprintf("/api/items/%d/file", mp4.ID) || len(d.Playback.Reasons) != 0 {
			t.Errorf("mp4 playback = %+v, want direct", d.Playback)
		}
		if len(d.Streams) != 2 || d.Path != "Test Movie (2020).mp4" {
			t.Errorf("mp4 detail streams/path = %+v / %q", d.Streams, d.Path)
		}

		d = l.detail(t, mkv.ID)
		if d.Playback.Method != "remux" || d.Playback.VideoAction != "copy" || d.Playback.AudioAction != "copy" ||
			d.Playback.URL != "" || len(d.Playback.Reasons) != 1 {
			t.Errorf("mkv playback = %+v, want remux (container only)", d.Playback)
		}

		// The same answer straight from the pure function over stored data.
		entry, err := l.store.GetEntry(t.Context(), mkv.ID)
		if err != nil {
			t.Fatal(err)
		}
		dec := playback.Decide(playback.Item{Container: entry.Container}, entry.Streams, playback.AVPlayer(), playback.NoOptions)
		if dec.Method != "remux" {
			t.Errorf("Decide(mkv hevc/eac3) = %+v", dec)
		}
	})

	t.Run("file endpoint serves the real bytes with ranges", func(t *testing.T) {
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		mp4 := find(l.items(t), "movie")
		want, err := os.ReadFile(filepath.Join(l.root, movieRel))
		if err != nil {
			t.Fatal(err)
		}

		srv := httptest.NewServer(l.handler)
		defer srv.Close()
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/items/%d/file", srv.URL, mp4.ID), nil)
		req.Header.Set("Range", "bytes=4-7")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 206 || string(body) != "ftyp" { // every mp4 starts with a size, then "ftyp"
			t.Errorf("status = %d, body = %q", resp.StatusCode, body)
		}
		if resp.Header.Get("Content-Type") != "video/mp4" ||
			resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 4-7/%d", len(want)) {
			t.Errorf("headers = %v", resp.Header)
		}
	})

	t.Run("rescan after delete removes the item", func(t *testing.T) {
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		mp4 := find(l.items(t), "movie")
		if mp4 == nil || len(l.items(t)) != 2 {
			t.Fatal("setup: both items should exist before the delete") // positive control
		}
		if rec := l.request(t, "GET", fmt.Sprintf("/api/items/%d", mp4.ID), nil); rec.Code != 200 {
			t.Fatalf("setup: item %d should exist, got %d", mp4.ID, rec.Code)
		}

		if err := os.Remove(filepath.Join(l.root, movieRel)); err != nil {
			t.Fatal(err)
		}
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		items := l.items(t)
		if len(items) != 1 || items[0].Kind != "episode" {
			t.Errorf("items after delete = %+v, want only the episode", items)
		}
		if rec := l.request(t, "GET", fmt.Sprintf("/api/items/%d", mp4.ID), nil); rec.Code != 404 {
			t.Errorf("deleted item still served: %d", rec.Code)
		}
		if rec := l.request(t, "GET", fmt.Sprintf("/api/items/%d/file", mp4.ID), nil); rec.Code != 404 {
			t.Errorf("deleted item's file still served: %d", rec.Code)
		}
	})

	t.Run("missing root does not wipe the library", func(t *testing.T) {
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		if n := len(l.items(t)); n != 2 { // positive control
			t.Fatalf("setup: %d items, want 2", n)
		}

		away := l.root + ".unmounted"
		if err := os.Rename(l.root, away); err != nil {
			t.Fatal(err)
		}
		if err := l.scan(t); err == nil {
			t.Error("scanning a missing root should report an error")
		}
		if n := len(l.items(t)); n != 2 {
			t.Errorf("missing root wiped the library: %d items left", n)
		}
		if st := l.scanner.Status(t.Context()); st.LastError == "" || st.ItemCount != 2 {
			t.Errorf("status = %+v", st)
		}

		// With the root back, a real deletion is applied again.
		if err := os.Rename(away, l.root); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(l.root, movieRel)); err != nil {
			t.Fatal(err)
		}
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		if n := len(l.items(t)); n != 1 {
			t.Errorf("after restoring the root and deleting a file: %d items, want 1", n)
		}
	})

	t.Run("unreadable root does not wipe the library", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permissions are not enforced for root")
		}
		l := newLib(t, fixtures)
		if err := l.scan(t); err != nil {
			t.Fatal(err)
		}
		if n := len(l.items(t)); n != 2 { // positive control
			t.Fatalf("setup: %d items, want 2", n)
		}

		if err := os.Chmod(l.root, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(l.root, 0o755) })
		if err := l.scan(t); err == nil {
			t.Error("scanning an unreadable root should report an error")
		}
		if n := len(l.items(t)); n != 2 {
			t.Errorf("unreadable root wiped the library: %d items left", n)
		}

		if err := os.Chmod(l.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := l.scan(t); err != nil {
			t.Fatalf("scan after restoring permissions: %v", err)
		}
		if n := len(l.items(t)); n != 2 {
			t.Errorf("items after restoring permissions = %d, want 2", n)
		}
	})
}
