package watch

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMain makes this test binary a watch helper when it is started as one.
// PLY_TEST_SLOW_CLOSE makes its backend slow to let go, like a network share.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		mk := localBackend
		if d, err := time.ParseDuration(os.Getenv("PLY_TEST_SLOW_CLOSE")); err == nil {
			mk = func() (backend, error) {
				be, err := localBackend()
				if err != nil {
					return nil, err
				}
				return slowClose{be, d}, nil
			}
		}
		os.Exit(helperMain(os.Stdin, os.Stdout, mk))
	}
	os.Exit(m.Run())
}

type slowClose struct {
	backend
	d time.Duration
}

func (s slowClose) close() error {
	time.Sleep(s.d)
	return s.backend.close()
}

func startTestHelper(t *testing.T, env ...string) *helperBackend {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, HelperArg)
	cmd.Env = append(os.Environ(), env...)
	be, err := startHelperCmd(cmd)
	if err != nil {
		t.Fatal(err)
	}
	b := be.(*helperBackend)
	t.Cleanup(func() { b.close() })
	return b
}

// The helper's errors keep their errno: the watcher tells a folder that is gone
// or private (skipped) from a limit (the root is given up).
func TestHelperAddErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	touch(t, file, 1)
	b := startTestHelper(t)
	if err := b.add(root); err != nil { // control
		t.Fatal(err)
	}
	if err := b.add(filepath.Join(root, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v, want not exist", err)
	}
	if err := b.add(file); err == nil {
		t.Fatal("a file was watched")
	}
	b.close()
	if err := b.add(root); !errors.Is(err, errBackendClosed) {
		t.Fatalf("add after close: %v", err)
	}
	if _, ok := <-b.events(); ok {
		t.Fatal("events channel not closed")
	}
}

// ply's close returns at once while the helper, in a session of its own (not the
// terminal's job, nor sent its Ctrl-C), takes its time to let go of the watches.
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

// A helper that dies closes the backend's channels (the watcher falls back to
// the periodic scans), and a request gets an error rather than waiting forever.
func TestHelperDeathEndsTheBackend(t *testing.T) {
	b := startTestHelper(t)
	root := t.TempDir()
	if err := b.add(root); err != nil {
		t.Fatal(err)
	}
	if err := b.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-b.events():
		case <-deadline:
			t.Fatal("events channel still open after the helper died")
		}
	}
	if err := b.add(root); err == nil {
		t.Fatal("add succeeded with the helper dead")
	}
}
