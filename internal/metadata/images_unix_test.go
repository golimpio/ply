//go:build unix

package metadata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFFmpeg is a script that writes a JPEG to its last argument (like ffmpeg
// writing the extracted cover), appends a line to a log and can be made to fail.
func fakeFFmpeg(t *testing.T, fail bool) (bin, logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\n" +
		"for last; do :; done\n" +
		"echo \"$@\" >> '" + logFile + "'\n"
	if fail {
		body += "echo 'Stream map 0:9 matches no streams' >&2\nexit 1\n"
	} else {
		body += "sleep 0.1\nprintf '\\377\\330\\377\\340cover-bytes' > \"$last\"\n"
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, logFile
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestEmbeddedCoverIsExtractedOnceAndRefreshedWhenTheFileChanges(t *testing.T) {
	bin, log := fakeFFmpeg(t, false)
	c := newCache(t, newImageServer(t, nil), func(o *ImageOptions) { o.FFmpeg = bin; o.Niceness = 10 })
	media := filepath.Join(t.TempDir(), "My Movies", "Movie (2020).mkv")
	if err := os.MkdirAll(filepath.Dir(media), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(media, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(media, old, old)

	// Twelve requests at once: one extraction.
	var wg sync.WaitGroup
	paths := make([]string, 12)
	for i := range paths {
		wg.Go(func() {
			var err error
			if paths[i], err = c.Embedded(t.Context(), 7, media, 4); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	want := filepath.Join(c.dir, "embedded", "7.jpg")
	for _, p := range paths {
		if p != want {
			t.Fatalf("path = %q, want %q", p, want)
		}
	}
	if got := calls(t, log); len(got) != 1 || !strings.Contains(got[0], "-map 0:4") || !strings.Contains(got[0], media) {
		t.Errorf("ffmpeg calls = %q, want one for stream 4 of the media file", got)
	}
	if b, _ := os.ReadFile(want); !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
		t.Errorf("cover file = %x", b)
	}
	if got := filesUnder(t, c.dir); len(got) != 1 {
		t.Errorf("files: %q", got)
	}

	// Cached.
	if _, err := c.Embedded(t.Context(), 7, media, 4); err != nil || len(calls(t, log)) != 1 {
		t.Errorf("second use: %v, %d calls", err, len(calls(t, log)))
	}
	// The media file is replaced by a newer one: extract again. (Positive control for the cache above.)
	now := time.Now().Add(time.Hour)
	os.Chtimes(media, now, now)
	if _, err := c.Embedded(t.Context(), 7, media, 4); err != nil || len(calls(t, log)) != 2 {
		t.Errorf("after the file changed: %v, %d calls", err, len(calls(t, log)))
	}
	// Another item has its own file.
	if p, err := c.Embedded(t.Context(), 8, media, 4); err != nil || filepath.Base(p) != "8.jpg" || len(calls(t, log)) != 3 {
		t.Errorf("item 8: %q, %v", p, err)
	}
}

func TestEmbeddedCoverFailures(t *testing.T) {
	bin, log := fakeFFmpeg(t, true)
	c := newCache(t, newImageServer(t, nil), func(o *ImageOptions) { o.FFmpeg = bin })
	media := filepath.Join(t.TempDir(), "m.mkv")
	if err := os.WriteFile(media, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if got, err := c.Embedded(t.Context(), 1, media, 9); !errors.Is(err, ErrImageUnavailable) || got != "" {
			t.Fatalf("attempt %d: %q, %v", i, got, err)
		}
	}
	if n := len(calls(t, log)); n != 1 {
		t.Errorf("%d ffmpeg runs for 3 requests, want 1 (a failure is remembered for a while)", n)
	}
	if got := filesUnder(t, c.dir); len(got) != 0 {
		t.Errorf("files after a failed extraction: %q", got)
	}
	// A media file that is gone is not an extraction attempt at all.
	if _, err := c.Embedded(t.Context(), 2, filepath.Join(t.TempDir(), "gone.mkv"), 0); !errors.Is(err, ErrImageUnavailable) {
		t.Errorf("missing media: %v", err)
	}
	if n := len(calls(t, log)); n != 1 {
		t.Errorf("ffmpeg ran for a missing file (%d runs)", n)
	}
}

func TestEmbeddedCoverRunsAFewAtATime(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	running := filepath.Join(dir, "running")
	// Each run records how many are running at once (mkdir is atomic, so the
	// counter is a set of directories).
	script := "#!/bin/sh\nfor last; do :; done\n" +
		"d=$(mktemp -d '" + running + ".XXXXXX')\n" +
		"sleep 0.25\n" +
		"n=$(ls -d '" + running + ".'* | wc -l)\n" +
		"echo $n >> '" + filepath.Join(dir, "peak") + "'\n" +
		"printf '\\377\\330\\377\\340x' > \"$last\"\n" +
		"rmdir \"$d\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newCache(t, newImageServer(t, nil), func(o *ImageOptions) { o.FFmpeg = bin })
	media := filepath.Join(dir, "m.mkv")
	os.WriteFile(media, []byte("v"), 0o644)
	var wg sync.WaitGroup
	for id := int64(1); id <= 8; id++ {
		wg.Go(func() {
			if _, err := c.Embedded(t.Context(), id, media, 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "peak"))
	peak := 0
	for f := range strings.FieldsSeq(string(b)) {
		n, _ := strconv.Atoi(f)
		peak = max(peak, n)
	}
	if peak == 0 || peak > maxCovers {
		t.Errorf("up to %d extractions ran at once, want at most %d (and some)", peak, maxCovers)
	}
}
