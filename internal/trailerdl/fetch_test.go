package trailerdl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/hwaccel"
	"ply/internal/store"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " is not installed")
		}
	}
}

// makeMedia makes a 2 s clip with ffmpeg: args are the codec options.
func makeMedia(t *testing.T, name string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=duration=2"}, args...), path)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return path
}

// codecs are ffprobe's codec names of the file's streams, in order.
func codecs(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(out)), ",")
}

// media serves files by name, with Range, and records the Range of every request.
type media struct {
	mu     sync.Mutex
	ranges []string
	fail   int // a status to answer instead
}

func (m *media) server(t *testing.T, files map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.ranges = append(m.ranges, r.Header.Get("Range"))
		fail := m.fail
		m.mu.Unlock()
		if fail != 0 {
			w.WriteHeader(fail)
			return
		}
		path, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubResolver answers every key with src.
type stubResolver Source

func (s stubResolver) Resolve(context.Context, string) (Source, error) { return Source(s), nil }

// testChunk is the tests' chunk: small, so that a 2 s clip takes several.
const testChunk = 16 << 10

func newFetcher(srv *httptest.Server, src Source) *Fetcher {
	return &Fetcher{YouTube: stubResolver(src), HTTP: srv.Client(), FFmpeg: "ffmpeg",
		Encoder: hwaccel.SoftwareEncoder, Chunk: testChunk}
}

var ytVideo = store.Video{Site: store.SiteYouTube, Key: "vKQi3bBA1y8"}

func size(t *testing.T, path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// The streams come in ranged chunks of about Chunk, and are muxed without converting.
func TestFetchRangedChunks(t *testing.T) {
	needFFmpeg(t)
	v := makeMedia(t, "v.mp4", "-map", "0:v", "-c:v", "libx264", "-g", "25")
	a := makeMedia(t, "a.m4a", "-map", "1:a", "-c:a", "aac")
	m := &media{}
	srv := m.server(t, map[string]string{"v": v, "a": a})
	src := Source{Video: Stream{URL: srv.URL + "/v", MIME: `video/mp4; codecs="avc1.4d401f"`, Height: 240, Size: size(t, v)},
		Audio: Stream{URL: srv.URL + "/a", MIME: `audio/mp4; codecs="mp4a.40.2"`, Size: size(t, a)}}
	var notes []string
	var last, total int64
	out, err := newFetcher(srv, src).Fetch(context.Background(), ytVideo, t.TempDir(), Steps{
		Progress: func(done, all int64, note string) {
			if len(notes) == 0 || notes[len(notes)-1] != note {
				notes = append(notes, note)
			}
			last, total = done, all
		}})
	if err != nil {
		t.Fatal(err)
	}
	if got := codecs(t, out); got != "h264,aac" {
		t.Errorf("codecs %s, want h264,aac", got)
	}
	if strings.Join(notes, ",") != "Downloading" || last != total || total != size(t, v)+size(t, a) {
		t.Errorf("progress notes %v, %d of %d", notes, last, total)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ranges) < 3 {
		t.Fatalf("%d requests (%q), want several chunks", len(m.ranges), m.ranges)
	}
	for _, r := range m.ranges {
		var from, to int64
		if _, err := fmt.Sscanf(r, "bytes=%d-%d", &from, &to); err != nil {
			t.Fatalf("Range %q: %v", r, err)
		}
		if n := to - from + 1; n > testChunk || (n < testChunk*95/100 && to+1 < size(t, v) && to+1 < size(t, a)) {
			t.Errorf("chunk of %d bytes (%s)", n, r)
		}
	}
}

// A stream that is not H.264 is converted, once, while downloading.
func TestFetchConvertsVP9(t *testing.T) {
	needFFmpeg(t)
	v := makeMedia(t, "v.webm", "-map", "0:v", "-c:v", "libvpx-vp9", "-deadline", "realtime")
	a := makeMedia(t, "a.m4a", "-map", "1:a", "-c:a", "aac")
	srv := (&media{}).server(t, map[string]string{"v": v, "a": a})
	src := Source{Video: Stream{URL: srv.URL + "/v", MIME: `video/webm; codecs="vp9"`, Height: 240},
		Audio: Stream{URL: srv.URL + "/a", MIME: `audio/mp4; codecs="mp4a.40.2"`}}
	var notes []string
	converting := 0
	out, err := newFetcher(srv, src).Fetch(context.Background(), ytVideo, t.TempDir(), Steps{
		Progress:   func(_, _ int64, note string) { notes = append(notes, note) },
		Converting: func(context.Context) error { converting++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if got := codecs(t, out); got != "h264,aac" || converting != 1 || notes[len(notes)-1] != "Converting to H.264" {
		t.Errorf("codecs %s, converting %d, notes %v", got, converting, notes)
	}
}

func TestFetchCancelled(t *testing.T) {
	needFFmpeg(t)
	v := makeMedia(t, "v.mp4", "-map", "0:v", "-c:v", "libx264")
	srv := (&media{}).server(t, map[string]string{"v": v})
	ctx, cancel := context.WithCancel(context.Background())
	src := Source{Video: Stream{URL: srv.URL + "/v", MIME: `video/mp4; codecs="avc1"`, Height: 240}}
	dir := t.TempDir()
	_, err := newFetcher(srv, src).Fetch(ctx, ytVideo, dir, Steps{Progress: func(int64, int64, string) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trailer.mp4")); err == nil {
		t.Error("an output file was made")
	}
}

func TestFetch403(t *testing.T) {
	m := &media{fail: http.StatusForbidden}
	srv := m.server(t, nil)
	src := Source{Video: Stream{URL: srv.URL + "/v", MIME: `video/mp4; codecs="avc1"`}}
	_, err := newFetcher(srv, src).Fetch(context.Background(), ytVideo, t.TempDir(), Steps{})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want the 403 named", err)
	}
}

// A start ad is waited for before the media is asked for, and a cancel ends the wait.
func TestFetchWaitsForTheStartAd(t *testing.T) {
	m := &media{}
	srv := m.server(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	src := Source{Video: Stream{URL: srv.URL + "/v", MIME: `video/mp4; codecs="avc1"`}, Wait: time.Hour}
	_, err := newFetcher(srv, src).Fetch(ctx, ytVideo, t.TempDir(), Steps{})
	if !errors.Is(err, context.DeadlineExceeded) || len(m.ranges) != 0 {
		t.Errorf("err = %v after %d requests; want the wait cut short and nothing asked", err, len(m.ranges))
	}
}
