package trailers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/library"
	"ply/internal/store"
	"ply/internal/tier"
	"ply/internal/trailerdl"
)

func TestDestination(t *testing.T) {
	m := library.Root{Path: "/m", Kind: library.KindMovies}
	tv := library.Root{Path: "/tv", Kind: library.KindShows}
	film := func(id int64, path, title string, year int) store.Item {
		return store.Item{ID: id, Kind: store.KindMovie, Path: path, Title: title, Year: year}
	}
	ep := func(id int64, path, show string) store.Item { return store.Item{ID: id, Kind: store.KindEpisode, Path: path, Show: show} }
	for _, c := range []struct {
		name  string
		roots []library.Root
		files []store.Item
		want  string
	}{
		{"own folder, two versions", []library.Root{m}, []store.Item{
			film(1, "/m/Vivre sa vie (1962)/Vivre sa vie (1962) - 1080p.mkv", "Vivre sa vie", 1962),
			film(2, "/m/Vivre sa vie (1962)/Vivre sa vie (1962) - 4K.mkv", "Vivre sa vie", 1962)},
			"/m/Vivre sa vie (1962)/Vivre sa vie (1962)-trailer.mp4"},
		{"loose in the root", []library.Root{m}, []store.Item{film(1, "/m/Heat.1995.1080p.BluRay.mkv", "Heat", 1995)},
			"/m/Heat (1995)-trailer.mp4"},
		{"no year", []library.Root{m}, []store.Item{film(1, "/m/Heat.mkv", "Heat", 0)}, "/m/Heat-trailer.mp4"},
		{"names a file cannot hold", []library.Root{m}, []store.Item{film(1, "/m/ACDC/x.mkv", `AC/DC: Live?`, 1991)},
			"/m/ACDC/AC DC Live (1991)-trailer.mp4"},
		{"a show", []library.Root{tv}, []store.Item{ep(1, "/tv/The Wire/Season 1/x.mkv", "The Wire")}, "/tv/The Wire/The Wire-trailer.mp4"},
		{"a show that is the root", []library.Root{{Path: "/tv/Lost", Kind: library.KindShows}},
			[]store.Item{ep(1, "/tv/Lost/Season 1/x.mkv", "Lost")}, "/tv/Lost/Lost-trailer.mp4"},
		{"versions in two folders: the first file's", []library.Root{m, {Path: "/other", Kind: library.KindMovies}}, []store.Item{
			film(4, "/m/Heat (1995)/Heat (1995).mkv", "Heat", 1995), film(9, "/other/Heat (1995).mkv", "Heat", 1995)},
			"/m/Heat (1995)/Heat (1995)-trailer.mp4"},
	} {
		dir, name, err := Destination(c.files, c.roots)
		if got := filepath.Join(dir, name); err != nil || got != c.want {
			t.Errorf("%s: %s, %v; want %s", c.name, got, err, c.want)
		}
	}
	if _, _, err := Destination(nil, []library.Root{m}); err == nil {
		t.Error("a title with no files has a destination")
	}
	// Episodes straight in a shows root have no show folder: a trailer there would be the root's.
	for _, path := range []string{"/tv/The.Wire.S01E01.mkv", "/tv/Season 1/The Wire S01E01.mkv"} {
		loose := store.Item{ID: 1, Kind: store.KindEpisode, Path: path, Show: "The Wire"}
		if dir, name, err := Destination([]store.Item{loose}, []library.Root{tv}); err == nil {
			t.Errorf("loose episode %s: destination %s", path, filepath.Join(dir, name))
		}
	}
}

// gate records what each step waited for.
type gate struct {
	mu    sync.Mutex
	needs []tier.Need
}

func (g *gate) Check(tier.Need) tier.Hold { return tier.Hold{} }
func (g *gate) Wait(ctx context.Context, n tier.Need, _ func(string)) error {
	g.mu.Lock()
	g.needs = append(g.needs, n)
	g.mu.Unlock()
	return ctx.Err()
}
func (g *gate) Changed() <-chan struct{}     { return nil }
func (g *gate) Poll() time.Duration          { return time.Hour }
func (g *gate) BrowseMaxHold() time.Duration { return time.Hour }
func (g *gate) State() tier.State            { return tier.State{} }

type env struct {
	t       *testing.T
	st      *store.Store
	q       *jobs.Queue
	g       *gate
	s       *Service
	title   int64
	dir     string // the film's folder
	online  bool
	videos  []store.Video
	mu      sync.Mutex
	tried   []string // the keys fetched
	fail    map[string]error
	block   chan struct{} // a fetch waits for it when set
	indexed []string
	orphan  bool // IndexFile reads the file as nobody's trailer
}

// IndexFile stores the trailer as the scanner would.
func (e *env) IndexFile(ctx context.Context, path string) (int64, error) {
	e.mu.Lock()
	e.indexed = append(e.indexed, path)
	e.mu.Unlock()
	owner := filepath.Dir(path)
	if e.orphan {
		owner = ""
	}
	return e.st.UpsertItem(ctx, &store.Item{Kind: store.KindTrailer, OwnerDir: owner, Title: "Vivre sa vie", Year: 1962,
		Path: path, Root: filepath.Dir(e.dir), Size: 1, MTime: 1}, nil)
}

func (e *env) fetch(ctx context.Context, v store.Video, dir string, s trailerdl.Steps) (string, error) {
	e.mu.Lock()
	e.tried = append(e.tried, v.Key)
	err, block := e.fail[v.Key], e.block
	e.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	s.Progress(10, 10, trailerdl.NoteDownloading)
	if err := s.Converting(ctx); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "trailer.mp4")
	return out, os.WriteFile(out, []byte("mp4 of "+v.Key), 0o644)
}

func yt(key string) store.Video { return store.Video{Site: store.SiteYouTube, Key: key} }

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(t.TempDir(), "Movies")
	e := &env{t: t, st: st, g: &gate{}, online: true, videos: []store.Video{yt("aaaaaaaaaaa")}, fail: map[string]error{},
		dir: filepath.Join(root, "Vivre sa vie (1962)")}
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := st.UpsertItem(ctx, &store.Item{Kind: store.KindMovie, Title: "Vivre sa vie", Year: 1962, Root: root,
		Path: filepath.Join(e.dir, "Vivre sa vie (1962) - 1080p.mkv"), Size: 1, MTime: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.title, err = st.UpsertTitle(ctx, &store.TitleInput{Title: store.Title{Kind: store.KindMovie, TMDBID: 25600, Title: "Vivre sa vie", Year: 1962}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemMatch(ctx, id, store.Match{TitleID: e.title, State: store.MatchMatched, Score: 0.9}); err != nil {
		t.Fatal(err)
	}
	e.q = jobs.New(jobs.Options{Persist: st, Gate: e.g})
	e.s = New(Options{Store: st, Queue: e.q, Scanner: e, Fetch: e.fetch, TempDir: t.TempDir(),
		Online: func(context.Context) bool { return e.online },
		Videos: func(context.Context, int64) ([]store.Video, error) { return e.videos, nil },
		Roots:  func() []library.Root { return []library.Root{{Path: root, Kind: library.KindMovies}} }})
	e.q.Register(e.s.Spec())
	qctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.q.Run(qctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return e
}

// wait waits for the job to finish and returns it.
func (e *env) wait(id int64) jobs.Job {
	e.t.Helper()
	for range 500 {
		for _, j := range e.q.Snapshot().History {
			if j.ID == id && j.State.Finished() {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("job %d did not finish", id)
	return jobs.Job{}
}

func (e *env) ensure() *Download {
	e.t.Helper()
	d, err := e.s.Ensure(context.Background(), e.title)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func TestDownloadWritesAndIndexes(t *testing.T) {
	e := newEnv(t)
	d := e.ensure()
	if d == nil || d.State != "queued" || d.JobID == 0 {
		t.Fatalf("Ensure = %+v", d)
	}
	j := e.wait(d.JobID)
	final := filepath.Join(e.dir, "Vivre sa vie (1962)-trailer.mp4")
	data, err := os.ReadFile(final)
	if j.State != jobs.Done || err != nil || string(data) != "mp4 of aaaaaaaaaaa" {
		t.Fatalf("job %s (%s); file %q, %v", j.State, j.Error, data, err)
	}
	if j.Title != "Trailer: Vivre sa vie (1962)" {
		t.Errorf("job title %q", j.Title)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".*")); len(left) != 0 {
		t.Errorf("left in the folder: %v", left)
	}
	if len(e.indexed) != 1 || e.indexed[0] != final {
		t.Errorf("indexed %v", e.indexed)
	}
	if left, _ := os.ReadDir(e.s.o.TempDir); len(left) != 0 {
		t.Errorf("left in the temp dir: %v", left)
	}
	// The steps: the download (no need), the conversion (CPU), the copy (the folder's disk).
	e.g.mu.Lock()
	defer e.g.mu.Unlock()
	var cpu, disk bool
	for _, n := range e.g.needs {
		cpu = cpu || n.CPU
		disk = disk || (len(n.Disks) == 1 && n.Disks[0] == e.dir)
	}
	if !cpu || !disk {
		t.Errorf("needs waited on: %+v", e.g.needs)
	}
}

func TestDownloadNeverOverwrites(t *testing.T) {
	e := newEnv(t)
	final := filepath.Join(e.dir, "Vivre sa vie (1962)-trailer.mp4")
	if err := os.WriteFile(final, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := e.wait(e.ensure().JobID)
	if data, _ := os.ReadFile(final); j.State != jobs.Done || string(data) != "mine" || len(e.tried) != 0 {
		t.Errorf("job %s, file %q, fetched %v", j.State, data, e.tried)
	}
}

func TestDownloadReadOnlyFolderLeavesNothing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	e := newEnv(t)
	if err := os.Chmod(e.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(e.dir, 0o755) })
	j := e.wait(e.ensure().JobID)
	if j.State != jobs.Failed || !strings.Contains(j.Error, "The folder is read-only: "+e.dir) {
		t.Errorf("job %s: %q", j.State, j.Error)
	}
	if left, _ := os.ReadDir(e.dir); len(left) != 0 {
		t.Errorf("in the folder: %v", left)
	}
	if left, _ := os.ReadDir(e.s.o.TempDir); len(left) != 0 {
		t.Errorf("left in the temp dir: %v", left)
	}
}

func TestFetchFallsBackToNextVideoOn403(t *testing.T) {
	e := newEnv(t)
	e.videos = []store.Video{yt("aaaaaaaaaaa"), yt("bbbbbbbbbbb"), yt("ccccccccccc"), yt("ddddddddddd")}
	e.fail["aaaaaaaaaaa"] = errors.New("download: HTTP 403")
	j := e.wait(e.ensure().JobID)
	data, _ := os.ReadFile(filepath.Join(e.dir, "Vivre sa vie (1962)-trailer.mp4"))
	if j.State != jobs.Done || string(data) != "mp4 of bbbbbbbbbbb" || strings.Join(e.tried, ",") != "aaaaaaaaaaa,bbbbbbbbbbb" {
		t.Errorf("job %s, file %q, tried %v", j.State, data, e.tried)
	}
	// At most 3 are tried.
	e2 := newEnv(t)
	e2.videos = e.videos
	for _, v := range e.videos {
		e2.fail[v.Key] = errors.New("download: HTTP 403")
	}
	if j := e2.wait(e2.ensure().JobID); j.State != jobs.Failed || len(e2.tried) != 3 {
		t.Errorf("job %s, tried %v; want 3 tried", j.State, e2.tried)
	}
}

func TestDownloadLookupsOff(t *testing.T) {
	e := newEnv(t)
	e.block = make(chan struct{})
	d := e.ensure()
	// Off while it waits its turn (here: the first one blocks; a second is not queued).
	e.online = false
	close(e.block)
	e.wait(d.JobID)
	if d := e.ensure(); d != nil {
		t.Errorf("Ensure with lookups off = %+v, want nothing", d)
	}
	// A queued job whose turn comes with lookups off fails at once.
	j, err := e.q.Enqueue(context.Background(), Kind, jobs.Opt{Key: Key(e.title), Payload: payload{TitleID: e.title}})
	if err != nil {
		t.Fatal(err)
	}
	if j := e.wait(j.ID); j.State != jobs.Failed || !strings.Contains(j.Error, "Online lookups are off") {
		t.Errorf("job %s: %q", j.State, j.Error)
	}
}

func TestEnsureQueuesOnce(t *testing.T) {
	e := newEnv(t)
	e.block = make(chan struct{})
	defer close(e.block)
	a, b := e.ensure(), e.ensure()
	if a == nil || b == nil || a.JobID != b.JobID {
		t.Fatalf("Ensure twice: %+v, %+v; want one job", a, b)
	}
	for range 100 { // once it runs, still the same job
		if e.q.Started(Kind) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c := e.ensure(); c == nil || c.JobID != a.JobID || c.State != "running" {
		t.Errorf("Ensure while it runs: %+v", c)
	}
}

func TestEnsureSkipsWithLocalTrailer(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.UpsertItem(context.Background(), &store.Item{Kind: store.KindTrailer, OwnerDir: e.dir, Title: "Vivre sa vie", Year: 1962,
		Path: filepath.Join(e.dir, "Vivre sa vie (1962)-trailer.mkv"), Root: filepath.Dir(e.dir), Size: 1, MTime: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if d := e.ensure(); d != nil {
		t.Errorf("Ensure = %+v with a trailer on disk", d)
	}
}

func TestEnsureNothingToDownload(t *testing.T) {
	e := newEnv(t)
	e.videos = nil
	if d := e.ensure(); d != nil {
		t.Errorf("Ensure = %+v with nothing to download", d)
	}
}

func TestEnsureRemembersFailure(t *testing.T) {
	e := newEnv(t)
	now := time.Unix(1_800_000_000, 0)
	e.s.o.Now = func() time.Time { return now }
	e.fail["aaaaaaaaaaa"] = errors.New("download: HTTP 500")
	j := e.wait(e.ensure().JobID)
	d := e.ensure()
	if d == nil || d.State != "failed" || d.JobID != j.ID || !strings.Contains(d.Error, "HTTP 500") {
		t.Fatalf("after a failure: %+v", d)
	}
	now = now.Add(5*time.Minute + time.Second) // RetryAfter(0, false)
	delete(e.fail, "aaaaaaaaaaa")
	if d := e.ensure(); d == nil || d.State != "queued" || d.JobID == j.ID {
		t.Fatalf("once due: %+v", d)
	}
	// Not available: a day.
	e2 := newEnv(t)
	e2.s.o.Now = func() time.Time { return now }
	e2.fail["aaaaaaaaaaa"] = errors.Join(errors.New("YouTube: Sign in"), trailerdl.ErrUnavailable)
	e2.wait(e2.ensure().JobID)
	now = now.Add(23 * time.Hour)
	if d := e2.ensure(); d == nil || d.State != "failed" {
		t.Errorf("23 h after a video that is not available: %+v", d)
	}
	// A retry from Activity that succeeds clears it.
	delete(e2.fail, "aaaaaaaaaaa")
	failed := e2.q.Snapshot().History[0]
	again, err := e2.q.Retry(context.Background(), failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	e2.wait(again.ID)
	if d := e2.ensure(); d != nil {
		t.Errorf("after a retry that worked: %+v (the trailer is there; Ensure is asked only without one)", d)
	}
}

func TestEnsureLookupsOff(t *testing.T) {
	e := newEnv(t)
	e.online = false
	if d := e.ensure(); d != nil || len(e.q.Snapshot().Queued) != 0 {
		t.Errorf("Ensure with lookups off = %+v", d)
	}
}

// A trailer written but read back as another title's (or nobody's) is a failure, remembered: the
// page would otherwise queue it again and again.
func TestDownloadThatDoesNotAttachIsAFailure(t *testing.T) {
	e := newEnv(t)
	e.orphan = true
	j := e.wait(e.ensure().JobID)
	if j.State != jobs.Failed || !strings.Contains(j.Error, "reads as another title's trailer") {
		t.Fatalf("job %s: %q", j.State, j.Error)
	}
	if d := e.ensure(); d == nil || d.State != "failed" {
		t.Errorf("Ensure after it: %+v, want the failure", d)
	}
}

// A .part an earlier run left (ply killed, the share gone mid-copy) does not block the title.
func TestDownloadClearsAStalePart(t *testing.T) {
	e := newEnv(t)
	part := filepath.Join(e.dir, ".Vivre sa vie (1962)-trailer.mp4.part")
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := e.wait(e.ensure().JobID)
	if _, err := os.Stat(filepath.Join(e.dir, "Vivre sa vie (1962)-trailer.mp4")); j.State != jobs.Done || err != nil {
		t.Errorf("job %s (%s), final: %v", j.State, j.Error, err)
	}
	if _, err := os.Stat(part); err == nil {
		t.Error("the stale .part is still there")
	}
}

// Two pages at once (the web and a phone): one job.
func TestEnsureConcurrent(t *testing.T) {
	e := newEnv(t)
	e.block = make(chan struct{})
	defer close(e.block)
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d, err := e.s.Ensure(context.Background(), e.title); err == nil && d != nil {
				ids[i] = d.JobID
			}
		}()
	}
	wg.Wait()
	snap := e.q.Snapshot()
	n := 0
	for _, j := range append(snap.Running, snap.Queued...) {
		if j.Kind == Kind {
			n++
		}
	}
	for _, id := range ids {
		if id == 0 || id != ids[0] {
			t.Fatalf("job ids %v, want one", ids)
		}
	}
	if n != 1 {
		t.Errorf("%d jobs, want 1", n)
	}
}

// A download a person cancelled in Activity is not queued again by the page at once.
func TestCancelIsRemembered(t *testing.T) {
	e := newEnv(t)
	e.block = make(chan struct{})
	defer close(e.block)
	d := e.ensure()
	for range 200 {
		if e.q.Started(Kind) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := e.q.Cancel(d.JobID); err != nil {
		t.Fatal(err)
	}
	if j := e.wait(d.JobID); j.State != jobs.Cancelled {
		t.Fatalf("control: the job is %s", j.State)
	}
	if again := e.ensure(); again == nil || again.State != "failed" || again.JobID != d.JobID {
		t.Errorf("Ensure after a cancel: %+v, want the cancel remembered", again)
	}
}
