//go:build unix

package subs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// stopped reports whether the process is suspended (ps says state T).
func stopped(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o state -p %d: %v", pid, err)
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "T")
}

// own starts a process of its own group that does nothing (like ffmpeg, a group leader).
func own(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// A background run is suspended while it is held and goes on when it is let go; a
// run somebody waits for is never suspended, and promoting one that is suspended
// lets it go.
func TestHoldSuspendsAndResumes(t *testing.T) {
	var c ctl
	pid := own(t)
	c.started(pid, 0)
	if stopped(t, pid) {
		t.Fatal("setup: the process starts suspended")
	}

	c.hold(true)
	waitFor(t, "the held process to stop", func() bool { return stopped(t, pid) })
	c.hold(false)
	waitFor(t, "the released process to go on", func() bool { return !stopped(t, pid) })

	c.hold(true)
	waitFor(t, "the held process to stop again", func() bool { return stopped(t, pid) })
	c.wait()
	waitFor(t, "the promoted process to go on", func() bool { return !stopped(t, pid) })
	c.hold(true)
	time.Sleep(100 * time.Millisecond)
	if stopped(t, pid) {
		t.Error("a run somebody waits for was suspended")
	}

	// After ffmpeg has been waited for, nothing is signalled: its pid may be anybody's.
	c.ended()
	c.hold(false)
	c.hold(true)
}

// hangingGen is a generator whose "ffmpeg" says its pid and hangs, and the pid file.
func hangingGen(t *testing.T, o Options) (g *Generator, pidOf func() int) {
	t.Helper()
	dir := t.TempDir()
	pids := filepath.Join(dir, "pid")
	ff := filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\necho $$ > " + pids + ".tmp\nmv " + pids + ".tmp " + pids + "\nexec sleep 300\n"
	if err := os.WriteFile(ff, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	// macOS vets the first run of a new executable, which takes from tens to hundreds of
	// milliseconds, a run's clock ticking: run it once, so that the runs under test start at once.
	_ = exec.Command(ff, "warm").Run()
	o.Dir, o.FFmpeg = filepath.Join(dir, "subs"), ff
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g, func() int {
		var pid int
		waitFor(t, "ffmpeg to start", func() bool {
			b, err := os.ReadFile(pids)
			if err != nil {
				return false
			}
			pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
			return err == nil
		})
		return pid
	}
}

// A run a viewer started is theirs while they watch; when the last one leaves (the page was
// closed) it is background work again: demoted, and held like any other while a play reads the
// disk. If another comes, it is theirs again.
func TestAnAbandonedRunIsBackgroundWorkAgain(t *testing.T) {
	if psInt(t, "nice", os.Getpid()) > 0 {
		t.Skip("the test process itself runs reniced; cannot tell reniced children apart")
	}
	var play atomic.Bool
	g, pidOf := hangingGen(t, Options{Hold: func(string) (bool, string) { return play.Load(), "a play reads the disk" }})
	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := pidOf()
	play.Store(true) // a play reads the disk: the viewer's run is not held for it
	time.Sleep(10 * paceEvery)
	if n := psInt(t, "nice", pid); n != 0 || stopped(t, pid) {
		t.Fatalf("while the viewer waits: nice %d, stopped %v; want 0 and running", n, stopped(t, pid))
	}

	fo.Close() // the viewer left
	waitFor(t, "the abandoned run to be demoted", func() bool { return psInt(t, "nice", pid) == 10 })
	waitFor(t, "the abandoned run to be held for the play", func() bool { return stopped(t, pid) })

	fo2, err := g.Follow(t.Context(), src, nil) // another viewer
	if err != nil {
		t.Fatal(err)
	}
	defer fo2.Close()
	waitFor(t, "the run to go on for the viewer", func() bool { return !stopped(t, pid) })
}

// timeLeft is what the clock of the run of src has left, once the pacer is done suspending or
// resuming it (apply holds the ctl's lock).
func timeLeft(t *testing.T, g *Generator, src Source) time.Duration {
	t.Helper()
	g.mu.Lock()
	c := g.inflight[cacheName(src)]
	g.mu.Unlock()
	if c == nil {
		t.Fatal("the run is not under way")
	}
	c.ctl.mu.Lock()
	defer c.ctl.mu.Unlock()
	c.ctl.bud.mu.Lock()
	defer c.ctl.bud.mu.Unlock()
	return c.ctl.bud.left
}

// The time a run may take is the time it runs: a run held for a play (suspended) keeps what it has
// left, and has it again when it goes on. (The budget alone is tested in TestBudgetStopsWhileSuspended;
// this is the run, with a real process.)
//
// How much a run has used when it is suspended depends on how long ffmpeg took to start, which is the
// machine's to say, so the test does not assume it: it reads what is left when the run is suspended
// (at most what the test knows it ran for), and holds the run to that.
func TestHeldTimeDoesNotCountAgainstARunsTimeout(t *testing.T) {
	const timeout, ran = time.Second, 200 * time.Millisecond
	var play atomic.Bool
	g, pidOf := hangingGen(t, Options{Timeout: timeout})
	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}
	filled := make(chan error, 1)
	began := time.Now()
	go func() {
		filled <- g.Fill(t.Context(), []Source{src}, func() (bool, string) { return play.Load(), "a play" }, nil)
	}()
	pid := pidOf()
	if d := time.Since(began); d > timeout/4 {
		t.Skipf("ffmpeg took %v to start, with a quarter of its %v gone: too little is left to tell how it is spent", d, timeout)
	}
	time.Sleep(ran) // the clock of the run was started before ffmpeg was: it has run for at least this long
	play.Store(true)
	waitFor(t, "ffmpeg to be suspended", func() bool {
		select {
		case err := <-filled:
			t.Fatalf("the run ended (%v) before it was suspended", err)
		default:
		}
		return stopped(t, pid)
	})
	kept := timeLeft(t, g, src)
	if kept > timeout-ran { // positive control: what the run ran for was taken off
		t.Fatalf("the run was suspended with %v of its %v left after running for at least %v", kept, timeout, ran)
	}

	select {
	case err := <-filled:
		t.Fatalf("the run ended (%v) while it was suspended: the held time counted", err)
	case <-time.After(2 * timeout): // longer than it has left, twice over
	}
	if !alive(pid) {
		t.Fatal("ffmpeg was killed while suspended")
	}

	play.Store(false)
	resumed := time.Now()
	select {
	case err := <-filled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Fill = %v, want the deadline", err)
		}
		if d := time.Since(resumed); d < kept {
			t.Errorf("the run ended %v after it went on, with %v left", d, kept)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run never ended: its time was not counted again after it went on")
	}
}

// Fill asks hold every so often and suspends the real ffmpeg for as long as it says
// yes. Only a play's disk holds it: the run goes on after.
func TestFillSuspendsFFmpegWhileHeld(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pid")
	ff := filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\necho $$ > " + pids + ".tmp\nmv " + pids + ".tmp " + pids + "\nfor last; do :; done\nprintf 'WEBVTT\\n\\n' > \"$last\"\nexec sleep 300\n"
	if err := os.WriteFile(ff, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Dir: filepath.Join(dir, "subs"), FFmpeg: ff})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)

	var hold atomic.Bool
	filled := make(chan error, 1)
	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}
	go func() {
		filled <- g.Fill(t.Context(), []Source{src}, func() (bool, string) { return hold.Load(), "a play" }, nil)
	}()

	var pid int
	waitFor(t, "ffmpeg to start", func() bool {
		b, err := os.ReadFile(pids)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	if stopped(t, pid) {
		t.Fatal("setup: ffmpeg is suspended while nothing holds it")
	}
	hold.Store(true)
	waitFor(t, "ffmpeg to be suspended while held", func() bool { return stopped(t, pid) })
	hold.Store(false)
	waitFor(t, "ffmpeg to go on once let go", func() bool { return !stopped(t, pid) })
	hold.Store(true)
	waitFor(t, "ffmpeg to be suspended again", func() bool { return stopped(t, pid) })

	// A play asks for the track: the run is released, and stays free.
	fo, err := g.Follow(t.Context(), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()
	waitFor(t, "ffmpeg to be released by the play", func() bool { return !stopped(t, pid) })
	time.Sleep(10 * paceEvery)
	if stopped(t, pid) {
		t.Error("the pacer suspended a run a play waits for")
	}
	select {
	case err := <-filled:
		t.Errorf("Fill returned early: %v", err)
	default:
	}
}
