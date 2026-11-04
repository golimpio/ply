package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"
)

// Where a keyframe index came from.
const (
	sourceCues        = "cues" // Matroska/WebM Cues element
	sourceSampleTable = "stss" // MP4/MOV sync sample table
	sourceScan        = "scan" // ffprobe packet scan of the whole file
)

// sniffLen is how many bytes tell the containers apart.
const sniffLen = 8

// Limits that keep a malformed or hostile file from making the index readers
// read forever or allocate hugely. The index of a real file is a few small
// regions, so a well-formed one never gets near any of these.
const (
	maxIndexBytes     = 64 << 20 // bytes read from one file
	maxIndexReads     = 1 << 15  // ReadAt calls on one file
	maxIndexElements  = 1 << 20  // EBML elements or MP4 boxes visited
	maxIndexKeyframes = 1 << 20  // keyframes in one index
	maxTableBytes     = 32 << 20 // one sample table or element read into memory
)

var (
	// errIndexLimit means a file asked for more than the limits above allow.
	errIndexLimit = errors.New("keyframe index: read limit reached")
	// errNoContainerIndex means the file is not a container this package can
	// read a keyframe index from.
	errNoContainerIndex = errors.New("keyframe index: unsupported container")
)

// limitErr is errIndexLimit, saying which limit it was.
func limitErr(what string) error { return fmt.Errorf("%w (%s)", errIndexLimit, what) }

// indexTimeout bounds the attempt to read an index out of the container. It
// takes a few reads, even over a network share; if the share has stalled, the
// scan, which has its own, much longer, timeout, is no better but no worse. A
// variable for the test of it.
var indexTimeout = newTimeout(60 * time.Second)

// timeout is a duration that can be changed while another goroutine reads it:
// an attempt left behind by a test (a read stuck on a FIFO) reads it for as long
// as it lasts.
type timeout struct{ ns atomic.Int64 }

func newTimeout(d time.Duration) *timeout {
	t := &timeout{}
	t.ns.Store(int64(d))
	return t
}

func (t *timeout) get() time.Duration  { return time.Duration(t.ns.Load()) }
func (t *timeout) set(d time.Duration) { t.ns.Store(int64(d)) }

// Keyframes lists the keyframe times of the first video stream of path, in
// seconds from the start of the file, like ExtractKeyframes. It first reads the
// index the container already has (the Cues of a Matroska/WebM file, the sync
// sample table of an MP4/MOV file), which takes a few reads wherever the file
// is, and only falls back to the ffprobe packet scan of the whole file when
// there is no usable index.
//
// It is the entry point for callers that only want the times. The Manager does
// not use it: it needs to know where the times came from (an index is checked
// against the first fragments before it is trusted, a scan is not), which
// keyframesVia reports.
func Keyframes(ctx context.Context, ffprobe, path string, nice int) ([]float64, error) {
	r, err := keyframesVia(ctx, ffprobe, path, nice, -1)
	return r.times, err
}

// keyframeResult is a keyframe index and how it was obtained.
type keyframeResult struct {
	times  []float64
	source string // sourceCues, sourceSampleTable or sourceScan
	skip   error  // why the container index was not used (source is sourceScan)
}

// keyframesVia is Keyframes, saying where the index came from. videoIndex is
// the ffprobe stream index of the video stream wanted; -1 means the first one.
// The packet scan always reads the first video stream.
func keyframesVia(ctx context.Context, ffprobe, path string, nice, videoIndex int) (keyframeResult, error) {
	kf, source, ierr := readKeyframeIndex(ctx, path, videoIndex)
	if ierr == nil {
		return keyframeResult{times: kf, source: source}, nil
	}
	if err := ctx.Err(); err != nil {
		return keyframeResult{}, err
	}
	r, err := keyframesScan(ctx, ffprobe, path, nice)
	r.skip = ierr
	return r, err
}

// keyframesScan is the packet scan, as a keyframeResult.
func keyframesScan(ctx context.Context, ffprobe, path string, nice int) (keyframeResult, error) {
	kf, err := ExtractKeyframes(ctx, ffprobe, path, nice)
	return keyframeResult{times: kf, source: sourceScan}, err
}

// readKeyframeIndex reads the index in its own goroutine, so that a read that
// hangs (a share that went away) cannot hold the caller past indexTimeout. The
// goroutine ends when the read does; the limits bound what it can still do. Until
// then it holds the file open (a descriptor, and on a share a handle), which is
// what a hung read costs; the caller does not wait for it.
func readKeyframeIndex(ctx context.Context, path string, videoIndex int) ([]float64, string, error) {
	ictx, cancel := context.WithTimeout(ctx, indexTimeout.get())
	defer cancel()
	type result struct {
		kf     []float64
		source string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		r.kf, r.source, r.err = readKeyframeIndexFile(ictx, path, videoIndex)
		done <- r
	}()
	select {
	case r := <-done:
		return r.kf, r.source, r.err
	case <-ictx.Done():
		return nil, "", fmt.Errorf("keyframe index: %w", ictx.Err())
	}
}

func readKeyframeIndexFile(ctx context.Context, path string, videoIndex int) ([]float64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !fi.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s is not a regular file", path)
	}
	return indexKeyframes(ctx, f, fi.Size(), videoIndex)
}

// indexKeyframes reads the keyframe index out of the container in r (size
// bytes): the times are in seconds, ascending, relative to the start of the
// file and cleaned like ExtractKeyframes'. videoIndex is the ffprobe stream
// index of the video stream (-1: the first video stream). Any error means "no
// usable index".
func indexKeyframes(ctx context.Context, r io.ReaderAt, size int64, videoIndex int) ([]float64, string, error) {
	x := &indexReader{r: r, size: size}
	var magic [sniffLen]byte
	if err := x.readAt(magic[:], 0); err != nil {
		return nil, "", errNoContainerIndex
	}
	switch {
	case magic[0] == 0x1A && magic[1] == 0x45 && magic[2] == 0xDF && magic[3] == 0xA3:
		kf, err := matroskaKeyframes(ctx, x, videoIndex)
		return kf, sourceCues, err
	case isMP4Box(string(magic[4:8])):
		kf, err := mp4Keyframes(ctx, x, videoIndex)
		return kf, sourceSampleTable, err
	}
	return nil, "", errNoContainerIndex
}

// isMP4Box reports whether typ can be the first box of an MP4/MOV file.
func isMP4Box(typ string) bool {
	switch typ {
	case "ftyp", "moov", "mdat", "free", "skip", "wide", "pnot":
		return true
	}
	return false
}

// indexWindow is how much a small read pulls in: the structures the index
// parsers walk (EBML headers, MP4 boxes) are contiguous, and on a network share
// a round trip costs far more than the few KiB that save the next one.
const indexWindow = 64 << 10

// indexReader is a metered, read-ahead io.ReaderAt: every read the index
// parsers make goes through it, so the limits hold whatever the file claims.
type indexReader struct {
	r     io.ReaderAt
	size  int64
	bytes int64 // fetched from r so far
	reads int   // calls made on r so far

	win    []byte // the last window fetched
	winOff int64
}

// check validates a read of len(p) bytes at off: past the end of the file is an
// error, not a short read.
func (x *indexReader) check(p []byte, off int64) error {
	if off < 0 || off >= x.size || int64(len(p)) > x.size-off {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// fetch reads p from the file, within the limits.
func (x *indexReader) fetch(p []byte, off int64) error {
	if x.reads++; x.reads > maxIndexReads {
		return limitErr("reads")
	}
	if x.bytes += int64(len(p)); x.bytes > maxIndexBytes {
		return limitErr("bytes")
	}
	n, err := x.r.ReadAt(p, off)
	if n == len(p) {
		return nil // ReadAt may report io.EOF together with the last bytes
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// windowEnd is where the window ends.
func (x *indexReader) windowEnd() int64 { return x.winOff + int64(len(x.win)) }

// windowed reports whether the n bytes at off are in the window: reading them
// costs nothing.
func (x *indexReader) windowed(off, n int64) bool {
	return off >= x.winOff && off+n <= x.windowEnd()
}

func (x *indexReader) inWindow(p []byte, off int64) bool {
	return off >= x.winOff && off+int64(len(p)) <= x.winOff+int64(len(x.win))
}

// readAt fills p from off. A small read that the last window does not cover
// fetches a new window starting at off, so walking forward through a structure
// costs one read per window; a large read goes straight to the file.
func (x *indexReader) readAt(p []byte, off int64) error {
	if len(p) == 0 {
		return nil
	}
	if err := x.check(p, off); err != nil {
		return err
	}
	if x.inWindow(p, off) {
		copy(p, x.win[off-x.winOff:])
		return nil
	}
	if len(p) >= indexWindow {
		return x.fetch(p, off)
	}
	return x.refill(p, off)
}

// readSmall is readAt for hops through a file (the headers of clusters, say):
// the next read will not be near this one, so there is no window to fetch.
func (x *indexReader) readSmall(p []byte, off int64) error {
	if len(p) == 0 {
		return nil
	}
	if err := x.check(p, off); err != nil {
		return err
	}
	if x.inWindow(p, off) {
		copy(p, x.win[off-x.winOff:])
		return nil
	}
	return x.fetch(p, off)
}

// refill fetches a window at off and copies p out of it.
func (x *indexReader) refill(p []byte, off int64) error {
	n := int(min(indexWindow, x.size-off))
	if cap(x.win) < n {
		x.win = make([]byte, n, indexWindow)
	}
	x.win = x.win[:n]
	if err := x.fetch(x.win, off); err != nil {
		x.win = x.win[:0]
		return err
	}
	x.winOff = off
	copy(p, x.win)
	return nil
}

// cover makes the window hold the n bytes at off, in one read unless it
// already does: for a structure worth reading whole, because on a network share
// one read of a few MB costs less than the dozens of small ones it replaces.
// The reads that follow inside it are served from memory.
func (x *indexReader) cover(off, n int64) error {
	if n <= 0 || off >= x.winOff && off+n <= x.winOff+int64(len(x.win)) {
		return nil
	}
	if off < 0 || off >= x.size || n > x.size-off || n > maxTableBytes {
		return io.ErrUnexpectedEOF
	}
	if int64(cap(x.win)) < n {
		x.win = make([]byte, n)
	}
	x.win = x.win[:n]
	if err := x.fetch(x.win, off); err != nil {
		x.win = x.win[:0]
		return err
	}
	x.winOff = off
	return nil
}

// readBody reads the n bytes at off into memory, refusing sizes that the file
// cannot hold or that are over max before allocating anything.
func (x *indexReader) readBody(off, n, max int64) ([]byte, error) {
	if n < 0 || n > max || off < 0 || off > x.size || n > x.size-off {
		return nil, fmt.Errorf("keyframe index: %d bytes at %d do not fit (file %d, limit %d)", n, off, x.size, max)
	}
	b := make([]byte, n)
	return b, x.readAt(b, off)
}
