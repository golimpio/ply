//go:build unix

package stream

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidAlive reports whether the process exists (a zombie counts).
func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func procState(pid int) string {
	out, _ := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out))
}

// A fake ffmpeg that behaves like the worst case of a throttled one: it has a
// helper process (like a real ffmpeg's children) and is itself stopped with
// SIGSTOP. Nothing short of SIGKILL reaches it, and it has to be sent to the
// whole process group to reach the helper.
func stoppedTreeFake(t *testing.T) (ffmpeg, helperPidFile string) {
	t.Helper()
	dir := t.TempDir()
	ffmpeg, helperPidFile = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "helper.pid")
	script := fmt.Sprintf("#!/bin/sh\nsleep 300 &\necho $! > %[1]q.tmp\nmv %[1]q.tmp %[1]q\nkill -STOP $$\nexec sleep 300\n", helperPidFile)
	if err := writeExecutable(ffmpeg, script); err != nil {
		t.Fatal(err)
	}
	return ffmpeg, helperPidFile
}

type stoppedTree struct {
	m              *Manager
	s              *Session
	leader, helper int
}

// startStoppedTree creates a session on the stopped-tree fake and waits until
// the tree is in place: both processes alive, the leader stopped.
func startStoppedTree(t *testing.T, mod func(*Options)) stoppedTree {
	t.Helper()
	ffmpeg, helperPidFile := stoppedTreeFake(t)
	m := fakeManager(t, fake{ffmpeg: ffmpeg}, mod)
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	leader := s.run.cmd.Process.Pid
	s.mu.Unlock()
	var helper int
	waitUntil(t, "the helper process to start", 10*time.Second, func() bool {
		s.touch() // a slow start must not let a short idle timeout end the session first
		b, err := os.ReadFile(helperPidFile)
		if err != nil {
			return false
		}
		helper, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	waitUntil(t, "the leader to stop itself", 10*time.Second, func() bool {
		s.touch()
		return strings.HasPrefix(procState(leader), "T")
	})
	t.Cleanup(func() { // whatever a failing test leaves behind
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		_ = syscall.Kill(helper, syscall.SIGKILL)
	})
	// Positive control: this is a live tree, so the checks below can fail.
	if !pidAlive(leader) || !pidAlive(helper) {
		t.Fatalf("setup: leader %d alive=%v, helper %d alive=%v", leader, pidAlive(leader), helper, pidAlive(helper))
	}
	return stoppedTree{m: m, s: s, leader: leader, helper: helper}
}

// gone waits for both processes to be gone. The leader has been waited for by
// the time the manager reports back; the helper is reaped by init.
func (st stoppedTree) wantGone(t *testing.T, how string) {
	t.Helper()
	if pidAlive(st.leader) {
		t.Errorf("the stopped ffmpeg (pid %d) survived %s", st.leader, how)
	}
	waitUntil(t, "the ffmpeg helper process to be gone after "+how, 2*time.Second, func() bool { return !pidAlive(st.helper) })
}

// Each way a session ends must kill a stopped ffmpeg and its helper at once:
// SIGKILL to the process group. SIGTERM to the pid, or no group, only shows
// up as WaitDelay's 3 s fallback (and a surviving helper).
func TestStoppedFFmpegIsKilledWithItsGroup(t *testing.T) {
	const promptly = 1500 * time.Millisecond // WaitDelay is 3 s

	t.Run("delete", func(t *testing.T) {
		st := startStoppedTree(t, nil)
		start := time.Now()
		if !st.m.Delete(st.s.ID) {
			t.Fatal("Delete found no session")
		}
		if d := time.Since(start); d > promptly {
			t.Errorf("Delete took %v: the stopped ffmpeg was not killed outright", d)
		}
		st.wantGone(t, "Delete")
	})

	t.Run("idle", func(t *testing.T) {
		st := startStoppedTree(t, func(o *Options) { o.IdleTimeout = 300 * time.Millisecond })
		start := time.Now()
		waitUntil(t, "the idle session to end", 10*time.Second, func() bool { return st.m.Count() == 0 })
		if d := time.Since(start); d > promptly+300*time.Millisecond {
			t.Errorf("idle expiry took %v: the stopped ffmpeg was not killed outright", d)
		}
		st.wantGone(t, "idle expiry")
	})

	t.Run("close", func(t *testing.T) {
		st := startStoppedTree(t, nil)
		start := time.Now()
		st.m.Close()
		if d := time.Since(start); d > promptly {
			t.Errorf("Close took %v: the stopped ffmpeg was not killed outright", d)
		}
		st.wantGone(t, "Close")
	})

	t.Run("seek", func(t *testing.T) {
		// A restart replaces a running ffmpeg the same way.
		st := startStoppedTree(t, func(o *Options) { o.WaitTimeout = 500 * time.Millisecond })
		start := time.Now()
		_, _ = st.s.OpenSegment(context.Background(), 100) // times out: the new fake stops itself too
		if d := time.Since(start); d > 500*time.Millisecond+promptly {
			t.Errorf("the seek took %v", d)
		}
		if pidAlive(st.leader) {
			t.Errorf("the replaced ffmpeg (pid %d) survived the seek", st.leader)
		}
		waitUntil(t, "the replaced ffmpeg's helper to be gone", 2*time.Second, func() bool { return !pidAlive(st.helper) })
	})
}

// Count() is how callers (and the idle path) learn that a session is over, so
// it must not drop before the ffmpeg is dead and the directory is gone.
func TestCountDropsOnlyWhenTheSessionIsGone(t *testing.T) {
	f := newFake(t)
	m := fakeManager(t, f, func(o *Options) { o.IdleTimeout = 300 * time.Millisecond })
	s, err := m.Create(context.Background(), fakeRequest())
	if err != nil {
		t.Fatal(err)
	}
	dir := s.Dir()
	s.mu.Lock()
	leader := s.run.cmd.Process.Pid
	s.mu.Unlock()
	waitUntil(t, "ffmpeg running", 5*time.Second, func() bool { return pidAlive(leader) })

	for m.Count() > 0 { // no sleeping: look at the very moment it drops
		runtime.Gosched()
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Count() reached 0 while the session dir was still there: %v", err)
	}
	if pidAlive(leader) {
		t.Errorf("Count() reached 0 while ffmpeg (pid %d) was still alive", leader)
	}
}

// A helper process, not a test: it runs a manager with one session on the fake
// ffmpeg named by the environment, reports "ready" and sits there until it is
// killed, like a ply that is about to crash.
func TestHelperHoldsASession(t *testing.T) {
	root, ffmpeg := os.Getenv("PLY_TEST_HELPER_ROOT"), os.Getenv("PLY_TEST_HELPER_FFMPEG")
	if root == "" {
		t.Skip("helper process of TestKilledServerLeavesNoFFmpeg")
	}
	m, err := NewManager(Options{FFmpeg: ffmpeg, TempRoot: root, Cache: fixedKeyframes{},
		Tick: 20 * time.Millisecond, IdleTimeout: time.Hour, ThrottleHigh: 1000, ThrottleLow: 500})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), fakeRequest()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	select {}
}

// ffmpeg has its own process group, so when ply is killed (SIGKILL, OOM) it
// lives on, holding its source file and burning CPU (a transcode of a long
// film can run for hours) until it fails to write into the directory that the
// next ply deletes. (A throttled, stopped ffmpeg is different: the kernel
// sends SIGHUP and SIGCONT to a stopped orphaned group, and ffmpeg exits on
// SIGHUP.) The next ply must find and kill it.
func TestKilledServerLeavesNoFFmpeg(t *testing.T) {
	f := newFake(t)
	root := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsASession$")
	helper.Env = append(os.Environ(), "PLY_TEST_HELPER_ROOT="+root, "PLY_TEST_HELPER_FFMPEG="+f.ffmpeg)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
		for _, l := range f.procs(t) { // whatever a failing test leaves behind
			if pid, err := strconv.Atoi(strings.Fields(l)[0]); err == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "ready" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the helper process exited before it was ready")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the helper process did not become ready")
	}

	var pid int
	waitUntil(t, "ffmpeg running", 10*time.Second, func() bool {
		p := f.procs(t)
		if len(p) != 1 {
			return false
		}
		pid, _ = strconv.Atoi(strings.Fields(p[0])[0])
		return true
	})

	// The crash. The orphan is still there: this is the bug's starting point.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	time.Sleep(200 * time.Millisecond)
	if !pidAlive(pid) {
		t.Fatalf("setup: ffmpeg (pid %d) died with its parent; nothing to clean up", pid)
	}

	m, err := NewManager(Options{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waitUntil(t, "the left-behind ffmpeg to be killed", 5*time.Second, func() bool { return !pidAlive(pid) })
	if entries, _ := os.ReadDir(root); len(entries) != 1 || entries[0].Name() != strconv.Itoa(os.Getpid()) {
		t.Errorf("the dead server's directory was not removed: %v", entries)
	}
}

// A pid file whose pid now belongs to something else must not get that
// something killed.
func TestStalePidFileOfAnotherProgramIsIgnored(t *testing.T) {
	bystander := exec.Command("sleep", "300")
	bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a group leader, like ffmpeg
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	root := t.TempDir()
	session := filepath.Join(root, "2147483000", "abc")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf("%d /usr/bin/ffmpeg\n", bystander.Process.Pid) // the pid was reused by `sleep`
	if err := os.WriteFile(filepath.Join(session, pidFile), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(Options{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := os.Stat(filepath.Join(root, "2147483000")); !os.IsNotExist(err) {
		t.Errorf("stale dir survived: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !pidAlive(bystander.Process.Pid) || strings.HasPrefix(procState(bystander.Process.Pid), "Z") {
		t.Error("an unrelated process that reused the recorded pid was killed")
	}
}

// removeStale used to delete whatever the temp root pointed to.
func TestSymlinkedTempRootIsRefused(t *testing.T) {
	target := t.TempDir()
	victim := filepath.Join(target, "precious.txt")
	victimDir := filepath.Join(target, "12345") // would even look like a stale session
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "ply-sessions")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	m, err := NewManager(Options{TempRoot: link})
	if err == nil {
		m.Close()
		t.Fatal("NewManager accepted a symlinked temp root")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a file behind the symlink was deleted: %v", err)
	}
	if _, err := os.Stat(victimDir); err != nil {
		t.Errorf("a directory behind the symlink was deleted: %v", err)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 2 {
		t.Errorf("the symlink's target was modified: %v", entries)
	}

	// Positive control: a real directory in the same place is fine.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(Options{TempRoot: link})
	if err != nil {
		t.Fatalf("a real temp root was refused: %v", err)
	}
	m.Close()
}

func TestTempRootOwnerIsChecked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("everything is ours to root")
	}
	mine, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByUs(mine) {
		t.Error("our own directory is not ours")
	}
	rootDir, err := os.Lstat("/")
	if err != nil {
		t.Fatal(err)
	}
	if ownedByUs(rootDir) {
		t.Error("/ (owned by root) counts as ours")
	}
	if err := prepareRoot("/"); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Errorf("prepareRoot(/) = %v, want a refusal", err)
	}
}

func TestTempRootIsCreatedPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ply-sessions")
	m, err := NewManager(Options{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if fi, err := os.Stat(root); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("temp root: %v, %v; want mode 0700", fi, err)
	}
	// A directory of ours that was made too open is tightened, not refused.
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(Options{TempRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if fi, _ := os.Stat(root); fi.Mode().Perm() != 0o700 {
		t.Errorf("temp root mode = %v, want 0700", fi.Mode().Perm())
	}
}
