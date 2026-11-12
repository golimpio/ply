-- The background tasks' schedules (docs/M12-SCHEDULES.md): when each runs, on which days, and when
-- the scheduler last queued it. A task with no row runs on its defaults; the scheduler writes the
-- defaults the first time it sees a task. Unix seconds.
CREATE TABLE schedules (
    task       TEXT    PRIMARY KEY,
    enabled    INTEGER NOT NULL DEFAULT 1,
    at_minute  INTEGER NOT NULL CHECK (at_minute BETWEEN 0 AND 1439),
    -- ISO weekdays 1 (Monday) to 7 (Sunday), comma-separated and in order; '' is every day.
    days       TEXT    NOT NULL DEFAULT '',
    last_run   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
