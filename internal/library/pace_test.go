package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The pace function is asked before every probe, with the progress, and a probe
// does not start while it blocks: that is how a scan yields to playback.
func TestPaceIsAskedBeforeEachProbeAndHoldsIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	for i := range 3 {
		write(t, filepath.Join(root, fmt.Sprintf("Movie %d (2020).mkv", i)), "x")
	}
	s, st, fp := newScanner(t, root)

	var mu sync.Mutex
	var calls [][2]int
	release := make(chan struct{})
	s.SetPace(func(ctx context.Context, done, total int) error {
		mu.Lock()
		calls = append(calls, [2]int{done, total})
		first := len(calls) == 1
		mu.Unlock()
		if first {
			<-release // playback needs the machine: the first probe must wait
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- s.Scan(context.Background()) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(calls)
		mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pace was never asked")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := fp.calls.Load(); n != 0 {
		t.Fatalf("%d probes ran while pace held the scan", n)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := fp.calls.Load(); n != 3 {
		t.Fatalf("%d probes, want 3", n)
	}
	mu.Lock()
	defer mu.Unlock()
	want := [][2]int{{0, 3}, {1, 3}, {2, 3}}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("pace calls = %v, want %v (done of total, before each probe)", calls, want)
	}
	if len(paths(t, st)) != 3 {
		t.Fatal("not every file was indexed")
	}
}

// A pace that gives up (the job was cancelled) stops the probes like the end of
// the context, and the scan keeps what it had; with no pace nothing waits.
func TestPaceErrorStopsProbing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	for i := range 4 {
		write(t, filepath.Join(root, fmt.Sprintf("Movie %d (2020).mkv", i)), "x")
	}
	s, _, fp := newScanner(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	s.SetPace(func(_ context.Context, done, _ int) error {
		if done == 2 {
			cancel()
			return errors.New("cancelled")
		}
		return nil
	})
	_ = s.Scan(ctx)
	if n := fp.calls.Load(); n > 2 {
		t.Fatalf("%d probes after pace gave up at the third", n)
	}

	// Positive control: without a pace every file is probed.
	s2, _, fp2 := newScanner(t, root)
	mustScan(t, s2)
	if n := fp2.calls.Load(); n != 4 {
		t.Fatalf("no pace: %d probes, want 4", n)
	}
}

// A big walk asks the pace function every pacePerDirs directories (0 of 0: no
// progress known yet).
func TestPaceIsAskedDuringALongWalk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	for i := range pacePerDirs + 5 {
		write(t, filepath.Join(root, fmt.Sprintf("d%03d", i), "notes.txt"), "x")
	}
	s, _, _ := newScanner(t, root)
	var walkCalls int
	s.SetPace(func(_ context.Context, done, total int) error {
		if done == 0 && total == 0 {
			walkCalls++
		}
		return nil
	})
	mustScan(t, s)
	if walkCalls != 1 {
		t.Fatalf("pace asked %d times during a walk of %d dirs, want 1", walkCalls, pacePerDirs+5)
	}
}
