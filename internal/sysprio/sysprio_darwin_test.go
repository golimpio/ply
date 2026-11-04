package sysprio

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// schedState is what the kernel shows of a process: its scheduling priority
// (31 for a normal user process, 4 under the background policy, which
// getpriority(2) cannot report for another process) and its niceness.
func schedState(t *testing.T, pid int) (pri, nice int) {
	t.Helper()
	out, err := exec.Command("ps", "-o", "pri=,nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &pri, &nice); err != nil {
		t.Fatalf("ps output %q: %v", out, err)
	}
	return pri, nice
}

// waitState polls until ok holds for pid's state (the priority follows the
// policy at the next scheduling decision).
func waitState(t *testing.T, pid int, what string, ok func(pri, nice int) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pri, nice := schedState(t, pid)
		if ok(pri, nice) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: priority %d, nice %d", what, pri, nice)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundAndForegroundOnARealChild(t *testing.T) {
	// A busy child, so that its priority is recomputed all the time.
	cmd := exec.Command("yes")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pid := cmd.Process.Pid

	normal := func(pri, nice int) bool { return pri > 20 && nice == 0 }
	// Positive control: a new child runs at the normal priority.
	waitState(t, pid, "new child, want normal", normal)

	throttled := func(pri, nice int) bool { return pri <= 4 && nice == 10 }
	Background(pid, 10)
	waitState(t, pid, "after Background, want throttled and nice 10", throttled)

	// Foreground lifts the policy; the niceness stays (only root may lower it;
	// ps shows the base priority, which a niceness does not move).
	Foreground(pid)
	waitState(t, pid, "after Foreground, want out of the background band, still nice 10",
		func(pri, nice int) bool { return pri > 20 && nice == 10 })

	// And back again: promotion and demotion can alternate.
	Background(pid, 10)
	waitState(t, pid, "after a second Background, want throttled", throttled)
}
