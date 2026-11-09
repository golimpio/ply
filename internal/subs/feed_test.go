package subs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Matroska elements the tests lay out that the walk does not look at.
const (
	idSeekHead    = 0x114D9B74
	idVoid        = 0xEC
	idAttachments = 0x1941A469
)

// el is an element of a Matroska file for mkv: its ID and the size of its body.
type el struct {
	id uint32
	n  int
}

// mkv is the bytes of a Matroska file with the elements given, in order, under a Segment of
// unknown size: the headers are right, the bodies are zeros.
func mkv(els ...el) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x84, 0, 0, 0, 0})                               // the EBML header, with a body of four bytes
	b.Write([]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) // a Segment of unknown size
	for _, e := range els {
		var id []byte
		for v := e.id; v > 0; v >>= 8 {
			id = append([]byte{byte(v)}, id...)
		}
		b.Write(id)
		b.Write([]byte{0x10 | byte(e.n>>24), byte(e.n >> 16), byte(e.n >> 8), byte(e.n)}) // a size of four bytes
		b.Write(make([]byte, e.n))
	}
	return b.Bytes()
}

// film is a file as a muxer writes it: the SeekHead, Info and Tracks, a large Attachments
// element, then the Clusters.
func film(clusterBytes int) []byte {
	return mkv(el{idSeekHead, 80}, el{idVoid, 60}, el{idInfo, 70}, el{idTracks, 900},
		el{idAttachments, 130_000}, el{idCluster, clusterBytes}, el{idCluster, 1000})
}

// countingReader is a file on a slow share: it says how much has been read from it.
type countingReader struct {
	r    io.Reader
	read atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

// gatedWriter is ffmpeg's pipe: it takes what it is given, one write at a time, as the
// test lets it (a suspended ffmpeg takes nothing).
type gatedWriter struct {
	mu     sync.Mutex
	sizes  []int
	taken  int
	gate   chan struct{} // a receive lets one write through; closed, all of them
	writes chan int      // each write that arrives, before it waits
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{gate: make(chan struct{}), writes: make(chan int, 1024)}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.writes <- len(p)
	<-w.gate
	w.mu.Lock()
	w.sizes = append(w.sizes, len(p))
	w.taken += len(p)
	w.mu.Unlock()
	return len(p), nil
}

// ffmpeg that is suspended takes nothing: the container is read one chunk, and not a byte
// more, until the pipe takes it. A read that has been asked of a share cannot be taken
// back, and a play's first requests wait behind it (see openFeed); this is the bound.
func TestACopyToAStoppedFFmpegHasOneChunkInFlight(t *testing.T) {
	const size = 8*feedChunk + 1000
	src := &countingReader{r: bytes.NewReader(make([]byte, size))}
	w := newGatedWriter()
	done := make(chan error, 1)
	go func() { _, err := copyChunks(w, src, size); done <- err }()

	if n := <-w.writes; n != feedChunk { // the first chunk is in the pipe's way
		t.Fatalf("first write was %d bytes, want %d", n, feedChunk)
	}
	if got := src.read.Load(); got != feedChunk {
		t.Fatalf("%d bytes read from the file while ffmpeg takes nothing, want exactly one chunk (%d)", got, feedChunk)
	}

	close(w.gate) // ffmpeg goes on
	if err := <-done; err != nil {
		t.Fatalf("copy: %v", err) // positive control: it does read on once let go
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.taken != size || src.read.Load() != size {
		t.Errorf("taken %d, read %d, want %d of each", w.taken, src.read.Load(), size)
	}
	for i, n := range w.sizes[:len(w.sizes)-1] {
		if n != feedChunk {
			t.Errorf("write %d was %d bytes, want whole chunks of %d", i, n, feedChunk)
		}
	}
}

// The feed reads nothing until the run says ffmpeg has started (and a run that is held from
// the start has been suspended): a feed that began with the process would read a chunk or two
// for an ffmpeg that was never to take them.
func TestTheFeedReadsNothingBeforeItIsReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	if err := os.WriteFile(path, film(4*feedChunk), 0o644); err != nil {
		t.Fatal(err)
	}
	fd := openFeed(path)
	if fd == nil {
		t.Fatal("a Matroska file is not fed")
	}
	defer fd.Close()
	src := &countingReader{r: fd.r}
	fd.r = src
	w := newGatedWriter()
	go fd.WriteTo(w)

	time.Sleep(100 * time.Millisecond)
	if got := src.read.Load(); got != 0 {
		t.Fatalf("%d bytes read before the release", got)
	}
	fd.release()
	if n := <-w.writes; n != feedChunk { // positive control: it reads once released
		t.Fatalf("first write was %d bytes, want %d", n, feedChunk)
	}
	if got := src.read.Load(); got != feedChunk {
		t.Errorf("%d bytes read after the release, with ffmpeg taking nothing, want %d", got, feedChunk)
	}
	close(w.gate)
}

// io.Copy, which is what the copy to a command's stdin is, uses the feed's own copy
// and not a read buffer of its own: 32 KB reads off a share are a round trip each.
func TestIOCopyUsesTheChunkedCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	data := film(3 * feedChunk)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	fd := openFeed(path)
	if fd == nil {
		t.Fatal("a Matroska file is not fed")
	}
	defer fd.Close()
	fd.release()
	w := newGatedWriter()
	close(w.gate)
	n, err := io.Copy(w, fd)
	if err != nil || n != int64(len(data)) {
		t.Fatalf("io.Copy = %d, %v, want %d", n, err, len(data))
	}
	if len(w.sizes) == 0 || w.sizes[0] != feedChunk {
		t.Errorf("writes %v: the first should be a chunk of %d", w.sizes, feedChunk)
	}
}

// A file that ends before the size it had when it was opened (the share dropped, or it
// was cut) is a failure that says where: ffmpeg takes the end of a pipe for the end of the film.
func TestACopyOfAFileCutShortFails(t *testing.T) {
	const size = 3 * feedChunk
	if n, err := copyChunks(io.Discard, bytes.NewReader(make([]byte, size)), size); err != nil || n != size {
		t.Fatalf("a whole file: %d, %v", n, err) // positive control
	}
	n, err := copyChunks(io.Discard, bytes.NewReader(make([]byte, size-100)), size)
	var short *shortFileError
	if !errors.As(err, &short) {
		t.Fatalf("a file cut short: %d bytes, %v; want a *shortFileError", n, err)
	}
	if want := "the file ended at 786332 of 786432 bytes"; err.Error() != want {
		t.Errorf("the error is %q, want %q", err, want)
	}
	if _, err := copyChunks(io.Discard, bytes.NewReader(make([]byte, size+5)), size); err != nil {
		t.Errorf("a file that grew while it was read: %v", err)
	}
}

// Only Matroska that can be read in order is fed: an MP4 may keep its index at the end, which
// a pipe cannot reach, and so may a Matroska file with its Info or Tracks after a Cluster (ffmpeg
// finds them by a seek, and on a pipe says "Stream map matches no streams").
func TestOnlyMatroskaThatCanBeReadInOrderIsFed(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	many := []el{}
	for range maxHeadElements {
		many = append(many, el{idVoid, 1})
	}
	for name, c := range map[string]struct {
		file []byte
		fed  bool
	}{
		"as a muxer writes it":           {film(2 * feedChunk), true},
		"Tracks, then Info":              {mkv(el{idTracks, 50}, el{idInfo, 50}, el{idCluster, 100}), true},
		"Tracks after a Cluster":         {mkv(el{idInfo, 70}, el{idCluster, 5000}, el{idTracks, 900}, el{idCluster, 5000}), false},
		"Info after a Cluster":           {mkv(el{idTracks, 900}, el{idCluster, 5000}, el{idInfo, 70}), false},
		"Tracks and Info at the end":     {mkv(el{idCluster, 5000}, el{idCluster, 5000}, el{idInfo, 70}, el{idTracks, 900}), false},
		"no Tracks":                      {mkv(el{idInfo, 70}, el{idCluster, 5000}), false},
		"no Info":                        {mkv(el{idTracks, 900}, el{idCluster, 5000}), false},
		"no Cluster":                     {mkv(el{idInfo, 70}, el{idTracks, 900}), false},
		"too many elements before":       {mkv(append(many, el{idInfo, 70}, el{idTracks, 900}, el{idCluster, 100})...), false},
		"cut inside Info":                {mkv(el{idInfo, 70}, el{idTracks, 900})[:60], false},
		"EBML header and no Segment":     {append(mkv()[:9:9], mkv(el{idInfo, 70})[21:]...), false},
		"an MP4":                         {append([]byte{0, 0, 0, 0x18}, "ftypisom"...), false},
		"a transport stream":             {[]byte{0x47, 0x40, 0x11, 0x10}, false},
		"text":                           {[]byte("media"), false},
		"two bytes":                      {[]byte{0x1A, 0x45}, false},
		"empty":                          {nil, false},
		"an EBML header of unknown size": {[]byte{0x1A, 0x45, 0xDF, 0xA3, 0xFF, 0, 0, 0}, false},
	} {
		fd := openFeed(write(strings.ReplaceAll(name, " ", "_"), c.file))
		if fd != nil {
			fd.Close()
		}
		if (fd != nil) != c.fed {
			t.Errorf("%s: fed %v, want %v", name, fd != nil, c.fed)
		}
	}

	fd := openFeed(write("a.mkv", film(10)))
	if fd == nil {
		t.Fatal("a Matroska file is not fed")
	}
	got, err := io.ReadAll(fd) // it is also a reader, from the start
	fd.Close()
	if err != nil || !bytes.HasPrefix(got, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		t.Errorf("read %d bytes, %v: should start at the beginning of the file", len(got), err)
	}
	if fd := openFeed(filepath.Join(dir, "missing.mkv")); fd != nil {
		fd.Close()
		t.Error("a file that is not there is fed")
	}
	if fd := openFeed(dir); fd != nil {
		fd.Close()
		t.Error("a directory is fed")
	}
}
