package trailerdl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeVimeo serves the player page of video 76979871 from file, or answers with status and body.
func fakeVimeo(t *testing.T, file string, status int, body string) (Vimeo, *http.Header) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/video/76979871" {
			http.NotFound(w, r)
			return
		}
		seen = r.Header.Clone()
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(body))
			return
		}
		http.ServeFile(w, r, "testdata/vimeo/"+file)
	}))
	t.Cleanup(srv.Close)
	return Vimeo{HTTP: srv.Client(), Base: srv.URL}, &seen
}

func TestVimeoProgressive(t *testing.T) {
	v, seen := fakeVimeo(t, "player.html", 0, "")
	src, err := v.Resolve(context.Background(), "76979871")
	if err != nil {
		t.Fatal(err)
	}
	if src.Video.URL != "MEDIA/1080.mp4" || src.Video.Height != 1080 || src.Audio.URL != "" || src.HLS != "" {
		t.Errorf("source: %+v, want the 1080p progressive file alone", src)
	}
	if ref := seen.Get("Referer"); !strings.HasSuffix(ref, "/video/76979871") {
		t.Errorf("Referer %q", ref)
	}
}

func TestVimeoHLSOnly(t *testing.T) {
	v, _ := fakeVimeo(t, "player_hls_only.html", 0, "")
	src, err := v.Resolve(context.Background(), "76979871")
	if err != nil || src.HLS != "MEDIA/akamai.m3u8" || src.Video.URL != "" {
		t.Errorf("source: %+v, %v; want the HLS master", src, err)
	}
}

func TestVimeoPrivacy403(t *testing.T) {
	v, _ := fakeVimeo(t, "", http.StatusForbidden, "<p>Because of its privacy settings, this video cannot be played here.</p>")
	_, err := v.Resolve(context.Background(), "76979871")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "privacy settings") {
		t.Errorf("err = %v", err)
	}
}
