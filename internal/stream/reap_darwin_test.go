package stream

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startFakePly starts a process to stand in for ply: something the reaper
// watches, that the test can kill. It is waited for as soon as it dies.
func startFakePly(t *testing.T) (pid int, kill func()) {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	kill = func() { _ = cmd.Process.Kill(); <-done }
	t.Cleanup(kill)
	return cmd.Process.Pid, kill
}

// The reaper kills the ffmpeg group the moment ply is gone: within 2 s, however
// the ply died, and a group that is stopped is no exception.
func TestReaperKillsTheGroupWhenPlyDies(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := map[bool]string{false: "running", true: "stopped"}[stopped]
		t.Run(name, func(t *testing.T) {
			o := startOrphan(t)
			ply, killPly := startFakePly(t)
			if stopped {
				if err := syscall.Kill(-o.leader, syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
				waitUntil(t, "the leader to stop", 5*time.Second, func() bool { return strings.HasPrefix(procState(o.leader), "T") })
			}
			result := make(chan error, 1)
			go func() { result <- reap(o.leader, ply) }()

			// While ply lives the reaper does nothing (a positive control for the kill).
			o.wantAlive(t, "the reaper acted while ply was still running")
			select {
			case err := <-result:
				t.Fatalf("the reaper returned (%v) while ply was running", err)
			default:
			}

			killPly()
			killed := time.Now()
			o.wantDead(t, "ply died")
			if d := time.Since(killed); d > 2*time.Second {
				t.Errorf("the group lived %v after ply died, want under 2 s", d)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Errorf("reap = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Error("the reaper did not return after it had killed the group")
			}
		})
	}
}

// If ffmpeg ends first, the reaper has nothing to do and goes; it must not kill
// a group when ply dies later (the pid may be somebody else's by then).
func TestReaperGoesWhenTheGroupIsGone(t *testing.T) {
	o := startOrphan(t)
	ply, killPly := startFakePly(t)
	result := make(chan error, 1)
	go func() { result <- reap(o.leader, ply) }()
	time.Sleep(200 * time.Millisecond)

	_ = syscall.Kill(-o.leader, syscall.SIGKILL)
	o.wantDead(t, "being killed")
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("reap = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the reaper stayed after its group was gone")
	}
	killPly()
}

// A pid that is not what was started a moment ago is not killed: the reaper is
// handed a group whose leader is not the process it saw (here, one that started
// after it was asked to guard the pid).
func TestReaperLeavesAGroupAloneIfItsLeaderIsNotTheSameProcess(t *testing.T) {
	o := startOrphan(t)
	start, err := processStart(o.leader)
	if err != nil {
		t.Fatal(err)
	}
	if err := killIfSame(o.leader, "not-"+start); err != nil {
		t.Fatal(err)
	}
	o.wantAlive(t, "killIfSame was given another start time")
	if err := killIfSame(o.leader, start); err != nil {
		t.Fatal(err)
	}
	o.wantDead(t, "killIfSame with the right start time") // positive control
}

// A reaper that is started after ply has already died (and its pid, which it was
// given, may be somebody else's by now) finds that its parent is not ply, and kills
// the group at once instead of waiting for a process that is not ply.
func TestAReaperStartedAfterPlyDiedKillsAtOnce(t *testing.T) {
	o := startOrphan(t)
	somebodyElse, _ := startFakePly(t) // alive, and has the pid that ply had

	err := make(chan error, 1)
	go func() { err <- runReaper(o.leader, somebodyElse, 1) }() // adopted by launchd (pid 1)
	o.wantDead(t, "a reaper that was adopted")
	select {
	case e := <-err:
		if e != nil {
			t.Errorf("runReaper = %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Error("the reaper did not return")
	}

	// Positive control: with ply as its parent it waits for it, and kills nothing.
	other := startOrphan(t)
	ply, killPly := startFakePly(t)
	done := make(chan error, 1)
	go func() { done <- runReaper(other.leader, ply, ply) }()
	other.wantAlive(t, "the reaper's parent is alive")
	killPly()
	other.wantDead(t, "ply died")
	<-done
}

func TestReapMainArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"1"}, {"x", "y"}, {"1", "2"}, {"0", "5"}, {"5", "-1"}, {"5", "6", "7"}} {
		if got := ReapMain(args); got != 2 {
			t.Errorf("ReapMain(%v) = %d, want 2 (usage)", args, got)
		}
	}
}
