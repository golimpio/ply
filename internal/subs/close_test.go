package subs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// An ffmpeg in a read from a share that is slow or gone does not exit when it is
// killed, and Close does not wait for it for ever (the shutdown would take as long as
// the read): it comes back after closeWait, and the guard of the group (the kernel, the
// reaper) ends the process when ply does.
func TestCloseDoesNotWaitForARunThatWillNotExit(t *testing.T) {
	f := &fakeRun{started: make(chan struct{}, 1), release: make(chan struct{})}
	g := newGen(t, f)
	g.closeWait = 100 * time.Millisecond
	release := sync.OnceFunc(func() { close(f.release) })
	t.Cleanup(release)
	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}

	pathErr := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src); pathErr <- err }()
	<-f.started // the run is there, and does not look at its context

	closed := make(chan struct{})
	start := time.Now()
	go func() { g.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for the run that does not exit")
	}
	if d := time.Since(start); d < g.closeWait || d > 2*time.Second {
		t.Errorf("Close took %v, want about closeWait (%v)", d, g.closeWait)
	}
	g.mu.Lock()
	inflight := len(g.inflight)
	g.mu.Unlock()
	if inflight != 1 { // positive control: the run was still there when Close came back
		t.Errorf("%d conversions in flight after Close, want the one that does not exit", inflight)
	}

	release()
	select {
	case <-pathErr:
	case <-time.After(5 * time.Second):
		t.Error("the request never returned after its run exited")
	}
}
