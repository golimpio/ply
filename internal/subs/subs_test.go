package subs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsText(t *testing.T) {
	for codec, want := range map[string]bool{
		"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "SubRip": true,
		"hdmv_pgs_subtitle": false, "dvd_subtitle": false, "dvb_subtitle": false, "": false, "eia_608": false,
	} {
		if got := IsText(codec); got != want {
			t.Errorf("IsText(%q) = %v, want %v", codec, got, want)
		}
	}
}

func TestCacheName(t *testing.T) {
	emb := cacheName(Source{ItemID: 7, Key: "3", Input: "/m/a.mkv", Stream: 3, MTime: 1234})
	if emb != "7-3-1234.vtt" {
		t.Errorf("embedded name = %q", emb)
	}
	a := cacheName(Source{ItemID: 7, Key: "ext0", Input: "/m/a.en.srt", Stream: -1, MTime: 1234})
	b := cacheName(Source{ItemID: 7, Key: "ext0", Input: "/m/a.fr.srt", Stream: -1, MTime: 1234})
	if !strings.HasPrefix(a, "7-ext0-1234-") || !strings.HasSuffix(a, ".vtt") {
		t.Errorf("sidecar name = %q", a)
	}
	if a == b {
		t.Error("two different sidecars with the same key and mtime share a cache file")
	}
}

// fakeRun stands in for ffmpeg: it writes a VTT to the last argument.
type fakeRun struct {
	calls   atomic.Int64
	args    [][]string
	mu      sync.Mutex
	fail    error
	started chan struct{}
	release chan struct{}
}

func (f *fakeRun) run(ctx context.Context, args []string) (string, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.args = append(f.args, args)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	if f.fail != nil {
		return "boom: bad input\nsecond line", f.fail
	}
	return "", os.WriteFile(args[len(args)-1], []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n"), 0o644)
}

func newGen(t *testing.T, f *fakeRun) *Generator {
	t.Helper()
	g, err := New(Options{Dir: filepath.Join(t.TempDir(), "subs dir")})
	if err != nil {
		t.Fatal(err)
	}
	g.run = f.run
	return g
}

func TestPathConvertsOnceAndCaches(t *testing.T) {
	f := &fakeRun{}
	g := newGen(t, f)
	src := Source{ItemID: 1, Key: "2", Input: "/m/a.mkv", Stream: 2, MTime: 5}

	p, err := g.Path(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); !strings.HasPrefix(string(got), "WEBVTT") {
		t.Errorf("cached file = %q", got)
	}
	if _, err := g.Path(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1 (second call is a cache hit)", n)
	}
	// An embedded stream is selected by its absolute index.
	args := strings.Join(f.args[0], " ")
	if !strings.Contains(args, "-map 0:2") || !strings.Contains(args, "-c:s webvtt") {
		t.Errorf("args = %s", args)
	}

	// A changed file (new mtime) is a new cache entry.
	src.MTime = 6
	if _, err := g.Path(t.Context(), src); err != nil || f.calls.Load() != 2 {
		t.Errorf("after mtime change: err %v, runs %d", err, f.calls.Load())
	}
}

func TestPathSharesConcurrentRuns(t *testing.T) {
	f := &fakeRun{started: make(chan struct{}, 4), release: make(chan struct{})}
	g := newGen(t, f)
	src := Source{ItemID: 1, Key: "ext0", Input: "/m/a.srt", Stream: -1, MTime: 5}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Path(t.Context(), src)
			errs <- err
		}()
	}
	<-f.started // the one conversion is running, the others are waiting for it
	time.Sleep(50 * time.Millisecond)
	close(f.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times for 3 concurrent requests, want 1", n)
	}
}

func TestFailureIsRememberedUntilTheFileChanges(t *testing.T) {
	f := &fakeRun{fail: errors.New("exit status 1")}
	g := newGen(t, f)
	src := Source{ItemID: 1, Key: "4", Input: "/m/a.mkv", Stream: 4, MTime: 5}

	_, err := g.Path(t.Context(), src)
	if !errors.Is(err, ErrConvert) || !strings.Contains(err.Error(), "boom: bad input") {
		t.Fatalf("err = %v, want ErrConvert carrying ffmpeg's first line", err)
	}
	if _, err := g.Path(t.Context(), src); !errors.Is(err, ErrConvert) {
		t.Errorf("second err = %v", err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("ffmpeg ran %d times, want 1 (the failure is cached)", n)
	}
	entries, _ := os.ReadDir(g.dir)
	if len(entries) != 0 {
		t.Errorf("a failed conversion left %d files behind", len(entries))
	}
	src.MTime = 6
	f.fail = nil
	if _, err := g.Path(t.Context(), src); err != nil {
		t.Errorf("after the file changed: %v", err)
	}
}

func TestEmptyOutputIsAFailure(t *testing.T) {
	g, _ := New(Options{Dir: t.TempDir()})
	g.run = func(ctx context.Context, args []string) (string, error) {
		return "", os.WriteFile(args[len(args)-1], nil, 0o644)
	}
	_, err := g.Path(t.Context(), Source{ItemID: 1, Key: "1", Input: "/m/a.mkv", Stream: 1, MTime: 1})
	if !errors.Is(err, ErrConvert) {
		t.Errorf("err = %v, want ErrConvert", err)
	}
}

func TestSidecarEncoding(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	utf8 := write("utf8.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\ncafé\n"))
	bom := write("bom.srt", []byte("\xef\xbb\xbf1\n00:00:01,000 --> 00:00:02,000\ncafé\n"))
	latin := write("latin.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\ncaf\xe9\n"))
	utf16 := write("utf16.srt", []byte{0xff, 0xfe, '1', 0, '\n', 0})
	for path, want := range map[string]string{utf8: "", bom: "", latin: "WINDOWS-1252", utf16: "", filepath.Join(dir, "missing"): ""} {
		if got := legacyCharset(path); got != want {
			t.Errorf("legacyCharset(%s) = %q, want %q", filepath.Base(path), got, want)
		}
	}

	// The flag reaches ffmpeg, before -i, only for the sidecar that needs it.
	f := &fakeRun{}
	g := newGen(t, f)
	if _, err := g.Path(t.Context(), Source{ItemID: 1, Key: "ext0", Input: latin, Stream: -1, MTime: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Path(t.Context(), Source{ItemID: 1, Key: "ext1", Input: utf8, Stream: -1, MTime: 1}); err != nil {
		t.Fatal(err)
	}
	if a := strings.Join(f.args[0], " "); !strings.Contains(a, "-sub_charenc WINDOWS-1252 -i "+latin) {
		t.Errorf("latin args = %s", a)
	}
	if a := strings.Join(f.args[1], " "); strings.Contains(a, "-sub_charenc") || strings.Contains(a, "-map") {
		t.Errorf("utf8 sidecar args = %s", a)
	}
}

func TestPrune(t *testing.T) {
	g, _ := New(Options{Dir: t.TempDir()})
	files := map[string]bool{ // name -> kept
		"1-3-100.vtt":             true,  // embedded, item unchanged
		"1-4-100.vtt":             true,  // another track of the same item
		"1-3-90.vtt":              false, // embedded, item changed since
		"2-3-100.vtt":             false, // item gone
		"1-ext0-777-0a0b0c0d.vtt": true,  // sidecar: only the item has to exist
		"2-ext0-777-0a0b0c0d.vtt": false, // item gone
		"notes.txt":               false,
		"1-3-100.vtt.tmp":         false, // old leftover of a crashed conversion
	}
	for name := range files {
		p := filepath.Join(g.dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-48 * time.Hour)
		os.Chtimes(p, old, old)
	}
	// A temp file that is being written right now survives.
	fresh := filepath.Join(g.dir, "1-5-100.vtt.tmp")
	os.WriteFile(fresh, []byte("x"), 0o644)

	n, err := g.Prune(map[int64]int64{1: 100})
	if err != nil {
		t.Fatal(err)
	}
	wantRemoved := 0
	for name, kept := range files {
		_, err := os.Stat(filepath.Join(g.dir, name))
		if kept != (err == nil) {
			t.Errorf("%s: kept = %v, want %v", name, err == nil, kept)
		}
		if !kept {
			wantRemoved++
		}
	}
	if n != wantRemoved {
		t.Errorf("Prune removed %d, want %d", n, wantRemoved)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("Prune removed a temp file that is still being written")
	}
}
