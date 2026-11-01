//go:build unix

package subs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// psInt returns one numeric ps field of a process (nice, pgid).
func psInt(t *testing.T, field string, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o %s -p %d: %v", field, pid, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps -o %s -p %d: %q", field, pid, out)
	}
	return n
}

// startHangingFFmpeg runs a conversion whose "ffmpeg" records its pid (and that
// of a helper process it forks, if helper is set) and then hangs. It returns
// the pids and the channel the request's result arrives on.
func startHangingFFmpeg(t *testing.T, o Options, helper bool) (g *Generator, leader, child int, result <-chan error) {
	t.Helper()
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	body := "#!/bin/sh\nsleep 300 &\necho \"$$ $!\" > " + pids + ".tmp\nmv " + pids + ".tmp " + pids + "\nwait\n"
	if !helper {
		body = "#!/bin/sh\necho \"$$ 0\" > " + pids + ".tmp\nmv " + pids + ".tmp " + pids + "\nexec sleep 300\n"
	}
	o.Dir, o.FFmpeg = filepath.Join(dir, "subs"), filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(o.FFmpeg, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.Close()
		for _, pid := range []int{leader, child} { // whatever a failed test left running
			if pid > 0 {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		}
	})

	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}
	res := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src); res <- err }()
	waitFor(t, "ffmpeg to start", func() bool {
		b, err := os.ReadFile(pids)
		if err != nil {
			return false
		}
		_, err = fmt.Sscan(string(b), &leader, &child)
		return err == nil
	})
	return g, leader, child, res
}

// A conversion's ffmpeg runs at the configured priority (default 10): a
// subtitle extraction reads a whole container and must not compete with
// playback for the disk and the CPU.
func TestFFmpegRunsNiced(t *testing.T) {
	baseline := psInt(t, "nice", os.Getpid())
	if baseline > 0 {
		t.Skipf("the test process itself runs at nice %d; cannot tell reniced children apart", baseline)
	}
	for _, tc := range []struct {
		name string
		o    Options
		want int
	}{
		{"default", Options{}, 10},
		{"configured", Options{Niceness: 5}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, leader, _, _ := startHangingFFmpeg(t, tc.o, false)
			// The parent renices the child just after starting it.
			waitFor(t, fmt.Sprintf("ffmpeg to run at nice %d", tc.want), func() bool {
				return psInt(t, "nice", leader) == tc.want
			})
		})
	}
}

// A conversion that outlives Timeout is stopped with everything it started, and
// the request gets the deadline error (not a conversion failure that would be
// remembered against the file).
func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	g, leader, helper, result := startHangingFFmpeg(t, Options{Timeout: 2 * time.Second}, true)
	start := time.Now()

	// Positive controls: both processes run, the leader leads its own group
	// (not ours), and the helper is in that group.
	if !alive(leader) || !alive(helper) {
		t.Fatalf("setup: processes %d and %d are not both running", leader, helper)
	}
	if pg := psInt(t, "pgid", leader); pg != leader {
		t.Fatalf("ffmpeg %d is in process group %d, want its own", leader, pg)
	}
	if pg := psInt(t, "pgid", helper); pg != leader {
		t.Fatalf("the helper %d is in process group %d, want ffmpeg's %d", helper, pg, leader)
	}

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrConvert) {
			t.Errorf("Path = %v, want the deadline error", err)
		}
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("a 2 s timeout took %v", d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the conversion was not stopped by its timeout")
	}
	waitFor(t, "ffmpeg and its helper to be gone", func() bool { return !alive(leader) && !alive(helper) })
	if len(g.failed) != 0 {
		t.Errorf("a timeout was remembered as a failure: %v", g.failed)
	}
}
