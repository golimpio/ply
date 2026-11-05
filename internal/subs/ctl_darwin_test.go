package subs

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A run that a person starts to wait for leaves the macOS background band (throttled CPU and I/O)
// at once, not at its next scheduling decision: the viewer's subtitles are not made at the pace of
// a background job. When the last one leaves it goes back.
func TestWaitingTakesARunOutOfTheBackgroundBand(t *testing.T) {
	pri := func(pid int) int {
		t.Helper()
		out, err := exec.Command("ps", "-o", "pri=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		var p int
		if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &p); err != nil {
			t.Fatalf("ps output %q: %v", out, err)
		}
		return p
	}
	cmd := exec.Command("yes")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (priority %d)", what, pri(cmd.Process.Pid))
			}
		}
	}

	var c ctl
	c.started(cmd.Process.Pid, 10)
	until("the background run to be in the background band", func() bool { return pri(cmd.Process.Pid) <= 4 })

	c.wait()
	until("the run somebody waits for to leave the band", func() bool { return pri(cmd.Process.Pid) > 4 })

	c.unwait()
	until("the abandoned run to go back to the band", func() bool { return pri(cmd.Process.Pid) <= 4 })
}
