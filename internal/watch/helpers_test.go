package watch

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/library"
)

// together gates calls that are to run n at a time: each call waits until n are
// in, or a second. One at a time, none ever sees another.
type together struct {
	n        int
	mu       sync.Mutex
	in, peak int
	opened   bool // all is closed
	all      chan struct{}
}

func newTogether(n int) *together { return &together{n: n, all: make(chan struct{})} }

func (g *together) wait() {
	g.mu.Lock()
	g.in++
	g.peak = max(g.peak, g.in)
	if g.in == g.n && !g.opened {
		g.opened = true
		close(g.all)
	}
	g.mu.Unlock()
	select {
	case <-g.all:
	case <-time.After(time.Second):
	}
	g.mu.Lock()
	g.in--
	g.mu.Unlock()
}

// most is how many calls were in at once, at most.
func (g *together) most() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// waitFor polls cond until it holds or timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// logBuf collects log output; it is printed if the test fails.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// count returns how many log lines contain all of subs.
func (l *logBuf) count(subs ...string) int {
	n := 0
	for line := range strings.SplitSeq(l.String(), "\n") {
		if line != "" && !slices.ContainsFunc(subs, func(s string) bool { return !strings.Contains(line, s) }) {
			n++
		}
	}
	return n
}

func testLogger(t *testing.T) (*slog.Logger, *logBuf) {
	lb := &logBuf{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log:\n%s", lb.String())
		}
	})
	return slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug})), lb
}

type call struct {
	dirs []library.Dir
	at   time.Time
}

// fakeScanner records the ScanDirs calls.
type fakeScanner struct {
	mu    sync.Mutex
	calls []call
	// fn, if set, answers call n (1-based).
	fn func(ctx context.Context, n int, dirs []library.Dir) (library.Result, error)
}

func (f *fakeScanner) ScanDirs(ctx context.Context, dirs []library.Dir) (library.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{dirs: slices.Clone(dirs), at: time.Now()})
	n, fn := len(f.calls), f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, n, dirs)
	}
	return library.Result{}, nil
}

func (f *fakeScanner) snapshot() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeScanner) count() int { return len(f.snapshot()) }

func (f *fakeScanner) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// saw reports whether some call so far had a dir satisfying match.
func (f *fakeScanner) saw(match func(library.Dir) bool) bool {
	for _, c := range f.snapshot() {
		if slices.ContainsFunc(c.dirs, match) {
			return true
		}
	}
	return false
}

func (f *fakeScanner) sawPath(path string) bool {
	return f.saw(func(d library.Dir) bool { return d.Path == path })
}

// fakeBackend is a backend the test drives by hand.
type fakeBackend struct {
	ev   chan event
	errc chan error

	mu      sync.Mutex
	added   map[string]int // add calls per directory
	live    map[string]bool
	removed map[string]int
	addErr  func(dir string) error
	closed  bool
	unnamed bool // events carry no names, like kqueue's
}

func (b *fakeBackend) nameless() bool { return b.unnamed }

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		ev: make(chan event, 1024), errc: make(chan error, 16),
		added: map[string]int{}, live: map[string]bool{}, removed: map[string]int{},
	}
}

func (b *fakeBackend) add(dir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.added[dir]++
	if b.addErr != nil {
		if err := b.addErr(dir); err != nil {
			return err
		}
	}
	b.live[dir] = true
	return nil
}

func (b *fakeBackend) remove(dir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed[dir]++
	delete(b.live, dir)
	return nil
}

func (b *fakeBackend) events() <-chan event { return b.ev }
func (b *fakeBackend) errors() <-chan error { return b.errc }

func (b *fakeBackend) close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

// watched returns the directories being watched now, sorted.
func (b *fakeBackend) watched() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.live))
	for d := range b.live {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func (b *fakeBackend) addCalls(dir string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.added[dir]
}

func (b *fakeBackend) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// mkdirs creates the directories (and parents) under root.
func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// touch writes a small file, creating its directory.
func touch(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolvedTemp is t.TempDir() without symlinks in it (on macOS /var is one).
func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// start runs w until the returned stop is called (or the test ends), and
// waits for the roots that exist to be watched.
func start(t *testing.T, w *Watcher) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return within 5s of cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

// waitRoot waits until the status of the root at index i satisfies ok.
func waitRoot(t *testing.T, w *Watcher, i int, what string, ok func(RootStatus) bool) {
	t.Helper()
	waitFor(t, 10*time.Second, what, func() bool { return ok(w.Status()[i]) })
}

func dirsOf(c call) []string {
	var out []string
	for _, d := range c.dirs {
		out = append(out, d.Path)
	}
	return out
}
