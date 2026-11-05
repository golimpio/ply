package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ply/internal/jobs"
	"ply/internal/tier"
)

// stalled is a playback whose waiting viewers a test sets.
type stalled struct {
	mu      sync.Mutex
	waiting int
}

func (s *stalled) PlaybackLoad() tier.PlaybackLoad {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tier.PlaybackLoad{Sessions: 1, Waiting: s.waiting}
}

func (s *stalled) set(n int) {
	s.mu.Lock()
	s.waiting = n
	s.mu.Unlock()
}

// The metadata worker's pace holds the next item while a viewer is stalled:
// outside a job on the governor, inside one through the job, which then says why
// it waits.
func TestPaceForHoldsWhileAViewerIsStalled(t *testing.T) {
	pb := &stalled{waiting: 1}
	gov := tier.New(tier.Options{Playback: pb, Poll: 5 * time.Millisecond})
	pace := paceFor(gov, tier.Need{CPU: true})

	// Outside a job.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := pace(ctx); err != context.DeadlineExceeded {
		t.Fatalf("outside a job: pace = %v while a viewer is stalled, want it to wait", err)
	}

	// Inside a job: it starts with nothing stalled, then a viewer stalls before
	// its next item.
	pb.set(0)
	q := jobs.New(jobs.Options{Gate: gov})
	started, goOn, passed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	q.Register(jobs.Spec{Kind: "meta", YieldNeed: func(json.RawMessage) tier.Need { return tier.Need{CPU: true} },
		Run: func(ctx context.Context, r *jobs.Run) error {
			close(started)
			<-goOn
			if err := pace(ctx); err != nil {
				return err
			}
			close(passed)
			return nil
		}})
	qctx, qcancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(qctx); close(done) }()
	defer func() { qcancel(); <-done }()
	<-q.Ready()
	if _, err := q.Enqueue(context.Background(), "meta", jobs.Opt{}); err != nil {
		t.Fatal(err)
	}
	<-started
	pb.set(1)
	close(goOn)

	deadline := time.Now().Add(5 * time.Second)
	for {
		s := q.Snapshot()
		if len(s.Running) == 1 && s.Running[0].State == jobs.Running && strings.Contains(s.Running[0].Held, "waiting for video") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the running job does not say it waits for a stalled viewer: %+v", s.Running)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-passed:
		t.Fatal("the item passed while the viewer was stalled")
	default:
	}
	pb.set(0)
	gov.Notify()
	select {
	case <-passed:
	case <-time.After(5 * time.Second):
		t.Fatal("the item never passed once the viewer was served")
	}
}
