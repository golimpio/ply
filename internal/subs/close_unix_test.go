//go:build unix

package subs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Shutdown must not leave ffmpeg reading a big file for minutes, nor half-written
// files: Close kills the process group and removes the temp file.
func TestCloseStopsConversionsInProgress(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	script := filepath.Join(dir, "ffmpeg")
	// Like ffmpeg: starts writing the output (the last argument), has a helper
	// process, and takes its time.
	body := "#!/bin/sh\nfor last; do :; done\necho partial > \"$last\"\nsleep 300 &\necho \"$$ $!\" > " + pids + ".tmp\nmv " + pids + ".tmp " + pids + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Dir: filepath.Join(dir, "subs"), FFmpeg: script})
	if err != nil {
		t.Fatal(err)
	}
	src := Source{ItemID: 1, Key: "2", Input: existingFile(t, "a.mkv"), Stream: 2, MTime: 5}

	pathErr := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src); pathErr <- err }()

	var leader, helper int
	waitFor(t, "ffmpeg and its helper to run", func() bool {
		b, err := os.ReadFile(pids)
		if err != nil {
			return false
		}
		_, err = fmt.Sscan(string(b), &leader, &helper)
		return err == nil
	})
	tmp := filepath.Join(g.dir, cacheName(src)+".tmp")
	if _, err := os.Stat(tmp); err != nil { // positive control: there is a half-written file to clean up
		t.Fatalf("setup: no temp file: %v", err)
	}
	if !alive(leader) || !alive(helper) {
		t.Fatalf("setup: processes %d and %d are not both running", leader, helper)
	}

	t.Cleanup(func() { // whatever a failing test leaves behind (the fake sleeps for 300 s); the helper is in the leader's group
		_ = syscall.Kill(-leader, syscall.SIGKILL)
	})

	// In a goroutine, so that a Close that never cancels fails here instead of
	// hanging the test for as long as the conversion runs.
	closed := make(chan struct{})
	start := time.Now()
	go func() { g.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: it does not cancel the conversion in progress")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Close took %v", d)
	}
	// No polling from here on: Close waits for the conversions, so when it
	// returns the conversion is over, its ffmpeg has been waited for and its
	// half-written file is gone.
	g.mu.Lock()
	inflight := len(g.inflight)
	g.mu.Unlock()
	if inflight != 0 {
		t.Errorf("%d conversions still in flight after Close returned", inflight)
	}
	if alive(leader) {
		t.Errorf("ffmpeg (pid %d) still running after Close returned", leader)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temp file left behind after Close returned: %v", err)
	}
	// The helper is a grandchild, reaped by init some moments after it is killed.
	waitFor(t, "ffmpeg's helper to be gone", func() bool { return !alive(helper) })
	select {
	case err := <-pathErr:
		if err == nil || errors.Is(err, ErrConvert) {
			t.Errorf("the waiting request got %v, want a cancellation (not a remembered conversion failure)", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the request waiting for the conversion never returned")
	}
	if _, err := g.Path(context.Background(), Source{ItemID: 2, Key: "1", Input: src.Input, Stream: 1, MTime: 1}); !errors.Is(err, ErrClosed) {
		t.Errorf("Path after Close = %v, want ErrClosed", err)
	}
	g.Close() // idempotent
	if len(g.failed) != 0 {
		t.Errorf("shutdown was remembered as a failure: %v", g.failed)
	}
}
