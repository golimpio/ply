//go:build integration && unix

package art

import (
	"context"
	"errors"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	return bin
}

func fixture(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make fixture %s: %v\n%s", name, err, b)
	}
	return out
}

func TestRealFFmpegMakesImagesAndColour(t *testing.T) {
	bin := requireFFmpeg(t)
	media := t.TempDir()
	movie := fixture(t, media, "movie.mp4", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=10",
		"-c:v", "libx264", "-g", "48", "-pix_fmt", "yuv420p")

	g, err := New(context.Background(), Options{Dir: filepath.Join(t.TempDir(), "art cache"), FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := Source{ID: 1, Path: movie, MTime: 1, DurationSec: 10}

	for kind, wantWidth := range map[Kind]int{Thumb: 640, Backdrop: 1280 /* not upscaled to 1600 */} {
		p, err := g.Path(context.Background(), in, kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := jpeg.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s is not a JPEG: %v", kind, err)
		}
		if cfg.Width != wantWidth {
			t.Errorf("%s width = %d, want %d", kind, cfg.Width, wantWidth)
		}
	}
	if hex, ok := g.Colour(in); !ok || !validHex(hex) {
		t.Errorf("colour = %q, %v", hex, ok)
	}
}

func TestRealFFmpegAudioOnlyHasNoFrame(t *testing.T) {
	bin := requireFFmpeg(t)
	media := t.TempDir()
	song := fixture(t, media, "song.m4a", "-f", "lavfi", "-i", "sine=duration=3", "-c:a", "aac")

	g, err := New(context.Background(), Options{Dir: t.TempDir(), FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Path(context.Background(), Source{ID: 1, Path: song, MTime: 1, DurationSec: 3}, Thumb)
	if !errors.Is(err, ErrNoFrame) {
		t.Fatalf("err = %v, want ErrNoFrame", err)
	}
}

func TestCancelKillsFFmpeg(t *testing.T) {
	bin := requireFFmpeg(t)
	g, err := New(context.Background(), Options{Dir: t.TempDir(), FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	pids := make(chan int, 1)
	g.started = func(pid int) { pids <- pid }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// Ten minutes of decoding: only cancellation can end this in time.
		_, err := g.exec(ctx, []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=600", "-f", "null", "-"})
		done <- err
	}()
	pid := <-pids
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("positive control: ffmpeg %d not running: %v", pid, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ffmpeg still running 10s after cancel")
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process group %d still exists after cancel (err = %v)", pid, err)
	}
}
