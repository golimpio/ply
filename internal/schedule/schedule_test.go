package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/store"
)

// ---- fakes -------------------------------------------------------------------------------------

// memStore is the schedules table in memory.
type memStore struct {
	mu   sync.Mutex
	recs map[string]store.ScheduleRecord
}

func newMemStore(recs ...store.ScheduleRecord) *memStore {
	m := &memStore{recs: map[string]store.ScheduleRecord{}}
	for _, r := range recs {
		m.recs[r.Task] = r
	}
	return m
}

func (m *memStore) Schedules(context.Context) ([]store.ScheduleRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.ScheduleRecord, 0, len(m.recs))
	for _, r := range m.recs {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b store.ScheduleRecord) int { return strings.Compare(a.Task, b.Task) })
	return out, nil
}

func (m *memStore) PutSchedule(_ context.Context, rec store.ScheduleRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[rec.Task] = rec
	return nil
}

func (m *memStore) MarkScheduleRun(_ context.Context, task string, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.recs[task]
	if !ok {
		return store.ErrNotFound
	}
	rec.LastRun = at
	m.recs[task] = rec
	return nil
}

func (m *memStore) get(task string) store.ScheduleRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recs[task]
}

// memQueue is the queue in memory: what was enqueued, and what the test says is busy.
type memQueue struct {
	mu     sync.Mutex
	jobs   []jobs.Job
	busy   map[string]bool
	err    error
	nextID int64
}

func newMemQueue() *memQueue { return &memQueue{busy: map[string]bool{}} }

func (q *memQueue) Enqueue(_ context.Context, kind string, o jobs.Opt) (jobs.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return jobs.Job{}, q.err
	}
	q.nextID++
	j := jobs.Job{ID: q.nextID, Kind: kind, Key: o.Key, User: o.User}
	q.jobs = append(q.jobs, j)
	return j, nil
}

func (q *memQueue) Busy(kind string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.busy[kind]
}

func (q *memQueue) kinds() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.jobs))
	for i, j := range q.jobs {
		out[i] = j.Kind
	}
	return out
}

// ---- the clock and the arithmetic --------------------------------------------------------------

func TestClockParseAndJSON(t *testing.T) {
	for _, s := range []string{"00:00", "02:30", "09:05", "23:59"} {
		c, err := ParseClock(s)
		if err != nil || c.String() != s {
			t.Errorf("ParseClock(%q) = %v, %v", s, c, err)
		}
	}
	for _, s := range []string{"", "2:30", "24:00", "23:60", "12:3", "ab:cd", "12:34:56", " 02:30"} {
		if _, err := ParseClock(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseClock(%q) = %v, want ErrInvalid", s, err)
		}
	}
	b, err := json.Marshal(struct{ At Clock }{MustClock("03:05")})
	if err != nil || string(b) != `{"At":"03:05"}` {
		t.Fatalf("JSON = %s, %v", b, err)
	}
	var e struct{ At Clock }
	if err := json.Unmarshal([]byte(`{"At":"03:05"}`), &e); err != nil || e.At != 3*60+5 {
		t.Fatalf("unmarshal = %v, %v", e.At, err)
	}
	if err := json.Unmarshal([]byte(`{"At":"25:00"}`), &e); !errors.Is(err, ErrInvalid) {
		t.Errorf("25:00 = %v, want ErrInvalid", err)
	}
	if err := json.Unmarshal([]byte(`{"At":150}`), &e); !errors.Is(err, ErrInvalid) {
		t.Errorf("a number = %v, want ErrInvalid", err)
	}
}

func TestNextAtWeekdays(t *testing.T) {
	at := MustClock("02:30")
	// Monday 2026-11-09, 09:00.
	mon := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		days []int
		want time.Time
	}{
		"every day":          {nil, time.Date(2026, 11, 10, 2, 30, 0, 0, time.UTC)},
		"same day next week": {[]int{1}, time.Date(2026, 11, 16, 2, 30, 0, 0, time.UTC)},
		"skip to Wednesday":  {[]int{3}, time.Date(2026, 11, 11, 2, 30, 0, 0, time.UTC)},
		"Mon Wed Fri":        {[]int{1, 3, 5}, time.Date(2026, 11, 11, 2, 30, 0, 0, time.UTC)},
		"Sunday":             {[]int{7}, time.Date(2026, 11, 15, 2, 30, 0, 0, time.UTC)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := nextAt(mon, at, tc.days); !got.Equal(tc.want) {
				t.Errorf("next = %v, want %v", got, tc.want)
			}
		})
	}
	// Strictly after: at 02:30 on the dot, the next occurrence is tomorrow's.
	exact := time.Date(2026, 11, 9, 2, 30, 0, 0, time.UTC)
	if got := nextAt(exact, at, nil); !got.Equal(time.Date(2026, 11, 10, 2, 30, 0, 0, time.UTC)) {
		t.Errorf("on the minute: next = %v, want tomorrow", got)
	}
}

func TestDueAndNextRun(t *testing.T) {
	at := 2*60 + 30
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC) // Monday, 09:00
	never := store.ScheduleRecord{Task: "t", Enabled: true, AtMinute: at}
	if !due(never, now) {
		t.Error("a schedule that never ran is not due at once")
	}
	yesterday := store.ScheduleRecord{Task: "t", Enabled: true, AtMinute: at, LastRun: now.AddDate(0, 0, -1).Unix()}
	if !due(yesterday, now) {
		t.Error("the run it missed while ply was off is not caught up")
	}
	today := store.ScheduleRecord{Task: "t", Enabled: true, AtMinute: at, LastRun: time.Date(2026, 11, 9, 2, 30, 0, 0, time.UTC).Unix()}
	if due(today, now) {
		t.Error("today's run is done, and tomorrow's is not due")
	}
	if got, want := nextRun(today, now), time.Date(2026, 11, 10, 2, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("next = %v, want tomorrow %v", got, want)
	}
	off := today
	off.Enabled = false
	if due(off, now) || !nextRun(off, now).IsZero() {
		t.Error("a schedule that is off is due, or has a next run")
	}
	// A schedule that is due now says so: its next run is now, not the occurrence that passed.
	if got := nextRun(yesterday, now); !got.Equal(now) {
		t.Errorf("next of a due schedule = %v, want now", got)
	}
}

// ---- the scheduler -----------------------------------------------------------------------------

func newScheduler(t *testing.T, st Storage, q Queue, now *time.Time, check Checker, tasks ...Task) (*Scheduler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s, err := New(Options{Store: st, Queue: q, Tasks: tasks, Check: check, Every: time.Hour,
		Logger: slog.New(slog.NewTextHandler(&buf, nil)), Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return s, &buf
}

func scanTask() Task {
	return Task{ID: "scan.full", Key: "full", Title: "Scan the library", Detail: "Walks the folders.",
		DefaultEnabled: true, DefaultAt: MustClock("02:30")}
}

// A task with no row runs at once, with its defaults stored, and once only: the next look finds
// today's run done.
func TestTickRunsANewScheduleOnceAndKeepsIt(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	s, _ := newScheduler(t, st, q, &now, nil, scanTask())
	ctx := context.Background()
	s.tick(ctx)
	if got := q.kinds(); !slices.Equal(got, []string{"scan.full"}) {
		t.Fatalf("queued %v, want the scan once", got)
	}
	rec := st.get("scan.full")
	if !rec.Enabled || rec.AtMinute != 150 || rec.LastRun != now.Unix() {
		t.Errorf("stored %+v, want the defaults and last run now", rec)
	}
	s.tick(ctx)
	if got := q.kinds(); len(got) != 1 {
		t.Errorf("the second look queued again: %v", got)
	}
	// The next night it is due again.
	now = now.AddDate(0, 0, 1)
	s.tick(ctx)
	if got := q.kinds(); len(got) != 2 {
		t.Errorf("the next night queued nothing: %v", got)
	}
	if rec := st.get("scan.full"); rec.LastRun != now.Unix() {
		t.Errorf("last run = %v, want when it ran (%v)", time.Unix(rec.LastRun, 0), now)
	}
}

// A task the checker refuses is not queued, the reason is said once a day, and last_run is left
// alone so that a look after the key is stored runs it at once.
func TestBlockedTaskIsSkippedLoggedOnceAndRunsWhenUnblocked(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	var mu sync.Mutex
	blocked := true
	check := func(context.Context, Task) Status {
		mu.Lock()
		defer mu.Unlock()
		if blocked {
			return Status{Missing: []string{"MDBList key"}, Blocked: "The MDBList key is not set."}
		}
		return Status{}
	}
	task := Task{ID: "ratings.mdblist", Key: "mdblist", Title: "Ratings", DefaultEnabled: true, DefaultAt: MustClock("03:30")}
	s, log := newScheduler(t, st, q, &now, check, task)
	ctx := context.Background()
	s.tick(ctx)
	s.tick(ctx)
	if got := q.kinds(); len(got) != 0 {
		t.Fatalf("a blocked task was queued: %v", got)
	}
	if n := strings.Count(log.String(), "API key is missing"); n != 1 {
		t.Errorf("the missing key is logged %d times, want once for the one occurrence: %s", n, log)
	}
	if !strings.Contains(log.String(), "MDBList key") {
		t.Errorf("the log does not name the key: %s", log)
	}
	if rec := st.get(task.ID); rec.LastRun != 0 {
		t.Errorf("a skipped occurrence was recorded as a run: last run %v", time.Unix(rec.LastRun, 0))
	}

	mu.Lock()
	blocked = false
	mu.Unlock()
	s.tick(ctx) // 09:00: not due, but it could not run at 03:30 and now it can
	if got := q.kinds(); !slices.Equal(got, []string{task.ID}) {
		t.Fatalf("queued %v after the key was added, want the task", got)
	}
	if rec := st.get(task.ID); rec.LastRun != now.Unix() {
		t.Errorf("last run = %v, want now", time.Unix(rec.LastRun, 0))
	}
	s.tick(ctx)
	if got := q.kinds(); len(got) != 1 {
		t.Errorf("it ran twice: %v", got)
	}
}

// A task that has never run waits for the kinds it needs: the first ratings pass is queued after the
// first scan and match, so it rates the library rather than the empty library of a first start.
func TestFirstRunWaitsForItsPrerequisites(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	q.busy["scan.full"] = true
	task := Task{ID: "ratings.mdblist", Key: "mdblist", Title: "Ratings", DefaultEnabled: true, DefaultAt: MustClock("03:30"),
		After: []string{"scan.full"}}
	s, _ := newScheduler(t, st, q, &now, nil, task)
	s.tick(context.Background())
	if got := q.kinds(); len(got) != 0 {
		t.Fatalf("the ratings ran before the scan: %v", got)
	}
	q.busy["scan.full"] = false
	s.tick(context.Background())
	if got := q.kinds(); !slices.Equal(got, []string{"ratings.mdblist"}) {
		t.Fatalf("after the scan queued %v, want the ratings", got)
	}
}

// A task whose job is already queued or running is not queued again, and the look is recorded.
func TestBusyTaskIsNotQueuedAgain(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	q.busy["scan.full"] = true
	s, _ := newScheduler(t, st, q, &now, nil, scanTask())
	s.tick(context.Background())
	if got := q.kinds(); len(got) != 0 {
		t.Errorf("queued while busy: %v", got)
	}
	if rec := st.get("scan.full"); rec.LastRun != now.Unix() {
		t.Error("the run in progress was not recorded, so it would be tried again every tick")
	}
}

// A key that is missing between two runs is not a run that was skipped: when it comes back, nothing
// runs until the task's time. The control below is the task whose time had come.
func TestAKeyMissingBetweenRunsDoesNotMakeOneDue(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC) // Monday
	ran := store.ScheduleRecord{Task: "scan.full", Enabled: true, AtMinute: 150, LastRun: now.Unix()}
	st, q := newMemStore(ran), newMemQueue()
	var mu sync.Mutex
	blocked := true
	check := func(context.Context, Task) Status {
		mu.Lock()
		defer mu.Unlock()
		if blocked {
			return Status{Missing: []string{"A key"}, Blocked: "The key is not set."}
		}
		return Status{}
	}
	s, _ := newScheduler(t, st, q, &now, check, scanTask())
	ctx := context.Background()
	s.tick(ctx) // blocked, but today's run is done
	mu.Lock()
	blocked = false
	mu.Unlock()
	s.tick(ctx) // the key is back: still not due until tomorrow at 02:30
	if got := q.kinds(); len(got) != 0 {
		t.Errorf("a key stored between runs queued %v", got)
	}
	// Control: tomorrow's time finds it due, and it runs.
	now = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	s.tick(ctx)
	if got := q.kinds(); !slices.Equal(got, []string{"scan.full"}) {
		t.Errorf("tomorrow queued %v, want the scan", got)
	}
}

// An enqueue that fails is not recorded as a run: the next look tries again.
func TestFailedEnqueueIsTriedAgain(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	q.err = errors.New("the queue is closed")
	s, _ := newScheduler(t, st, q, &now, nil, scanTask())
	s.tick(context.Background())
	if rec := st.get("scan.full"); rec.LastRun != 0 {
		t.Fatalf("a failed enqueue was recorded as a run: %+v", rec)
	}
	q.mu.Lock()
	q.err = nil
	q.mu.Unlock()
	s.tick(context.Background())
	if got := q.kinds(); !slices.Equal(got, []string{"scan.full"}) {
		t.Errorf("queued %v after the queue came back", got)
	}
}

// A schedule that is off stores nothing and queues nothing.
func TestDisabledScheduleNeverRuns(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	task := scanTask()
	task.DefaultEnabled = false
	s, _ := newScheduler(t, st, q, &now, nil, task)
	s.tick(context.Background())
	s.tick(context.Background())
	if got := q.kinds(); len(got) != 0 {
		t.Errorf("a disabled schedule queued %v", got)
	}
	entries, err := s.List(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Enabled || entries[0].Next != nil {
		t.Fatalf("list = %+v, %v: want off and no next run", entries, err)
	}
}

func TestSetListAndRunNow(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	check := func(_ context.Context, task Task) Status {
		if task.ID != "ratings.mdblist" {
			return Status{}
		}
		return Status{Missing: []string{"MDBList key"}, Blocked: "The MDBList key is not set.", Warnings: []string{"A warning."}}
	}
	tasks := []Task{scanTask(), {ID: "ratings.mdblist", Key: "mdblist", Title: "Ratings", DefaultEnabled: true, DefaultAt: MustClock("03:30")}}
	s, _ := newScheduler(t, st, q, &now, check, tasks...)
	ctx := context.Background()

	entries, err := s.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("list = %+v, %v", entries, err)
	}
	if entries[0].Task != "scan.full" || entries[1].Task != "ratings.mdblist" {
		t.Errorf("list order = %v, want the catalog's", entries)
	}
	if entries[0].Missing == nil || entries[0].Warnings == nil || entries[0].Days == nil {
		t.Errorf("a JSON list would be null: %+v", entries[0])
	}

	off, at, ds := false, MustClock("04:15"), []int{1, 3, 5}
	got, err := s.Set(ctx, "scan.full", Edit{Enabled: &off, At: &at, Days: &ds})
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.At != at || !slices.Equal(got.Days, ds) || got.Next != nil {
		t.Errorf("set = %+v", got)
	}
	if rec := st.get("scan.full"); rec.Enabled || rec.AtMinute != 255 || !slices.Equal(rec.Days, ds) {
		t.Errorf("stored %+v", rec)
	}
	if _, err := s.Set(ctx, "nope", Edit{Enabled: &off}); !errors.Is(err, ErrUnknownTask) {
		t.Errorf("unknown task = %v", err)
	}
	if _, err := s.Set(ctx, "scan.full", Edit{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty edit = %v, want ErrInvalid", err)
	}
	bad := []int{0}
	if _, err := s.Set(ctx, "scan.full", Edit{Days: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("day 0 = %v, want ErrInvalid", err)
	}
	if rec := st.get("scan.full"); rec.AtMinute != 255 {
		t.Error("a refused edit was stored")
	}

	// Run now: refused for the task whose key is missing, with the reason of the page.
	if err := s.RunNow(ctx, "ratings.mdblist"); err == nil {
		t.Error("a blocked task ran now")
	} else {
		var refused *Refused
		if !errors.As(err, &refused) || refused.Reason == "" || !slices.Contains(refused.Missing, "MDBList key") {
			t.Errorf("refusal = %v, want a *Refused with its reason and key", err)
		}
	}
	if err := s.RunNow(ctx, "scan.full"); err != nil {
		t.Fatal(err)
	}
	if got := q.kinds(); !slices.Equal(got, []string{"scan.full"}) {
		t.Fatalf("run now queued %v", got)
	}
	if q.jobs[0].User != true {
		t.Error("run now is not a person's request")
	}
	q.busy["scan.full"] = true
	if err := s.RunNow(ctx, "scan.full"); !errors.Is(err, ErrBusy) {
		t.Errorf("run now while busy = %v, want ErrBusy", err)
	}
	if err := s.RunNow(ctx, "nope"); !errors.Is(err, ErrUnknownTask) {
		t.Errorf("run now of an unknown task = %v", err)
	}
}

// Run looks at the schedules at once and again when woken, and stops with its context.
func TestRunWakesAndStops(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	st, q := newMemStore(), newMemQueue()
	task := scanTask()
	task.DefaultEnabled = false
	s, _ := newScheduler(t, st, q, &now, nil, task)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "the first look", func() bool { rec := st.get("scan.full"); return rec.Task != "" })
	if got := q.kinds(); len(got) != 0 {
		t.Fatalf("an off schedule queued %v", got)
	}
	on := true
	if _, err := s.Set(context.Background(), "scan.full", Edit{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	s.Wake()
	waitFor(t, "the woken look", func() bool { return len(q.kinds()) == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
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

// The catalog is checked where it is written: a task without an id, a key, a default time, or one
// declared twice is a mistake in the code, not a schedule that behaves oddly.
func TestNewRefusesABadCatalog(t *testing.T) {
	st, q := newMemStore(), newMemQueue()
	for name, tasks := range map[string][]Task{
		"no id":       {{Key: "k", DefaultAt: MustClock("01:00")}},
		"no key":      {{ID: "t", DefaultAt: MustClock("01:00")}},
		"bad default": {{ID: "t", Key: "k", DefaultAt: 24 * 60}},
		"bad weekday": {{ID: "t", Key: "k", DefaultAt: MustClock("01:00"), DefaultDays: []time.Weekday{8}}},
		"twice":       {{ID: "t", Key: "k", DefaultAt: MustClock("01:00")}, {ID: "t", Key: "k2", DefaultAt: MustClock("02:00")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Options{Store: st, Queue: q, Tasks: tasks}); err == nil {
				t.Fatal("the bad catalog was accepted")
			}
		})
	}
	if _, err := New(Options{Tasks: []Task{scanTask()}}); err == nil {
		t.Error("a scheduler without a store or a queue was accepted")
	}
	if _, err := New(Options{Store: st, Queue: q, Tasks: []Task{scanTask()}}); err != nil {
		t.Fatalf("control: a good catalog was refused: %v", err)
	}
}
