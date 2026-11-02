package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/library"
	"ply/internal/probe"
	"ply/internal/store"
)

// stubProber says every file has one video stream.
type stubProber struct{}

func (stubProber) Probe(_ context.Context, path string) (probe.Result, error) {
	return probe.Result{
		Container:   strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		DurationSec: 60,
		Bitrate:     1000,
		Streams:     []probe.Stream{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}},
	}, nil
}

// countingScanner counts the ScanDirs calls it passes on.
type countingScanner struct {
	*library.Scanner
	mu sync.Mutex
	n  int
}

func (c *countingScanner) ScanDirs(ctx context.Context, dirs []library.Dir) (library.Result, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Scanner.ScanDirs(ctx, dirs)
}

func (c *countingScanner) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// TestWatcherKeepsTheLibraryInSync drives the real scanner and store from
// real file system events, as the server does: files appear, grow, move and
// go away, and the rows follow.
func TestWatcherKeepsTheLibraryInSync(t *testing.T) {
	eachBackend(t, func(t *testing.T, bc backendCase) {
		const settle = 300 * time.Millisecond
		root := filepath.Join(t.TempDir(), "media root") // a space, as on a real Mac
		movies := filepath.Join(root, "Movies")
		if err := os.MkdirAll(movies, 0o755); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		log, _ := testLogger(t)
		sc := &countingScanner{Scanner: library.NewScanner(t.Context(), st, stubProber{}, []string{root}, log)}
		sc.SetSettle(settle)

		w := New(sc, []string{root}, log)
		w.mkBackend = bc.mk
		w.maxDirs = -1
		w.quiet, w.maxWait, w.retry, w.recheck = 100*time.Millisecond, time.Second, settle+100*time.Millisecond, time.Hour
		start(t, w)
		waitRoot(t, w, 0, "root watched", func(s RootStatus) bool { return s.Watching })

		states := func() map[string]store.FileState {
			m, err := st.FileStates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
		indexed := func(path string) bool { _, ok := states()[path]; return ok }
		waitIndexed := func(path string) int64 {
			t.Helper()
			waitFor(t, 15*time.Second, "indexing of "+path, func() bool { return indexed(path) })
			return states()[path].ID
		}
		waitMovedTo := func(from, to string, id int64) {
			t.Helper()
			waitFor(t, 15*time.Second, "the move to "+to, func() bool {
				m := states()
				return m[to].ID == id && !indexedIn(m, from)
			})
		}

		// 1. A file appears and is indexed.
		alpha := filepath.Join(movies, "Alpha (2020).mkv")
		touch(t, alpha, 100)
		id := waitIndexed(alpha)

		// 2. A file that keeps being written is not indexed until it stops.
		// An unrelated file made at the same time is: so the scanner does run
		// during the window, and it is the growing file that is held back.
		growing, stable := filepath.Join(movies, "Growing (2021).mkv"), filepath.Join(movies, "Stable (2019).mkv")
		callsBefore := sc.calls()
		f, err := os.Create(growing)
		if err != nil {
			t.Fatal(err)
		}
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			defer f.Close()
			for range 30 { // 2.4s
				f.Write(make([]byte, 10))
				time.Sleep(80 * time.Millisecond)
			}
		}()
		touch(t, stable, 150)
		waitIndexed(stable)
		select {
		case <-writerDone:
			t.Fatal("the writer finished before the stable file was indexed: the window shows nothing")
		default:
		}
		if indexed(growing) {
			t.Fatal("the growing file was indexed while it was being written")
		}
		// Keep looking while it grows; it must stay out, and the watcher must
		// keep coming back to it.
		for stillWriting := true; stillWriting; {
			select {
			case <-writerDone:
				stillWriting = false
			case <-time.After(100 * time.Millisecond):
				if indexed(growing) {
					t.Fatal("the growing file was indexed while it was being written")
				}
			}
		}
		got := sc.calls() - callsBefore
		t.Logf("%d scans while the file grew", got)
		if got < 2 {
			t.Fatalf("only %d scans during the window: the file was not held back, it was not looked at", got)
		}
		waitIndexed(growing) // and now it is

		// 3. Renaming a file keeps its id.
		renamed := filepath.Join(movies, "Alpha Renamed (2020).mkv")
		if err := os.Rename(alpha, renamed); err != nil {
			t.Fatal(err)
		}
		waitMovedTo(alpha, renamed, id)

		// 4. So does moving it to another directory.
		archive := filepath.Join(root, "Archive")
		mkdirs(t, root, "Archive")
		waitRoot(t, w, 0, "Archive watched", func(s RootStatus) bool { return s.Dirs == 3 })
		time.Sleep(4 * w.quiet) // let the scan of the new directory pass
		archived := filepath.Join(archive, "Alpha Renamed (2020).mkv")
		if err := os.Rename(renamed, archived); err != nil {
			t.Fatal(err)
		}
		waitMovedTo(renamed, archived, id)

		// 5. Renaming a directory keeps the ids of what is in it.
		vault := filepath.Join(root, "Vault")
		if err := os.Rename(archive, vault); err != nil {
			t.Fatal(err)
		}
		vaulted := filepath.Join(vault, "Alpha Renamed (2020).mkv")
		waitMovedTo(archived, vaulted, id)
		waitRoot(t, w, 0, "watches moved", func(s RootStatus) bool { return s.Dirs == 3 }) // root, Movies, Vault

		// 6. Deleting a file removes its row, and only its row.
		if !indexed(stable) || !indexed(growing) {
			t.Fatal("setup: the other files should be indexed before the delete")
		}
		if err := os.Remove(vaulted); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 15*time.Second, "the row to go", func() bool { return !indexed(vaulted) })
		if !indexed(stable) || !indexed(growing) {
			t.Fatal("the delete took other rows with it")
		}
	})
}

func indexedIn(m map[string]store.FileState, path string) bool {
	_, ok := m[path]
	return ok
}
