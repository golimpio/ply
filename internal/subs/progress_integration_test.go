//go:build integration

package subs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// With the real ffmpeg reading a real container at the pace of the file (-re stands in for a
// slow share), the Generator is told how far into the film it has read while it reads, and
// the position only goes forward.
func TestFillProgressWithRealFFmpegFollowsTheRead(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	const seconds = 8
	var sb strings.Builder
	for i := range seconds - 1 {
		fmt.Fprintf(&sb, "%d\n00:00:%02d,100 --> 00:00:%02d,600\ncue %d\n\n", i+1, i, i, i+1)
	}
	srt := filepath.Join(dir, "a.srt")
	if err := os.WriteFile(srt, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "film.mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=10:duration=%d", seconds), "-i", srt,
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-c:s", "srt", input).CombinedOutput(); err != nil {
		t.Fatalf("making the fixture: %v\n%s", err, out)
	}
	wrapper := filepath.Join(dir, "slow-ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+ffmpeg+" -re \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Dir: filepath.Join(dir, "subs"), FFmpeg: wrapper})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	src := Source{ItemID: 1, Key: "1", Input: input, Stream: 1, MTime: 1}

	var (
		mu   sync.Mutex
		told []time.Duration
		over atomic.Bool // Fill has returned
		late atomic.Bool // a position came after that
	)
	err = g.FillProgress(t.Context(), []Source{src}, nil, nil, func(at time.Duration) {
		if over.Load() {
			late.Store(true)
		}
		mu.Lock()
		told = append(told, at)
		mu.Unlock()
	})
	over.Store(true)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Cached(src) { // positive control: the run did its work
		t.Fatal("the track was not extracted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(told) < 4 { // about every half second, for the 8 seconds of the file
		t.Fatalf("told %d times (%v), want a few", len(told), told)
	}
	for i := 1; i < len(told); i++ {
		if told[i] < told[i-1] {
			t.Errorf("the position went back: %v", told)
			break
		}
	}
	if first, last := told[0], told[len(told)-1]; last <= first || last < seconds*time.Second/2 {
		t.Errorf("first %v, last %v of a %d s film: the position did not follow the read", first, last, seconds)
	}
	if late.Load() {
		t.Error("told after the run was over")
	}
}
