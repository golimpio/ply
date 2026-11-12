package watch

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestMain makes this test binary a watch helper when it is started as one.
// PLY_TEST_SLOW_CLOSE makes its backend slow to let go, like a network share;
// PLY_TEST_FLOOD makes it report that many events at once, as a burst of changes would.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		mk := func() (backend, error) {
			be, err := localBackend()
			if err != nil {
				return nil, err
			}
			if d, err := time.ParseDuration(os.Getenv("PLY_TEST_SLOW_CLOSE")); err == nil {
				be = slowClose{be, d}
			}
			if n, err := strconv.Atoi(os.Getenv("PLY_TEST_FLOOD")); err == nil {
				be = newFlooded(be, n)
			}
			return be, nil
		}
		os.Exit(helperMain(os.Stdin, os.Stdout, mk))
	}
	os.Exit(m.Run())
}

// flooded reports n made-up events first, then the backend's own.
type flooded struct {
	backend
	ev chan event
}

func newFlooded(be backend, n int) flooded {
	f := flooded{be, make(chan event)}
	go func() {
		defer close(f.ev)
		for i := range n {
			f.ev <- event{dir: "/flood", name: strconv.Itoa(i), op: opCreate}
		}
		for e := range be.events() {
			f.ev <- e
		}
	}()
	return f
}

func (f flooded) events() <-chan event { return f.ev }

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

// A reply never waits behind the events: while the watcher sets up a tree it reads
// no events, and a burst of them (more than the channel and the pipe hold) held
// every add after it, so the setup never ended and the events were never read.
// The events that do not fit are dropped and said to be lost (errOverflow: the
// watcher looks at its roots again), as the kernel does with its own.
func TestHelperRepliesDoNotWaitBehindEvents(t *testing.T) {
	b := startTestHelper(t, "PLY_TEST_FLOOD=20000")
	done := make(chan error, 1)
	go func() {
		time.Sleep(500 * time.Millisecond) // the burst first
		done <- b.add(t.TempDir())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("add waited for the events to be read")
	}
	select {
	case err := <-b.errors():
		if !errors.Is(err, errOverflow) {
			t.Fatalf("error %v, want the lost events reported as an overflow", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("events were lost without a word")
	}
	if _, ok := <-b.events(); !ok { // control: the ones that fit are there
		t.Fatal("no events at all")
	}
}
