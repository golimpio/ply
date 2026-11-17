-- ply:foreign-keys-off
-- M13: trailers on disk (docs/M13-TRAILERS.md). An item can be a trailer: a file of the
-- library that belongs to a film or a show (owner_dir: the folder of the film or show it
-- belongs to) and is never listed as one. SQLite cannot change a CHECK, so the table is
-- rebuilt; streams, subtitles_external, keyframes and progress refer to it by id, which
-- the copy keeps.
CREATE TABLE items_new (
    id              INTEGER PRIMARY KEY,
    kind            TEXT    NOT NULL CHECK (kind IN ('movie', 'episode', 'trailer')),
    title           TEXT    NOT NULL DEFAULT '',
    year            INTEGER NOT NULL DEFAULT 0,
    show            TEXT    NOT NULL DEFAULT '',
    season          INTEGER NOT NULL DEFAULT 0,
    episode         INTEGER NOT NULL DEFAULT 0,
    path            TEXT    NOT NULL UNIQUE, -- raw bytes as the OS reports them; never normalised
    root            TEXT    NOT NULL,
    size            INTEGER NOT NULL,
    mtime           INTEGER NOT NULL,        -- unix nanoseconds
    container       TEXT    NOT NULL DEFAULT '',
    duration_sec    REAL    NOT NULL DEFAULT 0,
    bitrate         INTEGER NOT NULL DEFAULT 0,
    added_at        INTEGER NOT NULL,        -- unix seconds
    updated_at      INTEGER NOT NULL,
    title_id        INTEGER REFERENCES titles (id) ON DELETE SET NULL,
    match_state     TEXT    NOT NULL DEFAULT 'pending'
        CHECK (match_state IN ('pending', 'matched', 'unmatched', 'manual', 'disabled')),
    needs_review    INTEGER NOT NULL DEFAULT 0,
    match_score     REAL    NOT NULL DEFAULT 0,
    match_attempts  INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0, -- unix seconds; 0 = due now
    local_poster    TEXT    NOT NULL DEFAULT '', -- absolute paths of sidecar artwork
    local_backdrop  TEXT    NOT NULL DEFAULT '',
    local_thumb     TEXT    NOT NULL DEFAULT '',
    fingerprint     TEXT    NOT NULL DEFAULT '', -- move fallback, see the scanner
    owner_dir       TEXT    NOT NULL DEFAULT ''  -- a trailer's: the folder of its film or show
);
INSERT INTO items_new (id, kind, title, year, show, season, episode, path, root, size, mtime, container,
    duration_sec, bitrate, added_at, updated_at, title_id, match_state, needs_review, match_score,
    match_attempts, next_attempt_at, local_poster, local_backdrop, local_thumb, fingerprint)
SELECT id, kind, title, year, show, season, episode, path, root, size, mtime, container,
    duration_sec, bitrate, added_at, updated_at, title_id, match_state, needs_review, match_score,
    match_attempts, next_attempt_at, local_poster, local_backdrop, local_thumb, fingerprint
FROM items;
DROP TABLE items;
ALTER TABLE items_new RENAME TO items;
CREATE INDEX items_root ON items (root);
CREATE INDEX items_browse ON items (kind, show, season, episode);
CREATE INDEX items_title ON items (title_id);
CREATE INDEX items_match ON items (match_state, next_attempt_at);
CREATE INDEX items_fingerprint ON items (size, fingerprint);
