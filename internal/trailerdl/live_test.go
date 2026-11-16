//go:build integration

package trailerdl

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// The canary for YouTube and Vimeo changing what ply speaks: a known trailer resolves to streams,
// and the first MiB of each answers. Run: go test -tags integration ./internal/trailerdl -run Live

func firstMiB(t *testing.T, s Stream, h http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Range", "bytes=0-1048575")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusPartialContent || n == 0 {
		t.Errorf("%s: HTTP %d, %d bytes", s.MIME, resp.StatusCode, n)
	}
}

func TestLiveYouTubeVisionOS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src, err := YouTube{}.Resolve(ctx, "vKQi3bBA1y8") // The Matrix, the trailer TMDB lists first
	if err != nil {
		t.Fatalf("YouTube: %v (compare client.go with yt-dlp's visionos client)", err)
	}
	if src.Video.Height < 720 || src.Audio.URL == "" {
		t.Fatalf("streams: %+v", src)
	}
	firstMiB(t, src.Video, src.Header)
	firstMiB(t, src.Audio, src.Header)
}

func TestLiveVimeo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src, err := Vimeo{}.Resolve(ctx, "76979871")
	if err != nil {
		t.Skipf("Vimeo refused: %v (it may block Go's TLS fingerprint; see docs/M13-TRAILERS.md)", err)
	}
	if src.Video.URL != "" {
		firstMiB(t, src.Video, src.Header)
	} else if src.HLS == "" {
		t.Errorf("no stream: %+v", src)
	}
}
