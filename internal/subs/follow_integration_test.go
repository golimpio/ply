//go:build integration

package subs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With the real ffmpeg reading a real container at the pace of the file (-re stands in for
// a slow share), the first cues of a track reach the reader long before the container has
// been read, and the second track of the container is extracted by the same run.
func TestFollowGivesCuesWhileFFmpegReadsTheContainer(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	const seconds = 10
	for _, lang := range []string{"a", "b"} {
		var sb strings.Builder
		for i := range seconds - 1 {
			fmt.Fprintf(&sb, "%d\n00:00:%02d,100 --> 00:00:%02d,600\n%s %d\n\n", i+1, i, i, lang, i+1)
		}
		if err := os.WriteFile(filepath.Join(dir, lang+".srt"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	input := filepath.Join(dir, "film.mkv")
	out, err := exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=10:duration=%d", seconds),
		"-i", filepath.Join(dir, "a.srt"), "-i", filepath.Join(dir, "b.srt"),
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-c:s", "srt", input).CombinedOutput()
	if err != nil {
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
	a := Source{ItemID: 1, Key: "1", Input: input, Stream: 1, MTime: 1}
	b := Source{ItemID: 1, Key: "2", Input: input, Stream: 2, MTime: 1}

	start := time.Now()
	fo, err := g.Follow(t.Context(), a, []Source{b})
	if err != nil {
		t.Fatal(err)
	}
	defer fo.Close()

	// The third cue ("a 3", from second 2) is here while the container has 7 seconds left to read.
	var got strings.Builder
	buf := make([]byte, 4096)
	for !strings.Contains(got.String(), "a 3") {
		n, err := fo.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			t.Fatalf("read %q then %v: the track ended before its third cue", got.String(), err)
		}
	}
	select {
	case <-fo.Done():
		t.Fatalf("the run was over when the third cue arrived (%v in): nothing was followed", time.Since(start))
	default:
	}
	if !strings.HasPrefix(got.String(), "WEBVTT") {
		t.Errorf("starts with %q", got.String())
	}
	if d := time.Since(start); d > seconds*time.Second/2 {
		t.Errorf("the third cue took %v, with a %d s container read at its own pace", d, seconds)
	}

	rest, err := io.ReadAll(fo)
	if err != nil {
		t.Fatal(err)
	}
	all := got.String() + string(rest)
	for i := 1; i < seconds; i++ {
		if !strings.Contains(all, fmt.Sprintf("a %d\n", i)) {
			t.Errorf("cue %d is missing from %q", i, all)
		}
	}
	// One run, both tracks, both cached.
	if !g.Cached(a) || !g.Cached(b) {
		t.Errorf("cached: a %v, b %v; want both (the sibling rode along)", g.Cached(a), g.Cached(b))
	}
	if p, _ := g.Path(t.Context(), b); p == "" {
		t.Error("no file for the sibling")
	} else if data, _ := os.ReadFile(p); !strings.Contains(string(data), "b 9\n") {
		t.Errorf("sibling = %q", data)
	}
}
