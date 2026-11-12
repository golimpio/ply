package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"ply/internal/stream"
)

// FuzzSessionFileName sends arbitrary file names to GET /api/sessions/{sid}/{file}
// of a live session. A name is either one the session serves (index.m3u8,
// init.mp4, segNNNNN.m4s) or it is a 404 (or a redirect, for the dot segments the
// router cleans); it never gets as far as opening anything, and so never reaches
// a path containing a slash or "..".
//
// The session exists and its ffmpeg is a stand-in that exits at once: a name
// that is let through ends in an error from the session (500/504) or a hit,
// never in the 404 of a name that was refused, which is how acceptance shows.
func FuzzSessionFileName(f *testing.F) {
	ffmpeg := filepath.Join(f.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		f.Fatal(err)
	}
	mgr, err := stream.NewManager(stream.Options{
		FFmpeg: ffmpeg, TempRoot: f.TempDir(), Cache: &kfCache{}, IdleTimeout: time.Hour,
		WaitTimeout: 100 * time.Millisecond, Tick: 20 * time.Millisecond,
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(mgr.Close)
	media := filepath.Join(f.TempDir(), "movie.mkv")
	if err := os.WriteFile(media, []byte("x"), 0o644); err != nil {
		f.Fatal(err)
	}
	sess, err := mgr.Create(f.Context(), stream.Request{
		ItemID: 1, Path: media, Container: "mkv", DurationSec: 2400, VideoIndex: 0, VideoCodec: "hevc", AudioIndex: -1,
	})
	if err != nil {
		f.Fatal(err)
	}
	srv := New(Options{Scanner: &fakeScanner{}, Streams: mgr})

	// The control: the session is there, and a name it serves gets past the
	// router and the name check (it is not the 404 of a refused name).
	get := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		p := "/api/sessions/" + sess.ID + "/"
		req.URL = &url.URL{Path: p + name, RawPath: p + url.PathEscape(name)}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("init.mp4"); rec.Code == http.StatusNotFound {
		f.Fatalf("positive control: init.mp4 of a live session is a %d: %s", rec.Code, rec.Body)
	}

	for _, s := range []string{
		"init.mp4", "seg00000.m4s", "seg00001.m4s", "seg00399.m4s", "seg00400.m4s", "seg99999.m4s",
		"", ".", "..", "../init.mp4", "..%2Finit.mp4", "seg00000.m4s/../init.mp4", "seg00000.m4s/", "/init.mp4",
		"a/b", "a\\b", "....//", "%2e%2e", "%2e%2e%2f%2e%2e%2fetc%2fpasswd", "init.mp4\n", "init.mp4\x00", "init.mp4?x=1",
		"seg00000.m4s#frag", "seg0000\u0660.m4s", "Seg00000.m4s", "INIT.MP4", "seg00000.m4s.tmp", "index.m3u8",
		"gop00000.m4s", "ffmpeg.pid", " init.mp4", "init.mp4 ", "\xff\xfe", "seg00000.m4s\r\n",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		rec := get(name)
		if served.MatchString(name) {
			if redirected(rec) {
				t.Errorf("%q redirected to %q", name, rec.Header().Get("Location"))
			}
			return // what the session makes of it is for the session tests
		}
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && !redirected(rec) {
			t.Fatalf("name %q was let through: %d %.200s", name, rec.Code, rec.Body)
		}
	})
}

// redirected: the router sends a client that asked for a dot segment to the
// cleaned path (a 3xx), which then is its own request. (A dot segment that comes
// to an /api path is a 400 before that: requireAuth.)
func redirected(rec *httptest.ResponseRecorder) bool { return rec.Code/100 == 3 }

// served is the whole grammar of names under a session (index.m3u8 has its
// own route), restated here so that the test does not trust the validator it
// checks.
var served = regexp.MustCompile(`^(index\.m3u8|init\.mp4|seg[0-9]{5}\.m4s)$`)
