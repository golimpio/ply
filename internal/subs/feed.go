package subs

import (
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"sync"
	"sync/atomic"
)

// feedChunk is how much of a container is read at once on its way to ffmpeg. On a
// network share a read costs a round trip, so it is large enough to keep the link
// busy, and no larger: a read that has been asked for cannot be taken back (a
// suspended or killed ffmpeg does not stop it), and everybody else's requests on
// the connection (a play's stat, open and first reads) queue behind it.
const feedChunk = 256 << 10

// The Matroska/WebM elements the walk of the head of a file looks at.
const (
	idEBML    = 0x1A45DFA3
	idSegment = 0x18538067
	idInfo    = 0x1549A966
	idTracks  = 0x1654AE6B
	idCluster = 0x1F43B675

	// maxHeadElements is how many elements before the first Cluster a file may have for the
	// walk to go on (a SeekHead, a Void, Info, Tracks, Chapters, Attachments, Tags: about
	// seven); headWindow is how much of the head one read takes.
	maxHeadElements = 64
	headWindow      = 4 << 10
)

// feed is a container as ffmpeg reads it from its standard input, with ply doing
// the reading (openFeed says why).
type feed struct {
	r    io.Reader // the file; the tests', when they count what is read
	f    *os.File
	size int64 // at the time it was opened: a file that ends before is a failed read

	gate chan struct{} // closed by release: nothing is read before
	once sync.Once
	cut  atomic.Pointer[shortFileError] // set when the file ended before its size
}

// openFeed opens the container at path to be fed to ffmpeg, or returns nil if
// ffmpeg should open the file itself: the file is not one that can be read from a
// pipe (see headersFirst), or it cannot be opened (the run then fails as it always
// did, and says why).
//
// The reason for a feed is the reading ahead of the macOS kernel. Left alone, it
// keeps several megabytes of a sequential read in flight at all times, which on the
// share of a slow NAS is seconds of the link (measured 2026-11-09: 1.2 to 12 s for a
// stat of another file, while ffmpeg read, and after the signal that suspends it,
// since the reads already asked for still come). A play that starts waits for all
// of it before its first request is answered. ffmpeg reads in blocks of 32 KB and
// cannot be told to read in larger ones, and 32 KB at a time with the kernel's
// reading ahead off is a round trip each: a third of the speed of the link. So ply
// reads, 256 KB at a time with the reading ahead off, and ffmpeg gets the bytes
// through a pipe: when ffmpeg is suspended the pipe fills and the reading stops, with
// at most one chunk in flight.
//
// Only Matroska/WebM: a pipe cannot be sought, and an MP4 may keep its index at the
// end. Of Matroska, only files that can be read in order.
func openFeed(path string) *feed {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	_ = readAheadOff(f) // before the first read, which would start it
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !headersFirst(f, fi.Size()) {
		f.Close()
		return nil
	}
	return &feed{r: f, f: f, size: fi.Size(), gate: make(chan struct{})}
}

// release lets the feed begin to read. The run calls it once ffmpeg has started and
// the pacer has had its say (started suspends a run that is held from the start): a
// feed that began with the process would read a chunk or two before the signal.
func (d *feed) release() { d.once.Do(func() { close(d.gate) }) }

func (d *feed) Read(p []byte) (int, error) { return d.r.Read(p) }

// WriteTo is what the copy to ffmpeg's pipe uses (io.Copy), in place of a read
// buffer of its own (32 KB).
func (d *feed) WriteTo(w io.Writer) (int64, error) {
	<-d.gate
	n, err := copyChunks(w, d.r, d.size)
	if s, ok := err.(*shortFileError); ok {
		d.cut.Store(s)
	}
	return n, err
}

// short is why the file was not read to its end, if it ended early.
func (d *feed) short() error {
	if s := d.cut.Load(); s != nil {
		return s
	}
	return nil
}

func (d *feed) Close() error { return d.f.Close() }

// shortFileError is a file that ended before the size it had when it was opened.
type shortFileError struct{ got, want int64 }

func (e *shortFileError) Error() string {
	return fmt.Sprintf("the file ended at %d of %d bytes", e.got, e.want)
}

// cutShort reports whether err is (or wraps) a file that ended before its size: whichever way
// it is classified, reading it again with other tracks, or on the file itself, will not help.
func cutShort(err error) bool {
	var s *shortFileError
	return errors.As(err, &s)
}

// copyChunks writes src to dst, reading feedChunk bytes at a time and not reading
// again until dst has taken them. It fails if src ends before size bytes: ffmpeg
// would take a file cut short for a short film, and write the subtitles of the part
// it got as if they were all.
func copyChunks(dst io.Writer, src io.Reader, size int64) (int64, error) {
	buf := make([]byte, feedChunk)
	var n int64
	for {
		m, rerr := io.ReadFull(src, buf)
		if m > 0 {
			w, werr := dst.Write(buf[:m])
			n += int64(w)
			if werr != nil {
				return n, werr
			}
		}
		switch rerr {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			if n < size {
				return n, &shortFileError{got: n, want: size}
			}
			return n, nil
		default:
			return n, rerr
		}
	}
}

// headersFirst reports whether the Matroska file in r (size bytes) can be read in
// order: both its Info and its Tracks come before the first Cluster. Muxers write
// them first, but a file edited by hand, or by a tool that appends, may not have
// them there, and ffmpeg then finds them only by a seek to where the SeekHead says
// they are, which a pipe cannot do ("Stream map matches no streams"). The Cues are
// not needed. The walk reads the headers of the top-level elements and jumps over
// their bodies: a handful of small reads, in one or two windows.
func headersFirst(r io.ReaderAt, size int64) bool {
	w := &headReader{r: r}
	h, ok := w.head(0)
	if !ok || h.id != idEBML || h.size < 0 {
		return false
	}
	pos := int64(h.n) + h.size
	seg, ok := w.head(pos)
	if !ok || seg.id != idSegment {
		return false
	}
	pos += int64(seg.n) // the children of the Segment follow its header, whatever its size says
	var info, tracks bool
	for range maxHeadElements {
		if pos >= size {
			return false
		}
		h, ok := w.head(pos)
		if !ok {
			return false
		}
		switch h.id {
		case idCluster:
			return info && tracks
		case idInfo:
			info = true
		case idTracks:
			tracks = true
		}
		if h.size < 0 { // only a Cluster may be of unknown size
			return false
		}
		pos += int64(h.n) + h.size
	}
	return false
}

// ebmlHead is the ID and data size at the start of an element; n is the length of both.
type ebmlHead struct {
	id   uint32
	size int64 // -1: unknown
	n    int
}

// headReader reads the head of a file in windows, so that the headers of elements that
// follow one another cost one read.
type headReader struct {
	r   io.ReaderAt
	off int64
	buf []byte
}

func (w *headReader) head(pos int64) (ebmlHead, bool) {
	const maxHead = 4 + 8
	if pos < 0 {
		return ebmlHead{}, false
	}
	if pos < w.off || pos+maxHead > w.off+int64(len(w.buf)) {
		buf := make([]byte, headWindow)
		n, _ := w.r.ReadAt(buf, pos) // short at the end of the file: what is there is parsed
		w.off, w.buf = pos, buf[:n]
	}
	b := w.buf[pos-w.off:]
	id, k := ebmlID(b)
	if k == 0 {
		return ebmlHead{}, false
	}
	size, m := ebmlSize(b[k:])
	if m == 0 {
		return ebmlHead{}, false
	}
	return ebmlHead{id, size, k + m}, true
}

// ebmlID decodes an element ID; n is its length, 0 when b does not start with a valid
// one. (As in internal/stream/kfmkv.go, which is not exported.)
func ebmlID(b []byte) (id uint32, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if n > 4 || len(b) < n {
		return 0, 0
	}
	for _, c := range b[:n] {
		id = id<<8 | uint32(c)
	}
	return id, n
}

// ebmlSize decodes a data size; n is its length (0 when invalid) and size is -1 for the
// reserved "unknown" value.
func ebmlSize(b []byte) (size int64, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0
	}
	mask := byte(1)<<(8-n) - 1 // zero for n == 8
	size = int64(b[0] & mask)
	unknown := b[0]&mask == mask
	for _, c := range b[1:n] {
		size = size<<8 | int64(c)
		unknown = unknown && c == 0xFF
	}
	if unknown {
		return -1, n
	}
	return size, n
}
