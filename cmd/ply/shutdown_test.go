package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testStopper(every, deadline time.Duration) (stopper, *lockedBuf) {
	var buf lockedBuf
	return stopper{log: slog.New(slog.NewTextHandler(&buf, nil)), every: every, deadline: deadline}, &buf
}

// A slow step is named while it is waited for, so a stop never looks frozen.
func TestShutdownSaysWhatItWaitsFor(t *testing.T) {
	s, out := testStopper(20*time.Millisecond, 5*time.Second)
	var order []string
	ok := s.run([]stopStep{
		{"quick", func() { order = append(order, "quick") }},
		{"library scan", func() { time.Sleep(150 * time.Millisecond); order = append(order, "scan") }},
		{"last", func() { order = append(order, "last") }},
	})
	if !ok {
		t.Fatal("run gave up")
	}
	if strings.Join(order, ",") != "quick,scan,last" {
		t.Errorf("steps ran as %v, want in order", order)
	}
	log := out.String()
	if !strings.Contains(log, `msg="still stopping" step="library scan"`) {
		t.Errorf("the slow step was not reported:\n%s", log)
	}
	if strings.Contains(log, `step=quick`) {
		t.Errorf("a quick step was reported as slow:\n%s", log)
	}
	if !strings.Contains(log, `msg=stopped`) {
		t.Errorf("no final line:\n%s", log)
	}
}

// A step that never ends does not hold the process: the stop gives up and says so.
func TestShutdownGivesUpOnAStuckStep(t *testing.T) {
	s, out := testStopper(10*time.Millisecond, 100*time.Millisecond)
	stuck := make(chan struct{})
	defer close(stuck)
	ran := false
	began := time.Now()
	ok := s.run([]stopStep{
		{"image downloads", func() { <-stuck }},
		{"after", func() { ran = true }},
	})
	if ok {
		t.Fatal("run reported a clean stop")
	}
	if time.Since(began) > 2*time.Second {
		t.Errorf("gave up after %v, want about the deadline", time.Since(began))
	}
	if ran {
		t.Error("a step after the stuck one ran")
	}
	if !strings.Contains(out.String(), `msg="not waiting any longer; exiting" step="image downloads"`) {
		t.Errorf("the give-up was not explained:\n%s", out.String())
	}
}

// A second signal aborts the shutdown immediately without waiting for slow steps or the deadline.
func TestShutdownQuickExitOnSecondSignal(t *testing.T) {
	// Positive control: when quick signal channel is present but receives no signal,
	// slow steps run to completion.
	t.Run("positive control without signal", func(t *testing.T) {
		s, out := testStopper(10*time.Millisecond, 2*time.Second)
		quick := make(chan os.Signal, 1)
		s.quick = quick
		onQuickCalled := false
		s.onQuick = func() { onQuickCalled = true }

		completed := false
		ok := s.run([]stopStep{
			{"slow step", func() {
				time.Sleep(50 * time.Millisecond)
				completed = true
			}},
		})
		if !ok {
			t.Fatal("positive control: run gave up")
		}
		if !completed {
			t.Fatal("positive control: step did not complete")
		}
		if onQuickCalled {
			t.Fatal("positive control: onQuick called without signal")
		}
		if strings.Contains(out.String(), "interrupted") {
			t.Fatalf("positive control: unexpected interrupt log:\n%s", out.String())
		}
	})

	// With quick signal sent while a step is blocked: stopper aborts immediately via onQuick,
	// does not wait for the deadline, logs the interruption, and returns false.
	t.Run("quick signal interrupts running step", func(t *testing.T) {
		s, out := testStopper(10*time.Millisecond, 5*time.Second)
		quick := make(chan os.Signal, 1)
		s.quick = quick
		onQuickCalled := false
		s.onQuick = func() { onQuickCalled = true }

		stuck := make(chan struct{})
		defer close(stuck)
		stepRanAfter := false

		go func() {
			time.Sleep(20 * time.Millisecond)
			quick <- os.Interrupt
		}()

		began := time.Now()
		ok := s.run([]stopStep{
			{"stuck step", func() { <-stuck }},
			{"subsequent step", func() { stepRanAfter = true }},
		})
		elapsed := time.Since(began)

		if ok {
			t.Fatal("run reported clean stop, want false on quick interrupt")
		}
		if !onQuickCalled {
			t.Fatal("onQuick hook was not called")
		}
		if stepRanAfter {
			t.Fatal("step after interrupted step was executed")
		}
		if elapsed > 1*time.Second {
			t.Fatalf("run took %v, want immediate abort well before deadline", elapsed)
		}
		if !strings.Contains(out.String(), `msg="interrupted during shutdown; quitting at once"`) {
			t.Fatalf("interruption was not logged:\n%s", out.String())
		}
	})

	// With quick signal already buffered before step runs: aborts immediately.
	t.Run("buffered signal aborts immediately", func(t *testing.T) {
		s, out := testStopper(10*time.Millisecond, 5*time.Second)
		quick := make(chan os.Signal, 1)
		quick <- os.Interrupt
		s.quick = quick
		onQuickCalled := false
		s.onQuick = func() { onQuickCalled = true }

		stuck := make(chan struct{})
		defer close(stuck)

		began := time.Now()
		ok := s.run([]stopStep{
			{"stuck step", func() { <-stuck }},
		})
		elapsed := time.Since(began)

		if ok {
			t.Fatal("run reported clean stop on buffered signal")
		}
		if !onQuickCalled {
			t.Fatal("onQuick hook was not called on buffered signal")
		}
		if elapsed > 500*time.Millisecond {
			t.Fatalf("run took %v, want immediate abort", elapsed)
		}
		if !strings.Contains(out.String(), `msg="interrupted during shutdown; quitting at once"`) {
			t.Fatalf("interruption was not logged:\n%s", out.String())
		}
	})
}
