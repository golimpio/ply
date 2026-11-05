package store

import (
	"context"
	"time"

	"ply/internal/jobs"
)

// The background queue's table (jobs.Persist). Its writes are low priority: a
// job's state can wait for a progress save or a setting.

const jobCols = `id, kind, key, title, priority, state, user, payload, attempts, error,
	created_at, run_at, started_at, finished_at`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// InsertJob stores a new job and returns its id.
func (s *Store) InsertJob(ctx context.Context, j jobs.Record) (int64, error) {
	res, err := s.exec(ctx, prioLow, `INSERT INTO jobs (kind, key, title, priority, state, user, payload, attempts,
		error, created_at, run_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Kind, j.Key, j.Title, j.Priority, string(j.State), j.User, j.Payload, j.Attempts, j.Error,
		ms(j.CreatedAt), ms(j.RunAt), ms(j.StartedAt), ms(j.FinishedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateJob stores a job's new state.
func (s *Store) UpdateJob(ctx context.Context, j jobs.Record) error {
	_, err := s.exec(ctx, prioLow, `UPDATE jobs SET title = ?, priority = ?, state = ?, user = ?, payload = ?,
		attempts = ?, error = ?, run_at = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		j.Title, j.Priority, string(j.State), j.User, j.Payload, j.Attempts, j.Error,
		ms(j.RunAt), ms(j.StartedAt), ms(j.FinishedAt), j.ID)
	return err
}

// LoadJobs returns the unfinished jobs and the newest `history` finished ones.
func (s *Store) LoadJobs(ctx context.Context, history int) ([]jobs.Record, error) {
	var out []jobs.Record
	err := s.snapshot(ctx, func(q queryer) error {
		for _, read := range []struct {
			query string
			args  []any
		}{
			{`SELECT ` + jobCols + ` FROM jobs WHERE state IN ('queued', 'running') ORDER BY id`, nil},
			{`SELECT ` + jobCols + ` FROM jobs WHERE finished_at > 0 ORDER BY finished_at DESC, id DESC LIMIT ?`, []any{history}},
		} {
			rows, err := q.QueryContext(ctx, read.query, read.args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var (
					r                                 jobs.Record
					state                             string
					created, runAt, started, finished int64
				)
				if err := rows.Scan(&r.ID, &r.Kind, &r.Key, &r.Title, &r.Priority, &state, &r.User, &r.Payload,
					&r.Attempts, &r.Error, &created, &runAt, &started, &finished); err != nil {
					rows.Close()
					return err
				}
				r.State = jobs.State(state)
				r.CreatedAt, r.RunAt, r.StartedAt, r.FinishedAt = fromMS(created), fromMS(runAt), fromMS(started), fromMS(finished)
				out = append(out, r)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// PruneJobs deletes the jobs that finished before `before`, a chunk at a time.
func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int, error) {
	total := 0
	for {
		res, err := s.exec(ctx, prioLow, `DELETE FROM jobs WHERE id IN (
			SELECT id FROM jobs WHERE finished_at > 0 AND finished_at < ? LIMIT ?)`, ms(before), writeChunk)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
		if n < writeChunk {
			return total, nil
		}
	}
}

const queuePausedKey = "jobs.paused"

// QueuePaused reports whether the person paused the queue.
func (s *Store) QueuePaused(ctx context.Context) (bool, error) {
	v, ok, err := s.GetSetting(ctx, queuePausedKey)
	return ok && v == "1", err
}

// SetQueuePaused stores the queue's pause switch.
func (s *Store) SetQueuePaused(ctx context.Context, paused bool) error {
	if !paused {
		return s.DeleteSetting(ctx, queuePausedKey)
	}
	return s.SetSetting(ctx, queuePausedKey, "1")
}

var _ jobs.Persist = (*Store)(nil)
