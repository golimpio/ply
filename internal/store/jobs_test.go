package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ply/internal/jobs"
)

// The queue's methods, for TestEveryStoreMethodIsRouted (pool_test.go).
func init() {
	extraReads = append(extraReads,
		routedCall("LoadJobs", 0, func(ctx context.Context, s *Store) error { _, err := s.LoadJobs(ctx, 10); return err }),
		routedCall("QueuePaused", 0, func(ctx context.Context, s *Store) error { _, err := s.QueuePaused(ctx); return err }),
	)
	extraWrites = append(extraWrites,
		// A person flips the switch: it goes before the background's writes.
		routedCall("SetQueuePaused", prioHigh, func(ctx context.Context, s *Store) error { return s.SetQueuePaused(ctx, true) }),
		// The queue's own bookkeeping.
		routedCall("InsertJob", prioLow, func(ctx context.Context, s *Store) error {
			_, err := s.InsertJob(ctx, jobs.Record{Kind: "k", State: jobs.Queued, CreatedAt: time.Now()})
			return err
		}),
		routedCall("UpdateJob", prioLow, func(ctx context.Context, s *Store) error {
			return s.UpdateJob(ctx, jobs.Record{ID: 1, State: jobs.Done})
		}),
		routedCall("PruneJobs", prioLow, func(ctx context.Context, s *Store) error { _, err := s.PruneJobs(ctx, time.Now()); return err }),
	)
}

func routedCall(name string, p prio, call func(context.Context, *Store) error) routed {
	return routed{name: name, prio: p, call: call}
}

func TestJobsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	t0 := time.UnixMilli(1_790_000_000_000)
	queued := jobs.Record{Kind: "scan.full", Title: "Scan the library", Priority: 30, State: jobs.Queued,
		Payload: []byte(`{"roots":["/nas"]}`), CreatedAt: t0}
	id, err := st.InsertJob(ctx, queued)
	if err != nil || id == 0 {
		t.Fatalf("InsertJob = %d, %v", id, err)
	}
	running := queued
	running.ID = id
	running.State, running.Attempts, running.StartedAt = jobs.Running, 1, t0.Add(time.Second)
	if err := st.UpdateJob(ctx, running); err != nil {
		t.Fatal(err)
	}
	// Finished jobs: an old one and two recent ones.
	var finished []int64
	for i, at := range []time.Time{t0.Add(-30 * 24 * time.Hour), t0.Add(time.Minute), t0.Add(2 * time.Minute)} {
		r := jobs.Record{Kind: "meta.match", Priority: 20, State: jobs.Done, CreatedAt: at, FinishedAt: at, Error: ""}
		if i == 2 {
			r.State, r.Error, r.User = jobs.Failed, "TMDB said 503", true
		}
		fid, err := st.InsertJob(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		finished = append(finished, fid)
	}

	got, err := st.LoadJobs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadJobs(history 1) = %d rows, want the running one and the newest finished", len(got))
	}
	r := got[0]
	if r.ID != id || r.State != jobs.Running || r.Attempts != 1 || string(r.Payload) != `{"roots":["/nas"]}` ||
		!r.StartedAt.Equal(t0.Add(time.Second)) || !r.CreatedAt.Equal(t0) || !r.FinishedAt.IsZero() {
		t.Fatalf("running job read back as %+v", r)
	}
	if f := got[1]; f.ID != finished[2] || f.State != jobs.Failed || f.Error != "TMDB said 503" || !f.User {
		t.Fatalf("newest finished = %+v", f)
	}

	all, _ := st.LoadJobs(ctx, 10)
	if len(all) != 4 { // positive control for the prune below
		t.Fatalf("before prune: %d rows", len(all))
	}
	n, err := st.PruneJobs(ctx, t0.Add(-7*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneJobs = %d, %v; want the month-old one", n, err)
	}
	left, _ := st.LoadJobs(ctx, 10)
	if len(left) != 3 {
		t.Fatalf("after prune: %d rows", len(left))
	}
	for _, l := range left {
		if l.ID == finished[0] {
			t.Fatal("the old job is still there")
		}
	}
}

func TestQueuePaused(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "ply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if p, err := st.QueuePaused(ctx); err != nil || p {
		t.Fatalf("new db: paused=%v err=%v", p, err)
	}
	for _, want := range []bool{true, false, true} {
		if err := st.SetQueuePaused(ctx, want); err != nil {
			t.Fatal(err)
		}
		if p, _ := st.QueuePaused(ctx); p != want {
			t.Fatalf("paused = %v, want %v", p, want)
		}
	}
}
