package art

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// clock is a settable time source for failure-TTL tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestTransientFailuresAreRetriedAfterTheTTL(t *testing.T) {
	for name, failErr := range map[string]error{
		"timeout":          context.DeadlineExceeded, // grab maps it to ErrUnavailable
		"ffmpeg I/O error": fmt.Errorf("%w: exit status 1: Input/output error", ErrUnavailable),
		"spawn error":      errors.New("start ffmpeg: fork/exec: resource temporarily unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			s := &stub{fail: func([]string) bool { return true }, failErr: failErr}
			g := newTestGenerator(t, t.TempDir(), s)
			c := &clock{t: time.Unix(1_000_000, 0)}
			g.now = c.now
			ctx := context.Background()

			_, err := g.Path(ctx, src(1, 10), Thumb)
			if err == nil || errors.Is(err, ErrNoFrame) {
				t.Fatalf("err = %v; want a transient failure, not ErrNoFrame", err)
			}
			if _, err := g.Path(ctx, src(1, 10), Thumb); err == nil || g.Runs() != 1 {
				t.Fatalf("within the TTL: err = %v, runs = %d; want the remembered failure and no new run", err, g.Runs())
			}
			c.t = c.t.Add(failureTTL + time.Second)
			s.fail = nil
			if _, err := g.Path(ctx, src(1, 10), Thumb); err != nil || g.Runs() != 2 {
				t.Fatalf("after the TTL: err = %v, runs = %d; want a fresh, successful run", err, g.Runs())
			}
		})
	}
}

func TestTimeoutIsReportedAsUnavailable(t *testing.T) {
	s := &stub{fail: func([]string) bool { return true }, failErr: context.DeadlineExceeded}
	g := newTestGenerator(t, t.TempDir(), s)
	_, err := g.Path(context.Background(), src(1, 10), Thumb)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestMissingInputRunsNoFFmpegAndIsRetriedNextTime(t *testing.T) {
	dir := t.TempDir()
	g := newTestGenerator(t, dir, &stub{})
	in := src(1, 10)
	in.Path = filepath.Join(dir, "on-a-share-not-mounted-yet.mkv")

	_, err := g.Path(context.Background(), in, Thumb)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v; want ErrUnavailable wrapping fs.ErrNotExist", err)
	}
	if g.Runs() != 0 {
		t.Fatalf("missing input started %d ffmpeg runs", g.Runs())
	}
	if err := os.WriteFile(in.Path, nil, 0o644); err != nil { // the share is mounted again
		t.Fatal(err)
	}
	if _, err := g.Path(context.Background(), in, Thumb); err != nil || g.Runs() != 1 {
		t.Fatalf("after the file appeared: err = %v, runs = %d; want success", err, g.Runs())
	}
}

func TestCloseStopsGenerationsAndRefusesNewOnes(t *testing.T) {
	// A cancelled run takes a while to exit, like ffmpeg being reaped.
	s := &stub{block: make(chan struct{}), slowStop: 150 * time.Millisecond}
	g := newTestGenerator(t, t.TempDir(), s)

	done := make(chan error, 1)
	go func() { _, err := g.Path(context.Background(), src(1, 10), Thumb); done <- err }()
	waitFor(t, func() bool { a, _ := s.snapshot(); return a == 1 })

	closed := make(chan struct{})
	go func() { g.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return: it must cancel the run in flight and wait for it")
	}
	if a, _ := s.snapshot(); a != 0 {
		t.Fatal("Close returned while a run was still in flight: it must wait for every generation")
	}
	if err := <-done; err == nil {
		t.Fatal("the request cut short by Close reported success")
	}
	if _, err := g.Path(context.Background(), src(2, 10), Thumb); !errors.Is(err, ErrClosed) {
		t.Fatalf("Path after Close = %v, want ErrClosed", err)
	}
	if g.Runs() != 1 {
		t.Fatalf("runs = %d after Close, want 1", g.Runs())
	}
}

func TestPruneKeepsTheTmpOfAGenerationInFlight(t *testing.T) {
	dir := t.TempDir()
	g := newTestGenerator(t, dir, &stub{})
	fresh := filepath.Join(dir, "1-thumb-10.jpg.tmp")
	stale := filepath.Join(dir, "2-thumb-10.jpg.tmp")
	for _, p := range []string{fresh, stale} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Prune(map[int64]int64{1: 10, 2: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh .tmp of an in-flight generation was pruned: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .tmp survived prune (err = %v)", err)
	}
}

func TestPruneForgetsFailuresOfItemsThatAreGone(t *testing.T) {
	s := &stub{fail: func([]string) bool { return true }}
	g := newTestGenerator(t, t.TempDir(), s)
	ctx := context.Background()
	if _, err := g.Path(ctx, src(1, 10), Thumb); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("precondition: err = %v, want a remembered ErrNoFrame", err)
	}
	if _, err := g.Prune(map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	s.fail = nil
	if _, err := g.Path(ctx, src(1, 10), Thumb); err != nil || g.Runs() != 2 {
		t.Fatalf("after prune: err = %v, runs = %d; want the failure forgotten and a new run", err, g.Runs())
	}
}

func TestFailedGrabLeavesNoPartialFiles(t *testing.T) {
	dir := t.TempDir()
	s := &stub{fail: func([]string) bool { return true }, partial: true}
	g := newTestGenerator(t, dir, s)
	if _, err := g.Path(context.Background(), src(1, 10), Thumb); err == nil {
		t.Fatal("expected the grab to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".jpg") {
			t.Errorf("failed grab left %s behind", e.Name())
		}
	}
}

func TestNewestColourFileWinsOnLoad(t *testing.T) {
	dir := t.TempDir()
	for name, hex := range map[string]string{"7-5.color2": "#111111", "7-9.color2": "#999999"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(hex), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := newTestGenerator(t, dir, &stub{})
	if hex, ok := g.Colour(src(7, 9)); !ok || hex != "#999999" {
		t.Errorf("Colour(mtime 9) = %q, %v; want #999999", hex, ok)
	}
	if _, ok := g.Colour(src(7, 5)); ok {
		t.Error("the older colour file won")
	}
}

func TestCorruptColourFilesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	for i, bad := range []string{"javascript", "#12", "#zzzzzz", "url(x)"} {
		name := fmt.Sprintf("%d-10.color2", i+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := newTestGenerator(t, dir, &stub{})
	for i := range 4 {
		if hex, ok := g.Colour(src(int64(i+1), 10)); ok {
			t.Errorf("corrupt colour file %d loaded as %q (it would reach the page's --tint)", i+1, hex)
		}
	}
}

func TestGrabDecodesKeyframesOnlyAndNeverReadsStdin(t *testing.T) {
	s := &stub{}
	g := newTestGenerator(t, t.TempDir(), s)
	if _, err := g.Path(context.Background(), src(1, 10), Thumb); err != nil {
		t.Fatal(err)
	}
	args := s.calls[0]
	i := slices.Index(args, "-skip_frame")
	if i < 0 || args[i+1] != "nokey" || i > slices.Index(args, "-i") {
		t.Errorf("want -skip_frame nokey as an input option: %q", args)
	}
	if !slices.Contains(args, "-nostdin") {
		t.Errorf("want -nostdin: %q", args)
	}
	if j := slices.Index(args, "-i"); j < 0 || args[j+1] != fixtureMedia {
		t.Errorf("input is not the source path: %q", args)
	}
}

func TestContentFailureClassification(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Stream specifier ':v:0' in filtergraph description … matches no streams.": true,
		"Output file does not contain any stream":                                  true,
		"x.mkv: Invalid data found when processing input":                          true,
		"x.mkv: Input/output error":                                                false,
		"x.mkv: Permission denied":                                                 false,
		"":                                                                         false,
	} {
		if got := contentFailure(stderr); got != want {
			t.Errorf("contentFailure(%q) = %v, want %v", stderr, got, want)
		}
	}
}
