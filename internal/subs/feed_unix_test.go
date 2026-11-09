//go:build unix

package subs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeFFmpeg is an "ffmpeg" that appends its arguments, a line to a run, to a log next to
// itself, then does what body says (a shell script; $last is the last argument, where the
// output goes). The script is run once, so that the runs under test start at once (macOS
// vets the first run of a new executable).
func fakeFFmpeg(t *testing.T, body string) (ff, log string) {
	t.Helper()
	dir := t.TempDir()
	ff, log = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "log")
	script := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\necho \"$@\" >> " + log + "\nfor last; do :; done\n" + body
	if err := os.WriteFile(ff, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command(ff, "warm").Run()
	return ff, log
}

const writesACue = "printf 'WEBVTT\\n\\n00:00.000 --> 00:01.000\\nhi\\n' > \"$last\"\n"

// runs are the lines of the log: the arguments of each run of ffmpeg.
func runs(t *testing.T, log string) []string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func newFakeGen(t *testing.T, ff string) *Generator {
	t.Helper()
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs"), FFmpeg: ff})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func writeFile(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The container of a film is not opened by ffmpeg but read by ply and given to it on its
// standard input (openFeed says why): ffmpeg is told to read "pipe:0" and receives every
// byte of the file, in order. Any other file is opened by ffmpeg as it always was.
func TestAMatroskaContainerIsReadByPlyAndGivenToFFmpeg(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	ff, log := fakeFFmpeg(t, "cat > "+stdin+"\n"+writesACue)
	g := newFakeGen(t, ff)

	content := film(3 * feedChunk)
	mkvFile, other := writeFile(t, "film.mkv", content), writeFile(t, "film.mp4", []byte("not matroska"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if _, err := g.Path(ctx, Source{ItemID: 1, Key: "2", Input: mkvFile, Stream: 2, MTime: 5}); err != nil {
		t.Fatal(err)
	}
	if r := runs(t, log); len(r) != 1 || !strings.Contains(r[0], "-i pipe:0 ") || strings.Contains(r[0], mkvFile) {
		t.Errorf("ffmpeg was run as %q: want the container given on pipe:0", r)
	}
	if got, _ := os.ReadFile(stdin); !bytes.Equal(got, content) {
		t.Errorf("ffmpeg was given %d bytes, want the %d of the file, unchanged", len(got), len(content))
	}

	if _, err := g.Path(ctx, Source{ItemID: 2, Key: "2", Input: other, Stream: 2, MTime: 5}); err != nil {
		t.Fatal(err)
	}
	r := runs(t, log)
	if len(r) != 2 || !strings.Contains(r[1], "-i "+other+" ") || strings.Contains(r[1], "pipe:0") {
		t.Errorf("ffmpeg was run as %q: want it to open %s itself", r, other)
	}
	if got, _ := os.ReadFile(stdin); len(got) != 0 { // positive control: the script ran again, and read nothing
		t.Errorf("ffmpeg was given %d bytes of an MP4", len(got))
	}
}

// A Matroska file whose Tracks come after a Cluster cannot be read from a pipe (ffmpeg finds
// Tracks only by a seek to where the SeekHead says): ffmpeg opens it itself, as it did before.
func TestAFileWithItsTracksAfterTheClustersIsOpenedByFFmpeg(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	ff, log := fakeFFmpeg(t, "cat > "+stdin+"\n"+writesACue)
	g := newFakeGen(t, ff)
	late := writeFile(t, "late.mkv", mkv(el{idSeekHead, 80}, el{idInfo, 70}, el{idCluster, 4 * feedChunk}, el{idTracks, 900}))
	if _, err := g.Path(t.Context(), Source{ItemID: 1, Key: "2", Input: late, Stream: 2, MTime: 5}); err != nil {
		t.Fatal(err)
	}
	if r := runs(t, log); len(r) != 1 || !strings.Contains(r[0], "-i "+late+" ") || strings.Contains(r[0], "pipe:0") {
		t.Errorf("ffmpeg was run as %q: want it to open %s itself", r, late)
	}
	if got, _ := os.ReadFile(stdin); len(got) != 0 {
		t.Errorf("ffmpeg was given %d bytes on its standard input", len(got))
	}
}

// A sidecar subtitle is a small file that ffmpeg opens itself.
func TestASidecarIsOpenedByFFmpeg(t *testing.T) {
	ff, log := fakeFFmpeg(t, writesACue)
	g := newFakeGen(t, ff)
	srt := writeFile(t, "a.srt", film(10))
	if _, err := g.Path(t.Context(), Source{ItemID: 3, Key: "ext0", Input: srt, Stream: -1, MTime: 5}); err != nil {
		t.Fatal(err)
	}
	if r := runs(t, log); len(r) != 1 || !strings.Contains(r[0], "-i "+srt+" ") {
		t.Errorf("ffmpeg was run as %q: want it to open %s itself", r, srt)
	}
}

// What a pipe cannot do (here, a file the walk of its head takes for one that can be read in
// order, which ffmpeg then says it finds no streams in) is not a failure of the file: the run is
// started again with the file itself, and nothing is remembered against it. If the file does
// fail, that is remembered as it always was.
func TestAFailedRunOnAPipeIsTriedOnTheFile(t *testing.T) {
	ff, log := fakeFFmpeg(t, "case \"$*\" in *pipe:0*) cat > /dev/null; echo \"Stream map '' matches no streams\" >&2; exit 234;; esac\n"+writesACue)
	g := newFakeGen(t, ff)
	src := Source{ItemID: 1, Key: "2", Input: writeFile(t, "film.mkv", film(feedChunk)), Stream: 2, MTime: 5}
	if _, err := g.Path(t.Context(), src); err != nil {
		t.Fatalf("Path: %v", err)
	}
	r := runs(t, log)
	if len(r) != 2 || !strings.Contains(r[0], "-i pipe:0 ") || !strings.Contains(r[1], "-i "+src.Input+" ") {
		t.Errorf("ffmpeg was run as %q: want the pipe, then the file", r)
	}
	if err := g.Failed(src); err != nil {
		t.Errorf("a failure was remembered for the file: %v", err)
	}

	// Positive control: a file that fails both ways is a failure, after the two tries.
	ff2, log2 := fakeFFmpeg(t, "cat > /dev/null; echo broken >&2; exit 1\n")
	g2 := newFakeGen(t, ff2)
	bad := Source{ItemID: 2, Key: "2", Input: writeFile(t, "bad.mkv", film(feedChunk)), Stream: 2, MTime: 5}
	if _, err := g2.Path(t.Context(), bad); !errors.Is(err, ErrConvert) {
		t.Errorf("Path = %v, want ErrConvert", err)
	}
	if r := runs(t, log2); len(r) != 2 {
		t.Errorf("a file that fails both ways was tried %d times, want 2: %q", len(r), r)
	}
	if g2.Failed(bad) == nil {
		t.Error("the failure of a file that fails both ways was not remembered")
	}
}

// A file that ends before its size (cut while it is read: the share dropped, or it was
// replaced) is not a conversion failure and not for ever: it says where it ended, nothing is
// remembered, and it is not tried again with the first track alone (that would read the file
// again for nothing); the job asks again later.
func TestAFileThatEndsEarlyIsUnavailableAndNotTriedAgain(t *testing.T) {
	dir := t.TempDir()
	hold := filepath.Join(dir, "go")
	ff, log := fakeFFmpeg(t, "while [ ! -e "+hold+" ]; do sleep 0.02; done\ncat > /dev/null\n"+writesACue)
	g := newFakeGen(t, ff)
	path := writeFile(t, "film.mkv", film(16*feedChunk))
	fi, _ := os.Stat(path)
	a, b := Source{ItemID: 1, Key: "2", Input: path, Stream: 2, MTime: 5}, Source{ItemID: 1, Key: "3", Input: path, Stream: 3, MTime: 5}

	filled := make(chan error, 1)
	go func() { filled <- g.Fill(t.Context(), []Source{a, b}, nil, nil) }()
	waitFor(t, "ffmpeg to start", func() bool { r, _ := os.ReadFile(log); return len(r) > 0 })
	// ffmpeg takes nothing yet: the feed holds one chunk, and the file is cut under it.
	const cutAt = 300 << 10
	if err := os.Truncate(path, cutAt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-filled:
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrConvert) {
			t.Errorf("Fill = %v, want ErrUnavailable and not ErrConvert", err)
		}
		if want := "the file ended at " + strconv.Itoa(cutAt) + " of " + strconv.Itoa(int(fi.Size())) + " bytes"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Fill = %v, want it to say %q", err, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Fill did not end")
	}
	if r := runs(t, log); len(r) != 1 {
		t.Errorf("ffmpeg was run %d times, want once: %q", len(r), r)
	}
	for _, s := range []Source{a, b} {
		if err := g.Failed(s); err != nil {
			t.Errorf("a failure was remembered for track %s: %v", s.Key, err)
		}
	}
}

// A file that ends early but is as long as it was when it was looked at again lies about its
// length (the share says a size it does not serve): reading it again, on every backoff for days,
// would not change that. That is a failure, recorded as any is, once: no other tracks, no second
// read on the file itself. (One that has changed is unavailable: the test above.)
func TestAFileThatLiesAboutItsLengthIsAFailure(t *testing.T) {
	dir := t.TempDir()
	goFile, done, restored := filepath.Join(dir, "go"), filepath.Join(dir, "done"), filepath.Join(dir, "restored")
	ff, log := fakeFFmpeg(t, "while [ ! -e "+goFile+" ]; do sleep 0.02; done\ncat > /dev/null\n: > "+done+"\nwhile [ ! -e "+restored+" ]; do sleep 0.02; done\n"+writesACue)
	g := newFakeGen(t, ff)
	path := writeFile(t, "film.mkv", film(16*feedChunk))
	fi, _ := os.Stat(path)
	a, b := Source{ItemID: 1, Key: "2", Input: path, Stream: 2, MTime: 5}, Source{ItemID: 1, Key: "3", Input: path, Stream: 3, MTime: 5}

	filled := make(chan error, 1)
	go func() { filled <- g.Fill(t.Context(), []Source{a, b}, nil, nil) }()
	waitFor(t, "ffmpeg to start", func() bool { r, _ := os.ReadFile(log); return len(r) > 0 })
	const cutAt = 300 << 10
	if err := os.Truncate(path, cutAt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// ffmpeg has read what there was; before the run looks at the file again, it is as long as it was.
	waitFor(t, "ffmpeg to have read the file", func() bool { _, err := os.Stat(done); return err == nil })
	if err := os.Truncate(path, fi.Size()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restored, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-filled:
		if !errors.Is(err, ErrConvert) || errors.Is(err, ErrUnavailable) {
			t.Errorf("Fill = %v, want ErrConvert and not ErrUnavailable", err)
		}
		if want := "the file ended at " + strconv.Itoa(cutAt) + " of " + strconv.Itoa(int(fi.Size())) + " bytes, and it is still that long"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Fill = %v, want it to say %q", err, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Fill did not end")
	}
	if r := runs(t, log); len(r) != 1 {
		t.Errorf("ffmpeg was run %d times, want once: %q", len(r), r)
	}
	for _, s := range []Source{a, b} {
		if g.Failed(s) == nil {
			t.Errorf("no failure was remembered for track %s", s.Key)
		}
	}
}

// Once a run on a pipe has failed, the runs after it in the same call (the first track alone,
// when several failed) let ffmpeg open the file: the pipe has been shown not to do for it. (The
// track that was given up has a call of its own, which starts with the pipe again.)
func TestRunsAfterAFailedRunOnAPipeAreOnTheFile(t *testing.T) {
	// Fails on a pipe, and on the file with two tracks: only one track alone, on the file, works.
	ff, log := fakeFFmpeg(t, "case \"$*\" in *pipe:0*) cat > /dev/null; exit 234;; esac\n"+
		"n=0; for a; do [ \"$a\" = -map ] && n=$((n+1)); done\n[ $n -gt 1 ] && { echo two tracks >&2; exit 1; }\n"+writesACue)
	g := newFakeGen(t, ff)
	path := writeFile(t, "film.mkv", film(feedChunk))
	a, b := Source{ItemID: 1, Key: "2", Input: path, Stream: 2, MTime: 5}, Source{ItemID: 1, Key: "3", Input: path, Stream: 3, MTime: 5}
	_ = g.Fill(t.Context(), []Source{a, b}, nil, nil)

	r := runs(t, log)
	if len(r) < 3 {
		t.Fatalf("ffmpeg was run %d times, want at least 3 (the pipe, the file, the first track alone): %q", len(r), r)
	}
	if !strings.Contains(r[0], "-i pipe:0 ") || strings.Count(r[0], "-map") != 2 {
		t.Errorf("the first run was %q, want both tracks on the pipe", r[0])
	}
	if !strings.Contains(r[1], "-i "+path+" ") || strings.Count(r[1], "-map") != 2 {
		t.Errorf("the second run was %q, want both tracks, on the file", r[1])
	}
	if !strings.Contains(r[2], "-i "+path+" ") || strings.Contains(r[2], "pipe:0") || strings.Count(r[2], "-map") != 1 {
		t.Errorf("the third run was %q, want the first track alone, with ffmpeg opening %s itself", r[2], path)
	}
	if !g.Cached(a) { // positive control: the first track alone did its work
		t.Error("the first track was not written")
	}
}

// openFiles is the number of files this process has open.
func openFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("no /dev/fd: %v", err)
	}
	return len(entries)
}

// A run that was given the container to read leaves the file closed, whatever the run did.
func TestRunsOnAFeedLeaveNoFileOpen(t *testing.T) {
	// A file nobody closed is closed by the garbage collector some time later: it must not be
	// allowed to hide the leak.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	ff, log := fakeFFmpeg(t, "cat > /dev/null\n"+writesACue)
	g := newFakeGen(t, ff)
	path := writeFile(t, "film.mkv", film(2*feedChunk))
	before := openFiles(t)
	for i := range 4 {
		if _, err := g.Path(t.Context(), Source{ItemID: int64(i + 1), Key: "2", Input: path, Stream: 2, MTime: 5}); err != nil {
			t.Fatal(err)
		}
	}
	if after := openFiles(t); after != before {
		t.Errorf("%d files open after four runs, %d before", after, before)
	}
	for _, line := range runs(t, log) { // positive control: the runs were on a feed
		if !strings.Contains(line, "-i pipe:0 ") {
			t.Errorf("a run did not use the feed: %q", line)
		}
	}
}
