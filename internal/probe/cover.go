package probe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// CoverTimeout bounds one extraction.
const CoverTimeout = 30 * time.Second

// ExtractCover writes the picture in the given stream of the media file to dest
// as a JPEG, through a temporary file renamed into place, so that a reader of
// dest never sees half an image. ffmpeg runs at the given nice value (0 leaves
// the priority alone). dest's directory is created.
func ExtractCover(ctx context.Context, ffmpeg, media string, stream int, dest string, niceness int) error {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp.jpg")
	defer os.Remove(tmp)

	ctx, cancel := context.WithTimeout(ctx, CoverTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-y", "-i", media,
		"-map", "0:"+strconv.Itoa(stream), "-frames:v", "1", "-q:v", "2", tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", ffmpeg, err)
	}
	if niceness != 0 {
		setNice(cmd.Process.Pid, niceness) // best effort
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("extract cover of %s: %w", filepath.Base(media), ctx.Err())
		}
		return fmt.Errorf("extract cover of %s: %w: %s", filepath.Base(media), err, firstLine(stderr.String()))
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return fmt.Errorf("extract cover of %s: ffmpeg wrote nothing", filepath.Base(media))
	}
	return os.Rename(tmp, dest)
}
