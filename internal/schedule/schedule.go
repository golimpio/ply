// Package schedule keeps the background tasks' schedules and queues their jobs when they fall due
// (docs/M12-SCHEDULES.md). A schedule is a time of day, the days of the week it runs on, an on/off
// switch and when it last ran, all kept in the app's database so that a restart neither repeats a
// run nor loses one. A task whose keys are missing is not run: the schedule says why, once, and the
// settings page shows it.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ply/internal/jobs"
	"ply/internal/store"
)

// Clock is a time of day in the server's local time, as minutes after midnight. In JSON it is
// "HH:MM".
type Clock int

// Day is a whole day, in minutes.
const Day Clock = 24 * 60

// ParseClock reads "HH:MM", 00:00 to 23:59.
func ParseClock(s string) (Clock, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("%w: %q is not a time of day (HH:MM)", ErrInvalid, s)
	}
	h, err := strconv.Atoi(s[:2])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("%w: %q is not a time of day (HH:MM)", ErrInvalid, s)
	}
	m, err := strconv.Atoi(s[3:])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("%w: %q is not a time of day (HH:MM)", ErrInvalid, s)
	}
	return Clock(h*60 + m), nil
}

// MustClock is ParseClock for a time written in the code: a wrong one is a programmer's error, not
// something a person can do.
func MustClock(s string) Clock {
	c, err := ParseClock(s)
	if err != nil {
		panic(err)
	}
	return c
}

// String is "HH:MM".
func (c Clock) String() string {
	if c < 0 || c >= Day {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60)
}

// Valid reports whether c is a minute of a day.
func (c Clock) Valid() bool { return c >= 0 && c < Day }

// MarshalJSON writes c as "HH:MM".
func (c Clock) MarshalJSON() ([]byte, error) {
	if !c.Valid() {
		return nil, fmt.Errorf("schedule: %d is not a time of day", int(c))
	}
	return json.Marshal(c.String())
}

// UnmarshalJSON reads "HH:MM".
func (c *Clock) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%w: a time of day is written \"HH:MM\"", ErrInvalid)
	}
	parsed, err := ParseClock(s)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// Errors of the schedule package.
var (
	// ErrInvalid is wrapped by what the page sent and the schedule cannot take.
	ErrInvalid = errors.New("invalid schedule")
	// ErrUnknownTask is a task id no one registered.
	ErrUnknownTask = errors.New("schedule: no such task")
	// ErrBusy means the task's job is already queued or running.
	ErrBusy = errors.New("schedule: the task is already queued or running")
)

// Refused is why a task cannot run now: its API key is missing, or online lookups are off.
type Refused struct {
	Reason  string
	Missing []string
}

func (r *Refused) Error() string { return r.Reason }

// Task is a background job that can be scheduled. The catalog lives where the kinds are registered
// (cmd/ply/schedules.go); ID is the queue's kind and the schedule's key in the database.
type Task struct {
	ID             string
	Key            string // the queue key its jobs merge on
	Title          string
	Detail         string
	DefaultEnabled bool
	DefaultAt      Clock
	DefaultDays    []time.Weekday // empty is every day
	// After are kinds that must not be queued or running when a task that has never run starts:
	// the first ratings pass waits for the first scan and match, so that it sees the library.
	After []string
}

// Status is what a task can do right now. Blocked says why it cannot run ("" it can); Missing names
// the keys it lacks; Warnings are what it will not do with the keys it has.
type Status struct {
	Missing  []string
	Blocked  string
	Warnings []string
}

// Checker answers what a task can do now. It is the server's own (cmd/ply/schedules.go): the keys,
// the privacy switch, and which provider fills which kind of title.
type Checker func(ctx context.Context, task Task) Status

// Entry is a task's schedule as the settings page shows it.
type Entry struct {
	Task     string     `json:"task"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail"`
	Enabled  bool       `json:"enabled"`
	At       Clock      `json:"at"`
	Days     []int      `json:"days"` // ISO weekdays, 1 Monday to 7 Sunday; empty is every day
	LastRun  *time.Time `json:"lastRun,omitempty"`
	Next     *time.Time `json:"next,omitempty"` // the next run, or now when one is due
	Blocked  string     `json:"blocked,omitempty"`
	Missing  []string   `json:"missing"`
	Warnings []string   `json:"warnings"`
}

// Edit is a change of a schedule; a nil field stays as it is.
type Edit struct {
	Enabled *bool  `json:"enabled"`
	At      *Clock `json:"at"`
	Days    *[]int `json:"days"`
}

// empty reports whether the edit asks for nothing.
func (e Edit) empty() bool { return e.Enabled == nil && e.At == nil && e.Days == nil }

// days sorts and checks a list of ISO weekdays.
func days(d []int) ([]int, error) {
	out := slices.Clone(d)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, d := range out {
		if d < 1 || d > 7 {
			return nil, fmt.Errorf("%w: %d is not a day of the week", ErrInvalid, d)
		}
	}
	return out, nil
}

// isoDays is a list of weekdays as the ISO numbers the store keeps, Monday 1 to Sunday 7.
func isoDays(ds []time.Weekday) []int {
	out := make([]int, len(ds))
	for i, d := range ds {
		out[i] = isoWeekday(d)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// weekdays is the other way: ISO numbers back to weekdays.
func weekdays(ds []int) []time.Weekday {
	out := make([]time.Weekday, len(ds))
	for i, d := range ds {
		out[i] = time.Weekday(d % 7) // 7 is Sunday, which time.Weekday calls 0
	}
	return out
}

// isoWeekday is Monday 1 to Sunday 7.
func isoWeekday(d time.Weekday) int {
	if d == time.Sunday {
		return 7
	}
	return int(d)
}

// nextAt is the first occurrence of at on one of ds strictly after after. No ds is every day.
func nextAt(after time.Time, at Clock, ds []int) time.Time {
	for i := 0; i < 8; i++ { // eight days find the next one, however late the days are
		day := after.AddDate(0, 0, i)
		when := time.Date(day.Year(), day.Month(), day.Day(), int(at)/60, int(at)%60, 0, 0, after.Location())
		if !when.After(after) {
			continue
		}
		if len(ds) == 0 || slices.Contains(ds, isoWeekday(when.Weekday())) {
			return when
		}
	}
	return time.Time{} // not reachable with a valid time and week; never a run the caller can lose
}

// due reports whether the schedule has an occurrence the last run did not cover. A schedule that
// never ran is due at once: a first start, or the first start after this version, fills the library.
func due(rec store.ScheduleRecord, now time.Time) bool {
	if !rec.Enabled {
		return false
	}
	if rec.LastRun == 0 {
		return true
	}
	last := time.Unix(rec.LastRun, 0).In(now.Location())
	return !nextAt(last, Clock(rec.AtMinute), rec.Days).After(now)
}

// nextRun is when the entry runs next, or now when it is due, or the zero time when it is off.
func nextRun(rec store.ScheduleRecord, now time.Time) time.Time {
	if !rec.Enabled {
		return time.Time{}
	}
	if rec.LastRun == 0 {
		return now
	}
	last := time.Unix(rec.LastRun, 0).In(now.Location())
	n := nextAt(last, Clock(rec.AtMinute), rec.Days)
	if !n.After(now) {
		return now
	}
	return n
}

// Storage is the schedules table as the scheduler uses it (implemented by store.Store).
type Storage interface {
	Schedules(ctx context.Context) ([]store.ScheduleRecord, error)
	PutSchedule(ctx context.Context, rec store.ScheduleRecord) error
	MarkScheduleRun(ctx context.Context, task string, at int64) error
}

// Queue is the background queue as the scheduler uses it (implemented by jobs.Queue).
type Queue interface {
	Enqueue(ctx context.Context, kind string, o jobs.Opt) (jobs.Job, error)
	Busy(kind string) bool
}

// Options configures a Scheduler.
type Options struct {
	Store  Storage
	Queue  Queue
	Tasks  []Task
	Check  Checker
	Logger *slog.Logger
	Now    func() time.Time
	// Every is how often the schedules are looked at (default 30 s). A schedule fires at the first
	// look at or after its time, so this is also the longest a run is late.
	Every time.Duration
}

// Scheduler keeps the schedules and queues the tasks they make due. It is safe for concurrent use.
type Scheduler struct {
	o     Options
	order []Task
	byID  map[string]Task

	wake chan struct{}

	mu         sync.Mutex
	wasBlocked map[string]bool
	skipped    map[string]string // the day last said to be skipped, by task
	setMu      sync.Mutex        // one change of a schedule at a time: read, edit and write are one step
}

// New returns the scheduler.
func New(o Options) (*Scheduler, error) {
	if o.Store == nil || o.Queue == nil {
		return nil, errors.New("schedule: a scheduler needs the store and the queue")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = 30 * time.Second
	}
	s := &Scheduler{o: o, byID: make(map[string]Task, len(o.Tasks)), wake: make(chan struct{}, 1), wasBlocked: map[string]bool{}, skipped: map[string]string{}}
	for _, t := range o.Tasks {
		if strings.TrimSpace(t.ID) == "" || t.Key == "" {
			return nil, fmt.Errorf("schedule: task %q needs an id and a queue key", t.Title)
		}
		if !t.DefaultAt.Valid() {
			return nil, fmt.Errorf("schedule: task %q has no default time", t.ID)
		}
		if _, dup := s.byID[t.ID]; dup {
			return nil, fmt.Errorf("schedule: two tasks are called %q", t.ID)
		}
		ds, err := days(isoDays(t.DefaultDays))
		if err != nil {
			return nil, fmt.Errorf("schedule: task %q: %w", t.ID, err)
		}
		t.DefaultDays = weekdays(ds)
		s.byID[t.ID] = t
		s.order = append(s.order, t)
	}
	return s, nil
}

// Wake asks for the schedules to be looked at now (a key was stored, the settings changed).
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run looks at the schedules at once and then until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	s.tick(ctx)
	t := time.NewTicker(s.o.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		case <-s.wake:
			s.tick(ctx)
		}
	}
}

// tick queues every task that is due and may run, and says so for one that may not.
func (s *Scheduler) tick(ctx context.Context) {
	now := s.o.Now()
	recs, err := s.o.Store.Schedules(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.o.Logger.Warn("schedules: reading them", "err", err)
		}
		return
	}
	byTask := make(map[string]store.ScheduleRecord, len(recs))
	for _, r := range recs {
		byTask[r.Task] = r
	}
	for _, t := range s.order {
		if ctx.Err() != nil {
			return
		}
		rec, ok := byTask[t.ID]
		if !ok {
			// The defaults are written the first time a task is seen, so that when it ran is kept.
			rec = store.ScheduleRecord{Task: t.ID, Enabled: t.DefaultEnabled, AtMinute: int(t.DefaultAt),
				Days: isoDays(t.DefaultDays), UpdatedAt: now.Unix()}
			if err := s.o.Store.PutSchedule(ctx, rec); err != nil {
				if ctx.Err() == nil {
					s.o.Logger.Warn("schedules: storing the defaults", "task", t.ID, "err", err)
				}
				continue
			}
		}
		status := s.status(ctx, t)
		blockedBefore := s.blocked(t.ID)
		// Only a blocked task whose time has come is "blocked" for the catch-up: a key that is
		// missing between two runs does not make one due when it comes back.
		blockedNow := status.Blocked != "" && due(rec, now)
		s.setBlocked(t.ID, blockedNow)
		if status.Blocked != "" {
			if blockedNow {
				// The run is not made, and last_run is left alone: the page says why, the log says it
				// once for this day, and a look after the key is stored runs it at once.
				s.skipOnce(t, status, now)
			}
			continue
		}
		s.clearSkipped(t.ID)
		if rec.LastRun == 0 && s.afterBusy(t) {
			// A first run waits for the scan and the match it needs: an empty library has nothing
			// to fill, and the next look (with the library there) is the one that counts.
			continue
		}
		if blockedBefore && rec.Enabled {
			// It could not run when it was due and now it can: fill in at once, then keep its time.
			if s.queue(ctx, t, now) {
				s.mark(ctx, rec, now)
			}
			continue
		}
		if !due(rec, now) {
			continue
		}
		if s.o.Queue.Busy(t.ID) {
			// The run that is queued or going is this one: it is not queued twice.
			s.o.Logger.Info("scheduled task is already queued or running", "task", t.ID)
			s.mark(ctx, rec, now)
			continue
		}
		if s.queue(ctx, t, now) {
			s.mark(ctx, rec, now)
		}
	}
}

// afterBusy reports whether a kind that a never-run task waits for is queued or running.
func (s *Scheduler) afterBusy(t Task) bool {
	for _, kind := range t.After {
		if s.o.Queue.Busy(kind) {
			return true
		}
	}
	return false
}

// blocked reports whether the task could not run at the last look.
func (s *Scheduler) blocked(task string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wasBlocked[task]
}

func (s *Scheduler) setBlocked(task string, blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wasBlocked[task] = blocked
}

// queue enqueues the task's job, and reports whether it was.
func (s *Scheduler) queue(ctx context.Context, t Task, now time.Time) bool {
	if _, err := s.o.Queue.Enqueue(ctx, t.ID, jobs.Opt{Key: t.Key}); err != nil {
		if ctx.Err() == nil {
			s.o.Logger.Warn("scheduled task not queued", "task", t.ID, "err", err)
		}
		return false
	}
	s.o.Logger.Info("scheduled task queued", "task", t.ID, "at", now.Format(time.RFC3339))
	return true
}

// skip says, once for the day, why the task is not run: the API key it needs is missing, or
// online lookups are off.
func (s *Scheduler) skip(t Task, status Status) {
	if len(status.Missing) > 0 {
		s.o.Logger.Warn("scheduled task skipped: API key is missing", "task", t.ID,
			"key", strings.Join(status.Missing, ", "), "reason", status.Blocked)
		return
	}
	s.o.Logger.Warn("scheduled task skipped", "task", t.ID, "reason", status.Blocked)
}

// skipOnce says why the task is not run, once for each day, however often the schedules are
// looked at.
func (s *Scheduler) skipOnce(t Task, status Status, now time.Time) {
	day := now.Format(time.DateOnly)
	s.mu.Lock()
	last, seen := s.skipped[t.ID]
	if seen && last == day {
		s.mu.Unlock()
		return
	}
	s.skipped[t.ID] = day
	s.mu.Unlock()
	s.skip(t, status)
}

// clearSkipped forgets a refusal that is over, so that a later one is said again.
func (s *Scheduler) clearSkipped(task string) {
	s.mu.Lock()
	delete(s.skipped, task)
	s.mu.Unlock()
}

// mark records that the schedule ran, or that a run of it is queued or going, at at. It is
// recorded even while ply is stopping: the job was queued, and a restart must not queue it again.
func (s *Scheduler) mark(ctx context.Context, rec store.ScheduleRecord, at time.Time) {
	if err := s.o.Store.MarkScheduleRun(context.WithoutCancel(ctx), rec.Task, at.Unix()); err != nil && ctx.Err() == nil {
		s.o.Logger.Warn("schedules: recording a run", "task", rec.Task, "err", err)
	}
}

// status asks the checker what the task can do; with no checker, nothing is lacking.
func (s *Scheduler) status(ctx context.Context, t Task) Status {
	if s.o.Check == nil {
		return Status{}
	}
	return s.o.Check(ctx, t)
}

// entryOf is a stored schedule as the page sees it.
func (s *Scheduler) entryOf(ctx context.Context, t Task, rec store.ScheduleRecord, now time.Time) Entry {
	st := s.status(ctx, t)
	e := Entry{Task: t.ID, Title: t.Title, Detail: t.Detail, Enabled: rec.Enabled, At: Clock(rec.AtMinute),
		Days: nonNil(slices.Clone(rec.Days)), Blocked: st.Blocked, Missing: nonNil(st.Missing), Warnings: nonNil(st.Warnings)}
	if rec.LastRun > 0 {
		at := time.Unix(rec.LastRun, 0).In(now.Location())
		e.LastRun = &at
	}
	if n := nextRun(rec, now); !n.IsZero() && st.Blocked == "" {
		e.Next = &n
	}
	return e
}

// List returns every task's schedule, in the catalog's order.
func (s *Scheduler) List(ctx context.Context) ([]Entry, error) {
	recs, err := s.o.Store.Schedules(ctx)
	if err != nil {
		return nil, err
	}
	byTask := make(map[string]store.ScheduleRecord, len(recs))
	for _, r := range recs {
		byTask[r.Task] = r
	}
	now := s.o.Now()
	out := make([]Entry, 0, len(s.order))
	for _, t := range s.order {
		rec, ok := byTask[t.ID]
		if !ok {
			rec = store.ScheduleRecord{Task: t.ID, Enabled: t.DefaultEnabled, AtMinute: int(t.DefaultAt), Days: isoDays(t.DefaultDays)}
		}
		out = append(out, s.entryOf(ctx, t, rec, now))
	}
	return out, nil
}

// Set changes a task's schedule and returns it as it is now.
func (s *Scheduler) Set(ctx context.Context, task string, e Edit) (Entry, error) {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	t, ok := s.byID[task]
	if !ok {
		return Entry{}, ErrUnknownTask
	}
	if e.empty() {
		return Entry{}, fmt.Errorf("%w: nothing to change", ErrInvalid)
	}
	recs, err := s.o.Store.Schedules(ctx)
	if err != nil {
		return Entry{}, err
	}
	rec := store.ScheduleRecord{Task: t.ID, Enabled: t.DefaultEnabled, AtMinute: int(t.DefaultAt), Days: isoDays(t.DefaultDays)}
	for _, r := range recs {
		if r.Task == t.ID {
			rec = r
			break
		}
	}
	if e.Enabled != nil {
		rec.Enabled = *e.Enabled
	}
	if e.At != nil {
		if !e.At.Valid() {
			return Entry{}, fmt.Errorf("%w: %q is not a time of day", ErrInvalid, e.At.String())
		}
		rec.AtMinute = int(*e.At)
	}
	if e.Days != nil {
		ds, err := days(*e.Days)
		if err != nil {
			return Entry{}, err
		}
		rec.Days = ds
	}
	rec.Task = t.ID
	rec.UpdatedAt = s.o.Now().Unix()
	if err := s.o.Store.PutSchedule(ctx, rec); err != nil {
		if errors.Is(err, store.ErrInvalidSchedule) {
			return Entry{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return Entry{}, err
	}
	s.Wake() // a schedule turned on, or made due by a new time, takes effect at once
	return s.entryOf(ctx, t, rec, s.o.Now()), nil
}

// RunNow queues a task's job now, as a person's request. A task whose keys are missing is refused,
// with the reason the settings page shows.
func (s *Scheduler) RunNow(ctx context.Context, task string) error {
	t, ok := s.byID[task]
	if !ok {
		return ErrUnknownTask
	}
	if st := s.status(ctx, t); st.Blocked != "" {
		return &Refused{Reason: st.Blocked, Missing: st.Missing}
	}
	if s.o.Queue.Busy(t.ID) {
		return ErrBusy
	}
	if _, err := s.o.Queue.Enqueue(ctx, t.ID, jobs.Opt{Key: t.Key, User: true}); err != nil {
		return err
	}
	s.o.Logger.Info("scheduled task queued now", "task", t.ID)
	return nil
}

// nonNil returns a slice that JSON writes as [] and not null.
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
