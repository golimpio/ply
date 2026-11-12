package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// stopping is what a job saw of the end of its context.
type stopping struct {
	stopping bool
	cause    error
}

// When ply stops, every running job is signalled with ErrStopping (not a person's cancellation),
// and it is queued again for the next start.
func TestStoppingTheQueueSignalsItsRunningJobs(t *testing.T) {
	mem := newMem()
	started := make(chan struct{})
	var seen atomic.Pointer[stopping]
	spec := Spec{Kind: "scan", Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		seen.Store(&stopping{stopping: r.Stopping(), cause: context.Cause(ctx)})
		return ctx.Err()
	}}
	q, stop := start(t, Options{Persist: mem}, spec)
	j, err := q.Enqueue(context.Background(), "scan", Opt{Key: "full"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	stop() // ply stops while the scan runs
	v := seen.Load()
	if v == nil {
		t.Fatal("the job did not see its context end")
	}
	if !v.stopping || !errors.Is(v.cause, ErrStopping) {
		t.Errorf("the job was stopped with stopping=%v cause=%v, want Stopping and ErrStopping", v.stopping, v.cause)
	}
	if r := mem.get(j.ID); r.State != Queued {
		t.Errorf("after the stop the job is %q, want queued for the next start", r.State)
	}
}

// A person's cancel is not the server stopping: the job ends as cancelled, and it is not queued
// again by the stop path.
func TestACancelledJobIsNotAStop(t *testing.T) {
	mem := newMem()
	started := make(chan struct{})
	var seen atomic.Pointer[stopping]
	spec := Spec{Kind: "scan", Run: func(ctx context.Context, r *Run) error {
		close(started)
		<-ctx.Done()
		seen.Store(&stopping{stopping: r.Stopping(), cause: context.Cause(ctx)})
		return ctx.Err()
	}}
	q, _ := start(t, Options{Persist: mem}, spec)
	j, err := q.Enqueue(context.Background(), "scan", Opt{Key: "full"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := q.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the job to end", func() bool { return state(q, j.ID) == Cancelled })
	v := seen.Load()
	if v == nil {
		t.Fatal("the job did not see its context end")
	}
	if v.stopping || !errors.Is(v.cause, ErrCancelled) {
		t.Errorf("the cancelled job saw stopping=%v cause=%v, want not stopping and ErrCancelled", v.stopping, v.cause)
	}
	if r := mem.get(j.ID); r.State != Cancelled {
		t.Errorf("the job is %q, want cancelled", r.State)
	}
}
