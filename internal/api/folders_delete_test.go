package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/library"
	"ply/internal/store"
)

// libraryFake stands in for the scanner and the watcher. It has a set of roots
// and knows the files each root holds on disk; a scan indexes the files of every
// root it has, as a periodic scan or a watcher event does. Its SetRoots and
// DropRoot run a scan on either side of their work, as one that was running would.
type libraryFake struct {
	st *store.Store

	mu    sync.Mutex
	roots []string
	disk  map[string][]string // root -> names of the files on disk
	steps []string            // SetRoots and DropRoot, in the order they were called
}

func (l *libraryFake) scan() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, root := range l.roots {
		for _, name := range l.disk[root] {
			it := &store.Item{Kind: store.KindMovie, Title: name, Path: filepath.Join(root, name), Root: root, Size: 5}
			if _, err := l.st.UpsertItem(context.Background(), it, nil); err != nil {
				panic(err)
			}
		}
	}
}

func (l *libraryFake) setRoots(roots []library.Root) {
	l.scan() // a scan that is running, with the roots it had
	l.mu.Lock()
	l.roots = l.roots[:0:0]
	for _, r := range roots {
		l.roots = append(l.roots, r.Path)
	}
	l.steps = append(l.steps, "roots:"+strings.Join(l.roots, ","))
	l.mu.Unlock()
	l.scan() // and the next one
}

func (l *libraryFake) dropRoot(ctx context.Context, path string, forget bool) (int, error) {
	l.scan()
	l.mu.Lock()
	l.steps = append(l.steps, "drop:"+path)
	l.mu.Unlock()
	n, err := l.st.ArchiveAndDeleteRoot(ctx, path, forget)
	l.scan()
	return n, err
}

// A scan that comes between the steps of a removal must not bring the folder's
// files back: the scanner stops covering the folder before the files are dropped.
func TestDeleteFolderStopsTheScannerBeforeDroppingTheFiles(t *testing.T) {
	ctx := context.Background()
	f := newFolderEnv(t, "", "movies:movies")
	other := f.path("other")
	lib := &libraryFake{st: f.store, disk: map[string][]string{f.path("movies"): {"m.mp4"}, other: {"a.mp4", "b.mp4"}}}
	f.srv.SetRoots, f.srv.DropRoot = lib.setRoots, lib.dropRoot
	ui := f.add(other, "movies")
	lib.roots = []string{f.path("movies"), other}
	lib.scan()
	counts := func() map[string]int {
		c, err := f.store.CountItemsByRoot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	// Positive controls: the files are indexed; and a scan that still has the root
	// brings them back after they were dropped, which is what the order must prevent.
	if c := counts(); c[other] != 2 || c[f.path("movies")] != 1 {
		t.Fatalf("control: after the first scan %v", c)
	}
	if _, err := f.store.ArchiveAndDeleteRoot(ctx, other, true); err != nil {
		t.Fatal(err)
	}
	if counts()[other] != 0 {
		t.Fatal("control: the files were not dropped")
	}
	lib.scan()
	if counts()[other] != 2 {
		t.Fatalf("control: a scan with the root in its set did not bring the files back: %v", counts())
	}

	wantStatus(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 204)

	c := counts()
	if c[other] != 0 {
		t.Errorf("%d files of the removed folder are back in the library", c[other])
	}
	if c[f.path("movies")] != 1 {
		t.Errorf("the other folder's files = %d, want 1", c[f.path("movies")])
	}
	if want := []string{"roots:" + f.path("movies"), "drop:" + other}; !slices.Equal(lib.steps, want) {
		t.Errorf("steps = %v, want %v", lib.steps, want)
	}
	if !slices.Equal(lib.roots, []string{f.path("movies")}) {
		t.Errorf("the scanner still covers %v", lib.roots)
	}
	if _, err := f.store.GetFolder(ctx, ui.ID); err == nil {
		t.Error("the folder is still saved")
	}
}

// A removal that fails puts the folder back in the scanner's set, so that it goes
// on being scanned, and its files stay.
func TestDeleteFolderThatFailsKeepsTheFolderScanned(t *testing.T) {
	ctx := context.Background()
	f := newFolderEnv(t, "")
	other := f.path("other")
	lib := &libraryFake{st: f.store, disk: map[string][]string{other: {"a.mp4"}}}
	f.srv.SetRoots = lib.setRoots
	f.srv.DropRoot = func(ctx context.Context, path string, forget bool) (int, error) {
		lib.mu.Lock()
		lib.steps = append(lib.steps, "drop:"+path)
		lib.mu.Unlock()
		return 0, context.DeadlineExceeded
	}
	ui := f.add(other, "movies")
	lib.roots = []string{other}
	lib.scan()

	wantJSONError(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 500)
	if !slices.Equal(lib.roots, []string{other}) {
		t.Errorf("the scanner covers %v after a failed removal, want %v", lib.roots, []string{other})
	}
	if want := []string{"roots:", "drop:" + other, "roots:" + other}; !slices.Equal(lib.steps, want) {
		t.Errorf("steps = %v, want %v", lib.steps, want)
	}
	if c, _ := f.store.CountItemsByRoot(ctx); c[other] != 1 {
		t.Errorf("the folder's files = %d, want 1", c[other])
	}
	if _, err := f.store.GetFolder(ctx, ui.ID); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}
}

// A removal that finds a scan taking its time is a 503 that says to try again: the
// folder stays, scanned, and its files too.
func TestDeleteFolderBehindAStuckScanIs503(t *testing.T) {
	ctx := context.Background()
	f := newFolderEnv(t, "")
	other := f.path("other")
	lib := &libraryFake{st: f.store, disk: map[string][]string{other: {"a.mp4"}}}
	f.srv.SetRoots = lib.setRoots
	f.srv.DropRoot = func(ctx context.Context, path string, forget bool) (int, error) {
		return 0, fmt.Errorf("drop %s: %w", path, library.ErrScanBusy)
	}
	ui := f.add(other, "movies")
	lib.roots = []string{other}
	lib.scan()

	rec := f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, "")
	wantJSONError(t, rec, 503)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if !slices.Equal(lib.roots, []string{other}) {
		t.Errorf("the scanner covers %v after a busy scan, want %v", lib.roots, []string{other})
	}
	if c, _ := f.store.CountItemsByRoot(ctx); c[other] != 1 {
		t.Errorf("the folder's files = %d, want 1", c[other])
	}
	if _, err := f.store.GetFolder(ctx, ui.ID); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}
}

// record adds a step to the log of what the scanner was told (see newFolderEnv).
func (f *folderEnv) record(step string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, step)
}

// A client that goes away while DropRoot waits for a scan must not leave the
// folder saved and no longer scanned: the scanner gets the whole set back, from
// memory, because the request's context is cancelled and a read of the store with
// it would fail.
func TestDeleteFolderRestoresTheRootsWhenTheClientHasGone(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies")
	other := f.path("other")
	ui := f.add(other, "movies")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.srv.DropRoot = func(_ context.Context, path string, forget bool) (int, error) {
		f.record("drop:" + path)
		cancel()                             // the client disconnects while DropRoot waits for a scan...
		return 0, errors.New("disk on fire") // ...and the drop fails
	}

	// Control: a cancelled context does make the store fail, which is what a restore
	// that read the roots again would run into.
	before, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	if _, err := f.store.Folders(before); err == nil {
		t.Fatal("control: a read of the store with a cancelled context succeeded")
	}

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	wantJSONError(t, rec, 500)

	want := []string{
		"roots:" + f.path("movies") + "=movies",
		"drop:" + other,
		"roots:" + f.path("movies") + "=movies," + other + "=movies", // the whole set, as it was
	}
	if calls := f.callLog(); !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if _, err := f.store.GetFolder(context.Background(), ui.ID); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}
}

// A client that goes away does not leave a removal half done either: it runs to
// its end.
func TestDeleteFolderGoesOnWhenTheClientHasGone(t *testing.T) {
	f := newFolderEnv(t, "")
	other := f.path("other")
	ui := f.add(other, "movies")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var dropCtxErr error
	f.srv.DropRoot = func(dctx context.Context, path string, forget bool) (int, error) {
		cancel()
		dropCtxErr = dctx.Err()
		return f.store.ArchiveAndDeleteRoot(dctx, path, forget)
	}
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil).WithContext(ctx)
	f.srv.ServeHTTP(httptest.NewRecorder(), req)
	if dropCtxErr != nil {
		t.Errorf("DropRoot got a cancelled context: %v", dropCtxErr)
	}
	if _, err := f.store.GetFolder(context.Background(), ui.ID); err == nil {
		t.Error("the folder is still saved")
	}
}

// The row is deleted after the files are dropped. When that fails the files are
// gone and the folder is not, so the scanner must cover the folder again: a scan
// brings the files back, and what was archived is restored.
func TestDeleteFolderRestoresTheRootsWhenTheRowCannotBeDeleted(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies")
	other := f.path("other")
	ui := f.add(other, "movies")
	f.srv.DropRoot = func(ctx context.Context, path string, forget bool) (int, error) {
		f.record("drop:" + path)
		n, err := f.store.ArchiveAndDeleteRoot(ctx, path, forget)
		f.store.Close() // the database goes away before the row is deleted
		return n, err
	}

	wantJSONError(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 500)

	want := []string{
		"roots:" + f.path("movies") + "=movies",
		"drop:" + other,
		"roots:" + f.path("movies") + "=movies," + other + "=movies",
	}
	if calls := f.callLog(); !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

// A removal waits for the scan, with the folders lock: nothing else changes the
// folders meanwhile. Otherwise a folder added or changed meanwhile would be told
// to the scanner in the middle of the removal, or refused for a folder that is
// about to be gone.
func TestDeleteFolderHoldsTheFoldersLock(t *testing.T) {
	movies := func(f *folderEnv) string { return f.path("movies") }
	for _, tc := range []struct {
		name string
		do   func(f *folderEnv, ui store.Folder) *httptest.ResponseRecorder
		want int // once the removal is over
	}{
		// While the folder is still there this one is a 409; after the removal, an add.
		{"an add of the same folder", func(f *folderEnv, _ store.Folder) *httptest.ResponseRecorder {
			return f.post(movies(f), "movies")
		}, 201},
		// A 400 while the folder is still there (it holds the new one).
		{"an add of a folder inside it", func(f *folderEnv, _ store.Folder) *httptest.ResponseRecorder {
			return f.post(f.path("movies/extras"), "movies")
		}, 201},
		// A 200 while the folder is still there.
		{"a change of its type", func(f *folderEnv, ui store.Folder) *httptest.ResponseRecorder {
			return f.do("PATCH", fmt.Sprintf("/api/folders/%d", ui.ID), f.auth(), `{"kind":"shows"}`)
		}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFolderEnv(t, "")
			ui := f.add(movies(f), "movies")
			entered, release := make(chan struct{}), make(chan struct{})
			var once, inOnce sync.Once
			letGo := func() { once.Do(func() { close(release) }) }
			t.Cleanup(letGo)
			f.srv.DropRoot = func(ctx context.Context, path string, forget bool) (int, error) {
				f.record("drop:" + path)
				inOnce.Do(func() { close(entered) })
				<-release // waiting for a scan to finish
				return f.store.ArchiveAndDeleteRoot(ctx, path, forget)
			}

			deleted := make(chan *httptest.ResponseRecorder, 1)
			go func() { deleted <- f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), f.auth(), "") }()
			<-entered
			other := make(chan *httptest.ResponseRecorder, 1)
			go func() { other <- tc.do(f, ui) }()

			select {
			case rec := <-other:
				letGo()
				t.Fatalf("%s finished with %d while the removal was waiting for a scan: it did not wait for the folders lock: %s",
					tc.name, rec.Code, rec.Body)
			case <-time.After(200 * time.Millisecond):
			}
			letGo()
			if rec := <-deleted; rec.Code != 204 {
				t.Fatalf("the removal = %d: %s", rec.Code, rec.Body)
			}
			if rec := <-other; rec.Code != tc.want {
				t.Errorf("%s after the removal = %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body)
			}
			// Whatever it was, the scanner heard the removal first and its own change after.
			calls := f.callLog()
			if len(calls) < 2 || calls[0] != "roots:" || calls[1] != "drop:"+movies(f) {
				t.Errorf("calls = %v, want the removal's roots: and drop: first", calls)
			}
		})
	}
}

// A -media folder is a command-line folder whatever its type: it cannot be
// removed here, or it would come back at the next start.
func TestDeleteFolderOfAMediaFlagFolderIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newFolderEnv(t, "", "tv:")                                         // -media
	row, _, err := f.store.UpsertFlagFolder(ctx, f.path("tv"), "", "shows") // guessed by the scanner
	if err != nil || row.ID == 0 || !row.Guessed {
		t.Fatalf("setup: %+v, %v", row, err)
	}
	ui := f.add(f.path("other"), "movies")
	del := func(id int64) {
		t.Helper()
		wantJSONError(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", id), nil, ""), 409)
	}

	del(row.ID)
	if got, err := f.store.GetFolder(ctx, row.ID); err != nil || got.Path != f.path("tv") {
		t.Errorf("the -media folder is gone: %+v, %v", got, err)
	}
	if calls := f.callLog(); len(calls) != 0 {
		t.Errorf("a refused removal reached the scanner: %v", calls)
	}
	// Controls: it is listed as a flag folder whose type can be changed, and the
	// folder next to it, added in Settings, can be removed.
	var listed bool
	for _, g := range decode[[]map[string]any](t, f.get("/api/folders")) {
		if g["path"] == f.path("tv") {
			listed = g["source"] == "flag" && g["kindLocked"] == false
		}
	}
	if !listed {
		t.Error("the -media folder is not listed as an unlocked flag folder")
	}
	wantStatus(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 204)
}
