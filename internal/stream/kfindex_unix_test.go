//go:build unix

package stream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A rescan is a scan: it does not join an index read that is under way for the
// same file (here stuck, like a share that went away), whose end it would have
// to wait for.
func TestForcedRescanDoesNotJoinAnIndexRead(t *testing.T) {
	// The index read gives up after indexTimeout, a minute: longer than this test.
	fifo := filepath.Join(t.TempDir(), "film.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	t.Cleanup(func() { // let the stuck open end when the test does
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
	})
	scan := fakeFFprobe(t, `printf '0.000000,K__,\n2.000000,K__,\n0.000000\n'`)
	m, _ := newGuardRig(t).manager(t, &recCache{}, scan, nil)
	req := Request{Path: fifo, Container: "mkv", DurationSec: 4, VideoIndex: 0}

	go m.keyframes(context.Background(), req, false) // stuck in the open
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kf, source, err := m.keyframes(ctx, req, true)
	if err != nil || source != sourceScan || !equalTimes(kf, []float64{0, 2}) {
		t.Errorf("the rescan: %v from %q, %v", kf, source, err)
	}
}

// A file that cannot be read holds the index attempt for its own short timeout,
// not for the much longer one of the scan: opening a FIFO with no writer blocks,
// like a read of a share that went away.
func TestIndexAttemptHasItsOwnTimeout(t *testing.T) {
	defer indexTimeout.set(indexTimeout.get())
	indexTimeout.set(200 * time.Millisecond)

	fifo := filepath.Join(t.TempDir(), "film.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	// Let the stuck open end when the test does.
	t.Cleanup(func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
	})

	scan := fakeFFprobe(t, `printf '0.000000,K__,\n2.000000,K__,\n0.000000\n'`)
	type result struct {
		r   keyframeResult
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		// The scan's context has all the time there is; only the index attempt may give up.
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		r, err := keyframesVia(ctx, scan, fifo, 0, -1)
		done <- result{r, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.r.source != sourceScan || !equalTimes(got.r.times, []float64{0, 2}) {
			t.Errorf("source %q, times %v", got.r.source, got.r.times)
		}
		if got.r.skip == nil || !strings.Contains(got.r.skip.Error(), "deadline exceeded") {
			t.Errorf("the index attempt was not abandoned for its timeout: %v", got.r.skip)
		}
		if took := time.Since(start); took < 150*time.Millisecond {
			t.Errorf("gave up after %v, before the timeout", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the index attempt held the caller for as long as the file did")
	}
}
