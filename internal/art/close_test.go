package art

import (
	"context"
	"testing"
	"time"
)

// An ffmpeg in a read from a share that is slow or gone does not exit when it is
// killed, and Close does not wait for it for ever (the shutdown would take as long as
// the read): it comes back after closeWait, and the guard of the group (the kernel, the
// reaper) ends the process when ply does.
func TestCloseDoesNotWaitForARunThatWillNotExit(t *testing.T) {
	const exitTakes = 1500 * time.Millisecond
	s := &stub{block: make(chan struct{}), slowStop: exitTakes}
	g := newTestGenerator(t, t.TempDir(), s)
	g.closeWait = 100 * time.Millisecond

	done := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src(1, 10), Thumb); done <- err }()
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })

	closed := make(chan struct{})
	start := time.Now()
	go func() { g.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the run that does not exit")
	}
	if d := time.Since(start); d < g.closeWait || d > exitTakes-200*time.Millisecond {
		t.Errorf("Close took %v, want about closeWait (%v), not the %v the run takes to exit", d, g.closeWait, exitTakes)
	}
	if a, _ := s.snapshot(); a != 1 { // positive control: the run was still there when Close came back
		t.Errorf("%d runs in flight after Close, want the one that has not exited", a)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("the request never returned after its run exited")
	}
}
