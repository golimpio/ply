package probe

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeFFprobePri is fakeFFprobe recording the child's scheduling priority,
// which shows the macOS background policy (4) that the niceness does not.
func fakeFFprobePri(t *testing.T) (bin, priFile string) {
	t.Helper()
	dir := t.TempDir()
	priFile = filepath.Join(dir, "pri")
	bin = filepath.Join(dir, "fake-ffprobe")
	script := "#!/bin/sh\n" +
		"sleep 0.3\n" +
		"ps -o pri= -p $$ > '" + priFile + "'\n" +
		`echo '{"format":{"format_name":"mpegts","duration":"1.5"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264"}]}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, priFile
}

func readPri(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pri file %q: %v", b, err)
	}
	return n
}

func TestProbeRunsInTheDarwinBackgroundBand(t *testing.T) {
	bin, priFile := fakeFFprobePri(t)
	if _, err := (FFprobe{Bin: bin, Niceness: 10}).Probe(context.Background(), "whatever.ts"); err != nil {
		t.Fatal(err)
	}
	if got := readPri(t, priFile); got > 4 {
		t.Errorf("background ffprobe runs at priority %d, want the background band (<= 4)", got)
	}

	// Positive control: an ffprobe that is not demoted stays in the normal band.
	bin, priFile = fakeFFprobePri(t)
	if _, err := (FFprobe{Bin: bin}).Probe(context.Background(), "whatever.ts"); err != nil {
		t.Fatal(err)
	}
	if got := readPri(t, priFile); got <= 4 {
		t.Errorf("ffprobe without Niceness runs at priority %d, want the normal band", got)
	}
}
