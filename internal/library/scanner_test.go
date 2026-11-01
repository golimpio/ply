package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/probe"
	"ply/internal/store"
)

// fakeProber answers from the file extension and counts calls.
type fakeProber struct {
	calls   atomic.Int64
	fail    sync.Map // path -> struct{}
	noVideo sync.Map // path -> struct{}
	block   chan struct{}
	entered chan struct{}
}

func (f *fakeProber) Probe(ctx context.Context, path string) (probe.Result, error) {
	f.calls.Add(1)
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return probe.Result{}, ctx.Err()
		}
	}
	if _, ok := f.fail.Load(path); ok {
		return probe.Result{}, errors.New("moov atom not found")
	}
	streams := []probe.Stream{
		{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Default: true},
	}
	if _, ok := f.noVideo.Load(path); ok {
		streams = streams[1:]
	}
	return probe.Result{
		Container:   strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		DurationSec: 60,
		Bitrate:     1000,
		Streams:     streams,
	}, nil
}

func newScanner(t *testing.T, roots ...string) (*Scanner, *store.Store, *fakeProber) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "data dir", "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fp := &fakeProber{}
	return NewScanner(context.Background(), st, fp, roots, nil), st, fp
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(t *testing.T, st *store.Store) map[string]store.Entry {
	t.Helper()
	entries, err := st.ListItems(context.Background(), store.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]store.Entry{}
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

func mustScan(t *testing.T, s *Scanner) {
	t.Helper()
	if err := s.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

func TestScanIndexesVideoFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My Media")
	write(t, filepath.Join(root, "Movie (2020).mp4"), "a")
	write(t, filepath.Join(root, "Shows", "Cool Show", "Season 1", "S01E02 Pilot.mkv"), "b")
	write(t, filepath.Join(root, "Clip.MKV"), "c")
	// Things that must not be indexed.
	write(t, filepath.Join(root, ".hidden.mp4"), "x")
	write(t, filepath.Join(root, "._Movie (2020).mp4"), "x") // AppleDouble
	write(t, filepath.Join(root, ".Trash", "deleted.mkv"), "x")
	write(t, filepath.Join(root, "notes.txt"), "x")
	write(t, filepath.Join(root, "movie.srt"), "x")

	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	got := paths(t, st)
	want := []string{
		filepath.Join(root, "Movie (2020).mp4"),
		filepath.Join(root, "Shows", "Cool Show", "Season 1", "S01E02 Pilot.mkv"),
		filepath.Join(root, "Clip.MKV"),
	}
	if len(got) != len(want) {
		t.Errorf("indexed %d items, want %d: %v", len(got), len(want), keys(got))
	}
	for _, p := range want {
		if _, ok := got[p]; !ok {
			t.Errorf("%s not indexed", p)
		}
	}

	movie := got[want[0]]
	if movie.Kind != store.KindMovie || movie.Title != "Movie" || movie.Year != 2020 ||
		movie.Container != "mp4" || movie.Root != root || movie.Size != 1 || movie.DurationSec != 60 ||
		len(movie.Streams) != 2 {
		t.Errorf("movie = %+v", movie)
	}
	ep := got[want[1]]
	if ep.Kind != store.KindEpisode || ep.Show != "Cool Show" || ep.Season != 1 || ep.Episode != 2 || ep.Title != "Pilot" {
		t.Errorf("episode = %+v", ep.Item)
	}
}

func keys(m map[string]store.Entry) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	return k
}

func TestScanFollowsFileSymlinksButNotDirectories(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	outside := filepath.Join(dir, "outside")
	write(t, filepath.Join(outside, "Real (2001).mkv"), "real")
	write(t, filepath.Join(outside, "Hidden In Dir (2002).mkv"), "x")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "Real (2001).mkv"), filepath.Join(root, "Linked (2001).mkv")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing.mkv"), filepath.Join(root, "Dangling.mkv")); err != nil {
		t.Fatal(err)
	}

	s, st, _ := newScanner(t, root)
	mustScan(t, s) // a dangling symlink is not an error

	got := paths(t, st)
	linked := filepath.Join(root, "Linked (2001).mkv")
	if e, ok := got[linked]; !ok || e.Size != 4 {
		t.Errorf("file symlink not indexed with the target's size: %+v (present=%v)", e.Item, ok)
	}
	if len(got) != 1 {
		t.Errorf("indexed %v; want only the file symlink (no dir symlinks, no dangling links)", keys(got))
	}
}

func TestScanSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a (2020).mp4")
	write(t, file, "12345")

	s, _, fp := newScanner(t, root)
	mustScan(t, s)
	if n := fp.calls.Load(); n != 1 {
		t.Fatalf("first scan probed %d files, want 1", n)
	}
	mustScan(t, s)
	if n := fp.calls.Load(); n != 1 {
		t.Errorf("unchanged file probed again: %d calls", n)
	}

	write(t, file, "1234567890") // size changed
	mustScan(t, s)
	if n := fp.calls.Load(); n != 2 {
		t.Errorf("changed file not re-probed: %d calls", n)
	}

	// Same size, new mtime also counts as changed.
	if err := os.Chtimes(file, time.Now(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	mustScan(t, s)
	if n := fp.calls.Load(); n != 3 {
		t.Errorf("mtime change not re-probed: %d calls", n)
	}
}

func TestRescanRemovesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep (2020).mp4")
	gone := filepath.Join(root, "gone (2021).mp4")
	write(t, keep, "k")
	write(t, gone, "g")

	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	before := paths(t, st)
	if _, ok := before[gone]; !ok || len(before) != 2 { // positive control
		t.Fatalf("setup: both files should be indexed, got %v", keys(before))
	}

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	mustScan(t, s)
	after := paths(t, st)
	if _, ok := after[gone]; ok {
		t.Error("deleted file still in the library")
	}
	if _, ok := after[keep]; !ok {
		t.Error("remaining file was removed")
	}
}

func TestMissingRootDoesNotWipeLibrary(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "media")
	write(t, filepath.Join(root, "a (2020).mp4"), "a")
	write(t, filepath.Join(root, "b (2021).mp4"), "b")

	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	if n := len(paths(t, st)); n != 2 {
		t.Fatalf("setup: %d items, want 2", n)
	}

	moved := filepath.Join(base, "unmounted")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	err := s.Scan(context.Background())
	if err == nil {
		t.Error("scan of a missing root should report an error")
	}
	if n := len(paths(t, st)); n != 2 {
		t.Errorf("missing root wiped the library: %d items left, want 2", n)
	}
	if s.Status(context.Background()).LastError == "" {
		t.Error("lastError not set")
	}

	// Positive control: with the root back, deletions do apply.
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "a (2020).mp4")); err != nil {
		t.Fatal(err)
	}
	mustScan(t, s)
	if n := len(paths(t, st)); n != 1 {
		t.Errorf("after restoring the root and deleting a file: %d items, want 1", n)
	}
}

func TestMissingRootDoesNotAffectOtherRoots(t *testing.T) {
	base := t.TempDir()
	good := filepath.Join(base, "good")
	missing := filepath.Join(base, "missing")
	write(t, filepath.Join(good, "g1 (2020).mp4"), "1")
	write(t, filepath.Join(good, "g2 (2021).mp4"), "2")
	write(t, filepath.Join(missing, "m (2019).mp4"), "m")

	s, st, _ := newScanner(t, good, missing)
	mustScan(t, s)
	if n := len(paths(t, st)); n != 3 {
		t.Fatalf("setup: %d items, want 3", n)
	}

	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(good, "g1 (2020).mp4")); err != nil {
		t.Fatal(err)
	}
	if err := s.Scan(context.Background()); err == nil {
		t.Error("expected an error for the missing root")
	}
	got := paths(t, st)
	if _, ok := got[filepath.Join(missing, "m (2019).mp4")]; !ok {
		t.Error("item under the missing root was removed")
	}
	if _, ok := got[filepath.Join(good, "g1 (2020).mp4")]; ok {
		t.Error("deleted file under the healthy root was not removed")
	}
	if len(got) != 2 {
		t.Errorf("items = %v, want 2", keys(got))
	}
}

func TestUnreadableDirectoryDoesNotWipeLibrary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	inLocked := filepath.Join(locked, "hidden (2020).mp4")
	elsewhere := filepath.Join(root, "other (2021).mp4")
	write(t, inLocked, "x")
	write(t, elsewhere, "y")

	s, st, _ := newScanner(t, root)
	mustScan(t, s)
	if n := len(paths(t, st)); n != 2 {
		t.Fatalf("setup: %d items, want 2", n)
	}

	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	// Unreadable directory (so its file looks missing) plus a genuinely
	// deleted file in a readable one: nothing may be removed.
	if err := os.Remove(elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := s.Scan(context.Background()); err == nil {
		t.Error("expected the walk error to be reported")
	}
	if n := len(paths(t, st)); n != 2 {
		t.Errorf("walk errors must block removals: %d items, want 2", n)
	}

	// Positive control: once readable again the removal goes through.
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	mustScan(t, s)
	got := paths(t, st)
	if _, ok := got[elsewhere]; ok || len(got) != 1 {
		t.Errorf("after restoring permissions: %v, want only the locked file", keys(got))
	}
}

func TestFilesWithoutVideoAreSkipped(t *testing.T) {
	root := t.TempDir()
	audioOnly := filepath.Join(root, "audio.mp4")
	video := filepath.Join(root, "video.mp4")
	write(t, audioOnly, "a")
	write(t, video, "v")

	s, st, fp := newScanner(t, root)
	fp.noVideo.Store(audioOnly, struct{}{})
	mustScan(t, s)
	got := paths(t, st)
	if _, ok := got[video]; !ok {
		t.Error("video file missing")
	}
	if _, ok := got[audioOnly]; ok {
		t.Error("file without a video stream was indexed")
	}
}

func TestProbeFailureIsReportedAndKeepsExistingItem(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good (2020).mp4")
	bad := filepath.Join(root, "bad (2021).mp4")
	write(t, good, "g")
	write(t, bad, "b")

	s, st, fp := newScanner(t, root)
	mustScan(t, s)
	if n := len(paths(t, st)); n != 2 {
		t.Fatalf("setup: %d items", n)
	}

	write(t, bad, "bb") // changed, but now unprobeable
	fp.fail.Store(bad, struct{}{})
	err := s.Scan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "moov atom not found") {
		t.Errorf("err = %v, want the probe failure reported", err)
	}
	got := paths(t, st)
	if len(got) != 2 {
		t.Errorf("a probe failure must not remove items: %v", keys(got))
	}
	if got[bad].Size != 1 {
		t.Errorf("failed re-probe should keep the old row, size = %d", got[bad].Size)
	}
}

func TestTriggerAllowsOneScanAtATime(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a (2020).mp4"), "a")

	s, _, fp := newScanner(t, root)
	fp.block = make(chan struct{})
	fp.entered = make(chan struct{}, 8)

	if !s.Trigger() {
		t.Fatal("first Trigger should start a scan")
	}
	select {
	case <-fp.entered: // the scan is in progress, holding the lock
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start probing")
	}
	if s.Trigger() {
		t.Error("second Trigger must report already running")
	}
	if err := s.Scan(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("Scan during a scan = %v, want ErrRunning", err)
	}
	st := s.Status(context.Background())
	if !st.Running || st.LastStarted == nil || st.LastFinished != nil {
		t.Errorf("status while running = %+v", st)
	}

	close(fp.block)
	s.Wait()
	st = s.Status(context.Background())
	if st.Running || st.LastFinished == nil || st.LastError != "" || st.ItemCount != 1 {
		t.Errorf("status after scan = %+v", st)
	}
	if !s.Trigger() { // the lock was released
		t.Error("Trigger after completion should start a new scan")
	}
	s.Wait()
}

func TestStatusJSONBeforeFirstScan(t *testing.T) {
	s, _, _ := newScanner(t, t.TempDir())
	b, err := json.Marshal(s.Status(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"running":false,"lastStarted":null,"lastFinished":null,"lastError":"","itemCount":0}`
	if string(b) != want {
		t.Errorf("status JSON = %s\nwant %s", b, want)
	}
}

func TestRunScansAtStartupAndPeriodically(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "first (2020).mp4"), "1")

	s, st, _ := newScanner(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, 20*time.Millisecond)
	}()

	waitFor(t, func() bool { return len(paths(t, st)) == 1 }, "startup scan")
	write(t, filepath.Join(root, "second (2021).mp4"), "2")
	waitFor(t, func() bool { return len(paths(t, st)) == 2 }, "periodic rescan")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunWithZeroIntervalScansOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "only (2020).mp4"), "1")
	s, st, fp := newScanner(t, root)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(context.Background(), 0)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run with interval 0 should return after the startup scan")
	}
	if n := len(paths(t, st)); n != 1 || fp.calls.Load() != 1 {
		t.Errorf("items = %d, probes = %d; want 1 and 1", n, fp.calls.Load())
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestScanCancelledDoesNotRemoveItems(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a (2020).mp4")
	write(t, file, "a")
	s, st, _ := newScanner(t, root)
	mustScan(t, s)

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Scan(ctx); err == nil {
		t.Error("cancelled scan should report an error")
	}
	if n := len(paths(t, st)); n != 1 {
		t.Errorf("cancelled scan removed items: %d left, want 1", n)
	}
	mustScan(t, s) // positive control: an uncancelled scan removes it
	if n := len(paths(t, st)); n != 0 {
		t.Errorf("uncancelled scan left %d items, want 0", n)
	}
}
