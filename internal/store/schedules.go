package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ScheduleRecord is one background task's schedule (docs/M12-SCHEDULES.md). A task with no row runs
// on its defaults: the scheduler writes them the first time it sees the task.
type ScheduleRecord struct {
	Task      string
	Enabled   bool
	AtMinute  int   // minutes after midnight, 0 to 1439
	Days      []int // ISO weekdays, 1 (Monday) to 7 (Sunday), in order; empty is every day
	LastRun   int64 // unix seconds; 0: never queued by its schedule
	UpdatedAt int64 // unix seconds
}

// ErrInvalidSchedule is wrapped by the errors PutSchedule returns for a schedule it will not store.
var ErrInvalidSchedule = errors.New("store: invalid schedule")

// Schedules returns every stored schedule, by task.
func (s *Store) Schedules(ctx context.Context) ([]ScheduleRecord, error) {
	var out []ScheduleRecord
	err := s.snapshot(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT task, enabled, at_minute, days, last_run, updated_at
			FROM schedules ORDER BY task`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ScheduleRecord
			var enabled int
			var days string
			if err := rows.Scan(&r.Task, &enabled, &r.AtMinute, &days, &r.LastRun, &r.UpdatedAt); err != nil {
				return err
			}
			r.Enabled = enabled != 0
			if r.Days, err = parseDays(days); err != nil {
				return fmt.Errorf("schedule %q: %w", r.Task, err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// PutSchedule stores rec, replacing the row of its task. A person's change waits for nothing: it is
// written at the high priority of the settings. No list shows schedules: it leaves Generation alone.
func (s *Store) PutSchedule(ctx context.Context, rec ScheduleRecord) error {
	days, err := cleanSchedule(rec)
	if err != nil {
		return err
	}
	return s.writeQuiet(ctx, prioHigh, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO schedules (task, enabled, at_minute, days, last_run, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (task) DO UPDATE SET enabled = excluded.enabled, at_minute = excluded.at_minute,
				days = excluded.days, last_run = excluded.last_run, updated_at = excluded.updated_at`,
			rec.Task, boolInt(rec.Enabled), rec.AtMinute, days, rec.LastRun, rec.UpdatedAt)
		return err
	})
}

// MarkScheduleRun records that the scheduler queued the task at at, leaving its settings alone. It is
// a background write. ErrNotFound for a task that has no schedule.
func (s *Store) MarkScheduleRun(ctx context.Context, task string, at int64) error {
	res, err := s.execQuiet(ctx, prioLow, `UPDATE schedules SET last_run = ? WHERE task = ?`, at, task)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// cleanSchedule checks rec and returns its days as they are stored.
func cleanSchedule(rec ScheduleRecord) (string, error) {
	if strings.TrimSpace(rec.Task) == "" {
		return "", fmt.Errorf("%w: no task", ErrInvalidSchedule)
	}
	if rec.AtMinute < 0 || rec.AtMinute > 24*60-1 {
		return "", fmt.Errorf("%w: %d is not a minute of the day", ErrInvalidSchedule, rec.AtMinute)
	}
	if rec.LastRun < 0 || rec.UpdatedAt < 0 {
		return "", fmt.Errorf("%w: %q has a negative time", ErrInvalidSchedule, rec.Task)
	}
	days := slices.Clone(rec.Days)
	slices.Sort(days)
	days = slices.Compact(days)
	for _, d := range days {
		if d < 1 || d > 7 {
			return "", fmt.Errorf("%w: %d is not an ISO weekday", ErrInvalidSchedule, d)
		}
	}
	if len(days) == 0 {
		return "", nil
	}
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = strconv.Itoa(d)
	}
	return strings.Join(parts, ","), nil
}

// parseDays reads what cleanSchedule wrote. Anything else is an error, never a schedule quietly
// changed to another set of days.
func parseDays(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	days := make([]int, 0, len(parts))
	for _, p := range parts {
		d, err := strconv.Atoi(p)
		if err != nil || d < 1 || d > 7 {
			return nil, fmt.Errorf("%w: %q is not a list of ISO weekdays", ErrInvalidSchedule, s)
		}
		days = append(days, d)
	}
	slices.Sort(days)
	return slices.Compact(days), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
