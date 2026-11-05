package jobs

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"ply/internal/tier"
)

// Options configures a Queue.
type Options struct {
	Persist Persist // nil: kept in memory only
	Gate    Gate    // nil: nothing holds the queue
	Logger  *slog.Logger
	// Heavy and Light are how many jobs of each class run at once (defaults 1 and
	// 2). Heavy jobs use the CPU or read a disk end to end (their Need says so);
	// light ones mostly wait on the network.
	Heavy, Light int
	// History is how many finished jobs are shown (default 200); they are kept for
	// Keep (default 7 days).
	History int
	Keep    time.Duration
	// Backoff is the wait before attempt n+1 of a failed job (default 1 min, doubling, at most 30 min).
	Backoff func(attempt int) time.Duration
	Now     func() time.Time
	// Extra lists work that runs outside the queue but belongs on the Activity
	// page (the scanner reading a folder that was just added), given the jobs
	// running in the queue. Its jobs have negative ids and can't be acted on. It is
	// called without the queue's lock, about once a second.
	Extra func(running []Job) []Job
}

// Queue runs jobs by priority, below playback and browsing. Safe for concurrent use.
//
// A job takes a slot of its lane (heavy or light) only once the gate lets it
// start: a job that playback holds does not keep the lane from a job that
// playback does not hold. The gate is asked again between the job's steps
// (Run.Yield).
type Queue struct {
	o     Options
	log   *slog.Logger
	specs map[string]*Spec
	hub   *hub
	enqMu sync.Mutex // one enqueue at a time: the merge check and the insert are one step
	ready chan struct{}

	mu        sync.Mutex
	live      map[int64]*entry // queued and running
	history   []Job            // newest first
	paused    bool
	pauseSet  bool         // SetPaused was called: the stored switch must not override it at load
	running   map[bool]int // started jobs, by heavy
	wake      chan struct{}
	retryC    <-chan time.Time // the earliest retry that is not due yet
	nextTemp  int64            // ids for a queue without Persist
	lastTier  tier.State
	lastExtra string // what Extra said last, to publish a change
}

type runKey struct{}

// RunFrom returns the job a context belongs to (the ctx given to Spec.Run, or one
// derived from it), or nil. Code that a job calls deep down (the scanner's pace
// function) uses it to report progress and to yield.
func RunFrom(ctx context.Context) *Run {
	r, _ := ctx.Value(runKey{}).(*Run)
	return r
}

type entry struct {
	Job
	spec    *Spec
	need    tier.Need // what it needs to start (Spec.Need of its payload)
	heavy   bool
	started bool // given a slot and a goroutine
	cancel  context.CancelCauseFunc
	// cancelled is set by a Cancel that came before the worker had set cancel.
	cancelled bool
	// browsedSince is when browsing alone began to hold it from starting.
	browsedSince time.Time
	result       any
	err          error // what the last run returned, for the callers of Do
	waiters      []chan outcome
	lastPub      time.Time
}

type outcome struct {
	result any
	err    error
}

// New returns a Queue. Register the kinds, then call Run.
func New(o Options) *Queue {
	if o.Heavy <= 0 {
		o.Heavy = 1
	}
	if o.Light <= 0 {
		o.Light = 2
	}
	if o.History <= 0 {
		o.History = 200
	}
	if o.Keep <= 0 {
		o.Keep = 7 * 24 * time.Hour
	}
	if o.Backoff == nil {
		o.Backoff = func(n int) time.Duration {
			d := time.Minute << max(n-1, 0)
			return min(d, 30*time.Minute)
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Queue{o: o, log: log, specs: map[string]*Spec{}, hub: newHub(512, o.Now()), live: map[int64]*entry{},
		running: map[bool]int{}, wake: make(chan struct{}, 1), ready: make(chan struct{})}
}

// Register declares a kind. It must be called before Run.
func (q *Queue) Register(s Spec) {
	if s.MaxAttempts <= 0 {
		s.MaxAttempts = 5
	}
	q.specs[s.Kind] = &s
}

// Ready is closed once Run has loaded what the last run left.
func (q *Queue) Ready() <-chan struct{} { return q.ready }

// Opt are the options of an enqueue.
type Opt struct {
	// Key identifies the job among its kind: one with the key of a queued job
	// is merged into it instead of queued twice. A job whose key is running waits
	// for it to finish. "" never merges.
	Key      string
	Payload  any
	Priority *int // default: the kind's
	User     bool // a person asked for it: priority 0 unless Priority says otherwise
	// Attempts overrides the kind's MaxAttempts (1: no retries, for a caller that waits).
	Attempts int
}

// Enqueue adds a job, or merges it into the queued job of the same kind and key.
func (q *Queue) Enqueue(ctx context.Context, kind string, o Opt) (Job, error) {
	return q.enqueue(ctx, kind, o, nil)
}

// Do enqueues a job and waits for it to finish, returning what its Run set with
// SetResult and the error it returned (as returned: errors.Is works on it).
// Merged into a queued job, it waits for that one.
func (q *Queue) Do(ctx context.Context, kind string, o Opt) (any, error) {
	ch := make(chan outcome, 1)
	if _, err := q.enqueue(ctx, kind, o, ch); err != nil {
		return nil, err
	}
	select {
	case out := <-ch:
		return out.result, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *Queue) enqueue(ctx context.Context, kind string, o Opt, waiter chan outcome) (Job, error) {
	spec, ok := q.specs[kind]
	if !ok {
		return Job{}, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	var payload json.RawMessage
	if o.Payload != nil {
		b, err := json.Marshal(o.Payload)
		if err != nil {
			return Job{}, fmt.Errorf("jobs: %s payload: %w", kind, err)
		}
		payload = b
	}
	prio := spec.Priority
	if o.User {
		prio = PriorityUser
	}
	if o.Priority != nil {
		prio = *o.Priority
	}

	q.enqMu.Lock()
	defer q.enqMu.Unlock()
	q.mu.Lock()
	if o.Key != "" {
		if e := q.queuedWithKey(kind, o.Key); e != nil {
			if spec.Merge != nil {
				e.Payload = spec.Merge(e.Payload, payload)
			} else if payload != nil {
				e.Payload = payload
			}
			e.Priority = min(e.Priority, prio)
			e.User = e.User || o.User
			e.Title = spec.title(e.Payload)
			e.setNeed()
			if waiter != nil {
				e.waiters = append(e.waiters, waiter)
			}
			j := e.Job
			q.mu.Unlock()
			q.save(ctx, j)
			q.publish(Event{Type: "job.updated", Job: &j})
			q.poke()
			return j, nil
		}
	}
	q.mu.Unlock()

	e := &entry{Job: Job{Kind: kind, Key: o.Key, Title: spec.title(payload), Priority: prio, State: Queued, User: o.User,
		Payload: payload, CreatedAt: q.o.Now()}, spec: spec}
	if o.Attempts > 0 {
		s := *spec
		s.MaxAttempts = o.Attempts
		e.spec = &s
	}
	e.setNeed()
	if waiter != nil {
		e.waiters = append(e.waiters, waiter)
	}
	id, err := q.insert(ctx, e.Job)
	if err != nil {
		return Job{}, err
	}
	q.mu.Lock()
	e.ID = id
	q.live[id] = e
	j := e.Job
	q.mu.Unlock()
	q.publish(Event{Type: "job.queued", Job: &j})
	q.poke()
	return j, nil
}

// queuedWithKey is the queued (not started) job of kind with key, or nil. q.mu is held.
func (q *Queue) queuedWithKey(kind, key string) *entry {
	for _, e := range q.live {
		if e.Kind == kind && e.Key == key && !e.started {
			return e
		}
	}
	return nil
}

func (e *entry) setNeed() {
	e.need = e.spec.need(e.Payload)
	e.heavy = !e.spec.Light && (e.need.CPU || len(e.need.Disks) > 0)
}

func (q *Queue) insert(ctx context.Context, j Job) (int64, error) {
	if q.o.Persist == nil {
		q.mu.Lock()
		q.nextTemp++
		id := q.nextTemp
		q.mu.Unlock()
		return id, nil
	}
	id, err := q.o.Persist.InsertJob(context.WithoutCancel(ctx), j.record())
	if err != nil {
		return 0, fmt.Errorf("jobs: saving a %s job: %w", j.Kind, err)
	}
	return id, nil
}

// save writes a job's state; a failure is logged, not returned: the queue in
// memory is the truth while ply runs, the table is for the next start.
func (q *Queue) save(ctx context.Context, j Job) {
	if q.o.Persist == nil {
		return
	}
	if err := q.o.Persist.UpdateJob(context.WithoutCancel(ctx), j.record()); err != nil {
		q.log.Warn("jobs: saving state", "job", j.ID, "kind", j.Kind, "err", err)
	}
}

func (q *Queue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run loads what the last run left, then dispatches jobs until ctx ends. Jobs
// running then are stopped and queued again for the next start.
func (q *Queue) Run(ctx context.Context) {
	q.load(ctx)
	close(q.ready)
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	tierTick := time.NewTicker(time.Second)
	defer tierTick.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		var held bool
		for {
			e, wait, h := q.next()
			held = held || h
			if e == nil {
				q.armTimer(wait)
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				q.work(ctx, e)
			}()
		}
		// A job the gate holds is looked at again when playback changes, or at
		// the gate's poll (browsing ends with no event).
		var gateC <-chan struct{}
		var poll *time.Timer
		var pollC <-chan time.Time
		if held && q.o.Gate != nil {
			gateC = q.o.Gate.Changed()
			poll = time.NewTimer(q.o.Gate.Poll())
			pollC = poll.C
		}
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-q.timerC():
		case <-gateC:
		case <-pollC:
		case <-prune.C:
			q.prune(ctx)
		case <-tierTick.C:
			q.publishTier()
		}
		if poll != nil {
			poll.Stop()
		}
	}
}

// timer for the earliest retry that is not due yet.
var noTimer = make(chan time.Time)

func (q *Queue) armTimer(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if d <= 0 {
		q.retryC = nil
		return
	}
	q.retryC = time.After(d)
}

func (q *Queue) timerC() <-chan time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.retryC == nil {
		return noTimer
	}
	return q.retryC
}

func (q *Queue) load(ctx context.Context) {
	defer q.publish(Event{Type: "snapshot"})
	if q.o.Persist == nil {
		return
	}
	if p, err := q.o.Persist.QueuePaused(ctx); err == nil {
		q.mu.Lock()
		if !q.pauseSet { // a pause or resume that came in meanwhile is newer
			q.paused = p
		}
		q.mu.Unlock()
	}
	q.prune(ctx)
	recs, err := q.o.Persist.LoadJobs(ctx, q.o.History)
	if err != nil {
		q.log.Warn("jobs: loading the queue", "err", err)
	}
	q.enqMu.Lock() // no enqueue between the duplicate check and the add
	defer q.enqMu.Unlock()
	for _, r := range recs {
		j := fromRecord(r)
		q.mu.Lock()
		_, known := q.live[j.ID] // enqueued by this run before Run was called
		q.mu.Unlock()
		if known {
			continue
		}
		if j.State.Finished() {
			q.mu.Lock()
			q.history = append(q.history, j)
			q.mu.Unlock()
			continue
		}
		spec, ok := q.specs[j.Kind]
		if !ok {
			q.end(ctx, j, Failed, "unknown kind of job (from another version of ply)")
			continue
		}
		q.mu.Lock()
		dup := j.Key != "" && q.queuedWithKey(j.Kind, j.Key) != nil
		q.mu.Unlock()
		if dup {
			// This run already queued the same work (its schedule): one is enough.
			q.end(ctx, j, Cancelled, "")
			continue
		}
		// A job that was running when ply stopped starts again.
		j.State, j.StartedAt = Queued, time.Time{}
		e := &entry{Job: j, spec: spec}
		e.setNeed()
		q.mu.Lock()
		q.live[j.ID] = e
		q.mu.Unlock()
	}
	q.mu.Lock()
	slices.SortFunc(q.history, func(a, b Job) int { return b.FinishedAt.Compare(a.FinishedAt) })
	q.mu.Unlock()
}

// end records a job that the load finishes at once.
func (q *Queue) end(ctx context.Context, j Job, s State, why string) {
	j.State, j.Error, j.FinishedAt = s, why, q.o.Now()
	q.save(ctx, j)
	q.mu.Lock()
	q.history = append(q.history, j)
	q.mu.Unlock()
}

// next takes the job to start, if any. Otherwise it says how long until a retry
// is due (0: none) and whether a job is waiting only for the gate.
//
// The candidates are asked of the gate in order, outside the queue's lock (the
// first look at a folder's disk is a stat). The first one the gate lets through
// starts; the first held one of each lane shows why it waits (Job.Held). Browsing
// alone holds a job for at most the gate's BrowseMaxHold.
func (q *Queue) next() (start *entry, due time.Duration, held bool) {
	cands, due, changed := q.candidates()
	now := q.o.Now()
	shown := map[bool]bool{} // a lane's first held job has been given its reason
	defer func() {
		for i := range changed {
			q.publish(Event{Type: "job.held", Job: &changed[i]})
		}
	}()
	for _, e := range cands {
		h := tier.Hold{}
		if q.o.Gate != nil {
			h = q.o.Gate.Check(e.need)
		}
		q.mu.Lock()
		if h.Browsing {
			if e.browsedSince.IsZero() {
				e.browsedSince = now
			} else if now.Sub(e.browsedSince) >= q.o.Gate.BrowseMaxHold() {
				h = tier.Hold{} // browsing that never stops: let it start
			}
		} else {
			e.browsedSince = time.Time{}
		}
		if h.Held {
			held = true
			reason := ""
			if !shown[e.heavy] {
				shown[e.heavy], reason = true, h.Reason
			}
			if e.Held != reason {
				e.Held = reason
				changed = append(changed, e.Job)
			}
			q.mu.Unlock()
			continue
		}
		// Still free to start? (A Cancel, or another start, may have come between.)
		if e.started || q.live[e.ID] != e || q.running[e.heavy] >= q.cap(e.heavy) || q.keyBusy(e) {
			q.mu.Unlock()
			continue
		}
		e.started, e.Held, e.browsedSince = true, "", time.Time{}
		q.running[e.heavy]++
		q.mu.Unlock()
		return e, 0, held
	}
	return nil, due, held
}

// candidates are the jobs that could start now (due, lane free, key not
// running), best first, and how long until the next retry is due. A queued job
// that is no candidate no longer waits for the gate: its Held is cleared (the
// cleared ones are returned, to publish).
func (q *Queue) candidates() (out []*entry, due time.Duration, cleared []Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.o.Now()
	for _, e := range q.live {
		if e.started {
			continue
		}
		if q.paused || e.RunAt.After(now) || q.running[e.heavy] >= q.cap(e.heavy) || q.keyBusy(e) {
			if !q.paused && e.RunAt.After(now) {
				if d := e.RunAt.Sub(now); due == 0 || d < due {
					due = d
				}
			}
			if e.Held != "" {
				e.Held = ""
				cleared = append(cleared, e.Job)
			}
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, compare)
	return out, due, cleared
}

// keyBusy reports whether a job with e's kind and key is running. q.mu is held.
func (q *Queue) keyBusy(e *entry) bool {
	if e.Key == "" {
		return false
	}
	for _, o := range q.live {
		if o != e && o.started && o.Kind == e.Kind && o.Key == e.Key {
			return true
		}
	}
	return false
}

func (q *Queue) cap(heavy bool) int {
	if heavy {
		return q.o.Heavy
	}
	return q.o.Light
}

// compare orders the queue: priority, then age, then id.
func compare(a, b *entry) int {
	if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// work runs one job and records how it ended.
func (q *Queue) work(parent context.Context, e *entry) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	q.mu.Lock()
	e.cancel = cancel
	if e.cancelled {
		cancel(ErrCancelled)
	}
	e.State, e.StartedAt, e.Error = Running, q.o.Now(), ""
	e.Attempts++
	e.Done, e.Total, e.Note = 0, 0, ""
	j := e.Job
	yield := e.need
	if e.spec.YieldNeed != nil {
		yield = e.spec.YieldNeed(e.Payload)
	}
	q.mu.Unlock()

	r := &Run{q: q, e: e, need: yield}
	ctx = context.WithValue(ctx, runKey{}, r)
	q.save(ctx, j)
	q.publish(Event{Type: "job.started", Job: &j})
	err := ctx.Err() // cancelled before it began
	if err == nil {
		err = q.call(ctx, e, r)
	}
	q.finish(parent, ctx, e, err)
}

// call runs the job's Run, turning a panic into an error.
func (q *Queue) call(ctx context.Context, e *entry, r *Run) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Permanent(fmt.Errorf("panic: %v", p))
			q.log.Error("jobs: a job panicked", "kind", e.Kind, "panic", p)
		}
	}()
	return e.spec.Run(ctx, r)
}

func (q *Queue) finish(parent, ctx context.Context, e *entry, err error) {
	now := q.o.Now()
	q.mu.Lock()
	q.running[e.heavy]--
	e.started, e.cancel, e.cancelled, e.Held = false, nil, false, ""
	cause := context.Cause(ctx)
	shutdown := parent.Err() != nil && !errors.Is(cause, ErrCancelled)
	var requeue bool
	switch {
	case shutdown:
		// Stopped by the end of ply: it runs again at the next start.
		e.State, e.StartedAt = Queued, time.Time{}
		j := e.Job
		q.mu.Unlock()
		q.save(context.Background(), j)
		return
	case errors.Is(cause, ErrCancelled):
		e.State, e.Error, e.err = Cancelled, "", ErrCancelled
	case err == nil:
		e.State, e.Error, e.err = Done, "", nil
		if e.Total > 0 {
			e.Done = e.Total // progress is told before each step: the last one is done too
		}
	default:
		e.Error, e.err = err.Error(), err
		var p permanent
		if errors.As(err, &p) || e.Attempts >= e.spec.MaxAttempts {
			e.State = Failed
		} else {
			e.State, e.RunAt, e.StartedAt = Queued, now.Add(q.o.Backoff(e.Attempts)), time.Time{}
			requeue = true
		}
	}
	if !requeue {
		e.FinishedAt = now
		delete(q.live, e.ID)
		q.history = append([]Job{e.Job}, q.history...)
		if len(q.history) > q.o.History {
			q.history = q.history[:q.o.History]
		}
	}
	j := e.Job
	waiters := e.waiters
	if !requeue {
		e.waiters = nil
	}
	result, werr := e.result, e.err
	q.mu.Unlock()

	q.save(context.Background(), j)
	typ := "job." + string(j.State)
	if requeue {
		typ = "job.retrying"
		q.log.Warn("jobs: failed, will retry", "kind", j.Kind, "title", j.Title, "attempt", j.Attempts, "err", err)
	} else if j.State == Failed {
		q.log.Warn("jobs: failed", "kind", j.Kind, "title", j.Title, "attempts", j.Attempts, "err", err)
	}
	q.publish(Event{Type: typ, Job: &j})
	if !requeue {
		for _, w := range waiters {
			w <- outcome{result, werr}
		}
	}
	q.poke()
}

// Cancel stops a running job or drops a queued one.
func (q *Queue) Cancel(id int64) error {
	q.mu.Lock()
	e, ok := q.live[id]
	if !ok {
		q.mu.Unlock()
		if q.inHistory(id) {
			return ErrState
		}
		return ErrNotFound
	}
	if e.started {
		cancel := e.cancel
		if cancel == nil {
			e.cancelled = true // work has not set it yet: it cancels itself
		}
		q.mu.Unlock()
		if cancel != nil {
			cancel(ErrCancelled)
		}
		return nil
	}
	e.State, e.FinishedAt, e.Held = Cancelled, q.o.Now(), ""
	delete(q.live, id)
	q.history = append([]Job{e.Job}, q.history...)
	j := e.Job
	waiters := e.waiters
	q.mu.Unlock()
	q.save(context.Background(), j)
	q.publish(Event{Type: "job.cancelled", Job: &j})
	for _, w := range waiters {
		w <- outcome{nil, ErrCancelled}
	}
	q.poke()
	return nil
}

// Bump moves a queued job to the front (a person's priority).
func (q *Queue) Bump(id int64) error {
	q.mu.Lock()
	e, ok := q.live[id]
	if !ok || e.started {
		q.mu.Unlock()
		if ok || q.inHistory(id) {
			return ErrState
		}
		return ErrNotFound
	}
	e.Priority, e.User, e.RunAt = PriorityUser, true, time.Time{}
	j := e.Job
	q.mu.Unlock()
	q.save(context.Background(), j)
	q.publish(Event{Type: "job.updated", Job: &j})
	q.poke()
	return nil
}

// Retry queues a finished job again, as a person's request.
func (q *Queue) Retry(ctx context.Context, id int64) (Job, error) {
	q.mu.Lock()
	var old *Job
	for i := range q.history {
		if q.history[i].ID == id {
			old = &q.history[i]
			break
		}
	}
	if old == nil {
		_, live := q.live[id]
		q.mu.Unlock()
		if live {
			return Job{}, ErrState
		}
		return Job{}, ErrNotFound
	}
	kind, key, payload := old.Kind, old.Key, old.Payload
	q.mu.Unlock()
	return q.Enqueue(ctx, kind, Opt{Key: key, Payload: json.RawMessage(payload), User: true})
}

func (q *Queue) inHistory(id int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.history {
		if j.ID == id {
			return true
		}
	}
	return false
}

// SetPaused pauses or resumes the queue. Running jobs finish; nothing new starts.
func (q *Queue) SetPaused(ctx context.Context, paused bool) error {
	if q.o.Persist != nil {
		if err := q.o.Persist.SetQueuePaused(ctx, paused); err != nil {
			return err
		}
	}
	q.mu.Lock()
	q.paused, q.pauseSet = paused, true
	q.mu.Unlock()
	typ := "queue.resumed"
	if paused {
		typ = "queue.paused"
	}
	q.publish(Event{Type: typ})
	q.poke()
	return nil
}

// Snapshot is the whole queue, for GET /api/jobs and the first SSE event.
type Snapshot struct {
	Paused bool `json:"paused"`
	// Running are the started jobs, and the next job of each lane while the
	// governor holds it (state queued, Held set).
	Running []Job      `json:"running"`
	Queued  []Job      `json:"queued"`  // in the order they will start
	History []Job      `json:"history"` // newest first
	Tier    tier.State `json:"tier"`
}

// Snapshot returns the queue as it is now, with the Extra work.
func (q *Queue) Snapshot() Snapshot { return q.withExtra(q.snapshot()) }

func (q *Queue) snapshot() Snapshot {
	var t tier.State
	if q.o.Gate != nil {
		t = q.o.Gate.State()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Snapshot{Paused: q.paused, Running: []Job{}, Queued: []Job{}, History: slices.Clone(q.history), Tier: t}
	if s.History == nil {
		s.History = []Job{}
	}
	var queued []*entry
	for _, e := range q.live {
		if e.started || e.Held != "" {
			s.Running = append(s.Running, e.Job)
		} else {
			queued = append(queued, e)
		}
	}
	slices.SortFunc(queued, compare)
	for _, e := range queued {
		s.Queued = append(s.Queued, e.Job)
	}
	slices.SortFunc(s.Running, func(a, b Job) int { return cmp.Compare(a.ID, b.ID) })
	return s
}

// withExtra adds the work outside the queue to s.Running.
func (q *Queue) withExtra(s Snapshot) Snapshot {
	if q.o.Extra != nil {
		s.Running = append(s.Running, q.o.Extra(slices.Clone(s.Running))...)
	}
	return s
}

// Busy reports whether a job of kind is queued or running (any kind for "").
func (q *Queue) Busy(kind string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.live {
		if kind == "" || e.Kind == kind {
			return true
		}
	}
	return false
}

// Started reports whether a job of kind is running.
func (q *Queue) Started(kind string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.live {
		if e.Kind == kind && e.started {
			return true
		}
	}
	return false
}

func (q *Queue) prune(ctx context.Context) {
	if q.o.Persist == nil {
		return
	}
	if _, err := q.o.Persist.PruneJobs(ctx, q.o.Now().Add(-q.o.Keep)); err != nil && ctx.Err() == nil {
		q.log.Warn("jobs: pruning history", "err", err)
	}
}

func (q *Queue) publishTier() {
	if q.o.Extra != nil {
		// Work outside the queue has no events of its own: a change of what it is
		// makes the clients reload the snapshot.
		sig := ""
		for _, j := range q.o.Extra(q.snapshot().Running) {
			sig += fmt.Sprintf("%d|%s|%s|%d/%d;", j.ID, j.Title, j.State, j.Done, j.Total)
		}
		q.mu.Lock()
		changed := sig != q.lastExtra
		q.lastExtra = sig
		q.mu.Unlock()
		if changed {
			q.publish(Event{Type: "snapshot"})
		}
	}
	if q.o.Gate == nil {
		return
	}
	t := q.o.Gate.State()
	q.mu.Lock()
	same := tierEqual(t, q.lastTier)
	q.lastTier = t
	q.mu.Unlock()
	if !same {
		q.publish(Event{Type: "tier", Tier: &t})
	}
}

func tierEqual(a, b tier.State) bool {
	return a.Browsing == b.Browsing && a.Hold == b.Hold && a.Playback.Sessions == b.Playback.Sessions &&
		a.Playback.Active == b.Playback.Active && a.Playback.Transcodes == b.Playback.Transcodes &&
		a.Playback.Software == b.Playback.Software && a.Playback.Waiting == b.Playback.Waiting
}

// Run is a job's handle on itself while it runs.
type Run struct {
	q    *Queue
	e    *entry
	need tier.Need
}

// ID is the job's id.
func (r *Run) ID() int64 { return r.e.ID }

// Decode unmarshals the job's payload into v.
func (r *Run) Decode(v any) error {
	if len(r.e.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(r.e.Payload, v)
}

// Progress reports how far the job is (total 0: not known) and what it is on.
func (r *Run) Progress(done, total int, note string) {
	q := r.q
	q.mu.Lock()
	r.e.Done, r.e.Total, r.e.Note = done, total, note
	now := q.o.Now()
	// At most 4 events a second for a job: a scan reports every file.
	if now.Sub(r.e.lastPub) < 250*time.Millisecond && done != total {
		q.mu.Unlock()
		return
	}
	r.e.lastPub = now
	j := r.e.Job
	q.mu.Unlock()
	q.publish(Event{Type: "job.progress", Job: &j})
}

// Yield waits while playback or browsing needs the machine (for what the job's
// YieldNeed says). Call it between steps.
func (r *Run) Yield(ctx context.Context) error {
	if r.q.o.Gate == nil {
		return ctx.Err()
	}
	return r.q.o.Gate.Wait(ctx, r.need, r.held)
}

// SetResult is what Do returns to the caller that waits for this job.
func (r *Run) SetResult(v any) {
	r.q.mu.Lock()
	r.e.result = v
	r.q.mu.Unlock()
}

func (r *Run) held(reason string) {
	q := r.q
	q.mu.Lock()
	r.e.Held = reason
	j := r.e.Job
	q.mu.Unlock()
	q.publish(Event{Type: "job.held", Job: &j})
}
