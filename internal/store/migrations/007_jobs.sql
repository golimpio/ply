-- M9: the background queue (internal/jobs). A row per job: queued, running, or
-- finished (kept a week, for the Activity page's history). Times are Unix ms, 0 = none.
CREATE TABLE jobs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL,
    key         TEXT    NOT NULL DEFAULT '',
    title       TEXT    NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    user        INTEGER NOT NULL DEFAULT 0,
    payload     BLOB,
    attempts    INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    run_at      INTEGER NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);
-- What a start loads: the unfinished jobs, and the newest finished ones.
CREATE INDEX jobs_unfinished ON jobs (state) WHERE state IN ('queued', 'running');
CREATE INDEX jobs_finished ON jobs (finished_at) WHERE finished_at > 0;
