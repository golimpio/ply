// Package jobs is ply's background queue: scans, metadata lookups and cache
// work, each a job with a kind, a priority and a state, kept in SQLite so that
// the Activity page can show what runs, what waits and what failed, and so that
// queued work survives a restart.
//
// The queue runs below playback and browsing: a job waits on the tier governor
// before it starts and between its steps (Run.Yield), so a viewer or a person
// browsing never waits for it (docs/M9-ARCHITECTURE.md, "Unified persistent queue").
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ply/internal/tier"
)

// State is where a job is in its life.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Finished reports whether s is an end state.
func (s State) Finished() bool { return s == Done || s == Failed || s == Cancelled }

// Priorities: lower runs first. A person's request beats everything.
const (
	PriorityUser     = 0
	PriorityDirs     = 10 // files that just appeared in a folder
	PriorityMatchNew = 20 // details for files just added
	PriorityScan     = 30 // the periodic full scan
	PriorityRefresh  = 40 // details that are due to be refreshed
	PriorityCache    = 50 // caches, prefetching, pruning
)

// Job is a job as the API shows it.
type Job struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Key      string `json:"key,omitempty"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	State    State  `json:"state"`
	// User is set for a job a person asked for (a button, a bump, a retry).
	User bool `json:"user"`
	// Done, Total and Note are its progress (Total 0: not known).
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Note  string `json:"note,omitempty"`
	// Held says why it is waiting for playback or browsing, while it is.
	Held     string `json:"held,omitempty"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`

	CreatedAt  time.Time `json:"createdAt"`
	RunAt      time.Time `json:"runAt,omitzero"` // not before (a retry's backoff)
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`

	Payload json.RawMessage `json:"-"`
}

// Spec declares a kind of job.
type Spec struct {
	Kind     string
	Priority int
	// Title names a job of this kind for people ("Scan /Volumes/NAS/Movies").
	Title func(payload json.RawMessage) string
	// Need says what the job uses; nil is light work (only a stalled viewer or
	// browsing holds it).
	Need func(payload json.RawMessage) tier.Need
	// Run does the work. It calls r.Yield between steps and r.Progress as it goes.
	// A Permanent error is not retried.
	Run func(ctx context.Context, r *Run) error
	// Merge combines the payload of a job enqueued with the key of one already
	// queued (default: the new payload replaces the old).
	Merge func(old, new json.RawMessage) json.RawMessage
	// MaxAttempts before a failure is final (default 5).
	MaxAttempts int
	// YieldNeed is what Run.Yield waits for between steps, when it differs from
	// Need: a full scan does not start while a video plays from the library's disk,
	// but once it holds the scanner it yields only to what hurts a viewer now (a
	// stalled viewer, a CPU transcode), so that it does not keep the scanner from
	// the folders the watcher saw change for the length of a film. Default: Need.
	YieldNeed func(payload json.RawMessage) tier.Need
	// Light puts the kind in the light lane whatever its Need: work that reads a
	// disk a little at a time (a file's header, its sidecars) between network calls.
	// Its Need still holds it while a video plays from that disk; it just does not
	// wait for the heavy lane (a full scan) to be free.
	Light bool
}

func (s *Spec) need(p json.RawMessage) tier.Need {
	if s.Need == nil {
		return tier.Need{}
	}
	return s.Need(p)
}

func (s *Spec) title(p json.RawMessage) string {
	if s.Title == nil {
		return s.Kind
	}
	return s.Title(p)
}

// Gate is the tier governor as the queue uses it (*tier.Governor).
type Gate interface {
	// Check answers whether a job with need n may start now.
	Check(n tier.Need) tier.Hold
	// Wait blocks a running job between its steps until n may run.
	Wait(ctx context.Context, n tier.Need, held func(reason string)) error
	// Changed is closed when playback changes; Poll is how often to look again
	// without that; BrowseMaxHold is the longest browsing alone may hold a job.
	Changed() <-chan struct{}
	Poll() time.Duration
	BrowseMaxHold() time.Duration
	State() tier.State
}

// Persist keeps jobs across restarts (implemented by store.Store).
type Persist interface {
	InsertJob(ctx context.Context, j Record) (int64, error)
	UpdateJob(ctx context.Context, j Record) error
	// LoadJobs returns the jobs that were queued or running, and the newest
	// `history` finished ones.
	LoadJobs(ctx context.Context, history int) ([]Record, error)
	PruneJobs(ctx context.Context, before time.Time) (int, error)
	QueuePaused(ctx context.Context) (bool, error)
	SetQueuePaused(ctx context.Context, paused bool) error
}

// Record is what is kept of a job: everything but its live progress.
type Record struct {
	ID         int64
	Kind       string
	Key        string
	Title      string
	Priority   int
	State      State
	User       bool
	Payload    []byte
	Attempts   int
	Error      string
	CreatedAt  time.Time
	RunAt      time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

func (j *Job) record() Record {
	return Record{ID: j.ID, Kind: j.Kind, Key: j.Key, Title: j.Title, Priority: j.Priority, State: j.State, User: j.User,
		Payload: j.Payload, Attempts: j.Attempts, Error: j.Error, CreatedAt: j.CreatedAt, RunAt: j.RunAt,
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt}
}

func fromRecord(r Record) Job {
	return Job{ID: r.ID, Kind: r.Kind, Key: r.Key, Title: r.Title, Priority: r.Priority, State: r.State, User: r.User,
		Payload: r.Payload, Attempts: r.Attempts, Error: r.Error, CreatedAt: r.CreatedAt, RunAt: r.RunAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
}

// permanent marks an error that retrying cannot fix.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent wraps err so that the job fails at once, without retries.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// ErrCancelled is the cause of a job's context when a person cancels it.
var ErrCancelled = errors.New("cancelled")

// ErrStopping is the cause of a job's context when ply is stopping: the job is asked to stop at its
// next step, and it is queued again for the next start. It is not a person's cancellation.
var ErrStopping = errors.New("ply is stopping")

// ErrUnknownKind is returned for a kind nobody registered.
var ErrUnknownKind = errors.New("jobs: unknown kind")

// ErrClosed is returned by an enqueue after Run has returned (ply is stopping).
var ErrClosed = errors.New("jobs: the queue has stopped")

// ErrNotFound is returned for an id the queue does not have (or no longer has).
var ErrNotFound = errors.New("jobs: no such job")

// ErrState is returned for an action the job's state does not allow (cancelling a
// finished job, retrying a running one).
var ErrState = errors.New("jobs: not possible in this state")
