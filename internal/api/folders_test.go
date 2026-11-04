package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"ply/internal/library"
	"ply/internal/store"
)

const folderToken = "folder-s3cret"

// folderEnv is an env whose server manages folders: it records what it tells
// the scanner (SetRoots, DropRoot) in calls, in order.
type folderEnv struct {
	*env
	base  string // a temp dir with the folders the tests use
	flags []library.Root

	mu      sync.Mutex
	calls   []string
	dropErr error
}

func rootsString(roots []library.Root) string {
	var parts []string
	for _, r := range roots {
		parts = append(parts, r.Path+"="+r.Kind)
	}
	return strings.Join(parts, ",")
}

// newFolderEnv builds the server; flagDirs names the command-line folders, as
// "movies:movies" (a directory under base, with its type), "media:" for -media.
func newFolderEnv(t *testing.T, token string, flagDirs ...string) *folderEnv {
	t.Helper()
	f := &folderEnv{env: newEnv(t, token), base: dotFreeTempDir(t)}
	for _, name := range []string{"movies", "tv", "other", "movies/extras", "tv/Show"} {
		if err := os.MkdirAll(filepath.Join(f.base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range flagDirs {
		dir, kind, _ := strings.Cut(spec, ":")
		f.flags = append(f.flags, library.Root{Path: filepath.Join(f.base, dir), Kind: kind})
	}
	f.srv = New(Options{Store: f.store, Scanner: f.scanner, UI: testUI, ID: "abc123", Name: "Test Server",
		Version: "9.9.9", Token: token, FlagFolders: f.flags,
		SetRoots: func(roots []library.Root) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, "roots:"+rootsString(roots))
		},
		DropRoot: func(ctx context.Context, path string, forget bool) (int, error) {
			f.mu.Lock()
			f.calls = append(f.calls, fmt.Sprintf("drop:%s:%v", path, forget))
			err := f.dropErr
			f.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return f.store.ArchiveAndDeleteRoot(ctx, path, forget)
		}})
	return f
}

func (f *folderEnv) path(name string) string { return filepath.Join(f.base, name) }

func (f *folderEnv) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// post adds a folder; it returns the response.
func (f *folderEnv) post(path, kind string) *httptest.ResponseRecorder {
	f.t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path, "kind": kind})
	return f.do("POST", "/api/folders", f.auth(), string(body))
}

func (f *folderEnv) auth() map[string]string {
	if f.srv.Token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + f.srv.Token}
}

func (f *folderEnv) add(path, kind string) store.Folder {
	f.t.Helper()
	fo, err := f.store.AddFolder(context.Background(), path, kind, false)
	if err != nil {
		f.t.Fatal(err)
	}
	return fo
}

func (f *folderEnv) storeFolder(path string) (store.Folder, error) {
	for _, fo := range f.folders() {
		if fo.Path == path {
			return fo, nil
		}
	}
	return store.Folder{}, store.ErrNotFound
}

func (f *folderEnv) folders() []store.Folder {
	f.t.Helper()
	fs, err := f.store.Folders(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return fs
}

func TestFolderKindsMatchTheStore(t *testing.T) {
	if library.KindMovies != store.FolderMovies || library.KindShows != store.FolderShows {
		t.Errorf("library kinds %q/%q and store kinds %q/%q differ", library.KindMovies, library.KindShows,
			store.FolderMovies, store.FolderShows)
	}
}

func TestListFolders(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies", "tv:", "gone:shows")
	// Empty: an array, not null.
	g := newFolderEnv(t, "")
	rec := g.get("/api/folders")
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no folders = %s, want []", rec.Body)
	}

	// movies (-movies) and gone (-shows, no directory) are saved at start; tv
	// (-media) is not stored until the scanner guesses; one folder is the user's.
	ctx := context.Background()
	for _, fl := range f.flags {
		if fl.Kind != "" {
			if _, _, err := f.store.UpsertFlagFolder(ctx, fl.Path, fl.Kind, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	other := f.add(f.path("other"), "shows")
	if _, err := f.store.SetProgress(ctx, f.mp4, 100, 1000); err != nil { // env items are under f.dir
		t.Fatal(err)
	}
	for i, p := range []string{"a.mp4", "b.mp4"} {
		it := &store.Item{Kind: store.KindMovie, Title: p, Path: filepath.Join(f.path("movies"), p), Root: f.path("movies"), Size: int64(i + 1)}
		if _, err := f.store.UpsertItem(ctx, it, nil); err != nil {
			t.Fatal(err)
		}
	}

	rec = f.get("/api/folders")
	wantStatus(t, rec, 200)
	got := decode[[]map[string]any](t, rec)
	if len(got) != 4 {
		t.Fatalf("folders = %v, want 4", got)
	}
	byPath := map[string]map[string]any{}
	for _, g := range got {
		byPath[g["path"].(string)] = g
		for _, k := range []string{"id", "path", "kind", "guessed", "source", "kindLocked", "items", "lastScanAt", "state", "error"} {
			if _, ok := g[k]; !ok {
				t.Errorf("%v has no %q", g, k)
			}
		}
	}
	check := func(path string, want map[string]any) {
		t.Helper()
		g := byPath[path]
		for k, v := range want {
			if g[k] != v {
				t.Errorf("%s: %s = %v (%T), want %v", filepath.Base(path), k, g[k], g[k], v)
			}
		}
	}
	check(f.path("movies"), map[string]any{"kind": "movies", "source": "flag", "kindLocked": true, "items": 2.0, "state": "ok", "error": "", "guessed": false})
	check(f.path("gone"), map[string]any{"kind": "shows", "source": "flag", "kindLocked": true, "items": 0.0, "state": "missing"})
	if byPath[f.path("gone")]["error"] == "" {
		t.Error("a missing folder says nothing about why")
	}
	check(f.path("other"), map[string]any{"id": float64(other.ID), "kind": "shows", "source": "ui", "kindLocked": false, "state": "ok", "lastScanAt": 0.0})
	// A -media folder with no row yet: listed, with no id and no type.
	check(f.path("tv"), map[string]any{"id": 0.0, "kind": "", "source": "flag", "kindLocked": false, "state": "ok", "guessed": false})

	// Once guessed it has a row, still a flag folder, still editable.
	if _, _, err := f.store.UpsertFlagFolder(ctx, f.path("tv"), "", "shows"); err != nil {
		t.Fatal(err)
	}
	got = decode[[]map[string]any](t, f.get("/api/folders"))
	for _, g := range got {
		if g["path"] == f.path("tv") && (g["kind"] != "shows" || g["guessed"] != true || g["source"] != "flag" || g["kindLocked"] != false || g["id"] == 0.0) {
			t.Errorf("guessed -media folder = %v", g)
		}
	}
	if len(got) != 4 {
		t.Errorf("the guessed folder is listed twice: %v", got)
	}

	// A folder that became a file is an error, not missing.
	file := f.path("file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.add(file, "movies")
	for _, g := range decode[[]map[string]any](t, f.get("/api/folders")) {
		if g["path"] == file && (g["state"] != "error" || g["error"] == "") {
			t.Errorf("a file as a folder = %v", g)
		}
	}
}

func TestListFoldersUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	f := newFolderEnv(t, "")
	dir := f.path("locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.add(dir, "movies")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	got := decode[[]map[string]any](t, f.get("/api/folders"))
	if len(got) != 1 || got[0]["state"] != "error" || !strings.Contains(got[0]["error"].(string), "permission denied") {
		t.Errorf("unreadable folder = %v", got)
	}
	// Positive control: the same folder, readable again.
	os.Chmod(dir, 0o755)
	if got := decode[[]map[string]any](t, f.get("/api/folders")); got[0]["state"] != "ok" {
		t.Errorf("readable folder = %v", got)
	}
}

func TestAddFolder(t *testing.T) {
	f := newFolderEnv(t, "", "tv:shows")
	rec := f.post(f.path("movies"), "movies")
	wantStatus(t, rec, 201)
	got := decode[map[string]any](t, rec)
	if got["path"] != f.path("movies") || got["kind"] != "movies" || got["source"] != "ui" || got["kindLocked"] != false ||
		got["state"] != "ok" || got["guessed"] != false || got["id"] == 0.0 {
		t.Errorf("created = %v", got)
	}
	saved := f.folders()
	if len(saved) != 1 || saved[0].Path != f.path("movies") || saved[0].Kind != "movies" || saved[0].Guessed {
		t.Errorf("stored = %+v", saved)
	}
	// The scanner and the watcher are told, with the command-line folders first.
	want := "roots:" + f.path("tv") + "=shows," + f.path("movies") + "=movies"
	if calls := f.callLog(); !slices.Equal(calls, []string{want}) {
		t.Errorf("calls = %v, want [%s]", calls, want)
	}
	// It shows in the list.
	list := decode[[]map[string]any](t, f.get("/api/folders"))
	found := false
	for _, g := range list {
		found = found || g["path"] == f.path("movies")
	}
	if !found {
		t.Errorf("not listed: %v", list)
	}

	// An unclean path is stored clean.
	rec = f.post(f.path("other")+"/../other/", "shows")
	wantStatus(t, rec, 201)
	if got := decode[map[string]any](t, rec); got["path"] != f.path("other") {
		t.Errorf("path = %v, want it cleaned to %s", got["path"], f.path("other"))
	}

	// With nobody listening (no SetRoots) it still works.
	g := newFolderEnv(t, "")
	g.srv.SetRoots = nil
	wantStatus(t, g.post(g.path("movies"), "movies"), 201)
}

func TestAddFolderRefusals(t *testing.T) {
	f := newFolderEnv(t, "", "tv:shows")
	f.add(f.path("movies"), "movies")
	file := f.path("a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := f.path("locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	tests := []struct {
		name, body string
		want       int
		says       string // part of the error
	}{
		{"not JSON", `nope`, 400, "JSON"},
		{"empty body", ``, 400, "JSON"},
		{"no kind", `{"path":"` + f.path("other") + `"}`, 400, "kind"},
		{"mixed", `{"path":"` + f.path("other") + `","kind":"mixed"}`, 400, "kind"},
		{"kind in the wrong case", `{"path":"` + f.path("other") + `","kind":"Movies"}`, 400, "kind"},
		{"no path", `{"kind":"movies"}`, 400, "absolute"},
		{"relative path", `{"path":"media/movies","kind":"movies"}`, 400, "absolute"},
		{"dot path", `{"path":".","kind":"movies"}`, 400, "absolute"},
		{"home shorthand", `{"path":"~/movies","kind":"movies"}`, 400, "absolute"},
		{"missing", `{"path":"` + f.path("nope") + `","kind":"movies"}`, 400, "no such file"},
		{"a file", `{"path":"` + file + `","kind":"movies"}`, 400, "not a directory"},
		{"inside a folder", `{"path":"` + f.path("movies/extras") + `","kind":"movies"}`, 400, f.path("movies")},
		{"inside a command-line folder", `{"path":"` + f.path("tv/Show") + `","kind":"shows"}`, 400, f.path("tv")},
		{"holds a folder", `{"path":"` + f.base + `","kind":"movies"}`, 400, "overlaps"},
		{"already there", `{"path":"` + f.path("movies") + `","kind":"movies"}`, 409, ""},
		{"already there, other kind", `{"path":"` + f.path("movies") + `","kind":"shows"}`, 409, ""},
		{"already there, unclean", `{"path":"` + f.path("movies") + `/./","kind":"movies"}`, 409, ""},
		{"already a command-line folder", `{"path":"` + f.path("tv") + `","kind":"shows"}`, 409, ""},
	}
	if os.Geteuid() != 0 {
		tests = append(tests, struct {
			name, body string
			want       int
			says       string
		}{"unreadable", `{"path":"` + locked + `","kind":"movies"}`, 400, "permission denied"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do("POST", "/api/folders", nil, tt.body)
			wantJSONError(t, rec, tt.want)
			if !strings.Contains(rec.Body.String(), tt.says) {
				t.Errorf("error %s should mention %q", rec.Body, tt.says)
			}
		})
	}
	if saved := f.folders(); len(saved) != 1 {
		t.Errorf("a refusal stored a folder: %+v", saved)
	}
	if calls := f.callLog(); len(calls) != 0 {
		t.Errorf("a refusal told the scanner: %v", calls)
	}
	// Control: a good folder next to the refused ones is accepted.
	wantStatus(t, f.post(f.path("other"), "shows"), 201)
	if calls := f.callLog(); len(calls) != 1 {
		t.Errorf("calls after the accepted one = %v", calls)
	}
	// Overlap only means containing: a sibling whose name starts the same is fine.
	if err := os.Mkdir(f.path("movies2"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.post(f.path("movies2"), "movies"), 201)
}

// Two requests at once for folders that overlap: one wins.
func TestAddFolderOverlapIsCheckedUnderALock(t *testing.T) {
	for round := 0; round < 20; round++ {
		f := newFolderEnv(t, "")
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, p := range []string{f.path("movies"), f.path("movies/extras")} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = f.post(p, "movies").Code
			}()
		}
		wg.Wait()
		created := 0
		for _, c := range codes {
			if c == 201 {
				created++
			}
		}
		if created != 1 || len(f.folders()) != 1 {
			t.Fatalf("round %d: codes %v, %d folders; want exactly one created", round, codes, len(f.folders()))
		}
	}
}

func TestPatchFolder(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies", "tv:")
	ui := f.add(f.path("other"), "movies")
	ctx := context.Background()
	if _, _, err := f.store.UpsertFlagFolder(ctx, f.path("movies"), "movies", ""); err != nil {
		t.Fatal(err)
	}
	guess, _, err := f.store.UpsertFlagFolder(ctx, f.path("tv"), "", "movies")
	if err != nil || !guess.Guessed {
		t.Fatalf("setup: %+v, %v", guess, err)
	}
	patch := func(id int64, body string) *httptest.ResponseRecorder {
		return f.do("PATCH", fmt.Sprintf("/api/folders/%d", id), nil, body)
	}
	// An item under the folder: PATCH does not re-parse it, the scanner does.
	it := &store.Item{Kind: store.KindMovie, Title: "Pilot", Path: f.path("other/x.mkv"), Root: f.path("other"), Size: 1}
	itemID, _ := f.store.UpsertItem(ctx, it, nil)

	rec := patch(ui.ID, `{"kind":"shows"}`)
	wantStatus(t, rec, 200)
	if got := decode[map[string]any](t, rec); got["kind"] != "shows" || got["id"] != float64(ui.ID) || got["source"] != "ui" || got["items"] != 1.0 {
		t.Errorf("patched = %v", got)
	}
	if got, _ := f.store.GetFolder(ctx, ui.ID); got.Kind != "shows" {
		t.Errorf("stored = %+v", got)
	}
	want := "roots:" + f.path("movies") + "=movies," + f.path("tv") + "=movies," + f.path("other") + "=shows"
	if calls := f.callLog(); !slices.Equal(calls, []string{want}) {
		t.Errorf("calls = %v, want [%s]", calls, want)
	}
	if e, _ := f.store.GetEntry(ctx, itemID); e.Kind != store.KindMovie {
		t.Errorf("PATCH re-parsed the item itself: %+v", e.Item)
	}

	// A -media folder is editable, and choosing ends the guess.
	rec = patch(guess.ID, `{"kind":"shows"}`)
	wantStatus(t, rec, 200)
	if got := decode[map[string]any](t, rec); got["kind"] != "shows" || got["guessed"] != false || got["source"] != "flag" || got["kindLocked"] != false {
		t.Errorf("patched -media folder = %v", got)
	}

	// Locked, unknown, malformed.
	flagMovies, err := f.storeFolder(f.path("movies"))
	if err != nil {
		t.Fatal(err)
	}
	wantJSONError(t, patch(flagMovies.ID, `{"kind":"shows"}`), 409)
	if got, _ := f.store.GetFolder(ctx, flagMovies.ID); got.Kind != "movies" {
		t.Errorf("a locked folder changed: %+v", got)
	}
	wantJSONError(t, patch(9999, `{"kind":"shows"}`), 404)
	wantJSONError(t, f.do("PATCH", "/api/folders/abc", nil, `{"kind":"shows"}`), 404)
	wantJSONError(t, patch(ui.ID, `{"kind":"mixed"}`), 400)
	wantJSONError(t, patch(ui.ID, `{}`), 400)
	wantJSONError(t, patch(ui.ID, `nope`), 400)
	if got, _ := f.store.GetFolder(ctx, ui.ID); got.Kind != "shows" {
		t.Errorf("a refused kind changed the folder: %+v", got)
	}
	if calls := f.callLog(); len(calls) != 2 { // the two accepted PATCHes
		t.Errorf("calls = %v, want 2", calls)
	}
}

func TestDeleteFolder(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies")
	ui := f.add(f.path("other"), "movies")
	ctx := context.Background()
	// Two files with progress under the folder, one elsewhere.
	for _, name := range []string{"a.mp4", "b.mp4"} {
		it := &store.Item{Kind: store.KindMovie, Title: name, Path: f.path("other/" + name), Root: f.path("other"), Size: 5}
		id, err := f.store.UpsertItem(ctx, it, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SetProgress(ctx, id, 100, 1000); err != nil {
			t.Fatal(err)
		}
	}
	del := func(target string) *httptest.ResponseRecorder { return f.do("DELETE", target, nil, "") }

	rec := del(fmt.Sprintf("/api/folders/%d", ui.ID))
	wantStatus(t, rec, 204)
	if rec.Body.Len() != 0 {
		t.Errorf("204 with a body: %q", rec.Body)
	}
	if left := f.folders(); len(left) != 0 {
		t.Errorf("folders left: %+v", left)
	}
	// The scanner is told first, with the roots that are left; then the items are
	// dropped through DropRoot, which archives. In that order, so that no scan can
	// bring the files back in between.
	want := []string{"roots:" + f.path("movies") + "=movies", "drop:" + f.path("other") + ":false"}
	if calls := f.callLog(); !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if got, _ := f.store.CountItemsByRoot(ctx); got[f.path("other")] != 0 {
		t.Errorf("items left under the folder: %v", got)
	}
	// The path can be added again, and its progress comes back.
	wantStatus(t, f.post(f.path("other"), "movies"), 201)
	it := &store.Item{Kind: store.KindMovie, Title: "a", Path: f.path("other/a.mp4"), Root: f.path("other"), Size: 5}
	id, _ := f.store.UpsertItem(ctx, it, nil)
	if _, _, ok, err := f.store.RestoreArchived(ctx, id, it.Path, 5); err != nil || !ok {
		t.Errorf("the removed folder's progress was not archived: %v, %v", ok, err)
	}

	// Unknown, malformed, and the command-line folder.
	wantJSONError(t, del("/api/folders/9999"), 404)
	wantJSONError(t, del("/api/folders/abc"), 404)
	fl, _, _ := f.store.UpsertFlagFolder(ctx, f.path("movies"), "movies", "")
	before := len(f.callLog())
	wantJSONError(t, del(fmt.Sprintf("/api/folders/%d", fl.ID)), 409)
	if _, err := f.store.GetFolder(ctx, fl.ID); err != nil {
		t.Errorf("the command-line folder is gone: %v", err)
	}
	if after := len(f.callLog()); after != before {
		t.Errorf("a refused delete told the scanner: %v", f.callLog()[before:])
	}
}

func TestDeleteFolderForget(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		query  string
		forget bool
	}{{"?forget=1", true}, {"?forget=true", true}, {"?forget=0", false}, {"?forget=false", false}, {"", false}} {
		f := newFolderEnv(t, "")
		ui := f.add(f.path("other"), "movies")
		it := &store.Item{Kind: store.KindMovie, Title: "a", Path: f.path("other/a.mp4"), Root: f.path("other"), Size: 5}
		id, _ := f.store.UpsertItem(ctx, it, nil)
		f.store.SetProgress(ctx, id, 100, 1000)
		wantStatus(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d%s", ui.ID, tc.query), nil, ""), 204)
		if calls := f.callLog(); len(calls) != 2 || calls[1] != fmt.Sprintf("drop:%s:%v", f.path("other"), tc.forget) {
			t.Errorf("%q: calls = %v", tc.query, calls)
		}
		_, _, restored, _ := f.store.RestoreArchived(ctx, id, it.Path, 5)
		if restored == tc.forget {
			t.Errorf("%q: archived = %v, want %v", tc.query, restored, !tc.forget)
		}
	}
	f := newFolderEnv(t, "")
	ui := f.add(f.path("other"), "movies")
	wantJSONError(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d?forget=maybe", ui.ID), nil, ""), 400)
	if len(f.folders()) != 1 || len(f.callLog()) != 0 {
		t.Error("a bad forget value changed something")
	}
}

// With no DropRoot (the scanner does not offer one) the store does the work.
func TestDeleteFolderWithoutDropRoot(t *testing.T) {
	ctx := context.Background()
	f := newFolderEnv(t, "")
	f.srv.DropRoot = nil
	ui := f.add(f.path("other"), "movies")
	it := &store.Item{Kind: store.KindMovie, Title: "a", Path: f.path("other/a.mp4"), Root: f.path("other"), Size: 5}
	id, _ := f.store.UpsertItem(ctx, it, nil)
	f.store.SetProgress(ctx, id, 100, 1000)
	if n, _ := f.store.CountItems(ctx); n != 4 {
		t.Fatalf("control: %d items", n)
	}
	wantStatus(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 204)
	if n, _ := f.store.CountItems(ctx); n != 3 {
		t.Errorf("%d items left, want the env's 3", n)
	}
	if _, _, ok, _ := f.store.RestoreArchived(ctx, id, it.Path, 5); !ok {
		t.Error("nothing archived")
	}
}

func TestDeleteFolderFailsWhenTheItemsCannotBeDropped(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies")
	ui := f.add(f.path("other"), "movies")
	f.dropErr = errors.New("disk on fire")
	rec := f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, "")
	wantJSONError(t, rec, 500)
	if strings.Contains(rec.Body.String(), "fire") {
		t.Errorf("internal error leaked: %s", rec.Body)
	}
	if len(f.folders()) != 1 {
		t.Error("the folder was removed although its items were not")
	}
	// The scanner was told to stop covering the folder, then to cover it again:
	// it is still in the library.
	want := []string{
		"roots:" + f.path("movies") + "=movies",
		"drop:" + f.path("other") + ":false",
		"roots:" + f.path("movies") + "=movies," + f.path("other") + "=movies",
	}
	if calls := f.callLog(); !slices.Equal(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	// Control: with the failure gone it works.
	f.dropErr = nil
	wantStatus(t, f.do("DELETE", fmt.Sprintf("/api/folders/%d", ui.ID), nil, ""), 204)
}

func TestFS(t *testing.T) {
	f := newFolderEnv(t, "")
	root := dotFreeTempDir(t)
	for _, d := range []string{"b", "A", ".hidden", "c d", "é"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)
	os.MkdirAll(filepath.Join(root, "b", "inner"), 0o755)
	os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "linkdir"))
	os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "linkfile"))
	os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "broken"))

	rec := f.get("/api/fs?path=" + root)
	wantStatus(t, rec, 200)
	got := decode[fsListing](t, rec)
	if got.Path != root || got.Parent != filepath.Dir(root) {
		t.Errorf("path %q parent %q", got.Path, got.Parent)
	}
	var names []string
	for _, d := range got.Dirs {
		names = append(names, d.Name)
		if d.Path != filepath.Join(root, d.Name) {
			t.Errorf("%s has path %s", d.Name, d.Path)
		}
	}
	// Directories only (a symlinked one included), no dot-folders, no files,
	// sorted without regard to case.
	if want := []string{"A", "b", "c d", "linkdir", "é"}; !slices.Equal(names, want) {
		t.Errorf("dirs = %v, want %v", names, want)
	}
	if _, ok := decode[map[string]any](t, rec)["places"]; ok {
		t.Error("a listing of a directory carries places")
	}

	// Drilling down; an unclean path is cleaned; no subdirectories is [] not null.
	rec = f.get("/api/fs?path=" + root + "/b/../b/inner/")
	wantStatus(t, rec, 200)
	if got := decode[fsListing](t, rec); got.Path != filepath.Join(root, "b", "inner") || got.Parent != filepath.Join(root, "b") || len(got.Dirs) != 0 {
		t.Errorf("inner = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"dirs":[]`) {
		t.Errorf("empty dirs = %s", rec.Body)
	}

	// The top of the tree has no parent.
	rec = f.get("/api/fs?path=/")
	wantStatus(t, rec, 200)
	if got := decode[fsListing](t, rec); got.Path != "/" || got.Parent != "" {
		t.Errorf("/ = %+v", got)
	}

	// Errors.
	wantJSONError(t, f.get("/api/fs?path=relative/dir"), 400)
	wantJSONError(t, f.get("/api/fs?path="+filepath.Join(root, "nope")), 404)
	wantJSONError(t, f.get("/api/fs?path="+filepath.Join(root, "file.txt")+"/sub"), 404)
	wantJSONError(t, f.get("/api/fs?path="+filepath.Join(root, "file.txt")), 400)
	wantJSONError(t, f.get("/api/fs?path="+filepath.Join(root, "broken")), 404)
	wantJSONError(t, f.do("POST", "/api/fs", nil, ""), 404) // no such route: not a way in either
}

func TestFSUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	f := newFolderEnv(t, "")
	dir := filepath.Join(dotFreeTempDir(t), "locked")
	os.Mkdir(dir, 0o755)
	os.Mkdir(filepath.Join(dir, "inside"), 0o755)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	rec := f.get("/api/fs?path=" + dir)
	wantJSONError(t, rec, 403)
	if !strings.Contains(rec.Body.String(), "permission denied") {
		t.Errorf("403 should carry the OS error: %s", rec.Body)
	}
	// Under it: stat fails with the same error.
	wantJSONError(t, f.get("/api/fs?path="+filepath.Join(dir, "inside")), 403)
	// Control: readable again, the same request lists it.
	os.Chmod(dir, 0o755)
	if got := decode[fsListing](t, f.get("/api/fs?path="+dir)); len(got.Dirs) != 1 || got.Dirs[0].Name != "inside" {
		t.Errorf("after chmod: %+v", got)
	}
}

func TestFSPlaces(t *testing.T) {
	f := newFolderEnv(t, "", "movies:movies")
	home := t.TempDir()
	t.Setenv("HOME", home)
	f.add(f.path("other"), "shows")

	rec := f.get("/api/fs")
	wantStatus(t, rec, 200)
	got := decode[fsListing](t, rec)
	if got.Path != "" || got.Parent != "" || got.Dirs == nil || len(got.Dirs) != 0 {
		t.Errorf("no path: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"dirs":[]`) || !strings.Contains(rec.Body.String(), `"places":[`) {
		t.Errorf("body = %s", rec.Body)
	}
	byPath := map[string]string{}
	for _, p := range got.Places {
		byPath[p.Path] = p.Name
		if fi, err := os.Stat(p.Path); err != nil || !fi.IsDir() {
			// a mount point on this machine may come and go; the folders and home must not
			if p.Path == home || p.Path == f.path("movies") {
				t.Errorf("place %s is not a directory: %v", p.Path, err)
			}
		}
	}
	if byPath[home] != "Home" {
		t.Errorf("home %s is not among the places: %v", home, got.Places)
	}
	// The current folders: the command-line one and the saved one.
	if byPath[f.path("movies")] != "movies" || byPath[f.path("other")] != "other" {
		t.Errorf("library folders are not among the places: %v", got.Places)
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/Volumes"); err == nil && !hasPrefix(got.Places, "/Volumes/") {
			t.Errorf("no /Volumes entry among %v", got.Places)
		}
	}
}

func hasPrefix(places []fsEntry, prefix string) bool {
	for _, p := range places {
		if strings.HasPrefix(p.Path, prefix) {
			return true
		}
	}
	return false
}

func TestMountParents(t *testing.T) {
	for goos, want := range map[string][]string{
		"darwin": {"/Volumes"}, "linux": {"/media", "/mnt"}, "windows": nil, "plan9": nil,
	} {
		if got := mountParents(goos); !slices.Equal(got, want) {
			t.Errorf("mountParents(%s) = %v, want %v", goos, got, want)
		}
	}
}

func TestListPlaces(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	media := filepath.Join(tmp, "media")
	mnt := filepath.Join(tmp, "mnt")
	for _, d := range []string{home, filepath.Join(media, "usb"), filepath.Join(media, "Disk"), filepath.Join(media, ".trash"), filepath.Join(mnt, "nas")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(media, "readme"), nil, 0o644)
	lib := filepath.Join(tmp, "lib")
	got := listPlaces(home, []string{media, filepath.Join(tmp, "absent"), mnt}, []string{lib, filepath.Join(mnt, "nas")})
	want := []fsEntry{
		{"Home", home},
		{"Disk", filepath.Join(media, "Disk")}, {"usb", filepath.Join(media, "usb")},
		{"nas", filepath.Join(mnt, "nas")}, // the library folder of the same path is not listed twice
		{"lib", lib},
	}
	if !slices.Equal(got, want) {
		t.Errorf("places = %v\nwant     %v", got, want)
	}
	if got := listPlaces("", nil, nil); got == nil || len(got) != 0 {
		t.Errorf("no places = %#v, want an empty list", got)
	}
}
