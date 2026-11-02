//go:build unix

package api

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ply/internal/metadata"
)

func TestEmbeddedCoverIsExtractedOnFirstRequest(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nfor last; do :; done\necho \"$@\" >> '" + log + "'\nprintf '\\377\\330\\377\\340embedded-cover' > \"$last\"\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newMetaEnv(t, apiToken, func(o *Options) {
		cache, err := metadata.NewImageCache(metadata.ImageOptions{Dir: filepath.Join(dir, "images"), FFmpeg: ffmpeg})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cache.Close)
		o.Images = cache
	})
	if err := m.store.SetItemEmbeddedCover(t.Context(), m.multi, 5); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/images/local/item/%d/poster", m.multi)

	// Nothing is extracted until someone asks.
	if _, err := os.Stat(log); err == nil {
		t.Fatal("ffmpeg ran before the first request")
	}
	rec := m.get(path)
	wantStatus(t, rec, 200)
	if !strings.HasSuffix(rec.Body.String(), "embedded-cover") || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("cover: %q %v", rec.Body.String(), rec.Header())
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "-map 0:5") || strings.Count(string(calls), "\n") != 1 {
		t.Errorf("ffmpeg calls = %q", calls)
	}
	// Cached for the next request.
	wantStatus(t, m.get(path), 200)
	if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 1 {
		t.Errorf("ffmpeg ran again: %q", calls)
	}
	// Show artwork cannot be embedded: only items have media files.
	req := httptest.NewRequest("GET", "/api/images/local/series/1/poster", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	rec = httptest.NewRecorder()
	m.srv.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("series embedded: %d", rec.Code)
	}
}
