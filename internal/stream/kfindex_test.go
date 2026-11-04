package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// countingReaderAt counts what the index readers ask of the file.
type countingReaderAt struct {
	r     io.ReaderAt
	bytes int64
	reads int
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	c.bytes += int64(len(p))
	return c.r.ReadAt(p, off)
}

// patternReader is a file that is never stored: prefix, then pattern repeated
// up to size bytes.
type patternReader struct {
	prefix, pattern []byte
	size            int64
}

func (r patternReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.size-off {
		n = int(r.size - off)
	}
	for i := 0; i < n; i++ {
		at := off + int64(i)
		switch {
		case at < int64(len(r.prefix)):
			p[i] = r.prefix[at]
		case len(r.pattern) > 0:
			p[i] = r.pattern[(at-int64(len(r.prefix)))%int64(len(r.pattern))]
		default:
			p[i] = 0
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// indexOf runs the index readers over data, counting what they read.
func indexOf(data []byte, videoIndex int) (kf []float64, source string, err error, cr *countingReaderAt) {
	cr = &countingReaderAt{r: bytes.NewReader(data)}
	kf, source, err = indexKeyframes(context.Background(), cr, int64(len(data)), videoIndex)
	return kf, source, err, cr
}

// wantTimes compares keyframe times to within a nanosecond.
func wantTimes(t *testing.T, got []float64, want ...float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d keyframes %v, want %d %v", len(got), head(got), len(want), head(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("keyframe %d = %v, want %v (got %v, want %v)", i, got[i], want[i], head(got), head(want))
		}
	}
}

func head(v []float64) []float64 {
	if len(v) > 6 {
		return v[:6]
	}
	return v
}

// every is n times spaced step apart, starting at first.
func every(n int, first, step float64) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = first + float64(i)*step
	}
	return v
}

// allocated reports the bytes allocated while f runs.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// wantLimit fails the test unless err is a limit of the index readers, and
// the one called what: bytes, reads, elements, keyframes or seekheads.
func wantLimit(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, errIndexLimit) || !strings.Contains(err.Error(), "("+what+")") {
		t.Fatalf("err = %v, want the %s limit", err, what)
	}
}

// The limits as documented, spelled out so that a test of them does not
// pass because the constant under test was changed along with it.
const (
	indexByteBudget = 64 << 20
	indexReadBudget = 1 << 15
)

// A well-formed file never gets near the limits; a file that asks for more
// stops at them, by bytes and by reads.
func TestIndexReaderLimits(t *testing.T) {
	const big = int64(1) << 40

	t.Run("bytes", func(t *testing.T) {
		cr := &countingReaderAt{r: patternReader{size: big}}
		x := &indexReader{r: cr, size: big}
		buf := make([]byte, 1<<20)
		var err error
		for i := 0; i < 1000 && err == nil; i++ {
			err = x.readAt(buf, int64(i)<<21)
		}
		if !errors.Is(err, errIndexLimit) {
			t.Fatalf("1000 reads of 1 MiB: err = %v, want the read limit", err)
		}
		if cr.bytes > indexByteBudget || cr.bytes < indexByteBudget/2 {
			t.Errorf("fetched %d bytes, want just under %d", cr.bytes, indexByteBudget)
		}
	})

	t.Run("reads", func(t *testing.T) {
		cr := &countingReaderAt{r: patternReader{size: big}}
		x := &indexReader{r: cr, size: big}
		var b [1]byte
		var err error
		for i := 0; i < 2*indexReadBudget && err == nil; i++ {
			err = x.readSmall(b[:], int64(i)<<20)
		}
		if !errors.Is(err, errIndexLimit) {
			t.Fatalf("tiny far-apart reads: err = %v, want the read limit", err)
		}
		if cr.reads != indexReadBudget {
			t.Errorf("made %d reads, want %d", cr.reads, indexReadBudget)
		}
	})

	t.Run("window", func(t *testing.T) {
		// Walking forward through a structure costs one read per window.
		cr := &countingReaderAt{r: patternReader{size: 1 << 20}}
		x := &indexReader{r: cr, size: 1 << 20}
		var b [16]byte
		for off := int64(0); off+16 <= 3*indexWindow/2; off += 16 {
			if err := x.readAt(b[:], off); err != nil {
				t.Fatal(err)
			}
		}
		if cr.reads != 2 {
			t.Errorf("%d reads for %d bytes of small reads, want 2", cr.reads, 3*indexWindow/2)
		}
		// The end of the file is a short window, not an error.
		if err := x.readAt(b[:], 1<<20-16); err != nil {
			t.Errorf("read of the last bytes: %v", err)
		}
		if err := x.readAt(b[:], 1<<20-15); err == nil {
			t.Error("a read past the end succeeded")
		}
	})
}

func TestReadBodyRefusesWhatTheFileCannotHold(t *testing.T) {
	x := &indexReader{r: bytes.NewReader(make([]byte, 100)), size: 100}
	if b, err := x.readBody(10, 90, 1<<20); err != nil || len(b) != 90 {
		t.Fatalf("readBody within the file: %d bytes, %v", len(b), err) // positive control
	}
	for _, c := range []struct{ off, n, max int64 }{{10, 91, 1 << 20}, {0, 1 << 40, 1 << 50}, {0, 50, 49}, {-1, 5, 10}, {101, 0, 10}, {0, -1, 10}} {
		var err error
		if a := allocated(func() { _, err = x.readBody(c.off, c.n, c.max) }); err == nil || a > 1<<16 {
			t.Errorf("readBody(%d, %d, %d): err %v, allocated %d bytes", c.off, c.n, c.max, err, a)
		}
	}
}

// fakeFFprobe writes a script that prints a fixed packet scan, or fails.
func fakeFFprobe(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The scan is the last resort: a file with no usable index gets it, a file with
// one never does.
func TestKeyframesFallsBackToTheScan(t *testing.T) {
	ctx := context.Background()
	scan := fakeFFprobe(t, `printf '1.400000,K__,\n1.442000,___,\n3.400000,K__,\n5.400000,K__,\n1.400000\n'`)
	broken := fakeFFprobe(t, "echo scan must not run >&2; exit 1")
	dir := t.TempDir()

	junk := filepath.Join(dir, "junk.mkv")
	if err := os.WriteFile(junk, bytes.Repeat([]byte("not a media file "), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := keyframesVia(ctx, scan, junk, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if r.source != sourceScan || !errors.Is(r.skip, errNoContainerIndex) {
		t.Errorf("source %q, skip %v, want the scan because the container is unknown", r.source, r.skip)
	}
	wantTimes(t, r.times, 0, 2, 4) // relative to the start time, like the index

	// A sparse index is not used either.
	sparse := goodMKV(func(f *mkvFile) {
		f.cues = f.cues[:0]
		f.cues = append(f.cues, mkvCue{0, 2}, mkvCue{50000, 2}, mkvCue{100000, 2})
	}).build()
	sp := filepath.Join(dir, "sparse.mkv")
	if err := os.WriteFile(sp, sparse, 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err = keyframesVia(ctx, scan, sp, 0, -1); err != nil || r.source != sourceScan {
		t.Errorf("sparse cues: source %q, err %v, want the scan", r.source, err)
	}

	// Positive control: with a usable index the scan is never started (the
	// fake fails when it is).
	good := filepath.Join(dir, "good.mkv")
	if err := os.WriteFile(good, goodMKV(nil).build(), 0o644); err != nil {
		t.Fatal(err)
	}
	kf, err := Keyframes(ctx, broken, good, 0)
	if err != nil {
		t.Fatalf("Keyframes with an index: %v", err)
	}
	wantTimes(t, kf, every(60, 0, 2)...)
	if r, err = keyframesVia(ctx, broken, good, 0, -1); err != nil || r.source != sourceCues || r.skip != nil {
		t.Errorf("source %q, skip %v, err %v, want cues", r.source, r.skip, err)
	}

	// A scan that fails is an error, not an empty index.
	if _, err := Keyframes(ctx, broken, junk, 0); err == nil {
		t.Error("a failing scan reported success")
	}
	// A cancelled context is reported as such, without starting the scan.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Keyframes(cctx, scan, junk, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}
