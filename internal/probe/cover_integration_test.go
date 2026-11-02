//go:build integration && unix

package probe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// taggedFile makes a one-second mp4 with descriptive tags and a cover picture.
func taggedFile(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	dir := filepath.Join(t.TempDir(), "My Media") // a space on purpose
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "Tagged Show S02E05.mp4")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=d=1:s=64x64:r=10",
		"-f", "lavfi", "-i", "sine=d=1",
		"-f", "lavfi", "-i", "color=c=red:s=48x32:r=1",
		"-map", "0:v", "-map", "1:a", "-map", "2:v",
		"-c:v:0", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-c:v:1", "mjpeg", "-frames:v:1", "1",
		"-disposition:v:1", "attached_pic", "-shortest",
		"-metadata", "title=Pilot", "-metadata", "date=2019-12-25", "-metadata", "description=The first one.",
		"-metadata", "show=Tagged Show", "-metadata", "season_number=2", "-metadata", "episode_sort=5",
		"-metadata", "episode_id=S02E05", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	return out
}

func TestProbeReadsTagsAndFindsTheCover(t *testing.T) {
	path := taggedFile(t)
	res, err := FFprobe{}.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := Tags{Title: "Pilot", Year: 2019, Date: "2019-12-25", Description: "The first one.", Show: "Tagged Show",
		Season: 2, Episode: 5, EpisodeID: "S02E05"}
	if res.Tags != want {
		t.Errorf("tags = %+v, want %+v", res.Tags, want)
	}
	if !res.HasCover {
		t.Fatalf("no cover found; streams %+v", res.Streams)
	}
	for _, st := range res.Streams {
		if st.Index == res.Cover {
			t.Errorf("the cover (stream %d) is also listed as a playable stream", res.Cover)
		}
	}
	if len(res.Streams) != 2 {
		t.Errorf("streams = %+v, want one video and one audio", res.Streams)
	}

	// Extraction: a JPEG, written in place without leftovers, nice'd.
	dest := filepath.Join(t.TempDir(), "images", "embedded", "7.jpg")
	if err := ExtractCover(context.Background(), "ffmpeg", path, res.Cover, dest, 10); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dest)
	if err != nil || len(b) < 100 || !bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("cover file: %d bytes, err %v, starts %x", len(b), err, b[:min(4, len(b))])
	}
	if entries, _ := os.ReadDir(filepath.Dir(dest)); len(entries) != 1 {
		t.Errorf("leftovers next to the cover: %v", entries)
	}

	// The video stream is not a cover; asking for a missing stream fails cleanly.
	if err := ExtractCover(context.Background(), "ffmpeg", path, 42, filepath.Join(t.TempDir(), "x.jpg"), 0); err == nil {
		t.Error("extracting a stream that does not exist succeeded")
	}
	if err := ExtractCover(context.Background(), "ffmpeg", filepath.Join(t.TempDir(), "missing.mp4"), 0, filepath.Join(t.TempDir(), "y.jpg"), 0); err == nil {
		t.Error("extracting from a missing file succeeded")
	}
}

func TestProbeWithoutTagsOrCover(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found on PATH", bin)
		}
	}
	out := filepath.Join(t.TempDir(), "plain.mkv")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=d=1:s=64x64:r=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-map_metadata", "-1", "-metadata:g", "encoder=", out).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	res, err := FFprobe{}.Probe(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasCover || !res.Tags.Empty() {
		t.Errorf("a plain file has tags %+v / cover %v", res.Tags, res.HasCover)
	}
}
