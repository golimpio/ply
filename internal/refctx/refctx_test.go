package refctx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func live(t *testing.T, c *Ctx) {
	t.Helper()
	if err := c.Context().Err(); err != nil {
		t.Fatalf("the work was cancelled: %v", err)
	}
}

func TestOnlyTheLastToLeaveCancels(t *testing.T) {
	c := New(context.Background())
	for range 3 {
		if !c.Join() {
			t.Fatal("Join refused on live work")
		}
	}
	if c.Waiters() != 3 {
		t.Fatalf("waiters = %d, want 3", c.Waiters())
	}
	c.Leave()
	c.Leave()
	live(t, c) // positive control: two of three left and nothing happened
	if c.Waiters() != 1 {
		t.Fatalf("waiters = %d, want 1", c.Waiters())
	}
	c.Leave()
	if err := c.Context().Err(); err == nil {
		t.Fatal("everyone left and the work was not cancelled")
	}
	if cause := context.Cause(c.Context()); !errors.Is(cause, ErrAbandoned) || !c.Abandoned() {
		t.Fatalf("cause = %v, Abandoned = %v", cause, c.Abandoned())
	}
}

func TestJoinAfterTheLastLeftIsRefused(t *testing.T) {
	c := New(context.Background())
	c.Join()
	c.Leave()
	if c.Join() {
		t.Fatal("Join accepted work that is being stopped")
	}
	c.Leave() // a stray Leave is harmless
}

func TestAnotherCauseIsNotAbandonment(t *testing.T) {
	stop := errors.New("a play began")
	c := New(context.Background())
	c.Join()
	c.Cancel(stop)
	c.Leave()
	if got := context.Cause(c.Context()); got != stop || c.Abandoned() {
		t.Fatalf("cause = %v, Abandoned = %v; the first cause stands", got, c.Abandoned())
	}
	p, cancel := context.WithCancel(context.Background())
	c = New(p)
	c.Join()
	cancel()
	if c.Abandoned() || c.Context().Err() == nil {
		t.Fatal("the parent's end is not abandonment, and it ends the work")
	}
}

// A genuine error that the work returned just before it was cancelled is not the
// cancellation, whoever cancelled it; the error of the cancellation is, whatever form
// it takes (net/http gives the cause, not context.Canceled).
func TestStoppedIsTheErrorOfTheCancellationNotOneBeforeIt(t *testing.T) {
	genuine := errors.New("404 Not Found")
	stop := errors.New("a play began")

	c := New(context.Background())
	c.Join()
	for _, err := range []error{nil, genuine, context.Canceled, ErrAbandoned} {
		if c.Stopped(err) { // the work has not been cancelled: nothing is its cancellation
			t.Errorf("Stopped(%v) while the work runs", err)
		}
	}
	c.Leave()
	cases := []struct {
		err     error
		stopped bool
	}{
		{nil, false},
		{genuine, false},
		{fmt.Errorf("image: %w", genuine), false},
		{context.DeadlineExceeded, false},
		{context.Canceled, true},
		{fmt.Errorf("extract: %w", context.Canceled), true},
		{ErrAbandoned, true}, // the cause
		{fmt.Errorf("get: %w", ErrAbandoned), true},
	}
	for _, tc := range cases {
		if got := c.Stopped(tc.err); got != tc.stopped {
			t.Errorf("after the last Leave: Stopped(%v) = %v, want %v", tc.err, got, tc.stopped)
		}
	}

	c = New(context.Background())
	c.Join()
	c.Cancel(stop)
	for _, tc := range []struct {
		err     error
		stopped bool
	}{{genuine, false}, {stop, true}, {fmt.Errorf("x: %w", stop), true}, {context.Canceled, true}} {
		if got := c.Stopped(tc.err); got != tc.stopped {
			t.Errorf("after Cancel(stop): Stopped(%v) = %v, want %v", tc.err, got, tc.stopped)
		}
	}
}

func TestConcurrentJoinAndLeave(t *testing.T) {
	c := New(context.Background())
	c.Join() // the owner's
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Join() {
				c.Leave()
			}
		}()
	}
	wg.Wait()
	live(t, c)
	c.Leave()
	if !c.Abandoned() {
		t.Fatal("not abandoned after the last Leave")
	}
}
