//go:build unix

package proc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// OwnGroup gives the child a group of its own, and cancelling the command kills the
// group, a helper of the child included; Start tells the caller how it ended.
func TestOwnGroupAndStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & wait")
	OwnGroup(cmd)
	exited := make(chan error, 1)
	if err := Start(cmd, func(err error) { exited <- err }); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if pg, err := syscall.Getpgid(pid); err != nil || pg != pid {
		t.Fatalf("group of the child = %d, %v; want its own pid %d", pg, err, pid)
	}
	waitUntil(t, "the helper in the group", 5*time.Second, func() bool { return syscall.Kill(-pid, 0) == nil })

	cancel()
	select {
	case err := <-exited:
		if err == nil {
			t.Error("a killed command ended well")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start never said the command ended")
	}
	waitUntil(t, "the whole group to be gone", 5*time.Second, func() bool { return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) })
}

// A command that cannot start says so, and calls nothing.
func TestStartReportsTheFailureToStart(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/no/such/program")
	OwnGroup(cmd)
	called := make(chan struct{}, 1)
	if err := Start(cmd, func(error) { called <- struct{}{} }); err == nil {
		t.Fatal("Start of a missing program returned no error")
	}
	select {
	case <-called:
		t.Error("exited was called for a command that never started")
	case <-time.After(100 * time.Millisecond):
	}
}

// StopReaper ends a process and waits for it; nil is fine.
func TestStopReaper(t *testing.T) {
	StopReaper(nil)
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	StopReaper(cmd)
	if err := syscall.Kill(cmd.Process.Pid, 0); err == nil {
		t.Error("the reaper is still there")
	}
}

// The link has the reaper's name; where it cannot be made, the program itself runs.
func TestReaperLink(t *testing.T) {
	dir := t.TempDir()
	link := ReaperLink(dir, "/bin/ply", nil)
	if want := dir + "/ply-reaper"; link != want {
		t.Fatalf("link = %q, want %q", link, want)
	}
	if target, err := os.Readlink(link); err != nil || target != "/bin/ply" {
		t.Errorf("the link points at %q, %v", target, err)
	}
	// Again (an earlier start left one): replaced, not an error.
	if again := ReaperLink(dir, "/bin/ply2", nil); again != link {
		t.Errorf("second link = %q", again)
	}
	if target, _ := os.Readlink(link); target != "/bin/ply2" {
		t.Errorf("the link was not replaced: %q", target)
	}
	if got := ReaperLink("", "/bin/ply", nil); got != "/bin/ply" {
		t.Errorf("with no directory = %q, want the program", got)
	}
}
