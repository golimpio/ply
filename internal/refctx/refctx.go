// Package refctx gives the context of a piece of work that several callers wait
// for (a download, a frame grab): the work goes on while any of them waits, and is
// cancelled when the last one has gone away.
package refctx

import (
	"context"
	"errors"
	"sync"
)

// ErrAbandoned is the cause of the cancellation of work that every caller left.
var ErrAbandoned = errors.New("refctx: every caller left")

// Ctx is the context of one shared piece of work and the count of the callers
// waiting for it. The work runs under Context. A caller Joins before it waits and
// Leaves when it stops, for whatever reason; the Leave that brings the count to
// zero cancels the work with the cause ErrAbandoned.
type Ctx struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	waiters int
	gone    bool // the last waiter left: the work is being stopped
}

// New returns a Ctx with no waiter, under parent.
func New(parent context.Context) *Ctx {
	c := &Ctx{}
	c.ctx, c.cancel = context.WithCancelCause(parent)
	return c
}

// Context is the context of the work.
func (c *Ctx) Context() context.Context { return c.ctx }

// Join counts one more waiter. It reports false when the work was abandoned: it is
// being stopped, and whoever wants it must wait for it to end and start it afresh.
func (c *Ctx) Join() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return false
	}
	c.waiters++
	return true
}

// Leave takes back one Join. It is safe after the work ended, and after a Join
// that was refused (it then does nothing).
func (c *Ctx) Leave() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiters > 0 {
		c.waiters--
	}
	if c.waiters == 0 && !c.gone {
		c.gone = true
		c.cancel(ErrAbandoned)
	}
}

// Waiters is the number of callers that have Joined and not Left. The tests of the
// packages that use refctx look at it.
func (c *Ctx) Waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters
}

// Cancel stops the work for a reason of the owner (shutdown, a play that needs the
// disk). The first cause stands. The owner calls Cancel(nil) when the work ends,
// to release the context.
func (c *Ctx) Cancel(cause error) { c.cancel(cause) }

// Abandoned reports whether the work was cancelled because every caller left.
func (c *Ctx) Abandoned() bool { return errors.Is(context.Cause(c.ctx), ErrAbandoned) }

// Stopped reports whether err is the cancellation of the work, as opposed to a
// failure of the work itself: the work has been cancelled (by whatever cause) and err
// is, or wraps, context.Canceled or the cause. A genuine failure that the work
// returned just before it was cancelled is not Stopped, so the caller can still
// tell what happened to the thing it was after.
func (c *Ctx) Stopped(err error) bool {
	if err == nil || c.ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.Cause(c.ctx))
}
