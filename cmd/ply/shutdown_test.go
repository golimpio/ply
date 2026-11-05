package main

import (
	"bytes"
	"log/slog"
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
