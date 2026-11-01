package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ExtractKeyframes lists the keyframe times of the first video stream of
// path, in seconds from the start of the file. It is a full packet scan, so
// it can take a while on big files; run it niced and cache the result.
func ExtractKeyframes(ctx context.Context, ffprobe, path string, nice int) ([]float64, error) {
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
		"-skip_frame", "nokey", "-show_entries", "packet=pts_time,flags:format=start_time",
		"-of", "csv=p=0", "-i", path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", ffprobe, err)
	}
	if nice != 0 {
		setNice(cmd.Process.Pid, nice)
	}
	kf, start, perr := parseKeyframes(out)
	werr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case werr != nil:
		return nil, fmt.Errorf("ffprobe keyframes: %w: %s", werr, stderr.String())
	case perr != nil:
		return nil, perr
	}
	for i := range kf {
		kf[i] -= start
	}
	return cleanKeyframes(kf), nil
}

// parseKeyframes reads ffprobe's csv: packet lines "pts_time,flags," and one
// format line "start_time". It returns the absolute keyframe times and the
// start time of the file.
func parseKeyframes(r io.Reader) (kf []float64, start float64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(strings.TrimRight(line, ","), ",")
		if len(fields) == 1 { // format section
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				start = v
			}
			continue
		}
		if !strings.Contains(fields[1], "K") {
			continue
		}
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			kf = append(kf, v)
		}
	}
	sort.Float64s(kf)
	return kf, start, sc.Err()
}

// tailBuffer keeps the last few KiB written to it (ffmpeg's stderr).
type tailBuffer struct {
	b []byte
}

const tailMax = 4096

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > tailMax {
		t.b = t.b[len(t.b)-tailMax:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.b)) }
