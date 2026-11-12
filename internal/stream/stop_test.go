//go:build unix

package stream

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// leaderOf is the pid of the ffmpeg of the session (which leads its process group).
func leaderOf(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run.cmd.Process.Pid
}

// answered runs f and returns how long it took. An f that does not come back fails
// the test (rather than hanging it).
func answered(t *testing.T, what string, f func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); f() }()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not come back in 10 s", what)
		return 0
	}
}

// A process stuck in a read from a share that is slow or gone is killed at once, but
// does not die until the read returns, and for as long as that takes it is not seen to
// exit. Stopping a session must not take that long: Delete answers, the session still
// counts as there (it holds its registry entry, its directory) but not against the
// limits, and everything is let go of when the exit is seen.
func TestDeleteAnswersBeforeTheProcessIsObservedGone(t *testing.T) {
	f := newFake(t)
	registry := filepath.Join(t.TempDir(), "sessions")
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 1; o.RegistryDir = registry })
	release := m.HoldExits() // the exit of the ffmpeg is not seen until the test says so
	t.Cleanup(release)       // before the manager's Close

	ctx := context.Background()
	a, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	leader, dir := leaderOf(a), a.Dir()
	entry, pidPath := m.reg.path(a.ID), filepath.Join(dir, pidFile)
	// Positive control: there is a process, a registry entry, a pid file and a directory
	// to be let go of, so that their being there later says something.
	if !pidAlive(leader) || !exists(entry) || !exists(pidPath) || !exists(dir) {
		t.Fatalf("setup: leader %d alive=%v, registry entry %v, pid file %v, dir %v",
			leader, pidAlive(leader), exists(entry), exists(pidPath), exists(dir))
	}

	var deleted bool
	took := answered(t, "Delete", func() { deleted = m.Delete(a.ID) })
	if !deleted {
		t.Fatal("Delete found no session")
	}
	if took > 1500*time.Millisecond {
		t.Errorf("Delete took %v with the exit of ffmpeg not seen, want about StopWait (%v)", took, m.o.StopWait)
	}
	waitUntil(t, "the leader to be dead", 2*time.Second, func() bool { return !pidAlive(leader) })
	if _, ok := m.Get(a.ID); ok {
		t.Error("the stopped session is still there to be asked for")
	}
	if n := m.Count(); n != 1 {
		t.Errorf("Count() = %d with its ffmpeg's exit not seen, want 1 (it still holds its entry and directory)", n)
	}
	if !exists(entry) || !exists(pidPath) || !exists(dir) {
		t.Errorf("let go of before the exit was seen: registry entry %v, pid file %v, dir %v", exists(entry), exists(pidPath), exists(dir))
	}
	if !m.Stopped(a.ID) {
		t.Error("Delete did not remember that the session was stopped on purpose")
	}

	// The limit is 1, and the session on its way out does not count against it.
	b, err := m.Create(ctx, fakeRequest())
	if err != nil {
		t.Fatalf("a new session while the old one's exit is not seen: %v", err)
	}

	release()
	waitUntil(t, "the old session to be let go of", 5*time.Second, func() bool { return m.Count() == 1 })
	if exists(entry) || exists(dir) {
		t.Errorf("after its exit was seen: registry entry %v, dir %v", exists(entry), exists(dir))
	}
	if !m.Delete(b.ID) {
		t.Fatal("Delete found no second session")
	}
	if n := m.Count(); n != 0 {
		t.Errorf("Count() = %d after both exits were seen", n)
	}
	if exists(m.reg.path(b.ID)) || exists(b.Dir()) {
		t.Error("the second session was not let go of")
	}
}

// Close kills every ffmpeg first and then waits for all of them together, for no
// more than CloseWait, however many there are: it is not the shutdown that waits for
// a read that will not return. What it leaves is for the next start to finish.
func TestCloseBoundsItsWait(t *testing.T) {
	f := newFake(t)
	registry := filepath.Join(t.TempDir(), "sessions")
	const wait = time.Second
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 3; o.RegistryDir = registry; o.CloseWait = wait })
	release := m.HoldExits()
	t.Cleanup(release)

	var sessions []*Session
	var leaders []int
	for range 3 {
		s, err := m.Create(context.Background(), fakeRequest())
		if err != nil {
			t.Fatal(err)
		}
		sessions, leaders = append(sessions, s), append(leaders, leaderOf(s))
	}
	for _, pid := range leaders { // positive control
		if !pidAlive(pid) {
			t.Fatalf("setup: ffmpeg %d is not running", pid)
		}
	}

	closed := make(chan time.Duration, 1)
	start := time.Now()
	go func() { m.Close(); closed <- time.Since(start) }()

	// All of them are killed at once, while Close is still waiting for the first.
	waitUntil(t, "every ffmpeg to be killed", wait/2, func() bool {
		return !slices.ContainsFunc(leaders, pidAlive)
	})
	select {
	case d := <-closed:
		t.Fatalf("Close came back after %v, before its wait was over (the exits are held)", d)
	default:
	}

	var took time.Duration
	select {
	case took = <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not come back")
	}
	if took < wait || took > wait*3/2 {
		t.Errorf("Close took %v with three ffmpeg that are not seen to exit, want CloseWait (%v) once, not once for each", took, wait)
	}

	// What the exits have not released is left for the next start.
	if !exists(m.Dir()) {
		t.Error("Close removed the directory of sessions that have not let go of it")
	}
	for _, s := range sessions {
		if !exists(m.reg.path(s.ID)) || !exists(s.Dir()) {
			t.Errorf("session %s let go of before the exit of its ffmpeg was seen: registry entry %v, dir %v",
				s.ID, exists(m.reg.path(s.ID)), exists(s.Dir()))
		}
	}
	if n := m.Count(); n != 3 {
		t.Errorf("Count() = %d, want 3 (none has exited as far as the manager knows)", n)
	}

	release()
	waitUntil(t, "the sessions to be let go of", 5*time.Second, func() bool { return m.Count() == 0 })
	for _, s := range sessions {
		if exists(m.reg.path(s.ID)) || exists(s.Dir()) {
			t.Errorf("session %s not let go of after its exit was seen", s.ID)
		}
	}
}

// With nothing held back, Close still waits for the exits (and so the directory is
// gone when it returns), as before.
func TestCloseWaitsForExitsThatComeInTime(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.MaxSessions = 3 })
	var dirs []string
	for range 3 {
		s, err := m.Create(context.Background(), fakeRequest())
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, s.Dir())
	}
	if !exists(m.Dir()) || !exists(dirs[0]) { // positive control
		t.Fatalf("setup: %s %v", m.Dir(), dirs)
	}
	if took := answered(t, "Close", m.Close); took > 1500*time.Millisecond {
		t.Errorf("Close took %v", took)
	}
	if exists(m.Dir()) || m.Count() != 0 {
		t.Errorf("after Close: dir %v, Count() = %d", exists(m.Dir()), m.Count())
	}
}

// A seek kills the ffmpeg and waits for its exit under the session lock, and a stop
// needs that lock to finish. With the exit not seen (a read from a share that is gone)
// the stop, and so Close and Delete, must not wait for it, and the seek must not go on
// to start another ffmpeg when the wait is over: it would run in a session that is gone.
func TestStopDuringASeekWaitingForAStuckExit(t *testing.T) {
	for _, how := range []string{"Close", "Delete"} {
		t.Run(how, func(t *testing.T) {
			f := newFake(t)
			m := fakeManager(t, f, func(o *Options) { o.CloseWait = 300 * time.Millisecond })
			release := m.HoldExits()
			t.Cleanup(release)
			ctx := context.Background()
			s, err := m.Create(ctx, fakeRequest())
			if err != nil {
				t.Fatal(err)
			}
			seg, err := s.OpenSegment(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			seg.Close()
			leader := leaderOf(s)

			seeked := make(chan error, 1)
			go func() { // far away: kills the ffmpeg and waits for its exit
				c, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				_, err := s.OpenSegment(c, 100)
				seeked <- err
			}()
			waitUntil(t, "the seek to kill the ffmpeg", 5*time.Second, func() bool { return !pidAlive(leader) })
			// Positive control: the seek is waiting for the exit, holding the lock.
			if s.mu.TryLock() {
				s.mu.Unlock()
				t.Fatal("setup: the session lock is free while a seek waits for the exit")
			}

			var took time.Duration
			if how == "Close" {
				took = answered(t, "Close", m.Close)
			} else {
				took = answered(t, "Delete", func() { m.Delete(s.ID) })
			}
			if took > 1500*time.Millisecond {
				t.Errorf("%s took %v with the exit of a replaced ffmpeg not seen", how, took)
			}
			select {
			case err := <-seeked:
				if !errors.Is(err, ErrClosed) {
					t.Errorf("the seek of a stopped session = %v, want ErrClosed", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("the seek did not come back after the stop")
			}
			if n := m.Count(); n != 1 { // positive control: the exit is still held back
				t.Errorf("Count() = %d with the exit not seen, want 1", n)
			}

			release()
			waitUntil(t, "the session to be let go of", 5*time.Second, func() bool { return m.Count() == 0 })
			s.mu.Lock()
			runs := s.runSeq
			s.mu.Unlock()
			if runs != 1 {
				t.Errorf("%d ffmpeg runs were started, want 1: the seek started another after the stop", runs)
			}
		})
	}
}

// The same wait is in the plan guard, which stops the run from the monitor, under the
// lock, when a fragment shows the index wrong.
func TestStopDuringTheGuardsWaitForAStuckExit(t *testing.T) {
	for _, path := range []string{"refute", "fail"} {
		t.Run(path, func(t *testing.T) {
			f := newFake(t)
			m := fakeManager(t, f, func(o *Options) { o.Tick = time.Hour }) // no monitor to compare anything
			release := m.HoldExits()
			t.Cleanup(release)
			s, err := m.Create(context.Background(), fakeRequest())
			if err != nil {
				t.Fatal(err)
			}
			g := newPlanGuard(nil, 0, time.Now().Add(time.Minute))
			s.mu.Lock()
			s.guard = g
			s.mu.Unlock()
			leader := leaderOf(s)

			returned := make(chan struct{})
			go func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if path == "refute" {
					s.refuteLocked(0, "a test")
				} else {
					s.failLocked(0, "a test")
				}
				close(returned)
			}()
			waitUntil(t, "the guard to kill the ffmpeg", 5*time.Second, func() bool { return !pidAlive(leader) })
			select { // positive control: it is waiting for the exit
			case <-returned:
				t.Fatal("setup: the guard did not wait for the exit")
			case <-time.After(200 * time.Millisecond):
			}

			gone := s.stop()
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("the stop did not cut short the guard's wait for the exit")
			}
			if path == "refute" && (!g.final() || !errors.Is(g.err, ErrClosed)) {
				t.Errorf("the plan of a session stopped during its refutation: final %v, err %v, want ErrClosed", g.final(), g.err)
			}
			select {
			case <-gone:
				t.Error("the session was let go of before the exit was seen")
			default:
			}
			release()
			select {
			case <-gone:
			case <-time.After(5 * time.Second):
				t.Error("the session was not let go of after the exit was seen")
			}
		})
	}
}

// Stop sets s.closed only when shutdown has the lock, and a request that holds it first
// must not start an ffmpeg meanwhile: nobody would wait for it but the kill that follows.
func TestNoFFmpegStartsOnceStopHasBeenCalled(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, nil)
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}

	// A request holds the lock, and the ffmpeg of the session is over (it ended by itself, say).
	s.mu.Lock()
	if !s.stopRunLocked() || s.run != nil {
		t.Fatal("setup: the run was not stopped")
	}
	gone := s.stop() // shutdown now waits for the lock
	err = s.startRunLocked(5)
	runs := s.runSeq
	s.mu.Unlock()
	if !errors.Is(err, ErrClosed) {
		t.Errorf("startRunLocked of a session that has been told to stop = %v, want ErrClosed", err)
	}
	if runs != 1 {
		t.Errorf("%d ffmpeg runs were started, want 1", runs)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Error("the session was not let go of")
	}
}

// The stand-in for `ply --reap` of TestExitingWithoutWaitingLeavesNoFFmpeg: this
// test binary, run as the reaper.
func TestHelperReaps(t *testing.T) {
	if os.Getenv("PLY_TEST_REAPER") == "" {
		t.Skip("reaper process of TestExitingWithoutWaitingLeavesNoFFmpeg")
	}
	os.Exit(ReapMain(flag.Args()[1:])) // flag.Args() is "--reap", pgid, ppid
}

// A helper process, not a test: a manager with one session whose ffmpeg cannot be
// killed (as one that is stuck in a read), told to close, and then to exit. It says
// "ready <pid>" when the session is there, "closed" when Close has come back, and
// goes when it is told on its standard input.
func TestHelperClosesAndExits(t *testing.T) {
	root := os.Getenv("PLY_TEST_HELPER_ROOT")
	if root == "" {
		t.Skip("helper process of TestExitingWithoutWaitingLeavesNoFFmpeg")
	}
	m, err := NewManager(Options{FFmpeg: os.Getenv("PLY_TEST_HELPER_FFMPEG"), TempRoot: root, Cache: fixedKeyframes{},
		RegistryDir: os.Getenv("PLY_TEST_HELPER_REGISTRY"), Reaper: os.Getenv("PLY_TEST_HELPER_REAPER"),
		Tick: 20 * time.Millisecond, IdleTimeout: time.Hour, CloseWait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	m.noKill = true
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready", leaderOf(s))
	in := bufio.NewReader(os.Stdin)
	_, _ = in.ReadString('\n') // the test has looked at the session
	m.Close()
	fmt.Println("closed")
	_, _ = in.ReadString('\n')
	os.Exit(0)
}

// What stops an ffmpeg that the shutdown could not wait for is not ply's waiting but
// the guard made for the moment ply is gone (the reaper of its group on macOS, the
// parent-death signal on Linux). Close must leave that guard in place until the
// process has been seen to exit; here it never is, and ply exits without waiting.
func TestExitingWithoutWaitingLeavesNoFFmpeg(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	reaper := ""
	if reaperWanted { // macOS: this test binary is the reaper
		reaper = filepath.Join(dir, "reaper")
		script := fmt.Sprintf("#!/bin/sh\nPLY_TEST_REAPER=1 exec %q -test.run='^TestHelperReaps$' -- \"$@\"\n", os.Args[0])
		if err := writeExecutable(reaper, script); err != nil {
			t.Fatal(err)
		}
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperClosesAndExits$")
	helper.Env = append(os.Environ(), "PLY_TEST_HELPER_ROOT="+filepath.Join(dir, "tmp"), "PLY_TEST_HELPER_FFMPEG="+f.ffmpeg,
		"PLY_TEST_HELPER_REGISTRY="+filepath.Join(dir, "sessions"), "PLY_TEST_HELPER_REAPER="+reaper)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	var leader int
	t.Cleanup(func() { // whatever a failing test leaves behind
		_ = helper.Process.Kill()
		_ = helper.Wait()
		if leader > 1 {
			_ = syscall.Kill(-leader, syscall.SIGKILL)
		}
	})

	lines := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func(want string) string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok || !strings.HasPrefix(l, want) {
				t.Fatalf("the helper said %q (open: %v), want %q", l, ok, want)
			}
			return l
		case <-time.After(20 * time.Second):
			t.Fatalf("the helper did not say %q", want)
			return ""
		}
	}
	leader, err = strconv.Atoi(strings.TrimPrefix(next("ready "), "ready "))
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg is up and producing: only the guard can end it from here on.
	waitUntil(t, "ffmpeg to produce a fragment", 10*time.Second, func() bool {
		g, _ := filepath.Glob(filepath.Join(dir, "tmp", "*", "*", "gop*.m4s"))
		return len(g) > 0
	})
	if !pidAlive(leader) || groupOf(leader) != leader {
		t.Fatalf("setup: ffmpeg %d is not running as a group leader", leader)
	}

	if _, err := fmt.Fprintln(stdin, "close"); err != nil {
		t.Fatal(err)
	}
	next("closed")
	// Positive control: the kill was disabled, Close has come back, and the process
	// is running still; nothing ply does from here on stops it.
	if !pidAlive(leader) || strings.HasPrefix(procState(leader), "Z") {
		t.Fatalf("setup: ffmpeg %d did not survive the Close of a manager whose kill does nothing", leader)
	}

	if _, err := fmt.Fprintln(stdin, "exit"); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	exited := time.Now()
	waitUntil(t, "the group to be killed after ply exited without waiting", 2*time.Second, func() bool {
		return errors.Is(syscall.Kill(-leader, 0), syscall.ESRCH)
	})
	t.Logf("the group was gone %v after ply exited", time.Since(exited).Round(time.Millisecond))
}
