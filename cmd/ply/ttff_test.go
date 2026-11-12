//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/api"
	"ply/internal/art"
	"ply/internal/jobs"
	"ply/internal/probe"
	"ply/internal/store"
	"ply/internal/stream"
	"ply/internal/subs"
	"ply/internal/tier"
)

// ttffRig is the queue with the subtitle and artwork kinds over a real store, a real
// subtitle generator and a real art generator, with fake ffmpegs, and a governor that
// sees the plays a test sets. Two disks: files under nas/ are on "nas", the rest on "ssd".
type ttffRig struct {
	t     *testing.T
	dir   string
	st    *store.Store
	gov   *tier.Governor
	q     *jobs.Queue
	subs  *subs.Generator
	art   *art.Generator
	log   string // the subtitle ffmpeg's argument log
	ctx   context.Context
	media int

	mu      sync.Mutex
	playing []string
	paused  bool            // the plays are paused (a play, but none active)
	mgr     *stream.Manager // when set, its plays (and plays being set up) count too
}

func (r *ttffRig) PlaybackLoad() tier.PlaybackLoad {
	r.mu.Lock()
	l := tier.PlaybackLoad{Sessions: len(r.playing), Active: len(r.playing), Paths: slices.Clone(r.playing)}
	if r.paused {
		l.Active = 0
	}
	mgr := r.mgr
	r.mu.Unlock()
	if mgr != nil {
		m := tier.PlaybackLoad(mgr.PlaybackLoad())
		l.Sessions += m.Sessions
		l.Active += m.Active
		l.Paths = append(l.Paths, m.Paths...)
	}
	return l
}

// play sets what is being played (paths), and tells the governor.
func (r *ttffRig) play(paths ...string) {
	r.mu.Lock()
	r.playing, r.paused = paths, false
	r.mu.Unlock()
	r.gov.Notify()
}

// pause pauses the plays.
func (r *ttffRig) pause(paused bool) {
	r.mu.Lock()
	r.paused = paused
	r.mu.Unlock()
	r.gov.Notify()
}

const (
	subsFake = `
echo "$@" >> "$0.log"
case "$*" in *bad.mkv*) echo "Invalid data found" >&2; exit 1;; esac
for a in "$@"; do case "$a" in *.tmp) printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n\n' > "$a";; esac; done
`
	artFake = `
case "$*" in *-filters*) exit 0;; esac
out=
for a in "$@"; do case "$a" in *.tmp) out=$a;; esac; done
[ -n "$out" ] || exit 0
printf '\377\330fakejpeg' > "$out"
i=0; while [ $i -lt 144 ]; do printf '\200\100\060'; i=$((i+1)); done
`
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newTTFFRig builds the rig; the queue runs, with the kinds registered.
func newTTFFRig(t *testing.T, subsScript string) *ttffRig {
	t.Helper()
	return newTTFFRigScripts(t, subsScript, artFake)
}

// newTTFFRigScripts is newTTFFRig with the art generator's ffmpeg a test's own.
func newTTFFRigScripts(t *testing.T, subsScript, artScript string) *ttffRig {
	t.Helper()
	r := &ttffRig{t: t, dir: t.TempDir()}
	var err error
	if r.st, err = store.Open(filepath.Join(r.dir, "data", "ply.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.st.Close() })
	disk := func(p string) (string, bool) {
		if strings.Contains(p, string(filepath.Separator)+"nas"+string(filepath.Separator)) {
			return "nas", true
		}
		return "ssd", true
	}
	r.gov = tier.New(tier.Options{Playback: r, Poll: 5 * time.Millisecond, Disk: disk})

	ff := writeScript(t, subsScript)
	r.log = ff + ".log"
	if r.subs, err = subs.New(subs.Options{Dir: filepath.Join(r.dir, "subs"), FFmpeg: ff}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.subs.Close)
	var cancel context.CancelFunc
	r.ctx, cancel = context.WithCancel(context.Background())
	if r.art, err = art.New(r.ctx, art.Options{Dir: filepath.Join(r.dir, "art"), FFmpeg: writeScript(t, artScript)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.art.Close)

	r.q = jobs.New(jobs.Options{Gate: r.gov, Logger: slog.New(slog.DiscardHandler)})
	d := queueDeps{store: r.st, gov: r.gov, subs: r.subs, art: r.art, log: slog.New(slog.DiscardHandler)}
	registerSubs(r.q, d)
	registerArtGrab(r.q, d)
	done := make(chan struct{})
	go func() { r.q.Run(r.ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-r.q.Ready()
	return r
}

// file makes a media file under dir/<disk>/ and returns its path.
func (r *ttffRig) file(disk, name string) string {
	r.t.Helper()
	p := filepath.Join(r.dir, disk, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("media"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// item adds a movie with two text subtitle streams (2 and 3) and a PGS one (4).
func (r *ttffRig) item(disk, name string, text bool) store.Entry {
	r.t.Helper()
	path := r.file(disk, name)
	fi, _ := os.Stat(path)
	streams := []probe.Stream{
		{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2, Default: true},
	}
	if text {
		streams = append(streams,
			probe.Stream{Index: 2, Type: "subtitle", Codec: "subrip", Language: "eng", Default: true},
			probe.Stream{Index: 3, Type: "subtitle", Codec: "subrip", Language: "por"},
			probe.Stream{Index: 4, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"})
	}
	r.media++
	id, err := r.st.UpsertItem(r.ctx, &store.Item{Kind: store.KindMovie, Title: fmt.Sprintf("Film %d", r.media), Path: path,
		Root: filepath.Join(r.dir, disk), Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Container: "mp4", DurationSec: 600}, streams)
	if err != nil {
		r.t.Fatal(err)
	}
	e, err := r.st.GetEntry(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

// episode adds an episode of show (with the text tracks of item, if text).
func (r *ttffRig) episode(disk, show string, season, number int, text bool) store.Entry {
	r.t.Helper()
	path := r.file(disk, fmt.Sprintf("%s S%02dE%02d.mkv", show, season, number))
	fi, _ := os.Stat(path)
	streams := []probe.Stream{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}, {Index: 1, Type: "audio", Codec: "aac", Channels: 2}}
	if text {
		streams = append(streams, probe.Stream{Index: 2, Type: "subtitle", Codec: "subrip", Language: "eng"})
	}
	id, err := r.st.UpsertItem(r.ctx, &store.Item{Kind: store.KindEpisode, Show: show, Season: season, Episode: number, Title: fmt.Sprintf("E%d", number),
		Path: path, Root: filepath.Join(r.dir, disk), Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Container: "mkv", DurationSec: 600}, streams)
	if err != nil {
		r.t.Fatal(err)
	}
	e, err := r.st.GetEntry(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

// watch records that somebody is pos seconds into e (of 600): started, or watched to the end.
func (r *ttffRig) watch(e store.Entry, pos float64) {
	r.t.Helper()
	if _, err := r.st.SetProgress(r.ctx, e.ID, pos, 600); err != nil {
		r.t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // progress is ordered by the time it was set
}

func (r *ttffRig) runs() int {
	b, _ := os.ReadFile(r.log)
	return strings.Count(string(b), "\n")
}

// queuedOrHeld counts the jobs of kind waiting in any form (queued, or held at the gate).
func (r *ttffRig) waiting(kind string) (n int, held []string) {
	s := r.q.Snapshot()
	for _, j := range append(slices.Clone(s.Running), s.Queued...) {
		if j.Kind == kind {
			n++
			if j.Held != "" {
				held = append(held, j.Held)
			}
		}
	}
	return n, held
}

func (r *ttffRig) state(kind, key string) jobs.State {
	for _, j := range append(append([]jobs.Job{}, r.q.Snapshot().Running...), r.q.Snapshot().History...) {
		if j.Kind == kind && j.Key == key && (j.State.Finished() || j.State == jobs.Running) {
			return j.State
		}
	}
	return ""
}

func (r *ttffRig) waitState(kind, key string, want jobs.State) {
	r.t.Helper()
	waitUntil(r.t, fmt.Sprintf("%s %s to be %s", kind, key, want), 10*time.Second, func() bool { return r.state(kind, key) == want })
}

func (r *ttffRig) enqueueSubs(e store.Entry) {
	r.t.Helper()
	if _, err := r.q.Enqueue(r.ctx, kindSubsPreextract, jobs.Opt{Key: subsKey(e.ID), Payload: subsPayload{ID: e.ID, Path: e.Path}}); err != nil {
		r.t.Fatal(err)
	}
}

// The kind: low priority, one job per item (the same item twice is one job), a title that
// names the file.
func TestSubsPreextractKind(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	if err := r.q.SetPaused(r.ctx, true); err != nil { // the jobs stay where the test can see them
		t.Fatal(err)
	}
	a, b := r.item("nas", "A (2020).mp4", true), r.item("nas", "B (2021).mp4", true)

	r.enqueueSubs(a)
	r.enqueueSubs(a)
	r.enqueueSubs(b) // positive control: another item is another job

	var got []jobs.Job
	for _, j := range r.q.Snapshot().Queued {
		if j.Kind == kindSubsPreextract {
			got = append(got, j)
		}
	}
	if len(got) != 2 {
		t.Fatalf("jobs = %+v, want one per item", got)
	}
	for _, j := range got {
		want := a
		if j.Key == subsKey(b.ID) {
			want = b
		}
		if j.Kind != kindSubsPreextract || j.Key != subsKey(want.ID) || j.Priority != jobs.PriorityCache ||
			j.Title != "Extract the subtitles of "+filepath.Base(want.Path) {
			t.Errorf("job = %+v", j)
		}
	}
	if r.runs() != 0 {
		t.Errorf("ffmpeg ran %d times with the queue paused", r.runs())
	}
}

// Pre-extraction reads the container once for all the text tracks, and the play path finds
// them in the cache with no ffmpeg at all.
func TestPreextractFillsTheCache(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	e := r.item("ssd", "Film (2020).mp4", true)
	srcs := api.EmbeddedSubtitles(e)
	if len(srcs) != 2 {
		t.Fatalf("setup: %d text tracks, want 2 (the PGS is not text)", len(srcs))
	}
	for _, s := range srcs {
		if r.subs.Cached(s) {
			t.Fatalf("setup: track %s cached before the job", s.Key) // positive control
		}
	}

	r.enqueueSubs(e)
	r.waitState(kindSubsPreextract, subsKey(e.ID), jobs.Done)
	for _, s := range srcs {
		if !r.subs.Cached(s) {
			t.Errorf("track %s is not cached after the job", s.Key)
		}
	}
	b, _ := os.ReadFile(r.log)
	if r.runs() != 1 || !strings.Contains(string(b), "-map 0:2") || !strings.Contains(string(b), "-map 0:3") || strings.Contains(string(b), "-map 0:4") {
		t.Errorf("ffmpeg log = %q, want one run mapping streams 2 and 3", b)
	}

	// The play path: both tracks come from the cache.
	for _, s := range srcs {
		p, err := r.subs.Path(r.ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "WEBVTT") {
			t.Errorf("track %s = %q", s.Key, b)
		}
	}
	if r.runs() != 1 {
		t.Errorf("playing ran ffmpeg again (%d runs)", r.runs())
	}
}

// Nothing runs while a play holds the disk; a file on another disk does not wait; when the
// play lets go, the held job runs.
func TestPreextractWaitsForTheDisk(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	r.play(r.file("nas", "playing.mkv"))
	held := r.item("nas", "Held (2020).mp4", true)
	free := r.item("ssd", "Free (2020).mp4", true)

	r.enqueueSubs(held)
	waitUntil(t, "the job to say why it waits", 5*time.Second, func() bool {
		n, why := r.waiting(kindSubsPreextract)
		return n == 1 && len(why) == 1 && strings.Contains(why[0], "same disk")
	})
	r.enqueueSubs(free)
	r.waitState(kindSubsPreextract, subsKey(free.ID), jobs.Done) // the other disk is free: it ran, past the held one
	if r.runs() != 1 {
		t.Fatalf("%d runs, want only the free disk's", r.runs())
	}
	if c := r.subs.Cached(api.EmbeddedSubtitles(held)[0]); c {
		t.Fatal("the track on the held disk was extracted while a video plays from it")
	}
	time.Sleep(100 * time.Millisecond)
	if r.state(kindSubsPreextract, subsKey(held.ID)) != "" || r.runs() != 1 {
		t.Fatalf("the held job ran: state %q, %d runs", r.state(kindSubsPreextract, subsKey(held.ID)), r.runs())
	}

	r.play() // the play is over
	r.waitState(kindSubsPreextract, subsKey(held.ID), jobs.Done)
	if r.runs() != 2 || !r.subs.Cached(api.EmbeddedSubtitles(held)[0]) {
		t.Errorf("after the play: %d runs, cached %v", r.runs(), r.subs.Cached(api.EmbeddedSubtitles(held)[0]))
	}
}

// stopped reports whether the process is suspended (ps says state T).
func stopped(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o state -p %d: %v", pid, err)
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "T")
}

// A play that starts while the extraction is running suspends ffmpeg (the read stops), and
// when the play is over it goes on.
func TestAPlayStartingSuspendsARunningExtraction(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := newTTFFRig(t, `echo $$ > `+pidFile+`.tmp; mv `+pidFile+`.tmp `+pidFile+`; exec sleep 300`)
	e := r.item("nas", "Film (2020).mp4", true)
	r.enqueueSubs(e)

	var pid int
	waitUntil(t, "ffmpeg to start", 10*time.Second, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	if stopped(t, pid) {
		t.Fatal("setup: ffmpeg is suspended with no play")
	}

	r.play(r.file("nas", "playing.mkv"))
	waitUntil(t, "ffmpeg to be suspended by the play", 5*time.Second, func() bool { return stopped(t, pid) })
	waitUntil(t, "the job to say it waits", 5*time.Second, func() bool {
		for _, j := range r.q.Snapshot().Running {
			if j.Kind == kindSubsPreextract && strings.Contains(j.Note, "same disk") {
				return true
			}
		}
		return false
	})

	r.play() // over
	waitUntil(t, "ffmpeg to go on", 5*time.Second, func() bool { return !stopped(t, pid) })
	for _, j := range r.q.Snapshot().Running { // and to stop saying it waits
		if j.Kind == kindSubsPreextract {
			if err := r.q.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.waitState(kindSubsPreextract, subsKey(e.ID), jobs.Cancelled)
	waitUntil(t, "ffmpeg to be gone with its job", 5*time.Second, func() bool {
		return exec.Command("ps", "-p", strconv.Itoa(pid)).Run() != nil
	})
}

// todoOf is what the feeder holds to queue, in order.
func todoOf(f *subsFeeder) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.todo)
}

func newFeeder(r *ttffRig) *subsFeeder {
	return newSubsFeeder(r.st, r.q, r.subs, slog.New(slog.DiscardHandler))
}

// failRecord writes the record a failed conversion leaves (subs.Generator.Failed) for every
// text track of e, as a conversion that failed in an earlier run of ply did.
func (r *ttffRig) failRecord(e store.Entry) {
	r.t.Helper()
	for _, s := range api.EmbeddedSubtitles(e) {
		p := filepath.Join(r.dir, "subs", fmt.Sprintf("%d-%s-%d.fail", s.ItemID, s.Key, s.MTime))
		if err := os.WriteFile(p, []byte("exit status 1\n"), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// The sweep queues what somebody is likely to play next, and nothing else: the items they are part
// way through (the one played last first), then the next episode of each show they watch. Never the
// library (a backlog is weeks of the link), and not what has no text track, is cached, or failed.
func TestFeederSweepQueuesOnlyWhatIsBeingWatched(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	f := newFeeder(r)
	for i := range 12 { // the backlog: films with text tracks that nobody has started
		r.item("ssd", fmt.Sprintf("Backlog %02d (2020).mp4", i), true)
	}
	half, second := r.item("ssd", "Half (2020).mp4", true), r.item("ssd", "Second (2020).mp4", true)
	plain := r.item("ssd", "Plain (2020).mp4", false)  // part way, no text track
	cached := r.item("ssd", "Cached (2020).mp4", true) // part way, extracted already
	failed := r.item("ssd", "Failed (2020).mp4", true) // part way, ffmpeg could not do it before
	alpha1, alpha2, alpha3 := r.episode("ssd", "Alpha", 1, 1, true), r.episode("ssd", "Alpha", 1, 2, true), r.episode("ssd", "Alpha", 1, 3, true)
	beta1, beta2 := r.episode("ssd", "Beta", 1, 1, true), r.episode("ssd", "Beta", 1, 2, true)
	r.episode("ssd", "Gamma", 1, 1, true) // a show nobody watches

	for _, s := range api.EmbeddedSubtitles(cached) {
		if err := os.WriteFile(filepath.Join(r.dir, "subs", fmt.Sprintf("%d-%s-%d.vtt", s.ItemID, s.Key, s.MTime)), []byte("WEBVTT\n\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.failRecord(failed)
	for _, e := range []store.Entry{half, second, plain, cached, failed} {
		r.watch(e, 100)
	}
	r.watch(alpha1, 590) // watched to the end: Alpha 2 is next
	r.watch(beta1, 100)  // Beta 1 is being watched; Beta 2 is next

	f.sweep(r.ctx)
	want := []int64{beta1.ID, second.ID, half.ID, beta2.ID, alpha2.ID}
	if got := todoOf(f); !slices.Equal(got, want) {
		t.Errorf("sweep = %v, want %v: what is being watched (last played first), then the next episodes (the show watched last first); not the backlog, nor %d (no text), %d (cached), %d (failed), nor Alpha 3 (%d)",
			got, want, plain.ID, cached.ID, failed.ID, alpha3.ID)
	}

	// Again: nothing twice.
	f.sweep(r.ctx)
	if got := todoOf(f); !slices.Equal(got, want) {
		t.Errorf("second sweep = %v, want the same %v", got, want)
	}
}

// Shows watched at the same moment (a progress written for a whole batch) come out in the order of their
// episodes' ids, whatever order the map of shows is walked in, and the show watched later comes first.
func TestNextEpisodesTiesGoByID(t *testing.T) {
	ep := func(id int64, show string, n int, p *store.Progress) store.Entry {
		return store.Entry{ID: id, Kind: store.KindEpisode, Show: show, Season: 1, Episode: n, Progress: p}
	}
	seen := func(at int64) *store.Progress {
		return &store.Progress{PositionSec: 100, DurationSec: 600, UpdatedAt: at}
	}
	// Listed by show, as ListItems does; the ids do not follow the shows' names.
	eps := []store.Entry{
		ep(5, "Alpha", 1, seen(100)), ep(40, "Alpha", 2, nil),
		ep(6, "Beta", 1, seen(100)), ep(30, "Beta", 2, nil),
		ep(7, "Gamma", 1, seen(200)), ep(50, "Gamma", 2, nil),
	}
	want := []int64{50, 30, 40} // Gamma (watched later), then Alpha and Beta together: lower id first
	ids := func(es []store.Entry) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	for i := range 100 { // the order shows are visited in changes from call to call
		if got := ids(nextEpisodes(eps, 20)); !slices.Equal(got, want) {
			t.Fatalf("call %d: next episodes %v, want %v", i, got, want)
		}
	}
	if got := ids(nextEpisodes(eps, 2)); !slices.Equal(got, want[:2]) {
		t.Errorf("limited to 2: %v, want %v", got, want[:2])
	}
}

// A scanner's new files go ahead of what the sweep queued.
func TestAddQueuesNewFilesAheadOfTheRest(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	f := newFeeder(r)
	f.todo = []int64{1, 2}
	f.Add(7, 8)
	if got, want := todoOf(f), []int64{7, 8, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("todo = %v, want %v", got, want)
	}
}

// The scanner hands over every file a scan created: the first scan of a library just added is all of
// them, and reading every film of it is the days of the link the sweep is built to avoid. Up to addMax
// files at a time are new files; more is an import, and none of it is queued.
func TestAddDoesNotQueueAnImport(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(100 + i)
	}

	f := newFeeder(r)
	f.Add(ids[:addMax]...) // positive control: the most that still counts as new files
	if got := todoOf(f); !slices.Equal(got, ids[:addMax]) {
		t.Fatalf("a scan that added %d files queued %v, want them all", addMax, got)
	}

	f = newFeeder(r)
	f.Add(ids[:addMax+1]...)
	f.Add(ids...) // the first scan of a library of a hundred films
	if got := todoOf(f); len(got) != 0 {
		t.Errorf("an import was queued: %v", got)
	}
	f.Add(7) // the feeder is alive: the next new file is queued
	if got := todoOf(f); !slices.Equal(got, []int64{7}) {
		t.Errorf("after an import, a new file queued %v, want [7]", got)
	}
}

// An item that ffmpeg cannot read fails once: not in this run of ply, not in the next, and not
// when two hundred other jobs have pushed it out of the queue's history (the record is the
// cache directory's, not the history's).
func TestAFailedItemIsNotQueuedAgain(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	bad := r.item("ssd", "bad.mkv", true)
	r.watch(bad, 100)
	f := newFeeder(r)
	f.sweep(r.ctx)
	if got := todoOf(f); !slices.Equal(got, []int64{bad.ID}) { // positive control: before it fails, it is queued
		t.Fatalf("sweep before = %v, want the item", got)
	}

	r.enqueueSubs(bad)
	r.waitState(kindSubsPreextract, subsKey(bad.ID), jobs.Failed)

	var last int64
	for i := range 205 { // other items' jobs finishing: the failure leaves the history of 200
		last = int64(1_000_000 + i)
		if _, err := r.q.Enqueue(r.ctx, kindSubsPreextract, jobs.Opt{Key: subsKey(last), Payload: subsPayload{ID: last, Path: "/x"}}); err != nil {
			t.Fatal(err)
		}
	}
	r.waitState(kindSubsPreextract, subsKey(last), jobs.Done)
	if r.state(kindSubsPreextract, subsKey(bad.ID)) != "" {
		t.Fatal("setup: the failure is still in the history")
	}
	f.todo = nil
	f.sweep(r.ctx)
	if got := todoOf(f); len(got) != 0 {
		t.Errorf("sweep after the history rolled = %v, want nothing", got)
	}

	// A new ply (a new generator on the same cache directory) knows too.
	g2, err := subs.New(subs.Options{Dir: filepath.Join(r.dir, "subs"), FFmpeg: "/no/such/ffmpeg"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g2.Close)
	f2 := newSubsFeeder(r.st, r.q, g2, slog.New(slog.DiscardHandler))
	f2.sweep(r.ctx)
	if got := todoOf(f2); len(got) != 0 {
		t.Errorf("sweep of a ply that started after the failure = %v, want nothing", got)
	}
}

// A job a person cancels in Activity counts as failed until ply restarts: the sweep and the
// scanner do not bring it back. (Another item goes on being queued: the feeder is alive.)
func TestACancelledJobIsNotQueuedAgainUntilRestart(t *testing.T) {
	r := newTTFFRig(t, "sleep 300")
	a, b := r.item("ssd", "A (2020).mp4", true), r.item("ssd", "B (2020).mp4", true)
	r.watch(a, 100)
	f := newFeeder(r)
	f.every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	f.Add(a.ID)
	r.waitState(kindSubsPreextract, subsKey(a.ID), jobs.Running)
	for _, j := range r.q.Snapshot().Running {
		if j.Kind == kindSubsPreextract {
			if err := r.q.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.waitState(kindSubsPreextract, subsKey(a.ID), jobs.Cancelled)
	waitUntil(t, "the feeder to hear of the cancel", 5*time.Second, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cancelled[a.ID]
	})

	f.Add(a.ID) // a scan found it new again
	f.Rescan()  // and a sweep found it part way
	f.Add(b.ID) // positive control: another item is queued
	r.waitState(kindSubsPreextract, subsKey(b.ID), jobs.Running)
	for _, j := range r.q.Snapshot().Running { // let the feeder go through the rest of what it holds
		if j.Kind == kindSubsPreextract {
			if err := r.q.Cancel(j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitUntil(t, "the feeder to go through its list", 5*time.Second, func() bool { return len(todoOf(f)) == 0 && !r.q.Busy(kindSubsPreextract) })
	time.Sleep(100 * time.Millisecond)
	if n, _ := r.waiting(kindSubsPreextract); n != 0 || r.state(kindSubsPreextract, subsKey(a.ID)) == jobs.Running {
		t.Errorf("the cancelled item was queued again (%d subtitle jobs waiting)", n)
	}

	// A new ply (a new feeder) has no memory of it.
	f2 := newFeeder(r)
	if !f2.wants(a) {
		t.Error("a feeder that started after the cancel does not want the item")
	}
}

// ffmpeg that behaves like the real one on a missing input: exit 1.
const missingAware = `
prev=; for a in "$@"; do if [ "$prev" = "-i" ] && [ ! -e "$a" ]; then echo "$a: No such file or directory" >&2; exit 1; fi; prev=$a; done
for a in "$@"; do case "$a" in *.tmp) printf 'WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n\n' > "$a";; esac; done
`

// A library on a share that is not mounted yet (ply starts at login, before the mount) must not end
// in failed jobs for good: the job is tried again after a while, and when the share is there it is
// done.
func TestAnUnreachableShareIsRetriedNotGivenUpOn(t *testing.T) {
	r := newTTFFRig(t, missingAware)
	e := r.item("nas", "Film (2020).mp4", true)
	r.watch(e, 100)
	if err := os.Rename(e.Path, e.Path+".away"); err != nil {
		t.Fatal(err)
	}
	r.enqueueSubs(e)

	var job jobs.Job
	waitUntil(t, "the first attempt to fail and the job to wait for its retry", 10*time.Second, func() bool {
		for _, j := range r.q.Snapshot().Queued {
			if j.Kind == kindSubsPreextract && j.Attempts == 1 && j.Error != "" {
				job = j
				return true
			}
		}
		return false
	})
	if !job.RunAt.After(time.Now().Add(30 * time.Second)) {
		t.Errorf("the retry is due at %v, want a backoff of a minute", job.RunAt)
	}
	if st := r.state(kindSubsPreextract, subsKey(e.ID)); st == jobs.Failed {
		t.Fatalf("the job failed for good with the share gone")
	}
	if err := r.subs.Failed(api.EmbeddedSubtitles(e)[0]); err != nil {
		t.Errorf("the unreachable file was recorded as a failure: %v", err)
	}

	// The share is mounted: the sweep wants the item as before, and the retry (brought forward) works.
	if err := os.Rename(e.Path+".away", e.Path); err != nil {
		t.Fatal(err)
	}
	if f := newFeeder(r); !f.wants(e) {
		t.Error("the feeder does not want the item although nothing failed")
	}
	if err := r.q.Bump(job.ID); err != nil {
		t.Fatal(err)
	}
	r.waitState(kindSubsPreextract, subsKey(e.ID), jobs.Done)
	if !r.subs.Cached(api.EmbeddedSubtitles(e)[0]) {
		t.Error("the track was not extracted once the share was back")
	}
}

// A share that sleeps over a weekend is asked again for days, not for the queue's default five tries
// (fifteen minutes): a new file the sweep does not list would otherwise wait for its first play.
func TestAnUnreachableShareIsRetriedPastTheDefaultFiveTries(t *testing.T) {
	r := newTTFFRig(t, missingAware)
	e := r.item("nas", "Film (2020).mp4", true)
	if err := os.Rename(e.Path, e.Path+".away"); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(jobs.Options{Gate: r.gov, Logger: slog.New(slog.DiscardHandler), Backoff: func(int) time.Duration { return time.Millisecond }})
	registerSubs(q, queueDeps{store: r.st, gov: r.gov, subs: r.subs, log: slog.New(slog.DiscardHandler)})
	qctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	if _, err := q.Enqueue(r.ctx, kindSubsPreextract, jobs.Opt{Key: subsKey(e.ID), Payload: subsPayload{ID: e.ID, Path: e.Path}}); err != nil {
		t.Fatal(err)
	}

	const defaultTries = 5
	waitUntil(t, "the job to be tried more often than the queue's default", 10*time.Second, func() bool {
		s := q.Snapshot()
		for _, j := range s.History {
			if j.Kind == kindSubsPreextract && j.State == jobs.Failed {
				t.Fatalf("the job failed for good after %d tries with the share gone: %s", j.Attempts, j.Error)
			}
		}
		for _, j := range append(slices.Clone(s.Running), s.Queued...) {
			if j.Kind == kindSubsPreextract && j.Attempts > defaultTries {
				return true
			}
		}
		return false
	})

	// Positive control: the job was alive all along, and the share coming back is what ends it.
	if err := os.Rename(e.Path+".away", e.Path); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the job to finish once the share is back", 10*time.Second, func() bool {
		for _, j := range q.Snapshot().History {
			if j.Kind == kindSubsPreextract && j.State == jobs.Done {
				return true
			}
		}
		return false
	})
	if !r.subs.Cached(api.EmbeddedSubtitles(e)[0]) {
		t.Error("the track was not extracted once the share was back")
	}
}

// recGate is the governor, noting what each running job asks it to wait for.
type recGate struct {
	*tier.Governor
	mu    sync.Mutex
	needs []tier.Need
}

func (g *recGate) Wait(ctx context.Context, n tier.Need, held func(reason string)) error {
	g.mu.Lock()
	g.needs = append(g.needs, n)
	g.mu.Unlock()
	return g.Governor.Wait(ctx, n, held)
}

// The job steps aside through the queue's yield between its reads (each sidecar, then the
// container), for the item's disk: the same need it starts with.
func TestPreextractYieldsToAPlayBetweenItsReads(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	e := r.item("nas", "Film (2020).mp4", true)
	side := filepath.Join(filepath.Dir(e.Path), "Film (2020).en.srt")
	if err := os.WriteFile(side, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetExternalSubtitles(r.ctx, e.ID, []store.ExternalSubtitle{{Path: side, Language: "en", Format: "srt"}}); err != nil {
		t.Fatal(err)
	}
	gate := &recGate{Governor: r.gov}
	q := jobs.New(jobs.Options{Gate: gate, Logger: slog.New(slog.DiscardHandler)})
	registerSubs(q, queueDeps{store: r.st, gov: r.gov, subs: r.subs, log: slog.New(slog.DiscardHandler)})
	qctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	<-q.Ready()
	if _, err := q.Enqueue(r.ctx, kindSubsPreextract, jobs.Opt{Key: subsKey(e.ID), Payload: subsPayload{ID: e.ID, Path: e.Path}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the job to be done", 10*time.Second, func() bool {
		for _, j := range q.Snapshot().History {
			if j.Kind == kindSubsPreextract && j.State == jobs.Done {
				return true
			}
		}
		return false
	})
	gate.mu.Lock()
	defer gate.mu.Unlock()
	want := []tier.Need{{Disks: []string{e.Path}}, {Disks: []string{e.Path}}} // before the sidecar, before the container
	if len(gate.needs) != 2 || !slices.EqualFunc(gate.needs, want, func(a, b tier.Need) bool { return a.CPU == b.CPU && slices.Equal(a.Disks, b.Disks) }) {
		t.Errorf("the job yielded for %+v, want %+v", gate.needs, want)
	}
	if r.runs() != 2 {
		t.Errorf("%d runs, want 2 (the sidecar, then the container)", r.runs())
	}
}

// The disk a play holds from an extraction: a play, or a viewer who is stalled, but not someone
// browsing (it would stop and start ffmpeg for every page of the library), and not a paused play
// for longer than two minutes.
func TestDiskHold(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	now := time.Unix(1_700_000_000, 0)
	h := newDiskHold(r.gov)
	h.now = func() time.Time { return now }
	nas := r.file("nas", "Film (2020).mp4")

	if wait, _ := h.check(nas); wait {
		t.Fatal("held with nothing playing")
	}

	r.play(r.file("ssd", "other.mkv"))
	if wait, _ := h.check(nas); wait {
		t.Error("held for a play on another disk")
	}

	r.play(r.file("nas", "playing.mkv"))
	if wait, why := h.check(nas); !wait || !strings.Contains(why, "same disk") {
		t.Errorf("a play on the same disk: wait %v, %q", wait, why)
	}

	// Paused: held for the first two minutes, then let go; playing again holds again, from the start.
	r.pause(true)
	if wait, _ := h.check(nas); !wait {
		t.Error("a paused play did not hold at first")
	}
	now = now.Add(pausedHoldFor - time.Second)
	if wait, _ := h.check(nas); !wait {
		t.Error("a play paused for just under two minutes does not hold")
	}
	now = now.Add(2 * time.Second)
	if wait, _ := h.check(nas); wait {
		t.Error("a play paused for two minutes still holds the disk")
	}
	r.pause(false)
	if wait, _ := h.check(nas); !wait {
		t.Error("a play that went on does not hold")
	}
	r.pause(true)
	if wait, _ := h.check(nas); !wait {
		t.Error("a play paused again does not hold for its first two minutes")
	}

	// Browsing alone: the governor holds a job for it (positive control), the extraction does not.
	r.play()
	browse := r.gov.Track(func(*http.Request) bool { return true })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	browse.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/items", nil))
	if hd := r.gov.Check(tier.Need{Disks: []string{nas}}); !hd.Held || !hd.Browsing {
		t.Fatalf("setup: browsing does not hold the governor's check: %+v", hd)
	}
	if wait, why := h.check(nas); wait {
		t.Errorf("someone browsing holds the extraction: %q", why)
	}

	// No governor: nothing holds.
	if wait, _ := newDiskHold(nil).check(nas); wait {
		t.Error("held with no governor")
	}
}

// gatedIndex holds Create where the keyframe scan of a first play would be, until the test lets
// it go with an error.
type gatedIndex struct {
	entered chan struct{}
	release chan error
}

func (g gatedIndex) Keyframes(ctx context.Context, _ int64) ([]float64, bool, error) {
	g.entered <- struct{}{}
	select {
	case err := <-g.release:
		return nil, false, err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}
func (gatedIndex) SetKeyframes(context.Context, int64, []float64) error { return nil }

// A play that is being set up holds the disk: the first play of a film over a slow share reads it
// for minutes (the keyframe index) before there is a session, and the extraction must not start
// reading at the same time; when the play is given up, the extraction goes.
func TestPreextractWaitsForAPlayBeingSetUp(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	gate := gatedIndex{entered: make(chan struct{}, 1), release: make(chan error, 1)}
	m, err := stream.NewManager(stream.Options{FFmpeg: "/no/such/ffmpeg", TempRoot: t.TempDir(), Cache: gate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	r.mu.Lock()
	r.mgr = m
	r.mu.Unlock()
	m.OnLoadChange(r.gov.Notify)

	playing := r.file("nas", "Playing (2020).mkv")
	e := r.item("nas", "Film (2020).mp4", true)
	created := make(chan error, 1)
	go func() {
		_, err := m.Create(r.ctx, stream.Request{ItemID: 1, Path: playing, Container: "mkv", DurationSec: 1200, VideoIndex: 0,
			VideoCodec: "hevc", AudioIndex: 1, AudioCodec: "eac3", AudioCopy: true})
		created <- err
	}()
	<-gate.entered // the play is being set up: no session yet, the index is being read
	if pb := m.PlaybackLoad(); pb.Sessions != 1 || !slices.Equal(pb.Paths, []string{playing}) {
		t.Fatalf("setup: the play being set up is not in the load: %+v", pb)
	}

	r.enqueueSubs(e)
	waitUntil(t, "the job to wait for the disk", 5*time.Second, func() bool {
		n, why := r.waiting(kindSubsPreextract)
		return n == 1 && len(why) == 1 && strings.Contains(why[0], "same disk")
	})
	time.Sleep(150 * time.Millisecond)
	if r.runs() != 0 {
		t.Fatalf("%d runs while a play was being set up on the disk", r.runs())
	}

	gate.release <- errors.New("the play was given up")
	if err := <-created; err == nil {
		t.Fatal("Create succeeded")
	}
	r.waitState(kindSubsPreextract, subsKey(e.ID), jobs.Done) // the hook told the governor: no wait for a poll
}

// The feeder keeps one subtitle job in the queue at a time (each reads a whole film), new
// files first, and skips what has nothing to extract.
func TestFeederQueuesOneJobAtATime(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	r.play(r.file("nas", "playing.mkv")) // jobs on the nas stay waiting
	a, b := r.item("nas", "A (2020).mp4", true), r.item("nas", "B (2020).mp4", true)
	plain := r.item("nas", "Plain (2020).mp4", false)

	f := newSubsFeeder(r.st, r.q, r.subs, slog.New(slog.DiscardHandler))
	f.every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	f.Add(plain.ID, a.ID, b.ID)
	waitUntil(t, "the first job to wait for the disk", 5*time.Second, func() bool { n, held := r.waiting(kindSubsPreextract); return n == 1 && len(held) == 1 })
	time.Sleep(150 * time.Millisecond) // many ticks of the feeder
	if n, _ := r.waiting(kindSubsPreextract); n != 1 {
		t.Fatalf("%d subtitle jobs in the queue, want 1 at a time", n)
	}
	if r.state(kindSubsPreextract, subsKey(plain.ID)) != "" {
		t.Error("an item with no text track was queued")
	}

	r.play() // the disk is free: a, then b
	r.waitState(kindSubsPreextract, subsKey(a.ID), jobs.Done)
	r.waitState(kindSubsPreextract, subsKey(b.ID), jobs.Done)
	if r.runs() != 2 {
		t.Errorf("%d runs for two films, want 2", r.runs())
	}
}

// ---- artwork -------------------------------------------------------------------

// The grab job needs the file's disk and the CPU, is one job per image, and makes the image
// once the disk is free.
func TestArtGrabKind(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	r.play(r.file("nas", "playing.mkv"))
	held, free := r.item("nas", "Held (2020).mp4", false), r.item("ssd", "Free (2020).mp4", false)
	grab := func(e store.Entry, k art.Kind) art.Source {
		src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime, DurationSec: e.DurationSec}
		if _, err := r.q.Enqueue(r.ctx, kindArtGrab, jobs.Opt{Key: artKey(src, k), Payload: art.Grab{Src: src, Kind: k}}); err != nil {
			t.Fatal(err)
		}
		return src
	}

	heldSrc := grab(held, art.Thumb)
	grab(held, art.Thumb) // the same image again: the same job
	waitUntil(t, "the grab to wait for the disk", 5*time.Second, func() bool {
		n, why := r.waiting(kindArtGrab)
		return n == 1 && len(why) == 1 && strings.Contains(why[0], "same disk")
	})
	grab(held, art.Backdrop) // positive control: another image is another job
	waitUntil(t, "two grabs", 5*time.Second, func() bool { n, _ := r.waiting(kindArtGrab); return n == 2 })

	freeSrc := grab(free, art.Thumb)
	r.waitState(kindArtGrab, artKey(freeSrc, art.Thumb), jobs.Done)
	if _, ok := r.art.Cached(freeSrc, art.Thumb); !ok {
		t.Error("the free disk's image was not made")
	}
	if _, ok := r.art.Cached(heldSrc, art.Thumb); ok || r.art.Runs() != 1 {
		t.Fatalf("an image was grabbed from a disk a video plays from (%d runs)", r.art.Runs())
	}

	r.play()
	r.waitState(kindArtGrab, artKey(heldSrc, art.Thumb), jobs.Done)
	if _, ok := r.art.Cached(heldSrc, art.Thumb); !ok {
		t.Error("the held image was not made once the disk was free")
	}
}

// Every page that shows the picture asks while a play starts; the queue is told once.
// (A job cancelled in between is not queued again by the next ask: the proof that it was not
// passed on.)
func TestArtLaterTellsTheQueueOnce(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	if err := r.q.SetPaused(r.ctx, true); err != nil {
		t.Fatal(err)
	}
	e := r.item("ssd", "Film (2020).mp4", false)
	src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime}
	later := artLater(r.ctx, r.q, slog.New(slog.DiscardHandler))
	queued := func() []jobs.Job {
		var out []jobs.Job
		for _, j := range r.q.Snapshot().Queued {
			if j.Kind == kindArtGrab {
				out = append(out, j)
			}
		}
		return out
	}

	later(src, art.Thumb, false)
	waitUntil(t, "the grab to be queued", 5*time.Second, func() bool { return len(queued()) == 1 })
	if j := queued()[0]; j.Key != artKey(src, art.Thumb) || j.Priority != jobs.PriorityCache || j.Title != "Make a picture for "+filepath.Base(e.Path) {
		t.Errorf("job = %+v", j)
	}
	if err := r.q.Cancel(queued()[0].ID); err != nil {
		t.Fatal(err)
	}
	later(src, art.Thumb, false) // asked again at once
	later(src, art.Backdrop, false)
	waitUntil(t, "the other image to be queued", 5*time.Second, func() bool { return len(queued()) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got := queued(); len(got) != 1 || got[0].Key != artKey(src, art.Backdrop) {
		t.Errorf("queued after the second ask: %+v, want only the backdrop (the thumb was asked for already)", got)
	}
}

// A grab that was stopped under way (a play began) is asked for again whatever was asked a moment
// ago: that ask was for the grab that has just been stopped.
func TestArtLaterAgainBypassesTheMemo(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	if err := r.q.SetPaused(r.ctx, true); err != nil {
		t.Fatal(err)
	}
	e := r.item("ssd", "Film (2020).mp4", false)
	src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime}
	later := artLater(r.ctx, r.q, slog.New(slog.DiscardHandler))
	queued := func() []jobs.Job {
		var out []jobs.Job
		for _, j := range r.q.Snapshot().Queued {
			if j.Kind == kindArtGrab {
				out = append(out, j)
			}
		}
		return out
	}
	later(src, art.Thumb, false)
	waitUntil(t, "the grab to be queued", 5*time.Second, func() bool { return len(queued()) == 1 })
	if err := r.q.Cancel(queued()[0].ID); err != nil { // (it ran and was stopped, say)
		t.Fatal(err)
	}
	later(src, art.Thumb, false)
	time.Sleep(100 * time.Millisecond)
	if len(queued()) != 0 {
		t.Fatal("setup: the memo did not hold back an ask of a moment ago") // positive control
	}
	later(src, art.Thumb, true)
	waitUntil(t, "the stopped grab to be queued again", 5*time.Second, func() bool { return len(queued()) == 1 })
}

// finished is the newest finished job of kind and key.
func (r *ttffRig) finished(kind, key string) (jobs.Job, bool) {
	for _, j := range r.q.Snapshot().History {
		if j.Kind == kind && j.Key == key {
			return j, true
		}
	}
	return jobs.Job{}, false
}

// The grab job ends at once, done and not tried again, when the file has no picture (the generator
// remembers that) or when a play took the file back from it (the API queued the image again).
func TestArtGrabJobEndsQuietly(t *testing.T) {
	grab := func(r *ttffRig, e store.Entry) (art.Source, string) {
		src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime, DurationSec: e.DurationSec}
		key := artKey(src, art.Thumb)
		if _, err := r.q.Enqueue(r.ctx, kindArtGrab, jobs.Opt{Key: key, Payload: art.Grab{Src: src, Kind: art.Thumb}}); err != nil {
			t.Fatal(err)
		}
		return src, key
	}

	t.Run("no picture in the file", func(t *testing.T) {
		r := newTTFFRigScripts(t, subsFake, `
case "$*" in *-filters*) exit 0;; esac
echo "Output file does not contain any stream" >&2
exit 1`)
		e := r.item("ssd", "Film (2020).mp4", false)
		src, key := grab(r, e)
		r.waitState(kindArtGrab, key, jobs.Done)
		if j, _ := r.finished(kindArtGrab, key); j.Attempts != 1 {
			t.Errorf("the job was tried %d times", j.Attempts)
		}
		if r.art.Runs() == 0 { // positive control: ffmpeg did run, and failed (twice: the seek, then from the start)
			t.Error("no grab ran")
		}
		if _, ok := r.art.Cached(src, art.Thumb); ok {
			t.Error("an image exists for a file with no picture")
		}
	})

	t.Run("a play took the file back", func(t *testing.T) {
		release := filepath.Join(t.TempDir(), "go")
		t.Cleanup(func() { os.WriteFile(release, nil, 0o644) })
		r := newTTFFRigScripts(t, subsFake, `
case "$*" in *-filters*) exit 0;; esac
while [ ! -e "`+release+`" ]; do sleep 0.02; done
exit 1`)
		e := r.item("ssd", "Film (2020).mp4", false)
		_, key := grab(r, e)
		waitUntil(t, "the grab to be reading the file", 5*time.Second, func() bool { return r.art.Runs() == 1 })
		if got := r.art.Defer(e.ID); len(got) != 1 {
			t.Fatalf("Defer stopped %v, want the one grab", got)
		}
		r.waitState(kindArtGrab, key, jobs.Done)
		if j, _ := r.finished(kindArtGrab, key); j.Attempts != 1 || j.Error != "" {
			t.Errorf("the job was tried %d times, error %q; want one, done", j.Attempts, j.Error)
		}
	})
}

// A grab that waited for hours (or across a restart) and finds the file replaced does not keep a
// picture of the new file under the old file's name: the scanner gives the item its new mtime and the
// page asks again.
func TestArtGrabLeavesAFileThatChangedSinceItWasQueued(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	changed, same := r.item("ssd", "Changed (2020).mp4", false), r.item("ssd", "Same (2020).mp4", false)
	grab := func(e store.Entry) (art.Source, string) {
		src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime, DurationSec: e.DurationSec}
		key := artKey(src, art.Thumb)
		if _, err := r.q.Enqueue(r.ctx, kindArtGrab, jobs.Opt{Key: key, Payload: art.Grab{Src: src, Kind: art.Thumb}}); err != nil {
			t.Fatal(err)
		}
		return src, key
	}
	if err := r.q.SetPaused(r.ctx, true); err != nil { // the jobs wait, as behind a film
		t.Fatal(err)
	}
	changedSrc, changedKey := grab(changed)
	sameSrc, sameKey := grab(same)
	later := time.Unix(0, changed.MTime).Add(time.Hour)
	if err := os.Chtimes(changed.Path, later, later); err != nil { // replaced while it waited
		t.Fatal(err)
	}
	if err := r.q.SetPaused(r.ctx, false); err != nil {
		t.Fatal(err)
	}

	r.waitState(kindArtGrab, changedKey, jobs.Done)
	r.waitState(kindArtGrab, sameKey, jobs.Done)
	if _, ok := r.art.Cached(sameSrc, art.Thumb); !ok || r.art.Runs() != 1 { // positive control: a file that did not change is grabbed
		t.Fatalf("the unchanged file: cached %v, %d grabs; want its image and 1 grab", ok, r.art.Runs())
	}
	if _, ok := r.art.Cached(changedSrc, art.Thumb); ok {
		t.Error("a picture of the replaced file was kept under the old name")
	}
}

// The whole chain: a play is set up, the page asks for the item's picture and is not given a
// grab; the grab is queued, made when the disk is free, and then served.
func TestArtGrabIsDeferredQueuedAndMade(t *testing.T) {
	r := newTTFFRig(t, subsFake)
	e, other := r.item("nas", "Playing (2020).mp4", false), r.item("nas", "Other (2020).mp4", false)
	srv := api.New(api.Options{Store: r.st, Art: r.art, Governor: r.gov, Jobs: r.q,
		ArtLater: artLater(r.ctx, r.q, slog.New(slog.DiscardHandler))})
	get := func(method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(rec, req)
		return rec
	}
	image := func(id int64) string { return fmt.Sprintf("/api/items/%d/art/thumb", id) }

	// Positive control: no play, so the page's request grabs at once.
	if rec := get("GET", image(other.ID)); rec.Code != 200 || r.art.Runs() != 1 {
		t.Fatalf("no play: status %d, %d grabs; want 200 and 1", rec.Code, r.art.Runs())
	}

	if rec := get("POST", fmt.Sprintf("/api/items/%d/playback", e.ID)); rec.Code != http.StatusOK {
		t.Fatalf("playback: %d %s", rec.Code, rec.Body.String())
	}
	rec := get("GET", image(e.ID))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || r.art.Runs() != 1 {
		t.Fatalf("while the play starts: status %d (Retry-After %q), %d grabs; want 503 and no new grab", rec.Code, rec.Header().Get("Retry-After"), r.art.Runs())
	}

	// The queue makes it (nothing plays from the disk through the stream manager here).
	src := art.Source{ID: e.ID, Path: e.Path, MTime: e.MTime, DurationSec: e.DurationSec}
	r.waitState(kindArtGrab, artKey(src, art.Thumb), jobs.Done)
	if _, ok := r.art.Cached(src, art.Thumb); !ok {
		t.Fatal("the queue did not make the image")
	}
	if rec := get("GET", image(e.ID)); rec.Code != 200 {
		t.Errorf("after the queue made it: %d, want the image", rec.Code)
	}
}
