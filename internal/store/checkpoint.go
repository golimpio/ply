package store

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// With synchronous=NORMAL a commit in WAL mode does not wait for the disk: the WAL
// is synced when it is checkpointed into the database file. A crash of ply loses
// nothing, but a power cut or a kernel panic takes back every commit since the last
// checkpoint, and the database is then whole but as old as that checkpoint. SQLite
// checkpoints by itself only when the WAL reaches 1,000 pages, which for the small
// writes of a person using the server can be days, and what a person wrote (the
// TMDB key, folders, manual matches, where they stopped) is what hurts to lose.
//
// The checkpointer bounds the exposure: a checkpoint (PASSIVE: it never blocks a
// reader, and a long read can leave a tail of frames for the next one) follows
// every interactive write within checkpointSoon, and any write within
// checkpointIdle of the last one, when the writer has gone quiet. Bulk writes in
// full flow are covered by SQLite's own checkpoints.
const (
	checkpointSoon = 2 * time.Second
	checkpointIdle = 30 * time.Second
)

// checkpointer runs run, which takes the writer at background priority and
// checkpoints, soon after high-priority writes and when the writer is idle. A
// checkpointer with no run does nothing (the zero value).
type checkpointer struct {
	run func()

	mu                   sync.Mutex
	soonDelay, idleDelay time.Duration
	soon, idle           *time.Timer // pending requests, nil when none
	running, closed      bool

	runs atomic.Int64 // checkpoints made, for the tests
}

func (c *checkpointer) start(run func()) {
	c.run, c.soonDelay, c.idleDelay = run, checkpointSoon, checkpointIdle
}

// setDelays changes how long after a write a checkpoint follows (tests).
func (c *checkpointer) setDelays(soon, idle time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.soonDelay, c.idleDelay = soon, idle
}

// wrote tells the checkpointer that a write of priority p committed. The first
// high-priority write since the last checkpoint starts the soon timer, which later
// ones leave alone (so the wait is bounded, however steady the writes); every write
// restarts the idle timer.
func (c *checkpointer) wrote(p prio) {
	if c.run == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if p == prioHigh && c.soon == nil {
		c.soon = time.AfterFunc(c.soonDelay, c.fire)
	}
	if c.idle == nil {
		c.idle = time.AfterFunc(c.idleDelay, c.fire)
	} else {
		c.idle.Reset(c.idleDelay)
	}
}

// fire makes the checkpoint that either timer asked for. It serves both: a write
// that commits while it runs asks again.
func (c *checkpointer) fire() {
	c.mu.Lock()
	if c.closed || c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.stopTimers()
	c.mu.Unlock()

	c.run()
	c.runs.Add(1)

	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
}

// stopTimers drops the pending requests. c.mu is held.
func (c *checkpointer) stopTimers() {
	for _, t := range []**time.Timer{&c.soon, &c.idle} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
}

// stop ends the checkpointer: nothing is scheduled after it.
func (c *checkpointer) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.stopTimers()
}

// checkpoint is the checkpointer's run: it waits its turn for the writer behind
// the callers that are already queued (background priority: a person's write is
// never held up by it) and moves the WAL's frames into the database file, syncing
// it. Failing is fine: the next write asks again.
func (s *Store) checkpoint() {
	ctx, cancel := context.WithTimeout(s.bg, 30*time.Second)
	defer cancel()
	// Not through acquire: this is not one of the callers' writes, and must not
	// show in what the tests record of them.
	if err := s.wl.lock(ctx, prioLow); err != nil {
		return
	}
	defer s.wl.unlock()
	var busy, frames, done int
	_ = s.rw.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &done)
}
