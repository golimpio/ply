package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ply/internal/tier"
)

// memPersist is Persist in memory.
type memPersist struct {
	mu     sync.Mutex
	rows   map[int64]Record
	next   int64
	paused bool
}

func newMem() *memPersist { return &memPersist{rows: map[int64]Record{}} }

func (m *memPersist) InsertJob(_ context.Context, r Record) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	r.ID = m.next
	m.rows[r.ID] = r
	return r.ID, nil
}

func (m *memPersist) UpdateJob(_ context.Context, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[r.ID] = r
	return nil
}

func (m *memPersist) LoadJobs(_ context.Context, history int) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

func (m *memPersist) PruneJobs(context.Context, time.Time) (int, error) { return 0, nil }

func (m *memPersist) QueuePaused(context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused, nil
}

func (m *memPersist) SetQueuePaused(_ context.Context, p bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = p
	return nil
}

func (m *memPersist) get(id int64) Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

// fakeGate holds the jobs whose need `only` picks (every job when nil) while
// reason is set.
type fakeGate struct {
	mu     sync.Mutex
	reason string
	only   func(tier.Need) bool
	change chan struct{}
	waits  atomic.Int32
}

func (g *fakeGate) Check(n tier.Need) tier.Hold {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reason != "" && (g.only == nil || g.only(n)) {
		return tier.Hold{Held: true, Reason: g.reason}
	}
	return tier.Hold{}
}

func (g *fakeGate) Changed() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.change
}

func (g *fakeGate) Poll() time.Duration          { return time.Hour } // only a change wakes it
func (g *fakeGate) BrowseMaxHold() time.Duration { return time.Hour }

func newGate() *fakeGate { return &fakeGate{change: make(chan struct{})} }

func (g *fakeGate) hold(reason string) {
	g.mu.Lock()
	g.reason = reason
	close(g.change)
	g.change = make(chan struct{})
	g.mu.Unlock()
}

func (g *fakeGate) Wait(ctx context.Context, n tier.Need, held func(string)) error {
	g.waits.Add(1)
	reported := false
	for {
		h := g.Check(n)
		r, ch := h.Reason, g.Changed()
		if r == "" {
			if reported {
				held("")
			}
			return ctx.Err()
		}
		held(r)
		reported = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (g *fakeGate) State() tier.State { return tier.State{} }

// harness runs a queue until the test ends.
func start(t *testing.T, o Options, specs ...Spec) (*Queue, context.CancelFunc) {
	t.Helper()
	q := New(o)
	for _, s := range specs {
		q.Register(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.Run(ctx)
		close(done)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	<-q.Ready()
	return q, stop
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func state(q *Queue, id int64) State {
	s := q.Snapshot()
	for _, l := range [][]Job{s.Running, s.Queued, s.History} {
		for _, j := range l {
			if j.ID == id {
				return j.State
			}
		}
	}
	return ""
}

// recorder is a kind whose jobs record their payload in the order they run.
type recorder struct {
	mu    sync.Mutex
	order []string
}

func (r *recorder) spec(kind string, prio int) Spec {
	return Spec{Kind: kind, Priority: prio, Run: func(ctx context.Context, run *Run) error {
		var s string
		_ = run.Decode(&s)
		r.mu.Lock()
		r.order = append(r.order, s)
		r.mu.Unlock()
		return nil
	}}
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

func TestPriorityOrder(t *testing.T) {
	rec := &recorder{}
	q, _ := start(t, Options{Light: 1}, rec.spec("scan", PriorityScan), rec.spec("dirs", PriorityDirs), rec.spec("cache", PriorityCache))
	ctx := context.Background()
	if err := q.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ kind, name string }{{"cache", "cache"}, {"scan", "scan"}, {"dirs", "dirs"}, {"cache", "asked"}} {
		o := Opt{Payload: e.name, User: e.name == "asked"}
		if _, err := q.Enqueue(ctx, e.kind, o); err != nil {
			t.Fatal(err)
		}
	}
	queued := q.Snapshot().Queued
	var names []string
	for _, j := range queued {
		var s string
		_ = json.Unmarshal(j.Payload, &s)
		names = append(names, s)
	}
	if want := []string{"asked", "dirs", "scan", "cache"}; !slices.Equal(names, want) {
		t.Fatalf("queued order = %v, want %v", names, want)
	}
	if len(rec.got()) != 0 {
		t.Fatal("a paused queue ran a job")
	}
	if err := q.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "four jobs", func() bool { return len(rec.got()) == 4 })
	if got, want := rec.got(), []string{"asked", "dirs", "scan", "cache"}; !slices.Equal(got, want) {
		t.Fatalf("ran in order %v, want %v", got, want)
	}
}

func TestSameKeyMergesWhileQueuedAndNeverRunsTwiceAtOnce(t *testing.T) {
	var running, maxRunning atomic.Int32
	release := make(chan struct{})
	var runs [][]string
	var mu sync.Mutex
	spec := Spec{Kind: "dirs",
		Merge: func(old, new json.RawMessage) json.RawMessage {
			var a, b []string
			_ = json.Unmarshal(old, &a)
			_ = json.Unmarshal(new, &b)
			out, _ := json.Marshal(append(a, b...))
			return out
		},
		Run: func(ctx context.Context, r *Run) error {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				m := maxRunning.Load()
				if n <= m || maxRunning.CompareAndSwap(m, n) {
					break
				}
			}
			var dirs []string
			_ = r.Decode(&dirs)
			mu.Lock()
			runs = append(runs, dirs)
			mu.Unlock()
			<-release
			return nil
		}}
	q, _ := start(t, Options{Light: 4}, spec)
	ctx := context.Background()
	first, _ := q.Enqueue(ctx, "dirs", Opt{Key: "k", Payload: []string{"a"}})
	waitFor(t, "the first to run", func() bool { return running.Load() == 1 })
	// The key is running: these queue behind it, merged into one job.
	second, _ := q.Enqueue(ctx, "dirs", Opt{Key: "k", Payload: []string{"b"}})
	third, _ := q.Enqueue(ctx, "dirs", Opt{Key: "k", Payload: []string{"c"}})
	if second.ID == first.ID || third.ID != second.ID {
		t.Fatalf("ids %d %d %d: want the 2nd and 3rd merged, apart from the running 1st", first.ID, second.ID, third.ID)
	}
	time.Sleep(30 * time.Millisecond)
	if running.Load() != 1 {
		t.Fatal("a job with the key of a running one started alongside it")
	}
	close(release)
	waitFor(t, "both runs", func() bool { mu.Lock(); defer mu.Unlock(); return len(runs) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(runs[0], []string{"a"}) || !slices.Equal(runs[1], []string{"b", "c"}) {
		t.Fatalf("runs = %v, want [[a] [b c]]", runs)
	}
	if maxRunning.Load() != 1 {
		t.Fatalf("%d ran at once", maxRunning.Load())
	}
}

// A Light kind with a disk need runs beside a heavy job (it does not wait for the
// heavy lane), and is still held by the gate for its disk.
func TestALightKindWithADiskNeedRunsBesideAHeavyJob(t *testing.T) {
	g := newGate()
	g.only = func(n tier.Need) bool { return len(n.Disks) > 0 }
	release := make(chan struct{})
	metaRan := make(chan struct{}, 1)
	q, _ := start(t, Options{Gate: g},
		Spec{Kind: "scan", Need: func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
			Run: func(context.Context, *Run) error { <-release; return nil }},
		Spec{Kind: "meta", Light: true, Need: func(json.RawMessage) tier.Need { return tier.Need{Disks: []string{"/nas"}} },
			Run: func(context.Context, *Run) error { metaRan <- struct{}{}; return nil }})
	defer close(release)
	q.Enqueue(context.Background(), "scan", Opt{})
	waitFor(t, "the heavy job", func() bool { return q.Started("scan") })

	// Held for its disk while a video plays from it…
	g.hold("a video is playing from the same disk")
	q.Enqueue(context.Background(), "meta", Opt{})
	select {
	case <-metaRan:
		t.Fatal("the light job ran while the gate held its disk")
	case <-time.After(50 * time.Millisecond):
	}
	// …and runs beside the heavy job once it may (the heavy lane is still taken).
	g.hold("")
	select {
	case <-metaRan:
	case <-time.After(5 * time.Second):
		t.Fatal("the light job waited for the heavy lane")
	}
}

func TestRetriesThenFails(t *testing.T) {
	var attempts atomic.Int32
	mem := newMem()
	q, _ := start(t, Options{Persist: mem, Backoff: func(int) time.Duration { return time.Millisecond }},
		Spec{Kind: "flaky", MaxAttempts: 3, Run: func(context.Context, *Run) error {
			attempts.Add(1)
			return errors.New("TMDB said 503")
		}},
		Spec{Kind: "broken", Run: func(context.Context, *Run) error {
			return Permanent(errors.New("no such file"))
		}})
	ctx := context.Background()
	j, _ := q.Enqueue(ctx, "flaky", Opt{})
	waitFor(t, "the flaky job to fail", func() bool { return state(q, j.ID) == Failed })
	if n := attempts.Load(); n != 3 {
		t.Fatalf("%d attempts, want 3", n)
	}
	// The queue records the end in memory first and in the store just after, outside
	// its lock: wait for the stored row rather than sample it mid-save (a flake seen in
	// a parallel run).
	waitFor(t, "the stored failure", func() bool { return mem.get(j.ID).State == Failed })
	if r := mem.get(j.ID); r.State != Failed || r.Attempts != 3 || r.Error != "TMDB said 503" {
		t.Fatalf("stored %+v", r)
	}
	b, _ := q.Enqueue(ctx, "broken", Opt{})
	waitFor(t, "the broken job", func() bool { return state(q, b.ID) == Failed })
	if h := q.Snapshot().History[0]; h.ID != b.ID || h.Attempts != 1 {
		t.Fatalf("a permanent error was retried: %+v", h)
	}

	// Retry queues it again as a person's request.
	again, err := q.Retry(ctx, j.ID)
	if err != nil || !again.User || again.Priority != PriorityUser || again.ID == j.ID {
		t.Fatalf("Retry = %+v, %v", again, err)
	}
	if _, err := q.Retry(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Retry of an unknown id = %v", err)
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	q, _ := start(t, Options{Light: 1}, Spec{Kind: "long", Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}, Spec{Kind: "next", Run: func(context.Context, *Run) error { return nil }})
	ctx := context.Background()

	result := make(chan error, 1)
	go func() {
		_, err := q.Do(ctx, "long", Opt{})
		result <- err
	}()
	<-started
	waiting, _ := q.Enqueue(ctx, "next", Opt{})
	// A queued job is dropped.
	if err := q.Cancel(waiting.ID); err != nil {
		t.Fatal(err)
	}
	if s := state(q, waiting.ID); s != Cancelled {
		t.Fatalf("queued job after Cancel: %q", s)
	}
	// A running one is stopped, and whoever waits for it is told.
	running := q.Snapshot().Running[0]
	if err := q.Cancel(running.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("Do = %v, want ErrCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not stop the running job")
	}
	if s := state(q, running.ID); s != Cancelled {
		t.Fatalf("running job after Cancel: %q (a cancel is not a failure to retry)", s)
	}
	if err := q.Cancel(running.ID); !errors.Is(err, ErrState) {
		t.Fatalf("Cancel of a finished job = %v, want ErrState", err)
	}
	if err := q.Cancel(424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Cancel of an unknown job = %v", err)
	}
}

// Review finding 1: a full scan that playback holds (it reads the disk a film
// plays from) must not keep the heavy lane from the watcher's small scan, which
// playback does not hold.
func TestAHeldJobDoesNotTakeTheLaneFromOneThatMayRun(t *testing.T) {
	g := newGate()
	g.only = func(n tier.Need) bool { return len(n.Disks) > 0 } // the same-disk rule
	g.hold("a video is playing from the same disk")
	dirsRan := make(chan struct{})
	q, _ := start(t, Options{Gate: g},
		Spec{Kind: "scan.full", Priority: PriorityUser, // first in line
			Need: func(json.RawMessage) tier.Need { return tier.Need{CPU: true, Disks: []string{"/nas"}} },
			Run:  func(context.Context, *Run) error { return nil }},
		Spec{Kind: "scan.dirs", Priority: PriorityDirs,
			Need: func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
			Run:  func(context.Context, *Run) error { close(dirsRan); return nil }})
	full, _ := q.Enqueue(context.Background(), "scan.full", Opt{})
	q.Enqueue(context.Background(), "scan.dirs", Opt{})
	select {
	case <-dirsRan:
	case <-time.After(5 * time.Second):
		t.Fatal("the held full scan kept the heavy lane: the small scan never ran")
	}
	// The full scan still waits, and says why; it is next of its lane, so listed with the running.
	waitFor(t, "the held reason", func() bool {
		for _, j := range q.Snapshot().Running {
			if j.ID == full.ID && j.State == Queued && strings.Contains(j.Held, "same disk") {
				return true
			}
		}
		return false
	})
	g.hold("")
	waitFor(t, "the full scan", func() bool { return state(q, full.ID) == Done })
}

// Review finding 2: Do returns the job's own error, so a caller can tell a
// "try again" error from the others (the watcher retries ErrNotScanned).
func TestDoKeepsTheErrorItself(t *testing.T) {
	sentinel := errors.New("not scanned: the database is busy")
	q, _ := start(t, Options{}, Spec{Kind: "dirs", Run: func(context.Context, *Run) error {
		return fmt.Errorf("scanning: %w", sentinel)
	}})
	_, err := q.Do(context.Background(), "dirs", Opt{Attempts: 1})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do = %v: errors.Is lost the job's error", err)
	}
}

// Review finding 3: the job a stopped run left queued and the one this run's
// schedule queued before Run (same kind and key) are one job, not two.
func TestARestartDoesNotRunAKeyedJobTwice(t *testing.T) {
	mem := newMem()
	mem.InsertJob(context.Background(), Record{Kind: "scan", Key: "full", State: Queued, CreatedAt: time.Now()})
	var runs atomic.Int32
	q := New(Options{Persist: mem})
	q.Register(Spec{Kind: "scan", Run: func(context.Context, *Run) error { runs.Add(1); return nil }})
	q.SetPaused(context.Background(), true)
	q.Enqueue(context.Background(), "scan", Opt{Key: "full"}) // the schedule, before Run
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	<-q.Ready()
	if n := len(q.Snapshot().Queued); n != 1 {
		t.Fatalf("%d scans queued after the restart, want 1", n)
	}
	q.SetPaused(context.Background(), false)
	waitFor(t, "the scan", func() bool { return !q.Busy("") })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("ran %d times", n)
	}
}

// Review finding 4: enqueues of one key from many goroutines at once make one job.
func TestConcurrentEnqueuesOfAKeyMakeOneJob(t *testing.T) {
	mem := &slowInsertMem{memPersist: newMem()} // the window: an insert takes a while (SQLite)
	q, _ := start(t, Options{Persist: mem}, Spec{Kind: "match", Run: func(context.Context, *Run) error { return nil }})
	_ = q.SetPaused(context.Background(), true)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Enqueue(context.Background(), "match", Opt{Key: "match"})
		}()
	}
	wg.Wait()
	if n := len(q.Snapshot().Queued); n != 1 {
		t.Fatalf("%d jobs for one key", n)
	}
}

// Review finding 7: event ids go on growing across a restart, so a browser that
// reconnects with an id of the old process gets a snapshot, not a replay of
// unrelated events.
func TestAnIdFromAnotherProcessResyncs(t *testing.T) {
	old := newHub(16, time.Unix(1_800_000_000, 0))
	old.publish(Event{Type: "job.queued"})
	stale := old.last()
	now := newHub(16, time.Unix(1_800_000_060, 0)) // ply restarted a minute later
	if _, ok, _, cancel := now.subscribe(stale); ok {
		cancel()
		t.Fatal("an id from the old process, before any new event: no snapshot")
	}
	for range 3 {
		now.publish(Event{Type: "job.progress"})
	}
	if _, ok, _, cancel := now.subscribe(stale); ok {
		cancel()
		t.Fatal("an id from the old process replayed the new one's events")
	}
	// Positive control: an id of this process replays.
	mine := now.last() - 1
	missed, ok, _, cancel := now.subscribe(mine)
	cancel()
	if !ok || len(missed) != 1 {
		t.Fatalf("own id: ok=%v missed=%d", ok, len(missed))
	}
}

func TestTheGateHoldsJobsAndSaysWhy(t *testing.T) {
	g := newGate()
	g.hold("a viewer is waiting for video")
	ran := make(chan struct{})
	q, _ := start(t, Options{Gate: g}, Spec{Kind: "scan", Run: func(context.Context, *Run) error {
		close(ran)
		return nil
	}})
	j, _ := q.Enqueue(context.Background(), "scan", Opt{})
	waitFor(t, "the job to be held", func() bool {
		r := q.Snapshot().Running
		return len(r) == 1 && r[0].Held != ""
	})
	held := q.Snapshot().Running[0]
	if held.State != Queued || !strings.Contains(held.Held, "waiting for video") || !held.StartedAt.IsZero() {
		t.Fatalf("held job = %+v: want queued, with the reason, not started", held)
	}
	select {
	case <-ran:
		t.Fatal("ran while the gate held it")
	case <-time.After(30 * time.Millisecond):
	}
	g.hold("")
	<-ran
	waitFor(t, "done", func() bool { return state(q, j.ID) == Done })
	if h := q.Snapshot().History[0]; h.Held != "" {
		t.Fatalf("finished job still says held: %q", h.Held)
	}
}

func TestYieldWaitsBetweenSteps(t *testing.T) {
	g := newGate()
	step := make(chan int, 10)
	proceed := make(chan struct{})
	q, _ := start(t, Options{Gate: g}, Spec{Kind: "scan", Run: func(ctx context.Context, r *Run) error {
		for i := range 3 {
			if err := r.Yield(ctx); err != nil {
				return err
			}
			r.Progress(i+1, 3, "file")
			step <- i
			if i == 0 {
				<-proceed
			}
		}
		return nil
	}})
	j, _ := q.Enqueue(context.Background(), "scan", Opt{})
	<-step
	g.hold("someone is browsing") // playback or browsing comes along mid-job
	close(proceed)
	select {
	case <-step:
		t.Fatal("the next step ran while held")
	case <-time.After(30 * time.Millisecond):
	}
	if r := q.Snapshot().Running; len(r) != 1 || r[0].State != Running || r[0].Held != "someone is browsing" {
		t.Fatalf("running job while held: %+v", r)
	}
	g.hold("")
	waitFor(t, "done", func() bool { return state(q, j.ID) == Done })
	if h := q.Snapshot().History[0]; h.Done != 3 || h.Total != 3 {
		t.Fatalf("progress = %d/%d", h.Done, h.Total)
	}
}

func TestHeavyJobsRunOneAtATimeBesideLightOnes(t *testing.T) {
	var heavy, light, maxHeavy atomic.Int32
	release := make(chan struct{})
	track := func(c *atomic.Int32, max *atomic.Int32) func(context.Context, *Run) error {
		return func(context.Context, *Run) error {
			n := c.Add(1)
			defer c.Add(-1)
			if max != nil && n > max.Load() {
				max.Store(n)
			}
			<-release
			return nil
		}
	}
	q, _ := start(t, Options{},
		Spec{Kind: "probe", Need: func(json.RawMessage) tier.Need { return tier.Need{CPU: true} }, Run: track(&heavy, &maxHeavy)},
		Spec{Kind: "match", Run: track(&light, nil)})
	ctx := context.Background()
	for range 3 {
		q.Enqueue(ctx, "probe", Opt{})
	}
	q.Enqueue(ctx, "match", Opt{})
	waitFor(t, "a heavy and a light job", func() bool { return heavy.Load() == 1 && light.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if heavy.Load() != 1 {
		t.Fatalf("%d heavy jobs at once", heavy.Load())
	}
	close(release)
	waitFor(t, "all", func() bool { return !q.Busy("") })
	if maxHeavy.Load() != 1 {
		t.Fatalf("max heavy at once = %d", maxHeavy.Load())
	}
}

func TestARunningJobStartsAgainAfterARestart(t *testing.T) {
	mem := newMem()
	started := make(chan struct{})
	block := Spec{Kind: "scan", Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	q, stop := start(t, Options{Persist: mem}, block)
	j, _ := q.Enqueue(context.Background(), "scan", Opt{Payload: "/nas"})
	<-started
	stop() // ply stops while it runs
	if r := mem.get(j.ID); r.State != Queued {
		t.Fatalf("after shutdown the job is %q, want queued for the next start", r.State)
	}
	// An unknown kind left by another version fails instead of waiting for ever.
	mem.InsertJob(context.Background(), Record{Kind: "gone.kind", State: Queued, CreatedAt: time.Now()})

	var got atomic.Value
	q2, _ := start(t, Options{Persist: mem}, Spec{Kind: "scan", Run: func(ctx context.Context, r *Run) error {
		var p string
		_ = r.Decode(&p)
		got.Store(p)
		return nil
	}})
	waitFor(t, "the job to run again", func() bool { return state(q2, j.ID) == Done })
	if got.Load() != "/nas" {
		t.Fatalf("payload after restart = %v", got.Load())
	}
	var unknown *Job
	for _, h := range q2.Snapshot().History {
		if h.Kind == "gone.kind" {
			unknown = &h
		}
	}
	if unknown == nil || unknown.State != Failed {
		t.Fatalf("unknown kind after restart: %+v", unknown)
	}
}

// A pause that comes while the queue is still loading is not undone by the
// stored switch it then reads.
func TestAPauseDuringLoadStands(t *testing.T) {
	mem := &slowPausedMem{memPersist: newMem(), read: make(chan struct{}), proceed: make(chan struct{})}
	q := New(Options{Persist: mem})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	<-mem.read // load has read "not paused"
	if err := q.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	close(mem.proceed)
	<-q.Ready()
	if !q.Snapshot().Paused {
		t.Fatal("the load overwrote a pause made while it ran")
	}
}

// slowInsertMem takes its time to insert a job.
type slowInsertMem struct{ *memPersist }

func (m *slowInsertMem) InsertJob(ctx context.Context, r Record) (int64, error) {
	time.Sleep(2 * time.Millisecond)
	return m.memPersist.InsertJob(ctx, r)
}

// slowPausedMem answers "not paused", then waits before the load goes on.
type slowPausedMem struct {
	*memPersist
	read, proceed chan struct{}
}

func (m *slowPausedMem) QueuePaused(ctx context.Context) (bool, error) {
	close(m.read)
	<-m.proceed
	return false, nil
}

// Once Run has returned (ply is stopping), an enqueue is refused instead of
// writing to a store that is being closed.
func TestNoEnqueueAfterTheQueueStopped(t *testing.T) {
	mem := newMem()
	q, stop := start(t, Options{Persist: mem}, Spec{Kind: "x", Run: func(context.Context, *Run) error { return nil }})
	_ = q.SetPaused(context.Background(), true)
	if _, err := q.Enqueue(context.Background(), "x", Opt{}); err != nil { // positive control
		t.Fatalf("enqueue while running: %v", err)
	}
	stop()
	mem.mu.Lock()
	before := len(mem.rows)
	mem.mu.Unlock()
	if _, err := q.Enqueue(context.Background(), "x", Opt{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("enqueue after stop = %v, want ErrClosed", err)
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if len(mem.rows) != before {
		t.Fatal("a refused enqueue still wrote to the store")
	}
}

func TestPausedSurvivesARestart(t *testing.T) {
	mem := newMem()
	q, stop := start(t, Options{Persist: mem})
	_ = q.SetPaused(context.Background(), true)
	stop()
	q2, _ := start(t, Options{Persist: mem})
	if !q2.Snapshot().Paused {
		t.Fatal("pause was lost")
	}
}

// Progress is reported before each step, so the last report says total-1 of total:
// a job that ends well shows all of it done; one that fails keeps where it stopped.
func TestAFinishedJobShowsAllItsSteps(t *testing.T) {
	q, _ := start(t, Options{Backoff: func(int) time.Duration { return time.Hour }},
		Spec{Kind: "ok", Run: func(ctx context.Context, r *Run) error {
			for i := range 3 {
				r.Progress(i, 3, "file")
			}
			return nil
		}},
		Spec{Kind: "bad", MaxAttempts: 1, Run: func(ctx context.Context, r *Run) error {
			r.Progress(1, 3, "file")
			return errors.New("disk gone")
		}})
	ok, _ := q.Enqueue(context.Background(), "ok", Opt{})
	bad, _ := q.Enqueue(context.Background(), "bad", Opt{})
	waitFor(t, "both", func() bool { return state(q, ok.ID) == Done && state(q, bad.ID) == Failed })
	for _, h := range q.Snapshot().History {
		switch h.ID {
		case ok.ID:
			if h.Done != 3 || h.Total != 3 {
				t.Errorf("done job shows %d of %d", h.Done, h.Total)
			}
		case bad.ID:
			if h.Done != 1 || h.Total != 3 {
				t.Errorf("failed job shows %d of %d, want where it stopped", h.Done, h.Total)
			}
		}
	}
}

func TestDoReturnsTheResult(t *testing.T) {
	q, _ := start(t, Options{}, Spec{Kind: "dirs", Run: func(ctx context.Context, r *Run) error {
		r.SetResult(42)
		return nil
	}})
	v, err := q.Do(context.Background(), "dirs", Opt{Attempts: 1})
	if err != nil || v != 42 {
		t.Fatalf("Do = %v, %v", v, err)
	}
	if _, err := q.Do(context.Background(), "nope", Opt{}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestAPanicFailsTheJobNotTheQueue(t *testing.T) {
	q, _ := start(t, Options{}, Spec{Kind: "bad", Run: func(context.Context, *Run) error { panic("boom") }},
		Spec{Kind: "good", Run: func(context.Context, *Run) error { return nil }})
	b, _ := q.Enqueue(context.Background(), "bad", Opt{})
	g, _ := q.Enqueue(context.Background(), "good", Opt{})
	waitFor(t, "both", func() bool { return state(q, b.ID) == Failed && state(q, g.ID) == Done })
}

func TestBump(t *testing.T) {
	rec := &recorder{}
	q, _ := start(t, Options{Light: 1}, rec.spec("x", PriorityCache))
	ctx := context.Background()
	_ = q.SetPaused(ctx, true)
	q.Enqueue(ctx, "x", Opt{Payload: "first"})
	last, _ := q.Enqueue(ctx, "x", Opt{Payload: "last"})
	if err := q.Bump(last.ID); err != nil {
		t.Fatal(err)
	}
	_ = q.SetPaused(ctx, false)
	waitFor(t, "both", func() bool { return len(rec.got()) == 2 })
	if got := rec.got(); got[0] != "last" {
		t.Fatalf("order after bump = %v", got)
	}
}

func TestEventsAndResync(t *testing.T) {
	q, _ := start(t, Options{}, Spec{Kind: "x", Run: func(ctx context.Context, r *Run) error {
		r.Progress(1, 1, "")
		return nil
	}})
	since := q.LastSeq()
	missed, resync, ch, cancel := q.Subscribe(since)
	defer cancel()
	if resync || len(missed) != 0 {
		t.Fatalf("subscribe at the newest: resync=%v missed=%d", resync, len(missed))
	}
	j, _ := q.Enqueue(context.Background(), "x", Opt{})
	var types []string
	timeout := time.After(5 * time.Second)
	for !slices.Contains(types, "job.done") {
		select {
		case e := <-ch:
			if e.Job != nil && e.Job.ID == j.ID {
				types = append(types, e.Type)
			}
		case <-timeout:
			t.Fatalf("events so far: %v", types)
		}
	}
	if types[0] != "job.queued" || !slices.Contains(types, "job.started") {
		t.Fatalf("events = %v", types)
	}
	// A client that reconnects gets what it missed; one too far behind resyncs.
	missed, resync, _, cancel2 := q.Subscribe(since)
	defer cancel2()
	if resync || len(missed) < 3 {
		t.Fatalf("replay: resync=%v missed=%d", resync, len(missed))
	}
	_, resync, _, cancel3 := q.Subscribe(0)
	defer cancel3()
	if !resync {
		t.Fatal("a new client must start from a snapshot")
	}
}
