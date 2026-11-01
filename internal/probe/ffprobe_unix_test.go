//go:build unix

package probe

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeFFprobe writes a shell script that prints minimal ffprobe JSON and
// records its own nice value (after a delay so the parent has time to renice
// it) into niceFile.
func fakeFFprobe(t *testing.T) (bin, niceFile string) {
	t.Helper()
	dir := t.TempDir()
	niceFile = filepath.Join(dir, "nice")
	bin = filepath.Join(dir, "fake-ffprobe")
	script := "#!/bin/sh\n" +
		"sleep 0.3\n" +
		"ps -o ni= -p $$ > '" + niceFile + "'\n" +
		`echo '{"format":{"format_name":"mpegts","duration":"1.5"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264"}]}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, niceFile
}

func readNice(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("nice file %q: %v", b, err)
	}
	return n
}

func TestProbeRunsNiced(t *testing.T) {
	bin, niceFile := fakeFFprobe(t)

	res, err := FFprobe{Bin: bin, Niceness: 10}.Probe(context.Background(), "whatever.ts")
	if err != nil {
		t.Fatal(err)
	}
	if res.Container != "ts" || res.DurationSec != 1.5 || len(res.Streams) != 1 {
		t.Errorf("result = %+v", res)
	}
	if got := readNice(t, niceFile); got < 10 {
		t.Errorf("child nice = %d, want >= 10", got)
	}
}

func TestProbeWithoutNicenessKeepsPriority(t *testing.T) {
	// Positive control for TestProbeRunsNiced: without Niceness the child
	// does not end up at the reduced priority.
	bin, niceFile := fakeFFprobe(t)
	if _, err := (FFprobe{Bin: bin}).Probe(context.Background(), "whatever.ts"); err != nil {
		t.Fatal(err)
	}
	if got := readNice(t, niceFile); got >= 10 {
		t.Errorf("child nice = %d without Niceness, want < 10 (test environment already niced?)", got)
	}
}

func TestProbeFailureIncludesStderr(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fail-ffprobe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'moov atom not found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := FFprobe{Bin: bin}.Probe(context.Background(), "broken.mp4")
	if err == nil || !strings.Contains(err.Error(), "moov atom not found") {
		t.Errorf("err = %v, want stderr in message", err)
	}
}

func TestProbeTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow-ffprobe")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := FFprobe{Bin: bin, Timeout: 200 * time.Millisecond}.Probe(context.Background(), "slow.mp4")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("probe did not stop promptly: %v", time.Since(start))
	}
}

func TestProbeMissingBinary(t *testing.T) {
	if _, err := (FFprobe{Bin: "/nonexistent/ffprobe"}).Probe(context.Background(), "x.mp4"); err == nil {
		t.Error("expected an error for a missing binary")
	}
}
