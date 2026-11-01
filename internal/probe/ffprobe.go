package probe

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FFprobe runs the ffprobe binary.
type FFprobe struct {
	Bin     string        // default "ffprobe"
	Timeout time.Duration // per file; default 60s
	// Niceness is the unix nice value applied to the child process (0 leaves
	// the priority unchanged). Scans use a positive value so probing a large
	// library does not compete with playback.
	Niceness int
}

// Probe returns the normalised ffprobe result for the file at path.
func (f FFprobe) Probe(ctx context.Context, path string) (Result, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Result{}, err
	}
	bin := f.Bin
	if bin == "" {
		bin = "ffprobe"
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-i", abs)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", bin, err)
	}
	if f.Niceness != 0 {
		setNice(cmd.Process.Pid, f.Niceness) // best effort
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("ffprobe %s: %w", filepath.Base(abs), ctx.Err())
		}
		return Result{}, fmt.Errorf("ffprobe %s: %w: %s", filepath.Base(abs), err, firstLine(stderr.String()))
	}
	return Parse(stdout.Bytes(), abs)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}
