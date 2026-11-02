//go:build unix

package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// within runs fn and fails the test if it does not return soon: the bugs these
// tests are about are calls that block forever.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

func TestReadNFOAndLocalArtNeverBlockOnAFIFO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Library")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "Movie.nfo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(dir, "linked.nfo")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, link} {
		within(t, "ReadNFO of "+filepath.Base(p), func() {
			if n, err := ReadNFO(p); err == nil || !errors.Is(err, ErrNotRegular) || n.HasData() {
				t.Errorf("ReadNFO(%s) = %+v, %v; want ErrNotRegular", filepath.Base(p), n, err)
			}
		})
	}
	poster := filepath.Join(dir, "poster.jpg")
	if err := syscall.Mkfifo(poster, 0o644); err != nil {
		t.Fatal(err)
	}
	within(t, "OpenLocalImage of a FIFO", func() {
		if f, _, _, err := OpenLocalImage(poster); err == nil || !errors.Is(err, ErrImageUnavailable) {
			if f != nil {
				f.Close()
			}
			t.Errorf("OpenLocalImage(fifo) = %v; want ErrImageUnavailable", err)
		}
	})
	within(t, "OpenRegular of a FIFO", func() {
		if f, err := OpenRegular(poster); !errors.Is(err, ErrNotRegular) {
			if f != nil {
				f.Close()
			}
			t.Errorf("OpenRegular(fifo) = %v", err)
		}
	})

	// A device is not a regular file either, and is not read (/dev/zero would
	// otherwise give a megabyte of nothing).
	zero := filepath.Join(dir, "zero.nfo")
	if err := os.Symlink("/dev/zero", zero); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNFO(zero); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadNFO(/dev/zero) = %v", err)
	}
	// Positive control: a regular file named the same way is read.
	good := filepath.Join(dir, "Good.nfo")
	if err := os.WriteFile(good, []byte("<movie><title>Fine</title></movie>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ReadNFO(good); err != nil || n.Title != "Fine" {
		t.Errorf("ReadNFO(regular) = %+v, %v", n, err)
	}
}

func TestEmbeddedCoverIsNotExtractedFromANonRegularFile(t *testing.T) {
	bin, log := fakeFFmpeg(t, false)
	c := newCache(t, newImageServer(t, nil), func(o *ImageOptions) { o.FFmpeg = bin })
	fifo := filepath.Join(t.TempDir(), "movie.mkv")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	within(t, "Embedded of a FIFO", func() {
		if _, err := c.Embedded(t.Context(), 1, fifo, 0); !errors.Is(err, ErrImageUnavailable) {
			t.Errorf("Embedded(fifo) = %v", err)
		}
	})
	if n := len(calls(t, log)); n != 0 {
		t.Errorf("ffmpeg ran %d times for a FIFO", n)
	}
}

func TestReadNFOReadsAtMostTheCapOfALargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.nfo")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("<movie><title>X</title></movie>"); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil { // a sparse file of 64 MiB
		t.Fatal(err)
	}
	f.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := ReadNFO(path)
	runtime.ReadMemStats(&after)
	if err != nil || n.Title != "X" {
		t.Fatalf("ReadNFO = %+v, %v", n, err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 16<<20 {
		t.Errorf("reading a 64 MiB file allocated %d MiB; it should read at most %d bytes", got>>20, MaxNFOBytes)
	}
}
