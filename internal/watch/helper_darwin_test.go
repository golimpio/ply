package watch

import (
	"syscall"
	"testing"
	"time"
)

// ply's close returns at once while the helper, in a session of its own (not the
// terminal's job, nor sent its Ctrl-C), takes its time to let go of the watches.
// Darwin only: the helper is macOS's backend, and Linux's syscall has no Getsid.
func TestHelperCloseDoesNotWaitForTheWatchesToGo(t *testing.T) {
	b := startTestHelper(t, "PLY_TEST_SLOW_CLOSE=3s")
	if err := b.add(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	pid := b.cmd.Process.Pid
	helperSid, err := syscall.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if ownSid, _ := syscall.Getsid(0); helperSid == ownSid || helperSid != pid {
		t.Fatalf("helper session %d, ours %d: want a session of its own", helperSid, ownSid)
	}
	began := time.Now()
	b.close()
	if d := time.Since(began); d > time.Second {
		t.Fatalf("close took %v: it waited for the helper to let go of the watches", d)
	}
	// Control: the helper is still letting go, so the wait above was not exercised for nothing.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("helper already gone (%v): its slow close did not run", err)
	}
}
