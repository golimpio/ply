package subs

import (
	"sync"
	"time"
)

// ctl is the hold a Generator keeps on one conversion's ffmpeg once it runs.
//
// A conversion somebody waits for (the track of the item being played) is neither
// demoted nor suspended. One that nobody waits for (pre-extraction) is demoted when
// it starts and suspended while a play reads the same disk. The two are not fixed:
// a play that asks for a track a background run is making promotes the run, and a
// run whose last waiter leaves (the viewer closed the page) is demoted again, and
// paced like any other background work.
type ctl struct {
	mu      sync.Mutex
	pid     int  // 0 until ffmpeg runs, and again once it has been waited for
	nice    int  // the niceness of background work, known once ffmpeg runs
	waiters int  // the requests that wait for this run
	demoted bool // the background policy is on the process
	held    bool // the pacer wants it suspended
	stopped bool // suspended now
	bud     *budget
}

// started is told the pid of ffmpeg. nice is the niceness of background work.
func (c *ctl) started(pid, nice int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pid, c.nice = pid, nice
	if c.waiters == 0 {
		setNice(pid, nice)
		c.demoted = true
	}
	c.apply()
}

// ended is told that ffmpeg has been waited for: its pid may be someone else's now.
func (c *ctl) ended() {
	c.mu.Lock()
	c.pid = 0
	c.mu.Unlock()
}

// wait adds a request that waits for the run: the run is promoted if it is the
// first (released if it was suspended, given back what the macOS and Linux
// policies took from it).
func (c *ctl) wait() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiters++
	if c.waiters == 1 && c.pid != 0 && c.demoted {
		setForeground(c.pid)
		c.demoted = false
	}
	c.apply()
}

// unwait is a request that waited for the run leaving. When it was the last the run is
// background work again.
func (c *ctl) unwait() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiters > 0 {
		c.waiters--
	}
	if c.waiters == 0 && c.pid != 0 && !c.demoted {
		setNice(c.pid, c.nice)
		c.demoted = true
	}
	c.apply()
}

func (c *ctl) isUrgent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters > 0
}

// cancelUnlessWaited calls cancel, unless somebody waits for the run: the look and
// the cancel are one step, so a request cannot join between them. It reports whether
// it cancelled.
func (c *ctl) cancelUnlessWaited(cancel func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiters > 0 {
		return false
	}
	cancel()
	return true
}

// hold suspends the conversion (on) or lets it go on (off). It has no effect on one
// somebody waits for.
func (c *ctl) hold(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = on
	c.apply()
}

// apply makes ffmpeg match what is wanted: suspended while held and nobody waits.
// c.mu is held.
func (c *ctl) apply() {
	want := c.held && c.waiters == 0
	if c.pid == 0 || want == c.stopped {
		return
	}
	c.stopped = want
	if want {
		pauseGroup(c.pid)
		c.bud.stop()
	} else {
		resumeGroup(c.pid)
		c.bud.start()
	}
}

// budget is the time a conversion may run. It does not tick while the conversion
// is suspended: a film that plays for two hours must not use up the minutes the
// extraction behind it needs.
type budget struct {
	mu     sync.Mutex
	timer  *time.Timer
	left   time.Duration
	since  time.Time
	closed bool
}

// newBudget starts the clock; expire is called, once, when d of running time has passed.
func newBudget(d time.Duration, expire func()) *budget {
	return &budget{timer: time.AfterFunc(d, expire), left: d, since: time.Now()}
}

func (b *budget) stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.timer.Stop() {
		b.left -= time.Since(b.since)
	}
}

func (b *budget) start() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.since = time.Now()
		b.timer.Reset(max(b.left, 0))
	}
}

func (b *budget) close() {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
}
