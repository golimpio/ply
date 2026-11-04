package store

import (
	"context"
	"database/sql"
	"strings"
	"sync"
)

// writeChunk is the most rows a bulk write changes in one transaction: a library
// of 10,000 files is deleted, re-parsed or pruned in batches, and whoever is
// waiting for the writer waits for one batch, not for all of them.
const writeChunk = 500

// prio says how soon a write is wanted.
type prio int

const (
	// prioHigh is for what a person is waiting for: a progress save, a setting, a
	// manual match, a folder that is added. It goes ahead of everything queued.
	prioHigh prio = iota
	// prioLow is for the scanner, the metadata worker and the chunks of a bulk
	// write. Bulk writers queue again between chunks, which is where they yield.
	prioLow
)

// writerLock hands the one writer to its callers: high-priority callers first,
// each class in the order it asked. It is a mutex whose unlock passes the lock
// straight to the next waiter, so that a caller that unlocks and asks again (the
// next chunk of a bulk write) goes to the back of its queue and cannot take the
// lock again ahead of one that has been waiting. The zero value is unlocked.
//
// A steady stream of high-priority writes would starve the low ones. There is none:
// they are single small statements a person's clicks and a 10 s progress save make.
type writerLock struct {
	mu     sync.Mutex
	held   bool
	queues [2][]*waiter // by prio

	// granted, when set, is called by Store.write and Store.exec as each takes the
	// lock, still holding it. Tests use it to see the order and to hold the writer.
	granted func(prio)
}

// waiter is a caller in a queue; ready is closed when the lock is handed to it.
type waiter struct{ ready chan struct{} }

// lock waits for the lock, or for ctx to end.
func (l *writerLock) lock(ctx context.Context, p prio) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		return nil
	}
	w := &waiter{ready: make(chan struct{})}
	l.queues[p] = append(l.queues[p], w)
	l.mu.Unlock()

	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		queued := l.dequeue(p, w)
		l.mu.Unlock()
		if !queued {
			// The lock was handed to us as ctx ended: pass it on.
			l.unlock()
		}
		return ctx.Err()
	}
}

// dequeue removes w from its queue and reports whether it was there. l.mu is held.
func (l *writerLock) dequeue(p prio, w *waiter) bool {
	q := l.queues[p]
	for i := range q {
		if q[i] == w {
			l.queues[p] = append(q[:i:i], q[i+1:]...)
			return true
		}
	}
	return false
}

// unlock hands the lock to the first waiter of the highest class, or frees it.
func (l *writerLock) unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for p := range l.queues {
		if q := l.queues[p]; len(q) > 0 {
			l.queues[p] = q[1:]
			close(q[0].ready) // held stays true: the lock changes hands
			return
		}
	}
	l.held = false
}

// waiting is how many callers wait, by class.
func (l *writerLock) waiting() (high, low int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.queues[prioHigh]), len(l.queues[prioLow])
}

// acquire takes the writer for a write of priority p. The returned func gives it up.
func (s *Store) acquire(ctx context.Context, p prio) (release func(), err error) {
	if err := s.wl.lock(ctx, p); err != nil {
		return nil, err
	}
	if s.wl.granted != nil {
		s.wl.granted(p)
	}
	return s.wl.unlock, nil
}

// write runs fn in one transaction on the writer connection, once it is the
// caller's turn, and commits it if fn returns nil. Everything that changes the
// database goes through here or s.exec, and fn reads through tx: a read that
// is part of a write belongs in the transaction, where nothing else can write
// between it and the change it leads to.
//
// A bulk write calls it once per chunk (writeChunk rows), so that the writer
// changes hands between chunks.
func (s *Store) write(ctx context.Context, p prio, fn func(tx *sql.Tx) error) error {
	release, err := s.acquire(ctx, p)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.ck.wrote(p)
	return nil
}

// exec is write for a single statement.
func (s *Store) exec(ctx context.Context, p prio, query string, args ...any) (sql.Result, error) {
	release, err := s.acquire(ctx, p)
	if err != nil {
		return nil, err
	}
	defer release()
	res, err := s.rw.ExecContext(ctx, query, args...)
	if err == nil {
		s.ck.wrote(p)
	}
	return res, err
}

// bulkUpdate is UPDATE table SET set WHERE where, for updateAll. A row that has
// been updated must no longer satisfy where.
type bulkUpdate struct{ table, set, where string }

// updateAll runs the updates until none finds a row, changing at most writeChunk
// rows of each per transaction at background priority, and returns how many rows
// it changed. After an error it returns what the earlier transactions changed.
func (s *Store) updateAll(ctx context.Context, updates []bulkUpdate) (int, error) {
	total := 0
	for {
		var changed int
		more := false
		err := s.write(ctx, prioLow, func(tx *sql.Tx) error {
			for _, u := range updates {
				res, err := tx.ExecContext(ctx, `UPDATE `+u.table+` SET `+u.set+` WHERE rowid IN (
					SELECT rowid FROM `+u.table+` WHERE `+u.where+` LIMIT ?)`, writeChunk)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				changed += int(n)
				more = more || n == writeChunk
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += changed
		if !more {
			return total, nil
		}
	}
}

// placeholders is "?, ?, ?" for n parameters.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return "?" + strings.Repeat(", ?", n-1)
}

// idArgs are ids as query arguments.
func idArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
