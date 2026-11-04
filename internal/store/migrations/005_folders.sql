-- M7: library folders managed in the web UI (docs/M7-FOLDERS.md). A folder holds
-- movies or shows, never both. Whether it came from the command line is per run
-- and is not stored.

CREATE TABLE folders (
    id         INTEGER PRIMARY KEY,
    path       TEXT    NOT NULL UNIQUE,   -- absolute, cleaned
    kind       TEXT    NOT NULL CHECK (kind IN ('movies', 'shows')),
    guessed    INTEGER NOT NULL DEFAULT 0, -- the kind was guessed, not chosen
    added_at   INTEGER NOT NULL
);

-- What a removed folder's files leave behind, so adding the folder back restores it.
-- Keyed by path, not by item: the items are deleted with the folder.
CREATE TABLE item_archive (
    path         TEXT    PRIMARY KEY,
    size         INTEGER NOT NULL,
    tmdb_id      INTEGER NOT NULL DEFAULT 0,  -- only for a manual (user-fixed) match
    tmdb_kind    TEXT    NOT NULL DEFAULT '', -- the title's kind: 'movie' or 'series'
    position_sec REAL    NOT NULL DEFAULT 0,
    duration_sec REAL    NOT NULL DEFAULT 0,
    watched      INTEGER NOT NULL DEFAULT 0,
    progress_at  INTEGER NOT NULL DEFAULT 0,  -- progress.updated_at (unix ns); 0 = no progress
    archived_at  INTEGER NOT NULL
);
