package art

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The art of the item being opened is grabbed with niceness 0: it must not be
// put under the macOS background policy. A niceness demotes.
func TestSetNiceZeroDoesNotDemote(t *testing.T) {
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
	child := func() int {
		t.Helper()
		cmd := exec.Command("yes")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}

	asked := child()
	setNice(asked, 0)
	time.Sleep(300 * time.Millisecond) // the policy shows at the next scheduling decision
	if p := pri(asked); p <= 4 {
		t.Errorf("setNice(pid, 0) left the child at priority %d, the background band", p)
	}

	// Positive control: with a niceness the same hook demotes.
	background := child()
	setNice(background, 10)
	deadline := time.Now().Add(3 * time.Second)
	for pri(background) > 4 {
		if time.Now().After(deadline) {
			t.Fatalf("setNice(pid, 10): priority %d, want the background band", pri(background))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
