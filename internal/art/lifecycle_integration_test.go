//go:build integration && unix

package art

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hangingFFmpeg is a stand-in for ffmpeg stuck on a slow mount: it answers the
// filter probe New makes, then sleeps instead of grabbing a frame.
func hangingFFmpeg(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\ncase \"$1\" in -hide_banner) exit 0;; esac\nexec sleep 60\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func groupGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for syscall.Kill(-pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("ffmpeg process group %d still alive after %s", pid, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCloseKillsTheFFmpegGroupAndRunsItNiced(t *testing.T) {
	g, err := New(context.Background(), Options{Dir: t.TempDir(), FFmpeg: hangingFFmpeg(t)})
	if err != nil {
		t.Fatal(err)
	}
	pids := make(chan int, 1)
	g.started = func(pid int) { pids <- pid }

	done := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src(1, 10), Thumb); done <- err }()
	pid := <-pids

	// Positive control: the process exists, and at the configured niceness.
	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("positive control: group %d not running: %v", pid, err)
	}
	out, err := exec.Command("ps", "-o", "nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || strings.TrimSpace(string(out)) != "10" {
		t.Errorf("ffmpeg niceness = %q (%v), want 10", strings.TrimSpace(string(out)), err)
	}

	g.Close()
	groupGone(t, pid, 3*time.Second)
	if err := <-done; err == nil {
		t.Fatal("the request cut short by Close reported success")
	}
	if _, err := g.Path(context.Background(), src(2, 10), Thumb); !errors.Is(err, ErrClosed) {
		t.Fatalf("Path after Close = %v, want ErrClosed", err)
	}
}

func TestTimeoutKillsTheFFmpegGroup(t *testing.T) {
	g, err := New(context.Background(), Options{Dir: t.TempDir(), FFmpeg: hangingFFmpeg(t), Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pids := make(chan int, 1)
	g.started = func(pid int) { pids <- pid }

	start := time.Now()
	_, err = g.Path(context.Background(), src(1, 10), Thumb)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable (timeout)", err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("a 300 ms timeout took %s", took)
	}
	groupGone(t, <-pids, 3*time.Second)
}
