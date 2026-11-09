//go:build unix

package metadata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The ffmpeg that extracts a cover from a media file is killed when the last client
// waiting for the cover leaves, and not while another still waits.
func TestEmbeddedCoverExtractionStopsWhenTheLastClientLeaves(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	bin := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "m.mkv")
	if err := os.WriteFile(media, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newCache(t, newImageServer(t, nil), func(o *ImageOptions) { o.FFmpeg = bin })
	alive := func(pid int) bool { return syscall.Kill(pid, 0) == nil }

	ctxA, leaveA := context.WithCancel(t.Context())
	ctxB, leaveB := context.WithCancel(t.Context())
	errA, errB := make(chan error, 1), make(chan error, 1)
	go func() { _, err := c.Embedded(ctxA, 3, media, 0); errA <- err }()
	var pid int
	waitFor(t, "ffmpeg to start", func() bool {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 0
	})
	go func() { _, err := c.Embedded(ctxB, 3, media, 0); errB <- err }()
	key := ""
	waitFor(t, "both clients to wait", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for k, call := range c.inflight {
			key = k
			return call.refs.Waiters() == 2
		}
		return false
	})
	if !alive(pid) {
		t.Fatalf("setup: ffmpeg %d is not running", pid)
	}

	leaveA() // one of two: ffmpeg goes on
	if err := <-errA; !errors.Is(err, context.Canceled) {
		t.Fatalf("the first client: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if !alive(pid) {
		t.Fatalf("ffmpeg %d was killed while a client still waited for %s", pid, key)
	}

	leaveB() // the last: ffmpeg is killed
	if err := <-errB; !errors.Is(err, context.Canceled) {
		t.Fatalf("the last client: %v", err)
	}
	waitFor(t, "ffmpeg to be killed", func() bool { return !alive(pid) })
	waitFor(t, "the run to end", func() bool { return c.running() == 0 })
	if files := filesUnder(t, c.dir); len(files) != 0 {
		t.Errorf("files after a cancelled extraction: %q", files)
	}
}
